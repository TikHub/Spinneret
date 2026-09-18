# Spinneret 开发文档（v0.1 初版）

2026-09-16 · @Someone

## 1. 项目概述

Spinneret 是多节点爬虫与 API 节点的控制平面：统一管理身份、代理、配置和密钥，并根据节点上报的请求结果自动冷却、封禁和熔断。

### 1.1 要解决的问题

- 凭证硬编码：Cookie、代理和密钥写在代码或 .env 中，每次更新都要重新发版。
- 调度无序：各节点自行随机挑选 Cookie，同一身份被并发使用，容易触发风控。
- 缺少反馈闭环：身份被风控后没有统一的冷却和剔除，失效凭证被持续使用。
- 缺少全局视角：无法快速定位是哪个接口、哪批身份或哪个代理出了问题。

### 1.2 设计目标

- 镜像不含环境配置，容器启动只需要服务地址和节点令牌。
- 领取接口低延迟，服务端处理 p99 目标低于 5 ms（不含网络）。
- 下游零门槛接入：普通 HTTP + JSON 即可调用，Python SDK 为可选增强。
- 策略在控制台配置，修改后秒级生效，节点无需重启。
- 核心与站点无关，不内置任何网站的签名、登录或绕过逻辑。

### 1.3 非目标

- 不代替节点发起爬虫请求，也不做任务调度，可与既有的爬虫任务调度平台配合。
- 不内置验证码识别、签名算法和登录流程，由使用方在节点侧或外部服务实现。
- 分布式全局限速、浏览器池、指纹配置分发不在 v0.1 范围内，见第 19 节。

### 1.4 v0.1 功能范围

| 模块 | 内容 |
| --- | --- |
| 身份调度 | Cookie、设备参数等身份的领取、租约、轮换策略、复用间隔、配额 |
| 代理分发 | 代理池管理、按池分配或按身份绑定、代理冷却与健康检查 |
| 结果上报与信号识别 | 请求结果异步上报，按可配置规则识别风控信号并归因 |
| 冷却与封禁 | 多层级冷却、临时与永久封禁、升级阶梯、人工处置与误封回滚 |
| 健康评分与生命周期 | EWMA 健康分、身份与代理状态机、自动隔离 |
| 熔断 | 端点组级滑动窗口统计与三态熔断 |
| 配置下发 | 版本化配置、发布与回滚、长轮询监听、密钥引用 |
| Vault | 信封加密的密钥存储、访问控制与审计 |
| Web 控制台 | 覆盖以上全部模块的管理界面 |

## 2. 典型使用场景

最常见的调用链是"领取 → 请求 → 上报"：爬虫每次请求目标站前领取身份，请求后异步上报结果，Spinneret 据此更新冷却、封禁和健康分。

```mermaid
sequenceDiagram
    participant C as FastAPI 爬虫节点
    participant S as Spinneret
    participant R as Redis 热状态
    participant T as 目标站
    C->>S: Acquire(site, client, uri)
    S->>R: Lua 筛选候选并写租约
    R-->>S: 身份 ID、代理 ID
    S-->>C: 租约、凭证、代理
    C->>T: 携带凭证与代理发起请求
    T-->>C: 响应
    C-)S: Report(lease_id, 状态码, 耗时, 标记)
    S-)R: Worker 更新健康分、冷却、窗口
    Note over S,R: 命中规则时执行冷却、封禁或熔断
```

图中实线箭头为同步调用，开口箭头为异步调用。

### 2.1 流程说明

1. 启动：容器只注入 `SPINNERET_URL` 和 `SPINNERET_TOKEN`，启动时拉取配置并写入本地快照。
2. 领取：请求目标站前调用 `LeaseService/Acquire`，传入站点、客户端类型和目标 URI。服务端匹配端点组，按轮换策略选出身份和代理，返回租约。
3. 请求：节点按段合并凭证。Web 爬虫通常使用 `cookie_header` 和 `headers`，App 爬虫通常使用 `query`、`json` 和 `values` 中的设备参数。
4. 上报：请求结束后调用 `ReportService/Report`，携带租约 ID、状态码、业务码、耗时和节点识别到的标记。SDK 在后台批量发送，不阻塞爬虫。
5. 判定：服务端按识别规则得出结果分类，再按处置规则执行冷却、失效或封禁，并计入熔断窗口。
6. 释放：上报带 `release: true` 时结束租约；节点崩溃未上报时，租约到期自动回收。

一个租约可以服务多次请求，例如翻页需要固定同一个 Cookie。每次请求各上报一次，最后一次带 `release: true`。

### 2.2 场景变体

| 场景 | 处理方式 |
| --- | --- |
| 翻页或多步流程需要同一身份 | Acquire 时传 `session_key`，粘性配置在有效期内返回同一身份 |
| 同一接口 X 秒内不能复用同一身份 | 在轮换策略中设置 `reuse_interval` |
| 批量任务一次需要多个身份 | 调用 `AcquireBatch`，返回互不相同的身份 |
| 暂时没有可用身份 | 返回 `resource_exhausted` 和建议等待时间；也可传 `wait_ms` 最多等待 5 秒 |
| 接口整体风控收紧 | 端点组熔断，Acquire 直接返回 `unavailable`，节点暂停该接口 |
| Cookie 登录态失效 | 身份标记为 `expired` 并通知刷新服务，更新载荷后重新验证 |
| 签名服务密钥等固定参数 | 放在配置中心或 Vault，不走身份调度 |

## 3. 核心概念与术语

身份是 Spinneret 的调度单位，冷却、封禁和健康分都挂在"身份 × 端点组"或更粗的层级上。

| 概念 | 标识 | 说明 | 示例 |
| --- | --- | --- | --- |
| 命名空间 | namespace | 隔离不同项目或环境的数据与权限 | `prod`、`staging` |
| 站点 | site | 一个目标平台，冷却与熔断的顶层作用域 | `shop`、`market` |
| 客户端类型 | client | 同一站点下的接入形态，决定可用的身份类型 | `web`、`app` |
| 端点组 | endpoint group | 风控特征相近的一组 URI，是冷却与熔断的最小接口粒度 | `search`、`detail` |
| URI 规则 | uri rule | 把请求 path 映射到端点组的匹配规则 | 前缀 `/api/v1/search` |
| 身份类型 | identity type | 身份的字段模板与交付格式 | `web_cookie`、`app_device` |
| 身份 | identity | 一份可调度的凭证，如一组 Cookie 或一套设备参数 | `idt_0192a3f0aa51` |
| 账号 | account | 可选，多个身份可归属同一账号，封禁可沿账号传播 | 同一账号多次登录得到的 Cookie |
| 代理 | proxy | 出口代理，含类型、地区、供应商 | 住宅代理、隧道代理 |
| 租约 | lease | 一次领取的结果，持有期间身份被占用 | `lse_0192a3f4c1d2`，默认 TTL 120 秒 |
| 上报 | report | 一次请求的结果回传 | 状态码 429，耗时 830 ms |
| 结果分类 | outcome | 服务端对上报的归类 | `success`、`captcha` |
| 策略 | policy | 轮换、识别、处置、熔断四类规则，可绑定到不同层级 | `web-search-rotation` |
| 节点 | node | 调用 Spinneret 的爬虫或 API 实例 | `crawler-hk-03` |

ID 统一使用带类型前缀的 UUIDv7（如 `idt_`、`pxy_`、`lse_`），按时间有序，便于日志排查。

策略按"端点组 > 客户端类型 > 站点 > 命名空间默认"的优先级查找，下层未绑定时继承上层。

## 4. 系统架构与技术栈

Spinneret 采用"无状态 Go 服务 + PostgreSQL 真相数据 + Redis 热状态"三层结构，领取和上报的热路径只访问 Redis。

```mermaid
flowchart LR
    N[爬虫 / API 节点<br/>HTTP JSON 或 gRPC] --> LB[负载均衡]
    W[Web 控制台] --> LB
    LB --> S[spinneret-server<br/>多实例 无状态]
    S --> R[(Redis / Valkey<br/>租约 冷却 评分)]
    S --> P[(PostgreSQL<br/>元数据 配置 审计)]
    R --> K[上报 Worker<br/>同一二进制内]
    K --> R
    K --> P
```

服务实例同时承担 API 和后台 Worker 角色，可通过启动参数只开启其中一种。

### 4.1 数据分层

| 层 | 存储 | 内容 | 一致性 |
| --- | --- | --- | --- |
| 真相数据 | PostgreSQL | 站点、端点组、策略、身份元数据与加密载荷、代理、配置、密钥、状态事件、审计 | 强一致，需要备份 |
| 热状态 | Redis / Valkey | 可用队列、租约、冷却截止、健康分、窗口计数、熔断状态 | 最终一致，可从 PostgreSQL 重建 |
| 事件流 | Redis Streams | 上报事件、实例间变更通知 | 至少一次投递 |
| 分析（可选） | ClickHouse | 逐请求原始事件 | v0.2 接入 |

