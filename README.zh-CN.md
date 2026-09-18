<h1 align="center">Spinneret</h1>

<p align="center"><em>为共用同一批稀缺、限速凭据与出口的节点集群提供控制平面。</em></p>

<div align="center">

[English](./README.md) | [简体中文](./README.zh-CN.md)

你的节点在每次请求前申请一个凭据和一个出口，用完之后把发生了什么上报回来。
Spinneret 在数秒内把这些上报变成冷却、封禁、健康分和熔断——并把版本化的配置与密钥分发给同一批节点。

一个 Go 二进制，加上 PostgreSQL 和 Valkey——如果你要用请求明细，再加一个 ClickHouse，默认栈会替你把它拉起来。
节点侧不需要 agent，也不需要 sidecar：一个服务端地址和一个令牌就是全部节点配置；SDK 只是普通的库，
直接用 HTTP 一样好使。

[![License](https://img.shields.io/github/license/TikHub/Spinneret?style=flat-square)](LICENSE)
[![Release](https://img.shields.io/github/v/release/TikHub/Spinneret?style=flat-square)](https://github.com/TikHub/Spinneret/releases/latest)
[![Stars](https://img.shields.io/github/stars/TikHub/Spinneret?style=flat-square)](https://github.com/TikHub/Spinneret/stargazers)
[![Forks](https://img.shields.io/github/forks/TikHub/Spinneret?style=flat-square)](https://github.com/TikHub/Spinneret/forks)
[![Issues](https://img.shields.io/github/issues/TikHub/Spinneret?style=flat-square)](https://github.com/TikHub/Spinneret/issues)
<br>
[![CI](https://img.shields.io/github/actions/workflow/status/TikHub/Spinneret/ci.yml?branch=main&style=flat-square&label=CI)](https://github.com/TikHub/Spinneret/actions/workflows/ci.yml)
[![Release build](https://img.shields.io/github/actions/workflow/status/TikHub/Spinneret/release.yml?style=flat-square&label=release%20build)](https://github.com/TikHub/Spinneret/actions/workflows/release.yml)
[![Last commit](https://img.shields.io/github/last-commit/TikHub/Spinneret?style=flat-square&label=last%20commit)](https://github.com/TikHub/Spinneret/commits/main)
<br>
[![Go](https://img.shields.io/github/go-mod/go-version/TikHub/Spinneret?style=flat-square&logo=go&logoColor=white&label=go)](go.mod)
[![Container image](https://img.shields.io/badge/ghcr.io-tikhub%2Fspinneret-2496ed?style=flat-square&logo=docker&logoColor=white)](https://github.com/TikHub/Spinneret/pkgs/container/spinneret)
[![文档](https://img.shields.io/badge/%E6%96%87%E6%A1%A3-21%20%E9%A1%B5%20%C2%B7%20%E4%B8%AD%E6%96%87%20%2F%20EN-2f6feb?style=flat-square&logo=readthedocs&logoColor=white)](documents/README.zh-CN.md)

</div>

<div align="center">
  <img src="documents/images/overview-zh.png" width="900" alt="Spinneret 控制台：按站点的健康度、吞吐、判定结果分布与熔断器"/>
</div>

---

## 🧭 Spinneret 是什么

有些资源既不可互换，也不是无状态的。一池 API key、账号、会话、设备标识或出口地址，数量有限、速率有限，
而且——正是这一点让常规工具失效——它的可用性会随着刚才怎么用它而改变。一秒内用两次，它被限流；
用在不该用的端点上，它被挑战。连接池没有这些性质，健康检查也发现不了它们，因为唯一能告诉你一个凭据
是否还能用的探针，就是用它发出的一次真实请求。

Spinneret 就是这类资源的仲裁者。节点手里不持有任何清单。它按请求申请，拿回一个已经渲染好、可直接使用的
凭据，可选的一条出口线路，以及一份租约：一个带 TTL、可续约、可回收的占用凭证，告诉调度器此刻有多少个
节点正持有这个凭据。

```text
Acquire(site, client, uri)   →  身份 + 凭据 + 代理 + 租约
   …… 节点用它们向自己的目标发出自己的请求 ……
Report(lease_id, status, latency, markers)
                             →  服务端分类、归因、冷却、封禁、
                                评分、熔断——全集群生效，数秒之内
```

节点只上报事实，从不下判决：HTTP 状态码、业务码、传输错误类型、它在响应里识别出的最多 32 个标记
（markers）、延迟、大小、时间戳。服务端对上报做分类，决定这笔账算谁的——凭据、线路、两者都算，或者谁都
不算——并在选定的作用域上对每个对象最多执行一个动作。这一切都是写成版本化 YAML 的策略：可发布、可对比、
可回滚，数秒后生效，不需要动任何一个节点。

Spinneret 永远不在数据通路上。它从不挡在你的节点和它们要访问的系统之间，不中转任何一个字节、不做签名，
也不含登录流程或验证码处理。它自己唯一的对外流量，是周期性地通过每条出口线路去访问一个由你配置的
探测地址。它管理请求周边的状态；请求本身仍然由你的节点发出。

---

## 🎯 适用场景

Spinneret 不关心你的节点在做什么。它管的是那些不能被浪费和滥用的东西：一池凭据、一池出口，以及告诉节点
该怎么行动的配置和密钥。只要有一批节点共用一个稀缺、限速或有状态的身份池，这套机制就适用。

| 应用场景 | 支撑它的能力 |
| --- | --- |
| **调用计量型第三方 API 的集群** —— 一池 key，每个 key 有自己的配额和并发上限 | 身份字段类型 `string` / `secret_ref`，让 key 只存在保管库里；通过 `headers` 或 `query` 下发；每个 key、每个端点组上可配多个滑动窗口 `quota`；`reuse_interval`、`max_concurrent_leases`、健康分、按端点组的熔断器 |
| **出口治理** —— 每个请求从哪条线路出去，以及一条线路坏了怎么退场 | 带类型、地区、供应商与标签的命名空间代理池；`none / pool / bind_identity / region_match` 四种分配模式；按线路的并发上限；周期性健康检查；把线路的责任和凭据的责任分开归因 |
| **分布式数据采集** —— 多个节点、一池会话、会反制的目标 | 身份租约、冷却阶梯、账号级封禁、用标记扩展新的失败形态、冷却热力图、请求明细 |
| **账号绑定型自动化** —— 每个账号都是一个稀缺且有状态的身份 | 账号实体、账号作用域的封禁与冷却、会话粘性、`bind_identity` 出口亲和、七态身份状态机 |
| **共用少量真实账号的测试与 CI 集群** | `max_concurrent_leases: 1` 就是一把带 TTL 的分布式互斥锁；回收被杀死的任务所持租约的租约回收器；`max_lease_lifetime`；`wait_ms` 最多排队 5 秒；`AcquireBatch` 一次往返取最多 50 个不同身份 |
| **集群配置与密钥分发** —— 不重新部署就改变行为 | 带草稿、发布、回滚与差异对比的版本化配置项；长轮询 `WatchConfig`；`${secret:path}` 引用；支持 KEK 轮换的信封加密保管库；`config:read` 与 `secret:read` 令牌作用域 |
| **跨团队共用资源池的治理** —— 一个池、多个团队、一条审计线 | 租户 → 命名空间 → 站点；`viewer / operator / admin / owner` 四种角色，可按命名空间和站点绑定；只在一个命名空间内有效的节点令牌；每一次密钥读取都留审计 |

### Spinneret 不是什么

- **不在数据通路上。** 不拦截、不做 sidecar、不终结 TLS。如果你要的是一个坐在通路上的东西，那你需要的是
  正向代理或者服务网格。
- **不是任务队列。** 它没有工作队列、不分发任务、不对你的业务任务去重，也不存储响应。队列还是你自己的；
  Spinneret 回答的是*以谁的身份去*，不是*去做什么*。
- **它不负责获取或刷新凭据。** 没有登录流程、没有签名算法、没有会话采集，也没有任何针对特定目标站的代码。
  `expire` 动作只是把一个凭据标记为失效，好让你自己的刷新任务去替换它。
- **没有针对单个目标的全集群请求速率上限。** 限制是按身份的（`quota`、`reuse_interval`、
  `max_concurrent_leases`）和按 API 令牌的（每秒请求数，且按实例计算）。熔断器是由失败驱动的刹车，不是速率调节器。
  分布式全局限流是 v0.2 的候选项。
- **代理是作为租约的一部分分配的。** 没有「只要出口、不要凭据」的租约。
- **上报的语义是围绕 HTTP 建模的。** `uri` 是必填的，`error_kind` 是一个封闭的传输失败枚举。别的协议可以靠
  `business_code` 和标记来表达，但这套字段语义会一直不合身。
- **规模不够时它是杀鸡用牛刀。** 一个进程、几个从来不会被限流的凭据：本地信号量比这套东西划算。
  临界点是机器多于一台，**并且**凭据多到一个人记不过来，**并且**可用性会随使用而变化。

### 术语

API 和控制台通篇使用六个名词。

| 术语 | 含义 |
| --- | --- |
| **站点（site）** | 命名空间内的一个上游目标系统——一个 API、一个服务、一个平台 |
| **客户端（client）** | 访问它的一种形态（`web`、`mobile`、`partner`）；身份在不同客户端之间不可互换 |
| **端点组（endpoint group）** | 一组行为相似的端点——配额、冷却、健康分和熔断的计量单位 |
| **身份（identity）** | 一个可用的凭据或身份形象：一个 key、一个令牌、一个会话、一组 cookie、一个设备指纹 |
| **节点（node）** | 你集群里的一个工作进程 |
| **代理（proxy）** | 一条出口线路 |

---

## 🧱 系统架构

### 整体结构

一个二进制、两种角色：发放凭据与出口的请求路径，以及把上报变回状态的管道——底下是作为事实来源的
PostgreSQL、承载热路径的 Valkey，以及存放原始明细的 ClickHouse。下面是同一套进程的三个视角。

**请求路径。** 节点会调用的全部接口，一次 Redis 往返完成，中途不碰 PostgreSQL。

```mermaid
flowchart LR
    subgraph FLEET["你的集群"]
        direction TB
        SDKGO["Go SDK"]
        SDKPY["Python SDK"]
        RAW["Connect · gRPC<br/>HTTP+JSON，不用 SDK"]
    end

    LB["Caddy<br/>least_conn<br/>/readyz 摘除<br/>失败重试到另一副本"]

    subgraph APIROLE["SPINNERET_ROLE=api 或 all"]
        direction TB
        NODEAPI["Node API<br/>Acquire · AcquireBatch<br/>Renew · Release · Report<br/>GetConfig · WatchConfig<br/>GetSecret"]
        SCHED["调度器<br/>acquire.lua<br/>一次往返"]
        CRED["凭据缓存 + 保管库<br/>信封解密<br/>secret_ref · 渲染"]
        INGEST["上报摄入<br/>校验 · 鉴权<br/>去重 · 分片"]
        CFGC["配置中心<br/>长轮询 watcher"]
    end

    RD[("Valkey<br/>热状态")]
    PG[("PostgreSQL<br/>事实来源")]

    SDKGO --> LB
    SDKPY --> LB
    RAW --> LB
    LB --> NODEAPI
    NODEAPI --> SCHED
    NODEAPI --> INGEST
    NODEAPI --> CFGC
    SCHED -->|"过闸门，选身份<br/>配代理，写租约"| RD
    SCHED --> CRED
    INGEST -->|"追加到<br/>流分片"| RD
    CRED -->|"加密载荷"| PG
    CFGC -->|"已发布版本"| PG
```

**上报管道。** 一条上报会变成什么：一次分类、一次归因、一次原子的热状态更新，以及每个对象最多一个动作。

```mermaid
flowchart TB
    RDIN[("Valkey<br/>上报流分片")]

    subgraph WORKERROLE["SPINNERET_ROLE=worker 或 all —— 上报管道与周期任务"]
        direction TB
        REG["分片注册表<br/>心跳<br/>归属再平衡"]
        CONS["每个自有分片一个消费者<br/>XREADGROUP · XAUTOCLAIM"]
        CLASS["分类<br/>信号策略 → 判定结果 + 归因"]
        OBS["observe.lua<br/>检查点 · 熔断窗口 · 配额<br/>健康分 EWMA · 连击 · 计数器"]
        PLAN["动作策略 → 处置计划<br/>每个对象取最严厉的那一个"]
        EXEC["执行器<br/>冷却 · 隔离<br/>封禁 · 过期"]
        JOBS["周期任务<br/>租约回收 1s · 熔断扫描 5s<br/>代理健康检查<br/>仅 leader：热状态快照 · 封禁到期<br/>告警 · 分区维护"]
        NOTIFY["告警评估<br/>与投递"]
    end

    RDOUT[("Valkey<br/>热状态")]
    PG[("PostgreSQL<br/>数据行与状态事件")]
    HOOK["Webhook 与<br/>聊天渠道"]

    RDIN -->|"流条目"| CONS
    REG <--> RDIN
    CONS --> CLASS
    CLASS --> OBS
    CLASS --> PLAN
    PLAN --> EXEC
    OBS -->|"一次原子更新"| RDOUT
    EXEC -->|"可用性变化<br/>立即生效"| RDOUT
    EXEC -->|"异步落库"| PG
    JOBS --> RDOUT
    JOBS -->|"用咨询锁<br/>选出 leader"| PG
    JOBS -->|"仅 leader"| NOTIFY
    NOTIFY -->|"带签名投递"| HOOK
```

**共享状态、控制台与可观测性。** 每个实例都带着同一套进程内缓存和同一条事件总线；三个存储由所有实例共享。

```mermaid
flowchart LR
    subgraph OPS["运维侧"]
        direction TB
        UI["React 控制台<br/>由 API 实例直接托管"]
        PROM["Prometheus"]
    end

    ADMINAPI["Admin API<br/>站点 · 身份 · 代理<br/>策略 · 熔断器 · 配置<br/>密钥 · 租户 · 令牌"]

    subgraph EVERY["每个实例上都有"]
        direction TB
        SSE["Server-Sent Events<br/>/api/v1/events/stream"]
        BUS["事件总线<br/>Redis pub/sub<br/>每命名空间一个频道"]
        CAT["目录（Catalog）<br/>进程内<br/>命名空间快照"]
        STATS["统计聚合器<br/>汇总与原始事件"]
    end

    subgraph STATE["状态存储"]
        direction TB
        PG[("PostgreSQL —— 事实来源<br/>租户 · 站点 · 身份及<br/>加密的载荷版本 · 账号<br/>代理 · 策略 · 配置 · 密钥<br/>状态事件 · 审计 · 汇总")]
        RD[("Valkey / Redis —— 热状态<br/>就绪队列 · 身份、账号与<br/>代理哈希 · 租约及到期集合<br/>熔断窗口 · 配额 · 会话粘性<br/>上报流 · 去重 · 控制台会话")]
        CH[("ClickHouse —— 可选<br/>report_events · lease_events")]
    end

    UI --> ADMINAPI
    PROM -.->|"抓取<br/>/metrics"| EVERY
    ADMINAPI --> SSE
    ADMINAPI --> PG
    SSE -.->|"熔断、身份、代理、<br/>告警、发布事件"| UI
    BUS <--> RD
    BUS -.->|"失效"| CAT
    BUS -.->|"扇出"| SSE
    CAT -->|"变更时重载"| PG
    STATS -->|"聚合结果"| PG
    STATS -->|"原始事件"| CH
    CH -->|"请求明细"| ADMINAPI
```


**热路径。** `Acquire` 对 Redis 执行一段 Lua 脚本：检查熔断器、采样候选、按可用性过滤（冷却、重用间隔、
配额、并发）并写入租约——全程不碰 PostgreSQL。`Report` 做校验、去重，追加到由 lease ID 推导出的 Redis
流分片上，然后返回。

**状态分层。** PostgreSQL 是事实来源：目录、身份、策略、密钥、审计。Redis 保存派生出来的热状态，
随时可以用 `spnr rebuild` 从 PostgreSQL 重建。ClickHouse 是可选的，存放供请求明细查询的原始请求事件。

**角色。** 一个二进制，`SPINNERET_ROLE=all|api|worker`。`all` 是默认值，也是 Compose 跑的形态；
当一波上报洪峰不能拖慢 `Acquire` 时，再把角色拆开。

### 一次请求的完整链路

```mermaid
sequenceDiagram
    autonumber
    participant A as 节点 A
    participant API as Spinneret API
    participant R as Valkey 热状态
    participant T as 目标系统
    participant W as Spinneret 上报管道
    participant PG as PostgreSQL
    participant B as 节点 B

    A->>API: Acquire(site, client, uri, wait_ms)
    Note over API: 校验令牌，解析站点、客户端<br/>与端点组，解析轮换策略
    API->>R: acquire.lua —— 一次往返
    Note over R: 过熔断闸门，处理会话粘性，采样就绪队列，<br/>剔除冷却中、已被租、超配额或仍在重用间隔内的，<br/>按健康分平方加权，分配代理，写入租约
    R-->>API: 身份 + 代理 + lease id
    API->>PG: 读取加密的载荷版本（有缓存）
    API-->>A: 租约、凭据、代理、提示
    A->>T: 用该凭据、经该代理发出请求
    T-->>A: 节点识别为「挑战」的响应
    A->>API: Report(report_id, lease_id, status, markers, latency, release)
    API->>R: 去重，然后追加到该租约的流分片
    API-->>A: 已接收
    W->>R: 读取分片条目
    Note over W: 信号策略判定：结果为 captcha，归因到身份
    W->>R: observe.lua —— 检查点、熔断窗口、配额、<br/>健康分、失败连击、计数器
    Note over W: 动作策略计划一次冷却；若计数条件命中则是封禁<br/>—— 每个对象取最严厉的那一个
    W->>R: 执行动作并结束租约
    W->>PG: 异步写入状态行与状态事件
    B->>API: Acquire，同一个端点组
    API->>R: acquire.lua
    R-->>API: 换一个身份 —— 被处置的那个不会被发出
    API-->>B: 租约、凭据、代理
    Note over W,R: 5 秒内熔断扫描重读窗口。如果整个组都在失败，<br/>熔断器打开，之后每次 Acquire 都返回 circuit_open<br/>和重试提示，直到一次探测成功
```

### 部署形态

默认栈就是一个 Compose 文件：Caddy 后面两个合并角色的实例，底下是 PostgreSQL、Valkey 和 ClickHouse。
服务端本身不依赖 ClickHouse——只有请求明细要用它——但发行的 Compose 文件会启动它并等它就绪，
所以要去掉它得改 `deploy/compose/docker-compose.yml`。

```mermaid
flowchart TB
    F["你的集群"] --> CADDY["Caddy —— 对外的入口"]

    subgraph HOST["单主机 · Docker Compose"]
        direction TB
        CADDY
        subgraph APPS["spinneret · SPINNERET_ROLE=all · 默认 2 副本"]
            direction LR
            I1["实例"]
            I2["实例"]
        end
        PG[("PostgreSQL")]
        VK[("Valkey")]
        CH[("ClickHouse —— 服务端可不用<br/>但默认栈会启动它")]
        MIG["migrate —— 一次性任务，先于实例执行"]
        PROM["Prometheus —— 可选 profile"]
    end

    CADDY --> APPS
    APPS --> PG
    APPS --> VK
    APPS --> CH
    MIG --> PG
    PROM -.->|"抓取指标"| APPS
    KEK["KEK，以文件 secret 挂载"] -.-> APPS
```

当上报处理和请求服务需要各自独立扩容时，把角色拆开。实例是无状态的，可以随意复制；三个存储是共享的；
acquire 的天花板仍然在那唯一一个 Valkey 上。

```mermaid
flowchart LR
    F["集群 —— N 个应用进程"] --> LB["负载均衡"]

    subgraph REPL["可复制 —— 无状态，想加多少加多少"]
        direction TB
        subgraph APIS["SPINNERET_ROLE=api —— 只服务流量，不跑周期任务"]
            direction LR
            A1["api"]
            A2["api"]
            A3["api"]
        end
        subgraph WKS["SPINNERET_ROLE=worker —— 只跑管道与周期任务，不对外提供 API"]
            direction LR
            W1["worker"]
            W2["worker"]
        end
    end

    subgraph SHARED["所有实例共享"]
        direction TB
        PG[("PostgreSQL<br/>同时用咨询锁<br/>选出 leader 任务")]
        VK[("Valkey<br/>热状态与上报流<br/>—— acquire 的天花板在这里")]
        CH[("ClickHouse —— 可选")]
    end

    LB --> APIS
    APIS -->|"把上报追加到分片"| VK
    APIS --> PG
    WKS -->|"认领并消费分片"| VK
    WKS --> PG
    APIS --> CH
    WKS --> CH
    OPS["控制台与 Prometheus"] --> LB
```

### 场景拓扑

同一个控制回路，按应用场景各画一次。变的只有身份和目标。

**为限速上游 API 做 key 池化。** 一池供应商 key，每个都有自己的预算，一次只发一个，上游一说不行就立刻收回。

```mermaid
flowchart LR
    APP["应用节点"] -->|"Acquire，轮换模式：不分配代理"| S["Spinneret"]
    K[("Key 池 —— 一个 key 一个身份<br/>key 本身是指向保管库的 secret_ref")] --- S
    S -->|"一个仍在预算内的 key"| APP
    APP -->|"调用"| U["限速的上游 API"]
    APP -->|"上报拿到的状态"| S
    S --> Q["按 key 的滑动配额、重用间隔、<br/>每个 key 同时只有一个进行中的租约"]
    S --> L["被限流 → 冷却这个 key<br/>鉴权被拒 → 标记为过期，等待刷新"]
    S --> BR["持续失败 → 熔断器停掉<br/>那个端点组，而不是整条集成"]
```

**共享出口治理。** 命名空间内所有业务共用一个出口池，并且在处置之前先分清是凭据坏了还是线路坏了。

```mermaid
flowchart LR
    N["任意出网业务"] -->|"Acquire"| S["Spinneret"]
    P[("命名空间出口池<br/>类型、地区、供应商、标签、最大并发")] --- S
    S -->|"池内挑选、按地区匹配，或按身份绑定"| N
    N -->|"上报：状态、错误类型、延迟"| S
    S --> BL{"归因"}
    BL -->|"身份"| I["由凭据买单"]
    BL -->|"代理"| X["由线路买单：只对这个目标冷却<br/>或全局冷却，并扣它的分"]
    BL -->|"交叉归因"| CA["同一条线路在多个凭据上都失败<br/>就是线路的锅，会被改判"]
    HC["后台健康检查<br/>连通性、出口 IP、地区"] --> S
```

**分布式采集。** 采集器只上报响应长什么样；由服务端决定这一次的代价，以及什么时候干脆停止发送。

```mermaid
flowchart LR
    C["采集进程"] -->|"Acquire"| S["Spinneret"]
    S -->|"身份 + 出口 + 租约"| C
    C -->|"请求"| T["目标系统"]
    T -->|"响应"| C
    C -->|"带上它识别出的标记上报"| S
    S --> D{"信号策略"}
    D -->|"成功"| OK["加分，直接回到就绪队列"]
    D -->|"挑战、限流、登录失效"| CD["在这里冷却这个身份<br/>窗口内命中 N 次则封禁"]
    D -->|"整个组都在失败"| BR["打开熔断器，停掉流量，<br/>并撤回它自己造成的那些冷却"]
```

**长会话与多步流程。** 一个必须全程守着同一个身份的流程：租约可以续，有上限防止它被永久持有，
还有回收器给死掉的进程善后。

```mermaid
flowchart LR
    W["多步流程"] -->|"带 session_key 的 Acquire"| S["Spinneret"]
    S -->|"只要它还可用，<br/>每一步都是同一个身份"| W
    W -->|"提示到点就 Renew"| S
    S -->|"租约 TTL，受 max_lease_lifetime 封顶"| W
    W -->|"最后一次带 release 的 Report"| S
    S --> ACC["身份按账号归组"]
    ACC -->|"一条上报说这个账号完了"| OUT["该账号下的所有身份<br/>同时退出调度"]
    S --> RE["进程持着租约死掉 →<br/>回收器在一个 TTL 内结束它，<br/>把身份还回来"]
```

**运行时配置与密钥。** 版本化配置和信封加密的密钥在数秒内到达整个集群，不用发版，也不用把密钥打进镜像。

```mermaid
flowchart LR
    OP["运维或 CI"] -->|"发布一个新版本"| S["Spinneret 配置中心"]
    S --- V[("版本化配置项：json、yaml、text<br/>可回滚到任意已发布版本")]
    F["集群里的每个进程"] -->|"WatchConfig 长轮询"| S
    S -->|"只下发变化的部分，数秒内"| F
    S -->|"在服务端解析 ${secret:...}<br/>每一次读取都留审计"| SEC[("保管库<br/>信封加密的密钥")]
    S -->|"保留的 _runtime 分组：<br/>当前熔断状态与已暂停的目标"| F
```

**多团队治理。** 一套安装服务多个团队：租户之间硬隔离，一个团队一个命名空间，令牌够不到别处，
每一次变更都有审计。

```mermaid
flowchart TB
    PA["平台团队"] --> T["租户 —— 硬隔离边界"]
    T --> NSA["命名空间：团队 A<br/>自有凭据、出口、策略、配置"]
    T --> NSB["命名空间：团队 B<br/>自有凭据、出口、策略、配置"]
    NSA --> TOKA["API 令牌，只在本命名空间内有效"]
    NSB --> TOKB["API 令牌，只在本命名空间内有效"]
    TOKA --> WA["团队 A 的进程"]
    TOKB --> WB["团队 B 的进程"]
    PA --> RB["角色绑定：viewer、operator、admin、owner<br/>可进一步收窄到一个命名空间或一组目标"]
    PA --> SH["影子模式：新策略照常计划一切，<br/>但什么都不执行，只记录它本来会做什么"]
    PA --> AU[("审计日志与状态事件<br/>谁改了什么，以及是哪条上报导致的")]
```

---

## 🖥 控制台

| | |
| --- | --- |
| ![概览](documents/images/overview-zh.png) | ![身份](documents/images/identities.png) |
| 概览：按站点的健康度、吞吐、判定结果分布、熔断器 | 身份：状态、健康分、冷却、筛选 |
| ![冷却热力图](documents/images/heatmap.png) | ![策略](documents/images/policies.png) |
| 冷却热力图：身份 × 端点组的可用性 | 策略：YAML 编辑器、版本、差异、发布 |
| ![熔断器](documents/images/breakers.png) | ![请求明细](documents/images/requests.png) |
| 熔断器：状态、窗口、手动开合、站点开关 | 请求明细：每一条上报及其判定结果与归因 |

21 个路由覆盖全部模块，中英双语，明暗主题，通过 Server-Sent Events 实时更新。英文界面下的同一个页面：
[`documents/images/overview.png`](documents/images/overview.png)。

---

## 🧩 v0.1 包含什么

下面每个模块都已经实现。

| 模块 | 它给你什么 |
| --- | --- |
| **[身份调度](documents/zh/06-identities.md)** | 带类型化载荷字段和下发模板的身份类型；轮换策略（`weighted_random`、`least_recently_used`、`round_robin`、`best_health`）、租约 TTL 与生命周期上限、重用间隔与锚点、配额、会话粘性、预热、探测加权 |
| **[出口分配](documents/zh/07-proxies.md)** | 带类型、地区、供应商与标签的代理池；`none / pool / bind_identity / region_match` 四种分配模式；会话模板；周期性健康检查；按站点的代理冷却 |
| **[上报与信号判定](documents/zh/08-policies.md)** | 批量、幂等的上报；可配置的信号规则，条件覆盖状态码、业务码、错误类型、标记、URI、方法、延迟与响应大小；12 种判定结果；身份与代理之间的交叉归因 |
| **[冷却与封禁](documents/zh/08-policies.md)** | 冷却可作用于身份-端点、身份-站点、身份、账号、代理-站点与代理；封禁可作用于身份、账号与代理；隔离可作用于身份与代理；身份可标记过期；带上限的指数退避；升级阶梯；影子模式；人工操作与批量回滚（`RevertActions`） |
| **[健康与生命周期](documents/zh/06-identities.md)** | 带时间衰减的 EWMA 健康分、按端点的低分冷却、自动隔离、身份状态机（`pending → active → quarantined / banned / expired / disabled / retired`） |
| **[熔断](documents/zh/08-policies.md)** | 按端点组的滑动窗口统计、三态熔断器（closed / open / half-open）配探测租约、手动开合、可选撤回触发窗口内施加的冷却 |
| **[配置中心](documents/zh/09-config-center.md)** | 带草稿、发布、回滚与差异对比的版本化配置项；长轮询 `WatchConfig`；SDK 侧本地快照；`${secret:path}` 引用；只读的 `_runtime` 分组，暴露熔断状态与站点开关 |
| **[密钥保管库](documents/zh/10-secrets.md)** | AES-256-GCM 信封加密（KEK → DEK → 数据）、文件或环境变量 KEK 提供者、在线 KEK 轮换与重加密、密钥版本与过期、每一次读取都留审计 |
| **[认证与多租户](documents/zh/11-access-control.md)** | 租户 → 命名空间 → 站点；控制台用户与 `viewer / operator / admin / owner` 四种角色，可按命名空间和站点绑定；细粒度作用域的节点 API 令牌；Argon2id 口令、登录限流、会话、CSRF |
| **[Web 控制台](documents/zh/05-console-overview.md)** | 21 个路由覆盖全部模块，中英双语，明暗主题，通过 SSE 实时更新 |
| **[通知](documents/zh/12-observability.md)** | 带 HMAC 签名投递的 Webhook，外加四种聊天平台发送器；11 类自动告警和一个测试告警，带去重和按站点路由 |
| **[可观测性](documents/zh/12-observability.md)** | `/healthz`、`/readyz`、Prometheus `/metrics`、可选 OTLP 链路追踪、基于 ClickHouse 的请求明细 |

---

## ⚡️ 快速开始

环境要求：带 Compose 插件的 Docker Engine，Compose 2.24 或更新，约 4 GB 空闲内存，一个空闲的主机端口
（默认 8080）。

### 一条命令

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.zh.sh -o install.zh.sh
less install.zh.sh       # 先读一遍；你马上就要运行它了
bash install.zh.sh
```

这个引导式安装器会检测主机环境，在缺少 Docker 时提议安装，克隆仓库，**在本机**生成口令和保管库主密钥，
为这台主机写好 Compose 覆盖文件，拉取或构建镜像，执行迁移，启动服务，等待 `/readyz`，最后创建第一个
管理员——全过程问七个问题，每个都有默认值。`--yes` 全部取默认值，`--check` 不改动任何东西、只打印将会
发生什么，`--manage` 则把它重新打开成一个管理菜单：状态、升级、账号、令牌、备份、恢复、健康、磁盘、卸载。

行为完全一致的英文版是 [`install/install.sh`](install/install.sh)；
[`install/README.md`](install/README.md) 记录了它问的每一个问题、每一个参数，以及它写下的每一个文件。

### 或者手动来

```bash
git clone https://github.com/TikHub/Spinneret.git
cd Spinneret

# 1. 生成 deploy/compose/.env（随机口令）和 deploy/compose/secrets/kek.key
./scripts/compose-init.sh

# 2. 构建镜像，启动 Postgres、Valkey、ClickHouse、迁移任务、2 个副本和负载均衡
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait

# 3. 创建第一个管理员、租户和命名空间（幂等）
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

打开 <http://localhost:8080>，用 `admin` 登录；口令在 `deploy/compose/.env` 里：

```bash
grep SPINNERET_ADMIN_PASSWORD deploy/compose/.env
```

### 跑一遍示例节点

一个完整的节点：对着内置的模拟目标申请租约、发请求、上报结果。

```bash
./scripts/example-quickstart.sh        # 或者：make example
```

脚本是幂等的。它会预置站点 `example`（2 个端点组、20 个身份、2 个模拟代理、已发布的轮换与信号策略、
一个配置项），创建一个节点令牌，在 <http://localhost:18000> 上启动示例服务并调用它：

```bash
curl 'http://localhost:18000/crawl/search?q=shoes'
curl  http://localhost:18000/crawl/item/42
curl  http://localhost:18000/config
```

现在让目标开始返回异常，在控制台里看 Spinneret 的反应——身份、熔断器、请求明细三个页面：

```bash
curl -X PUT localhost:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'
for i in $(seq 1 60); do curl -s -o /dev/null 'http://localhost:18000/crawl/search?q=x'; done
curl -X DELETE localhost:19090/_admin/rules
```

详见 [`examples/fastapi-crawler/README.zh-CN.md`](examples/fastapi-crawler/README.zh-CN.md)，
用 `./scripts/example-quickstart.sh --reset` 撤销它做的一切。

### 接下来看哪里

| 如果你想 | 就读 |
| --- | --- |
| 在动手配置之前先弄懂模型 | [核心概念](documents/zh/04-concepts.md) |
| 用十分钟从零跑通一个节点 | [快速开始](documents/zh/01-quickstart.md) |
| 上 TLS、多机部署，或者升级一套现有环境 | [安装与部署](documents/zh/02-installation.md) |
| 搞清每一个 `SPINNERET_*` 变量的作用 | [配置](documents/zh/03-configuration.md) |

---

## 🔌 接入一个节点

每个 RPC 都是 `POST /spinneret.v1.<Service>/<Method>`，`Content-Type: application/json`——Connect、gRPC
和 gRPC-Web 同样可用。字段名是 snake_case，时间戳是 RFC 3339 UTC。

### 两个调用讲完 API

先创建令牌。`spnr` 就装在镜像里，而且只需要 PostgreSQL，所以用一次性的 `migrate` 服务来跑它，
而不是去 exec 一个未必健康的副本。作用域按站点、按配置分组划分：

```bash
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate \
  token create --tenant default --namespace default --name node-hk \
    --scope lease:acquire:example --scope report:write:example \
    --scope config:read:crawler --expires 720h
# spn_EXAMPLEtokenEXAMPLEtokenEXAMPLEtoken1234567
```

**Acquire** —— 为你即将调用的 URI 申请一个身份和一条出口线路：

```bash
curl -s http://localhost:8080/spinneret.v1.LeaseService/Acquire \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: node-hk-03" \
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
    "headers": { "User-Agent": "ExampleClient/17" },
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

**Report** —— 上报发生了什么，只报事实，判定交给服务端。`release: true` 同时结束租约：

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

失败会在响应头里带上机器可读的原因和重试提示，客户端不用解析人话就能退避：

```http
HTTP/1.1 429 Too Many Requests
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 59367

{"code":"resource_exhausted","message":"no identity available for example/web/search"}
```

完整参考——每一个节点服务、每一个错误原因、每一条重试规则——在
[Node API 参考](documents/zh/13-node-api.md)。

### SDK

| SDK | 包 | 特性 |
| --- | --- | --- |
| **Python** —— [`sdk/python`](sdk/python/README.zh-CN.md) | `sdk/python`，导入名 `spinneret` | 基于 httpx 的同步 `Client` 与 asyncio `AsyncClient`，pydantic v2 模型，`with client.lease(...)` 把凭据和代理合并成 httpx 参数，后台批量上报器，带本地快照的配置 watcher |
| **Go** —— [`sdk/go`](sdk/go/README.zh-CN.md) | `github.com/TikHub/Spinneret/sdk/go/spinneret` | Connect JSON 或 gRPC，类型化错误（`IsCircuitOpen`、`IsNoIdentity` 等），`Lease.Apply(req)` 与 `Lease.Transport(base)`，批量 `Reporter`，带快照的 `ConfigWatcher` |

```python
import httpx, spinneret

with spinneret.Client() as client:                       # SPINNERET_URL / SPINNERET_TOKEN
    with client.lease(site="example", client="web", uri="/site/search") as lease:
        with httpx.Client(**lease.httpx_kwargs()) as http:
            r = http.get("http://target/site/search", params={"q": "shoes"})
        lease.report_response(r, markers=["empty_list"] if not r.json() else [])
```

```go
client, _ := spinneret.New(spinneret.Options{})          // SPINNERET_URL / SPINNERET_TOKEN
lease, err := client.Lease(ctx, &spinneret.AcquireRequest{Site: "example", Client: "web", Uri: target})
if err != nil {
	return err                                           // spinneret.IsNoIdentity(err)、IsCircuitOpen(err) ……
}
defer lease.Close(ctx)                                   // 最后一次上报会释放租约

transport, _ := lease.Transport(nil)                     // http.DefaultTransport 克隆 + 租到的代理
req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
lease.Apply(req)                                         // 凭据的 cookies、headers、query
resp, err := (&http.Client{Transport: transport}).Do(req)
if err != nil {
	return lease.ReportError(err, spinneret.ReportInput{})
}
return lease.ReportResponse(resp, spinneret.ReportInput{Markers: detectMarkers(resp)})
```

没有你那门语言的 SDK？纯 HTTP + JSON 就是一等公民客户端——见
[Node API 参考](documents/zh/13-node-api.md) 和 [`proto/README.md`](proto/README.md)。

---

## ⚗️ 技术栈

| 层 | 用了什么 |
| --- | --- |
| 服务端 | Go 1.27，单个二进制，`SPINNERET_ROLE=all\|api\|worker` |
| 传输 | 一套 Protobuf 定义同时产出 Connect、gRPC、gRPC-Web 和 HTTP+JSON |
| 数据 | PostgreSQL（事实来源）、Valkey 或 Redis（热状态、租约、上报流、会话）、ClickHouse（原始请求事件，可选） |
| 热路径 | 在 Redis 服务端执行的 Lua 脚本：每次 `Acquire` 只有一次往返 |
| 控制台 | React 18 + TypeScript，用 `go:embed` 嵌进二进制 |
| 部署 | Docker Compose + Caddy；镜像发布在 `ghcr.io/tikhub/spinneret`，覆盖 linux/amd64 与 linux/arm64 |
| 可观测性 | Prometheus 指标，可选 OTLP 链路追踪 |
| 工具链 | buf、sqlc、golangci-lint、k6、Playwright、Vitest、pytest |

CI 里实测、Compose 里锁定的兼容版本：

| 组件 | 版本 |
| --- | --- |
| Go | 1.27 或更新 |
| Node 与 pnpm | Node 22、pnpm 10 |
| Python（SDK） | 3.9、3.12、3.13 |
| PostgreSQL | Compose 里是 17 |
| Valkey 或 Redis | Compose 里是 Valkey 8 |
| ClickHouse | Compose 里是 25.8，可选 |
| Docker Compose | 2.24 或更新 |

---

## 🗂 目录结构

```text
cmd/                spinneret-server、spnr（管理 CLI）
proto/              Protobuf 定义；通信契约
internal/
  scheduler/        Acquire、AcquireBatch、Renew、Release、租约回收器、acquire.lua
  worker/           上报管道：分片归属、分类、observe.lua
  policy/           轮换、信号、动作与熔断四种策略，版本化 YAML
  action/           冷却、隔离、封禁、过期、撤回
  breaker/          滑动窗口、三态熔断器、_runtime 分组
  identity/         身份类型、载荷渲染、脱敏、导入
  identitysvc/      身份与账号服务、状态机
  proxy/            代理池、分配模式、健康检查、脱敏 URL
  configcenter/     版本化配置项、草稿、发布、回滚、WatchConfig
  vault/            信封加密、KEK 提供者、重加密
  auth/             用户、角色、节点令牌、会话、口令哈希
  tenancy/ authz/ audit/
                    租户与命名空间、作用域与权限、审计流水
  server/           HTTP 与 Connect 挂载、SSE、组件装配
  store/            PostgreSQL 查询、Redis key、ClickHouse 表结构
web/                React 控制台，嵌入二进制
sdk/go, sdk/python  客户端库
examples/           一个对着模拟目标跑的完整示例节点
install/            引导式安装器，中英文各一份
deploy/compose/     Compose 栈、Caddy、Prometheus
documents/          手册，中英文各 21 页
test/               e2e、压测（k6）与热路径基准
```

---

## 📊 状态与性能

Spinneret 处于 1.0 之前，v0.1 的功能已经完整：核心链路、风控回路、基础设施、控制台与发布四个里程碑全部实现，
并且随包提供 Go 端到端场景、一次副本故障切换演练、一套 Playwright 控制台用例和 k6 压测场景。

以下数字实测于 Compose 栈：单站点、10 万身份、50 个端点组，一个服务端实例、一个 Valkey 实例。
只有第一行是在开启 acquire 准入控制的情况下测得的；另外三行都早于准入闸门，性能页对此有明确说明。
完整表格、方法和注意事项见 [性能与调优](documents/zh/17-performance.md)。

| 测量项 | 结果 |
| --- | --- |
| 完整 acquire→report 闭环，独占租约，**开启准入控制** | 施加 4,500/s 时 **单实例 4,499/s**，acquire p99 **1.97 ms**，卸载 0.4/s |
| 只测 `Acquire` | **单实例 4,993/s**，服务端 p99 **4.32 ms**，峰值 7,792/s |
| 上报摄入 | 单实例接收 **44,437/s**、零拒绝；其中约 20,000/s 在 200 ms 预算内落到热状态 |
| 配置变更到全集群感知 | 唤醒全部 watcher 的 p99 **46.1 ms**，该项以 200 个 watcher 测得；另一项单独测量中，每实例可挂住约 1 万个并发长轮询，占 0.05 核 |

**两个副本共用一个 Redis，所以加副本加的是服务端容量，不是 acquire 吞吐。** 单副本服务 4,499/s 的地方，
两个副本服务约 4,000/s。抬高 acquire 天花板的是 Redis 容量；副本抬高的是长轮询容量、上报处理能力和可用性。
越过那个天花板之后，正确做法是拆成多套独立部署，而不是继续横向扩容。

每个实例用 **acquire 准入控制**（`SPINNERET_ACQUIRE_FLEET_INFLIGHT`，默认整个集群 64，按各自看到的存活
实例数均分；`0` 关闭）给自己在 Redis 上的并发设上限。超出的 acquire 在服务端内部、发出任何一条 Redis
命令之前就被卸载，返回 `unavailable`/`overloaded` 并附带带抖动的重试提示。它在单副本上不要任何代价
（施加 4,500 放行 4,499，卸载 0.4/s）；在容量点上，双副本在开启与关闭准入控制时表现基本一致，代价只体现在尾部
（acquire p99 2.99 ms 对 1.66 ms）；越过容量后，它把过载变成负载卸载而不是超时。
**它在远超拐点之后的行为没有被穷尽测量**——性能页写明了哪些是刻意没测的，并给出了在你自己硬件上做 A/B
的方法。

v0.2 的候选项，也就是 v0.1 明确不做的部分：分布式全局限流、外部校验与刷新 webhook、代理供应商适配、
NATS JetStream、OIDC 与 TOTP、mTLS、配置灰度发布、指纹分发、浏览器池。

---

## 🔢 版本与兼容性

Spinneret 遵循 SemVer 的 1.0 之前语义。在 1.0 之前，节点 API 的传输格式（wire format，`spinneret.v1.*`）、
`SPINNERET_*` 变量和策略 YAML 都可能在一个 minor 版本里变化。每一次变更都记录在
[`CHANGELOG.md`](CHANGELOG.md)，格式遵循 Keep a Changelog。

表结构迁移用 `spnr migrate up` 执行、`spnr migrate status` 查看、`spnr migrate down --to <version>` 回滚；
升级前先备份，具体见[运维](documents/zh/16-operations.md)。容器镜像由 `release` 工作流按每个发布 tag
发布到 `ghcr.io/tikhub/spinneret`，覆盖 linux/amd64 与 linux/arm64。安全修复会同时进入最新发布版和
`main`，流程见 [`SECURITY.md`](SECURITY.md)。

---

## 🔐 安全

请通过 <https://github.com/TikHub/Spinneret/security/advisories/new> 私下报告漏洞。
永远不要为安全问题开公开 issue。流程见 [`SECURITY.md`](SECURITY.md)。

有三件事在任何部署里都是你自己的责任：包裹保管库数据密钥的 KEK 必须存放在仓库之外、镜像之外；
控制台和节点 API 必须放在 TLS 后面；节点令牌必须限定在一个命名空间、它需要的那些站点，仅此而已。
[安全加固](documents/zh/19-security.md)就是在别人能访问到这套安装之前应当逐条过一遍的清单。

---

## 📖 文档

**从这里开始** —— [快速开始](documents/zh/01-quickstart.md) ·
[核心概念](documents/zh/04-concepts.md) · [控制台总览](documents/zh/05-console-overview.md)

**部署与运维** —— [安装](documents/zh/02-installation.md) ·
[配置](documents/zh/03-configuration.md) · [运维](documents/zh/16-operations.md) ·
[性能与调优](documents/zh/17-performance.md) ·
[故障排查](documents/zh/18-troubleshooting.md) ·
[安全加固](documents/zh/19-security.md)

**配置这套回路** —— [身份与账号](documents/zh/06-identities.md) ·
[代理](documents/zh/07-proxies.md) · [策略](documents/zh/08-policies.md) ·
[配置中心](documents/zh/09-config-center.md) · [密钥保管库](documents/zh/10-secrets.md) ·
[访问控制](documents/zh/11-access-control.md) ·
[可观测性与告警](documents/zh/12-observability.md)

**接入** —— [Node API 参考](documents/zh/13-node-api.md) · [SDK](documents/zh/14-sdks.md) ·
[CLI 参考](documents/zh/15-cli.md) · [`proto/README.md`](proto/README.md) ·
[`sdk/python/README.zh-CN.md`](sdk/python/README.zh-CN.md) ·
[`sdk/go/README.zh-CN.md`](sdk/go/README.zh-CN.md) ·
[`examples/fastapi-crawler/README.zh-CN.md`](examples/fastapi-crawler/README.zh-CN.md)

**参考** —— [FAQ 与术语表](documents/zh/21-faq.md) ·
[贡献指南](documents/zh/20-contributing.md) · [`install/README.md`](install/README.md) ·
[`web/README.md`](web/README.md) · [`test/load/README.md`](test/load/README.md) ·
[`test/perf/README.md`](test/perf/README.md)

两种语言的完整索引在 [`documents/README.zh-CN.md`](documents/README.zh-CN.md) ·
[English](documents/README.md)。

---

## 🛠 开发

环境要求：Go 1.27 或更新，Node 22 配 pnpm 10，用于集成测试基础设施的 Docker，以及 Python 3.9 或更新
（给 SDK 用）。

```bash
export PATH="$(go env GOPATH)/bin:$PATH"

make infra-up          # 测试用的 PostgreSQL、Valkey、ClickHouse，端口 45432 / 46379 / 49000
make test              # go test ./...            （集成测试用上面那套基础设施）
make test-race         # go test -race ./...
make lint vet fmt      # golangci-lint、go vet、gofmt
make generate          # buf generate + sqlc generate（生成结果要提交）

make web-install web   # pnpm install + pnpm build（控制台通过 go:embed 嵌入）
make build             # bin/spinneret-server 和 bin/spnr
make docker up down    # 构建镜像、启动和停止 Compose 栈
```

| 命令 | 跑的是什么 |
| --- | --- |
| `make test` | Go 单元测试与集成测试 |
| `make e2e` | 在 Compose 网络里跑的 Go 端到端场景（`test/e2e`，build tag `e2e`） |
| `make e2e-failover` | acquire 与上报负载下的副本故障切换演练 |
| `make e2e-web` | 驱动真实控制台的 Playwright 用例（`web/e2e`） |
| `cd web && pnpm test` | 控制台单元测试（Vitest） |
| `make python-test` | Python SDK 测试 |
| `make example-test` | 示例节点测试 |
| `make load` | k6 压测场景（profile `loadtest`） |

`.github/workflows/ci.yml` 会用 `-race` 跑 Go 全量测试，检查生成代码是否是最新的，构建并测试控制台，
在 3.9、3.12、3.13 上测试 Python SDK，并构建镜像。

`spnr` CLI 用来管理一套部署，读的是和服务端同一套 `SPINNERET_*` 环境变量：

```bash
spnr migrate up|down|status      spnr admin init --username admin --password-stdin
spnr token create --name ...     spnr rebuild [--site ...]
spnr kek generate|status|rewrap  spnr seed --site loadtest --identities 100000
spnr config check                spnr healthcheck --url http://127.0.0.1:8080/readyz
```

---

## 🤝 参与与支持

| | |
| --- | --- |
| 发现了 bug，或者想要某个功能？ | 开一个 [issue](https://github.com/TikHub/Spinneret/issues) —— [CONTRIBUTING.md](CONTRIBUTING.md) 说明了什么样的 issue 是好 issue，[documents/zh/20-contributing.md](documents/zh/20-contributing.md) 是开发指南 |
| 不确定某个东西怎么工作？ | 先看 [FAQ 与术语表](documents/zh/21-faq.md)，再看[故障排查](documents/zh/18-troubleshooting.md)，然后是 [Discussions](https://github.com/TikHub/Spinneret/discussions) |
| 发现了安全问题？ | **不要**开公开 issue。[SECURITY.md](SECURITY.md) 说明了私下报告的方式 |
| 其他 | <support@tikhub.io> |

社区支持在 issues 和 discussions 上进行，不承诺 SLA。商业支持由 TikHub 提供。

---

## 📄 许可证

Spinneret 以 [Apache License 2.0](LICENSE) 发布。简单说：你可以使用、修改和再分发它，包括商用；
你必须保留许可证和署名声明，并标注重大修改；许可证同时授予专利权，而一旦你就本软件发起专利诉讼，
这份授权即告终止。

Spinneret 由 [TikHub](https://github.com/TikHub) 开发、维护并开源，TikHub 自身也构建在同一套代码之上。

你把它指向哪里，责任在你。Spinneret 仲裁的是你提供的凭据和出口、你选择的目标；
合法地获得这些凭据、遵守你所调用系统的条款、符合你所在地的法律，这些都需要你自己把握。
