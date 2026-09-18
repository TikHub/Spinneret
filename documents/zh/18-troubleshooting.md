# 故障排查

**按「现象 → 原因 → 解决」组织：Spinneret 部署中真正会发生的故障、完整的错误原因（reason）对照表、如何阅读服务端日志，以及提交缺陷报告时如何采集诊断包。**

[English](../en/18-troubleshooting.md)

---

## 目录

- [开始之前](#开始之前)
- [整个栈起不来](#整个栈起不来)
- [控制台打不开或登录不进去](#控制台打不开或登录不进去)
- [节点收到 no_identity_available](#节点收到-no_identity_available)
- [节点收到 no_proxy_available](#节点收到-no_proxy_available)
- [节点收到 circuit_open](#节点收到-circuit_open)
- [节点收到 scope_missing 或 permission_denied](#节点收到-scope_missing-或-permission_denied)
- [上报被接收了，但什么都没发生](#上报被接收了但什么都没发生)
- [身份池被抽干且再也恢复不了](#身份池被抽干且再也恢复不了)
- [不该被封禁的身份被封禁了](#不该被封禁的身份被封禁了)
- [请求明细报错或为空](#请求明细报错或为空)
- [配置变更到不了节点](#配置变更到不了节点)
- [密钥读不出来](#密钥读不出来)
- [延迟升高或吞吐崩塌](#延迟升高或吞吐崩塌)
- [Redis、PostgreSQL 或 ClickHouse 内存或磁盘耗尽](#redispostgresql-或-clickhouse-内存或磁盘耗尽)
- [升级之后](#升级之后)
- [恢复备份之后](#恢复备份之后)
- [完整的错误原因对照表](#完整的错误原因对照表)
- [阅读服务端日志](#阅读服务端日志)
- [安全地打开调试日志](#安全地打开调试日志)
- [采集诊断包](#采集诊断包)

---

## 开始之前

三条命令就能回答大部分问题。本页所有示例都假定你位于仓库根目录，并且已设置 `COMPOSE`：

```bash
COMPOSE="docker compose -f deploy/compose/docker-compose.yml"
```

```bash
# 1. 哪些容器起来了，哪些是 healthy 的？
$COMPOSE ps

# 2. 服务实例对哪个依赖不满意？
curl -s http://localhost:${SPINNERET_PORT:-8080}/readyz | python3 -m json.tool

# 3. 服务端真正加载的配置，是不是你以为的那份？
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate config check
```

`/readyz` 是整个系统里最有用的一个端点。它返回处理该请求的那个实例上每个依赖的状态：

```json
{
  "status": "ok",
  "checks": {"postgres": "ok", "redis": "ok", "catalog": "ok", "hotstate": "ok"}
}
```

| 检查项 | 什么情况下失败 | 明细字符串 |
| --- | --- | --- |
| `postgres` | 连接池 `Ping` 失败 | `unreachable` |
| `redis` | `PING` 失败 | `unreachable` |
| `catalog` | 内存中的目录从未加载成功 | `not loaded` |
| `hotstate` | worker 实例的 `EnsureBuilt` 尚未完成 | `building` |
| `hotstate` | Redis 中的热状态 epoch 键不存在 | `epoch missing (rebuild pending)` |
| `hotstate` | epoch 查询本身失败 | `unreachable` |

任一检查失败时整个文档返回 HTTP 503（`"status": "unavailable"`）；实例正在关闭时返回 503 和
`{"status": "draining"}`。`/healthz` 只是存活探针：只要进程还能提供 HTTP 服务就返回
`{"status":"ok"}`，不反映任何依赖状态。

**注意。** 多副本时，通过负载均衡器 `curl` 找不出那个坏副本。Caddy 每 2 秒轮询每个副本的 `/readyz`，
不就绪的实例会被摘除，所以你拿到的回应永远来自一个健康实例。要定位出问题的副本，去
`$COMPOSE logs spinneret` 里找 `readiness: … failed`，并按 `instance` 字段分组（两者本页下文都有
说明）；或者用 `$COMPOSE ps` 加 `docker inspect` 逐个查看容器自身的健康检查记录。

**`deploy/compose/.env` 能改什么，不能改什么。** 只有 `deploy/compose/docker-compose.yml` 的
`x-spinneret-env` 锚点真正映射进容器的变量才能从 `.env` 设置：`SPINNERET_LOG_LEVEL`、
`SPINNERET_COOKIE_SECURE`、`SPINNERET_DATABASE_MAX_CONNS`、`SPINNERET_REPORT_SHARDS`、
`SPINNERET_REPORT_DEDUP_TTL` 和 `SPINNERET_PROXY_CHECK_URL`——外加由 Compose 自己读取的
`SPINNERET_PORT`、`SPINNERET_REPLICAS` 以及数据库密码。本页提到的其他 `SPINNERET_*` 变量
（`SPINNERET_UI_ENABLED`、`SPINNERET_ALLOWED_ORIGINS`、`SPINNERET_MAX_WATCHERS`、
`SPINNERET_STREAM_MAXLEN`、`SPINNERET_RETENTION_*` 一组、`SPINNERET_CLICKHOUSE_TTL_DAYS`、
`SPINNERET_RECORD_COOLDOWN_EVENTS`、`SPINNERET_ACQUIRE_FLEET_INFLIGHT`、`SPINNERET_PPROF_ADDR` 等）
都必须先在那个锚点里加一行，只写进 `.env` 不会有任何效果。`SPINNERET_TRUSTED_PROXIES` 更特殊：锚点
里把它写成了硬编码字面量 `172.16.0.0/12,10.0.0.0/8,192.168.0.0/16`，不是 `${...}` 引用，所以根本无法
从 `.env` 覆盖——要放宽它只能改 `docker-compose.yml`。

---

## 整个栈起不来

### `docker compose up` 直接拒绝启动

```text
error while interpolating services.spinneret.environment...: required variable PG_PASSWORD is missing a value: run scripts/compose-init.sh
```

共享的 env 锚点把 `PG_PASSWORD` 和 `CLICKHOUSE_PASSWORD` 标记为必填，报错文案就是上面这句。
`SPINNERET_ADMIN_PASSWORD` 同样必填，但只对 `init` profile 里的 `init-admin` 服务生效，而且跑那个
profile 时报的是另一句话（`set SPINNERET_ADMIN_PASSWORD in .env`）。两种情况的根因一样：
`deploy/compose/.env` 还不存在。执行一次初始化脚本即可，它可以重复执行，已存在的文件会被保留：

```bash
./scripts/compose-init.sh
```

它会从 `.env.example` 生成 `deploy/compose/.env` 并填入随机密码，同时生成
`deploy/compose/secrets/kek.key`（一把新的 KEK），权限 `0644`——容器以 distroless 的 `nonroot`
用户运行，必须能读取这个挂载进去的密钥文件。

### `postgres` 一直不健康

健康检查：`pg_isready -U spinneret -d spinneret`，每 5 秒一次，超时 3 秒，重试 30 次。

| 原因 | 如何判断 | 解决 |
| --- | --- | --- |
| 首次启动之后改过 `PG_PASSWORD` | `$COMPOSE logs postgres` 出现 `password authentication failed for user "spinneret"` | 改回原密码，或在数据库里改密码，或删除 `pgdata` 卷重来（会丢全部数据） |
| 磁盘满 | `could not extend file`、`No space left on device` | 释放 Docker 数据目录所在磁盘空间后重启 |
| 卷是更高版本 PostgreSQL 写的 | `database files are incompatible with server` | 换回创建该卷的镜像版本，或导出后重新导入 |

### `valkey` 一直不健康

健康检查：`valkey-cli ping`，每 5 秒一次，超时 3 秒，重试 30 次。

Valkey 特意使用 `--maxmemory-policy noeviction`：Spinneret 的热状态绝不允许被悄悄淘汰。如果容器被
OOM 杀掉（`$COMPOSE ps` 显示不断重启，`docker inspect` 退出码 137），见
[Redis、PostgreSQL 或 ClickHouse 内存或磁盘耗尽](#redispostgresql-或-clickhouse-内存或磁盘耗尽)。
AOF 文件损坏（`Bad file format reading the append only file`）同样会导致重启循环；由于 AOF 就是
持久化机制本身，这属于恢复而不是修复，见[恢复备份之后](#恢复备份之后)。

### `clickhouse` 一直不健康

健康检查：`wget -qO- http://127.0.0.1:8123/ping`，每 5 秒一次，超时 3 秒，重试 40 次——它的启动
窗口是整个栈里最长的。

健康检查只问 `/ping`，而写入失败时它照样成功——所以 `clickhouse` 不健康意味着服务根本没起来，不是
压力大。两种原因：容器被宿主机 OOM 杀掉（`docker inspect` 退出码 137），或者 `deploy/compose/config/`
下某个配置值在启动时被拒绝。例如把 `background_pool_size` 降到 4 会触发 MergeTree 的合法性检查，服务
以 `BAD_ARGUMENTS`（退出码 36）退出。容器仍然 *healthy* 而每条 INSERT 都失败，是另一类故障，见
[Redis、PostgreSQL 或 ClickHouse 内存或磁盘耗尽](#redispostgresql-或-clickhouse-内存或磁盘耗尽)。

ClickHouse 对*二进制*来说是可选的。不设置 `SPINNERET_CLICKHOUSE_URL` 时，服务端会打印
`clickhouse disabled: raw report events are not stored` 并正常启动；除请求明细以外一切照常。但在自带
的 Compose 栈里，`spinneret` 服务仍然声明了 `depends_on: clickhouse: service_healthy`，所以在那里不带
ClickHouse 运行还要从 `deploy/compose/docker-compose.yml` 里删掉这条 `depends_on`（或整个服务）。

### `migrate` 非零退出

`migrate` 是一次性服务（`restart: "no"`，健康检查已禁用），执行 `spnr migrate up`，必须成功结束
`spinneret` 才会启动。它失败，服务端就永远起不来。

```bash
$COMPOSE logs migrate
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate migrate status
```

`migrate status` 打印已应用版本和二进制内嵌的版本。这里失败几乎总是 PostgreSQL 不可达或配置错误
——`spnr migrate` 只需要 `SPINNERET_DATABASE_URL`。并发执行由 PostgreSQL 顾问锁串行化，所以第二个
`migrate` 容器不是问题所在。

### `spinneret` 启动后立即退出

二进制会打印两类前缀之一，并以状态码 1 退出：

```text
spinneret-server: invalid configuration:
spinneret-server: startup failed: <当时在做什么>
```

| 消息片段 | 含义 | 解决 |
| --- | --- | --- |
| `invalid configuration:` | 一个或多个 `SPINNERET_*` 校验失败，所有问题会逐条列出 | 修 `.env`，参见[配置参考](./03-configuration.md) |
| `connect postgresql` | 连接池打不开 | 检查 `SPINNERET_DATABASE_URL`、`postgres` 容器及密码 |
| `database schema is at version <n>, binary expects <m>: run spnr migrate up` | 有待应用的迁移，但没有给 `--migrate` | 让 `migrate` 服务先跑，或用 `--migrate` 启动 |
| `ensure partitions` | 分区维护器无法创建当期分区 | 检查 PostgreSQL 权限和磁盘空间 |
| `connect redis` | Redis/Valkey 不可达 | 检查 `SPINNERET_REDIS_URL` / `SPINNERET_REDIS_ADDRS` |
| `connect clickhouse` / `migrate clickhouse` | 配置了 ClickHouse 但不可用 | 修好 ClickHouse，或清空 `SPINNERET_CLICKHOUSE_URL` 以不带它启动 |
| `load key-encryption keys` | `SPINNERET_KEK_FILE` 不可读，或密钥内容格式错误 | 确认密钥文件存在且权限为 `0644`；`spnr kek generate` 会打印一行合法的密钥 |
| `load dedupe_pepper system key (is the KEK the one used to initialize this database?)` | KEK 与初始化该数据库时使用的那把不一致 | 恢复原 KEK，参见[密钥保管库](./10-secrets.md) |
| `listen on SPINNERET_HTTP_ADDR` | 地址被占用或无法绑定 | 换 `SPINNERET_HTTP_ADDR`，或释放端口 |
| `load TLS certificate` | `SPINNERET_TLS_CERT_FILE` / `_KEY_FILE` 无法解析 | 修正密钥对，两者必须同时设置 |
| `SPINNERET_DATABASE_MAX_CONNS=… is too small for a worker instance` | worker 角色每个 leader 任务在持锁期间各占一条连接 | 按消息给出的最小值调大连接池 |

**注意。** 数据库 schema 比二进制*更新*是被接受的，只会打出告警
`database schema is newer than this binary`——这是滚动升级进行中的正常现象，不是错误。

### `spinneret` 起来了但一直不健康

镜像的健康检查是 `spnr healthcheck --url http://127.0.0.1:8080/readyz`，每 10 秒一次，超时 3 秒，
启动宽限 20 秒，重试 3 次。容器 running 但 unhealthy，说明就绪检查在失败，直接看它：

```bash
$COMPOSE logs --tail 100 spinneret
curl -s http://localhost:${SPINNERET_PORT:-8080}/readyz
```

首次启动最常见的原因是 `api` 角色实例报 `hotstate: epoch missing (rebuild pending)`，而没有任何
`worker` 角色实例在构建热状态。默认的 `SPINNERET_ROLE=all` 下每个实例都能自己构建。

### `lb` 一直不健康，或返回 503

Caddy 自身的健康检查是 `wget -qO- http://127.0.0.1:8080/healthz`，每 5 秒一次。它要等至少一个
`spinneret` 实例健康才会启动（`depends_on: service_healthy`），所以 `lb` 不健康通常意味着
`spinneret` 不健康，先修那个。

所有副本都被摘除时 Caddy 会返回 `503`。它在一次拨号失败后就把副本摘除（`max_fails 1`）
`fail_duration 2s`，并且每 2 秒轮询一次 `/readyz`，所以报 `draining` 或 `unavailable` 的实例大约
两秒内就会离开转发池。压测中出现成片 503，说明副本在丢失就绪，而不是 Caddy 的问题——逐个检查
`/readyz`。

### `init-admin` 什么都没做

`init-admin` 属于 `init` profile，只有显式调用才会运行：

```bash
$COMPOSE --profile init run --rm init-admin
```

它执行 `spnr admin init --username "$SPINNERET_ADMIN_USERNAME" --password-env SPINNERET_ADMIN_PASSWORD`。
该命令是幂等的：已经存在平台管理员时打印 `already initialized` 并以 0 退出。如果你丢了管理员密码，
看到的就是这句话——这条命令不能再建第二个管理员。唯一的回去路径是：另一个已登录、持有 `user:write`
权限（`owner` 角色）的用户在`用户`页面重置密码；而平台管理员只能由另一个平台管理员来管理，所以如果丢
的正是唯一的那个账号，重置就只能在数据库里手工完成。参见[租户、用户与令牌](./11-access-control.md)。

---

## 控制台打不开或登录不进去

| 现象 | 可能原因 | 如何判断 | 解决 |
| --- | --- | --- | --- |
| 浏览器无响应 / 连接被拒 | `lb` 没运行，或宿主机端口被占 | `$COMPOSE ps lb` | 启动栈；换 `SPINNERET_PORT` |
| 所有控制台路由 HTTP 404 | `SPINNERET_UI_ENABLED=false`，静态控制台未挂载 | `spnr config check` 里能看到这个值 | 改回 `true` 并重启 |
| 所有路由 HTTP 503 | Caddy 后面没有健康副本 | `curl /readyz` | 见[整个栈起不来](#整个栈起不来) |
| 页面能打开，所有 API 调用都 `unauthenticated` | 会话 Cookie 没被浏览器保存 | 开发者工具 → Application → Cookies | 见下面关于 Cookie 的说明 |
| 提示「用户名或密码错误。」 | 凭据错误，或账号被禁用 | 审计日志、`用户`页面 | 重置密码 |
| 提示「失败次数过多，请在 … 后重试。」 | 登录限流 | — | 等待，见下文 |
| 登录成功但每个页面都提示「你没有执行此操作的权限」 | 用户的角色绑定没覆盖当前选中的范围 | `用户`页面 → 角色绑定 | 在正确的租户/命名空间下授予角色 |
| 调用失败并返回 `csrf_missing` | 前置代理把 `X-Spinneret-CSRF` 请求头剥掉了 | 直连实例复现 | 修代理 |

**关于 Cookie。** 会话 Cookie 名为 `spinneret_session`：`HttpOnly`、`SameSite=Strict`、`Path=/`、
`Max-Age = SPINNERET_SESSION_TTL`（默认 `12h`）。它的 `Secure` 属性取决于 `SPINNERET_COOKIE_SECURE`：

- `auto`（默认）——请求经 TLS 到达，或者经由一个发送了 `X-Forwarded-Proto: https`
  **并且**其地址在 `SPINNERET_TRUSTED_PROXIES` 内的反向代理到达时，标记 `Secure`。
- `true`——始终 `Secure`。此时在纯 HTTP 下登录会静默失败，因为浏览器拒绝保存这个 Cookie。
- `false`——从不 `Secure`。仅限纯 HTTP 内网。

如果登录「成功」后控制台立刻弹回登录页，几乎一定是这个原因：要么纯 HTTP 下设了
`SPINNERET_COOKIE_SECURE=true`，要么在终止 TLS 的代理后面用 `auto` 而代理 IP 不被信任，服务端于是
认为请求是纯 HTTP 的。把代理所在网段加进 `SPINNERET_TRUSTED_PROXIES`——在 Compose 栈里这意味着改
`x-spinneret-env` 锚点里的那个字面量，而不是改 `.env`。

**关于 CSRF 请求头。** 所有使用 Cookie 认证的非安全请求都必须携带 `X-Spinneret-CSRF: 1`。控制台会
发送它。任何会剥掉未知请求头的中间件都会让每一次写操作返回 `csrf_missing`（HTTP 403）。

**关于登录限流。** 计数保存在 Redis 中：15 分钟窗口内，同一用户名 **5** 次失败、同一客户端 IP
**20** 次失败即触发，返回 `login_throttled` 并带 `Spinneret-Retry-After-Ms` 提示。管理员重置密码会
清除用户名计数器；从某个地址成功登录一次会释放该 IP 的计数。

**跨域。** 如果控制台与 API 不同源，该来源必须列入 `SPINNERET_ALLOWED_ORIGINS`，否则浏览器会拦截
请求，而控制台只显示一个网络错误，服务端日志里什么都没有。

---

## 节点收到 `no_identity_available`

Connect 码 `resource_exhausted`，HTTP 429，`Spinneret-Retry-After-Ms` 被限制在 50 毫秒到 60 秒之间。
含义是 `acquire.lua` 返回了 `EXHAUSTED`：在等待预算内（`wait_ms`，最长 5 秒），该端点组就绪队列里
没有候选身份通过全部过滤条件。

原因，按出现频率排序：

1. **所有身份都在冷却。** 这是正常健康的情形：抓取速度超出了轮换策略允许的节奏。重试提示就是最早
   一个候选身份恢复可用的时间。
2. **身份都被租走了。** 在 `max_concurrent_leases: 1` 下，正在使用的身份对其他所有租借都不可见。
   用概览页面的 **可用** 指标卡和低水位卡片，对比`身份`页面上 `active` 身份的数量：可用数远低于
   active 数，说明池子在忙，而不是没有身份。身份数量不作为 Prometheus 指标导出，见
   [可观测性与告警](./12-observability.md)。
3. **身份被封禁、隔离、失效或禁用。** 某条信号规则可能封禁得远超预期，见
   [不该被封禁的身份被封禁了](#不该被封禁的身份被封禁了)。
4. **身份所属账号被封禁或在冷却。** 每个候选身份都会检查账号状态，一个被封账号会一次性带走它名下
   的全部身份。
5. **该端点组没有类型匹配的身份**，或者轮换策略里的某个过滤条件（标签、配额、地区）把它们全排除了。
6. **站点根本没有身份**——全新部署，或者导入失败了。
7. **热状态与 PostgreSQL 不一致。** 罕见，也是唯一一种控制台与调度器说法不一的情形：控制台读
   PostgreSQL，调度器读 Redis。

如何区分：

```bash
# 控制台可以直接回答第 1-6 种情况：
#   身份       → 按站点、端点组、状态筛选
#   冷却热力图 → 看清楚是整池在冷却还是本来就空
#   策略       → 绑定到该站点/客户端/端点组的轮换策略
```

```promql
# 多频繁，打在哪个端点组上？
rate(spinneret_acquire_total{result="exhausted", site="example-site"}[1m])
# 是持续饥饿还是突发？和成功的租借对比一下。
rate(spinneret_acquire_total{result="ok", site="example-site"}[1m])
```

可用身份是周期性掉到 0（冷却）还是一直是 0（耗尽），要去概览页面和冷却热力图上看，而不是查
Prometheus：身份相关的 gauge 是预留的，不会导出任何时间序列。

解决手段，按通常适用的顺序：补充身份；调高 `max_concurrent_leases`（它同时也是最大的吞吐杠杆，见
[性能与调优](./17-performance.md)）；放宽轮换策略里的冷却；放宽端点组的身份类型过滤；解封那些被
误封的身份。如果控制台显示身份池健康而调度器仍然说耗尽，重新物化该站点的热状态：

```bash
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  rebuild --tenant default --namespace default --site example-site
```

**注意。** 节点应当把它当作背压而不是错误：遵守 `Spinneret-Retry-After-Ms`，不要比提示更密集地重试。
对着一个被抽干的池狂打只会让情况更糟——一次失败的租借消耗的 Redis 命令数大约是成功租借的五倍。

**它和 `overloaded` 不是一回事。** `overloaded` 表示准入控制在请求到达 Redis 之前就在服务端把它甩掉
了，与身份池无关。两者不会互相掩盖：只有当等待阶梯里*没有任何一次*尝试到达过 Redis 时，被甩掉的请求
才会以 `overloaded` 返回；一旦 `acquire.lua` 已经回答过 `EXHAUSTED` 或 `NO_PROXY`，那个原因就会保留
下来，调用方拿到的是 `no_identity_available` 或 `no_proxy_available`。

---

## 节点收到 `no_proxy_available`

Connect 码 `resource_exhausted`，HTTP 429，带重试提示。`acquire.lua` 找到了身份但挂不上代理：站点或
端点组要求代理，而没有代理通过过滤。

| 原因 | 如何判断 | 解决 |
| --- | --- | --- |
| 健康检查把所有代理标成了 `dead` | `代理`页面，或 `spinneret_proxies{site,state}` | 见下文 |
| 代理在按站点冷却 | 代理详情 → 各站点冷却 | 放宽动作策略，或补充代理 |
| 代理过滤条件（种类、地区、供应商、标签）匹配不到任何代理 | 轮换策略里的代理选择器 | 放宽条件，或给更多代理打标签 |
| 身份绑定到了一个不可用的代理 | 身份详情 → 绑定的代理 | 解绑，或修好那个代理 |
| 所有代理都达到了 `max_concurrency` | `代理`页面 | 调高上限（`0` 表示 1；更新时接受 1–100000），或补充代理 |

健康检查这个坑值得说清楚。检查器每 `SPINNERET_PROXY_CHECK_INTERVAL`（默认 60 秒）**穿过每个代理**
去拉取 `SPINNERET_PROXY_CHECK_URL`，超时 `SPINNERET_PROXY_CHECK_TIMEOUT`（默认 10 秒）。默认 URL 是
一个公网地址。在没有外网出口的主机上，所有代理都会检查失败并被标成 `dead`——包括那些对你真正的目标
站点完全可用的代理。把检查地址指到可达的地方：

```bash
# deploy/compose/.env
SPINNERET_PROXY_CHECK_URL=http://mocktarget:9090/healthz
```

代理状态有 `active`、`disabled`、`dead`、`banned`、`quarantined`、`retired`，只有 `active` 可被分配。
参见[代理池](./07-proxies.md)。

---

## 节点收到 `circuit_open`

Connect 码 `unavailable`，HTTP 503，带重试提示（至少 1 毫秒；熔断器返回打开窗口的剩余时间，无期限的
打开则返回 60 秒）。该端点组的熔断器处于熔断状态——这是有意为之，因为熔断策略判定当前对这个端点组的
请求正在失败。

在`熔断器`页面判断属于哪一种：

1. **自动熔断。** 熔断策略的阈值被击穿。页面会显示触发原因和时间窗口。这是系统在正常工作：正确的
   做法是让底层请求恢复成功，而不是强行关闭熔断器。
2. **手动熔断。** 有人从控制台或 API 打开了它——转换记录的触发方式是 `manual` 并带有操作者。不带
   时长的手动熔断会一直保持到被手动关闭。
3. **半开探测。** 状态为 `half_open`：放行少量探测租约，其余仍返回 `circuit_open`。这是恢复路径，
   会自行结束。

```promql
spinneret_breaker_state{site="example-site"}        # 0 关闭，1 半开，2 熔断
rate(spinneret_breaker_transitions_total[5m])       # 标签 "to" 是目标状态
```

**`site_paused` 不是同一回事。** 如果整个站点被暂停（`熔断器`页面上的站点开关），所有租借都会以
`site_paused` 失败，码为 `unavailable`，重试提示固定为 30 秒。取消暂停即可。

熔断策略字段见[策略](./08-policies.md)，告警见[可观测性与告警](./12-observability.md)。

---

## 节点收到 `scope_missing` 或 `permission_denied`

两者的 Connect 码都是 `permission_denied`，HTTP 403，但含义不同：

| 原因 | 什么时候产生 |
| --- | --- |
| `scope_missing` | **API 令牌**的权限范围不足以覆盖目标资源所需的权限，或该令牌对请求中指定的命名空间无效 |
| `permission_denied` | **控制台用户**的角色绑定没有授予该资源上的权限 |

节点用令牌认证，所以节点拿到的是 `scope_missing`。节点需要的权限范围：

| 调用 | 权限范围 | 可选后缀 |
| --- | --- | --- |
| `Acquire`、`AcquireBatch`、`Renew`、`Release` | `lease:acquire` | `:<site>` |
| `Report`、`ReportBatch` | `report:write` | `:<site>` |
| `GetConfig`、`WatchConfig` | `config:read` | `:<group glob>` |
| `GetSecret` | `secret:read:<namespace>/<path glob>` | 必填 |

查看令牌实际拥有什么：

```bash
# 控制台：访问控制 → 令牌 → 选中令牌 → 权限范围、命名空间、IP 白名单。
# 新建一个权限正确的令牌：
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate token create \
  --tenant default --namespace default --name crawler-01 \
  --scope lease:acquire --scope report:write --scope config:read --expires 720h
```

三个坑：

- **后缀是收窄，不是放宽。** `lease:acquire:search` 对站点 `detail` 不授予任何权限；不带后缀的
  `lease:acquire` 覆盖该令牌所属命名空间下的全部站点。
- **一个令牌只属于一个命名空间。** 请求里指定了别的命名空间时，即使权限范围看起来没问题，也会
  返回 `scope_missing`。
- **`ReportBatch` 是逐条拒绝的。** 如果批量里有*一条*上报所属站点缺少 `report:write`，接口仍返回
  HTTP 200，只是那一条出现在 `rejected[]` 中、原因为 `scope_missing`，其余照常接收。只看 HTTP 状态
  码的节点永远发现不了。

`ip_not_allowed`（同样是 `permission_denied`，HTTP 403）是另一项检查：令牌配置了 IP 白名单，而客户端
地址不在其中。在反向代理后面，只有直连对端位于 `SPINNERET_TRUSTED_PROXIES` 内时才会采信
`X-Forwarded-For`，否则所有节点看起来都来自代理的地址。在 Compose 栈里这个变量是
`x-spinneret-env` 锚点中的字面量，要在那里放宽，而不是在 `.env` 里。

参见[租户、用户与令牌](./11-access-control.md)。

---

## 上报被接收了，但什么都没发生

`Report` 返回成功，请求明细里能看到这条上报，但没有任何身份被冷却、封禁或改分。按顺序排查：

1. **影子模式。** 处于 `shadow` 模式的动作策略会规划出全部动作但一个都不执行。状态事件会以
   `shadow=true` 记录下来，所以你能准确看到「本来会发生什么」。`策略`页面显示模式，
   `spinneret_actions_total{mode="shadow"}` 统计数量。
2. **没有 worker 在运行。** 上报由 API 写入 Redis 流，由 worker 消费。如果所有实例都是
   `SPINNERET_ROLE=api`，就没有任何东西消费这些流。默认角色是 `all`。用
   `spinneret_stream_owned_shards`（各实例之和应等于 `SPINNERET_REPORT_SHARDS`，默认 16）和持续增长的
   `spinneret_stream_pending{shard}` 确认。
3. **worker 落后了。** `spinneret_report_lag_seconds` 是从接收到处理的延迟。高负载下达到秒级是容量
   问题，不是正确性问题，见[延迟升高或吞吐崩塌](#延迟升高或吞吐崩塌)。
4. **没有信号规则命中。** 信号策略把状态码、延迟和标记翻译成判定结果；没有规则命中就是默认判定结果，
   动作策略也就无事可做。在`策略`页面用规则调试器输入你真实发出的那条上报试一下。
5. **这是一条重复上报。** 在 `SPINNERET_REPORT_DEDUP_TTL`（默认 `1h`）内出现过的 `report_id` 会被
   计为重复并丢弃，`spinneret_report_ingest_total{result="duplicated"}` 统计它们。SDK 每次尝试都会
   生成新的 `report_id`；在重试时复用同一个 ID 的节点，第一条之后的全部会被静默丢掉。
6. **上报在一个整体成功的批次里被拒绝了。** `ReportBatch` 返回逐条结果，检查 `rejected[]`，
   `spinneret_report_ingest_total{result="rejected"}` 统计它们。
7. **上报太晚了。** 租约结束超过 `SPINNERET_LATE_REPORT_WINDOW`（默认 `10m`）之后到达的上报不会再
   作用到身份上。
8. **绑定到该端点组的动作策略对这个判定结果什么都不做。** 检查`策略`页面上的绑定层级——更具体层级
   上的绑定是*替换*你改的那条，不是与之合并。

---

## 身份池被抽干且再也恢复不了

区分性现象是：概览页面的 **可用** 指标卡掉到 0 并一直保持，流量解释不了这一点，而控制台的
`身份`页面显示大部分身份仍是 `active`。这些身份是被*租走*了，而没有任何东西在结束这些租约。

| 原因 | 如何判断 | 解决 |
| --- | --- | --- |
| 节点只租借，从不归还也不上报 | `spinneret_lease_reaped_total{kind="abandoned"}` 上涨而 `spinneret_report_ingest_total{result="accepted"}` 平坦——租约是被回收器结束的，不是被 `Release`/`Report` 结束的。该指标只有 `expired` 和 `abandoned` 两个 kind，不存在 `released` 这条时间序列 | 修节点：在 `finally` 里务必 `Release` 或带 `release: true` 上报 |
| 上报 worker 停了或卡住了 | `spinneret_stream_pending` 增长，`spinneret_report_lag_seconds` 增长 | 重启 worker 实例，见[运维手册](./16-operations.md) |
| 有人手动删除了 Redis 里的租约键 | 其他原因都解释不通 | 见下文重建 |
| 租约太长而池子太小 | 轮换策略里的 `lease_ttl`（默认 120 秒，范围 5 秒–30 分钟）和 `max_lease_lifetime` 与池规模对比 | 缩短租约，或扩大池子 |

租约回收器在每个实例上每秒运行一次，结束已过期或被遗弃的租约，所以一个池子空置超过一个租约 TTL，
不会是回收器的问题。

**警告。** 绝不要手动删除活跃的租约键（`<prefix>:{s<siteKey>}:ls:*`）。租约哈希正是租约结束时用来递减身份
活跃租约计数的东西；删掉一个就会把计数泄漏，该身份将*永远*不再可用。这不是理论推演——压测中做过一次，
结果 100,000 个身份里有 97,520 个被永久占租，之后每次运行都崩进 `resource_exhausted`。裁剪一条仍有
未处理 `release: true` 上报的流，后果完全相同。

修复办法是清掉整个站点前缀，再从 PostgreSQL 重新物化。一个站点的所有键都在
`<prefix>:{s<siteKey>}:` 下，其中 `<prefix>` 是 `SPINNERET_REDIS_PREFIX`（默认 `sp`），
`<siteKey>` 是站点的**数字**键——不是站点名。这个数字键就是服务端日志记录里的 `site_key` 字段
（见[阅读服务端日志](#阅读服务端日志)）。默认前缀、站点键为 12 时，模式就是 `sp:{s12}:*`：

```bash
# 1. 先数一下键的数量。数出 0 说明模式写错了，而不是站点本来就干净——写错的模式匹配不到任何东西，
#    而且是静默失败。
$COMPOSE exec -T valkey sh -c \
  "valkey-cli --scan --pattern 'sp:{s12}:*' --count 5000 > /tmp/k; wc -l < /tmp/k"

# 2. 数量合理的话，彻底移除该站点的热状态。
$COMPOSE exec -T valkey sh -c "xargs -a /tmp/k -n 1000 valkey-cli unlink"

# 3. 重建。
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  rebuild --tenant default --namespace default --site example-site
```

`spnr rebuild --site` 本身**不会**重置运行时计数器——这是设计如此，重建不能丢掉在用的租约。清除计数
的是那次 unlink。此外，重建后的站点是冷的：其就绪分数被均匀铺开到接下来的 60 秒内，头一百万次左右
的租借效率不如热站点。

不带 `--site` 时，`spnr rebuild` 会删除热状态 epoch 并重建全部站点；在此期间 API 实例报告未就绪
（`epoch missing (rebuild pending)`），Caddy 会把它们摘出转发池。请据此安排窗口。

---

## 不该被封禁的身份被封禁了

身份状态机只会因动作而变化，而每个动作都有记录。从记录出发，不要从策略出发。

1. 打开`身份` → 该身份 → **状态时间线**。每次转换都会写明动作、触发它的上报、命中的规则和操作者。
2. 如果操作者是 `system`，就是某条信号规则命中了。打开`策略` → 绑定到该端点组的信号策略，找到那条
   规则，用规则调试器针对出问题的那条上报重放一遍。
3. 如果操作者是某个用户，那就是一次人工操作。

常见元凶：

- **规则把本不是封禁的 HTTP 状态码当成了封禁。** `429` 和 `503` 是限流；把它们分类成封禁，会在目标
  站点任何一次故障期间把整个池子烧掉。
- **标记太宽。** 节点在任何非 200 响应上都设置的标记，会把临时故障一并命中。
- **阈值的统计窗口太长**，于是一次短暂故障积累出足够多的连续失败，把所有身份都封掉。
- **动作的作用范围搞错了。** 作用于账号的动作会封掉该账号下的所有身份；作用于代理的动作会封掉共用
  该代理的一切。

解决：

- **撤销处置。** `身份`页面有一个**撤销处置**对话框。它不是靠勾选身份来驱动的：你描述损害范围，它
  自己去找这些身份。**时间范围是必填的**（开始时间为空会被拒绝，这样一次撤销绝不会悄悄覆盖全部历史），
  还可以按站点、策略 ID、规则名，以及要撤销 `ban`、`quarantine`、`expire`、`cooldown` 中的哪几种进一步
  收窄——一个都不选就是撤销全部四种。先跑**试运行**：它只列出将被影响的身份，什么都不改。只有由系统
  记录的事件（`shadow=false`、操作者 `system`）会被触及。被撤销的身份回到 `pending`，而不是 `active`；
  被撤销的冷却会被清除。`reset_failures` 和 `reset_health` 是两个独立的可选项，分别额外清零连续失败
  计数和把健康分恢复到基线。
- **先走影子模式。** 把修正后的信号或动作策略以 `shadow` 模式发布，让它跑在真实流量上，把
  `spinneret_actions_total{mode="shadow"}` 与你的预期对比无误后，再切到 `enforce`。
- **版本回滚。** 策略是带版本的，`策略`页面可以比对两个版本并一步回滚到上一版。

参见[策略](./08-policies.md)与[身份与账号](./06-identities.md)。

---

## 请求明细报错或为空

`请求明细`页面从 ClickHouse 读取原始上报事件。控制台其余部分都读 PostgreSQL，所以只有这一个页面会
单独失败。

| 你看到的 | 原因 | 解决 |
| --- | --- | --- |
| 「服务不可用 — request events are unavailable: ClickHouse is not configured」 | `SPINNERET_CLICKHOUSE_URL` 为空；服务端启动时会打印 `clickhouse disabled: raw report events are not stored` | 配置 ClickHouse 后重启 |
| 「请求超时」（`query_timeout`） | 查询超过了单次调用 30 秒的 ClickHouse 预算 | 缩小时间范围，加上站点/判定结果筛选 |
| 「请求过于频繁」（`query_too_large`） | 查询需要超过单查询 512 MiB 的内存上限，或超出 ClickHouse 允许的行数/字节数 | 同上：缩范围、加筛选 |
| 时间范围报「请求参数无效」 | 范围超过请求事件的 7 天上限 | 拆成多次查询 |
| 空，但没有报错 | 该范围内没有事件，或时间范围在未来，或所选范围内没有可读站点 | 放宽范围；检查范围切换器 |
| 上报明明在被接收，页面却是空的 | 上报已入队但未被处理，或 ClickHouse 写入器在失败 | 看 `spinneret_report_lag_seconds`、`spinneret_db_write_batches_total{writer,result="error"}` |

聚合类页面（概览、各站点卡片、冷却热力图、风险事件）来自 PostgreSQL，时间范围上限为 31 天；
ClickHouse 挂掉时它们照常工作。

**注意。** `request events are unavailable` 返回的 Connect 码是 `unavailable`（HTTP 503），但携带的
原因是 `failed_precondition`。如果你要对它做自动化处理，请匹配原因而不是状态码。

---

## 配置变更到不了节点

节点用 `GetConfig` 读配置，用 `WatchConfig` 订阅——这是一个长轮询：有变更时立即返回，否则最多挂起
60 秒。实测从发布到唤醒的端到端延迟远低于 100 毫秒，所以「节点没察觉」绝不正常。

| 原因 | 如何判断 | 解决 |
| --- | --- | --- |
| 变更还停留在草稿 | `配置中心`显示该配置项有未发布的改动 | 发布它 |
| 节点订阅的是别的分组或配置项键 | 对比节点的订阅请求与配置项的分组和键 | 修节点 |
| 令牌的 `config:read` 通配符没覆盖该分组 | 调用会以 `scope_missing` 失败，而不是静默 | 重新签发令牌 |
| 节点根本没有调用 `WatchConfig` | 看各实例的 `spinneret_config_watchers` | 修节点 |
| 触达订阅者上限 | `WatchConfig` 返回 `rate_limited`，重试提示 1 秒 | 调高 `SPINNERET_MAX_WATCHERS`（默认每实例 20,000），或加副本 |
| 代理缓冲了长轮询 | 节点只在每 60 秒整齐地看到变更 | 自带的 Caddyfile 设置了 `flush_interval -1`；换别的代理需要等价配置 |
| 节点缓存了值并且不再重读 | 只有节点自己的日志能看出来 | 修节点 |

**策略**（轮换、信号、动作、熔断）变更走的是另一条路：策略在目录（catalog）里，不在配置中心。实例
收到失效事件后重载该命名空间的目录（100 毫秒去抖），并每 60 秒做一次全量重载作为兜底；此外每个管理
请求在执行前都会同步目录，所以控制台总能看到自己刚写入的内容。如果一条已发布的策略几秒后仍未在节点
上生效，到服务端日志里找 `catalog: full reload failed` 或 `catalog: reload after invalidation failed`
——重载持续失败的命名空间会一直沿用上一份成功加载的快照。

参见[配置中心](./09-config-center.md)与[节点 API 参考](./13-node-api.md)。

---

## 密钥读不出来

| 现象 | 原因 | 解决 |
| --- | --- | --- |
| 节点收到 HTTP 403、`scope_missing` | 令牌没有覆盖该路径的 `secret:read:<namespace>/<path glob>` | 用正确的通配符重新签发令牌 |
| 控制台提示「你没有执行此操作的权限」 | 用户在该命名空间缺少 `secret:reveal` | 授予含该权限的角色 |
| HTTP 404、`not_found` | 该路径没有密钥，或没有该版本 | 在`密钥`页面核对路径和版本 |
| HTTP 500、`internal`，服务端日志显示解密失败 | 用任何已配置的 KEK 都解不开这把 DEK | 见下文 |
| 启动失败于 `load dedupe_pepper system key (is the KEK the one used to initialize this database?)` | KEK 与数据库不匹配 | 见下文 |
| 配置项里仍然是字面量 `${secret:...}` | 管理端读取按设计返回原始内容；节点读取要么替换要么失败 | 见下文 |

**关于 KEK。** 密钥采用信封加密：每条密钥用一把数据加密密钥（DEK）加密，每把 DEK 又用一把密钥加密
密钥（KEK）包裹，KEK 从 `SPINNERET_KEK_FILE`（Compose 栈把 `deploy/compose/secrets/kek.key` 挂载到
`/run/secrets/kek`）或 `SPINNERET_KEKS` 加载。如果某把曾包裹过 DEK 的 KEK 从配置里被移除，对应的密钥
就再也解不开。查看什么被什么包裹着：

```bash
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate kek status
```

它会列出已配置的 KEK、各自包裹的记录数以及重新包裹的进度。把缺失的那行 KEK 补回去、重启，然后用当前
KEK 重新包裹全部数据密钥：

```bash
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate kek rewrap
```

**配置中的 `${secret:...}`。** 配置项可以用 `${secret:<path>}` 或 `${secret:<path>#<version>}` 引用
密钥。解析发生在*节点*读取配置项的时刻，使用节点自己的身份主体——所以令牌缺少该路径 `secret:read`
的节点会收到错误，而不是一个悄悄没被替换的字符串。节点读取要么把每个引用都替换掉，要么整个请求失败：
节点无权读取被引用的密钥时返回 `permission_denied`，密钥不存在时返回 `failed_precondition`。

所以看到字面量 `${secret:...}`，几乎总是因为你看的是一次**管理端**读取：管理端读取按设计总是返回原始
内容，控制台编辑器里显示的就是它。引用扫描对每种格式都生效（`json`、`yaml`、`text` 一视同仁），并且
**没有转义语法**——每个 `${secret:` 序列都必须构成一个合法引用，不合法的在草稿保存时就会被拒绝，发布
时还会再校验一次。要看*节点*实际拿到什么，用节点自己的令牌调一次 `GetConfig`。

每一次读取尝试——允许、拒绝或失败——都会以 `secret.read` 写入审计日志，带上用途、版本和客户端 IP。
这是判断节点的调用到底有没有到达服务端最快的方式。参见[密钥保管库](./10-secrets.md)。

---

## 延迟升高或吞吐崩塌

Spinneret 越过拐点后不是优雅降级，而是崩塌。认出这个特征比记住任何单个数字都重要。

**特征。** 租借 p99 跳升一个数量级，`spinneret_report_lag_seconds` 从几十毫秒变成几秒，
`spinneret_acquire_total{result="exhausted"}` 陡增，同时 Redis CPU 很高而*有效*吞吐在下降。回路是：

1. Redis 饱和，上报 worker 落后；
2. 租约因此得不到释放，身份一直处于被租状态；
3. 租借采样到这些被租身份、拒绝它们，并把它们的就绪队列分数往后推——每次一个 `HGET` 加一个 `ZADD`；
4. 一次失败的租借要走访的候选远多于成功的租借（实测 220 条 Redis 命令对 49 条），这又把 Redis 推得
   更饱和。

这是一个正反馈回路，不是 CPU 天花板。一旦启动，必须把负载压到拐点以下才能解开。

**准入控制给这个回路加了上限。** 每个实例都会限制自己在 Redis 上同时在飞的 `acquire.lua` 调用数量，
超出的部分在服务端被甩掉，而不是把并发倍增到 Redis 上。`SPINNERET_ACQUIRE_FLEET_INFLIGHT`（默认
`64`，范围 0–65536）是*整个集群*的预算：单个实例准入的数量是这个预算除以它看到的存活 API 实例数，并
夹在 `[4, 4096]` 内。`SPINNERET_ACQUIRE_MAX_INFLIGHT`（默认 `0`，表示按集群预算推导，最大 4096）则直接
钉住单实例的上限，并关掉用来统计同伴数量的心跳。把 `SPINNERET_ACQUIRE_FLEET_INFLIGHT` 设为 `0` 会关闭
准入控制，恢复原来无上限的行为。一次尝试最多等 50 毫秒（且绝不超过它自己剩余的 `wait_ms` 预算）来拿
许可；拿不到就以 `overloaded` 原因（码 `unavailable`，HTTP 503）被甩掉，重试提示在 100–200 毫秒之间
抖动。被甩掉的请求完全没有发出任何 Redis 命令，这正是要点：它把正反馈回路变成了普通的背压。批量请求
的许可按 `count` 加权，所以 `AcquireBatch` 承担的是它真正消耗的 Lua 工作量。

在读指标之前，关于 `overloaded` 有两点需要先知道：

- 它的含义是*“该实例已到上限，或者有调用方排在你前面”*，而不是严格的“那一刻在飞数正好等于上限”。
  不带等待预算的调用方永远不会进入等候室，所以在那些愿意等待的调用方还排在前面时，它就会被甩掉。
  这个窗口只有一个排空周期（大约一次 Redis 往返），是先进先出规则的代价——正是这条规则让已经在等的
  调用方不会被新到达者饿死。
- 由于所有 SDK 的 `wait_ms` 默认都是 `0`，**对默认流量来说 `spinneret_acquire_queued` 恒为 0**，
  无论实例甩得多凶：根本没有请求被挂起。只有当调用方显式传 `wait_ms > 0` 时它才会非零。默认客户端
  看 `shed_no_wait`，会等待的客户端看 `queued` 和 `shed_queue_full`。

两个 SDK 都会自动重试 `unavailable`，而重试会重新进入请求的*未受管控*的前半段：认证、按令牌的限流、
目录与端点组解析。因此在大量甩请求期间，前门的请求速率会按重试倍数上升，某个租户可能在低于其配置
RPS 的负载下就开始看到 `resource_exhausted/rate_limited`。这两者要一起看：需要盯的比值是客户端请求
速率对 `rate(spinneret_acquire_admission_total{result=~"shed.*"}[1m])`。

| 指标 | 说明 |
| --- | --- |
| `spinneret_acquire_admission_total{result}` | 准入判定，每次*尝试*记一条：`immediate`（有空闲许可）、`queued`（挂起后获准）、`shed_no_wait`（无许可且无等待预算——即默认客户端）、`shed_queue_full`（无许可且 4×上限 的等候室已满）、`shed_timeout`（挂起后预算耗尽）、`shed_canceled`（调用方已离开）。甩请求比率是 `rate(…{result=~"shed.*"}[1m]) / rate(…[1m])`，分母取*全部*标签——按尝试计，不是按请求计，所以 `spinneret_acquire_total` 不是正确的分母 |
| `spinneret_acquire_admission_wait_seconds` | 尝试等待许可的时长 |
| `spinneret_acquire_inflight` | 本实例上在飞的脚本调用数；跨副本 `sum()` 就是整个集群压在 Redis 上的并发 |
| `spinneret_acquire_queued` | 本实例上正在等待许可的尝试数——除非调用方传了 `wait_ms > 0`，否则恒为 0 |
| `spinneret_acquire_inflight_limit` | 本实例当前的准入上限。`sum(spinneret_acquire_inflight_limit) > 256` 说明单实例下限 4 已经反超了集群预算：超过约 16 个副本后，集群总量又变成 4 × 副本数。此时应给 Redis 分片，或把 `SPINNERET_ACQUIRE_FLEET_INFLIGHT` 调低 |
| `spinneret_acquire_peers` | 集群预算被除以的存活 API 实例数。请与被抓取的 API 角色实例数对照（自带 Compose 栈里就是 `count(up{job="spinneret"})`）：两者不一致是区分“只有一个副本”和“分摊坏了”的唯一信号 |
| `spinneret_acquire_peer_beat_age_seconds` | 这个计数的年龄。它从进程启动开始增长，所以从未成功过的心跳不可能看起来是新鲜的 |
| `spinneret_acquire_peer_beat_failures_total` | 失败的心跳次数。心跳失败期间计数会冻结在上一个值上，这只会收窄上限，绝不会放宽 |
| `spinneret_acquire_script_seconds` | `acquire.lua` 在 Redis 上的往返耗时；与 `spinneret_acquire_duration_seconds` 共用分桶，两者之差就是等待阶梯加渲染 |
| `spinneret_acquire_total{result="overloaded"}` | 以甩掉结束的租借次数 |

| 观察到的现象 | 含义 | 该做什么 |
| --- | --- | --- |
| `exhausted` 上涨 | 该端点组的身份池空了——开启准入控制后，`exhausted` 只表示身份池耗尽，绝不表示过载 | 增加身份、放宽轮换策略，或缩短 `max_concurrent_leases` 的持有；过载请改看 `overloaded` |
| `report_lag_seconds` 到秒级 | worker 排不空流 | 增加 worker 实例（各自拥有 16 个分片中的一部分），或降负载 |
| `overloaded` 上涨、Redis CPU 还没到顶，**且** `spinneret_acquire_script_seconds` 的 p99 正常 | 准入控制在甩请求：单实例上限比 Redis 实际能吃下的更窄 | 调高 `SPINNERET_ACQUIRE_FLEET_INFLIGHT`，或用 `SPINNERET_ACQUIRE_MAX_INFLIGHT` 钉一个实测值 |
| `overloaded` 上涨、Redis CPU 很低，**但** `spinneret_acquire_script_seconds` 的 p99 飙升 | Redis 卡住了，不是上限太窄。一次卡顿会把所有许可占用长达 2 秒的脚本超时，而 Redis CPU 却是空闲的，症状看起来完全一样 | 先修 Redis（AOF 重写 fork、swap、慢 `SAVE`、邻居抢资源）。**不要调高预算**——那会重新打开崩塌回路 |
| 多副本集群里每个副本的 `spinneret_acquire_peers` 都是 1 | 要么心跳一直失败，要么多个副本共用了同一个 `SPINNERET_INSTANCE_ID`，在注册表里合并成了一个成员——于是每个副本都准入*整个*集群预算 | 先看 `spinneret_acquire_peer_beat_failures_total` 和 `spinneret_acquire_peer_beat_age_seconds`。如果心跳正常，就是实例 id 重复了：不要设置 `SPINNERET_INSTANCE_ID`，让基于主机名的默认值生效 |
| 集群总并发超过了配置的预算 | 一部分 API 实例钉了 `SPINNERET_ACQUIRE_MAX_INFLIGHT`，另一部分在推导。钉住的实例不会注册，于是推导的实例除以了一个偏小的数，钉住的那份配额又叠加在上面 | 要么*所有* API 实例都钉，要么都不钉 |
| 加副本反而*更糟* | 关掉准入控制（`SPINNERET_ACQUIRE_FLEET_INFLIGHT=0`）时，两个副本对同一个 Redis 施加双倍并发，比一个副本更早崩塌。打开它之后，集群预算会在存活实例之间分摊，所以多加副本只增加服务端容量，不增加 Redis 并发 | 为租借吞吐扩 Redis，而不是扩副本；并保持准入控制开启 |
| 刚种子化或刚重建的站点很早就崩 | 冷站点的就绪分数是相关的 | 先用流量预热再压测 |
| Valkey 每约 52 秒卡顿一次 | AOF 重写 fork | 使用自带的 Valkey 参数（`auto-aof-rewrite-percentage 300`、`auto-aof-rewrite-min-size 1gb`、`--save ""`） |
| 尾延迟差但吞吐正常 | Valkey `io-threads` | 自带默认值是 4；`VALKEY_IO_THREADS=1` 可退回单线程行为 |
| 负载均衡器成片返回 HTTP 503 | 副本被被动健康检查摘除 | 见上文 `lb` 一节 |

各项杠杆按收益排序列在[性能与调优](./17-performance.md)。这里最要紧的两条：`max_concurrent_leases`
（`1` 时单实例可持续约 4,500 次租借→上报/秒，`4` 时约 5,500 且降级平缓得多），以及 Redis 容量
（大致按每 4,500 次/秒配一个 Redis 主节点估算）。

**性能剖析。** 设置 `SPINNERET_PPROF_ADDR`（例如 `127.0.0.1:6060`；在 Compose 栈里需要先往
`x-spinneret-env` 锚点加一行）会在独立监听器上提供
`/debug/pprof/`。它默认关闭，绝不会挂到 API 或指标监听器上，启用时服务端会打印告警。这些端点无需
认证且会暴露堆内容和 goroutine 栈——永远不要把这个端口对外开放。如果 pprof 地址与 API 或指标地址
相同，配置会直接被拒绝。

---

## Redis、PostgreSQL 或 ClickHouse 内存或磁盘耗尽

### Redis / Valkey

Valkey 使用 `--maxmemory-policy noeviction`：Spinneret 的热状态绝不允许在它背后被淘汰。内存耗尽时
写入会明确失败，而不是悄悄弄坏身份池；如果容器被宿主机 OOM 杀掉，你会看到退出码 137 和重启循环。

真正把 Redis 撑满的不是数据集，而是与流量成正比的状态：

```text
工作集 ≈ 数据集
       + 上报数/秒 × SPINNERET_REPORT_DEDUP_TTL × 80 B    （去重标记）
       + 租借数/秒 × SPINNERET_REPORT_DEDUP_TTL × 290 B   （已结束租约哈希）
       + 积压条目数 × 440 B                                （上报流）
```

在 4,500 次/秒、默认 1 小时去重 TTL 下，这就是约 1.2 GiB 的去重标记加 4.4 GiB 的已结束租约哈希——
远超一个 100,000 身份数据集本身（约 350 MiB）。

最大的杠杆是 `SPINNERET_REPORT_DEDUP_TTL`。节点的重试是以秒计的，不是以小时计：5–15 分钟通常足够，
能把这一项砍掉 4–12 倍。允许的最小值是 `1m`。第二个杠杆是 `SPINNERET_STREAM_MAXLEN`（默认每分片
1,000,000），它限制的是 worker 排不空时的积压上限——按 `速率 × 可容忍积压秒数 / 分片数` 来定，并记住
超出上限的条目会被静默丢弃。

### PostgreSQL

增长来自分析数据和审计记录，全部由保留期设置控制。默认值：`SPINNERET_RETENTION_RISK_EVENTS` 与
`SPINNERET_RETENTION_MINUTE_STATS` 为 720 小时，`SPINNERET_RETENTION_HOUR_STATS` 为 4320 小时，
`SPINNERET_RETENTION_STATE_EVENTS` 与 `SPINNERET_RETENTION_AUDIT` 为 8760 小时。`partition_manager`
任务在 leader 实例上每小时创建和删除分区；它如果一直失败，分区就不再被删除，磁盘会被填满。检查
`spinneret_job_runs_total{job="partition_manager",result="error"}`。

如果冷却状态事件在行数中占主导而你并不需要它们，把 `SPINNERET_RECORD_COOLDOWN_EVENTS` 设为 `false`。

### ClickHouse

两类不同的故障。**磁盘**：由 `SPINNERET_CLICKHOUSE_TTL_DAYS` 控制（默认 90，范围 1–3650），服务端
启动做表迁移时写入。**内存**：自带的 `deploy/compose/config/clickhouse-limits.xml` 压低了各类缓存，因为默认配置是按
独占分析机器来设定的。要保证 `max_server_memory_usage` 高于进程的空闲常驻内存（alpine 镜像约
1.2 GiB）——低于它，每条 INSERT 都会以 `MEMORY_LIMIT_EXCEEDED` 失败，在服务端日志里表现为
`clickhouse batch insert failed`，在高负载下表现为上报 worker 被堵住。整个过程中容器仍然是 *healthy*
的，因为健康检查只问 `/ping`。

ClickHouse 宕机不会影响控制平面：租借、上报、策略和配置照常工作，只是请求明细一片空白。

---

## 升级之后

| 现象 | 原因 | 解决 |
| --- | --- | --- |
| 服务端拒绝启动，日志说 schema 落后于二进制 | 迁移没有应用 | 运行 `migrate` 服务，或用 `--migrate` 启动 |
| 日志告警 `database schema is newer than this binary` | 还有旧副本与新副本并存 | 滚动升级期间正常，最后一个旧副本被替换后自然消失 |
| `invalid configuration:` 列出了以前能用的变量 | 校验规则收紧，或变量改名了 | 按列表逐条修——每个问题都点名了——并对照[配置参考](./03-configuration.md) |
| 控制台还是旧版本 | 浏览器缓存了 `index.html` | 强制刷新；控制台由二进制内嵌提供，新镜像就是新控制台 |
| 把 `SPINNERET_REPORT_SHARDS` *调小*了 | 租约 ID 编码了所属分片，分片号 `>=` 新分片数的上报会被拒为 `lease_unknown`，超出新分片数的在途租约就此搁浅 | 改回去。调大分片数只会改变新租约的路由，不会拒掉在途租约，但无论哪个方向都应视为一个部署生命周期内固定 |

升级后务必执行 `$COMPOSE ps`，确认每个 `spinneret` 副本都到达 `healthy`，而不只是 `running`。

---

## 恢复备份之后

顺序很重要，并且有一样东西很容易被忘记。

1. **KEK 不在数据库里。** `deploy/compose/secrets/kek.key`（或 `SPINNERET_KEK_FILE` 指向的文件）是
   一份独立的产物。恢复 PostgreSQL 却没有恢复当时那把 KEK，会让所有密钥和身份载荷永久不可读。服务端
   甚至起不来：它会在
   `load dedupe_pepper system key (is the KEK the one used to initialize this database?)` 上失败。
2. **Redis 是 PostgreSQL 的缓存，但只恢复 PostgreSQL 并不会自动重建它。** 如果热状态里还是恢复之前的
   世界，重建它：

   ```bash
   $COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate rebuild
   ```

   不带 `--site` 时这会删除 epoch 并重建全部站点；在此期间 API 实例的 `/readyz` 返回
   `hotstate: epoch missing (rebuild pending)`，Caddy 会把它们摘出转发池。请安排维护窗口。
3. **恢复后的站点是冷的。** 就绪分数被均匀铺开到接下来的 60 秒，最初一段时间效率偏低，`exhausted`
   会偏高，属于预期。
4. **租约没有存活下来。** 持有恢复前租约的节点在 `Renew`、`Release`、`Report` 上会收到
   `lease_unknown`。节点应把它当作「从头再来」，重新租借。
5. **ClickHouse 是单独恢复的，或者根本没恢复。** 丢掉它只丢请求级历史。

完整的备份与恢复演练见[运维手册](./16-operations.md)。

---

## 完整的错误原因对照表

每个应用级错误都带有一个稳定的、机器可读的原因。Connect 和 gRPC 客户端从错误元数据里读取它，普通
HTTP+JSON 客户端从 **`Spinneret-Reason`** 响应头读取。可重试的错误还会带上
**`Spinneret-Retry-After-Ms`**——至少等待这么久。这些原因定义在 `internal/apperr/apperr.go`，
HTTP 状态列是 Connect 协议对该错误码的映射。

| 原因 | Connect 码 | HTTP | 含义 | 可重试？ | 该做什么 |
| --- | --- | ---: | --- | --- | --- |
| `token_invalid` | `unauthenticated` | 401 | Bearer 令牌不是已知的 API 令牌 | 否 | 核对令牌；重新签发 |
| `token_expired` | `unauthenticated` | 401 | 令牌已过有效期 | 否 | 签发新令牌 |
| `token_revoked` | `unauthenticated` | 401 | 令牌已被吊销 | 否 | 签发新令牌 |
| `ip_not_allowed` | `permission_denied` | 403 | 客户端地址不在令牌的 IP 白名单内 | 否 | 修白名单，或修 `SPINNERET_TRUSTED_PROXIES` |
| `session_invalid` | `unauthenticated` | 401 | 没有控制台会话、会话过期或账号被禁用；服务在没有认证主体时被调用也返回它 | 否 | 重新登录 |
| `csrf_missing` | `permission_denied` | 403 | 使用 Cookie 认证的非安全请求没有带 `X-Spinneret-CSRF` 头 | 否 | 带上该请求头；不要在代理里剥掉它 |
| `login_throttled` | `resource_exhausted` | 429 | 15 分钟内同一用户名 5 次或同一 IP 20 次登录失败 | 按提示后重试 | 等待；管理员重置密码可清除用户名计数 |
| `scope_missing` | `permission_denied` | 403 | API 令牌的权限范围不足以覆盖该资源或命名空间 | 否 | 用正确的权限范围重新签发令牌 |
| `permission_denied` | `permission_denied` | 403 | 用户的角色绑定未授予该权限 | 否 | 在正确的范围内授予角色 |
| `site_unknown` | `invalid_argument` | 400 | 调用者命名空间内没有该站点，或 `site` 为空 | 否 | 修请求 |
| `client_unknown` | `invalid_argument` | 400 | 该站点没有声明这个客户端 | 否 | 声明它，或修请求 |
| `endpoint_group_unknown` | `invalid_argument` | 400 | 该站点/客户端下没有这个端点组 | 否 | 修请求 |
| `uri_invalid` | `invalid_argument` | 400 | URI 无法规范化，或超过站点的最大路径长度 | 否 | 修请求 |
| `invalid_argument` | `invalid_argument` | 400 | 请求校验失败（也是不带原因的校验错误的兜底原因） | 否 | 修请求 |
| `no_identity_available` | `resource_exhausted` | 429 | 等待预算内该端点组没有身份通过全部过滤 | 是 | 遵守重试提示，见上文对应章节 |
| `no_proxy_available` | `resource_exhausted` | 429 | 找到了身份但挂不上代理 | 是 | 遵守提示；检查代理池 |
| `circuit_open` | `unavailable` | 503 | 该端点组的熔断器处于熔断状态 | 是 | 遵守提示，不要空转重试 |
| `site_paused` | `unavailable` | 503 | 站点开关已暂停，重试提示为 30 秒 | 是 | 取消暂停 |
| `overloaded` | `unavailable` | 503 | 租借准入控制在请求到达 Redis 之前就把它甩掉了——本实例已到租借并发上限。没有发出任何 Redis 命令，所以重试永远是安全的 | 是 | 遵守提示（100–200 毫秒，带抖动），见[延迟升高或吞吐崩塌](#延迟升高或吞吐崩塌) |
| `rebuilding` | `unavailable` | 503 | 热状态正在重建 | 是 | 按提示重试 |
| `lease_unknown` | `not_found` | 404 | 没有这个租约：ID 错、命名空间错，或已经过期淘汰 | 否 | 重新租借 |
| `lease_released` | `failed_precondition` | 400 | 租约已经归还过了 | 否 | 重新租借 |
| `lease_expired` | `failed_precondition` | 400 | 调用之前租约 TTL 已过 | 否 | 重新租借；提前续约 |
| `lease_lifetime_exceeded` | `failed_precondition` | 400 | `Renew` 会超出租约的最长使用时间 | 否 | 归还后重新租借 |
| `rate_limited` | `resource_exhausted` | 429 | 触达令牌的每秒请求上限，或该实例上并发配置订阅者过多（提示 1 秒） | 是 | 遵守提示；调高上限或加副本 |
| `not_found` | `not_found` | 404 | 目标资源不存在 | 否 | 修请求 |
| `already_exists` | `already_exists` | 409 | 违反唯一性约束 | 否 | 换个名字 |
| `failed_precondition` | `failed_precondition` | 400 | 系统当前状态不允许该操作；未配置 ClickHouse 时查询请求明细返回的 `unavailable`（503）也携带这个原因 | 否 | 满足前置条件 |
| `conflict` | `aborted` | 409 | 资源被并发修改 | 是 | 刷新后重试 |
| `query_too_large` | `resource_exhausted` | 429 | 分析查询所需资源超出 ClickHouse 允许的范围 | 否 | 缩小时间范围或加筛选 |
| `query_timeout` | `deadline_exceeded` | 504 | 分析查询超出时间预算 | 否 | 缩小时间范围或加筛选 |
| `internal` | `internal` 或 `unavailable` | 500 / 503 | 非预期故障（`internal`），或者某个依赖不可达——上报入队写不进去时，接口会以 `unavailable` 携带这个原因并带上重试提示返回。`internal` 时客户端只会看到统一的 `internal error`，原因只在服务端日志里 | 码为 `unavailable` 且带提示时可以 | `unavailable` 时退避重试；`internal` 时看服务端日志，然后提缺陷 |

两个值得知道的协议细节：

- 客户端永远看不到 `internal` 错误的根因。它在服务端连同过程名和操作者一起记录，在网络上被替换成
  `internal error`——这是刻意的。
- 被取消或超时的请求会变成 `canceled` / `deadline_exceeded`，而不是 `internal`。

---

## 阅读服务端日志

服务端默认以 JSON 格式输出到 **stdout**（`SPINNERET_LOG_FORMAT=json`，另一个选项是 `text`），级别为
`SPINNERET_LOG_LEVEL`（默认 `info`）。

```bash
$COMPOSE logs -f spinneret
$COMPOSE logs --no-color --since 30m spinneret | python3 -c \
  'import json,sys
for line in sys.stdin:
    try: r = json.loads(line)
    except ValueError: continue
    if r.get("level") in ("ERROR", "WARN"):
        print(r["time"], r["level"], r["msg"], {k: v for k, v in r.items() if k not in ("time", "level", "msg")})'
```

真正会用到的字段：

| 字段 | 含义 |
| --- | --- |
| `time`、`level`、`msg` | slog 的内建字段 |
| `instance` | `SPINNERET_INSTANCE_ID`，每条记录都有——靠它区分副本。不设置时（自带的栈就没设）默认是 `<hostname>-<6 位十六进制>`，在 Compose 里每次容器启动都会变；想要跨重启稳定的副本标识就显式设置它 |
| `component` | 子系统：`rpc`、`http`、`sse`、`jobs`、`catalog_sync`、`report_ingest` 等 |
| `procedure` | 失败请求的 Connect 过程名 |
| `actor` | 失败请求的身份主体（如果有） |
| `error` | 被包装的根因，包含客户端看不到的全部内容 |
| `site_id`、`site_key`、`namespace_id`、`report_id`、`lease_id` | 相关的主体标识 |

值得认识的几条记录：

| 消息 | 级别 | 含义 |
| --- | --- | --- |
| `starting spinneret` | INFO | 随后是生效的配置，机密已脱敏 |
| `spinneret started` | INFO | 已开始服务；带 `role`、`addr`、`version` |
| `shutdown requested` | INFO | 收到信号，开始排空 |
| `request failed` | ERROR | 结果为 `internal`、`unknown` 或 `data_loss`，根因在 `error` 里 |
| `handler panic` / `stream handler panic` | ERROR | 这是 bug——记录里带栈。请提交给我们 |
| `background loop failed, restarting` | ERROR | 某个受监督的循环挂了，`spinneret_loop_restarts_total{loop}` 会计数 |
| `listener failed, shutting down` | ERROR | HTTP 监听器挂了，进程会非零退出 |
| `readiness: … failed` | WARN | `/readyz` 变红的原因 |
| `catalog: full reload failed` | ERROR | 该命名空间会继续沿用上一份好快照 |
| `database schema is newer than this binary` | WARN | 滚动升级进行中 |
| `pprof debug listener enabled…` | WARN | `SPINNERET_PPROF_ADDR` 已设置 |
| `clickhouse disabled: raw report events are not stored` | INFO | 请求明细将为空 |
| `report ingest failed` | ERROR | 上报队列写不进去；调用方收到的是 `unavailable`、原因 `internal` 并带重试提示。检查 Redis |
| `state change flush failed` / `dropping state change batch` | ERROR | 动作已执行但未落库，检查 PostgreSQL |
| `proxy binding queue full, dropping bindings` | WARN | 代理绑定写入出现背压 |

**脱敏。** 键名为 `password`、`token`、`secret`、`authorization`、`cookie`、`payload` 或
`url_credentials` 的属性——以及嵌套在同名分组下的任何属性——都会被写成 `[REDACTED]`。启动时打印的
配置同样如此。但日志仍然是敏感的：里面有站点名、身份 ID 和操作者名。

---

## 安全地打开调试日志

在有限时间内于生产开启调试日志是安全的。它不会记录凭据（上面的脱敏规则在任何级别都生效），也不会
记录请求体。它很啰嗦，所以先确认你的日志容量能承受。

```bash
# 在 deploy/compose/.env 中
SPINNERET_LOG_LEVEL=debug
```

```bash
$COMPOSE up -d spinneret     # 以新级别重建副本
# …… 复现问题，采集日志 ……
# 然后改回去
sed -i.bak 's/^SPINNERET_LOG_LEVEL=debug/SPINNERET_LOG_LEVEL=info/' deploy/compose/.env
$COMPOSE up -d spinneret
```

级别在启动时读取，没有运行时开关。合法取值是 `debug`、`info`、`warn`、`error`，其他值会导致配置校验
失败，服务端不会启动。

要在多副本部署上缩小影响面，可以先把服务缩到一个副本，采集完再扩回去；或者额外跑一个调试级别的实例，
只把你自己的流量打到它上面。

**不要**把 `SPINNERET_PPROF_ADDR` 当成「调试开关」绑到可达地址上。pprof 端点无需认证，会暴露堆内容和
goroutine 栈。把它绑到 `127.0.0.1`，通过 SSH 隧道访问。

---

## 采集诊断包

在 <https://github.com/TikHub/Spinneret/issues> 提交缺陷之前先跑这个。它只收集维护者需要的东西，
不会碰 `.env` 和 KEK 文件。

```bash
#!/usr/bin/env bash
set -euo pipefail
cd /path/to/Spinneret
COMPOSE="docker compose -f deploy/compose/docker-compose.yml"
OUT="spinneret-diag-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$OUT"

# 版本与拓扑。
{ docker version; docker compose version; uname -a; } > "$OUT/host.txt" 2>&1
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate version > "$OUT/version.txt" 2>&1
$COMPOSE ps                                    > "$OUT/ps.txt"        2>&1
$COMPOSE config --no-interpolate               > "$OUT/compose.yml"   2>&1

# 生效的配置，命令本身会做脱敏。
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate config check \
                                               > "$OUT/config-check.txt" 2>&1

# 数据库 schema 与热状态。
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate migrate status \
                                               > "$OUT/migrate-status.txt" 2>&1
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate kek status \
                                               > "$OUT/kek-status.txt" 2>&1

# 健康与指标（经负载均衡器，只覆盖其中一个副本）。
BASE="http://localhost:${SPINNERET_PORT:-8080}"
curl -fsS "$BASE/healthz"  > "$OUT/healthz.json" || true
curl -fsS "$BASE/readyz"   > "$OUT/readyz.json"  || true
curl -fsS "$BASE/metrics"  > "$OUT/metrics.txt"  || true

# 日志。
for svc in spinneret migrate lb postgres valkey clickhouse; do
  $COMPOSE logs --no-color --tail 5000 "$svc" > "$OUT/logs-$svc.txt" 2>&1 || true
done

# 数据存储的关键指标。
$COMPOSE exec -T valkey valkey-cli info \
  > "$OUT/valkey-info.txt" 2>&1 || true
# --stat 没有采样次数上限（-c 是布尔的集群模式开关），所以从外面给它设一个时限。
timeout 10 $COMPOSE exec -T valkey valkey-cli --stat -i 1 \
  > "$OUT/valkey-stat.txt" 2>&1 || true

tar czf "$OUT.tar.gz" "$OUT" && rm -rf "$OUT"
echo "wrote $OUT.tar.gz"
```

附上之前请注意：

- **检查 `metrics.txt` 和日志里有没有你不愿公开的名字。** 站点名、客户端名、端点组名、命名空间名和
  节点名会出现在指标标签和日志字段里。敏感的话，在诊断包里改名。
- `config-check.txt` 由 `spnr config check` 自行脱敏，但仍建议读一遍。
- 绝不要附上 `deploy/compose/.env`、`deploy/compose/secrets/kek.key`、数据库导出文件或堆剖析文件
  ——堆剖析里可能含有已解密的载荷。

在报告里写清楚：你做了什么、期望什么、实际发生了什么、确切的原因字符串和 `Spinneret-Reason` 响应头
（如果有），以及事发的 UTC 时间戳，方便维护者在日志里定位。

---

## 下一步

- [运维手册](./16-operations.md) —— 计划内的操作流程：备份、升级、扩容、KEK 轮换、排空。
- [性能与调优](./17-performance.md) —— 崩塌特征背后的实测数据，以及按收益排序的调优杠杆。
- [可观测性与告警](./12-observability.md) —— 把这些现象变成告警所需的指标与规则。
- [节点 API 参考](./13-node-api.md) —— 节点针对每个原因必须实现的重试规则。
- [配置参考](./03-configuration.md) —— 本页提到的每一个 `SPINNERET_*` 变量。