v0.1 的事件流用 Redis Streams 替代之前讨论的 NATS JetStream，使最小部署只依赖 PostgreSQL 和 Redis。事件总线抽象为接口，吞吐不足时再接入 NATS。

### 4.2 热路径原则

- Acquire 在一个 Lua 脚本内完成候选筛选、加权选择和写租约，只需一次 Redis 往返。
- Report 接口只做校验、去重和入队，随即返回；状态更新由 Worker 异步完成，p99 目标低于 200 ms。
- 策略、URI 规则、端点组和令牌校验结果缓存在进程内存，变更通过 Redis Pub/Sub 通知所有实例刷新。
- 身份载荷解密后按"身份 ID + 载荷版本"缓存在进程内存，避免每次领取都解密。

### 4.3 技术选型

| 领域 | 选型 |
| --- | --- |
| 语言 | Go 最新稳定版 |
| API | ConnectRPC + Protobuf，buf 管理，protovalidate 校验，标准库 net/http |
| 数据库访问 | pgx + sqlc，迁移使用 goose |
| Redis 客户端 | rueidis（自动 pipeline，高并发吞吐更高），兼容 Redis 与 Valkey |
| 加密 | 标准库 AES-256-GCM；控制台密码使用 Argon2id 哈希 |
| 可观测性 | log/slog、OpenTelemetry、Prometheus |
| 前端 | React + TypeScript + Vite，TanStack Router / Query / Table / Virtual，shadcn/ui + Tailwind CSS，ECharts，Monaco Editor，connect-es |
| 打包发布 | 前端通过 embed 打入二进制，goreleaser 构建多平台二进制与镜像 |
| 测试 | testcontainers-go 做 PostgreSQL 与 Redis 集成测试，k6 做 HTTP 压测 |

## 5. 身份与凭证模型

身份由"身份类型"定义字段与交付格式，同一套调度逻辑即可同时服务 Web 爬虫的 Cookie 和 App 爬虫的设备参数、Token 或任意 JSON。

### 5.1 身份类型

身份类型声明所属站点与客户端类型、载荷字段、交付映射、去重键和激活方式。

```yaml
# Web 爬虫：Cookie 类身份
name: web_cookie
site: shop
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
  signature:  { type: string, sensitive: true }
unique_by: [cookies.sessionid]
activation: probe
deliver:
  cookies: "{{ cookies }}"
  cookie_header: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
  values:
    signature: "{{ signature }}"
```

```yaml
# App 爬虫：设备参数类身份
name: app_device
site: shop
client: app
fields:
  device_id:  { type: string, required: true }
  install_id: { type: string, required: true }
  cookies:    { type: cookie_map, sensitive: true }
  extra:      { type: json }
unique_by: [device_id]
activation: immediate
deliver:
  query:
    device_id: "{{ device_id }}"
    install_id: "{{ install_id }}"
  cookie_header: "{{ cookies }}"
  json: "{{ extra }}"
```

交付模板只支持字段占位符，不支持表达式，避免模板注入和额外开销。`cookie_map` 字段放入 `cookie_header` 时自动拼接为 `k1=v1; k2=v2`。

### 5.2 字段类型

| 类型 | 说明 | 交付处理 |
| --- | --- | --- |
| `string` / `number` / `bool` | 基础类型 | 原样输出 |
| `cookie_map` | Cookie 键值对 | 输出为对象，或拼接为 Cookie 头 |
| `json` | 任意 JSON 结构 | 原样透传 |
| `secret_ref` | 引用 Vault 中的共享密钥 | 领取时解析为明文 |

标记为 `sensitive` 的字段加密存储，控制台脱敏显示，服务端日志不输出。

### 5.3 交付格式

Acquire 响应中的 `credential` 固定包含 6 个段，节点无需了解身份类型细节，按段合并即可。

| 段 | 类型 | 节点用法 |
| --- | --- | --- |
| `cookies` | 对象 | 传给 httpx 的 `cookies` 参数 |
| `cookie_header` | 字符串 | 直接作为 `Cookie` 请求头 |
| `headers` | 对象 | 合并到请求头 |
| `query` | 对象 | 合并到查询参数 |
| `json` | 任意 JSON | 合并到请求体，或按需使用 |
| `values` | 对象 | 自定义取值，如签名计算需要的 Token |

### 5.4 导入、更新与刷新

- 导入：控制台上传 JSON Lines 或 CSV，也可调用 `IdentityAdminService/ImportIdentities`。每行按 JSON Schema 校验，失败行返回原因。
- 去重：按 `unique_by` 去重，重复导入视为更新载荷。
- 载荷版本：每次更新版本号加 1，旧版本加密保留（默认 5 个），用于审计和回滚。
- 激活：`immediate` 导入即为 `active`；`probe` 先进入 `pending`，以低权重参与调度，首次成功后转为 `active`。
- 刷新（v0.1）：身份进入 `expired` 时发送 Webhook，刷新服务也可轮询 `ListIdentities(state=expired)`。刷新服务拿到新凭证后调用 `UpdateIdentityPayload`，身份回到 `pending` 重新验证。
- 刷新（v0.2）：支持配置刷新器与校验器 Webhook，由 Spinneret 主动调用。

### 5.5 账号

身份可关联 `account_id`，表达"多份 Cookie 属于同一账号"。账号级封禁对其下全部身份生效；身份被判定 `banned` 时，可按处置规则沿账号传播。

## 6. 调度与轮换

一次领取依次执行"匹配端点组 → 检查熔断 → 粘性复用 → 采样候选 → 过滤 → 选择 → 写租约"，候选过滤与选择在同一个 Lua 脚本中原子完成。

```mermaid
flowchart TD
    A[Acquire 请求] --> B[URI 匹配端点组]
    B --> C{熔断状态}
    C -- 打开 --> X[返回 unavailable]
    C -- 关闭或半开 --> D{session_key<br/>已绑定且可用}
    D -- 是 --> I[复用该身份]
    D -- 否 --> E[取最早可用的 K 个候选]
    E --> F[过滤 状态 并发 配额 账号]
    F -- 无候选 --> Y[返回 resource_exhausted]
    F --> G[按轮换策略选择]
    G --> I
    I --> J[写租约 更新可用时间]
    J --> K[分配代理并返回]
```

半开状态下只放行少量探测租约，见第 10 节。

### 6.1 URI 匹配

- 端点组隶属于"站点 + 客户端类型"，包含一组 URI 规则，只匹配 path，忽略查询参数。
- 规则类型：`exact` 精确、`template` 路由模板（如 `/api/item/{id}/`）、`prefix` 前缀、`regex` 正则。
- 优先级：`exact` > `template` > `prefix`（最长优先）> `regex`（按配置顺序）；都未命中时归入站点默认端点组 `_default`。
- 实现：精确匹配用哈希表，模板和前缀用 radix tree，正则用预编译的 RE2；规则变更后原子替换整棵树。
- 节点可直接传 `endpoint_group` 跳过匹配；控制台提供 URI 测试器，显示命中的端点组和生效策略。

### 6.2 轮换策略配置

```yaml
name: web-search-rotation
bind: { site: shop, client: web, endpoint_group: search }
identity_types: [web_cookie]
rotation:
  strategy: weighted_random     # weighted_random | least_recently_used | round_robin | best_health
  candidate_sample: 32          # 每次采样的候选数 K
  lease_ttl: 120s
  max_concurrent_leases: 1      # 每个身份同时持有的租约数，1 即独占
  reuse_interval: 30s           # 同一身份两次使用的最小间隔
  reuse_anchor: released        # 间隔起点：acquired 领取时 | released 释放时
  reuse_scope: endpoint_group   # 间隔作用域：endpoint_group | site
  quota:                        # 每个身份在本端点组的请求配额
    - { limit: 60, window: 1h }
    - { limit: 500, window: 24h }
  sticky:
    enabled: true
    ttl: 10m
  warmup:                       # 新激活身份的预热期
    duration: 24h
    quota_factor: 0.3
  probe:                        # pending 身份的探测流量
    weight_factor: 0.1
    max_leases: 2
proxy:
  mode: bind_identity
  kinds: [residential]
```

### 6.3 选择策略

| 策略 | 行为 | 适用场景 |
| --- | --- | --- |
| `weighted_random`（默认） | 在最早可用的 K 个候选中按健康分加权随机 | 通用，兼顾均匀使用和质量优先 |
| `least_recently_used` | 选择在本端点组最久未使用的身份 | 身份质量接近，追求平均消耗 |
| `round_robin` | 按固定顺序轮询 | 身份很少，需要可预测的行为 |
| `best_health` | 总是选择健康分最高的身份 | 小流量高价值接口；注意会集中消耗优质身份 |

