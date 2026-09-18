# Spinneret Go SDK

[English](README.md)

[Spinneret](https://github.com/TikHub/Spinneret) 的 Go 客户端。Spinneret 是爬虫节点的控制面：
向节点租出身份（Cookie、设备参数、账号）与代理，根据请求上报执行冷却、封禁与熔断，并下发配置与密钥。

- 基于生成的 Connect 客户端的 `Client`，默认使用 Connect JSON，也可切换为 gRPC
- 每次调用自动携带鉴权（`Authorization: Bearer`）与节点（`X-Spinneret-Node`）请求头
- 携带服务端原因与建议等待时间的类型化错误（`IsNoIdentity`、`IsCircuitOpen` 等）
- `Lease` 租约助手：把凭证应用到 `*http.Request`，为 `http.Transport` 提供代理，排队上报，并用最后一次上报释放租约
- 后台 `Reporter`：攒满 100 条或每 200 ms 发送一批，有界队列，失败按退避重试
- `ConfigWatcher`：长轮询、变更回调、版本跟踪，本地快照永不落盘密钥
- `ClassifyError`：把 `net/http` 请求错误映射为上报用的错误类型

SDK 位于主模块中：`github.com/TikHub/Spinneret/sdk/go/spinneret`（Go 1.27+），运行时只依赖
`connectrpc.com/connect` 与 `google.golang.org/protobuf`。

## 安装

```bash
go get github.com/TikHub/Spinneret@latest
```

```go
import "github.com/TikHub/Spinneret/sdk/go/spinneret"
```

## 配置

`spinneret.New(spinneret.Options{...})` 只校验参数，不会连接服务端。`BaseURL`、`Token`、`Node` 为空时读取环境变量：

| 选项 | 环境变量 | 含义 | 默认值 |
| --- | --- | --- | --- |
| `BaseURL` | `SPINNERET_URL` | 服务地址，如 `https://spinneret.internal`（允许路径前缀） | 必填 |
| `Token` | `SPINNERET_TOKEN` | 节点令牌（`spn_...`） | 必填 |
| `Node` | `SPINNERET_NODE` | 作为 `X-Spinneret-Node` 发送的节点名（清洗为 `[A-Za-z0-9._:@-]`，最长 128 字符） | 主机名 |
| `UseGRPC` | | 使用 gRPC（二进制 Protobuf，需要 HTTP/2）代替 Connect JSON | `false` |
| `Timeout` | | 一元调用每次尝试的超时 | `10s` |
| `HTTPClient` | | 自定义 `connect.HTTPClient`（TLS 根证书、出口代理等） | 调优后的 `http.Client` |
| `Retry` | | `*RetryPolicy`；`spinneret.NoRetry()` 关闭重试 | 重试 2 次 |
| `Reporter` | | `client.Reporter()` 的 `ReporterOptions` | 见下文 |
| `Logger` | | `*slog.Logger` | `slog.Default()` |
| `UserAgent` | | 追加在 `spinneret-go/<版本>` 之前 | |

```go
client, err := spinneret.New(spinneret.Options{
	BaseURL: "https://spinneret.internal",
	Token:   os.Getenv("SPINNERET_TOKEN"),
	Node:    "crawler-hk-03",
})
if err != nil {
	return err
}
defer client.Close(context.Background()) // 投递队列中剩余的上报
```

默认 HTTP 客户端拨号超时 3 秒，遵循 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`，通过 TLS 协商 HTTP/2；
开启 `UseGRPC` 时对 `http://` 地址使用明文 HTTP/2（h2c）。自定义客户端的 `http.Client.Timeout`
不能小于 `WatchConfig` 的等待时间（默认 35 秒）：SDK 通过请求 context 为每次调用设置截止时间。

## 快速开始

```go
ctx := context.Background()
target := "https://target.example.com/api/v1/search?keyword=go"

lease, err := client.Lease(ctx, &spinneret.AcquireRequest{Site: "shop", Client: "web", Uri: target})
switch {
case spinneret.IsNoIdentity(err), spinneret.IsNoProxy(err):
	time.Sleep(spinneret.RetryAfterOf(err)) // 等待资源
	return nil
case spinneret.IsCircuitOpen(err), spinneret.IsSitePaused(err):
	return errPaused // 暂停该端点组或站点
case err != nil:
	return err
}
defer lease.Close(ctx) // 用最后一次上报释放，没有上报时调用 LeaseService/Release

transport, err := lease.Transport(nil) // 克隆 http.DefaultTransport 并设置租约代理
if err != nil {
	return err
}
defer transport.CloseIdleConnections()
httpClient := &http.Client{Transport: transport, Timeout: 15 * time.Second}

req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
lease.Apply(req) // 凭证中的 Cookie、请求头与查询参数
started := time.Now()
resp, err := httpClient.Do(req)
if err != nil {
	return lease.ReportError(err, spinneret.ReportInput{StartedAt: started})
}
defer resp.Body.Close()
body, _ := io.ReadAll(resp.Body)
return lease.ReportResponse(resp, spinneret.ReportInput{
	StartedAt:     started,
	ResponseBytes: int64(len(body)),
	Markers:       detectMarkers(body), // 如 "captcha_page"、"empty_list"
})
```

完整节点示例见 [`examples/basic/main.go`](examples/basic/main.go)：

```bash
export SPINNERET_URL=http://localhost:8080 SPINNERET_TOKEN=spn_xxx
go run ./sdk/go/examples/basic -site shop -client web -target https://... -config crawler/search.json
```

## 协议

| | Connect JSON（默认） | gRPC（`UseGRPC: true`） |
| --- | --- | --- |
| 传输 | HTTP/1.1 或 HTTP/2 | 仅 HTTP/2（TLS 下 ALPN 协商，`http://` 使用 h2c） |
| 编码 | 蛇形字段名的 JSON，忽略响应中的未知字段 | 二进制 Protobuf |
| 错误原因与建议等待时间 | 响应头 | 响应尾部（trailer） |

两种协议返回相同的 `Error`。使用 gRPC 时负载均衡器必须端到端支持 HTTP/2，而 Docker Compose 中的负载均衡器
做不到：`deploy/compose/config/Caddyfile` 里 `reverse_proxy` 的 transport 没有写 `versions h2c 2`，
Caddy 会用 HTTP/1.1 转发给明文上游，因此栈的 8080 端口只能走 Connect JSON。要使用 gRPC，请在该 transport
块中加上 `versions h2c 2`，或直连某个实例（`spinneret` 服务本身没有映射宿主机端口）。

## 租约

`client.Lease(ctx, *AcquireRequest)` 调用 `LeaseService/Acquire` 并返回 `*Lease`；
`client.LeaseBatch(ctx, *AcquireBatchRequest)` 包装 `AcquireBatch` 返回的每个租约；
`client.NewLease(resp, uri)` 包装从其他途径获得的响应。租约可并发使用。

| 成员 | 说明 |
| --- | --- |
| `ID()`、`IdentityID()`、`Info()`、`Hints()`、`Response()`、`URI()` | 租约元数据（`Info().GetProbe()`、`GetSticky()` 等） |
| `ExpiresAt()` | 过期时间，`Renew` 后更新 |
| `Credential()` | `cookies`、`cookie_header`、`headers`、`query`、`json`、`values` |
| `Proxy()`、`ProxyURL()` | 分配的代理（无代理时为 `nil`）；URL 含凭据，切勿写入日志 |
| `Transport(base)` | 克隆 `base`（或 `http.DefaultTransport`）并通过代理发送 |
| `Apply(req)` | 把凭证合并进 `*http.Request` |
| `Report(ReportInput)` | 排队一条上报 |
| `ReportResponse(resp, ReportInput)` | 上报 `*http.Response`（状态码、方法、路径、`Content-Length`；407 自动设为 `proxy_auth`） |
| `ReportError(err, ReportInput)` | 上报失败的请求，`error_kind` 由 `ClassifyError` 得出（方法与路径取自 `*url.Error`） |
| `Renew(ctx, extend)` | 续期（`0` 表示使用策略 TTL） |
| `Close(ctx)` | 释放租约（幂等） |

释放语义：

- 上报进入 `client.Reporter()` 队列。最近一条上报会暂存到下一条上报或 `Close`，以便携带 `release: true`。
- `Close` 把暂存的上报以 `release: true` 发出；没有任何上报时调用 `LeaseService/Release`。SDK 不会凭空生成上报，
  失败的请求需要自行调用 `ReportError`，服务端才会计入。
- `Close` 返回 `Release` 调用的错误，但表示租约已结束的原因（`lease_released`、`lease_unknown`、`lease_expired`、
  `lease_lifetime_exceeded`）除外。上报器已关闭时，最后一条上报会直接同步投递。
- `ReportInput{Release: true}` 立即释放；之后的上报返回 `IsLeaseGone(err)` 为真的错误。
- 一个租约可服务多个请求（翻页）：每个请求都上报，由 `Close` 释放。
- `client.Reporter().Flush(ctx)` 等待队列中的上报投递完成。

### 凭证

`Apply(req)`（以及 `spinneret.ApplyCredential(req, cred)`）不会覆盖请求已有的内容：

- 请求没有的请求头才会设置（忽略 `Host`：net/http 从 URL 取主机）；
- Cookie 合并为单个 `Cookie` 请求头，跳过请求中已存在的同名 Cookie；凭证没有 Cookie 字典且请求没有 Cookie 时，
  `cookie_header` 原样发送（不重新编码）；
- 缺少的查询参数追加到 URL，已有查询串逐字节保留，签名参数不会失效。

`credential.GetValues().AsMap()` 与 `credential.GetJson().AsInterface()` 可读取类型化取值（设备参数、请求体片段）。
`http.Transport` 支持 `http://`、`https://` 与 `socks5://` 代理地址。

### 上报

`ReportInput` 字段：`HTTPStatus`、`Method`、`URI`、`BusinessCode`、`ErrorKind`、`Markers`、`OutcomeHint`、
`Latency`、`ResponseBytes`、`StartedAt`、`FinishedAt`、`ReportID`、`Release`。

- `ReportID` 默认随机 UUID，`FinishedAt` 默认当前时间，`StartedAt` 默认 `FinishedAt - Latency`，
  `Latency` 默认 `FinishedAt - StartedAt`。
- `URI` 默认取领取时的 URI，并裁剪为请求路径（去掉查询串与片段，最长 2048 字符）：端点组只按路径匹配，
  查询参数常含签名凭证。只按 `endpoint_group` 领取的租约需要显式传 `URI`。
- 输入按服务端规则校验（`error_kind` 取值、最多 32 个 1..64 字符的标记、状态码 0..999 等）；
  非法输入返回 `invalid_argument` 错误，不会入队。
- `ReportResponse` 无法得知分块传输的响应体大小：读完响应体后通过 `ResponseBytes` 传入。

## 后台上报

`client.Reporter()`（或 `spinneret.NewReporter(send, options, logger)`）批量上报：

- 队列中攒满 `BatchSize`（100）条或最早一条等待超过 `FlushInterval`（200 ms）时发送，每次最多 `MaxBatchSize`（500）条；
- 队列上限 `MaxQueueSize`（10 000）：超出时丢弃最早的上报，计入 `Stats().Dropped` 并（限频）记录日志；
- `IsRetryable(err)` 为真的失败（传输错误、`unavailable`、`internal`、`unknown`、`deadline_exceeded`、`aborted`、
  `resource_exhausted`）按带抖动的指数退避重试（`InitialBackoff` 500 ms .. `MaxBackoff` 30 s，且不短于服务端建议），
  其他失败丢弃该批；
- 服务端拒绝的上报会计数、记录日志并回调 `OnRejected`；
- `Submit` 不会阻塞在网络上，并在为空时补齐 `report_id`、`finished_at` 与 `started_at`；
- `Flush(ctx)` 立即发送并等待队列清空；`Close(ctx)`（由 `client.Close` 调用）在 `ctx` 结束前尽量投递
  （`ctx` 无截止时间时使用 `CloseTimeout` 5 秒），到期后中止进行中的调用，有上报被丢弃时返回错误。

```go
client, err := spinneret.New(spinneret.Options{
	Reporter: spinneret.ReporterOptions{
		FlushInterval: 200 * time.Millisecond,
		BatchSize:     100,
		MaxQueueSize:  10_000,
		OnRejected: func(r *spinneret.RejectedReport) {
			log.Printf("rejected %s: %s", r.GetReportId(), r.GetReason())
		},
	},
})
stats := client.Reporter().Stats() // Submitted、Sent、Accepted、Duplicated、Rejected、Dropped、FailedSends、Queued
```

## 配置中心

```go
watcher, err := client.NewConfigWatcher(spinneret.WatcherOptions{
	Items:       []spinneret.ConfigKey{{Group: "crawler", Key: "search.json"}, {Group: "_runtime", Key: "breakers"}},
	SnapshotDir: "/var/cache/spinneret",
	OnChange: func(item *spinneret.ConfigItem) { reload(item.GetContent()) },
})
if err != nil {
	return err
}
if err := watcher.Start(ctx); err != nil { // ctx 结束、调用 Stop 或 client.Close 时循环停止
	return err
}
item, ok := watcher.Get("crawler", "search.json")
versions := watcher.Versions()
changed, err := watcher.WaitForChange(ctx, "crawler", "search.json")
```

- `Start` 通过 `BatchGetConfig` 加载配置项，随后启动 goroutine 携带已知版本长轮询 `WatchConfig`
  （`Timeout` 30 秒，最长 60 秒），保存最新配置项，并对每次变更（包括初始值）调用监听器。监听器顺序执行；
  监听器 panic 会被记录，不会中断监听。`Stop` 会取消进行中的长轮询。
- 轮询失败按退避重试（`InitialBackoff` 1 秒 .. `MaxBackoff` 30 秒）。
- 启动时服务端不可用（`IsRetryable` 类错误）会改为加载本地快照，在服务端应答前 `FromSnapshot()` 为 `true`。
  鉴权与参数错误由 `Start` 返回，修复后可再次调用 `Start`。
- 设置 `SnapshotDir` 后，每个拉取到的配置项以原子方式写入（临时文件、`fsync`、重命名；文件 `0600`、目录 `0700`）
  `<dir>/<host>/<namespace>/<group>/<key>.json`，各路径段经百分号编码，布局与格式与 Python SDK 一致。
- 密钥内容永不落盘：带 `has_secret_refs`（服务端已把 `${secret:...}` 引用解析进内容）的配置、内容中仍含 `${secret:`
  的配置以及被 `TreatAsSecret` 命中的配置只保存在内存中，其旧快照会被删除。`TreatAsSecret` 只能追加密钥配置，
  panic 的判定函数视为真。Python SDK 写入的加密快照会被忽略。

一次性读取：`GetConfig`、`BatchGetConfig`、`WatchConfig`（`timeout_ms` 为 0 时等待 30 秒）与 `GetSecret`（`version` 为 0 读取当前版本）。

## 直接调用

所有节点接口都可以用生成的请求与响应类型直接调用，这些类型以别名形式导出（`spinneret.AcquireRequest`、
`spinneret.Report` 等）：`Acquire`、`AcquireBatch`、`Renew`、`Release`、`Report`（1..500 条，同步）、`GetConfig`、
`BatchGetConfig`、`WatchConfig`、`GetSecret`。`client.LeaseService()` 等方法返回底层生成的客户端（已带鉴权，
不做重试与错误转换）。

## 错误

所有调用返回 `*spinneret.Error`（使用 `spinneret.AsError(err)` 或 `errors.As`）：

| 字段 / 方法 | 含义 |
| --- | --- |
| `Code` | `connect.Code`，如 `connect.CodeResourceExhausted` |
| `Reason` | `Spinneret-Reason`，如 `no_identity_available`；未收到响应时为 `transport` |
| `Message` | 服务端消息 |
| `RetryAfter` | `Spinneret-Retry-After-Ms` 转换的 `time.Duration`（缺省为 0） |
| `Procedure` | 失败的 RPC |
| `ErrorKind`、`Transport()` | 传输失败的分类 |
| `FromServer()` | 错误由服务端返回（而非由传输失败或裸 HTTP 状态码推断） |
| `Unwrap()` | 底层的 `*connect.Error` / `*url.Error` |

| 辅助函数 | 错误码 / 原因 | 节点应对 |
| --- | --- | --- |
| `IsUnauthenticated` | `unauthenticated`（`token_invalid` 等） | 停止并告警 |
| `IsPermissionDenied` | `permission_denied`（`scope_missing`） | 停止并告警 |
| `CodeOf(err) == connect.CodeInvalidArgument` | `site_unknown`、`uri_invalid` 等 | 修正调用代码 |
| `IsNoIdentity`、`IsNoProxy` | `resource_exhausted` | 等待 `RetryAfterOf(err)` 后重试 |
| `IsCircuitOpen`、`IsSitePaused` | `unavailable` | 暂停该端点组 / 站点 |
| `IsLeaseGone` | `lease_unknown`、`lease_released`、`lease_expired`、`lease_lifetime_exceeded` | 重新领取 |
| `IsTransport` | 原因 `transport` | 稍后重试 |
| `ReasonOf(err) == spinneret.ReasonClientClosed` | `failed_precondition` | 创建新的客户端 |

没有 Connect 错误体的响应（例如负载均衡器的错误页）保留由 HTTP 状态码推断的错误码，原因为空。

## 重试与超时

- 每次尝试受 `Timeout`（10 秒）限制；`Acquire`/`AcquireBatch` 额外加上 `wait_ms`；`WatchConfig` 使用
  `timeout_ms`（为 0 时 30 秒）加 5 秒。`ctx` 上更短的截止时间优先。发送给服务端的截止时间
  （`Connect-Timeout-Ms` / `grpc-timeout`）比客户端超时晚 1 秒，因此超时的尝试始终被判定为传输超时，
  不会与同一截止时间触发的 `deadline_exceeded` 应答竞争。
- `RetryPolicy{MaxRetries: 2, InitialBackoff: 100ms, MaxBackoff: 2s, MaxRetryAfter: 5s}`：传输失败与 `unavailable`
  应答按带抖动的指数退避重试；`circuit_open` 与 `site_paused` 从不重试；服务端建议等待超过 `MaxRetryAfter`
  时直接返回错误；证书错误从不重试。
- `Acquire` 与 `AcquireBatch` 不是幂等的：只有确定请求尚未发出（拨号与 DNS 错误）或服务端明确应答 `unavailable`
  时才重试。含糊的失败（单次调用超时、连接被重置、裸 502/503/504）直接返回。
- `overloaded`（`unavailable`）表示服务端已到自己的租借并发上限，在真正尝试之前就把这次调用甩掉了。
  它没有发出任何 Redis 命令，因此与其他 `unavailable` 一样可以重试：重试会遵守
  `Spinneret-Retry-After-Ms`，服务端会把它抖动到 100–200 毫秒。它不是 `no_identity_available`——
  身份池根本没有被查询——并且不需要 SDK 做任何改动。
- 后台上报器与配置监听器不使用该策略，而是按各自的退避重试。

## 错误类型

`spinneret.ClassifyError(err)` 把请求错误映射为上报的 `error_kind`：`timeout`、`conn_reset`、`conn_refused`、
`proxy_auth`、`tls`、`dns` 或 `other`（`nil` 返回 `""`）。它能识别 `*url.Error` 及其包装的 `net`、`syscall`、
`crypto/tls`、`crypto/x509` 错误、HTTP 代理 `CONNECT` 拒绝（`Proxy Authentication Required`）与 SOCKS5 认证失败，
其他情况依据错误消息判断。普通 HTTP 代理返回的 `407` 是响应而非错误：用 `ReportResponse` 上报即可，会自动设为 `proxy_auth`。

## 日志与安全

SDK 通过 `Options.Logger`（默认 `slog.Default()`）输出日志：重试为 debug 级别，丢弃或被拒绝的上报与投递失败为
warning 或 error 级别。SDK 从不记录令牌、凭证、代理地址、配置内容或密钥值；令牌只出现在 `Authorization` 请求头中。

## 开发

```bash
go vet ./sdk/go/...
go test -race -count=1 -cover ./sdk/go/...
golangci-lint run ./sdk/go/...
```

单元测试用 `httptest` 服务器承载生成的处理器与假实现，覆盖 Connect JSON 与 gRPC（h2c），不访问外部网络。
在线测试针对真实部署（例如带 mocktarget 站点的 Docker Compose 环境）运行：

```bash
export SPINNERET_LIVE_ADMIN_PASSWORD=...   # 环境的管理员密码
go test -tags live -race -count=1 -run TestLive ./sdk/go/spinneret
```

在线测试会创建名为 `gosdk-<run>` 的命名空间（含站点、身份、令牌、配置项与密钥），覆盖两种协议，结束后全部删除
（可用 `SPINNERET_LIVE_URL`、`SPINNERET_LIVE_TARGET`、`SPINNERET_LIVE_GRPC_URL`、`SPINNERET_LIVE_TENANT`、
`SPINNERET_LIVE_ADMIN_USER` 覆盖默认值）。
