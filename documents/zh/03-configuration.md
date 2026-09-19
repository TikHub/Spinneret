# 配置参考

**Spinneret 实例的每一项运行时设置：变量名、代码里真正的默认值、它做什么、什么时候该改它。这是你一边开着
`.env` 一边要看的那一页。**

[English](../en/03-configuration.md)

---

## 目录

- [两层配置](#两层配置)
- [环境变量是怎么读取的](#环境变量是怎么读取的)
- [校验，以及部署前的检查](#校验以及部署前的检查)
- [进程身份](#进程身份)
- [PostgreSQL](#postgresql)
- [Redis / Valkey](#redis--valkey)
- [ClickHouse](#clickhouse)
- [密钥](#密钥)
- [上报与流](#上报与流)
- [缓存](#缓存)
- [Acquire 准入控制](#acquire-准入控制)
- [会话与 Cookie](#会话与-cookie)
- [TLS 与受信代理](#tls-与受信代理)
- [指标、性能剖析与链路追踪](#指标性能剖析与链路追踪)
- [日志](#日志)
- [代理健康检查与 GeoIP](#代理健康检查与-geoip)
- [数据保留](#数据保留)
- [请求限额](#请求限额)
- [控制台](#控制台)
- [Compose 已经替你设好的变量](#compose-已经替你设好的变量)
- [只给 Compose 和安装脚本用的变量](#只给-compose-和安装脚本用的变量)
- [多副本之间：必须相同、必须不同、可以分别调](#多副本之间必须相同必须不同可以分别调)
- [环境里的机密](#环境里的机密)
- [哪些东西不是环境变量](#哪些东西不是环境变量)
- [三份实战配置](#三份实战配置)
- [下一步](#下一步)

---

## 两层配置

Spinneret 里有两样东西都叫"配置"，把它们搞混会浪费一个下午。它们之间没有任何关系。

| | 第一层——进程环境 | 第二层——配置中心 |
| --- | --- | --- |
| 配置的对象 | **Spinneret 服务进程** | **你的爬虫节点** |
| 形式 | `SPINNERET_*` 环境变量 | 命名空间 / 分组 / 键 的配置项 |
| 存在哪里 | 编排系统、`.env`、systemd unit 文件 | PostgreSQL，带版本 |
| 怎么改 | 编辑后**重启实例** | 在控制台或管理 API 里发布 |
| 何时生效 | 下次启动 | 约一秒内，送达每个订阅节点 |
| 参考文档 | **本页** | [配置中心](./09-config-center.md) |

服务端从不从配置中心读自己的设置。这里没有自举循环，也没有先有鸡还是先有蛋的问题：`SPINNERET_DATABASE_URL`
不可能存在数据库里。反过来，Spinneret 也从不解释配置项的内容——对服务端来说那只是一段经过校验的 JSON、
YAML 或文本，属于你的节点。

所以：

- "我想把 403 之后的冷却时间从 5 分钟改成 10 分钟"——那是**策略**，运行时发布。见
  [策略](./08-policies.md)，本页没有。
- "我想让节点按每秒 20 个请求跑"——那是**配置项**，运行时发布。见
  [配置中心](./09-config-center.md)，本页没有。
- "我想让这个实例不再对外提供请求、只跑后台任务"——那是 `SPINNERET_ROLE=worker`，在本页。

本页所有内容都属于进程环境，本页所有内容都需要重启，本页所有内容都**不能**从控制台修改。这是刻意的：
容器带着什么配置在跑，这件事要在启动它的编排系统里看得见。

没有配置文件。服务端不读 YAML、不读 TOML、不读 `spinneret.conf`。也没有重载信号——`SIGINT` 和 `SIGTERM`
触发优雅停机，没有任何东西会重新读取一个正在运行进程的环境。

---

## 环境变量是怎么读取的

全部逻辑就在 `internal/appconfig/appconfig.go` 里。两个可执行程序——`spinneret-server` 和 `spnr`——通过同一个
函数读同一套环境变量，所以在主机 shell 里跑 `spnr` 校验的，就是服务端拿到这套环境后会做的事。

| 规则 | 细节 |
| --- | --- |
| 值会被 trim | 处理之前先去掉首尾空白。 |
| 空值等于未设置 | `SPINNERET_METRICS_ADDR=` 表示**默认值**，而不是空字符串；只填空格也算未设置。正因如此，Compose 才能传 `SPINNERET_PROXY_CHECK_URL: ${SPINNERET_PROXY_CHECK_URL:-}` 而仍然拿到内置默认值。 |
| 整数 | 普通十进制。**不接受** `1_000_000`，要写 `1000000`。 |
| 布尔 | `true`/`false`、`1`/`0`、`t`/`f`、`T`/`F`、`TRUE`/`True`、`FALSE`/`False`。 |
| 时长 | `500ms`、`30s`、`10m`、`24h`、`7d`，以及 `1h30m`、`1d12h` 这样的组合。`d` 是 Spinneret 的扩展单位，必须写在最前面。字面的 `0` 是零；空值走的是**默认值**，和上面那行一致。 |
| 不接受 `permanent` | 策略里的时长允许 `permanent`，环境变量里的时长不允许。 |
| 列表 | 逗号分隔，每项 trim，空项丢弃。适用于 `SPINNERET_REDIS_ADDRS`、`SPINNERET_TRUSTED_PROXIES` 和 `SPINNERET_ALLOWED_ORIGINS`。 |
| 大小写 | `SPINNERET_ROLE`、`SPINNERET_COOKIE_SECURE`、`SPINNERET_LOG_LEVEL`、`SPINNERET_LOG_FORMAT` 在校验前转小写，所以 `INFO` 和 `Info` 都行。其余变量都不转。 |

有一个变量不以 `SPINNERET_` 开头：`OTEL_EXPORTER_OTLP_ENDPOINT`，因为那是标准名字，其余 OpenTelemetry
环境变量由 SDK 自己读，不经过 Spinneret。

---

## 校验，以及部署前的检查

在连接任何东西之前，每个值都会被检查。报出来的问题分两类：

**解析错误**会指出变量名并把它看到的值引出来：

```text
SPINNERET_REPORT_SHARDS: invalid integer "sixteen"
SPINNERET_DATABASE_MAX_CONNS: invalid 32-bit integer "9999999999"
SPINNERET_PAYLOAD_CACHE: invalid boolean "on"
SPINNERET_SESSION_TTL: invalid duration "12 hours"
```

**取值范围和一致性错误**就是下面这张完整的表。所有问题会被一次收集完，所以跑一次就能知道全部，而不是
每重启一次发现一个：

| 条件 | 原文提示 |
| --- | --- |
| 角色不是 `all`/`api`/`worker` | `SPINNERET_ROLE must be one of all, api, worker (got "leader")` |
| 没有数据库 | `SPINNERET_DATABASE_URL is required` |
| 没有 Redis | `SPINNERET_REDIS_URL or SPINNERET_REDIS_ADDRS is required` |
| 没有密钥 | `SPINNERET_KEK_FILE or SPINNERET_KEKS is required` |
| 键前缀不合法 | `SPINNERET_REDIS_PREFIX must be non-empty and must not contain '{', '}', ':' or spaces` |
| 连接池太小 | `SPINNERET_DATABASE_MAX_CONNS must be >= 2` |
| 分片数越界 | `SPINNERET_REPORT_SHARDS must be between 1 and 255` |
| 去重窗口太短 | `SPINNERET_REPORT_DEDUP_TTL must be >= 1m` |
| 迟到窗口为负 | `SPINNERET_LATE_REPORT_WINDOW must be >= 0` |
| 流上限太小 | `SPINNERET_STREAM_MAXLEN must be >= 1000` |
| 集群预算越界 | `SPINNERET_ACQUIRE_FLEET_INFLIGHT must be between 0 (admission control off) and 65536` |
| 固定上限越界 | `SPINNERET_ACQUIRE_MAX_INFLIGHT must be between 0 (derive from the fleet budget) and 4096` |
| 载荷缓存容量为负，或 DEK 缓存容量小于 1 | `cache sizes must be positive` |
| 载荷缓存开着但容量为 0 | `SPINNERET_PAYLOAD_CACHE_SIZE must be >= 1 when SPINNERET_PAYLOAD_CACHE is enabled` |
| 会话生命周期太短 | `SPINNERET_SESSION_TTL must be >= 1m` |
| CIDR 解析失败 | `SPINNERET_TRUSTED_PROXIES: <解析错误>` |
| 任一保留期不足一天 | `SPINNERET_RETENTION_RISK_EVENTS must be >= 24h`（另外四个同理） |
| Cookie 模式不合法 | `SPINNERET_COOKIE_SECURE must be auto, true or false` |
| 只配了证书或只配了私钥 | `SPINNERET_TLS_CERT_FILE and SPINNERET_TLS_KEY_FILE must be set together` |
| 日志级别不合法 | `SPINNERET_LOG_LEVEL must be debug, info, warn or error` |
| 日志格式不合法 | `SPINNERET_LOG_FORMAT must be json or text` |
| 代理检测周期低于 `1s`，或超时低于 `100ms` | `proxy check interval/timeout too small` |
| ClickHouse TTL 越界 | `SPINNERET_CLICKHOUSE_TTL_DAYS must be between 1 and 3650` |
| 订阅上限小于 1 | `SPINNERET_MAX_WATCHERS must be >= 1` |
| 管理请求体上限不足 1 MiB | `SPINNERET_ADMIN_MAX_REQUEST_BYTES must be >= 1MiB` |
| pprof 和 API 同地址 | `SPINNERET_PPROF_ADDR must not be the API address: the profiling endpoints are unauthenticated` |
| pprof 和指标同地址 | `SPINNERET_PPROF_ADDR must not be the metrics address` |

服务端会在一个统一的标题下把它们全部打出来，然后以退出码 1 结束，一个连接都不会建立：

```text
spinneret-server: invalid configuration:
SPINNERET_ROLE must be one of all, api, worker (got "leader")
SPINNERET_REPORT_SHARDS must be between 1 and 255
```

### `spnr config check`

同一套校验，随时可以手动跑，并把解析后的配置以 JSON 打印出来，所有凭据都打码。它不连接任何东西，
所以在数据库还没起来的主机上，它就是第一条该跑的命令。

```bash
# 手工部署，环境变量已加载
spnr config check

# Compose 编排里，用一个一次性容器
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate config check

# 或者进一个已经在跑的副本（distroless 镜像里没有 shell，要写绝对路径；
# 加 --index 是因为这个服务有 SPINNERET_REPLICAS 个副本，默认 2 个）
docker compose -f deploy/compose/docker-compose.yml \
  exec --index 1 spinneret /usr/local/bin/spnr config check

# 用安装脚本生成的包装脚本，不需要有容器在跑
./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate config check
```

退出码 0 表示环境合法，退出码 1 会打印上面那张表里的问题。URL 和 libpq DSN 里的口令会被替换成
`xxxxx`，内联的密钥材料只会显示为 `<set>`。输出的是运维最容易配错的那部分设置，不是全部五十个变量——
完整的示例输出和命令参数见[命令行工具 → `spnr config`](./15-cli.md#spnr-config)。

进入变量表之前还有两件事。下面的变量按"它配置什么"分组，**服务端读取的每一个变量都只出现在其中一组
里**。不在上表中的校验发生在稍后的启动阶段，因为它们需要的信息比一个值更多：一个 worker 实例的连接池
如果不够它的 leader 任务用，启动会被拒绝并提示
`SPINNERET_DATABASE_MAX_CONNS=… is too small for a worker instance: … leader jobs each hold a connection
while leading; use at least …`；TLS 证书与私钥不匹配、密钥文件读不出来，也是同样在启动时失败。

---

## 进程身份

这个实例是谁、跑什么、在哪儿监听。

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_ROLE` | `all` | `all` 既提供节点 API、管理 API 和控制台，**又**运行后台 worker。`api` 只对外提供服务，不跑后台任务。`worker` 只跑管道，只提供 `/healthz`、`/readyz`，以及（未单独配置指标地址时）`/metrics`。 | 当你要把请求服务和上报管道拆开，让上报洪峰拖不慢 `Acquire` 时。见[运维手册 → 扩容](./16-operations.md#扩容)。 |
| `SPINNERET_INSTANCE_ID` | 主机名加 6 位随机十六进制，例如 `node-1-bba039` | 在日志、任务租约、Redis 流消费者名、以及 acquire 预算注册表里标识这个实例。 | 几乎永远不要改。留空让唯一的默认值生效。如果非要设，它必须**每个实例都不同**——不同会发生什么见 [Acquire 准入控制](#acquire-准入控制)。 |
| `SPINNERET_HTTP_ADDR` | `:8080` | 节点 API、管理 API、控制台、事件流，以及（未设置 `SPINNERET_METRICS_ADDR` 时）`/metrics` 的监听地址。 | 要绑定单个网卡（例如同机反向代理后面的 `127.0.0.1:8080`），或者要换端口。 |
| `SPINNERET_SHUTDOWN_TIMEOUT` | `30s` | 优雅停机的总预算。先关掉 keep-alive，然后进入一个 `min(5s, timeout/4)` 的排空窗口——此时 `/readyz` 已经回答 `draining`、每个响应都带 `Connection: close`，但监听端口还开着。之后 HTTP 停机拿走剩余预算的一半，后台循环拿剩下的。 | 上报积压需要更久才能刷完时调大；调小时要记住编排系统的强杀超时必须比它更长。Compose 编排给这个服务设了 `stop_grace_period: 40s`，就是为了这个。 |

`spinneret-server --role api` 会在命令行上覆盖 `SPINNERET_ROLE`，这在一个 unit 文件要伺候两种角色时很方便。
完整参数见[命令行工具 → spinneret-server](./15-cli.md#spinneret-server)。

排空窗口是滚动重启无感的关键：负载均衡看到 `draining` 就会在监听端口关闭之前停止转发，所以没有请求会被
半途切断。长连接请求——控制台事件流和节点的 `WatchConfig` 长轮询——在排空一开始就被取消，而不是留着等
超时，这样客户端会立刻重连到一个健康实例。

---

## PostgreSQL

PostgreSQL 保存持久状态：租户、命名空间、身份、代理、策略、配置项、密钥、审计日志，以及分区的统计表。
在预热后的稳定状态下热路径不碰它：它看到的是批量写入，在所有压测场景里 CPU 都低于 3%。但它并没有完全
不在热路径上——载荷缓存未命中时，就要从 PostgreSQL 读出并解密身份载荷，而 `SPINNERET_PAYLOAD_CACHE=false`
时每次租借都要这么做。这也是为什么连 `api` 实例也需要一个连接池。

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_DATABASE_URL` | **必填** | `postgres://` URL 或 libpq 键值 DSN。查询参数会原样传下去，所以 `?sslmode=verify-full&pool_max_conn_lifetime=1h` 是有效的。 | 总是要填——没有默认值。 |
| `SPINNERET_DATABASE_MAX_CONNS` | `32` | **每个实例**的连接池大小，最小 2。 | 改副本数或改数据库 `max_connections` 的时候。 |

连接池怎么定，就是一次乘法：`副本数 × SPINNERET_DATABASE_MAX_CONNS + 余量` 必须小于 PostgreSQL 的
`max_connections`，Compose 编排把它设为 300。默认的两副本 × 32 用掉其中 64 个。

worker 实例除了上限还有下限。每个 leader 任务在持有它的 advisory lock 期间会占住一条连接，所以如果连接池
不足以给每个 leader 任务一条连接再加两条备用，启动会被直接拒绝，而不是在压力下死锁。如果你在 `worker`
或 `all` 实例上把池子调小导致起不来，错误信息里会写清最小值。

只访问数据库的 `spnr` 子命令用大小为 4 的连接池，同时也尊重已设置的 `SPINNERET_DATABASE_MAX_CONNS`；
它们完全不需要配置 Redis 或密钥。

---

## Redis / Valkey

Redis（或者 Valkey——编排里用的是 Valkey 8，协议完全相同）保存热状态：`Acquire` 挑选用的候选集合、冷却和
封禁标记、租约哈希、熔断器计数、上报流，以及控制台会话。这就是热路径。

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_REDIS_URL` | 未设 `SPINNERET_REDIS_ADDRS` 时**必填** | `redis://host:port/db`，TLS 用 `rediss://`。凭据写在 URL 里。 | 单主或 Sentinel 前置地址时总是要填。 |
| `SPINNERET_REDIS_ADDRS` | 空 | `host:port` 列表，逗号分隔。列表非空就切到 **Cluster 模式**，并替换种子地址。它和 URL 至少要有一个，而且两者是**可以叠加**的：都设的时候，URL 继续提供凭据和 TLS 配置，这个列表提供节点地址。Cluster 模式下不能选数据库编号——此时 URL 里带非 0 的 db 会失败并提示 `redis: database N cannot be selected in cluster mode`。 | 跑 Cluster 的时候。 |
| `SPINNERET_REDIS_PREFIX` | `sp` | 所有键的前缀。必须非空，且不能含 `{`、`}`、`:` 或空格。 | 两套互不相干的 Spinneret 部署共用一个 Redis 时。其他情况别动。 |

只要前缀不同，两套部署共用一个 Redis 是安全的，因为前缀是每个键的第一段。改一个**正在运行**部署的前缀
会让旧键变成孤儿，现象和热状态凉了一模一样——见
[运维手册 → 重建热状态](./16-operations.md#重建热状态)。

键带了 hash tag，这样 Lua 脚本在 Cluster 下仍然是原子的：同一个站点的所有键共享 `{s<站点键>}`，同一个上报
分片的所有键共享 `{r<分片>}`。这个不需要你配置，但在看 `KEYS` 输出或规划槽位迁移时值得知道。

**热状态绝对不能被淘汰。** 它是派生数据、可以用 `spnr rebuild` 重建，但悄悄**丢掉**一个键不是失败而是
改变了一个调度判断：冷却标记消失了，正在冷却的身份就又可用了。这就是 Compose 编排把 Valkey 跑成
`maxmemory-policy noeviction` 的原因，你也应该这么做。怎么把实例容量定到永远碰不到上限，见
[性能与调优 → Redis 容量估算](./17-performance.md#redis-容量估算)。

---

## ClickHouse

ClickHouse 存的是原始逐请求事件和租约事件——请求明细页面背后的那些行。它是可选的。不配它其他功能照常
工作，你失去的是逐请求下钻，聚合数据仍然保留，那些在 PostgreSQL 里。

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_CLICKHOUSE_URL` | 空 | `clickhouse://user:password@host:9000/database`。留空则不存原始事件，服务端启动时打印 `clickhouse disabled: raw report events are not stored`。 | 要用请求明细就配上。评估用的小主机上可以留空，少跑一个容器。 |
| `SPINNERET_CLICKHOUSE_TTL_DAYS` | `90` | `report_events` 和 `lease_events` 两张表的保留天数，1–3650。 | 磁盘或审计要求不是 90 天的时候。 |

TTL 由启动时运行的 schema 迁移施加，而且对已存在的表也有效：配置值和表上的值不一致时，服务端会执行
`ALTER TABLE … MODIFY TTL`。所以改这个变量再重启就够了——但调小意味着下一轮 TTL 合并就会删数据，而且
不可逆。

这个变量和 `SPINNERET_RETENTION_*` 那一族毫无关系，后者管的是 PostgreSQL 的表。见
[数据保留](#数据保留)。

---

## 密钥

这几项是包裹整个部署里所有数据加密密钥（DEK）的密钥加密密钥（KEK）。身份载荷、代理 URL、密钥版本、
通知凭据各自用一把自己的 DEK 加密；DEK 再被一把 KEK 包裹；数据库里只有被包裹的密钥和密文。KEK 本身
永远不进数据库。

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_KEK_FILE` | 空 | 密钥文件路径，最大 1 MiB。内容是若干 `id:base64` 行，或者一行裸 base64——那种情况下 id 就是 `k1`。空行、`#` 注释、首尾空白、开头的 BOM 都会被正确处理。 | 这是推荐形式，理由见[环境里的机密](#环境里的机密)。 |
| `SPINNERET_KEKS` | 空 | 同样的密钥，内联写：`k1:base64,k2:base64`。空项忽略。 | 只在实在没法用文件机密的地方用。 |
| `SPINNERET_KEK_CURRENT` | 列表里最后一把密钥的 id（先文件条目，再内联条目） | 用哪把密钥包裹**新的** DEK。所有已配置的密钥都仍能解包，这正是轮换可以在线做的原因。 | 轮换期间，先让新写入用新密钥，再去重新包裹旧记录。 |

`SPINNERET_KEK_FILE` / `SPINNERET_KEKS` 至少要有一个。两个可以同时设，密钥集合会被合并；同一个 id 出现
两次只在密钥材料完全相同时允许，否则启动失败并提示
`vault: kek "k1" is configured twice with different keys`。每把密钥都必须解码出正好 32 字节，标准或
URL-safe base64、带不带填充都可以。

```bash
spnr kek generate --id k1   # 打印一行 "k1:<32 字节随机数的 base64>"
spnr kek status             # 每把密钥各包裹了多少条已存储的记录
spnr kek rewrap             # 用当前 KEK 重新包裹所有已存储的 DEK
```

**在往部署里放数据之前先备份这把密钥，每次轮换之后再备份一次。** 没有 KEK 的数据库备份，对每一个加密
字段来说都是不可恢复的。这是设计上的结果，没有找回途径。

Compose 编排把密钥放在 `deploy/compose/secrets/kek.key`，以 Docker 机密 `kek` 的形式挂到
`/run/secrets/kek`，并把 `SPINNERET_KEK_FILE` 指向那个路径。这个文件为什么故意是 `0644`：见
[安装与部署 → 密钥加密密钥](./02-installation.md#密钥加密密钥)。怎么轮换：见
[运维手册 → 密钥轮换](./16-operations.md#密钥轮换)。信封加密方案本身：见
[密钥保管库](./10-secrets.md)。

---

## 上报与流

一次 `Report` 先写进一个 Redis 流分片，再被异步处理。下面五个变量决定这条管道的形状。

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_REPORT_SHARDS` | `16` | Redis 流分片数，1–255。一个分片只有一个归属 worker，所以这个数决定了上报处理的并行度上限。 | 第一次启动前定好，之后基本永远别动。**租约 ID 里编码了分片号**，改它会让在途租约失效——正确做法见[运维手册 → 修改分片数](./16-operations.md#修改分片数)。 |
| `SPINNERET_REPORT_DEDUP_TTL` | `1h` | 在这个窗口内重复出现的 `report_id` 被判为 `duplicated`，而不是被应用两次。最小 `1m`。 | 让它明显长于节点的重试时间跨度。Valkey 内存吃紧时可以缩短它：去重标记同时会把已结束的租约哈希钉住同样长的时间，这使它成为 Valkey 内存里随流量增长的主要项。见[性能与调优 → Redis 容量估算](./17-performance.md#redis-容量估算)。 |
| `SPINNERET_LATE_REPORT_WINDOW` | `10m` | 晚于这个时间才到的上报仍然会被记录、仍然计入统计，但不再改变身份状态。`0` 关闭这个截断。 | 节点会长时间离线缓存时调大。设成 `0` 之前想清楚：你真的希望昨天的一条上报今天去冷却一个身份吗。 |
| `SPINNERET_STREAM_MAXLEN` | `1000000` | 每个分片积压的近似上限，最小 1000。worker 会把自己的分片裁剪到消费位置，所以跟得上的部署离这个上限很远；跟不上时超出上限的条目会被丢弃。 | 计划内的 worker 停机不希望丢上报就调大；要在最坏情况下压住 Valkey 内存就调小。 |
| `SPINNERET_RECORD_COOLDOWN_EVENTS` | `true` | 每次冷却都写一条状态事件，而不只是封禁、隔离和失效。 | 超高量场景下状态事件存储成为主要开销时关掉。你失去的是逐次冷却的审计线索，不是冷却本身。 |

分片数是本页唯一一个之后真的很难改的设置。定它要按你**预期的 worker 实例数**，而不是按上报速率：一个
分片只有一个归属者，所以规则是**每个规划中的 worker 实例 2–4 个分片**。分片比 worker 还少会让 worker
闲着；每个 worker 超过四个分片则什么也换不来，还多一个消费者组的开销。容量通常不是把它调到 16 以上的
理由：实测下来，在默认 16 分片时，单个 worker 实例能在 200 ms 目标内应用约 **20,000 条上报/秒**。所以
32 个分片适合规划 8–16 个 worker 实例的部署，低于这个数它并不是免费的——见
[性能与调优 → 调优手段，按收益排序](./17-performance.md#调优手段按收益排序)和
[运维手册 → 上报滞后在变大](./16-operations.md#4-上报滞后在变大)。

---

## 缓存

三个进程内缓存，用内存换热路径上的延迟。都是每实例独立的，都不共享。

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_PAYLOAD_CACHE` | `true` | 在进程内缓存渲染好的凭据，这样重复租借同一个身份不必再读一次载荷、再解密一次。 | 设 `false` 可以让解密后的载荷不留在进程内存里——一种加固选择，代价是每次租借都要解密一次。 |
| `SPINNERET_PAYLOAD_CACHE_SIZE` | `200000` | LRU 条目上限，缓存开着时至少为 1。只有载荷版本、身份类型 ID 和类型版本三者都还匹配时条目才会被使用，所以改载荷或改类型天然就让它失效。 | 活跃身份的工作集超过 20 万时调大；要压内存时调小。 |
| `SPINNERET_DEK_CACHE_SIZE` | `100000` | 已解包数据密钥的 LRU，至少为 1。没有它，每次读载荷都要做一次 KEK 解包。 | 同样的道理，低一层：按工作集里不同加密记录的条数来定。 |
| `SPINNERET_DEK_CACHE_TTL` | `10m` | 该缓存里一个条目的生命周期。 | 想缩短解包后的密钥留在内存中的时间时调小。 |
| `SPINNERET_TOKEN_CACHE_TTL` | `30s` | 一次令牌校验结果被缓存多久。 | 很少需要改。它只是最坏情况的上界：吊销令牌会发出一个事件，让每个实例立刻丢掉对应条目，这个值管的是那个事件丢失的情形。 |

解析过 `${secret:...}` 引用的凭据，无论容量怎么配，最多只缓存 60 秒，这样密钥轮换能很快到达节点，而不必
把缓存整个关掉。那 60 秒不可配置。

---

## Acquire 准入控制

一次失败的 `Acquire` 大约是一次成功的五倍开销，因为它会走一遍那些用不了的候选。所以过了拐点之后，把每个
请求都排队压在 Redis 上的集群不是变慢，而是**崩塌**：并发越高，吞吐越低。准入控制给整个集群在 Redis 上
的在途量设上界，超出的部分带重试提示直接卸掉，而不是排队。

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_ACQUIRE_FLEET_INFLIGHT` | `64` | **整个集群**允许在 Redis 上同时在途的 acquire 脚本数，0–65536。每个 API 实例把这个预算除以它看到的存活 API 实例数，再夹到 `[4, 4096]`。超限时一次 acquire 最多等 50 ms 拿许可，然后以 `unavailable`、原因 `overloaded`、重试提示 100–200 ms 被卸掉。`0` 关闭准入控制、回到无上界行为——但只在 `SPINNERET_ACQUIRE_MAX_INFLIGHT` 同时也是 `0` 的时候，因为固定上限是在查集群预算**之前**就生效的。 | 你测出 Redis 还能吃更多时调大。如果卸载带来的伤害超过它防住的拥塞，把它设成 `0` 作为故障处置手段——而在固定了上限的集群里，要把**两个**都设成 `0`，否则闸门仍停在固定值上，这个手段会静默失效。 |
| `SPINNERET_ACQUIRE_MAX_INFLIGHT` | `0`（推导） | 固定**本实例**的上限，不再去除集群预算，0–4096。 | Redis 容量随主节点数增长时（填每主节点的数字）、你测出了自己的天花板时，或者要做可复现的压测时。 |

有两种失败方式值得写清楚，因为它们都是静默的。

**实例 ID 重复会把除数压塌。** 注册表的成员名就是 `SPINNERET_INSTANCE_ID`，所以 N 个共用一个 ID 的实例
只注册成一个成员。于是每个都看到只有一个存活对等体、都上报 `spinneret_acquire_peers = 1`，都放行**整个**
集群预算——合计是预算的 N 倍。唯一的默认值能避免这一点，是手工设置这个变量把它弄坏的。

**只在部分实例上固定上限会超预算。** 正的 `SPINNERET_ACQUIRE_MAX_INFLIGHT` 同时会停掉那个统计存活 API
实例的心跳，于是固定上限的实例不会注册成 acquirer。剩下那些推导的实例就会除以一个过小的数，集群总量
超出预算的部分正好是固定实例的整份配额。所以要么**每个** API 实例都固定，要么一个都不固定。

关注 `spinneret_acquire_inflight_limit`、`spinneret_acquire_inflight`、`spinneret_acquire_queued` 和
`spinneret_acquire_peers`。上限自己变化时会按每次变化打一条日志，带上旧的和新的对等体数量。默认值背后
的测量数据和怎么定这个值：见[性能与调优 → 准入控制](./17-performance.md#准入控制)。

---

## 会话与 Cookie

这两个管控台登录。它们对节点 API 令牌没有任何影响，后者完全不用 Cookie。

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_SESSION_TTL` | `12h` | 控制台会话生命周期，最小 `1m`。过期是滑动的：活跃会话会被续期，而且只在会话仍然存在时才续，所以一次和登出竞争的续期不会把它救回来。 | 控制台暴露在不可信网络上时缩短；作为内部工具让人一直开着时放长。 |
| `SPINNERET_COOKIE_SECURE` | `auto` | `auto` 表示请求走的是 TLS、或者带着来自受信代理的 `X-Forwarded-Proto: https` 时，给会话 Cookie 打上 `Secure`；`true` 总是打；`false` 永不打。Cookie 始终是 `HttpOnly` 加 `SameSite=Strict`。 | 只要发布到 loopback 之外就设 `true`，明确写出来而不是依赖 `auto`。`false` 只属于纯 HTTP 的内网。 |

`auto` 依赖 [`SPINNERET_TRUSTED_PROXIES`](#tls-与受信代理) 配对。如果代理不受信，它的
`X-Forwarded-Proto` 会被忽略，请求看起来就是纯 HTTP，于是在一个 HTTPS 部署上 Cookie 不会被打上
`Secure`。设 `SPINNERET_COOKIE_SECURE=true` 能把 Cookie 从这个依赖里解开，但解不开客户端 IP。完整的
会话模型见[安全 → 控制台认证与会话](./19-security.md#控制台认证与会话)。

---

## TLS 与受信代理

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_TLS_CERT_FILE` | 空 | PEM 证书链。直接提供 HTTPS 时启用 TLS 上的 HTTP/2，最低 TLS 1.2。 | 没有反向代理来终结 TLS 的时候。 |
| `SPINNERET_TLS_KEY_FILE` | 空 | 对应的私钥。必须和证书一起设置。 | 和上面一起。 |
| `SPINNERET_TRUSTED_PROXIES` | 空 | 逗号分隔的 CIDR 前缀（IPv4 和 IPv6），只有来自这些网段的 `X-Forwarded-For` 和 `X-Forwarded-Proto` 才被采信。 | **在任何反向代理后面都必须配。** |

证书和私钥在启动时加载，所以证书损坏或不匹配会让启动直接失败，报 `load TLS certificate <路径>: …`，而
不是等到第一个请求才失败。没有配证书时监听的是纯 HTTP，并启用非加密 HTTP/2（h2c），这也是内网负载均衡
说的协议。

`SPINNERET_TRUSTED_PROXIES` 是最常被忘掉、影响面又最大的一个。在代理后面把它留空，所有请求看起来都来自
代理的地址，于是：令牌 IP 白名单匹配的是代理而不是节点，登录限流把全世界当成一个客户端，每条审计记录和
风险事件带的源 IP 都是错的。把它设成你的代理真实来源的前缀——不要 `0.0.0.0/0`，那等于信任任何能连上这个
端口的人伪造的头。

Compose 编排设的是 `172.16.0.0/12,10.0.0.0/8,192.168.0.0/16`，覆盖 Docker 自己的 bridge 网络和同机边缘
代理会用的私有网段。前置代理还必须做到什么——长轮询不缓冲、请求超时不低于轮询窗口——见
[安装与部署 → 反向代理与 TLS](./02-installation.md#反向代理与-tls)。

---

## 指标、性能剖析与链路追踪

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_METRICS_ADDR` | 空 | 留空则 `/metrics` 挂在主监听上，而且**没有认证**。填 `:9091` 这样的值会让它单独监听，那个监听上什么别的都没有。 | 只要主监听对外发布了就设上：这样指标端口可以只对监控网络可达。 |
| `SPINNERET_PPROF_ADDR` | 空 | 留空则完全关闭性能剖析。填 `127.0.0.1:6060` 这样的值会在独立监听上提供 `/debug/pprof/`，且不设读超时，这样一次 CPU profile 或执行 trace 能把响应挂住。 | 只在剖析期间开，之后改回去。 |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 空 | 设了就启用 OTLP 链路导出，并给每个 RPC 装上追踪拦截器。其余标准 `OTEL_*` 变量由 SDK 读取。 | 有 collector 的时候。它对每个请求都有一点开销，所以默认关闭。 |

pprof 端点没有认证，会暴露堆内容、goroutine 栈和命令行。它永远不会挂在 API 或指标监听上，配置也会拒绝
一个等于这两者的地址。开启时每次启动都会打一条告警——
`pprof debug listener enabled; keep this address unreachable from untrusted networks`。把它绑到
loopback，用 SSH 隧道访问。

任何级别下都不会记录或导出敏感内容：载荷字段、代理凭据、密钥值和令牌材料由日志组件本身脱敏，而不是靠
调用点自觉。该对哪些指标告警、每个指标是什么意思：见[可观测性与告警](./12-observability.md)。

---

## 日志

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_LOG_LEVEL` | `info` | `debug`、`info`、`warn`（`warning` 作为同义词也接受）或 `error`。 | 排查期间用 `debug`，查完改回来——它在热路径上很吵。 |
| `SPINNERET_LOG_FORMAT` | `json` | `json` 输出结构化日志，`text` 给终端前的人看。 | 笔记本上跑前台进程用 `text`；有日志采集器读的地方保持 `json`。 |

两者都输出到 stderr。`spnr` 的日志始终是 stderr 上的 `text`，命令输出留在 stdout，所以不论这个设置是
什么，把 `spnr` 的输出管给 `jq` 都能用。怎么读一行 Spinneret 日志、该 grep 哪些字段：见
[故障排查](./18-troubleshooting.md)。

---

## 代理健康检查与 GeoIP

健康检查器会按周期**通过**每个代理去取一个 URL，并根据结果把代理标为可用或不可用。单次运行内的并发固定
为 64 个检测。

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_PROXY_CHECK_URL` | `http://example.com/` | 通过每个代理去取的那个 URL。 | **在任何没有公网出口的主机上都要改。** 否则每个代理都检测失败、被标为不可用，于是明明代理池是健康的，节点却拿到 `no_proxy_available`。把它指向代理真的能访问到的地址。 |
| `SPINNERET_PROXY_CHECK_INTERVAL` | `60s` | 两次检测之间的周期，最小 `1s`。 | 住宅代理池波动大就缩短；大而稳定的池子可以放长以减少检测流量。 |
| `SPINNERET_PROXY_CHECK_TIMEOUT` | `10s` | 单个代理的超时，最小 `100ms`。 | 希望慢代理更快被标为不可用就调小；高延迟出口调大。 |
| `SPINNERET_PROXY_EXIT_IP_URL` | 空 | 一个会返回调用方自身 IP 的 URL。设了之后每次检测都会记录该代理的出口 IP。 | 想在控制台里看到出口 IP、发现两个入口共用一个出口，或者想用 GeoIP 补地区的时候。 |
| `SPINNERET_GEOIP_DB` | 空 | MaxMind 格式数据库的路径，用于在代理地区为空而出口 IP 已知时补上地区。 | 供应商不告诉你地区、而你又按地区路由的时候。 |

对 `test`、`loadtest`、`example` 这几个 profile，检测 URL 应该指向内置的 mock target，也就是
`http://mocktarget:9090/healthz`——`scripts/example-quickstart.sh` 就是把这一行写进 `.env` 的。代理池
模型、分配方式，以及"不可用"对调度意味着什么：见[代理池](./07-proxies.md)。

---

## 数据保留

这五个变量管的是 PostgreSQL 的分区表。每小时运行的 `partition_manager` leader 任务负责把未来的分区滚出
来，并删掉整段范围都早于保留期的分区，所以只有当一个分区里每一行都过期了，它才会被删。每个值至少 `24h`。

| 变量 | 默认值 | 管哪些表 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_RETENTION_RISK_EVENTS` | `720h`（30 天） | `risk_events`——控制台风险事件流背后的行 | 你会在几周之后回头查事故就放长。 |
| `SPINNERET_RETENTION_MINUTE_STATS` | `720h`（30 天） | `outcome_stats_minutely`、`acquire_stats_minutely`、`node_stats_minutely`、`payload_access_minutely` | 按行数算这是五个里最大的。PostgreSQL 磁盘吃紧时先缩它。 |
| `SPINNERET_RETENTION_HOUR_STATS` | `4320h`（180 天） | `identity_stats_hourly`——长周期趋势 | 要做同比就放长，代价很小。 |
| `SPINNERET_RETENTION_STATE_EVENTS` | `8760h`（365 天） | `state_events`——每一次身份和代理状态变迁 | 只有在你不需要解释上个季度某个身份为什么被封时才缩短。 |
| `SPINNERET_RETENTION_AUDIT` | `8760h`（365 天） | `audit_logs`——包括每一次密钥读取 | 通常是合规决定，不是磁盘决定。 |

原始请求事件在 ClickHouse 里，按 `SPINNERET_CLICKHOUSE_TTL_DAYS` 过期，和这五个完全独立。告警事件按固定
的 90 天窗口清理，那个不可配置。

放长一个保留期在下一轮每小时任务里生效，而且当下不花任何代价——数据还得慢慢攒。缩短一个会在下一轮就
删掉分区，删了就是删了。每张表每天实际占多少：见
[运维手册 → 数据保留与磁盘](./16-operations.md#数据保留与磁盘)。

---

## 请求限额

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_ADMIN_MAX_REQUEST_BYTES` | `67108864`（64 MiB） | 管理类 RPC 的请求体上限，最小 1 MiB。一次很大的身份或代理导入撞到的就是这个限制。 | 要做一次超大的一次性导入时调大；想减少一个已认证的管理客户端能让服务端缓冲的量时调小。 |
| `SPINNERET_MAX_WATCHERS` | `20000` | 本实例同时挂住的 `WatchConfig` 长轮询数。超限时调用方拿到 `resource_exhausted`、原因 `rate_limited`，并带重试提示，两个 SDK 都会据此退避。 | 单实例要承载超过 2 万个订阅节点时调大；要压住内存和文件描述符时调小。 |

旁边还有两个限额**不可配置**，写在这里免得你去找变量：面向节点的 RPC 请求体上限是 8 MiB，请求头上限是
1 MiB。

---

## 控制台

| 变量 | 默认值 | 作用 | 什么时候改 |
| --- | --- | --- | --- |
| `SPINNERET_UI_ENABLED` | `true` | 从主监听提供内嵌的控制台。`false` 则只保留 API 和健康检查端点。 | 只服务节点的实例上设 `false`，让控制台只在一个你能保护的地址上可达。 |
| `SPINNERET_ALLOWED_ORIGINS` | 空 | CORS 允许的来源列表，例如 `http://localhost:5173`。留空则完全不装 CORS 中间件。`*` 也接受，表示允许任意来源。 | 只在要把控制台的开发服务器指向这个实例时用。生产环境留空。 |
| `SPINNERET_UPDATE_CHECK_URL` | `https://api.github.com/repos/TikHub/Spinneret/releases/latest` | **设置 → 系统**页面点「检查更新」时读取的发布源。没有任何轮询；成功的结果缓存 1 小时，失败缓存 1 分钟。留空即关闭检查，控制台会隐藏按钮并说明原因。 | 内网隔离的部署、或者任何不允许对外发起连接的主机上留空。如果你自己发布内部构建，就指向自己的镜像地址。 |

`worker` 角色的实例不管 `SPINNERET_UI_ENABLED` 怎么设都不提供控制台，因为它不提供 API。控制台里有什么、
哪个页面对应哪篇文档：见[控制台总览](./05-console-overview.md)。

更新检查跑在服务端，不在浏览器里：控制台的 CSP 是 `connect-src 'self'`，而且经 VPN 访问的控制台往往自己出不去、
主机却出得去。这个请求不带任何部署信息——不带版本、不带标识——只是去取一个公开地址。

---

## Compose 已经替你设好的变量

`deploy/compose/docker-compose.yml` 会在 `migrate`、`spinneret`、`init-admin` 三个服务的容器环境里设好
下面这些。你不需要把它们写进 `.env`——其中好几个是由别的值拼出来的，而且改它们的方式不是去编辑
Compose 文件。

| 变量 | 编排里的值 | 说明 |
| --- | --- | --- |
| `SPINNERET_HTTP_ADDR` | `:8080` | 固定；对外发布的端口是负载均衡上的 `SPINNERET_PORT`。 |
| `SPINNERET_DATABASE_URL` | `postgres://spinneret:${PG_PASSWORD}@postgres:5432/spinneret?sslmode=disable` | `sslmode=disable` 在私有 Compose 网络里没问题，跨主机就不行了。 |
| `SPINNERET_REDIS_URL` | `redis://valkey:6379/0` | |
| `SPINNERET_CLICKHOUSE_URL` | `clickhouse://spinneret:${CLICKHOUSE_PASSWORD}@clickhouse:9000/spinneret` | |
| `SPINNERET_KEK_FILE` | `/run/secrets/kek` | 来自 `deploy/compose/secrets/kek.key` 的 Docker 机密。 |
| `SPINNERET_TRUSTED_PROXIES` | `172.16.0.0/12,10.0.0.0/8,192.168.0.0/16` | 覆盖 Compose bridge 网络和同机边缘代理。 |
| `SPINNERET_DATABASE_MAX_CONNS` | `${SPINNERET_DATABASE_MAX_CONNS:-32}` | 可在 `.env` 里覆盖。 |
| `SPINNERET_LOG_LEVEL` | `${SPINNERET_LOG_LEVEL:-info}` | 可在 `.env` 里覆盖。 |
| `SPINNERET_COOKIE_SECURE` | `${SPINNERET_COOKIE_SECURE:-auto}` | 可在 `.env` 里覆盖。 |
| `SPINNERET_REPORT_SHARDS` | `${SPINNERET_REPORT_SHARDS:-16}` | 可在 `.env` 里覆盖。 |
| `SPINNERET_REPORT_DEDUP_TTL` | `${SPINNERET_REPORT_DEDUP_TTL:-1h}` | 可在 `.env` 里覆盖。 |
| `SPINNERET_ACQUIRE_FLEET_INFLIGHT` | `${SPINNERET_ACQUIRE_FLEET_INFLIGHT:-64}` | 可在 `.env` 里覆盖。 |
| `SPINNERET_ACQUIRE_MAX_INFLIGHT` | `${SPINNERET_ACQUIRE_MAX_INFLIGHT:-0}` | 可在 `.env` 里覆盖。 |
| `SPINNERET_PROXY_CHECK_URL` | `${SPINNERET_PROXY_CHECK_URL:-}` | 空值传下去等于未设置，于是内置默认值生效。 |

本页上任何其他变量，如果你想在 Compose 部署里用，就写进你自己的一个小 override 文件，放在随包文件之后
传入；不要去改 `docker-compose.yml`，升级会把它换掉：

```yaml
# deploy/compose/compose.local.yml
services:
  spinneret:
    environment:
      SPINNERET_METRICS_ADDR: ":9091"
      SPINNERET_SESSION_TTL: "4h"
      SPINNERET_RETENTION_MINUTE_STATS: "168h"
```

```bash
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/compose.local.yml up -d
```

`SPINNERET_INSTANCE_ID` 是**故意**不传下去的：每个副本都有自己的容器主机名，默认值会由它推导出唯一的
ID；把一个值套在所有副本上会破坏 acquire 预算。见
[多副本之间](#多副本之间必须相同必须不同可以分别调)。

---

## 只给 Compose 和安装脚本用的变量

这些由 Compose 文件或安装脚本读取，服务端从不读。它们住在 `deploy/compose/.env` 里——那个文件由
`scripts/compose-init.sh` 从 `.env.example` 生成并填入随机口令，已被 git 忽略，也被排除在 Docker 构建
上下文之外。

| 变量 | 默认值 | 作用 |
| --- | --- | --- |
| `PG_PASSWORD` | 随机生成 | 被插进 `SPINNERET_DATABASE_URL`，同时给 `postgres` 容器。只在数据目录还是空的时候有效：之后再改它，还得同步改数据库里的口令。 |
| `CLICKHOUSE_PASSWORD` | 随机生成 | ClickHouse 同理。 |
| `SPINNERET_ADMIN_USERNAME` | `admin` | 只被一次性的 `init-admin` 服务读一次。 |
| `SPINNERET_ADMIN_PASSWORD` | 随机生成 | 同上。首次登录后就改掉；在你改之前，它也明文躺在这里。 |
| `SPINNERET_PORT` | `8080` | 负载均衡在宿主机上的端口。 |
| `SPINNERET_REPLICAS` | `2` | `spinneret` 服务的副本数。 |
| `VALKEY_IO_THREADS` | `4` | Valkey 的 `io-threads`。`1` 恢复单线程行为——测量数据见[性能与调优 → Valkey io-threads](./17-performance.md#valkey-io-threads)。**`.env.example` 里没有这一项**：它只以插值形式出现在 `docker-compose.yml` 里，要改就自己往 `.env` 里加一行。 |
| `PROMETHEUS_PORT` | `9090` | `observability` profile 里 Prometheus 的宿主机端口。 |
| `MOCK_TARGET_PORT` / `MOCK_PROXY_PORT` | `19090` / `19091` | mock 目标站点和它那个需要认证的代理的宿主机端口。 |
| `EXAMPLE_PORT` | `18000` | 示例爬虫的宿主机端口。 |
| `EXAMPLE_TOKEN` | 空 | 示例爬虫的节点令牌，由 `scripts/example-quickstart.sh` 写入。 |
| `LOADTEST_TOKEN` | 空 | k6 场景用的节点令牌；`spnr seed` 会打印一个。 |
| `K6_SCRIPT` | `acquire_report.js` | `loadtest` profile 跑哪个场景脚本。**`.env.example` 里同样没有这一项**：和 `VALKEY_IO_THREADS` 一样只是 `docker-compose.yml` 里的一个插值，自己往 `.env` 里加。 |

引导式安装脚本最多会往 `.env` 里写三个自己的变量——`SPINNERET_BIND_HOST` 总是写，另外在安装已发布镜像
（而不是从检出的源码构建）时还会写 `SPINNERET_IMAGE` 和 `SPINNERET_IMAGE_TAG`——其余的只从你的 shell 里读：

| 变量 | 默认值 | 作用 |
| --- | --- | --- |
| `SPINNERET_BIND_HOST` | `127.0.0.1` | 负载均衡对外监听的地址，`127.0.0.1` 或 `0.0.0.0`。写进 `.env`，由生成的 `compose.host.yml` 读取。**随包的 `docker-compose.yml` 不读它**：那里发布的是不带主机部分的 `"${SPINNERET_PORT:-8080}:8080"`，所以在手工 Compose 部署里这个变量什么也不做，你需要[三份实战配置](#三份实战配置)里的那个 override。 |
| `SPINNERET_IMAGE` | `tikhubio/spinneret` | 镜像仓库。同一次构建也会推到 GitHub Packages 的 `ghcr.io/tikhub/spinneret`，digest 完全相同，但那边需要登录；按需把这个变量指向它、私有镜像源或 fork。由生成的 `compose.image.yml` 读取。 |
| `SPINNERET_IMAGE_TAG` | `latest` | 镜像标签。生产安装请固定到一个确切的标签。同样由生成的 `compose.image.yml` 读取。 |
| `SPINNERET_PROJECT` | `spinneret` | Compose 项目名，也是查找已有安装的依据。 |
| `SPINNERET_INSTALL_DIR` | root 下是 `/opt/spinneret`，否则是 `~/spinneret` | 安装到哪里。 |
| `SPINNERET_ENABLE_OBSERVABILITY` | `0` | `1` 会加上 Prometheus profile。 |
| `SPINNERET_USE_PUBLISHED` | `1` | `1` 拉已发布镜像，`0` 从检出的源码构建。 |
| `NO_COLOR` | 未设置 | 设成任何值都会关闭彩色输出。 |

`.env.example` 为它定义的每个变量都写了注释，而且 `.env` 就是从它生成的，所以以后往那里加的东西会在下次
安装时自动出现。安装脚本的完整说明见
[安装与部署 → 引导式安装脚本](./02-installation.md#引导式安装脚本)。

---

## 多副本之间：必须相同、必须不同、可以分别调

同一个部署里的各个实例是无状态、可互换的，但它们不是互相独立的。分三类：

### 必须每个实例都相同

| 变量 | 为什么 |
| --- | --- |
| `SPINNERET_DATABASE_URL` | 当然是同一个数据库——但还有一层：两个池子指向不同数据库，会得到两个看起来像一个的互不相交的部署。 |
| `SPINNERET_REDIS_URL` / `SPINNERET_REDIS_ADDRS` | 同一份热状态。 |
| `SPINNERET_REDIS_PREFIX` | 前缀不同，就是同一个 Redis 下的另一份热状态。 |
| `SPINNERET_KEK_FILE` / `SPINNERET_KEKS` / `SPINNERET_KEK_CURRENT` | 缺一把密钥的实例解不开别的实例用那把密钥写的记录，那些读取会失败。把整套密钥分发到每个实例。 |
| `SPINNERET_REPORT_SHARDS` | 租约 ID 里编码了分片号。分片数不一致的实例会写出别人解不开的租约。 |
| `SPINNERET_REPORT_DEDUP_TTL`、`SPINNERET_LATE_REPORT_WINDOW`、`SPINNERET_STREAM_MAXLEN` | 每个实例按自己的值做摄入，而每个分片又有自己的归属者，所以值不一致会让去重、迟到截断和积压上限**取决于是哪个实例正好处理了这条上报**。那是一个不可复现的判定，不是每实例的调优旋钮。 |
| `SPINNERET_RECORD_COOLDOWN_EVENTS` | 由持有该分片的 worker 决定这次冷却要不要写状态事件行。值不一致会让审计线索取决于是哪个 worker 跑的。 |
| `SPINNERET_PROXY_CHECK_URL`、`SPINNERET_PROXY_CHECK_INTERVAL`、`SPINNERET_PROXY_CHECK_TIMEOUT`、`SPINNERET_PROXY_EXIT_IP_URL`、`SPINNERET_GEOIP_DB` | 健康检查器在**每一个**能跑 worker 的实例上都运行，而它们写的是同一批代理行。检测 URL、周期或超时不同，就会出现不同实例对同一个代理给出相反结论，把它在 `可用` 和 `不可用` 之间来回翻——正是[代理健康检查](#代理健康检查与-geoip)那节警告的抖动，而背后并没有任何一个坏代理。 |
| `SPINNERET_CLICKHOUSE_URL`、`SPINNERET_CLICKHOUSE_TTL_DAYS` | 同一张表，只能有一个 TTL。两个值会让启动迁移把 TTL 来回改。 |
| `SPINNERET_RETENTION_*` | 分区任务是 leader 任务，谁拿到锁谁说话——不一致就把设置变成了抽签。 |
| `SPINNERET_ACQUIRE_FLEET_INFLIGHT` | 它是一个集群级预算，由每个实例在本地做除法。值不同就意味着对一个本该只有一个数的预算做出了不同的划分。 |
| `SPINNERET_ACQUIRE_MAX_INFLIGHT` | 要么都固定，要么都不固定。混着来集群会超预算——见 [Acquire 准入控制](#acquire-准入控制)。 |
| `SPINNERET_TRUSTED_PROXIES` | 客户端 IP 的归属不能取决于哪个副本应答了。 |
| `SPINNERET_COOKIE_SECURE`、`SPINNERET_SESSION_TTL` | 一个控制台会话会被负载均衡到别的副本；Cookie 在所有副本上必须是同一个含义。 |

### 必须不同

| 变量 | 为什么 |
| --- | --- |
| `SPINNERET_INSTANCE_ID` | 它是 acquire 注册表的成员名、Redis 流的消费者名，也是任务租约的持有者名。重复会压塌 acquire 的除数，并让两个消费者去抢同一份 pending 列表。**把它留空**，默认值在每台主机上都唯一；真的必须设的时候，按副本模板化。 |

### 可以有意不同

| 变量 | 为什么你会这么做 |
| --- | --- |
| `SPINNERET_ROLE` | 拆分部署的全部意义就在这里。 |
| `SPINNERET_HTTP_ADDR`、`SPINNERET_METRICS_ADDR`、`SPINNERET_PPROF_ADDR` | 不同主机、不同网卡；在一个实例上临时开 pprof 做一次剖析。 |
| `SPINNERET_UI_ENABLED` | 管理入口后面的实例开控制台，只服务节点的实例关掉。 |
| `SPINNERET_TLS_CERT_FILE`、`SPINNERET_TLS_KEY_FILE` | 只有终结 TLS 的实例需要证书。 |
| `SPINNERET_LOG_LEVEL`、`SPINNERET_LOG_FORMAT` | 盯着一个实例看的时候把它调成 `debug`。 |
| `SPINNERET_DATABASE_MAX_CONNS` | `worker` 实例需要比 `api` 实例更大的池子；小的 `api` 机器需要更小的。 |
| `SPINNERET_PAYLOAD_CACHE`、`SPINNERET_PAYLOAD_CACHE_SIZE`、`SPINNERET_DEK_CACHE_SIZE`、`SPINNERET_DEK_CACHE_TTL`、`SPINNERET_TOKEN_CACHE_TTL` | 每实例的内存和每实例能容忍的陈旧度。只跑 worker 的实例几乎用不到载荷缓存。 |
| `SPINNERET_ADMIN_MAX_REQUEST_BYTES` | 只有管理入口后面的实例接管理类 RPC，一次大导入可以专门指向其中一个。 |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 只追踪某一个实例，或者把不同层送到不同的 collector。 |
| `SPINNERET_SHUTDOWN_TIMEOUT` | 跟各自编排系统的强杀超时对齐。 |
| `SPINNERET_MAX_WATCHERS` | 每实例的容量；按实例而不是按集群来定。 |
| `SPINNERET_ALLOWED_ORIGINS` | 只有开发者把 dev server 指向的那个实例需要它。 |

一个好习惯：把"必须相同"的那一组放进一个所有实例都加载的文件，其余放进每实例各自的文件。这样出现分歧
时你看到的是一个 diff，而不是一次发现。

---

## 环境里的机密

本页涉及的机密有四个：PostgreSQL 口令、ClickHouse 口令、密钥加密密钥，以及——在你还没改掉生成值之前
的——管理员口令。

**下面这些永远不要做：**

- 在命令行上敲机密。`SPINNERET_KEKS=k1:… spinneret-server` 会落进 shell 历史和 `/proc/<pid>/cmdline`，
  而且在多数系统上 `ps` 能看到你自己进程的环境变量。
- 把机密烤进镜像。`ENV SPINNERET_KEKS=…` 或者 `COPY` 一个密钥文件，会把它留在任何能 pull 这个镜像的人
  都能读的层里，永久有效——包括你在后面某一层里"删掉"它之后。
- 提交 `.env`。`deploy/compose/.env` 和 `deploy/compose/secrets/` 被 git 忽略、被排除在 Docker 构建上下文
  之外，就是为了这件事；你自己的叠加层也请保持这样。
- 把 KEK 放进会回显命令输出的 CI 变量里。`SPINNERET_KEK_FILE` 加一个挂载的文件，没有任何东西可以被
  echo 出来。

**优先用文件，而不是内联值。** `SPINNERET_KEK_FILE` 指向一个路径，`SPINNERET_KEKS` 本身就带着密钥材料。
文件形式让密钥不进进程环境、不进 `docker inspect`、不进编排系统序列化后的 Pod 规格，也不进任何会序列化
环境变量的崩溃转储。这就是 Compose 编排用 Docker **文件机密**的原因：

```yaml
# deploy/compose/docker-compose.yml
x-spinneret-env: &spinneret-env
  SPINNERET_KEK_FILE: /run/secrets/kek
  # …

services:
  spinneret:
    environment: *spinneret-env
    secrets: [kek]

secrets:
  kek:
    file: ./secrets/kek.key
```

环境里带的是一个路径，密钥以文件形式出现在容器里，对这个服务执行 `docker inspect` 看到的是
`/run/secrets/kek` 而不是 32 字节的 base64。`scripts/compose-init.sh` 会在首次运行时用一把新密钥生成
那个文件，权限是刻意的 `0644`：Compose 把这个文件原样 bind-mount 进容器，而容器跑的是 distroless 的
`nonroot` 用户，它在宿主机上不对应任何用户，也读不了 `0600` 的文件。所以保护应该落在目录上。引导式安装
脚本会做这件事（`chmod 0700 deploy/compose/secrets`），`scripts/compose-init.sh` 不会，所以手工 Compose
部署要自己来：

```bash
chmod 0700 deploy/compose/secrets
```

细节见[安装与部署 → 密钥加密密钥](./02-installation.md#密钥加密密钥)。

**软件本身做了什么来帮你。** 所有会打印配置的地方都会脱敏。`spnr config check` 和启动日志会把任何 URL
或 libpq DSN 里的口令替换成 `xxxxx`，会掩盖查询参数 `password`、`passwd`、`pass`、`pwd`、`secret`、
`token`、`sslpassword`、`access_token`、`api_key`、`apikey`，并且内联密钥材料只报成 `<set>`。KEK 解析
错误只带行号，绝不带那一行的内容。这是安全网，不是策略：一个从来不在环境里的机密，不会因为某处忘了
脱敏而泄露。

这一切所处的威胁模型、以及运维方要负责什么：见[安全](./19-security.md)。

---

## 哪些东西不是环境变量

这是一条刻意划出的线。知道一样东西在哪一边，能省下去错地方找的时间：

| 你想改的东西 | 它住在哪里 |
| --- | --- |
| 身份怎么挑选、租借、冷却、封禁；一个标记意味着什么；熔断器何时熔断、怎么恢复 | **策略**——带版本、运行时发布、一键回滚。[策略](./08-policies.md) |
| 你的节点用什么配置 | **配置中心**——节点长轮询订阅，约一秒生效。[配置中心](./09-config-center.md) |
| 节点需要的凭据，以 `${secret:path}` 引用 | **密钥保管库**。[密钥保管库](./10-secrets.md) |
| 有哪些代理、它们的地区、标签和分配方式 | **代理池**。[代理池](./07-proxies.md) |
| 谁能做什么、一个令牌能碰到什么 | 用户、角色、角色绑定、令牌权限范围和 IP 白名单。[租户、用户与令牌](./11-access-control.md) |
| 告警发到哪里、由什么触发 | 按站点配置的通知渠道和告警规则。[可观测性与告警](./12-observability.md) |
| 一次封禁多久、一次冷却多久 | 是策略，不是 `SPINNERET_RETENTION_*`。[策略](./08-policies.md) |

这些都不需要重启。正是这条分界线，让一条冷却规则可以在几秒内改掉而不用重新部署任何一个节点——也正是
本页能这么短的原因。

---

## 三份实战配置

下面每一份都是真实的 `.env` 片段，**只写和默认值不同的部分**。没列出来的就是默认值；一个够用的默认值
不值得写下来。

### 1. 单机评估

一台机器、Compose 编排、控制台只开在 loopback、通过 SSH 隧道访问。目标是完全不用在调优上花心思。

```bash
# deploy/compose/.env —— 其余全部保持 scripts/compose-init.sh 生成的样子
PG_PASSWORD=<随机生成>
CLICKHOUSE_PASSWORD=<随机生成>
SPINNERET_ADMIN_USERNAME=admin
SPINNERET_ADMIN_PASSWORD=<随机生成>

# 评估用一个副本足够，内存也少一半。
SPINNERET_REPLICAS=1
SPINNERET_PORT=8080
```

随包的 `docker-compose.yml` 发布的是 `"${SPINNERET_PORT:-8080}:8080"`，**不带主机部分**，也就是说这个端口
会开在机器的每一个网卡上。在手工部署里 `SPINNERET_BIND_HOST` 解决不了这件事——只有安装脚本生成的
`compose.host.yml` 才会读它。用一个单服务的 override 自己把它绑住，本页其他 Compose 不传递的变量也都写
进这个文件：

```yaml
# deploy/compose/compose.local.yml
services:
  lb:
    # "!override" 是替换发布端口列表，而不是往上追加：普通合并会给容器端口 8080
    # 留下两条映射，第二条会绑定失败。需要 Compose 2.24 或更新。
    ports: !override
      - "127.0.0.1:${SPINNERET_PORT:-8080}:8080"
```

然后通过隧道访问控制台：

```bash
ssh -L 8080:127.0.0.1:8080 <host>
```

`SPINNERET_COOKIE_SECURE=auto` 在这里是对的，因为控制台是 loopback 上的纯 HTTP；主机有公网出口的话内置
检测 URL 就能用；随包的 `SPINNERET_TRUSTED_PROXIES` 已经覆盖了 Caddy 负载均衡所在的 Compose 网络。

如果这台主机**没有**公网出口，加一行，否则你导入的每个代理都会被标为不可用：

```bash
# 内置 mock target 在编排内部可达（profile test/loadtest/example）。
SPINNERET_PROXY_CHECK_URL=http://mocktarget:9090/healthz
```

启动：

```bash
cd deploy/compose
chmod 0700 secrets
docker compose -f docker-compose.yml -f compose.local.yml up -d --build --wait
docker compose -f docker-compose.yml -f compose.local.yml --profile init run --rm init-admin
docker compose -f docker-compose.yml -f compose.local.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate config check
```

**说明。** 上面这些引导式安装脚本都会替你做——它按 `SPINNERET_BIND_HOST` 写出 `compose.host.yml`、设好
目录权限，还留下一个带好 `-f` 参数的 `spnrctl` 包装脚本。这一份配置走的是手工路径，适合你想把每个文件
都看清楚的主机。

这套流程一步一步、一直做到节点真的完成一次租借和上报的版本：见[快速开始](./01-quickstart.md)。

### 2. 生产环境：负载均衡后面两个副本

还是 Compose，但通过一个终结 TLS 的边缘代理对外发布，指标单独一个端口，保留期按磁盘裁剪过。

```bash
# deploy/compose/.env
PG_PASSWORD=<随机生成>
CLICKHOUSE_PASSWORD=<随机生成>
SPINNERET_ADMIN_USERNAME=ops
SPINNERET_ADMIN_PASSWORD=<随机生成，首次登录后已更换>

SPINNERET_REPLICAS=2
SPINNERET_PORT=8080

# TLS 在边缘终结；明确写死，而不是依赖转发头的启发式判断。
SPINNERET_COOKIE_SECURE=true

# 2 副本 × 48 = 96 条连接，离 max_connections=300 还很宽裕。
SPINNERET_DATABASE_MAX_CONNS=48
```

其余的都写进 override 文件，因为 Compose 编排不会把它们传下去——包括绑定地址和镜像，随包的
`docker-compose.yml` 根本不从 `.env` 读这两项：

```yaml
# deploy/compose/compose.local.yml
services:
  lb:
    # 边缘代理跑在同一台机器上、通过 loopback 连过来，所以编排不能发布到每个网卡上。
    # "!override" 是替换端口列表而不是往上追加；需要 Compose 2.24 或更新。
    ports: !override
      - "127.0.0.1:${SPINNERET_PORT:-8080}:8080"
  # 跑固定标签的已发布镜像，而不是从检出的源码构建，这样你说得清在跑哪个构建，也能把它
  # 放回去。"build: !reset null" 丢掉 build 段，这样一次 compose build 不会悄悄盖掉
  # 拉下来的镜像。引导式安装脚本按 SPINNERET_IMAGE / SPINNERET_IMAGE_TAG 写出的
  # compose.image.yml 就是这个内容。
  migrate:
    image: tikhubio/spinneret:v0.1.0
    build: !reset null
  init-admin:
    image: tikhubio/spinneret:v0.1.0
    build: !reset null
  spinneret:
    image: tikhubio/spinneret:v0.1.0
    build: !reset null
    environment:
      # 把 /metrics 从对外监听上移走，只对监控网络可达。
      SPINNERET_METRICS_ADDR: ":9091"
      # 对外发布的控制台，会话短一些。
      SPINNERET_SESSION_TTL: "4h"
      # 节点在两分钟内就会重试完，留一小时的去重标记纯属浪费内存。
      # 见 documents/zh/17-performance.md。
      SPINNERET_REPORT_DEDUP_TTL: "15m"
      # 分钟级表占 PostgreSQL 磁盘最多；仪表盘看两周足够，
      # 长周期趋势由小时级表保留。
      SPINNERET_RETENTION_MINUTE_STATS: "336h"
      # 要低于编排系统的强杀超时；stop_grace_period 是 40s。
      SPINNERET_SHUTDOWN_TIMEOUT: "35s"
```

```bash
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/compose.local.yml up -d --wait
```

`SPINNERET_INSTANCE_ID` 故意不出现：每个副本的容器主机名会产生各不相同的默认值，这正是 acquire 预算
需要的。`SPINNERET_TRUSTED_PROXIES` 不出现，是因为随包的值已经覆盖了 Compose 网络；如果你的边缘代理
在另一个网段，把那个前缀加进去。

### 3. api / worker 拆分部署

一个手工部署——systemd unit、一对 Kubernetes Deployment，或者任何能调度进程的东西——把请求服务和上报
管道拆开，让上报积压拖不慢 `Acquire`。三个文件：一个公共的，两个按角色的。

```bash
# /etc/spinneret/common.env —— 每个实例都完全相同
SPINNERET_DATABASE_URL=postgres://spinneret@db.internal:5432/spinneret?sslmode=verify-full
SPINNERET_REDIS_URL=rediss://:@valkey.internal:6379/0
SPINNERET_CLICKHOUSE_URL=clickhouse://spinneret@clickhouse.internal:9000/spinneret
SPINNERET_KEK_FILE=/etc/spinneret/kek.key

SPINNERET_REPORT_SHARDS=32
SPINNERET_TRUSTED_PROXIES=10.40.0.0/16
SPINNERET_COOKIE_SECURE=true
SPINNERET_ACQUIRE_FLEET_INFLIGHT=96
SPINNERET_RETENTION_MINUTE_STATS=336h
```

```bash
# /etc/spinneret/api.env —— 4 个 API 实例上，在 common.env 之后加载
SPINNERET_ROLE=api
SPINNERET_HTTP_ADDR=0.0.0.0:8080
SPINNERET_METRICS_ADDR=127.0.0.1:9091
# api 实例服务节点和控制台；它不跑 leader 任务，
# 所以连接池只需要扛住请求并发。
SPINNERET_DATABASE_MAX_CONNS=24
# 4 个 API 实例 × 24 = 96，加上下面的 worker，仍在 max_connections 之内。
SPINNERET_MAX_WATCHERS=40000
```

```bash
# /etc/spinneret/worker.env —— 2 个 worker 实例上，在 common.env 之后加载
SPINNERET_ROLE=worker
# 只有健康检查和指标；没有节点 API，没有控制台。
SPINNERET_HTTP_ADDR=127.0.0.1:8080
SPINNERET_METRICS_ADDR=127.0.0.1:9091
SPINNERET_UI_ENABLED=false
# 每个 leader 任务在担任期间占住一条连接，所以 worker 需要
# api 实例不需要的余量。池子太小启动会被拒绝，并写明最小值。
SPINNERET_DATABASE_MAX_CONNS=48
# 这里不做租借，载荷缓存收益很小。
SPINNERET_PAYLOAD_CACHE_SIZE=20000
# 上报积压刷完需要的时间，比 api 实例排空需要的时间长。
SPINNERET_SHUTDOWN_TIMEOUT=60s
```

这种形态下要注意的几点：

- `SPINNERET_ACQUIRE_FLEET_INFLIGHT=96` 会被除以存活的 **API** 实例数——这里是 4，所以每个 24。worker
  实例不做租借，也不参与计数。扩缩 API 层之后，除法在一个心跳内自动跟上，不需要改配置。
- `SPINNERET_INSTANCE_ID` 一个地方都没设。每个进程主机名不同，所以每个默认值都不同。如果你的调度器
  会让两个进程拿到同一个主机名，就按 Pod 名或 unit 实例名把这个 ID 模板化。
- 负载均衡的健康检查要打 `/readyz`，不是 `/healthz`：`/readyz` 才是在停机排空窗口里转成 `draining`
  的那个，而且它会报告 PostgreSQL、Redis、目录和热状态的就绪情况。
- 如果 TLS 在边缘终结，只有 API 实例需要证书材料；只有它们在有开发者把 dev server 指过来时才需要
  `SPINNERET_ALLOWED_ORIGINS`。
- 在任何东西启动之前，带着每套环境跑一次 `spnr config check`：
  `set -a; . /etc/spinneret/common.env; . /etc/spinneret/worker.env; set +a; spnr config check`。

两层各自的容量规划、以及拆分之后该盯什么：见[运维手册 → 扩容](./16-operations.md#扩容)和
[性能与调优](./17-performance.md)。

---

## 下一步

- [安装与部署](./02-installation.md) —— 这些变量在真实部署里逐个服务地设在哪儿。
- [运维手册](./16-operations.md) —— 会改动其中一部分的日常运维流程。
- [性能与调优](./17-performance.md) —— 哪些变量在压力下真的重要，带测量数据。
- [安全](./19-security.md) —— 本页里那些其实是安全决策的设置。
- [命令行工具](./15-cli.md) —— `spnr config check`、`spnr kek` 和其他所有命令。
- [故障排查](./18-troubleshooting.md) —— 配置合法但还是跑不起来的时候。