粘性（`sticky`）是叠加在以上策略之上的开关：同一 `session_key` 在有效期内优先复用原身份，原身份不可用时按策略重选并更新绑定。

### 6.4 可用时间与复用间隔

每个身份在每个端点组上有一个"可用时间"，存为 ZSET 分值，所有时间类约束都折算进这一个值：

```
可用时间 = max(复用间隔到期, 端点组冷却截止, 站点级冷却截止, 账号冷却截止, 封禁截止, 独占租约到期)
```

- 领取时：`reuse_anchor = acquired` 则复用间隔从领取时刻起算；`max_concurrent_leases = 1` 时，可用时间至少推到租约到期。
- 释放时：`reuse_anchor = released` 则复用间隔从释放时刻起算，并重新计算可用时间。
- `reuse_scope = site` 时，间隔写入身份全局状态，对站点下所有端点组生效。
- 身份级冷却或封禁会同时更新该站点所有端点组的分值（端点组通常只有几十个），领取脚本再做一次状态兜底检查。

这样采样候选只需一次 `ZRANGEBYSCORE rdy -inf <now> LIMIT 0 K`，不必逐个检查冷却。

### 6.5 选择算法

1. 从可用队列取可用时间不晚于当前时刻的前 K 个身份，最早可用的优先，天然带有 LRU 倾向。
2. 过滤生命周期状态、活跃租约数、配额、账号状态和代理绑定。被过滤的身份按原因推迟可用时间，避免下次再被采样。
3. 计算权重 `w = max(score, 5)^2`，`pending` 身份再乘以 `probe.weight_factor`。
4. 按权重轮盘赌抽样，随机数由 Go 侧作为脚本参数传入，便于测试复现。
5. K 个候选全部被过滤时最多再采样 2 轮，仍无结果则返回 `resource_exhausted`。

### 6.6 并发、配额与预热

- 并发：按身份计数活跃租约，领取加 1，释放或过期减 1，达到 `max_concurrent_leases` 后不可领取。
- 配额：按"当前窗口计数 + 上一窗口计数 × 剩余比例"近似滑动窗口，内存占用小、计算快。
- 配额耗尽的身份，可用时间直接推到窗口滚动时刻。
- 预热：身份首次激活后的 `warmup.duration` 内，配额按 `quota_factor` 缩放。

### 6.7 无可用身份

- 默认立即返回 `resource_exhausted`，响应头 `Spinneret-Retry-After-Ms` 为最早可用身份的剩余等待时间，上限 60 秒。
- 请求携带 `wait_ms`（不超过 5000）时，服务端在等待期内按 50–200 ms 递增间隔重试。
- 端点组可用身份数低于 `low_watermark` 时触发告警。

## 7. 结果上报与风控信号识别

节点每次请求后异步上报观察到的事实，服务端用可配置规则把它归为 12 种结果分类之一，并判定责任归属于身份、代理，还是都不是。

节点只上报事实，判定逻辑集中在服务端，调整识别规则无需重新部署爬虫。

### 7.1 上报字段

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `report_id` | 是 | 节点生成的 UUID，用于幂等去重 |
| `lease_id` | 是 | 对应的租约 |
| `uri` | 是 | 实际请求的 path |
| `method` | 否 | HTTP 方法 |
| `http_status` | 否 | HTTP 状态码，网络错误时省略 |
| `business_code` | 否 | 响应体中的业务状态码 |
| `error_kind` | 否 | 节点侧错误：`timeout`、`conn_reset`、`conn_refused`、`proxy_auth`、`tls`、`dns` |
| `markers` | 否 | 节点识别出的特征标记，如 `captcha_page`、`login_redirect`、`empty_list` |
| `outcome_hint` | 否 | 节点自行判断的分类，服务端可配置是否采信 |
| `latency_ms` | 否 | 请求耗时 |
| `response_bytes` | 否 | 响应体大小 |
| `started_at` / `finished_at` | 是 | 请求起止时间 |
| `release` | 否 | 为 true 时同时释放租约 |

`markers` 用来表达服务端看不到的响应体特征，节点不需要上传响应体。

### 7.2 结果分类

| 分类 | 含义 | 严重程度 | 默认归因 |
| --- | --- | --- | --- |
| `success` | 请求成功且数据有效 | 无 | 无 |
| `empty` | 返回成功但数据为空或截断，疑似软风控 | 软 | 身份 |
| `rate_limited` | 被限流，如 429 或限流业务码 | 软 | 身份与代理 |
| `captcha` | 出现验证码、滑块或验证页 | 硬 | 身份 |
| `auth_invalid` | 登录态失效或 Cookie 过期 | 硬 | 身份 |
| `forbidden` | 访问被拒或账号异常提示 | 硬 | 身份 |
| `banned` | 账号已被封禁 | 致命 | 身份或账号 |
| `proxy_error` | 代理连接或认证失败 | 软 | 代理 |
| `network_error` | 超时、连接重置 | 软 | 代理（弱） |
| `target_error` | 目标站 5xx | 中性 | 仅统计 |
| `client_error` | 参数或签名错误，属于爬虫自身问题 | 中性 | 无，触发告警 |
| `unknown` | 未命中任何规则 | 中性 | 无 |

下文的"风控类结果"指 `rate_limited`、`captcha`、`forbidden`、`banned` 四类。

### 7.3 识别规则

识别规则绑定到站点、客户端类型或端点组，自上而下匹配，命中第一条即停止。

```yaml
name: web-signals
bind: { site: shop, client: web }
trust_outcome_hint: false
rules:
  - when: { error_kind: [proxy_auth, conn_refused] }
    outcome: proxy_error
  - when: { error_kind: [timeout, conn_reset, tls, dns] }
    outcome: network_error
  - when: { markers: [captcha_page] }
    outcome: captcha
  - when: { markers: [login_redirect] }
    outcome: auth_invalid
  - when: { http_status: [429] }
    outcome: rate_limited
  - when: { http_status: { gte: 500 } }
    outcome: target_error
  - when: { http_status: [200], business_code: [10001, 10002] }   # 示例值，按站点实际填写
    outcome: forbidden
  - when: { http_status: [200], markers: [empty_list] }
    outcome: empty
  - when: { http_status: [400] }
    outcome: client_error
  - when: { http_status: { gte: 200, lt: 300 } }
    outcome: success
```

- 可用条件：`http_status`、`business_code`、`error_kind`、`markers`（任一命中）、`uri`（前缀或正则）、`latency_ms` 与 `response_bytes`（范围）。同一条规则内的条件是"且"关系。
- 规则加载时编译为内存结构，由 Worker 在 Go 中执行，不进入 Lua。
- 规则可覆盖默认归因：`blame: identity | proxy | both | none`。
- `unknown` 占比在总览页展示，超过阈值时告警，提示补充规则。

### 7.4 交叉归因

同一个失败既可能是身份问题，也可能是代理问题，v0.1 用简单的交叉统计纠正归因：

- 10 分钟窗口内，同一代理在至少 3 个不同身份上出现风控类结果，则后续该代理上的风控类结果改为归因代理，并冷却该代理。
- 同一身份在至少 3 个不同代理上都出现风控类结果，则确认归因身份。
- 用 Redis 小集合记录窗口内"代理 → 身份"和"身份 → 代理"的关系，窗口与阈值可配置。

### 7.5 可靠性

- 幂等：`report_id` 去重记录保留 1 小时，重复上报计为 `duplicated`。
- 批量：一次请求最多 500 条；SDK 默认每 200 ms 或攒满 100 条发送一次。
- 迟到上报：租约结束后 10 分钟内的上报仍参与处置判定，更晚的只计入统计。
- 未上报：租约到期仍无上报时记为 `abandoned` 事件，默认不惩罚身份，但计入节点的未上报率。
- 顺序：租约 ID 内嵌站点标识和分片号，同一身份的上报总是进入同一个 Stream 分片，按顺序处理。

## 8. 冷却与封禁

处置分为冷却、失效、隔离、临时封禁、永久封禁五种，按身份、账号、代理等层级生效；同一次上报命中多条规则时，对每个主体只执行最严重的处置。

冷却是调度层面的短时限制，只推迟可用时间。失效、隔离和封禁会改变生命周期状态、写入状态事件，并在控制台醒目展示。

### 8.1 作用层级

| 层级 | 效果 | 典型触发 |
| --- | --- | --- |
| 身份 × 端点组 | 该身份暂停调用此端点组，其余端点组照常 | 搜索接口返回 429 |
| 身份 × 站点 | 该身份在整个站点暂停 | 出现验证码 |
| 账号 | 账号下全部身份暂停或封禁 | 账号封禁提示 |
| 代理 × 站点 | 该代理对此站点暂停 | 多个身份经同一代理被限流 |
| 代理全局 | 该代理对所有站点暂停 | 代理连续连接失败 |
| 端点组 | 所有身份暂停此端点组 | 熔断，见第 10 节 |

