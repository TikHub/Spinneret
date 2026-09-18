# Spinneret

[English](README.md)

**Spinneret 是多节点爬虫与 API 节点的控制平面。** 它统一管理身份（Cookie、设备参数、账号）、代理、配置和密钥，
并根据节点上报的请求结果自动执行冷却、封禁、健康评分和熔断——秒级生效，无需重新发布任何节点。

节点启动只需要两样东西：服务地址和 API 令牌。其余的一切——用哪份 Cookie、走哪个代理、某个端点是否正在熔断、
爬虫的配置是什么——都在请求时由 Spinneret 下发。

```
Acquire(site, client, uri)  →  身份 + 凭证 + 代理 + 租约
  …… 节点携带凭证向目标站发起请求 ……
Report(lease_id, 状态码, 耗时, 标记)  →  服务端识别信号、冷却、封禁、熔断
```

Spinneret **与站点无关**：不内置签名算法、登录流程和验证码识别。它管理请求周围的*状态*，请求本身仍由你的节点发出。

---

## 目录

- [解决什么问题](#解决什么问题)
- [功能概览](#功能概览)
- [系统架构](#系统架构)
- [界面截图](#界面截图)
- [快速开始](#快速开始)
- [节点 API 速览](#节点-api-速览)
- [SDK](#sdk)
- [文档索引](#文档索引)
- [开发](#开发)
- [项目状态](#项目状态)

---

## 解决什么问题

| 问题 | Spinneret 的做法 |
| --- | --- |
| Cookie、代理、密钥硬编码在镜像和 `.env` 里 | 镜像不含环境配置；凭证按请求领取，配置通过长轮询下发 |
| 各节点自行随机挑 Cookie，同一身份被并发使用 | 服务端统一发放租约，带复用间隔、配额和并发上限 |
| 缺少反馈闭环，失效身份被持续使用 | 上报驱动冷却、封禁、失效和健康分，全部在服务端判定 |
| 缺少全局视角，不知道哪里出了问题 | 控制台提供站点看板、身份 × 端点组热力图、请求浏览器和风控事件 |

---

## 功能概览

v0.1 的范围来自[设计文档](docs/design/0_first_doc.md)，下列模块均已实现。

| 模块 | 能力 |
| --- | --- |
| **身份调度**（§5、§6） | 带类型字段和交付模板的身份类型；轮换策略（`weighted_random`、`least_recently_used`、`round_robin`、`best_health`）、租约 TTL 与生命周期上限、复用间隔与锚点、配额、粘性会话、预热、探针权重 |
| **代理分发**（§11） | 按类型/地区/供应商/标签管理代理池，分配模式 `none / pool / bind_identity / region_match`，会话模板，周期健康检查，按站点的代理冷却 |
| **上报与信号识别**（§7） | 批量幂等上报；可配置识别规则（状态码、业务码、错误类型、标记、URI、方法、耗时、大小）；12 种结果分类；身份与代理之间的交叉归因 |
| **冷却与封禁**（§8） | 在身份×端点、身份×站点、身份、账号、代理×站点、代理六个层级执行 cooldown / expire / quarantine / ban；带上限的指数退避；升级阶梯；影子模式；人工处置与批量回滚（`RevertActions`） |
| **健康分与生命周期**（§9） | 带时间衰减的 EWMA 健康分、端点级低分冷却、自动隔离、身份状态机（`pending → active → quarantined / banned / expired / disabled / retired`） |
| **熔断**（§10） | 端点组级滑动窗口统计，三态熔断（关闭/打开/半开）与探针租约，人工开关，可选撤销触发窗口内施加的冷却 |
| **配置下发**（§12） | 版本化配置项，草稿、发布、回滚与差异对比；长轮询 `WatchConfig`；SDK 本地快照；`${secret:path}` 引用；只读 `_runtime` 分组暴露熔断与站点开关 |
| **Vault**（§13） | AES-256-GCM 信封加密（KEK → DEK → 数据），文件或环境变量 KEK 提供方，在线轮换与重新包装，密钥版本与过期，读取全部审计 |
| **认证与多租户**（§14） | 租户 → 命名空间 → 站点；控制台账号角色 `viewer / operator / admin / owner`，可按命名空间和站点绑定；节点令牌的细粒度作用域；Argon2id 口令、登录限流、会话、CSRF |
| **Web 控制台**（§17） | 21 个路由覆盖全部模块，中英双语，明暗主题，SSE 实时更新 |
| **告警通知**（§18.3） | Webhook（HMAC 签名）、飞书、钉钉、企业微信、Telegram；11 类告警，支持去重与按站点路由 |
| **可观测性**（§18.3） | `/healthz`、`/readyz`、Prometheus `/metrics`、可选 OTLP 链路追踪、基于 ClickHouse 的请求浏览器 |

---

## 系统架构

```mermaid
flowchart LR
    subgraph nodes["爬虫节点"]
        PY["Python SDK"]
        GO["Go SDK"]
        HTTP["纯 HTTP + JSON"]
    end

    subgraph plane["Spinneret（无状态，N 副本）"]
        API["API 角色<br/>LeaseService · ReportService<br/>ConfigService · SecretService<br/>管理服务 · 控制台"]
        WRK["Worker 角色<br/>上报管道 · 熔断评估<br/>租约回收 · 后台任务"]
    end

    subgraph data["数据层"]
        RD[("Redis / Valkey<br/>热状态、租约、<br/>上报流、会话")]
        PG[("PostgreSQL<br/>唯一真相源<br/>+ 聚合、审计")]
        CH[("ClickHouse<br/>原始请求事件<br/>可选")]
    end

    PY & GO & HTTP -->|Connect / gRPC / JSON| LB["负载均衡"]
    LB --> API
    API -->|"Lua：筛选、选择、写租约"| RD
    API -->|"上报入队"| RD
    RD -->|"Stream 分片"| WRK
    WRK -->|"识别 → 处置 → 观测"| RD
    WRK --> PG
    WRK --> CH
    API <--> PG
    API -.->|"Pub/Sub：catalog、config、runtime"| RD
    WRK -->|告警| NOTIFY["Webhook · 飞书<br/>钉钉 · 企业微信 · Telegram"]
    BROWSER["运维浏览器"] --> LB
```

**热路径。** `Acquire` 在 Redis 中执行一个 Lua 脚本：检查熔断、采样候选、按可用性过滤（冷却、复用间隔、配额、
并发）并写入租约，全程不访问 PostgreSQL。`Report` 校验、去重后写入由租约 ID 决定的 Redis Stream 分片即返回。
Worker 独占分片，按识别策略分类每条上报，执行处置策略，更新热状态并持久化聚合、状态事件和风控事件。

**数据分层。** PostgreSQL 是唯一真相源（目录、身份、策略、密钥、审计）。Redis 只保存派生的热状态，随时可以从
PostgreSQL 重建（`spnr rebuild`）。ClickHouse 可选，保存原始请求事件供请求浏览器查询。

**角色。** 同一个二进制，`SPINNERET_ROLE=all|api|worker`。默认 `all`，Compose 也使用它；当请求处理和上报消费
需要独立扩容时再拆分。

---

## 界面截图

| | |
| --- | --- |
| ![总览](docs/images/overview.png) | ![身份](docs/images/identities.png) |
| 总览：各站点健康度、QPS、结果分布、熔断 | 身份：状态、健康分、冷却、筛选 |
| ![热力图](docs/images/heatmap.png) | ![策略](docs/images/policies.png) |
| 热力图：身份 × 端点组的可用性 | 策略：YAML 编辑器、版本、差异、发布 |
| ![熔断](docs/images/breakers.png) | ![请求](docs/images/requests.png) |
| 熔断：状态、窗口、手动开关、站点开关 | 请求浏览器：每条上报的结果与归因 |

控制台是双语的——同一页面的中文版：[`docs/images/overview-zh.png`](docs/images/overview-zh.png)。

---

## 快速开始

环境要求：Docker 24+（含 Compose 插件）、约 4 GB 空闲内存、8080 端口可用。

```bash
git clone https://github.com/Evil0ctal/Spinneret.git
cd Spinneret

# 1. 生成 deploy/compose/.env（随机口令）和 deploy/compose/secrets/kek.key
./scripts/compose-init.sh

# 2. 构建镜像并启动 Postgres、Valkey、ClickHouse、迁移、2 个服务副本和负载均衡
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait

# 3. 创建第一个管理员、租户和命名空间（幂等）
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

打开 <http://localhost:8080>，以 `admin` 登录，口令在 `deploy/compose/.env` 中：

```bash
grep SPINNERET_ADMIN_PASSWORD deploy/compose/.env
```

### 运行示例爬虫

一个完整的 FastAPI 节点，对内置的 mock 目标站完成领取、请求、上报：

```bash
./scripts/example-quickstart.sh        # 或：make example
```

脚本是幂等的。它会创建站点 `example`（2 个端点组、20 个身份、2 个 mock 代理、已发布的轮换/识别策略、一个配置项），
创建节点令牌，在 <http://localhost:18000> 启动 `example-crawler` 服务并调用它：

```bash
curl 'http://localhost:18000/crawl/search?q=shoes'
curl  http://localhost:18000/crawl/item/42
curl  http://localhost:18000/config
```

然后让目标站"出问题"，在控制台（身份、熔断、请求）观察 Spinneret 的反应：

```bash
curl -X PUT localhost:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'
for i in $(seq 1 60); do curl -s -o /dev/null 'http://localhost:18000/crawl/search?q=x'; done
curl -X DELETE localhost:19090/_admin/rules
```

详见 [`examples/fastapi-crawler/README.zh-CN.md`](examples/fastapi-crawler/README.zh-CN.md)；
`scripts/example-quickstart.sh --reset` 可以清理这些示例数据。

---

## 节点 API 速览

每个 RPC 都是 `POST /spinneret.v1.<Service>/<Method>`，`Content-Type: application/json` 即可调用；
Connect、gRPC、gRPC-Web 同样支持。字段名是 snake_case，时间戳是 RFC 3339 UTC。

创建令牌（作用域可以精确到站点和配置分组）：

```bash
docker compose -f deploy/compose/docker-compose.yml exec spinneret \
  spnr token create --tenant default --namespace default --name crawler-hk \
    --scope lease:acquire:example --scope report:write:example \
    --scope config:read:crawler --expires 720h
# spn_EXAMPLEtokenEXAMPLEtokenEXAMPLEtoken1234567
```

**Acquire**：为即将发起的请求领取身份和代理。

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

**Report**：只上报事实，结果由服务端判定。`release: true` 同时结束租约：

```bash
curl -s http://localhost:8080/spinneret.v1.ReportService/Report \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"reports":[{
        "report_id":"9f1c4c40-2f6a-4d6e-9c63-8f1b1d8f0a11",
        "lease_id":"lse_01a0b0ff449e78a49c2d8cc240515316_i_05",
        "uri":"/site/search","method":"GET","http_status":200,
        "latency_ms":143,"response_bytes":48213,"markers":[],
        "started_at":"2026-09-17T20:11:54.100Z",
        "finished_at":"2026-09-17T20:11:54.243Z",
        "release":true}]}'
```

```json
{ "accepted": 1, "duplicated": 0, "rejected": [] }
```

错误通过响应头返回机器可读的原因和重试建议：

```http
HTTP/1.1 429 Too Many Requests
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 59367

{"code":"resource_exhausted","message":"no identity available for example/web/search"}
```

完整参考（全部节点服务、错误原因表、重试规则）见 [`docs/api.zh-CN.md`](docs/api.zh-CN.md)。

---

## SDK

| SDK | 包 | 特性 |
| --- | --- | --- |
| **Python** — [`sdk/python`](sdk/python/README.zh-CN.md) | `pip install spinneret` | 基于 httpx 的同步 `Client` 与异步 `AsyncClient`，pydantic v2 模型，`with client.lease(...)` 自动把凭证和代理合并进 `httpx` 参数，后台批量上报器，带本地快照的配置监听器 |
| **Go** — [`sdk/go`](sdk/go/README.zh-CN.md) | `github.com/Evil0ctal/Spinneret/sdk/go/spinneret` | Connect JSON 或 gRPC，类型化错误（`IsCircuitOpen`、`IsNoIdentity` 等），`Lease.Apply(req)` / `Lease.Transport(base)`，批量 `Reporter`，带快照的 `ConfigWatcher` |

```python
import httpx, spinneret

with spinneret.Client() as client:                       # 读取 SPINNERET_URL / SPINNERET_TOKEN
    with client.lease(site="example", client="web", uri="/site/search") as lease:
        with httpx.Client(**lease.httpx_kwargs()) as http:
            r = http.get("http://target/site/search", params={"q": "shoes"})
        lease.report_response(r, markers=["empty_list"] if not r.json() else [])
```

```go
client, _ := spinneret.New(spinneret.Options{})          // SPINNERET_URL / SPINNERET_TOKEN
lease, err := client.Lease(ctx, &spinneret.AcquireRequest{Site: "example", Client: "web", Uri: target})
if err != nil {
	return err                                           // spinneret.IsNoIdentity(err)、IsCircuitOpen(err) 等
}
defer lease.Close(ctx)                                   // 最后一条上报会释放租约

transport, _ := lease.Transport(nil)                     // http.DefaultTransport 副本 + 领取到的代理
req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
lease.Apply(req)                                         // 凭证中的 cookies、headers、query
resp, err := (&http.Client{Transport: transport}).Do(req)
if err != nil {
	return lease.ReportError(err, spinneret.ReportInput{})
}
return lease.ReportResponse(resp, spinneret.ReportInput{Markers: detectMarkers(resp)})
```

没有对应语言的 SDK？纯 HTTP + JSON 是一等公民，见 [`docs/api.zh-CN.md`](docs/api.zh-CN.md)。

---

## 文档索引

| 文档 | 内容 |
| --- | --- |
| [`docs/deployment.zh-CN.md`](docs/deployment.zh-CN.md) · [EN](docs/deployment.md) | 环境要求、Compose 部署、全部 `SPINNERET_*` 变量、KEK 管理、TLS 与反向代理、扩容、升级、备份、安全加固、故障排查 |
| [`docs/operations.zh-CN.md`](docs/operations.zh-CN.md) · [EN](docs/operations.md) | 日常运维手册：租户、用户与角色、令牌、站点与端点组、身份类型与导入、四类策略的 YAML、熔断、回滚、代理、配置中心与密钥、告警、监控、热状态重建、KEK 轮换、数据保留 |
| [`docs/api.zh-CN.md`](docs/api.zh-CN.md) · [EN](docs/api.md) | 节点 API 参考（请求/响应 JSON）、错误原因表、重试建议、管理 API 概览 |
| [`docs/benchmarks.zh-CN.md`](docs/benchmarks.zh-CN.md) · [EN](docs/benchmarks.md) | 针对 v0.1 性能目标的实测：方法、各场景结果、耗时归因、Redis 容量估算、调优建议 |
| [`proto/README.md`](proto/README.md) | 协议约定、JSON 映射、请求头、服务列表 |
| [`sdk/python/README.zh-CN.md`](sdk/python/README.zh-CN.md) | Python SDK 参考 |
| [`sdk/go/README.zh-CN.md`](sdk/go/README.zh-CN.md) | Go SDK 参考 |
| [`examples/fastapi-crawler/README.zh-CN.md`](examples/fastapi-crawler/README.zh-CN.md) | 示例节点逐接口说明 |
| [`web/README.md`](web/README.md) | 控制台开发 |
| [`test/load/README.md`](test/load/README.md) | k6 压测场景与性能目标 |
| [`docs/design/`](docs/design/) | 设计文档与据此编写的工程规格 |
| [`CHANGELOG.md`](CHANGELOG.md) | 版本说明 |

---

## 开发

环境要求：Go 1.27+、Node 22 + pnpm 10、Docker（集成测试依赖）、Python 3.9+（SDK）。

```bash
export PATH="$(go env GOPATH)/bin:$PATH"

make infra-up          # 测试用的 PostgreSQL、Valkey、ClickHouse，端口 45432 / 46379 / 49000
make test              # go test ./...            （集成测试使用上面的基础设施）
make test-race         # go test -race ./...
make lint vet fmt      # golangci-lint、go vet、gofmt
make generate          # buf generate + sqlc generate（生成结果需要提交）

make web-install web   # pnpm install + pnpm build（控制台通过 go:embed 内嵌）
make build             # bin/spinneret-server 和 bin/spnr
make docker up down    # 构建镜像、启动/停止 Compose 栈
```

测试套件：

| 命令 | 内容 |
| --- | --- |
| `make test` | Go 单元测试与集成测试 |
| `make e2e` | Compose 网络内的 Go 端到端场景（`test/e2e`，构建标签 `e2e`） |
| `make e2e-failover` | 在领取/上报压力下停掉一个副本的故障演练 |
| `make e2e-web` | 驱动真实控制台的 Playwright 套件（`web/e2e`） |
| `cd web && pnpm test` | 控制台单元测试（Vitest） |
| `make python-test` | Python SDK 测试 |
| `make example-test` | 示例爬虫测试 |
| `make load` | k6 压测场景（profile `loadtest`） |

`.github/workflows/ci.yml` 会以 `-race` 运行 Go 套件、校验生成代码是否最新、构建并测试控制台、在
3.9 / 3.12 / 3.13 上测试 Python SDK，并构建镜像。

`spnr` CLI 负责部署的管理操作（迁移、首个管理员、令牌、热状态重建、KEK、压测数据），读取与服务端相同的
`SPINNERET_*` 环境变量：

```bash
spnr migrate up|down|status      spnr admin init --username admin --password-stdin
spnr token create --name ...     spnr rebuild [--site ...]
spnr kek generate|status|rewrap  spnr seed --site loadtest --identities 100000
spnr config check                spnr healthcheck --url http://127.0.0.1:8080/readyz
```

---

## 项目状态

Spinneret v0.1 已按设计文档完成全部功能：四个里程碑（核心链路、风控闭环、基础设施、控制台与发布）均已实现，
并附带 Go 端到端场景、故障演练、Playwright 控制台套件和 k6 压测场景。

**§18.4 的全部性能目标已在单个服务实例 + 单个 Redis 实例上达成。** 在设计文档自己的测试条件
（单站点、10 万身份、50 个端点组）下，Lua 热路径优化后的实测：

* 仅 Acquire：**单实例 4,993/s，服务端 p99 4.32 ms**（峰值 7,792/s），目标是 5,000/s 且 p99 < 5 ms。
* 完整「领取 → 上报」循环（5 个 Lua 脚本）：`max_concurrent_leases: 4` 时**单实例 5,500/s**，
  压测 seed 配置的独占租约下 **4,500/s** —— 原为 3,000/s 和 2,000/s。
* 上报接收单实例 **44,437 条/秒**零拒绝（目标 20,000/s），其中 **19,761 条/秒**在 p99 30.7 ms 内写入
  热态（目标 p99 < 200 ms）。
* 一次「领取 → 上报」循环消耗 **168.3 µs 的 Redis CPU**，原为 271.2 µs：**每个 Redis 线程
  5,940 循环/秒**，原为 3,690。

剩下的瓶颈不是 Redis CPU。负载均衡器后面挂两个副本时总吞吐只有 3,000 循环/秒 —— 比单副本自己还少 ——
因为每个实例都对共享 Redis 施加各自不设上限的并发，而失败的 acquire 成本约是成功的 5 倍，于是争用被
放大成拥塞崩溃，而不是优雅降级。下一步的杠杆依次是：Acquire 路径上的每实例准入控制、让被拒候选变便宜、
然后才是 Redis Cluster。完整的优化前后对比表、逐脚本成本以及这些数字所依赖的 Valkey 配置见
[`docs/benchmarks.zh-CN.md`](docs/benchmarks.zh-CN.md)。

v0.2 候选（明确不在 v0.1 范围内）：分布式全局限速、外部校验器与刷新器 Webhook、代理供应商适配器、
NATS JetStream、OIDC 与 TOTP、mTLS、配置灰度发布、指纹配置分发、浏览器池。

**许可证：** 暂无。仓库尚未附带许可证文件，在添加之前作者保留一切权利，再分发前请先询问。
