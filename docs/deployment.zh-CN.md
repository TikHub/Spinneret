# 部署指南

[English](deployment.md) · [运维手册](operations.zh-CN.md) · [API 参考](api.zh-CN.md)

Spinneret 交付为一个无状态二进制（`spinneret-server`）加一个管理 CLI（`spnr`），两者打包在同一个 distroless
镜像中。本文覆盖官方支持的部署方式——Docker Compose——以及围绕它的一切：配置、密钥、TLS、扩容、升级、备份、
安全加固与故障排查。

- [环境要求](#环境要求)
- [Compose 部署](#compose-部署)
- [配置参考](#配置参考)
- [KEK 管理](#kek-管理)
- [TLS 与反向代理](#tls-与反向代理)
- [扩容](#扩容)
- [升级与迁移](#升级与迁移)
- [备份](#备份)
- [安全加固清单](#安全加固清单)
- [故障排查](#故障排查)

---

## 环境要求

| 组件 | 版本 | 说明 |
| --- | --- | --- |
| Docker Engine | 24+（含 Compose 插件） | `docker compose version` 需可用 |
| PostgreSQL | 17（16 亦可） | 唯一真相源；Compose 使用 `postgres:17-alpine` |
| Redis / Valkey | Valkey 8 / Redis 7+ | 热状态、租约、上报流、会话。必须开启持久化（`appendonly yes`） |
| ClickHouse | 25.x，可选 | 请求浏览器的原始事件；留空 `SPINNERET_CLICKHOUSE_URL` 即禁用 |

中等规模参考配置（单站点 10 万身份、50 个端点组、每秒数千次领取）：2 个服务副本，各 4 vCPU / 8 GB；
PostgreSQL 4 vCPU / 8 GB 配高速磁盘；Valkey 2 vCPU / 4 GB。服务实例无状态，横向扩容即可。
`deploy/compose` 的默认值（2 副本、`max_connections=300`、`shared_buffers=512MB`）在 8 GB 开发机上即可运行。

Compose 对宿主机暴露的端口：

| 端口 | 服务 | 变量 |
| --- | --- | --- |
| 8080 | 经 Caddy 负载均衡的控制台与 API | `SPINNERET_PORT` |
| 9090 | Prometheus（profile `observability`） | `PROMETHEUS_PORT` |
| 19090 / 19091 | mock 目标站 / mock 代理（profile `test`、`loadtest`、`example`） | `MOCK_TARGET_PORT`、`MOCK_PROXY_PORT` |
| 18000 | 示例爬虫（profile `example`） | `EXAMPLE_PORT` |

PostgreSQL、Valkey、ClickHouse **不**对宿主机暴露，仅在 Compose 网络内可达。

---

## Compose 部署

全部内容在 [`deploy/compose/docker-compose.yml`](../deploy/compose/docker-compose.yml)，项目名 `spinneret`。

### 1. 初始化密钥

```bash
./scripts/compose-init.sh
```

若文件不存在则创建：

- `deploy/compose/.env`：由 `.env.example` 生成，随机填充 `PG_PASSWORD`、`CLICKHOUSE_PASSWORD`、
  `SPINNERET_ADMIN_PASSWORD`；
- `deploy/compose/secrets/kek.key`：全新的 32 字节密钥加密密钥（`k1:<base64>`）。

脚本可重复执行，已存在的文件不会被覆盖。**继续之前先备份 `kek.key`**——用它加密的数据在丢失该密钥后无法恢复。
两个文件都已加入 git 忽略，并被排除在 Docker 构建上下文之外。

### 2. 启动

```bash
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
```

启动顺序：

| 服务 | 作用 |
| --- | --- |
| `postgres` | PostgreSQL 17，命名卷 `pgdata` |
| `valkey` | Valkey 8，AOF 持久化（`appendfsync everysec`），`maxmemory-policy noeviction` |
| `clickhouse` | ClickHouse 25.8，命名卷 `chdata` |
| `migrate` | 一次性执行 `spnr migrate up`，服务端等待其成功结束 |
| `spinneret` | 服务端，2 副本（`SPINNERET_REPLICAS`），`stop_grace_period: 40s` |
| `lb` | Caddy 反向代理，宿主机 8080 端口，基于就绪探针做健康检查 |

`--wait` 会等到所有容器健康后返回。迁移由 PostgreSQL advisory lock 串行化，多个实例同时启动是安全的。

### 3. 创建第一个管理员

```bash
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

创建平台管理员（`SPINNERET_ADMIN_USERNAME`，默认 `admin`）、租户 `default`、命名空间 `default` 及其四条默认策略。
该命令幂等——重复执行会输出 `already initialized` 并以 0 退出。

```bash
grep SPINNERET_ADMIN_PASSWORD deploy/compose/.env     # 口令
open http://localhost:8080
```

### 4. 验证

```bash
curl -s localhost:8080/healthz    # {"status":"ok"}
curl -s localhost:8080/readyz     # {"checks":{"catalog":"ok","hotstate":"ok","postgres":"ok","redis":"ok"},"status":"ok"}
docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr migrate status
docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr config check   # 环境变量，密钥已脱敏
```

### 可选 profile

| Profile | 服务 | 命令 |
| --- | --- | --- |
| `init` | `init-admin` | `--profile init run --rm init-admin` |
| `observability` | 抓取两个副本的 `prometheus` | `--profile observability up -d` |
| `test` | `mocktarget`（mock 站点 + 带认证的代理） | `make e2e` 使用 |
| `loadtest` | `mocktarget` + `k6` | `make load`，见 [`test/load/README.md`](../test/load/README.md) |
| `example` | `mocktarget` + `example-crawler` | `./scripts/example-quickstart.sh` |

Profile 不改变核心栈，只是增加容器。

### 停止与清理

```bash
docker compose -f deploy/compose/docker-compose.yml down       # 停止，保留数据卷
docker compose -f deploy/compose/docker-compose.yml down -v    # 同时删除 pgdata、valkeydata、chdata
```

---

## 配置参考

服务端与 `spnr` 读取同一套 `SPINNERET_*` 环境变量。值会被去除首尾空白，空值视为未设置。时长支持 `ms`、`s`、
`m`、`h`、`d` 后缀（`30s`、`7d`、`1h30m`）。非法值会让进程启动失败并指出具体变量——部署前用 `spnr config check`
先校验一遍。

### 核心

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `SPINNERET_HTTP_ADDR` | `:8080` | API、控制台（以及默认的 `/metrics`）监听地址 |
| `SPINNERET_ROLE` | `all` | `all`、`api`（只处理请求）或 `worker`（只跑后台管道） |
| `SPINNERET_INSTANCE_ID` | 主机名 + 6 位随机十六进制 | 用于日志、任务租约和 Stream 消费组的实例标识 |
| `SPINNERET_SHUTDOWN_TIMEOUT` | `30s` | 优雅退出总预算。前四分之一（最多 5 秒）是排空窗口，其间 `/readyz` 返回 `draining`，之后才关闭监听 |

### 存储

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `SPINNERET_DATABASE_URL` | **必填** | PostgreSQL URL 或 libpq DSN |
| `SPINNERET_DATABASE_MAX_CONNS` | `32` | 每个实例的连接池大小（至少 2）。领导者任务持有 advisory lock 期间各占用一条连接，启动时会拒绝会被它们耗尽的连接池 |
| `SPINNERET_REDIS_URL` | 未设 `_ADDRS` 时**必填** | `redis://` 或 `rediss://` |
| `SPINNERET_REDIS_ADDRS` | 空 | Redis Cluster 的 `host:port` 列表，逗号分隔 |
| `SPINNERET_REDIS_PREFIX` | `sp` | 键前缀；不能含 `{`、`}`、`:` 或空格。共用一套 Redis 的两个部署必须使用不同前缀 |
| `SPINNERET_CLICKHOUSE_URL` | 空 | `clickhouse://user:pass@host:9000/db`；留空则关闭原始事件与请求浏览器 |
| `SPINNERET_CLICKHOUSE_TTL_DAYS` | `90` | 原始事件表的 TTL（1–3650） |

### 密钥

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `SPINNERET_KEK_FILE` | — | KEK 文件，每行 `id:base64`；也可以是单个 base64 密钥（id 为 `k1`） |
| `SPINNERET_KEKS` | — | 直接内联：`k1:base64,k2:base64` |
| `SPINNERET_KEK_CURRENT` | 列表中最后一个 | 用于包装**新** DEK 的 KEK id |

`SPINNERET_KEK_FILE` / `SPINNERET_KEKS` 至少提供一个；密钥必须恰好 32 字节。详见 [KEK 管理](#kek-管理)。

### 上报管道

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `SPINNERET_REPORT_SHARDS` | `16` | Redis Stream 分片数（1–255）。**部署后不可更改**——租约 ID 编码了分片号，见[修改分片数](#修改分片数) |
| `SPINNERET_REPORT_DEDUP_TTL` | `1h` | 重复 `report_id` 被计为 `duplicated` 的窗口（至少 1m） |
| `SPINNERET_LATE_REPORT_WINDOW` | `10m` | 迟于此窗口到达的上报仍会记录，但不再改变身份状态 |
| `SPINNERET_STREAM_MAXLEN` | `1000000` | 每个分片**积压**的近似长度上限（至少 1000）。Worker 会按消费位点裁剪自己的分片，消费干净的 Stream 不会占用内存；Worker 跟不上时超过上限的条目会被丢弃 |
| `SPINNERET_RECORD_COOLDOWN_EVENTS` | `true` | 为每次冷却写状态事件。极高流量下若状态事件存储成为负担可设为 `false` |

### 缓存

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `SPINNERET_PAYLOAD_CACHE` | `true` | 在进程内缓存渲染后的凭证。关闭可避免解密后的载荷驻留内存，代价是每次领取都要解密 |
| `SPINNERET_PAYLOAD_CACHE_SIZE` | `200000` | LRU 条目数。含已解析密钥引用的条目无论如何 60 秒后过期 |
| `SPINNERET_DEK_CACHE_SIZE` | `100000` | 已解包 DEK 的 LRU 容量 |
| `SPINNERET_DEK_CACHE_TTL` | `10m` | 该缓存的 TTL |
| `SPINNERET_TOKEN_CACHE_TTL` | `30s` | 令牌校验结果的缓存时长。吊销会发布事件立即清除缓存，此值只是最坏情况上界 |

### 控制台、会话与传输安全

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `SPINNERET_SESSION_TTL` | `12h` | 控制台会话有效期，剩余不足一半时自动续期（至少 1m） |
| `SPINNERET_COOKIE_SECURE` | `auto` | `auto`：请求经 TLS 或带 `X-Forwarded-Proto: https` 时标记 `Secure`；`true`：始终；`false`：从不（仅限内网明文 HTTP） |
| `SPINNERET_TLS_CERT_FILE` / `SPINNERET_TLS_KEY_FILE` | 空 | 直接提供 HTTPS，必须成对设置 |
| `SPINNERET_TRUSTED_PROXIES` | 空 | 可信任其 `X-Forwarded-For` / `X-Forwarded-Proto` 的 CIDR。**在反向代理后必须设置**，否则客户端 IP 全部是代理的（影响令牌 IP 白名单、登录限流、审计） |
| `SPINNERET_UI_ENABLED` | `true` | 提供内嵌控制台；`false` 则只保留 API |
| `SPINNERET_ALLOWED_ORIGINS` | 空 | CORS 白名单，供本地 `pnpm dev` 连接使用。生产环境保持为空 |
| `SPINNERET_ADMIN_MAX_REQUEST_BYTES` | `67108864`（64 MiB） | 管理 RPC 的请求体上限（身份、代理导入）；至少 1 MiB |
| `SPINNERET_MAX_WATCHERS` | `20000` | 单实例并发 `WatchConfig` 长轮询上限，超出返回 `resource_exhausted` |

### 代理健康检查

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `SPINNERET_PROXY_CHECK_URL` | `http://example.com/` | 通过每个代理访问的 URL。无外网的主机请改为可达地址，例如 `http://mocktarget:9090/healthz` |
| `SPINNERET_PROXY_CHECK_INTERVAL` | `60s` | 检查周期（至少 1s） |
| `SPINNERET_PROXY_CHECK_TIMEOUT` | `10s` | 单个代理的超时（至少 100ms） |
| `SPINNERET_PROXY_EXIT_IP_URL` | 空 | 返回调用方 IP 的 URL，用于记录代理出口 IP |
| `SPINNERET_GEOIP_DB` | 空 | MaxMind 数据库路径，用出口 IP 补全代理地区 |

### 数据保留

保留策略作用于 PostgreSQL 的分区表；超期分区由每小时执行的 `partition_manager` 任务删除。每个值至少 `24h`。

| 变量 | 默认值 |
| --- | --- |
| `SPINNERET_RETENTION_RISK_EVENTS` | `720h`（30 天） |
| `SPINNERET_RETENTION_MINUTE_STATS` | `720h`（30 天） |
| `SPINNERET_RETENTION_HOUR_STATS` | `4320h`（180 天） |
| `SPINNERET_RETENTION_STATE_EVENTS` | `8760h`（365 天） |
| `SPINNERET_RETENTION_AUDIT` | `8760h`（365 天） |

### 可观测性

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `SPINNERET_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `SPINNERET_LOG_FORMAT` | `json` | `json` 或 `text` |
| `SPINNERET_METRICS_ADDR` | 空 | 留空则 `/metrics` 由主监听提供；设为 `:9091` 等可放到独立、不对外的监听上 |
| `SPINNERET_PPROF_ADDR` | 空 | 留空则关闭性能分析。设为 `127.0.0.1:6060` 等会在独立监听上提供 `/debug/pprof/`，用于临时采样。这些端点**无鉴权**，会暴露堆内容与 goroutine 栈：切勿对外发布该端口（见 [benchmarks.zh-CN.md](benchmarks.zh-CN.md)） |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 空 | 设置后启用 OTLP 链路追踪 |

### 仅 Compose 使用的变量（`deploy/compose/.env`）

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `PG_PASSWORD`、`CLICKHOUSE_PASSWORD` | 随机生成 | 数据库口令，被服务 URL 引用 |
| `SPINNERET_PORT` | `8080` | 负载均衡的宿主机端口 |
| `SPINNERET_REPLICAS` | `2` | `spinneret` 副本数 |
| `SPINNERET_ADMIN_USERNAME` / `SPINNERET_ADMIN_PASSWORD` | `admin` / 随机生成 | 仅被 `init-admin` 使用一次 |
| `PROMETHEUS_PORT`、`MOCK_TARGET_PORT`、`MOCK_PROXY_PORT`、`EXAMPLE_PORT` | `9090`、`19090`、`19091`、`18000` | 各 profile 服务的宿主机端口 |
| `EXAMPLE_TOKEN`、`LOADTEST_TOKEN` | 空 | 示例爬虫与 k6 的节点令牌；分别由 `scripts/example-quickstart.sh` 写入、由 `spnr seed` 打印 |

---

## KEK 管理

Spinneret 用信封加密保护身份载荷、代理 URL、密钥版本和通知渠道凭据：每条记录随机生成 32 字节 **DEK** 加密数据
（AES-256-GCM），DEK 再由你提供的 **KEK** 包装。数据库里只有包装后的 DEK 和密文，KEK 从不入库。

### 提供密钥

```bash
# 生成一行密钥
docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr kek generate --id k1
# k1:kZ7s...base64...=
```

服务端可以读文件：

```
SPINNERET_KEK_FILE=/run/secrets/kek        # 每行一个 "id:base64"
```

也可以内联（`SPINNERET_KEKS=k1:...,k2:...`）。`SPINNERET_KEK_CURRENT` 指定包装新 DEK 使用的密钥，不设置则以
列表最后一个为当前。列表中的所有密钥都仍可用于*解包*，这正是轮换可以在线进行的原因。

Compose 把 `deploy/compose/secrets/kek.key` 作为 Docker secret `kek` 挂载到 `/run/secrets/kek`。

### 轮换

1. 追加新密钥并使其成为当前密钥：

   ```bash
   docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr kek generate --id k2 \
     >> deploy/compose/secrets/kek.key
   ```

   （或显式设置 `SPINNERET_KEK_CURRENT=k2`；默认最后一行为当前。）

2. 重启实例以加载新的密钥列表：

   ```bash
   docker compose -f deploy/compose/docker-compose.yml up -d --wait spinneret
   ```

3. 用当前 KEK 重新包装全部 DEK。该任务可断点续跑，在领导者实例上执行；命令会跟踪进度，出错时以非 0 退出：

   ```bash
   docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr kek rewrap
   ```

   控制台的 **Secrets → KEK** 页面提供同样的操作（`SecretAdminService/StartKEKRewrap`，仅平台管理员）。

4. 当 `spnr kek status` 显示旧密钥已无任何记录时，把该行从文件中删除并重启。

### 备份

`kek.key` 是整个部署中最重要的文件。请存放在密钥管理系统或离线备份中，与数据库备份分开保存。
**没有 KEK 的数据库备份对所有加密字段来说是不可恢复的。** 在 `spnr kek status` 显示旧密钥记录数为 0 之前不要删除它，
且密钥备份的保留时间不得短于用它加密的任何数据备份。

---

## TLS 与反向代理

服务端可以自己终结 TLS（`SPINNERET_TLS_CERT_FILE` + `SPINNERET_TLS_KEY_FILE`），但常见做法是放在反向代理之后。
Compose 自带 Caddy 作为网络内负载均衡（[`deploy/compose/config/Caddyfile`](../deploy/compose/config/Caddyfile)），
你可以在它前面再放一层边缘代理，或者直接替换它。

无论使用哪种代理，都必须做到：

- **透传 `X-Forwarded-For` 与 `X-Forwarded-Proto`**，并让服务端信任它：
  `SPINNERET_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12`（Compose 已设置）。否则所有客户端 IP 都是代理的 IP，
  令牌 IP 白名单、登录限流和审计记录都会失真。
- **不要缓冲长轮询和 SSE。** `WatchConfig` 最长阻塞 60 秒，`/api/v1/events/stream` 是持续流。Caddy 用
  `flush_interval -1`；nginx 用 `proxy_buffering off` 配合 `proxy_read_timeout 120s`。
- **健康检查用就绪探针而不是存活探针**：每约 2 秒探测 `/readyz`。正在退出的实例会在关闭监听**之前**用
  `503 {"status":"draining"}` 回答 `SPINNERET_SHUTDOWN_TIMEOUT` 的四分之一（最多 5 秒），这正是滚动重启不丢请求的关键。
  `/healthz` 只说明进程还活着。
- **想让 POST 可重试就必须缓冲请求体。** 所有 RPC 都是 POST，代理只有持有请求体才能把它重放到另一个上游。
  Caddy 的 `request_buffers 128KiB` 覆盖了全部节点和控制台调用；更大的请求体（身份、代理导入）以流式传输，不重试。
- **客户端使用 gRPC 时必须端到端 HTTP/2。** Connect JSON 在 HTTP/1.1 上即可工作；Go SDK 的 `UseGRPC` 模式需要
  h2c（明文）或 TLS 上的 HTTP/2。

nginx 最小配置：

```nginx
location / {
    proxy_pass         http://spinneret_upstream;
    proxy_http_version 1.1;
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
    proxy_buffering    off;            # 长轮询与 SSE
    proxy_read_timeout 120s;
}
```

在边缘终结 TLS 时，把 `SPINNERET_COOKIE_SECURE` 保持为 `auto`（默认，根据 `X-Forwarded-Proto` 判断）或设为
`true` 强制标记会话 Cookie 为 `Secure`。

---

## 扩容

**服务副本。** 实例无状态，用 `SPINNERET_REPLICAS`（Compose）或编排系统扩容。协调全部通过 Redis 和 PostgreSQL 完成：
Stream 分片归属、任务领导者选举（PostgreSQL advisory lock）、目录与配置失效通知（Redis Pub/Sub）。

```bash
SPINNERET_REPLICAS=4 docker compose -f deploy/compose/docker-compose.yml up -d --wait
```

**角色。** `SPINNERET_ROLE=api` 只处理请求、不跑后台任务；`SPINNERET_ROLE=worker` 只跑上报管道、熔断评估、
租约回收、代理健康检查、告警评估、热状态快照和分区维护，不对外服务。当上报消费与请求处理的扩容曲线不同时拆分，
并**至少保留两个 worker**，以免单点故障导致管道停摆。默认的 `all` 表示每个实例两者都做。

**上报分片。** `SPINNERET_REPORT_SHARDS`（默认 16）决定同时能有多少 worker 并行消费上报——一个分片只有一个归属者。
经验法则：每个 worker 实例至少 2–4 个分片。关注 `spinneret_stream_pending{shard}` 和
`spinneret_report_lag_seconds`：待处理条目持续增长说明瓶颈在 worker 而不是分片数；各分片待处理均匀且 CPU 空闲
则说明分片太少。

### 修改分片数

租约 ID 编码了上报应进入的分片，因此改变分片数会**使在途租约失效**：

1. 停止节点流量，或接受变更前发出的租约会上报到无人消费的分片。
2. 等待最长租约 TTL（默认 2 分钟，`max_lease_lifetime` 最长 30 分钟）让在途租约全部过期，并等 Stream 排空
   （`spinneret_stream_pending` 归零）。
3. 修改 `SPINNERET_REPORT_SHARDS` 并同时重启所有实例——不要出现新旧配置混跑的集群。

**数据库。** PostgreSQL 不在领取/上报热路径上，承担聚合写入、目录和管理流量。随副本数提高
`SPINNERET_DATABASE_MAX_CONNS`，并让服务端的 `max_connections` 高于 `副本数 × 该值 + 余量`。

**Redis。** 单实例即可达到设计目标，它是领取吞吐的瓶颈所在。`SPINNERET_REDIS_ADDRS` 启用 Redis Cluster
（键按命名空间打了哈希标签）。**绝不要开启淘汰策略**：Compose 已设置 `maxmemory-policy noeviction`，
丢失热状态键会悄无声息地改变调度结果。

**设计目标**（v0.1，用 [k6 场景](../test/load/README.md)验证）：服务端领取延迟 p99 < 5 ms、单实例 ≥ 5 000 次/秒，
单实例上报接收 ≥ 20 000 条/秒，上报到状态更新 p99 < 200 ms，配置变更 < 1 秒可见，单实例并发长轮询 ≥ 10 000。

---

## 升级与迁移

迁移脚本内嵌在二进制中，由 `spnr migrate up` 应用，并由 advisory lock 串行化。Compose 在一次性的 `migrate`
服务中执行它，服务端依赖其完成，因此常规升级就是：

```bash
git pull
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
```

Compose 会重建镜像、重新执行 `migrate`，然后逐个重启副本。每个副本先排空（`/readyz` 返回 `draining`、
关闭 keep-alive、处理完在途请求），负载均衡的 2 秒就绪检查会把流量切到另一个副本。

生产升级清单：

1. **先备份**——至少一份 PostgreSQL 导出（见[备份](#备份)）。
2. 升级前后执行 `docker compose ... exec spinneret spnr migrate status`，它会打印已应用版本与二进制内嵌版本。
3. worker 重启期间关注各副本的 `/readyz` 与 `spinneret_stream_pending`。
4. 迫不得已才回滚迁移：`spnr migrate down --to N` **会删除表和数据**，恢复备份通常更安全。

关于零停机：有两个及以上副本并配合就绪检查的代理时，节点客户端除了被停止副本上的在途 POST 之外不会看到失败，
而这些 SDK 会重试（`unavailable`）。节点 API 与 SDK 的线格式向前兼容——双向都会忽略未知 JSON 字段——所以节点
不必与服务端同步升级。

---

## 备份

| 对象 | 方式 | 原因 |
| --- | --- | --- |
| **KEK**（`deploy/compose/secrets/kek.key`） | 首次使用前及每次轮换后复制到密钥管理系统/离线存储 | 没有它，加密的载荷、代理 URL 和密钥无法恢复 |
| **PostgreSQL** | 每日 `pg_dump -Fc`，或流复制 / PITR | 唯一真相源：目录、身份、策略、密钥、用户、审计 |
| **Valkey/Redis** | AOF（`appendonly yes`、`appendfsync everysec`，已默认开启）加定期卷备份 | 加快恢复；并非必需，见下 |
| **ClickHouse** | 仅当需要保留超过 `SPINNERET_CLICKHOUSE_TTL_DAYS` 的历史原始事件时 | 纯分析数据，由上报派生 |

```bash
# 从 Compose 栈导出 PostgreSQL
docker compose -f deploy/compose/docker-compose.yml exec -T postgres \
  pg_dump -U spinneret -Fc spinneret > spinneret-$(date -u +%Y%m%d).dump

# 恢复到空库
docker compose -f deploy/compose/docker-compose.yml exec -T postgres \
  pg_restore -U spinneret -d spinneret --clean --if-exists < spinneret-20260917.dump
```

**Redis 是可重建的。** 热状态由 PostgreSQL 派生，丢失后重建即可，不需要恢复备份：

```bash
docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr rebuild
```

该命令删除热状态 epoch 并从 PostgreSQL 重新物化每个站点（身份、代理，健康分从周期性的
`hot_state_snapshots` 恢复）。重建期间 API 实例返回未就绪（`rebuilding`）。
`spnr rebuild --tenant t --namespace n --site s` 只重建单个站点，不需要全局停机。

Redis 丢失后确实无法找回、重建也不会恢复的东西：在途租约（节点上报会收到 `lease_unknown`，重新领取即可）、
仍在 Stream 中未消费的上报、当前的熔断窗口，以及控制台会话（所有人需要重新登录）。

请演练恢复流程：把 PostgreSQL 导出连同对应的 KEK 恢复到一个空栈，再执行 `spnr rebuild`，部署必须能完全恢复。

---

## 安全加固清单

**网络**

- [ ] 不要把 PostgreSQL、Valkey、ClickHouse 端口暴露到宿主机或公网（Compose 文件默认不暴露）。
- [ ] 在边缘终结 TLS，HTTP 跳转 HTTPS，并把 `SPINNERET_TRUSTED_PROXIES` 设为代理的 CIDR，保证客户端 IP 真实。
- [ ] 把 `/metrics` 放到独立监听（`SPINNERET_METRICS_ADDR=:9091`）且只对监控网络开放——在主监听上它是无鉴权的。
- [ ] 生产环境保持 `SPINNERET_ALLOWED_ORIGINS` 为空（它只为本地控制台开发存在）。

**令牌与账号**

- [ ] 节点令牌只授予够用的最小作用域：`lease:acquire:<site>`、`report:write:<site>`、`config:read:<group>`，
      以及节点确实需要的 `secret:read:<ns>/<path glob>`。避免使用 `admin`。
- [ ] 设置有效期（`--expires 720h`）并定期轮换；在控制台为令牌配置来源 CIDR 白名单和速率限制。
- [ ] 控制台账号使用够用的最小角色（`viewer` < `operator` < `admin` < `owner`），并把绑定限制到命名空间或具体站点。
- [ ] 平台管理员（`is_platform_admin`）绕过全部权限检查——数量越少越好。
- [ ] 首次登录后修改引导 `admin` 的口令；修改或重置口令会使该用户的其他会话全部失效。

**密钥**

- [ ] KEK 不要进仓库、不要进镜像（构建上下文已排除）、不要和数据库备份放在一起；定期轮换，怀疑泄露时立即轮换。
- [ ] 在配置中用 `${secret:path}`、在身份类型中用 `secret_ref` 字段引用密钥，而不是直接粘贴值；每次读取都有审计。
- [ ] `secret:reveal`（控制台）与 `secret:read`（令牌）是两个独立权限——reveal 只给人。
- [ ] 绝不记录租约凭证和代理 URL；SDK 已将它们排除在 `repr`/`String()` 之外，服务端也从不记录载荷字段。

**浏览器**

- [ ] 控制台带有 `Content-Security-Policy: default-src 'self'`（仅允许经哈希的内联引导脚本）、
      `X-Frame-Options: DENY`、`X-Content-Type-Options: nosniff` 和 `Referrer-Policy: same-origin`。
      自建代理时请保留它们，不要额外添加宽松的 CSP 或 CORS 头。
- [ ] 会话 Cookie 是 `HttpOnly`、`SameSite=Strict`，TLS 下带 `Secure`；带 Cookie 的非安全方法请求还必须携带
      `X-Spinneret-CSRF: 1`。

**运维**

- [ ] 保留审计日志（`SPINNERET_RETENTION_AUDIT`，默认 365 天），并定期审查 `secret.read` 记录。
- [ ] 以镜像自带的非 root 用户运行（distroless `nonroot`），平台支持时启用只读文件系统。
- [ ] 按[监控](operations.zh-CN.md#监控)一节配置告警，尤其是熔断打开和上报积压。

---

## 故障排查

**`up` 报 `PG_PASSWORD: run scripts/compose-init.sh`** —— 缺少 `deploy/compose/.env`，执行
`./scripts/compose-init.sh`。

**`/readyz` 返回 `503` 且某项检查失败**

| 检查项 | 含义 |
| --- | --- |
| `postgres` | 连接池无法访问 PostgreSQL——检查凭据、`max_connections`、网络 |
| `redis` | Redis/Valkey 不可达，或前缀指向了已被清空的库 |
| `hotstate` | 正在重建（`rebuilding`），或热状态 epoch 缺失——执行 `spnr rebuild` |
| `catalog` | 无法从 PostgreSQL 载入命名空间快照 |

**`503 {"status":"draining"}`** —— 重启期间的正常现象，最长持续 `SPINNERET_SHUTDOWN_TIMEOUT` 的四分之一
（最多 5 秒）。此时代理应已把该实例摘除。

**节点收到 `429 no_identity_available`** —— 当前没有身份满足可用性规则：全部被租出
（`max_concurrent_leases`）、处于复用间隔内、正在冷却、配额用尽或已被封禁。`Spinneret-Retry-After-Ms` 头给出了
服务端估计的等待时长。在身份页和该端点组的热力图中排查。

**节点收到 `429 no_proxy_available`** —— 轮换策略要求代理但没有可用的。最常见原因是健康检查把代理标记为
`dead`，因为服务端访问不到 `SPINNERET_PROXY_CHECK_URL`（默认 `http://example.com/`）。
隔离环境请把它指向可达地址，例如 `http://mocktarget:9090/healthz`，然后重启 `spinneret` 服务。

**节点收到 `503 circuit_open` / `503 site_paused`** —— 端点组熔断打开（探针成功后会在 `open_duration` 之后关闭），
或站点开关被关闭。两者都能在熔断页查看和恢复。

**节点收到 `503 rebuilding`** —— 正在重建热状态；等待完成，或确认是否有意外触发的 `spnr rebuild`。

**上报积压** —— `spinneret_stream_pending{shard}` 增长且 `spinneret_report_lag_seconds` 上升。检查是否有
具备 worker 能力的实例在运行（`SPINNERET_ROLE`）、各实例的 `spinneret_stream_owned_shards` 之和是否等于
`SPINNERET_REPORT_SHARDS`，以及 PostgreSQL 是否饱和。

**登录返回 `login_throttled`** —— 15 分钟内同一用户名 5 次失败或同一 IP 20 次失败。等待窗口过去；
如果所有用户都被同一个 IP 限流，多半是没设置 `SPINNERET_TRUSTED_PROXIES`，所有请求看起来都来自代理。

**控制台报 CSRF 或会话错误** —— 请求缺少 `X-Spinneret-CSRF: 1`（代理剥掉了请求头），或 HTTPS 站点上的 Cookie
没有 `Secure`。检查 `SPINNERET_COOKIE_SECURE` 以及 `X-Forwarded-Proto` 是否传到了服务端。

**gRPC 客户端失败但 JSON 正常** —— 代理没有端到端 HTTP/2。为上游启用 h2c 或 HTTP/2，或改用 Connect JSON。

日志：`docker compose -f deploy/compose/docker-compose.yml logs -f spinneret`。需要更多细节时设置
`SPINNERET_LOG_LEVEL=debug`；密钥、载荷、代理凭据和令牌永远不会被记录。