### 8.2 处置类型

| 处置 | 状态变化 | 恢复方式 |
| --- | --- | --- |
| `cooldown` 冷却 | 不变，仅推迟可用时间 | 到期自动恢复 |
| `expire` 失效 | 变为 `expired` | 更新载荷后进入 `pending` |
| `quarantine` 隔离 | 变为 `quarantined` | 人工恢复，或隔离期满进入 `pending` |
| `ban` 临时封禁 | 变为 `banned`，带截止时间 | 到期进入 `pending` 重新验证，可配置为直接恢复 `active` |
| `ban` 永久封禁 | 变为 `banned`，无截止时间 | 仅人工解封 |

严重程度从高到低：永久封禁 > 临时封禁 > 失效 > 隔离 > 冷却；同级时取时长更长者。

### 8.3 冷却时长

```
冷却时长 = min(base × multiplier ^ min(n - 1, max_exponent), max) × jitter
```

- `n` 为该层级的连续失败次数（含本次）；`jitter` 在 0.8–1.2 之间均匀随机，避免一批身份同时结束冷却。
- 一次成功使 `n` 减半，超过 `failure_reset_after`（默认 1 小时）无失败则清零。
- 例如 base 60s、multiplier 2、max 30m 时，连续第 1 到第 6 次失败的冷却约为 1、2、4、8、16、30 分钟。

### 8.4 处置规则

```yaml
name: web-search-actions
bind: { site: shop, client: web, endpoint_group: search }
extends: web-default            # 继承站点级规则，本策略的规则追加在后
mode: enforce                   # enforce 生效 | shadow 影子模式
rules:
  - when: { outcome: rate_limited }
    action: cooldown
    scope: identity_endpoint
    base: 60s
    multiplier: 2
    max: 30m

  - when: { outcome: empty, count: { gte: 3, within: 10m } }
    action: cooldown
    scope: identity_endpoint
    base: 5m

  - when: { outcome: captcha }
    action: cooldown
    scope: identity_site
    base: 30m
    multiplier: 2
    max: 6h

  - when: { outcome: captcha, count: { gte: 3, within: 24h } }
    action: ban
    scope: identity
    duration: 12h

  - when: { outcome: auth_invalid }
    action: expire
    scope: identity

  - when: { outcome: banned }
    action: ban
    scope: account
    duration: permanent

  - when: { outcome: proxy_error }
    action: cooldown
    scope: proxy_site
    base: 2m
    max: 30m

escalation:                     # 升级阶梯，施加临时封禁时评估
  - when: { bans: { gte: 2, within: 7d } }
    duration: 72h
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
```

- 与识别规则不同，处置规则会评估所有匹配项，再按主体取最严重的处置。
- `count` 条件基于"主体 × 结果分类"的滑动窗口计数。
- 影子模式只记录"将会执行的处置"，不实际生效，用于新规则上线前评估误封率。

### 8.5 人工处置与误封回滚

- 控制台和管理 API 支持：指定层级与时长的冷却、临时封禁、永久封禁、解封、隔离、停用、启用、归档、重置统计。
- 支持按筛选条件批量操作，例如解封某个时间段内被某条规则封禁的全部身份，用于规则误触发后的回滚。
- 解封时可选择是否同时重置连续失败次数和健康分。
- 每次处置写入状态事件：主体、前后状态、处置类型、触发规则与上报 ID 或操作人、时间。

## 9. 健康评分与生命周期

每个身份在每个端点组维护一个 0–100 的健康分，按指数加权移动平均（EWMA）更新，并随空闲时间向基线回归；健康分决定选择权重和自动隔离。

### 9.1 计算方式

- 观测值：`success` 100、`empty` 60、`network_error`（归因身份时）50、`rate_limited` 30、`forbidden` 10、`captcha` 0；其余分类不影响身份分。
- 更新：`score = α × v + (1 - α) × score`，默认 α = 0.1，约最近 20 次请求起主要作用。
- 时间回归：读取时惰性计算 `score = baseline + (score - baseline) × e^(-Δt / τ)`，默认 baseline 为 70、τ 为 6 小时。
- 新身份和载荷刚更新的身份从 baseline 起算。
- 身份全局分为各端点组分数按请求量加权的平均值，用于展示和全局隔离判断。
- 代理按"代理 × 站点"使用相同算法，只计入归因于代理的结果和健康检查结果。

### 9.2 分数的作用

- 选择权重：`w = max(score, 5)^2`，高分身份更常被选中，低分身份不会完全饿死。
- 端点组低分：某端点组分数低于 15 且样本不少于 10 时，只对该端点组施加 6 小时冷却。
- 全局隔离：全局分低于 20 且样本不少于 10 时，身份进入 `quarantined`。
- 以上阈值均可在处置策略中配置。

### 9.3 身份生命周期

```mermaid
stateDiagram-v2
    [*] --> pending: 导入（probe）
    [*] --> active: 导入（immediate）
    pending --> active: 验证成功
    pending --> expired: 验证失败
    active --> expired: 登录态失效
    active --> banned: 封禁
    active --> quarantined: 低分或人工隔离
    banned --> pending: 临时封禁到期
    banned --> active: 人工解封
    quarantined --> pending: 隔离期满
    quarantined --> active: 人工恢复
    expired --> pending: 更新载荷
    active --> disabled: 人工停用
    disabled --> active: 人工启用
```

| 状态 | 参与调度 | 说明 |
| --- | --- | --- |
| `pending` | 低权重 | 待验证：新导入、载荷刚更新、封禁或隔离期满 |
| `active` | 是 | 正常可用，端点组级冷却不改变此状态 |
| `expired` | 否 | 凭证失效，等待刷新 |
| `banned` | 否 | 临时封禁（有截止时间）或永久封禁 |
| `quarantined` | 否 | 低分或可疑，等待人工处理或隔离期满 |
| `disabled` | 否 | 人工停用 |
| `retired` | 否 | 已归档，保留历史，默认列表不显示 |

`pending` 身份验证失败时，按处置规则落入 `expired`、`banned` 或 `quarantined`。除 `active` 外的状态都可以人工归档为 `retired`。

代理使用相同的状态集合，另有 `dead` 表示健康检查连续失败，见第 11 节。

## 10. 熔断

熔断以"站点 × 端点组"为单位：滑动窗口内风控类结果比例过高，或出现验证码的不同身份过多时打开，所有节点暂停请求该端点组，避免整批身份在几分钟内被烧掉。

### 10.1 窗口统计

- 默认窗口 60 秒，由 12 个 5 秒桶组成，每桶累计请求总数、成功数、风控类结果数，以及出现验证码的不同身份数（HyperLogLog）。
- 窗口样本少于 `min_requests`（默认 50）时不做判断，避免小流量误触发。
- 评估任务每 5 秒运行一次，Worker 处理风控类结果时也会即时检查。

### 10.2 触发条件

满足任一条件即打开：

| 条件 | 默认阈值 | 用意 |
| --- | --- | --- |
| 风控类结果比例 | ≥ 40% | 接口整体收紧 |
| 出现验证码的不同身份数 | ≥ 10 | 区分"个别坏身份"和"接口级风控" |
| 成功率 | ≤ 20% | 兜底，包括目标站故障 |

### 10.3 三态模型

```mermaid
stateDiagram-v2
    [*] --> closed
    closed --> open: 满足触发条件
    open --> half_open: 打开时长到期
    half_open --> closed: 探测成功率达标
    half_open --> open: 探测失败，时长翻倍
    closed --> open: 人工打开
    open --> closed: 人工关闭
```

- 打开时长：`open_duration × 2^(连续打开次数 - 1)`，默认从 2 分钟起，最长 1 小时；关闭状态持续 30 分钟后，连续打开次数清零。
- 半开：每 10 秒最多放行 5 个探测租约，从健康分较高的身份中选择，避免把身份问题误判为接口问题。
- 探测样本不少于 5 且成功率不低于 80% 时关闭，否则重新打开。

### 10.4 熔断的影响

- Acquire 直接返回 `unavailable`，响应头带 `Spinneret-Reason: circuit_open` 和建议等待时间。
- 熔断状态写入只读配置 `_runtime/breakers`，持有长租约或粘性会话的节点通过配置监听在 1 秒内感知。
- 熔断打开期间的上报只计入统计，不再触发身份处置，避免接口级风控时误伤全部身份。
- 可选开启 `revert_recent_cooldowns`：打开时撤销本窗口内因风控类结果施加的身份冷却（不撤销封禁），默认关闭。
- 打开、再次打开和关闭都会发送告警。

### 10.5 手动干预

- 控制台可手动打开或强制关闭任一端点组的熔断，手动打开可设时长或无限期。
- 站点总开关可一键暂停站点下全部端点组，用于目标站大改版或紧急止损。

