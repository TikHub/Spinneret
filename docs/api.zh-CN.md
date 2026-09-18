# API 参考

[English](api.md) · [部署指南](deployment.zh-CN.md) · [运维手册](operations.zh-CN.md)

API 定义在 [`proto/spinneret/v1`](../proto/spinneret/v1)，通过 [Connect](https://connectrpc.com) 提供服务：
每个 RPC 都是 `POST /spinneret.v1.<Service>/<Method>`，可以用纯 HTTP + JSON、Connect 协议、gRPC 或 gRPC-Web 调用。
线格式约定（JSON 映射、分页、请求头）见 [`proto/README.md`](../proto/README.md)。

本文覆盖爬虫节点调用的四个**节点服务**，并概览控制台使用的管理服务。

- [如何调用](#如何调用)
- [LeaseService](#leaseservice)
- [ReportService](#reportservice)
- [ConfigService](#configservice)
- [SecretService](#secretservice)
- [错误](#错误)
- [重试建议](#重试建议)
- [管理 API](#管理-api)

---

## 如何调用

```http
POST /spinneret.v1.LeaseService/Acquire HTTP/1.1
Host: spinneret.internal:8080
Authorization: Bearer spn_EXAMPLEtokenEXAMPLEtokenEXAMPLEtoken1234567
X-Spinneret-Node: crawler-hk-03
Content-Type: application/json

{"site":"example","client":"web","uri":"/site/search?q=shoes","wait_ms":2000}
```

| 请求头 | 必需 | 含义 |
| --- | --- | --- |
| `Authorization: Bearer spn_…` | 是 | 节点 API 令牌，它决定了命名空间——节点请求从不需要指定命名空间 |
| `Content-Type: application/json` | 是 | 也接受 `application/proto`、`application/connect+json`、`application/grpc` |
| `X-Spinneret-Node` | 建议 | 节点实例名（≤ 128 字符），用于按节点统计；不传时显示为 `_` |

节点客户端需要知道的约定：

- 字段名是 **snake_case**，与 `.proto` 文件完全一致。
- 响应中零值总是存在（`""`、`0`、`false`、`[]`、`{}`）；未设置的消息为 `null`。
- **未知请求字段会被忽略**，新客户端可以调用旧服务端，反之亦然。
- 时间戳是 RFC 3339 UTC 字符串：`"2026-09-17T20:11:54.243Z"`。
- 节点 API 的时长是 `*_ms` 字段中的整数**毫秒**。
- `int64` 字段在响应中是 JSON **字符串**（proto3 JSON 映射）；请求中数字和字符串都接受。
  节点 API 里唯一的 `int64` 是 `Report.response_bytes`。

用 `spnr` CLI 创建令牌（见[运维手册](operations.zh-CN.md#api-令牌与作用域)）：

```bash
spnr token create --tenant default --namespace default --name crawler-hk \
  --scope lease:acquire:example --scope report:write:example --scope config:read:crawler \
  --expires 720h
```

---

## LeaseService

需要 `lease:acquire[:<site>]` 作用域。

### Acquire

为即将发起的请求领取一个身份——如果轮换策略要求，还会分配一个代理。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `site` | string，1–64 | 令牌所属命名空间内的站点名 |
| `client` | string，1–32 | 客户端类型，如 `web`、`app` |
| `uri` | string，≤ 2048 | 路径、路径+查询串或完整 URL，用于匹配端点组。只匹配路径 |
| `endpoint_group` | string，≤ 64 | 显式指定端点组，优先级高于 `uri` |
| `session_key` | string，≤ 256 | 粘性会话键；轮换策略开启 `sticky` 时复用同一身份 |
| `wait_ms` | int32，0–5000 | 服务端最多等待多久直到有身份可用，`0` 表示立即失败 |

```bash
curl -s http://localhost:8080/spinneret.v1.LeaseService/Acquire \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: crawler-hk-03" \
  -H 'Content-Type: application/json' \
  -d '{"site":"example","client":"web","uri":"/site/search?q=shoes","wait_ms":2000}'
```

```json
{
  "lease": {
    "lease_id": "lse_01a0b0ff449e78a49c2d8cc240515316_i_05",
    "identity_id": "idt_01a0b0a4dc4774759bba124ee0e6be8f",
    "identity_type": "example_web_cookie",
    "endpoint_group": "search",
    "expires_at": "2026-09-17T20:12:54.398Z",
    "sticky": false,
    "probe": false
  },
  "credential": {
    "cookies": { "csrftoken": "csrf-17", "sessionid": "example-17" },
    "cookie_header": "",
    "headers": { "User-Agent": "ExampleCrawler/17" },
    "query": {},
    "json": null,
    "values": {}
  },
  "proxy": {
    "proxy_id": "pxy_01a0b0a4dc4b7c5984d858b85e92f447",
    "url": "http://expx1:secret@mocktarget:9091",
    "kind": "datacenter",
    "region": ""
  },
  "hints": { "renew_before_ms": 15000 }
}
```

**无论身份类型是什么，`credential` 始终包含同样的六个段**，把非空的部分合并进请求即可：

| 段 | 类型 | 用法 |
| --- | --- | --- |
| `cookies` | 对象 | 请求的 Cookie 集合 |
| `cookie_header` | 字符串 | 现成的 `Cookie` 头值（`k1=v1; k2=v2`）；`cookies` 为空时使用它 |
| `headers` | 对象 | 合并到请求头 |
| `query` | 对象 | 合并到查询参数——不要丢弃已有参数 |
| `json` | 任意 | 请求体片段，或身份类型定义的任意结构；未使用时为 `null` |
| `values` | 对象 | 自定义取值，例如签名逻辑需要的 token |

策略不分配代理时 `proxy` 为 `null`。`hints.renew_before_ms` 是租约 TTL 的四分之一：剩余时间低于它时应续租。
`lease.probe: true` 表示这条租约会决定一次状态转换（半开熔断的探针，或正在验证的 `pending` 身份）——
请如实上报，不要丢弃。

租约是把你的请求和服务端记账关联起来的凭据：**无论成功与否都要上报**。未上报的租约会过期，并被计为 `abandoned`。

### AcquireBatch

字段与 Acquire 相同，另加 `count`（1–50）；`session_key` 只在 `count` 为 1 时生效。返回实际发放的租约
（可能少于请求数）和请求数量。只有一个都发不出来时才返回错误。

```json
{ "leases": [ { "lease": {…}, "credential": {…}, "proxy": {…}, "hints": {…} } ], "requested": 10 }
```

### Renew

续租。`extend_ms` 从当前时刻开始计算（`0` 表示使用策略的租约 TTL，最大 1 800 000）。新的到期时间不会超过策略的
`max_lease_lifetime`，触顶时返回 `lease_lifetime_exceeded`。

```bash
-d '{"lease_id":"lse_…","extend_ms":0}'
```

```json
{ "expires_at": "2026-09-17T20:14:54.398Z" }
```

### Release

提前结束租约。通常没有必要——在最后一条上报里设置 `release: true` 即可一次完成上报和释放。
该调用是幂等的：租约已经结束时 `released` 为 `false`。

```bash
-d '{"lease_id":"lse_…"}'
```

```json
{ "released": true }
```

---

## ReportService

需要 `report:write[:<site>]` 作用域。

### Report

一次接收 1–500 条上报。**只上报事实，不要下判断**：结果由服务端的识别策略决定，这样检测规则变化时无需重新发布节点。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `report_id` | string，1–64 个 `[A-Za-z0-9_.:-]` | 幂等键，推荐 UUID。在 `SPINNERET_REPORT_DEDUP_TTL`（1 小时）内重复会计为 `duplicated` |
| `lease_id` | string | 本次请求使用的租约 |
| `uri` | string，1–2048 | 请求的路径。请去掉查询串：端点组只按路径匹配，且查询串常含签名值 |
| `method` | string，≤ 16 | `GET`、`POST` 等 |
| `http_status` | int32，0–999 | `0` 表示没有收到响应 |
| `business_code` | string，≤ 64 | 响应体中的业务状态码（如果目标站有） |
| `error_kind` | 枚举字符串 | `""`、`timeout`、`conn_reset`、`conn_refused`、`proxy_auth`、`tls`、`dns`、`other` |
| `markers` | 最多 32 个，每个 1–64 字符 | 节点识别出的响应特征：`captcha_page`、`login_redirect`、`empty_list` 等 |
| `outcome_hint` | string，≤ 32 | 节点提议的结果；仅当识别策略设置了 `trust_outcome_hint` 时使用 |
| `latency_ms` | int32 ≥ 0 | 请求耗时 |
| `response_bytes` | int64 ≥ 0 | 响应体大小（JSON 字符串或数字均可） |
| `started_at` | 时间戳 | **必填** |
| `finished_at` | 时间戳 | **必填**，不得早于 `started_at` |
| `release` | bool | 本条上报处理完后释放租约 |

```bash
curl -s http://localhost:8080/spinneret.v1.ReportService/Report \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"reports":[{
        "report_id":"9f1c4c40-2f6a-4d6e-9c63-8f1b1d8f0a11",
        "lease_id":"lse_01a0b0ff449e78a49c2d8cc240515316_i_05",
        "uri":"/site/search","method":"GET","http_status":200,
        "business_code":"","error_kind":"","markers":[],
        "latency_ms":143,"response_bytes":48213,
        "started_at":"2026-09-17T20:11:54.100Z",
        "finished_at":"2026-09-17T20:11:54.243Z",
        "release":true}]}'
```

```json
{ "accepted": 1, "duplicated": 0, "rejected": [] }
```

每条上报**独立校验**，一条有问题不会导致整批失败：

```json
{
  "accepted": 0,
  "duplicated": 0,
  "rejected": [
    { "report_id": "x1", "reason": "lease_unknown", "message": "lease is unknown or expired" }
  ]
}
```

拒绝原因：`invalid_argument`、`lease_unknown`（已过期、已释放或从未存在）、`scope_missing`。
调用本身返回 `200`，请检查 `rejected` 而不是 HTTP 状态码。

接收是异步的：被接受的上报会入队到 Redis Stream 分片，由 worker 在毫秒级内消费。晚于
`SPINNERET_LATE_REPORT_WINDOW`（10 分钟）到达的上报仍会记录，但不再改变身份状态。

批量建议：两个 SDK 都会把上报排队，每 200 毫秒或每 100 条刷新一次。自建客户端也请照此批量提交——
每个请求一次 RPC 虽然可行，但浪费往返。

---

## ConfigService

需要 `config:read[:<group glob>]` 作用域。只下发当前已发布的版本，并解析其中的 `${secret:…}` 引用——
后者额外需要匹配的 `secret:read` 作用域。

### GetConfig

```bash
-d '{"group":"crawler","key":"example.json"}'
```

```json
{
  "item": {
    "namespace": "default",
    "group": "crawler",
    "key": "example.json",
    "format": "json",
    "version": 3,
    "content": "{\"search_page_size\": 10, \"item_fields\": [\"id\", \"title\"]}",
    "updated_at": "2026-09-17T18:33:09.468485Z",
    "has_secret_refs": false
  }
}
```

配置项不存在或没有已发布版本时返回 `not_found`。`has_secret_refs: true` 表示内容中包含已解析的密钥——
**不要把它写入明文缓存**。

### BatchGetConfig

一次最多 200 项；不存在的项会出现在 `missing` 中，而不会让整个调用失败。

```bash
-d '{"items":[{"group":"crawler","key":"example.json"},{"group":"crawler","key":"missing.json"}]}'
```

```json
{
  "items": [ { "namespace": "default", "group": "crawler", "key": "example.json", "version": 3, … } ],
  "missing": [ { "group": "crawler", "key": "missing.json" } ]
}
```

### WatchConfig

长轮询。提交你当前持有的版本号；有版本不同的项时立即返回，否则最多等待 `timeout_ms`（0 表示 30 000，最大 60 000）
后返回空列表。

```bash
-d '{"items":[{"group":"crawler","key":"example.json","version":3}],"timeout_ms":30000}'
```

```json
{ "items": [] }
```

```json
{ "items": [ { "group": "crawler", "key": "example.json", "version": 4, "content": "…", … } ] }
```

版本填 `0` 表示"我还没有"，配置项一旦有已发布版本就会立即返回。每次响应后立刻重新发起轮询，并更新持有的版本号；
变更在一秒内即可感知。HTTP 客户端的超时要大于等待时间（建议至少 35 秒）。单实例并发监听数受
`SPINNERET_MAX_WATCHERS`（20 000）限制，超出时返回 `resource_exhausted`。

保留的只读分组 `_runtime` 暴露 `breakers` 和 `site_switches`，节点可以监听它，在下一次 `Acquire` 失败之前先行退避。

---

## SecretService

需要 `secret:read:<"命名空间/路径" 的 glob>` 作用域。**每次读取都会写入审计日志。**

### GetSecret

```bash
-d '{"path":"signing/api_key","version":0}'
```

```json
{
  "path": "signing/api_key",
  "version": 2,
  "value": "sk-live-…",
  "expires_at": null
}
```

`version: 0` 读取当前版本。路径相对于命名空间，需匹配 `^[a-z0-9][a-z0-9_./-]*$`。
请在进程内缓存该值供整个生命周期使用，不要每次请求都读取，也不要写入磁盘。

---

## 错误

错误使用 Connect 的错误体，外加两个响应头，因此纯 JSON 客户端无需解析错误详情：

```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 59367

{"code":"resource_exhausted","message":"no identity available for example/web/search"}
```

| 响应头 | 含义 |
| --- | --- |
| `Spinneret-Reason` | 机器可读的原因——请按它分支，而不是按 message |
| `Spinneret-Retry-After-Ms` | 服务端能估算时给出的建议等待毫秒数 |

使用 gRPC 协议时，同样的值以 trailer 返回。两个 SDK 都把它们暴露为类型化错误
（Go：`spinneret.IsCircuitOpen(err)`、`RetryAfterOf(err)`；Python：`NoIdentityAvailable`、`err.retry_after_ms`）。

### 原因对照表

| 原因 | Connect code | HTTP | 何时出现 | 怎么办 |
| --- | --- | --- | --- | --- |
| `token_invalid` | `unauthenticated` | 401 | 令牌不存在或格式错误 | 换正确的令牌，不要重试 |
| `token_expired` | `unauthenticated` | 401 | 超过 `expires_at` | 签发新令牌 |
| `token_revoked` | `unauthenticated` | 401 | 在控制台被吊销 | 签发新令牌 |
| `ip_not_allowed` | `permission_denied` | 403 | 客户端 IP 不在令牌白名单内 | 修白名单或出口 IP |
| `scope_missing` | `permission_denied` | 403 | 令牌缺少该调用或该站点的作用域 | 补授作用域 |
| `permission_denied` | `permission_denied` | 403 | 控制台用户缺少权限 | — |
| `session_invalid` | `unauthenticated` | 401 | 会话过期、被撤销或账号被禁用 | 重新登录 |
| `csrf_missing` | `permission_denied` | 403 | Cookie 认证的非安全方法请求缺少 `X-Spinneret-CSRF: 1` | 补上该请求头 |
| `login_throttled` | `resource_exhausted` | 429 | 15 分钟内同一用户 5 次或同一 IP 20 次登录失败 | 等待窗口过去 |
| `site_unknown` | `invalid_argument` | 400 | 令牌命名空间内没有该站点 | 修请求，不要重试 |
| `client_unknown` | `invalid_argument` | 400 | 站点没有该客户端类型 | 修请求 |
| `endpoint_group_unknown` | `invalid_argument` | 400 | 显式指定的 `endpoint_group` 不存在 | 修请求 |
| `uri_invalid` | `invalid_argument` | 400 | URI 无法解析 | 修请求 |
| `invalid_argument` | `invalid_argument` | 400 | 参数校验失败 | 修请求 |
| `no_identity_available` | `resource_exhausted` | 429 | 当前没有身份满足可用性规则 | **等待 `Spinneret-Retry-After-Ms` 后重试** |
| `no_proxy_available` | `resource_exhausted` | 429 | 策略要求代理但无可用代理 | 等待后重试；检查代理健康 |
| `circuit_open` | `unavailable` | 503 | 端点组熔断打开 | 按建议时长暂停该端点组 |
| `site_paused` | `unavailable` | 503 | 站点开关关闭 | 暂停该站点，不要猛打 |
| `rebuilding` | `unavailable` | 503 | 正在重建热状态 | 退避重试 |
| `rate_limited` | `resource_exhausted` | 429 | 触发令牌速率限制 | 降速 |
| `lease_unknown` | `not_found` | 404 | 租约从未存在或早已消失 | 重新领取 |
| `lease_released` / `lease_expired` | `failed_precondition` | 400 | 租约已经结束 | 重新领取 |
| `lease_lifetime_exceeded` | `failed_precondition` | 400 | `Renew` 触达 `max_lease_lifetime` | 释放并重新领取 |
| `not_found` | `not_found` | 404 | 配置项、密钥或对象不存在 | 不要重试 |
| `already_exists` | `already_exists` | 409 | 名称已被占用 | 换个名字 |
| `failed_precondition` | `failed_precondition` | 400 | 当前状态下该操作不合法 | 看 message |
| `conflict` | `aborted` | 409 | 并发修改 | 重新读取后重试一次 |
| `internal` | `internal` | 500 | 服务端故障 | 退避重试；查服务端日志 |

在 `Report` 中，单条上报的失败**不是** RPC 错误：调用返回 `200`，失败项在 `rejected` 里。

---

## 重试建议

| 情况 | 是否重试 | 方式 |
| --- | --- | --- |
| 连接被拒、DNS 失败、请求**发出之前**超时 | 是 | 带抖动的指数退避；对所有 RPC 都安全 |
| `unavailable` / `internal`（服务端） | 是 | 带抖动退避，尝试若干次 |
| 带 `Spinneret-Retry-After-Ms` 的 `resource_exhausted` | 是 | 先按建议时长休眠再重试 |
| `circuit_open`、`site_paused` | 不要立刻重试 | 按建议时长停止对该端点组/站点的领取；反复重试只会消耗配额 |
| `unauthenticated`、`permission_denied`、`invalid_argument`、`not_found` | 否 | 修配置 |
| 超时后用相同 `report_id` 重发 `Report` | 是 | 去重窗口内幂等——重试会返回 `duplicated` |
| 超时后重试 `Acquire` | 谨慎 | 它不是幂等的，重试可能多发一个租约。仅当失败发生在请求**发出之前**，或错误是服务端明确的 `unavailable` 时才重试 |

两个 SDK 实现的正是这套规则：2 次重试、100 ms–2 s 等抖动退避、服务端 `Retry-After` 建议最多采纳 5 秒，
`Acquire`/`AcquireBatch` 只在发送前失败或明确的 `unavailable` 时重试。

让整个集群保持健康的客户端约定：

- 给 `Acquire` 设置 `wait_ms`（500–2000），而不是写紧凑的重试循环——服务端排队的效率更高。
- 把 `circuit_open` 和 `site_paused` 当作暂停*工作线程*的信号，而不是重试*该请求*的信号。
- 失败也要上报：`http_status: 0` 配合正确的 `error_kind`，正是识别策略把责任归给代理而不是身份所需要的。
- 每次尝试用一个 `report_id`，并在重发同一条上报时保持不变。

---

## 管理 API

控制台用会话 Cookie 调用这些服务；脚本可以使用带 `admin` 作用域的令牌（或下文注明的更小作用域）。
控制台请求通过 `X-Spinneret-Tenant: <租户 id>` 携带租户，在请求体中按**名称**指定命名空间；
带 Cookie 的非安全方法请求还需要 `X-Spinneret-CSRF: 1`。

| 服务 | RPC |
| --- | --- |
| `AuthService` | `Login`、`Logout`、`GetMe`、`ChangePassword` |
| `TenantAdminService` | 租户与命名空间：`ListTenants`、`CreateTenant`、`UpdateTenant`、`DeleteTenant`、`ListNamespaces`、`CreateNamespace`、`UpdateNamespace`、`DeleteNamespace` |
| `AccessAdminService` | 令牌、用户、角色绑定、审计：`ListTokens`、`CreateToken`、`RevokeToken`、`ListUsers`、`CreateUser`、`UpdateUser`、`ResetPassword`、`ListRoleBindings`、`CreateRoleBinding`、`DeleteRoleBinding`、`ListAuditLogs` |
| `SiteAdminService` | 站点、端点组、URI 规则：`ListSites`、`GetSite`、`CreateSite`、`UpdateSite`、`DeleteSite`、`ListEndpointGroups`、`CreateEndpointGroup`、`UpdateEndpointGroup`、`DeleteEndpointGroup`、`ListURIRules`、`ReplaceURIRules`、`TestURI` |
| `IdentityAdminService` | 身份类型、身份、账号：`…IdentityType(s)`、`PreviewDelivery`、`ListIdentities`、`GetIdentity`、`ImportIdentities`、`UpdateIdentityPayload`、`UpdateIdentity`、`OperateIdentities`、`BulkOperateIdentities`、`RevertActions`、`ListStateEvents`、`GetIdentityHotState`、`ListAccounts`、`UpsertAccount`、`OperateAccount` |
| `ProxyAdminService` | `ListProxies`、`GetProxy`、`ImportProxies`、`UpdateProxy`、`OperateProxies`、`DeleteProxies`、`CheckProxy`、`GetProviderStats` |
| `PolicyAdminService` | `ListPolicies`、`GetPolicy`、`CreatePolicy`、`SaveDraft`、`PublishPolicy`、`RollbackPolicy`、`DeletePolicy`、`ListPolicyVersions`、`DiffPolicyVersions`、`ListBindings`、`SetBinding`、`DeleteBinding`、`ResolvePolicies`、`DebugReport`、`ValidatePolicy` |
| `BreakerAdminService` | `ListBreakers`、`GetBreaker`、`OpenBreaker`、`CloseBreaker`、`ListBreakerEvents`、`SetSitePaused` |
| `ConfigAdminService` | `ListConfigItems`、`GetConfigItem`、`CreateConfigItem`、`SaveConfigDraft`、`PublishConfig`、`RollbackConfig`、`DeleteConfigItem`、`ListConfigVersions`、`DiffConfigVersions` |
| `SecretAdminService` | `ListSecrets`、`GetSecret`、`CreateSecret`、`UpdateSecret`、`DeleteSecret`、`RevealSecret`、`ListSecretVersions`、`ListSecretAccessLogs`、`GetKEKStatus`、`StartKEKRewrap` |
| `NotificationAdminService` | `ListChannels`、`CreateChannel`、`UpdateChannel`、`DeleteChannel`、`TestChannel`、`ListAlertEvents` |
| `DashboardService` | `GetOverview`、`GetTimeSeries`、`GetHeatmap`、`ListRiskEvents`、`QueryRequestEvents`、`GetNodeStats` |

示例——登录并列出站点：

```bash
curl -s -c cookies.txt http://localhost:8080/spinneret.v1.AuthService/Login \
  -H 'Content-Type: application/json' -H 'X-Spinneret-CSRF: 1' \
  -d '{"username":"admin","password":"…"}'
# 响应包含用户信息、其租户、命名空间和角色绑定

curl -s -b cookies.txt http://localhost:8080/spinneret.v1.SiteAdminService/ListSites \
  -H 'Content-Type: application/json' -H 'X-Spinneret-CSRF: 1' \
  -H 'X-Spinneret-Tenant: ten_01a0afa70af372698c40d9d64c36123b' \
  -d '{"namespace":"default"}'
```

管理 API 的约定：

- **分页**：`page_size`（0 表示 50，最大 500）和不透明的 `page_token`；响应返回 `next_page_token`
  （最后一页为空）以及在代价可接受时返回 `total`。
- **时长**在这里是字符串而不是毫秒：`"500ms"`、`"30s"`、`"10m"`、`"24h"`、`"7d"`、`"1h30m"`、`"permanent"`。
- **可选字段**在更新中省略表示"保持不变"，在列表中省略表示"不过滤"。
- 每个有副作用的管理 RPC 都会写**审计日志**（`AccessAdminService/ListAuditLogs`）。
- 管理请求跨实例保证 read-your-writes：一次写入即使落在另一个副本上，下一次管理调用也能看到。

非 Connect 的 HTTP 端点：

| 端点 | 鉴权 | 用途 |
| --- | --- | --- |
| `GET /healthz` | 公开 | 存活检查 |
| `GET /readyz` | 公开 | 就绪检查：PostgreSQL、Redis、热状态、目录；退出期间为 `draining` |
| `GET /metrics` | 在主监听上公开 | Prometheus 指标——放到 `SPINNERET_METRICS_ADDR` 上可限制访问 |
| `GET /api/v1/events/stream?tenant=<id>&namespace=<name>` | 会话 | 控制台事件 SSE 流，按权限过滤 |
| `GET /*` | 公开 | 内嵌控制台（SPA 回退） |

`.proto` 文件是所有消息和校验规则的唯一真相源。可以用 [buf](https://buf.build) 从
[`proto/spinneret/v1`](../proto/spinneret/v1) 为任何语言生成客户端。