### 10.6 配置示例

```yaml
name: search-breaker
bind: { site: shop, endpoint_group: search }
window: 60s
min_requests: 50
trip:
  risk_ratio_gte: 0.4
  distinct_captcha_identities_gte: 10
  success_ratio_lte: 0.2
open_duration: 2m
max_open_duration: 1h
half_open:
  probe_leases_per_10s: 5
  close_min_samples: 5
  close_success_ratio_gte: 0.8
revert_recent_cooldowns: false
```

## 11. 代理分发

代理在领取身份时一并分配，支持不分配、按池分配、按身份绑定、按地区匹配四种模式，并独立维护"代理 × 站点"的冷却和健康分。

### 11.1 代理模型

| 字段 | 说明 |
| --- | --- |
| `url` | 支持 `http`、`https`、`socks5`，认证信息加密存储 |
| `kind` | `datacenter` 机房、`residential` 住宅、`mobile` 移动、`tunnel` 隧道 |
| `region` / `city` | 出口地区，手动填写或由健康检查识别 |
| `provider` | 供应商，用于分组统计 |
| `max_concurrency` | 最大并发租约数，隧道代理通常设得较大 |
| `tags` | 自定义标签，如 `premium`、`us-east` |
| `session_template` | 隧道代理的会话保持模板，如在用户名中注入会话 ID |

隧道代理是"一个入口对应供应商侧的动态 IP 池"，Spinneret 把它当作一个高并发代理管理。供应商支持会话保持时，可用 `session_template` 为每个身份生成固定会话参数，例如 `user-{username}-session-{identity_hash}`。

### 11.2 分配模式

| 模式 | 行为 | 适用场景 |
| --- | --- | --- |
| `none` | 不分配代理 | 节点自带出口，或 API 类凭证 |
| `pool` | 每次从符合条件的代理中按健康分加权选择 | 身份与出口 IP 无需保持一致 |
| `bind_identity` | 身份首次领取时绑定代理，之后固定使用 | Cookie 需要稳定出口，降低异地登录风险 |
| `region_match` | 只选择与身份 `region` 一致的代理，可与绑定模式组合 | 账号有注册地区属性 |

- 过滤条件：`kinds`、`tags`、`providers`、`regions`，在轮换策略的 `proxy` 段中配置。
- 换绑：绑定的代理死亡或被封禁时自动换绑；短时冷却（默认不超过 5 分钟）期间身份一同等待，更长则换绑。
- 换绑限频：每个身份每天最多换绑 3 次，避免频繁切换 IP 本身成为风控特征。

### 11.3 健康检查

- 默认每 60 秒对未停用的代理探测一次：经代理请求可配置的探测地址，记录连通性和延迟。
- 可选出口识别：从探测结果取出口 IP，结合离线 GeoIP 库（如 DB-IP Lite、GeoLite2）识别地区。
- 连续失败 3 次标记为 `dead`，之后按指数间隔继续探测，成功即恢复。
- 探测任务按代理 ID 分片到各服务实例执行，避免重复探测。

### 11.4 冷却与统计

- 冷却层级为"代理 × 站点"和"代理全局"，规则写在处置策略中，见第 8 节。
- 按供应商与站点汇总成功率、风控率和平均延迟，用于评估供应商质量。
- 可用代理数量低于低水位时告警。

### 11.5 导入

- v0.1：控制台或 API 批量导入，每行一个代理 URL，可附带类型、地区和标签。
- v0.2：供应商适配器插件，定时从供应商 API 拉取和释放 IP。

## 12. 配置下发

配置按"命名空间 / 分组 / 键"组织，每次发布生成不可变版本；节点通过长轮询在 1 秒内感知变更，配置中可以引用 Vault 密钥。

### 12.1 数据模型

- 定位：命名空间 + 分组 + 键，例如 `prod / crawler / search.json`。
- 格式：`json`、`yaml`、`text`；JSON 与 YAML 可绑定 JSON Schema，发布前校验。
- 版本：每次发布版本号加 1，记录完整内容、发布人、备注和时间，版本不可修改。
- 草稿：编辑内容先存为草稿，确认 diff 后再发布。

### 12.2 发布与回滚

- 发布：写入新版本并更新当前版本指针，通过 Pub/Sub 通知所有实例，唤醒挂起的长轮询。
- 回滚：以历史版本的内容发布一个新版本，版本号继续递增，历史保持线性。
- 权限：发布需要 `config:publish`；双人审核和灰度发布在 v0.2 实现。

### 12.3 长轮询监听

```http
POST /spinneret.v1.ConfigService/WatchConfig

{
  "namespace": "prod",
  "items": [
    { "group": "crawler", "key": "search.json", "version": 12 },
    { "group": "_runtime", "key": "breakers", "version": 348 }
  ],
  "timeout_ms": 30000
}
```

- 服务端比较版本号，有变化立即返回变化项的最新内容；无变化则挂起到超时，返回空列表。
- `timeout_ms` 上限 60000；SDK 默认 30000，返回后立即发起下一轮。
- 挂起期间每个请求只占一个 goroutine 和少量内存，单实例目标支持 1 万个并发监听。

### 12.4 本地快照

- SDK 把最近一次成功获取的配置写入本地目录（默认 `~/.spinneret/cache/`），服务不可用时用快照启动。
- 含密钥引用的配置默认只缓存在内存；显式开启 `cache_secrets` 后，用节点令牌派生的密钥加密落盘。

### 12.5 密钥引用

- 配置内容中写 `${secret:signing/api_key}`，节点读取时由服务端解析为明文。
- 节点令牌必须拥有对应路径的读取权限，否则整个请求返回 `permission_denied` 并写入审计。
- 控制台始终展示引用原文，不展示解析后的明文。

### 12.6 内置运行时分组

`_runtime` 分组由系统维护、只读，包含 `breakers`（各端点组熔断状态）和 `site_switches`（站点总开关）。节点订阅后，即使暂时不领取身份，也能及时收到暂停指令。

## 13. Vault 密钥管理

Vault 用信封加密保存所有敏感数据：每条密钥和每份身份载荷使用独立的数据密钥（DEK）加密，DEK 再由主密钥（KEK）加密，轮换主密钥时只需重新加密 DEK。

### 13.1 加密层级

| 层级 | 生成方式 | 存放位置 | 用途 |
| --- | --- | --- | --- |
| KEK 主密钥 | 部署时生成 32 字节随机数，或由外部 KMS 托管 | 本地文件、环境变量或外部 KMS，不进入数据库 | 加密 DEK |
| DEK 数据密钥 | 每条记录随机生成 32 字节 | 经 KEK 加密后与密文存在同一行 | 加密数据 |
| 密文 | AES-256-GCM，随机 12 字节 nonce | PostgreSQL | 存储数据 |

加密时以"记录 ID + 字段名"作为 GCM 附加认证数据（AAD），防止密文被挪到其他记录上解密。

### 13.2 KEK 提供方

- v0.1 提供 `local`：从文件或环境变量读取，支持同时配置多把，按 `kek_id` 解密旧数据。
- v0.2 以插件形式支持 OpenBao Transit、AWS KMS、GCP KMS 等。
- 轮换流程：新增 KEK 并设为当前 → 后台任务逐条重新加密 DEK → 全部完成后下线旧 KEK。

### 13.3 密钥对象

- 路径式命名：`<命名空间>/<路径>`，例如 `prod/signing/api_key`。
- 多版本：更新时生成新版本，读取默认取最新版本，也可指定版本。
- 元数据：描述、标签、创建人、最近访问时间、过期时间（到期前告警）。

### 13.4 访问控制与审计

- 节点令牌按路径通配授权，例如 `secret:read:prod/signing/*`。
- 控制台默认脱敏，只显示末 4 位；查看明文需要 `secret:reveal` 权限并二次确认。
- 每次读取密钥写审计：主体、路径、版本、来源 IP、时间、结果。
- 身份载荷的领取量很大，不逐条审计，改为按分钟聚合记录"令牌 × 身份类型 × 次数"。

### 13.5 性能与取舍

- 解密后的 DEK 在进程内缓存 10 分钟，LRU 上限 10 万条，只存在内存中。
- 身份载荷解密一次后，按"身份 ID + 载荷版本"缓存交付结构，载荷更新即失效；可设置 `cache_payloads: false` 关闭。
- AES-256-GCM 在支持 AES-NI 的 CPU 上解密 KB 级数据只需微秒级，对领取延迟影响可以忽略。

## 14. 认证与权限

节点使用长期 API 令牌，控制台用户使用账号密码登录；两者都按作用域授权，并限定在命名空间内。

### 14.1 节点令牌

- 格式：`spn_` 前缀加 32 字节随机数的 base62 编码，前缀便于 gitleaks 等工具识别泄露。
- 存储：服务端只保存 SHA-256 哈希和前 8 位用于识别，明文只在创建时显示一次。
- 使用：请求头 `Authorization: Bearer spn_xxx`。
- 属性：名称、命名空间、作用域、IP 白名单（可选）、过期时间（可选）、最近使用时间与 IP。
- 吊销：立即生效，通过 Pub/Sub 通知各实例清除缓存；校验结果在进程内缓存 30 秒。

### 14.2 作用域

| 作用域 | 允许的操作 |
| --- | --- |
| `lease:acquire[:站点]` | 领取、续租、释放租约，可限定站点 |
| `report:write[:站点]` | 上报请求结果 |
| `config:read[:分组]` | 读取与监听配置 |
| `config:publish[:分组]` | 发布与回滚配置 |
| `secret:read:<路径>` | 读取密钥，支持通配 |
| `identity:write[:站点]` | 导入与更新身份载荷，供刷新服务使用 |
| `proxy:write` | 导入与更新代理 |
| `admin` | 全部管理接口，不建议发给节点 |

推荐组合：爬虫节点使用 `lease:acquire` + `report:write` + `config:read`；Cookie 刷新服务使用 `identity:write`；CI 发布脚本使用 `config:publish`。

### 14.3 控制台账号与角色

| 角色 | 权限 |
| --- | --- |
| `owner` | 全部权限，包括用户管理与 KEK 轮换 |
| `admin` | 命名空间内全部资源的管理 |
| `operator` | 身份与代理的日常操作，策略与配置草稿编辑 |
| `viewer` | 只读，不能查看密钥明文 |

- `config:publish` 和 `secret:reveal` 是可以单独授予的附加权限。
- 密码使用 Argon2id 哈希，登录失败按账号和 IP 限速。
- 会话使用 HttpOnly、Secure、SameSite=Strict 的 Cookie，服务端会话存 Redis，默认 12 小时。
- 首个 owner 账号通过 `spnr admin init` 命令创建，系统不提供默认密码。
- v0.2 支持 TOTP 二次验证与 OIDC 单点登录。

### 14.4 传输安全

- 推荐部署在 WireGuard 等内网中，服务只监听内网地址。
- 暴露到公网时必须启用 TLS，可由服务端直接加载证书，或由反向代理终止 TLS。
- v0.2 支持可选的 mTLS 节点认证。

## 15. API 设计

所有接口用 Protobuf 定义并通过 ConnectRPC 提供，下游用普通 HTTP POST + JSON 即可调用；节点侧共 4 个服务，其余为管理服务。

### 15.1 协议约定

- 路径：`POST /spinneret.v1.<服务>/<方法>`，`Content-Type: application/json`；Go 节点也可使用 gRPC。
- 字段名使用 snake\_case，零值字段始终输出。
- 时间使用 RFC 3339 字符串；时长统一用毫秒整数字段（如 `wait_ms`），避免 int64 被序列化为字符串。
- 分类与状态字段在 proto 中用 `string` 并由 protovalidate 限定取值，JSON 中是 `"rate_limited"` 这类小写值，而不是枚举形式的 `OUTCOME_RATE_LIMITED`。
- 节点标识：请求头 `X-Spinneret-Node` 传节点实例名（如主机名或 Pod 名），用于按节点统计。
- 结构化错误原因放在响应头 `Spinneret-Reason` 与 `Spinneret-Retry-After-Ms`，因为 Connect 的错误详情是 base64 编码的 Protobuf，纯 JSON 客户端不便读取。
- 面向下游的接口不使用流式调用；配置监听用长轮询，控制台实时事件用 SSE。

### 15.2 服务列表

| 服务 | 方法 | 调用方 |
| --- | --- | --- |
| `LeaseService` | `Acquire`、`AcquireBatch`、`Renew`、`Release` | 节点 |
| `ReportService` | `Report` | 节点 |
| `ConfigService` | `GetConfig`、`BatchGetConfig`、`WatchConfig` | 节点 |
| `SecretService` | `GetSecret` | 节点 |
| `IdentityAdminService` | 身份类型与身份的增删改查、导入、载荷更新、状态操作、批量操作、事件时间线 | 控制台、刷新服务 |
| `ProxyAdminService` | 代理增删改查、导入、状态操作、供应商统计 | 控制台 |
| `SiteAdminService` | 站点、端点组、URI 规则、URI 测试 | 控制台 |
| `PolicyAdminService` | 四类策略的版本管理、绑定、规则调试 | 控制台 |
| `BreakerAdminService` | 熔断状态查询、手动打开与关闭、站点总开关 | 控制台 |
| `ConfigAdminService` | 草稿、发布、回滚、版本对比 | 控制台、CI |
| `SecretAdminService` | 密钥管理、KEK 轮换 | 控制台 |
| `AccessAdminService` | 节点令牌、用户、角色 | 控制台 |
| `DashboardService` | 统计查询 | 控制台 |

### 15.3 Acquire

请求：

```http
POST /spinneret.v1.LeaseService/Acquire
Authorization: Bearer spn_xxx
X-Spinneret-Node: crawler-hk-03
Content-Type: application/json

{
  "site": "shop",
  "client": "web",
  "uri": "/api/v1/search",
  "session_key": "task-8842",
  "wait_ms": 0
}
```

响应：

```json
{
  "lease": {
    "lease_id": "lse_0192a3f4c1d27b8e9a01",
    "identity_id": "idt_0192a3f0aa51f2c3d4e5",
    "identity_type": "web_cookie",
    "endpoint_group": "search",
    "expires_at": "2026-09-16T08:32:10Z",
    "sticky": true
  },
  "credential": {
    "cookies": { "sessionid": "a1b2c3", "csrf_token": "d4e5f6" },
    "cookie_header": "sessionid=a1b2c3; csrf_token=d4e5f6",
    "headers": { "User-Agent": "Mozilla/5.0 ..." },
    "query": {},
    "json": null,
    "values": { "signature": "x9y8z7" }
  },
  "proxy": {
    "proxy_id": "pxy_0192a1b2c3d4e5f6a7b8",
    "url": "http://user:pass@203.0.113.10:8000",
    "kind": "residential",
    "region": "US"
  },
  "hints": {
    "renew_before_ms": 30000
  }
}
```

### 15.4 Report

```http
POST /spinneret.v1.ReportService/Report
Authorization: Bearer spn_xxx
Content-Type: application/json

{
  "reports": [
    {
      "report_id": "2c6f0e5a-8d8b-4a52-9f0e-1b7f3c9d2a41",
      "lease_id": "lse_0192a3f4c1d27b8e9a01",
      "uri": "/api/v1/search",
      "method": "GET",
      "http_status": 200,
      "markers": [],
      "latency_ms": 842,
      "response_bytes": 48213,
      "started_at": "2026-09-16T08:30:11.120Z",
      "finished_at": "2026-09-16T08:30:11.962Z",
      "release": true
    }
  ]
}
```

响应为 `{"accepted": 1, "duplicated": 0, "rejected": []}`，被拒条目会带上 `report_id` 和原因，如 `lease_unknown`。

### 15.5 Renew、Release 与 AcquireBatch

- `Renew`：`{"lease_id": "...", "extend_ms": 120000}`，返回新的 `expires_at`；单个租约累计时长默认上限 30 分钟。
- `Release`：`{"lease_id": "..."}`；通常不必单独调用，最后一次上报带 `release: true` 即可。
- `AcquireBatch`：在 Acquire 参数上增加 `count`（上限 50），返回互不相同身份的租约列表；数量不足时返回已领取的部分。

### 15.6 错误码

| Connect 错误码 | HTTP | Spinneret-Reason | 节点应对 |
| --- | --- | --- | --- |
| `unauthenticated` | 401 | `token_invalid` | 停止并告警 |
| `permission_denied` | 403 | `scope_missing` | 停止并告警 |
| `invalid_argument` | 400 | `site_unknown`、`uri_invalid` | 修正调用代码 |
| `resource_exhausted` | 429 | `no_identity_available` | 按建议时间等待后重试 |
| `unavailable` | 503 | `circuit_open`、`site_paused` | 暂停该端点组或站点 |
| `not_found` | 404 | `lease_unknown` | 重新领取 |
| `failed_precondition` | 400 | `lease_released` | 重新领取 |

```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 1200

{"code": "resource_exhausted", "message": "no identity available for shop/web/search"}
```

### 15.7 Proto 草案

```protobuf
syntax = "proto3";

package spinneret.v1;

import "buf/validate/validate.proto";
import "google/protobuf/struct.proto";
import "google/protobuf/timestamp.proto";

service LeaseService {
  rpc Acquire(AcquireRequest) returns (AcquireResponse);
  rpc AcquireBatch(AcquireBatchRequest) returns (AcquireBatchResponse);
  rpc Renew(RenewRequest) returns (RenewResponse);
  rpc Release(ReleaseRequest) returns (ReleaseResponse);
}

message AcquireRequest {
  string site = 1 [(buf.validate.field).string.min_len = 1];
  string client = 2 [(buf.validate.field).string.min_len = 1];
  string uri = 3;             // 与 endpoint_group 二选一
  string endpoint_group = 4;
  string session_key = 5;     // 可选，粘性会话键
  int32 wait_ms = 6 [(buf.validate.field).int32 = {gte: 0, lte: 5000}];
}

message AcquireResponse {
  Lease lease = 1;
  Credential credential = 2;
  Proxy proxy = 3;
  Hints hints = 4;
}

message Lease {
  string lease_id = 1;
  string identity_id = 2;
  string identity_type = 3;
  string endpoint_group = 4;
  google.protobuf.Timestamp expires_at = 5;
  bool sticky = 6;
}

message Credential {
  map<string, string> cookies = 1;
  string cookie_header = 2;
  map<string, string> headers = 3;
  map<string, string> query = 4;
  google.protobuf.Value json = 5;
  google.protobuf.Struct values = 6;
}
```

## 16. 数据存储设计

PostgreSQL 保存需要备份的真相数据，Redis 保存可重建的调度热状态；上报事件经 Redis Streams 分片进入 Worker，状态变更再批量写回 PostgreSQL。

### 16.1 PostgreSQL 主要表

| 表 | 主要字段 | 说明 |
| --- | --- | --- |
| `sites`、`endpoint_groups`、`uri_rules` | 名称、客户端类型、规则类型、模式、优先级、低水位 | 站点与接口结构 |
| `identity_types` | 字段模板、JSON Schema、交付映射、去重键、激活方式 | 身份类型 |
| `accounts` | 外部引用、地区、标签 | 账号 |
| `identities` | 类型、账号、状态、封禁截止、地区、标签、载荷版本 | 身份元数据 |
| `identity_payloads` | 身份、版本、密文、加密后的 DEK、kek\_id | 加密载荷，多版本 |
| `proxies`、`proxy_bindings` | 类型、地区、供应商、加密 URL、并发上限、状态；绑定时间、当日换绑次数 | 代理与绑定 |
| `policies`、`policy_bindings` | 策略种类、名称、版本、规格（jsonb）、绑定层级 | 四类策略，版本化 |
| `hot_state_snapshots` | 主体、端点组、健康分、连续失败次数、冷却截止 | 热状态快照，用于重建 |
| `state_events` | 主体、前后状态、处置、规则、上报 ID、操作人、时间 | 状态变更记录，按月分区 |
| `risk_events` | 上报字段、结果分类、归因 | 非成功上报明细，按天分区 |
| `outcome_stats_minutely` | 分钟、站点、端点组、代理、结果分类、次数、耗时合计 | 分钟级聚合，按天分区 |
| `identity_stats_hourly` | 小时、身份、端点组、结果分类、次数 | 身份维度小时聚合 |
| `breaker_events` | 端点组、前后状态、触发原因、窗口指标 | 熔断记录 |
| `config_items`、`config_versions` | 分组、键、版本、内容、格式、Schema、发布人 | 配置 |
| `secrets`、`secret_versions` | 路径、版本、密文、加密后的 DEK、kek\_id、过期时间 | 密钥 |
| `api_tokens`、`users`、`role_bindings` | 令牌哈希、作用域、白名单；用户名、密码哈希；角色 | 访问控制 |
| `audit_logs` | 操作者、动作、资源、详情、IP、时间 | 审计，按月分区 |

成功上报不保存明细，只进入聚合。默认保留期：风险明细与分钟聚合 30 天，小时聚合 180 天，状态事件与审计 1 年，到期直接删除分区。

### 16.2 Redis 键设计

站点相关的键使用哈希标签 `{s}`（命名空间 + 站点），同一站点的热状态落在同一个 slot，Lua 脚本在 Redis Cluster 下也能原子执行。

| 键 | 类型 | 内容 |
| --- | --- | --- |
| `sp:{s}:rdy:<端点组>` | ZSET | 身份 → 在该端点组的可用时间（毫秒） |
| `sp:{s}:hs:<端点组>` | HASH | 身份 → 健康分、更新时间、连续失败次数、冷却截止 |
| `sp:{s}:id:<身份>` | HASH | 生命周期状态、封禁截止、站点级冷却与复用截止、活跃租约数、账号、载荷版本 |
| `sp:{s}:acc:<账号>` | HASH | 账号级封禁与冷却截止 |
| `sp:{s}:ls:<租约>` | HASH + TTL | 身份、代理、端点组、节点、会话键、领取与到期时间 |
| `sp:{s}:lsexp` | ZSET | 租约 → 到期时间，供回收任务扫描 |
| `sp:{s}:stk:<端点组>:<会话键>` | STRING + TTL | 粘性会话绑定的身份 |
| `sp:{s}:q:<端点组>:<身份>` | HASH + TTL | 配额的当前窗口与上一窗口计数 |
| `sp:{s}:px:<代理>` | HASH | 代理在该站点的状态、冷却截止、健康分、活跃租约数 |
| `sp:{s}:pxrdy` | ZSET | 代理 → 在该站点的可用时间 |
| `sp:{s}:win:<端点组>:<桶>` | HASH + TTL | 熔断窗口计数 |
| `sp:{s}:brk:<端点组>` | HASH | 熔断状态、打开时间、连续打开次数、半开令牌 |
| `sp:{s}:cnt:<主体>:<分类>` | HASH + TTL | 处置规则 `count` 条件的窗口计数 |
| `sp:{rpt:<分片>}:stream` | STREAM | 上报事件队列，默认 16 个分片 |
| `sp:{rpt:<分片>}:dedup:<上报ID>` | STRING + TTL | 上报去重，保留 1 小时 |

- 代理的全局状态（死亡、停用）变化时，由服务端同步到各站点的 `px` 键，避免 Lua 脚本跨 slot 访问。
- 单个超大站点会集中在一个 slot；需要时可把身份按哈希拆成多个子池（`{s:0}` 到 `{s:N}`），领取时轮流尝试。

### 16.3 上报处理管道

```mermaid
flowchart LR
    A[Report 接口] --> B[Lua 去重并入队<br/>按分片 XADD]
    B --> C[Worker 读取<br/>XREADGROUP]
    C --> D[识别规则<br/>Go 内存执行]
    D --> E[Lua 更新健康分<br/>冷却 计数 窗口]
    E --> F[处置与熔断判定]
    F --> G[批量写入<br/>PostgreSQL]
    F --> H[Pub/Sub 通知<br/>实例与控制台]
```

- 去重与入队在同一个 Lua 脚本中完成（两个键共享分片哈希标签），避免去重成功但入队失败。
- 每个分片由一个实例独占消费（通过 Redis 锁分配），保证同一身份的上报按顺序处理；实例增减时重新分配。
- Worker 每次批量读取 100 条，处理完成后 `XACK`；实例崩溃时，接管者用 `XAUTOCLAIM` 取回未确认消息，状态脚本再按 `report_id` 二次幂等。
- 聚合数据在内存中按分钟累积，每 10 秒用 COPY 批量写入 PostgreSQL。

### 16.4 热状态重建

- 触发：启动时发现 Redis 缺少 `sp:meta:epoch` 标记，或管理员执行 `spnr rebuild`。
- 过程：从 PostgreSQL 读取非归档的身份、代理、策略和生命周期状态，重建可用队列与全局状态。
- 快照：每 60 秒把健康分、连续失败次数和冷却截止写入 `hot_state_snapshots`，重建时据此恢复。
- 冷启动保护：重建后所有身份的可用时间在 0–60 秒内随机打散，防止所有节点同时涌向目标站。
- 建议 Redis 开启 AOF（`appendfsync everysec`），降低需要重建的概率。

## 17. Web 控制台

控制台与服务端打包在同一个二进制中，v0.1 包含 12 个页面，覆盖日常运营中的查看、配置和人工处置。

| 页面 | 主要功能 |
| --- | --- |
| 总览 | 各站点可用身份数、领取与上报 QPS、成功率与风控率趋势、熔断状态、未知分类占比、低水位告警 |
| 身份 | 虚拟滚动大表格，按状态、类型、标签、健康分、账号筛选；批量冷却、封禁、解封、停用、归档 |
| 身份详情 | 脱敏载荷、各端点组健康分与冷却、绑定代理、状态事件时间线、最近风险事件 |
| 身份类型 | 字段模板与 Schema 编辑；交付预览：输入样例载荷，展示 Acquire 返回结构 |
| 代理 | 代理列表、健康检查结果、按站点的冷却与分数、供应商统计、批量导入 |
| 站点与端点组 | 站点、客户端类型、端点组与 URI 规则管理，内置 URI 测试器 |
| 策略 | 表单与 YAML 双模式编辑、版本对比、绑定关系；规则调试器：粘贴一条上报，显示命中的识别规则、结果分类和将触发的处置 |
| 冷却热力图 | 身份 × 端点组矩阵，颜色表示冷却剩余时间或健康分，可下钻到身份详情 |
| 熔断 | 各端点组状态与窗口指标、打开历史、手动打开与关闭、站点总开关 |
| 配置中心 | 配置树、Monaco 编辑器、Schema 校验、草稿与发布、版本 diff、回滚 |
| 密钥 | 路径树、版本、脱敏展示、明文查看（需权限与二次确认）、访问记录 |
| 访问与审计 | 节点令牌、用户与角色、审计日志检索 |

- 技术栈：React + TypeScript + Vite，TanStack Router / Query / Table / Virtual，shadcn/ui + Tailwind CSS，ECharts，Monaco Editor，connect-es。
- 实时性：熔断状态和处置事件通过 SSE 推送，列表数据默认每 5 秒刷新。
- 发布：构建产物通过 Go embed 打入二进制，前端路由由服务端回退到 index.html。
- 语言：v0.1 提供中文与英文界面。

## 18. 部署、运维与性能目标

最小部署只需 spinneret-server、PostgreSQL、Redis（或 Valkey）三个组件；服务实例无状态，可直接水平扩展。

### 18.1 Docker Compose

```yaml
services:
  spinneret:
    image: ghcr.io/<org>/spinneret:0.1
    ports: ["8080:8080"]
    environment:
      SPINNERET_DATABASE_URL: postgres://spinneret:${PG_PASSWORD}@postgres:5432/spinneret
      SPINNERET_REDIS_URL: redis://valkey:6379/0
      SPINNERET_KEK_FILE: /run/secrets/kek
    secrets: [kek]
    depends_on: [postgres, valkey]

  postgres:
    image: postgres:17
    environment:
      POSTGRES_USER: spinneret
      POSTGRES_PASSWORD: ${PG_PASSWORD}
    volumes: [pgdata:/var/lib/postgresql/data]

  valkey:
    image: valkey/valkey:8
    command: ["valkey-server", "--appendonly", "yes", "--appendfsync", "everysec"]
    volumes: [valkeydata:/data]

secrets:
  kek:
    file: ./kek.key    # openssl rand -base64 32 > kek.key

volumes:
  pgdata:
  valkeydata:
```

首次启动后执行 `spnr admin init` 创建 owner 账号。

### 18.2 后台任务

| 任务 | 周期 | 并发控制 |
| --- | --- | --- |
| 上报消费 | 持续 | 按 Stream 分片独占 |
| 租约回收 | 每秒 | 按站点加锁 |
| 熔断评估 | 每 5 秒 | 按端点组加锁 |
| 封禁到期与隔离期满处理 | 每 10 秒 | 单实例领导者 |
| 统计聚合写库 | 每 10 秒 | 各 Worker 各自写入 |
| 代理健康检查 | 每 60 秒 | 按代理 ID 分片 |
| 热状态快照 | 每 60 秒 | 单实例领导者 |
| 数据保留清理 | 每天 | 单实例领导者 |

领导者选举使用 PostgreSQL advisory lock，不引入额外组件。

### 18.3 监控与告警

- 健康检查端点：`/healthz` 检查存活，`/readyz` 检查 PostgreSQL 与 Redis，`/metrics` 输出 Prometheus 指标。
- 核心指标：
    - `spinneret_acquire_total{site,group,result}`、`spinneret_acquire_duration_seconds`
    - `spinneret_report_total{site,group,outcome}`、`spinneret_report_lag_seconds`
    - `spinneret_identities{site,type,state}`、`spinneret_identities_available{site,group}`
    - `spinneret_actions_total{site,action,scope}`、`spinneret_breaker_state{site,group}`
    - `spinneret_proxies{site,state}`、`spinneret_stream_pending{shard}`
- 告警渠道：通用 Webhook、飞书、钉钉、企业微信、Telegram。
- 告警事件：熔断打开、身份或代理低于低水位、封禁数突增、上报积压、未知分类占比过高、`client_error` 突增。

### 18.4 性能目标

以下为 v0.1 目标值，需在 M1 阶段压测验证。测试条件为单站点 10 万身份、50 个端点组，服务实例 4 vCPU / 8 GB，单个 Redis 实例。

| 指标 | 目标 |
| --- | --- |
| Acquire 服务端延迟 | p99 < 5 ms |
| Acquire 吞吐（单实例） | ≥ 5,000 次/秒 |
| Report 接收吞吐（单实例） | ≥ 20,000 条/秒 |
| 上报到状态更新完成 | p99 < 200 ms |
| 配置变更感知 | < 1 秒 |
| 并发长轮询（单实例） | ≥ 10,000 |

### 18.5 仓库结构

```
spinneret/
├── proto/spinneret/v1/     # 接口定义，唯一真相源
├── cmd/
│   ├── spinneret-server/   # 服务端入口
│   └── spnr/               # 管理 CLI：init、rebuild、import、token
├── internal/
│   ├── auth/               # 令牌、会话、作用域
│   ├── site/               # 站点、端点组、URI 匹配树
│   ├── identity/           # 身份类型、载荷、交付渲染
│   ├── proxy/              # 代理池、绑定、健康检查
│   ├── scheduler/          # 领取、轮换、复用间隔、配额
│   ├── signal/             # 上报接收、识别规则、归因
│   ├── action/             # 冷却、封禁、升级阶梯、健康分
│   ├── breaker/            # 熔断窗口与状态机
│   ├── config/             # 配置版本、发布、长轮询
│   ├── vault/              # 信封加密、KEK 提供方
│   ├── jobs/               # 后台任务、领导者选举
│   ├── notify/             # 告警渠道
│   └── store/
│       ├── postgres/       # sqlc 代码与迁移
│       └── redis/          # Lua 脚本与封装
├── sdk/
│   ├── python/             # httpx + pydantic，同步与异步客户端
│   └── go/
├── web/                    # React 控制台
├── deploy/                 # docker-compose，Helm（v0.2）
└── docs/
```

## 19. 里程碑与待定问题

v0.1 分四个阶段交付：先打通"领取 → 上报 → 冷却"主链路，再补齐风控闭环、基础设施和控制台。

| 阶段 | 内容 | 验收标准 |
| --- | --- | --- |
| M1 核心链路 | proto 定义、身份类型与导入、Acquire 与 Report、复用间隔、冷却、Lua 脚本、Python SDK 雏形 | 压测达到第 18 节性能目标；FastAPI 示例项目跑通领取与上报 |
| M2 风控闭环 | 识别规则、处置规则、封禁与升级阶梯、健康分、生命周期、熔断、影子模式 | 模拟 429 与验证码场景，冷却、封禁、熔断按配置生效 |
| M3 基础设施 | 配置中心、Vault、令牌与作用域、代理池与绑定、健康检查、热状态重建 | 节点仅凭地址和令牌完成启动、拉取配置和领取 |
| M4 控制台与发布 | 12 个控制台页面、告警、docker-compose、使用文档 | 新用户 15 分钟内完成部署并跑通示例 |

v0.2 候选：分布式全局限速、外部校验器与刷新器 Webhook、代理供应商适配器、ClickHouse 原始事件、NATS JetStream、OIDC 与 TOTP、mTLS、配置灰度发布、指纹配置分发、浏览器池。

### 待定问题

- [ ] 是否需要多团队共用一套部署的多租户，还是命名空间隔离已经足够？
  - 需要多租户，同事之间和管理之间都需要做权限隔离以及项目级别的资源隔离，比如负责平台A爬虫的租户或团队只能看到平台A的站点、端点组、身份类型、策略等资源，不能看到平台B的资源。
- [ ] 冷却与复用间隔的最小粒度定为端点组；需要精确到单个 URI 时，直接为它建一个端点组是否可行？
  - 可行，URI 规则的优先级高于端点组，建一个端点组只影响该 URI 的领取。
- [ ] 身份载荷明文缓存在服务进程内存中是否可以接受，还是默认关闭？
  - 可以，项目是部署在内网中，服务端进程内存泄露的风险很低；默认开启，提供 `cache_payloads: false` 关闭选项。
- [ ] 熔断打开时，是否默认撤销本窗口内施加的身份冷却？
  - 这个按照你的推荐来，但是你可以带入自己是爬虫节点的角度来考虑。
- [ ] 成功请求是否需要保存明细，还是只保留聚合数据？
  - 保留，可以使用Clickhouse来做。
- [ ] 开源许可证选择 Apache-2.0 还是 AGPL-3.0？
  - 待定，暂时无协议。
- [ ] Python SDK 的包名 `spinneret` 在 PyPI 上是否可用？
  - 待定，先预留。
