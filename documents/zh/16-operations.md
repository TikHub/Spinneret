# 运维手册

**Day 2 的活：给三个存储做容量规划、备份并验证备份真的能恢复、升级、扩容、重建热状态、轮换那把所有数据都
依赖的密钥、把磁盘增长控制住 —— 以及每一类会真的把你叫起来的故障各配一份编号处置流程。**

[English](../en/16-operations.md)

---

## 目录

- [一套健康部署长什么样](#一套健康部署长什么样)
- [控制脚本与 CLI](#控制脚本与-cli)
- [容量规划](#容量规划)
- [备份](#备份)
- [恢复](#恢复)
- [升级与数据库迁移](#升级与数据库迁移)
- [扩容](#扩容)
- [修改分片数](#修改分片数)
- [重建热状态](#重建热状态)
- [密钥轮换](#密钥轮换)
- [数据保留与磁盘](#数据保留与磁盘)
- [账号、口令与令牌](#账号口令与令牌)
- [日常巡检](#日常巡检)
- [故障处置手册](#故障处置手册)
- [呼人还是开单](#呼人还是开单)
- [下一步](#下一步)

---

## 一套健康部署长什么样

两个端点加四个数字，基本就把情况说完了。

```bash
curl -s 127.0.0.1:8080/healthz   # {"status":"ok"} —— 进程活着
curl -s 127.0.0.1:8080/readyz    # 每个依赖，逐项列出
```

```json
{"checks":{"catalog":"ok","hotstate":"ok","postgres":"ok","redis":"ok"},"status":"ok"}
```

负载均衡器探的是 `/readyz`；`/healthz` 只说明进程还在。正在关停的副本会在**关闭监听之前**先用
`503 {"status":"draining"}` 回应 `min(5s, SPINNERET_SHUTDOWN_TIMEOUT/4)` 这么久 —— 每次重启都会出现，属于
正常现象；持续时间明显更长，说明前面的代理配置有问题。

四个数字，来自 `/metrics`：

| 指标 | 它告诉你什么 | 正常值 |
| --- | --- | --- |
| `spinneret_stream_pending{shard}` | 等待被应用的上报 | 接近 0，有毛刺。持续增长说明 worker 跟不上了 |
| `spinneret_report_lag_seconds` | 调度器决策依据的状态有多旧 | p99 在几十毫秒量级 |
| `spinneret_stream_owned_shards` | 本实例消费的分片数 | 所有实例相加等于 `SPINNERET_REPORT_SHARDS` |
| `spinneret_acquire_peers` | 每个实例看到多少个存活的 API 实例 | 等于 api 角色的副本数 |

每个指标的定义和完整清单在[可观测性与告警](./12-observability.md)。本页只使用它们，不重复定义。

---

## 控制脚本与 CLI

用 `install/install.sh` 安装出来的目录里有一个 `./spnrctl`。它就是 `docker compose`，只是把每次都必须写对的
四件事预置好了：项目名、按顺序排好的覆盖文件、能让 `./config`、`./secrets` 和构建上下文正确解析的工作目录，
以及 `COMPOSE_ENV_FILES`。

```bash
./spnrctl ps
./spnrctl logs -f spinneret
./spnrctl restart lb
./spnrctl up -d --wait
./spnrctl down              # 停掉容器，数据保留
./spnrctl down -v           # 停掉并删除数据卷。不可逆。
```

管理 CLI 在镜像里的路径是 `/usr/local/bin/spnr`。请通过一次性的 `migrate` 服务执行它，而不是钻进某个副本：
该服务已经带着完整环境变量和密钥文件，没有服务端 entrypoint 挡路，而且只依赖 PostgreSQL —— 所以在一个副本
都不健康的时候它照样能用。

本页出现的每一条 `spnr …` 都是这个意思。每个 shell 会话粘一次下面这行，本页其余命令就都能直接复制运行：

```bash
spnr() { ./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate "$@"; }

spnr migrate status
spnr kek status
spnr rebuild --help
```

其他执行 CLI 的方式 —— 在运行中的副本里执行，或者在宿主机上用自己的连接串执行 —— 见
[命令行工具 → 对已部署环境执行 spnr](./15-cli.md#对已部署环境执行-spnr)。

`bash install.sh --manage` 会把安装脚本重新打开成一个菜单。顶层是 **1** 状态、**2** 升级（或切换到另一个已
发布的 tag，包括更旧的 tag）、**3** *管理*、**4** 停止或移除。本页涉及的操作都在**管理**里：

| 管理 | | 管理 | |
| --- | --- | --- | --- |
| 1 | 修改管理员口令 | 7 | 健康检查 |
| 2 | 添加管理员 | 8 | 日志 |
| 3 | 列出账号 | 9 | 重启服务 |
| 4 | 创建 API 令牌 | 10 | 立即备份 |
| 5 | 重建热状态 | 11 | 恢复备份 |
| 6 | 查看配置 | 12 | 释放磁盘空间 |

每个条目都只是转调下文这些命令，所以菜单不可能和它声称做的事跑偏。本页把它们写成 *管理 → 10* 这种形式。

---

## 容量规划

三个存储分别估算。它们按完全不同的项增长，而且只有一个会以让你意外的方式随请求速率增长。

### Redis / Valkey —— 由流量决定，不是由数据量决定

这是最需要算对的一个。数据集本身既小又可预测；真正大头是**与流量成正比**的那部分状态，通常是数据集的几倍。
下面是在一个 10 万身份的真实数据集上用 `MEMORY USAGE` 实测的结果：

| 结构 | 单位开销 |
| --- | --- |
| 身份哈希 | 平均 **201 B**，最大 296 B，每个身份 |
| 就绪队列成员 | **64.6 B** 每（身份 × 端点组）—— 每百万条约 62 MiB |
| 健康状态条目 | **≈ 71 B** 每个**已预热**的（身份 × 端点组） |
| 上报去重标记 | **80 B** 每条上报，存活 `SPINNERET_REPORT_DEDUP_TTL` |
| 已结束的租约哈希 | **≈ 290 B** 每个租约，结束后再保留 `max(SPINNERET_LATE_REPORT_WINDOW, SPINNERET_REPORT_DEDUP_TTL)` |
| 上报流条目 | **≈ 440 B** 每条积压条目 |

```text
Redis 工作集 ≈ 身份数                       × 201 B
             + 身份数 × 端点组数             × 64.6 B    （就绪队列）
             + 已预热的（身份 × 组）          × 71 B      （健康状态，最终会覆盖全部组合）
             + 上报/秒   × dedup_TTL         × 80 B      （去重标记）
             + acquire/秒 × max(late, dedup) × 290 B     （已结束的租约哈希）
             + 积压条目数                    × 440 B     （上报流）
```

这个形状带来三个结论：

1. **`SPINNERET_REPORT_DEDUP_TTL` 是最大的一根内存杠杆。** 默认 `1h`，最小允许 `1m`，而节点重试是按秒计的，
   不是按小时。5–15 分钟通常就够，能把两个流量项砍掉 4–12 倍。
2. **把它降到 `SPINNERET_LATE_REPORT_WINDOW`（默认 `10m`）以下就不再有收益了**，因为已结束的租约哈希按两者
   中较大的那个保留。要继续降就两个一起降，否则停在迟到窗口这个值上。
3. **端点组会成倍放大按身份存的状态。** 每一个（身份 × 组）组合要 ~65 B 就绪队列加 ~71 B 健康状态。端点组应
   该按需要的策略划分，而不是按 URL 数量。

Valkey 以 `--maxmemory-policy noeviction` 运行：这部分状态绝不能在服务端不知情的情况下被淘汰。**永远不要开启
淘汰策略。** 按算出来的工作集的**两倍**准备内存：AOF 重写会 fork 进程，而 Redis 到顶之后是让写入明确失败，
不是缓慢降级。

### PostgreSQL —— 由目录规模和保留期决定

热路径完全不碰 PostgreSQL；它只承接批量写入，在所有压测场景里 CPU 都低于 3 %。需要规划的是磁盘，而磁盘由
保留期决定，不是由请求速率决定：

| 增长的东西 | 行数 | 受什么约束 |
| --- | --- | --- |
| `identities`、`identity_payloads`、`proxies`、`accounts` | 每个对象一行（载荷：每个身份保留最近 **5** 个版本） | 你的目录规模 |
| `outcome_stats_minutely`、`acquire_stats_minutely` | 每（分钟 × 站点 × 端点组 × 代理 × 判定结果/结果）一行 | `SPINNERET_RETENTION_MINUTE_STATS`，720 h |
| `node_stats_minutely` | 每（分钟 × 命名空间 × 节点）一行 | `SPINNERET_RETENTION_MINUTE_STATS` |
| `payload_access_minutely` | 每（分钟 × 命名空间 × 令牌 × 身份类型）一行 | `SPINNERET_RETENTION_MINUTE_STATS` |
| `identity_stats_hourly` | 每（小时 × 身份 × 端点组 × 判定结果）一行 —— **最大的一项** | `SPINNERET_RETENTION_HOUR_STATS`，4320 h |
| `risk_events` | 每个风险事件一行 | `SPINNERET_RETENTION_RISK_EVENTS`，720 h |
| `state_events` | 每次身份/代理状态变更一行 | `SPINNERET_RETENTION_STATE_EVENTS`，8760 h |
| `audit_logs` | 每次管理操作、每次密钥读取一行 | `SPINNERET_RETENTION_AUDIT`，8760 h |
| `alert_events` | 每条触发的告警一行 | 固定 **90 天**，不可配置 |

要算的就是 `identity_stats_hourly`，因为只有它的基数里带着你的身份数量：

```text
每小时行数 = 该小时内产生过流量的（身份, 端点组, 判定结果）去重组合数
          ≤ min( 每小时请求数 , 身份数 × 端点组数 × 判定结果种类数 )
每天行数   = 每小时行数 × 24
```

它的默认保留期是 180 天，按**月**分区，所以磁盘上长期躺着六到七个这个量级的分区。其他聚合表的基数只跟站点、
端点组、代理、节点、令牌和判定结果的种类数有关，与身份数无关，通常小到可以忽略。

另一个 PostgreSQL 限制是连接数，而且是硬限制：必须保证
`副本数 × SPINNERET_DATABASE_MAX_CONNS + 余量 < max_connections`。Compose 编排把 `max_connections` 设为
`300`，`SPINNERET_DATABASE_MAX_CONNS` 默认 `32`（最小 `2`）。给 `spnr`、`psql` 和迁移任务自己的锁连接留出
余量。

### ClickHouse —— 由请求速率和 TTL 决定

ClickHouse 里 `report_events` 每条上报一行，`lease_events` 每个租约生命周期事件一行，两张表都是 `MergeTree`，
都按 `toYYYYMMDD(event_time)` 分区，都带
`TTL toDateTime(event_time) + INTERVAL <SPINNERET_CLICKHOUSE_TTL_DAYS> DAY`（默认 `90`，范围 1–3650）。

单行很便宜 —— 所有低基数列都是 `LowCardinality(String)`，`event_time` 带
`CODEC(DoubleDelta, ZSTD(1))` —— 但**行数**等于你的全量请求量乘保留天数，所以这是唯一随流量线性增长的存储。
不要相信别人给的每行字节数，在自己的数据上量：

```bash
# spnrctl 只是把 .env 交给 Compose，并不会把它导出到你的 shell 里。每个会话先设一次密码 ——
# 这一页后面所有 clickhouse-client 命令都要用到它。
CLICKHOUSE_PASSWORD=$(grep -m1 '^CLICKHOUSE_PASSWORD=' deploy/compose/.env | cut -d= -f2-)

./spnrctl exec -T clickhouse clickhouse-client --user spinneret --password "$CLICKHOUSE_PASSWORD" -q "
  SELECT table, formatReadableSize(sum(bytes_on_disk)) AS disk, sum(rows) AS rows
  FROM system.parts WHERE active AND database = 'spinneret' GROUP BY table"
```

用磁盘除以行数量一次，然后乘 `速率 × 86400 × TTL天数`。内存是另一个更尖锐的问题：
`deploy/compose/config/clickhouse-limits.xml` 给各类缓存设了上限，而 `max_server_memory_usage` 必须**高于**
镜像的空闲常驻内存（约 1.2 GiB）—— 见下面的 ClickHouse 处置流程。

ClickHouse 是可选的。不配 `SPINNERET_CLICKHOUSE_URL`，控制平面照常工作，只有请求明细页面看不到东西。

### 服务实例

后面挂一个 Valkey 时，单实例能稳定承载：独占租约下 **4,500 个 acquire→report 周期/秒**，
`max_concurrent_leases: 4` 下 **5,500/秒**，在 200 ms 滞后目标内应用约 **20,000 条上报/秒**，以及 **10,000**
个并发 `WatchConfig` 长轮询（0.05 核、约 504 MiB）。在这些速率下，实例自身只用掉压测虚拟机 16 核里的
1.4–1.6 核，所以 v0.1 假定的每实例 4 vCPU / 8 GiB 是够的。全部数据见[性能与调优](./17-performance.md)。

请把实际负载规划在拐点的 **80 %** 左右。过了拐点这套系统不是缓慢变差，是直接崩塌 —— 见
[性能与调优 → 过了拐点：拥塞崩塌](./17-performance.md#过了拐点拥塞崩塌)。

### 一个算例

一个站点、**20 万身份**、**20 个端点组**、稳定 **2,000 个 acquire→report 周期/秒**、保留 90 天请求历史。

**Redis，全用默认值（`dedup_TTL = 1h`）：**

| 项 | 算式 | 大小 |
| --- | --- | ---: |
| 身份哈希 | 200,000 × 201 B | 38 MiB |
| 就绪队列 | 200,000 × 20 × 64.6 B | 246 MiB |
| 健康状态（全部预热后） | 4,000,000 × 71 B | 271 MiB |
| 去重标记 | 2,000/秒 × 3,600 秒 × 80 B | 549 MiB |
| 已结束的租约哈希 | 2,000/秒 × 3,600 秒 × 290 B | **1.94 GiB** |
| **工作集合计** | | **≈ 3.0 GiB** |

给 Redis 配 **6 GiB**。现在把 `SPINNERET_REPORT_DEDUP_TTL` 设成 `10m`（与迟到窗口相等，两个流量项一起缩小）：

| 项 | 算式 | 大小 |
| --- | --- | ---: |
| 数据集 + 健康状态 | 同上 | 555 MiB |
| 去重标记 | 2,000/秒 × 600 秒 × 80 B | 92 MiB |
| 已结束的租约哈希 | 2,000/秒 × 600 秒 × 290 B | 332 MiB |
| **工作集合计** | | **≈ 0.95 GiB** |

同样的负载，**2 GiB** Redis 就够了。一个变量的差别，就是 6 GiB 实例和 2 GiB 实例的差别。

**PostgreSQL：** 目录大约 20 万身份行，加最多 100 万条载荷版本行。聚合表受基数约束：20 个端点组乘几种判定
结果、每分钟一行，可以忽略；`identity_stats_hourly` 上限由流量给出 —— 2,000/秒 × 3,600 = 每小时 720 万请求，
分布在最多 200,000 × 20 × 判定结果种类数 个组合上 —— 实际大致是每小时几十万行，往前留 180 天。起步给
**100 GiB**，然后盯住这张表的那六个月分区。

**ClickHouse：** 2,000 条上报/秒 × 86,400 × 90 天 = `report_events` 里 **156 亿行**，再加上租约事件。在定磁盘
之前先量出自己的每行字节数；是这个存储决定你留 90 天还是 14 天。杠杆就是
`SPINNERET_CLICKHOUSE_TTL_DAYS`。

**服务实例：** 2,000 周期/秒是单实例独占租约拐点的 44 %。跑**两个**实例是为了可用性，不是为了吞吐，
`SPINNERET_ACQUIRE_FLEET_INFLIGHT` 保持默认即可。

---

## 备份

这套部署的一份备份是**三样东西**，缺一样就不算备份。

| 制品 | 为什么需要 |
| --- | --- |
| `postgres.dump` | `pg_dump -Fc`。唯一事实来源：目录、站点、身份、代理、策略、配置、密钥、用户、令牌、审计 |
| `secrets/kek.key` | 没有它，dump 里所有加密字段都无法恢复 |
| `.env` | 里面的口令就是当初建数据卷时用的口令 |

只有其中一部分时，你会损失什么：

| 你手里有 | 能恢复 | 不能恢复 |
| --- | --- | --- |
| dump + KEK + `.env` | 全部 | —— |
| dump + KEK | 全部，恢复到一套新口令的新栈里 | 重新挂回旧数据卷 |
| 只有 dump，没有 KEK | 所有路径、名称、描述、标签、策略、用户和权限 | **任何身份载荷、代理 URL、密钥值或通知渠道凭据。** 永久损失。服务端甚至起不来 |
| 只有 KEK + `.env`，没有 dump | 什么都恢复不了 | —— |

Valkey 故意不在清单里：热状态是从 PostgreSQL 派生出来的，`spnr rebuild` 能重新生成。ClickHouse 也不在：它存
的是请求级历史，自己按 TTL 过期。如果这份历史对你有价值，单独备份它（`clickhouse-backup`，或者停服后对
`chdata` 卷做文件系统快照），并且把它的丢失仅仅当成"请求明细看不到了"，别当别的。

### 命令

只用镜像自带的东西 —— 不需要额外工具：

```bash
umask 077
./spnrctl exec -T postgres pg_dump -U spinneret -Fc spinneret > postgres.dump
cp deploy/compose/secrets/kek.key  ./kek.key
cp deploy/compose/.env             ./env
```

*管理 → 10* 会把这三样一起写进 `<安装目录>/backups/<UTC 时间戳>/` —— dump 在 `umask 077` 下写出，两个副本
都按 `0600` 安装，`.env` 存成 `env` 这样列目录时不会被藏起来。但**目录本身**是按当前 umask 创建的，通常是
`0755`，所以自己动手收紧一次：`chmod 0700 <安装目录>/backups`。注意这个路径在**安装目录里面**，按目录卸载
会把它一起带走：**一定要复制到这台机器之外。**

在信任它们之前先验一下 dump 和密钥：

```bash
# pg_dump -Fc 归档以魔数 PGDMP 开头；被截断的 dump 不是。
head -c 5 postgres.dump   # PGDMP

# 每一行密钥都必须正好解码出 32 字节。只看第一行：轮换之后这个文件里会有多行
# `id:base64`，直接 base64 -d 会把它们一次全解出来。
head -1 kek.key | cut -d: -f2 | base64 -d | wc -c   # 32
```

这两项都不能证明 dump 能恢复。只有真的恢复一次才能，那就是下一节。

### 放在哪里、留多久

把密钥和 dump **分开**存放 —— 密钥管理系统、离线密码库、封好的信封。两者同时丢失是这套系统里唯一无法挽回的
故障。

每把 KEK 至少要保留到用它加密过的最旧那份备份失效为止。一把 KEK 从配置里删掉之后，还需要它的归档 dump 就
永久打不开了 —— 任何人都打不开。这也是安装脚本把被替换掉的密钥存成 `kek.key.<UTC 时间戳>.previous`、而不是
存成一个固定名字的原因：第二次恢复绝不能把第一次留下的密钥毁掉。

**在它离开这台机器、并且你真的往一套空栈里恢复过一次之前，它都还不算备份。** 别的都证明不了它能用。

---

## 恢复

### 演练

在需要它之前，有意识地在一台备用机器上把这套流程走一遍。

```bash
# 1. 干净的代码检出，然后放入这份备份自己的 .env 和密钥。Compose 只从 Compose 文件
#    所在目录读 .env，所以后面所有命令都在那个目录里执行。
git clone https://github.com/TikHub/Spinneret.git /srv/spinneret-drill
cd /srv/spinneret-drill/deploy/compose
cp /backups/20260918T031500Z/env      .env
mkdir -p secrets && cp /backups/20260918T031500Z/kek.key secrets/kek.key
chmod 0644 secrets/kek.key            # 容器以非 root 用户运行

# 2. 先起存储，构建镜像，建好表结构。
docker compose up -d --wait postgres valkey clickhouse
docker compose build migrate
docker compose run --rm migrate

# 3. 先停掉在提供服务的进程，再恢复。--clean 会 DROP 它准备重建的对象，而对一个
#    还有活连接在用的对象执行 DROP 会失败 —— 留下一个恢复了一半的 schema。
docker compose stop spinneret
docker compose exec -T postgres pg_restore -U spinneret -d spinneret --clean --if-exists \
  < /backups/20260918T031500Z/postgres.dump

# 4. 必做：热状态描述的是一瞬间之前那个数据库。
docker compose run --rm -T --entrypoint /usr/local/bin/spnr migrate rebuild

# 5. 起全栈，逐项检查依赖。
docker compose up -d --wait
curl -s 127.0.0.1:8080/readyz
```

然后要证明它真的成了 —— 能起来的栈还不等于解开过任何密文的栈：登录控制台，打开**身份**确认数量，打开一个
身份详情确认载荷字段是列出来的、而不是被标成无法解密，打开**密钥**做一次明文查看，再确认**审计日志**里有这
次明文查看记录。能登录但解不开密文的恢复，就是用错了密钥的恢复。

有五件事要知道：

1. **先停副本。** `--clean` 会 DROP 它准备重建的对象；对一个还有活连接在用的对象执行 DROP 会失败，结果是一
   个恢复了一半的 schema，而不是一个干净的错误。
2. **重建不是可选项。** 在它跑完之前，每个副本的 `/readyz` 都报
   `hotstate: epoch missing (rebuild pending)`，负载均衡器什么都不会转发。
3. **`pg_restore` 会很吵，这是正常的。** `--clean --if-exists` 会对本来就不存在的对象逐个报一句。要读输出，
   但"退出码为 0"和"没有输出"不是一回事。
4. **租约没有活下来。** 还握着恢复前租约的节点在 `Renew`、`Release` 和 `Report` 上会收到 `lease_unknown`，
   它们应该直接重新租借。
5. **恢复出来的站点是冷的。** 每个身份在每个端点组里都拿到同一个就绪队列分数，摊到随后 60 秒里，之后靠流量
   把它们打散。要预期一段 `exhausted` 偏高的时间 ——
   见[性能与调优 → 预热](./17-performance.md#刚播种或刚重建的站点必须先预热)。

*管理 → 11* 会针对 `<安装目录>/backups/` 下的某个备份目录执行第 3–5 步，要求你输入 `restore` 这个词，最后
一台一台地滚动重启副本。

### 用错 KEK 去恢复会怎样

这就是做错时你会看到的那个报错，值得一眼认出来。服务端启动、连上数据库，然后退出：

```text
load dedupe_pepper system key (is the KEK the one used to initialize this database?)
```

除了那把对的密钥，什么都救不了它。重建救不了，重新恢复救不了，迁移也救不了。这条消息里的系统密钥是用数据库
初始化时的当前 KEK 包装的，所以用另一把密钥恢复出来的 dump 解不开它 —— 而 dump 里所有身份载荷、代理 URL 和
密钥版本都是同样的处境。

如果备份里自带了 `kek.key` 并且和线上那把不同，*管理 → 11* 会告诉你，并在第二重确认（默认**否**）之后才愿
意替换线上密钥，替换前先把旧密钥存成 `kek.key.<UTC 时间戳>.previous`。替换线上密钥是这个脚本里唯一不可逆的
一步：已经用旧密钥加密过的东西仍然是用旧密钥加密的，只有那份带时间戳的副本还能打开它们。

不确定的时候有个安全做法：用这份备份自带的密钥和 `.env`，恢复到**另一套独立的栈**里，确认能打开，再决定线上
那套怎么处理。

---

## 升级与数据库迁移

迁移脚本内嵌在二进制里，由 `spnr migrate up` 应用，用一个会话级 PostgreSQL 建议锁（持在一条专用连接上）串
行化 —— 所以多个实例同时启动是安全的，单连接的连接池也不会和它死锁。Compose 编排把它放在 `spinneret`
依赖的一次性 `migrate` 服务里执行。

### 安全的顺序

```bash
# 0. 先备份：管理 → 10，或者用"备份"一节里的命令。
spnr migrate status
# database schema version 6 (up to date)

# 已发布镜像路线
./spnrctl pull && ./spnrctl run --rm migrate && ./spnrctl up -d --wait

# 源码路线
git pull && ./spnrctl build && ./spnrctl run --rm migrate && ./spnrctl up -d --wait

spnr migrate status
./spnrctl ps
curl -s 127.0.0.1:8080/readyz
```

**先**拉镜像或构建，迁移作为独立一步，最后才换副本。安装脚本顶层的 **2** 就是这个顺序，而且它在钉住新 tag
**之前**先拉取 —— 否则先写下 tag 再拉取失败，会让这套部署指向一个不存在的镜像。

### 升级前后各验什么

| 之前 | 之后 |
| --- | --- |
| `spnr migrate status` —— 记下版本号 | `spnr migrate status` —— 已是最新，且版本号按预期变化了 |
| `spnr config check` —— 配置对新二进制仍然合法 | `./spnrctl ps` —— 每个 `spinneret` 副本都到了 `healthy`，不只是 `running` |
| 有一份在机器之外的备份 | 每个副本的 `/readyz` 返回 `status: ok`，四项检查全过 |
| `sum(spinneret_stream_pending)` 接近 0 | `spinneret_stream_pending` 回落；`spinneret_stream_owned_shards` 重新加总到 `SPINNERET_REPORT_SHARDS` |
| | `spinneret_acquire_peers` 重新等于 api 副本数（优雅停止后立即；崩溃后约 60 秒内） |

### 滚动重启

Compose 会把一个扩了副本的服务的所有副本一起重建 —— 两副本的情况下就是五到十秒谁都起不来的窗口。负载均衡器
的重试能盖住它，但没必要把重试花在这上面：删掉一个容器，再执行
`up -d --no-recreate --no-deps spinneret`，就会按新的 spec 补上那个空位，其他副本不动。

```bash
for id in $(./spnrctl ps -q spinneret); do
  docker rm -f "$id"
  ./spnrctl up -d --no-recreate --wait --no-deps spinneret
done
./spnrctl up -d --wait
```

安装脚本的升级就是这么一台一台做的，一旦其中任何一步不成，立刻退回到普通的整体重建。给编排系统的停止宽限
时间要长于 `SPINNERET_SHUTDOWN_TIMEOUT`；Compose 文件针对默认的 30 秒设了 `stop_grace_period: 40s`。

### 怎么回滚

| 情况 | 怎么做 |
| --- | --- |
| schema 没变 | 把镜像 tag 换回去，滚动重启。又快又安全 —— 安装脚本顶层的 2 就接受更旧的 tag，正是为了这个 |
| schema 变了，而新版本坏了 | 恢复备份。这是安全路径 |
| 任何情况 | **永远不要用 `spnr migrate down`。** 向下迁移会 DROP 表和表里的数据 |

旧副本和新副本并存时会打出 `database schema is newer than this binary`。滚动升级期间这是正常的，最后一个旧
副本被换掉之后就消失；反过来，**新**二进制遇到旧 schema 会直接拒绝启动。

### 迁移规则

- 实际使用中迁移是单向的。`down` 存在、会删数据、不是回滚方案。
- schema 版本是一个数字，每个迁移在建议锁下原子地应用。
- **升级过程中绝不要改 `SPINNERET_REPORT_SHARDS`** —— 见[修改分片数](#修改分片数)。
- 节点不需要跟着一起升：节点 API 和 SDK 在两个方向上都忽略不认识的 JSON 字段，SDK 客户端会重试
  `unavailable`，而正在关停的副本上没打完的调用看起来就是这个。

---

## 扩容

**机制**部分 —— 什么能扩什么不能、角色表、分片怎么分、acquire 预算怎么分 —— 在
[安装与部署 → 横向扩容](./02-installation.md#横向扩容)。本节是运维这一侧：命令、一次变更收敛期间该盯什么、
以及怎么把一个实例摘出来。

### 增加和减少副本

实例是无状态的。所有需要协调的事情都通过 Redis 和 PostgreSQL 协调：上报分片归属（Redis 锁）、周期任务的
leader 选举（PostgreSQL 建议锁）、目录与配置失效（Redis pub/sub）。所以扩容就是一个变量：

```bash
SPINNERET_REPLICAS=4 ./spnrctl up -d --wait
```

围绕它有四件事要做对：

- **一个会话里每次 Compose 调用都要带上 `SPINNERET_REPLICAS`**，否则下一次调用会把服务缩回 `.env` 里的值。
  想永久生效就写进 `.env`。
- **抬 `SPINNERET_DATABASE_MAX_CONNS` 只能抬到** `副本数 × max_conns + 余量 < max_connections` 允许的程度。
  Compose 编排是 300 和 32，所以四个副本已经占掉 300 里的 128。
- **每个实例都要有互不相同的 `SPINNERET_INSTANCE_ID`。** 默认值是主机名加一段随机后缀，所以在任何环境下都
  天然互不相同。只有你自己钉死的 id 才会撞：两个实例共用一个 id 会退化成注册表里的同一个成员，于是每个都
  放行整个集群的 acquire 预算。
- **减少副本就是同一条命令换个更小的数字。** 分片归属和 acquire 预算都会自己重新收敛：优雅关闭的实例
  会主动从两个注册表里注销，所以是立即生效的。不必
  先排空，不过先排空更干净 —— 见下文。

横向扩容增加的是服务端容量：长轮询容量、上报处理、可用性。它**不**增加 Redis 容量，而 acquire 吞吐受 Redis
限制。粗略预算：独占租约下每 4,500 个 acquire→report 周期/秒一个 Redis 主节点，`max_concurrent_leases: 4`
下每 5,500/秒一个；超出之后用 `SPINNERET_REDIS_ADDRS` 走 Cluster 模式。

**这些都是单实例上限，不会随副本数相乘。** 所有已公开的吞吐数字都早于 acquire 准入控制门；在没有这道门的
条件下实测，负载均衡器后面两个副本合起来只到 **3,000 周期/秒** —— 比单实例还*低*。这正是那道门要解决的瓶颈，
而带门的数字目前还没有公开。请按单实例数字做规划，在假定它能相加之前先在自己的集群上验证，并先读
[性能与调优 → 准入控制](./17-performance.md#准入控制)。

### api / worker 拆分

`SPINNERET_ROLE` 取 `all`（默认）、`api` 或 `worker`。在
[安装与部署 → api / worker 角色拆分](./02-installation.md#api--worker-角色拆分)那张角色表之外，还有两条运维
规则：

- 至少保留**两个**能跑 worker 的实例，这样一个挂掉不会让上报管道停摆 —— 而且绝不能是零个。一个 worker 都
  没有时，上报会被接收、入队，但永远不会被应用：`sum(spinneret_stream_owned_shards)` 掉到 0，
  `spinneret_stream_pending` 无上限增长。
- `worker` 实例只提供 `/healthz`、`/readyz` 和 `/metrics`，**别的什么都不提供** —— 其余路径都是 404。也就是
  说它能通过就绪探针，但对节点毫无用处，所以要确认负载均衡器的 upstream 列表里只有 api 角色的实例。

### 一次扩容变更收敛期间该盯什么

两个注册表都是每 **2 秒**心跳一次，但对「漏掉心跳」的处理不同，因为它们保护的东西不同。**worker**
注册表的存活窗口是 **10 秒**：停止心跳的 worker 应该尽快交出分片，而且这样做是安全的，因为分片归属
本身是一把带 TTL 的锁。**acquire** 注册表的存活窗口是 **60 秒**：心跳需要 Redis，而准入控制真正
起作用的时刻恰恰是 Redis 饱和的时刻——窗口一短，繁忙的实例就会被同伴剪除，同伴随即用更小的数去分
全局预算，于是在最该守住的时候**放宽**了闸门。优雅关闭会让实例立刻把自己注销，所以长窗口只是延后
发现崩溃；而崩溃实例残留的成员身份只会让存活实例更窄，这是安全的方向。

因此：扩容时分片约十秒收敛，acquire 预算最多一分钟；优雅缩容时两者都立即收敛。盯这五条
序列：

| 指标 | 应该发生什么 |
| --- | --- |
| `spinneret_stream_owned_shards`（按实例） | 稳定到目标份额 `ceil(SPINNERET_REPORT_SHARDS / 存活 worker 数)` |
| `sum(spinneret_stream_owned_shards)` | 回到正好等于 `SPINNERET_REPORT_SHARDS`。**低于它就说明有上报没被应用** |
| `spinneret_stream_pending{shard}` | 被接管的分片上有一次短毛刺，然后回落。新 owner 先把前任领取了却没确认的条目做完 —— 这个毛刺就是机制在工作 |
| `spinneret_acquire_peers` | 达到真实的 API 实例数。随后 `spinneret_acquire_inflight_limit` 稳定到 `预算 / peers`，并被夹到 `[4, 4096]` |
| `spinneret_acquire_peer_beat_age_seconds` | 保持在几秒 |

新实例会短暂地放行超过自己那一份的量，因为它一开始假设自己是独自一个，然后随着发现同伴逐步收窄。这是有意
设计，也无害。**卡住不动**的 peer 计数就不是了：在二十副本的集群里，如果这个计数陈旧地停在 1，二十个实例会
各自向 Redis 放行整个预算。这正是 `spinneret_acquire_peer_beat_age_seconds` 和
`spinneret_acquire_peer_beat_failures_total` 的用途，也是限额变化时会带着新旧 peer 数打一条日志的原因 ——
这是把"我这个实例的 acquire 限额自己变了"和一波 shed 关联起来的唯一办法。

### 为维护排空一个实例

收到信号后的关停是有顺序的，整体由 `SPINNERET_SHUTDOWN_TIMEOUT`（默认 `30s`）约束：

1. `/readyz` 翻成 `503 {"status":"draining"}`，`/healthz` 继续返回 `200`，这样存活探针不会在排空中途把进程
   杀掉。
2. 停止 keep-alive；排空期间每个响应都带 `Connection: close`。
3. 等 `min(5s, SPINNERET_SHUTDOWN_TIMEOUT/4)`，给负载均衡器时间发现就绪检查失败。
4. 在途请求跑完；长轮询和事件流被取消，免得它们把窗口一直撑着。
5. 后台循环按层依次停止，让写入器把各服务产出的东西刷出去。

```bash
./spnrctl stop --timeout 40 spinneret     # 排空整个服务
docker stop --timeout 40 <container-id>   # 排空单个副本
```

给编排系统的停止宽限时间要**长于** `SPINNERET_SHUTDOWN_TIMEOUT`，否则它会在第 4 步中间把进程杀掉；Compose
文件针对默认的 30 秒设了 `stop_grace_period: 40s`。该实例还持有的分片锁会在 10 秒内过期，由其他实例接管。
**第二个**信号会立刻终止进程，所以卡住的关停总有办法掐断。

---

## 修改分片数

租约 id 里编码了它的上报必须进哪个分片，所以改 `SPINNERET_REPORT_SHARDS` 会**让在途租约失效**。这是一个维护
窗口，不是滚动变更。

1. 停掉节点流量 —— 或者接受变更之前发出的租约会把上报送进没人拥有的分片。
2. 等过你所有轮换策略里最长的 `lease_ttl`，让每个未结束的租约都过期，并让流排空：
   `sum(spinneret_stream_pending)` 归零。
3. 改值，然后**同时**重启**所有**实例。不要让新旧混跑。

**调小比调大更糟。** 租约 id 编码的分片号 `>=` 新分片数时，上报会被以 `lease_unknown` 拒绝，那些租约一直搁
着直到被回收。调大会把新租约重新路由过去，而不会拒绝在途租约 —— 但无论哪个方向，都请把这个值当成部署生命
周期内固定不变的。合法范围是 1–255；默认的 16 一直够用到几十个 worker 实例。

---

## 重建热状态

Redis 里放的是派生状态：就绪队列、可用性、健康分、熔断器窗口、租约。如果数据卷被重建、被清空，或者你有理由
认为它和 PostgreSQL 不一致，那就重建 —— 不要去恢复 Redis。

```bash
spnr rebuild                                                      # 所有站点；先删掉 epoch
spnr rebuild --tenant default --namespace default --site example-site   # 只重建一个站点，没有全局窗口
```

### `spnr rebuild` 到底做了什么

在 PostgreSQL 建议锁 `spinneret:hotstate:rebuild` 下执行，所以同时只会有一个在跑；对每个站点：

1. 写站点元数据哈希；
2. 从 `hot_state_snapshots` 表里把 Redis 中缺失的健康状态恢复回来 —— 这张表由 leader 的 `hotstate_snapshot`
   任务每 **60 秒**写一次，所以重建**不是**把系统学到的东西清零，最多丢掉最近一分钟；
3. 以权威模式重新物化代理和身份，带冷启动保护：本该落在过去的就绪分数会被均匀摊到接下来 60 秒里；
4. 清掉队列里的陈旧成员，并移除数据库中已不存在的站点在 Redis 里的残留；
5. 最后写入一个新的 epoch 键。

全量重建会**先删掉 epoch 键**，所以在它跑完之前每个副本的 `/readyz` 都报
`hotstate: epoch missing (rebuild pending)`，节点收到 `503 rebuilding`。这是一次有意的停服。用 `--site` 指
定单个站点可以完全避开它 —— 只重新物化那一个站点，这也是白天该用的形式。

### 要跑多久

主要取决于要写进 Redis 的 身份数 × 端点组数，再加上对 `hot_state_snapshots` 的分页读取（每页 5,000 行）。

一个实测数据点，环境是[性能与调优](./17-performance.md#测试环境)里那台笔记本级虚拟机：单站点 10 万身份
× 50 个端点组对应 `hot_state_snapshots` 里 **489 万行**（980 页），在丢弃 Redis 数据卷之后，启动时的
自动重建耗时 **24.5 分钟**（取自 `hot-state rebuild finished` 日志行的 `duration` 字段），期间
`/readyz` 一直报 `hotstate: building`，而且两个副本在同时做这份工作。在安排维护窗口之前，关于这个
数字有两点值得知道：

- 它随 身份数 × 端点组数 增长，与流量无关。端点组减半，重建时间也减半。
- **每个 API 副本都会各自独立重建一遍。** 这个过程是幂等的，所以结果是对的，但两个副本会把同一份分页
  读取做两次，并且在此期间争抢同一个 PostgreSQL 和 Redis。如果你是在争分夺秒地恢复一个大规模部署，
  先只起**一个**副本，等它跑完，再起其余的。

所以：小规模部署上重建是喝杯咖啡的事；到十万身份量级就是一个维护窗口。别信这两个形容词里的任何一个，
在你自己的数据副本上实测一次。

用 `spnr rebuild` 启动的重建，结束时会在它自己的标准输出上打出耗时 —— `rebuilt hot state of every site in …`，
或者 `rebuilt hot state of site <tenant>/<namespace>/<site> in …`。把一次重建夹住的那两行日志
（`hot-state rebuild started` 带站点数、`hot-state rebuild finished` 带站点数和一个 `duration` 字段）由真正
执行重建的那个进程写出：走 CLI 时那是一次性的 `migrate` 运行，不是 `spinneret` 服务。要看**启动时自动**触发的
那次重建：

```bash
./spnrctl logs spinneret | grep 'hot-state rebuild'
```

**不要用 `DBSIZE` 看进度。** 恢复过程用 `HSETNX` 把身份健康度写进「每个端点组一个哈希」里，所以
10 万身份 × 50 个端点组只会新增 **50 个键**，而不是 500 万个 —— 整个重建期间 `DBSIZE` 纹丝不动，
看起来就像卡住了。改看其中一个哈希的字段数，它会朝身份总数增长：

```bash
./spnrctl exec valkey valkey-cli --scan --pattern 'sp:*:hs:*' --count 100 | head -1   # 先挑一个键
./spnrctl exec valkey valkey-cli hlen '<上面打印出来的键>'
```

拿自己数据的副本量一次，你就知道一次全量重建是一杯咖啡的事，还是一个维护窗口。

epoch 键缺失时，实例在**启动时也会自动重建**。所以 Redis 整体丢失其实不需要人工干预，只需要耐心。

### Redis 丢失会毁掉什么，且重建也拿不回来

| 丢失的东西 | 后果 |
| --- | --- |
| 在途租约 | 节点上报时收到 `lease_unknown`，重新租借即可 |
| 还在流分片里排队的上报 | 那些判定结果永远到不了状态机 |
| 当前的熔断器评估窗口 | 熔断器从一个干净窗口重新开始 |
| 最多 60 秒的健康分变化 | 上一次快照之后发生的一切 |
| 控制台会话 | 所有人重新登录 |

刚重建出来的站点，调度成本会短暂高于已预热的站点，因为每个身份在每个端点组里都从同一个就绪队列分数开始，
靠流量把它们打散。在 10 万身份的数据集上实测：冷站点在 2,500 周期/秒就崩了，而同一个站点预热之后能扛
4,500/秒。

**注意。** `spnr rebuild --site` 不会重置活跃租约计数之类的运行时计数器，这是有意的 —— 重建绝不能把活着的
租约丢掉。如果这些计数器真的坏了，修法是 unlink 整个站点键前缀然后重建那个站点。绝不要单独 unlink 一个活着
的租约哈希（`<前缀>:{s<siteKey>}:ls:*`，默认前缀、站点键 12 时就是 `sp:{s12}:ls:*`）：租约结束时正是这个哈希
去把身份的活跃租约计数减一，删掉一个就会把计数漏
掉，那个身份再也不会变成可用。

---

## 密钥轮换

轮换是在线的：所有已配置的密钥都能解包，只有当前那把用来包装新的数据密钥。它重新加密的只是那一小列被包装的
DEK，不是数据本身，所以这个任务很便宜，在有流量时执行也安全。

```bash
# 1. 生成一把新密钥并追加到文件里。默认最后一行是当前密钥。
spnr kek generate --id k2 >> deploy/compose/secrets/kek.key

# 2. 重启实例，让它们加载新的密钥列表。KEK 集合只在启动时读一次，而密钥文件是
#    bind-mount 的 secret，所以只改文件时 `up -d` 什么都不会重建 —— 必须显式重启进程。
./spnrctl restart spinneret

# 3. 确认两把密钥都加载了，并看还有多少活要干。
spnr kek status
# current kek: k2
#   k1               wrapped_records=18422
#   k2               wrapped_records=0 current
# rewrap: idle (0/0)

# 4. 把所有已存的数据密钥重新包装到当前 KEK 上。可续跑；如果别的实例已经在跑，
#    它会跟随那个任务的进度而不是再起一个。
spnr kek rewrap
# kek rewrap started
# progress: 5000/18422 records re-wrapped to k2
# ...
# kek rewrap finished

# 5. 只有当 k1 显示 wrapped_records=0 之后，才删掉它那一行并重启。
spnr kek status
```

### 重新包装做了什么

| 属性 | 值 |
| --- | --- |
| 覆盖的表 | `identity_payloads`、`secret_versions`、`proxies`、`notification_channels`、`system_keys` |
| 批大小 | 500 行 |
| 并发 | 同时只有一个实例在跑，靠建议锁 `spinneret:kek:rewrap` |
| 进度 | 持久化在 `system_settings` 的 `kek_rewrap_status` 键下，所以每个实例和控制台都能报进度 |
| 心跳 | 每 2 秒；任务所在实例停止心跳 30 秒后被报告为已中断 |
| 安全性 | 每次更新都是对旧 KEK id **和**旧包装 DEK 的 compare-and-swap，所以并发被重新封装过的记录绝不会被覆盖 |

控制台在 **密钥 → 主密钥列表** 下做同样的事，带每把密钥的包装记录数和一个进度条。中断 CLI 会停掉它启动的
那个任务；再跑一次会从还不在当前密钥上的那些记录继续。

### 怎么验证

```bash
spnr kek status
# current kek: k2
#   k1               wrapped_records=0
#   k2               wrapped_records=18422 current
# rewrap: idle (18422/18422)
# last finished: 2026-09-18T03:41:07Z
```

要看三件事：每把退役密钥的 `wrapped_records=0`；没有任何密钥被标成 `NOT-CONFIGURED`（那意味着仍有记录引用着
本进程没有的密钥，那些行解不开）；没有 `last error:` 行。然后确认数据真的能打开：在控制台做一次明文查看，
再打开一个身份的载荷字段。最后把密钥文件重新备份一遍。

### 怎么退回去

轮换是容错的，因为整个过程中两把密钥都在配置里：

| 阶段 | 怎么退 |
| --- | --- |
| 做完第 1–2 步、还没重新包装 | 设 `SPINNERET_KEK_CURRENT=k1`（或把 `k1` 放到最后一行）并重启。没有任何东西是用 `k2` 包装的 |
| 重新包装进行中 | 同上，然后再跑一次 `spnr kek rewrap`，把已经换过去的记录搬回 `k1` |
| 第 5 步做完、`k1` 已删除 | 退不回去，也不需要退：所有记录都在 `k2` 上，而 `k2` 是配置里的。真正的风险在反面 —— 轮换**之前**取的 dump 仍然需要 `k1` |

**每把退役密钥都要留着**，直到 `spnr kek status` 报告用它包装的记录数为零，**并且**你还在依赖的备份里没有一
份是在它之下取的。信封加密本身的细节见
[密钥保管库 → 轮换 KEK](./10-secrets.md#轮换-kek)。

---

## 数据保留与磁盘

### 所有保留期设置

| 设置 | 默认值 | 清理什么 | 机制 |
| --- | --- | --- | --- |
| `SPINNERET_RETENTION_RISK_EVENTS` | `720h`（30 天） | `risk_events` | 丢弃按天分区 |
| `SPINNERET_RETENTION_MINUTE_STATS` | `720h`（30 天） | `outcome_stats_minutely`、`acquire_stats_minutely`、`node_stats_minutely`、`payload_access_minutely` | 丢弃按天分区 |
| `SPINNERET_RETENTION_HOUR_STATS` | `4320h`（180 天） | `identity_stats_hourly` | 丢弃按月分区 |
| `SPINNERET_RETENTION_STATE_EVENTS` | `8760h`（365 天） | `state_events` | 丢弃按月分区 |
| `SPINNERET_RETENTION_AUDIT` | `8760h`（365 天） | `audit_logs` | 丢弃按月分区 |
| `SPINNERET_CLICKHOUSE_TTL_DAYS` | `90` | `report_events`、`lease_events` | ClickHouse `TTL`，由 ClickHouse 自己执行 |
| `SPINNERET_REPORT_DEDUP_TTL` | `1h` | Redis 里的上报去重标记和已结束的租约哈希 | Redis 键过期 |
| `SPINNERET_LATE_REPORT_WINDOW` | `10m` | Redis 里已结束的租约哈希（下限） | Redis 键过期 |
| `SPINNERET_STREAM_MAXLEN` | `1000000` | 每个分片的上报流积压 | 分片 owner 裁剪到消费位点 |

每个 PostgreSQL 保留期至少 `24h`。`alert_events` 按固定 **90 天**清理，每批 5,000 行，不可配置。身份载荷版
本每个身份保留最近 **5** 个。

### 分区是怎么被丢弃的

每小时一次的 `partition_manager` 任务在 **leader** 实例上运行，超时 15 分钟，做三件事：提前创建缺失的分区、
丢弃已过期的分区、清理 `alert_events`。

分区命名是按天表 `<表名>_p<YYYYMMDD>`、按月表 `<表名>_p<YYYYMM>`。只有符合这个命名规则的才会被考虑，而且只
有当分区的**上界**不晚于 `now - 保留期` 时才丢弃 —— 也就是说，只要一个分区还可能装着保留期之内的行，它就绝
不会被丢掉。第一遍目录扫描不加锁，所以常见的"没有过期分区"这一趟永远不会阻塞读写；只有真的发现了过期分区，
才会对父表加 `ACCESS EXCLUSIVE` 锁，并且只在执行 DROP 期间持有。

按天表始终覆盖从昨天到往后 7 天；按月表覆盖从上个月初到往后 3 个月。

调大保留期只影响从那之后写入的数据 —— 已经被丢掉的分区就是没了。调小则会在下一次每小时任务里丢弃分区。

如果这个任务一直失败，分区就不再被丢弃，磁盘会悄悄涨满。这正是
`spinneret_job_runs_total{job="partition_manager",result="error"}` 的用途。

### 急着腾磁盘时

按该动手的先后顺序。

**1. 先查清空间去哪了。** 这里以及下面的 ClickHouse 命令都需要你自己 shell 里有 `CLICKHOUSE_PASSWORD` ——
按[容量规划](#容量规划)里的写法设一次。

```bash
docker system df -v
./spnrctl exec -T postgres psql -U spinneret -c "
  SELECT relname, pg_size_pretty(pg_total_relation_size(c.oid)) AS size
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname='public' AND c.relkind IN ('r','p')
  ORDER BY pg_total_relation_size(c.oid) DESC LIMIT 20"
./spnrctl exec -T clickhouse clickhouse-client --user spinneret --password "$CLICKHOUSE_PASSWORD" -q "
  SELECT table, partition, formatReadableSize(sum(bytes_on_disk)) AS disk
  FROM system.parts WHERE active AND database='spinneret'
  GROUP BY table, partition ORDER BY sum(bytes_on_disk) DESC LIMIT 20"
```

**2. 调小某个保留期，让下一次每小时任务去干活。** 这是最干净的杠杆。改变量、重启实例、最多等一个小时 ——
或者重启 leader 强制跑一趟，因为该任务在拿到 leader 之后几乎立刻执行一次。

**3. 立刻丢弃 PostgreSQL 分区**，用的就是那个任务用的同一个函数，截止时间你自己定：

```bash
./spnrctl exec -T postgres psql -U spinneret -c \
  "SELECT spinneret_drop_partitions_before('identity_stats_hourly','month', now() - interval '60 days')"
./spnrctl exec -T postgres psql -U spinneret -c \
  "SELECT spinneret_drop_partitions_before('outcome_stats_minutely','day', now() - interval '7 days')"
```

它返回丢弃的分区数，只在真的有分区过期时才对父表加锁，遇到非分区表会直接报错。粒度要用这张表真正的粒度 ——
上面那张表里的 `day` 或 `month`。

**4. 立刻丢弃 ClickHouse 分区。** 分区是一天一个，名字形如 `YYYYMMDD`：

```bash
./spnrctl exec -T clickhouse clickhouse-client --user spinneret --password "$CLICKHOUSE_PASSWORD" -q \
  "ALTER TABLE spinneret.report_events DROP PARTITION '20260601'"
```

调小 `SPINNERET_CLICKHOUSE_TTL_DAYS` 也有效 —— 当配置值与表上带的值不同时，服务端会在启动时执行
`ALTER TABLE … MODIFY TTL` —— 但随后 ClickHouse 会在已有数据块上物化这个新 TTL，那是一次很重的后台重写。
丢弃整个分区是瞬时的；改 TTL 才是长期的修法。如果**现在**就缺磁盘，先做丢弃。

**5. 旧镜像和构建缓存。** *管理 → 12* 会列出属于本 Compose 项目、且没有任何容器（运行中或已停止）引用的镜
像，并询问是否删除，默认不删。它也能清构建缓存，但 BuildKit 的缓存是**按主机而不是按项目**的：清掉之后，
这台机器上任何项目的下一次构建都是冷构建。本项目从不自作主张地清理一台共享主机。

**不要做的事：** 不要删 `chdata` 卷来腾空间。它自己会按 TTL 过期行，删掉它等于为了一个丢弃分区就能精确解决
的问题把全部请求历史扔掉。另外，绝不要裁剪还存着未处理上报的上报流，也不要 unlink 活着的租约哈希 ——
见[重建热状态](#重建热状态)那条注意。

---

## 账号、口令与令牌

有三件事只存在于控制台 RPC，没有对应的 CLI 命令：修改口令、添加用户、列出用户。安装脚本的管理菜单通过控制
台用的同一个端点、在 loopback 上访问它们，口令读入时不回显、通过 stdin 传入 —— 绝不会作为命令行参数，因为
`argv` 是这台机器上任何进程都能读到的。

| 要做的事 | 在哪做 |
| --- | --- |
| 修改管理员口令 | 控制台，或 *管理 → 1*。该账号的其他所有会话都会结束。还会询问是否同步更新 `.env` 里的 `SPINNERET_ADMIN_PASSWORD`，免得它过期 |
| 添加管理员 | 控制台 **访问控制 → 用户 → 新建**，或 *管理 → 2*。`admin` 是租户级的，不是平台级的 |
| 节点令牌 | `spnr token create --name … --scope …`（两个参数都是必填的），或 *管理 → 4*。明文只打印一次，之后拿不回来 |
| 吊销一个令牌 | 控制台 **令牌**，或 `RevokeToken`。通过事件总线全集群生效；总线不可用时，在 `SPINNERET_TOKEN_CACHE_TTL`（30 秒）内生效 |
| 管理员口令丢了 | `spnr admin init` 是幂等的，**不会**重置已存在的口令。用另一个管理员账号重置，或者以平台管理员身份在控制台里重置 |

权限范围、角色、绑定，以及各自能触达什么：[租户、用户与令牌](./11-access-control.md)。

---

## 日常巡检

### 每天，两分钟

| 看什么 | 正常 |
| --- | --- |
| 每个副本的 `/readyz` | `status: ok`，四项检查都是 `ok` |
| `sum(spinneret_stream_pending)` | 接近 0；有毛刺但会回落 |
| `spinneret_report_lag_seconds` p99 | 几十毫秒。约 10,000 上报/秒时实测 9.5 ms，约 20,000/秒时 30.7 ms |
| `spinneret_acquire_duration_seconds` p99（按站点） | 个位数毫秒。4,500 周期/秒时实测 1.86 ms |
| `spinneret_acquire_total{result="ok"}` 占比 | 远高于 95 %。`overloaded` 要单独看：那是在卸载，不是失败 |
| `spinneret_breaker_state` | 处处为 `0`。任何不是人为打开的 `2` 都值得看一眼 |
| `spinneret_identities_available`（按端点组） | 每个有流量的组都大于 0 |
| 控制台**概览**的站点卡片 | 没有哪个站点的风险占比相比昨天在往上爬 |

### 每周，二十分钟

| 看什么 | 正常 |
| --- | --- |
| `spinneret_stream_owned_shards` 求和 | 正好等于 `SPINNERET_REPORT_SHARDS` |
| `spinneret_acquire_peers` 和 `spinneret_acquire_peer_beat_age_seconds` | peers 等于 api 副本数；心跳年龄几秒 |
| `spinneret_job_runs_total{result="error"}`（按 job） | 平的。`partition_manager` 上出现任何斜率都意味着磁盘不再被清理了 |
| `spinneret_db_write_batches_total{result="dropped"}`、`spinneret_state_writer_dropped_changes_total` | **永远是 0。** 任何增长都是数据丢失 |
| `spinneret_report_total{outcome="unknown"}` 占比 | 低于 20 %。更高说明信号规则没能分类节点上报的内容 |
| `spinneret_lease_reaped_total{kind="abandoned"}` 与 `ok` acquire 的比 | 低于 5 %。更高说明节点租了却不上报 |
| `spinneret_notify_deliveries_total{result!="ok"}` | 0，否则下次故障你不会收到通知 |
| 三个数据卷的磁盘，以及分区列表 | 按保留期推算的速度增长，没有更快 |
| 有一份在机器之外的备份，且密钥能正确解码 | 是 |
| `spnr kek status` | 没有 `NOT-CONFIGURED` 的密钥，退役密钥上没有记录 |
| 控制台**审计日志** | 没有解释不了的条目；每条 `secret.reveal` 旁边都有名字 |

### 每月或每个版本

- 在备用机器上跑一次恢复演练。从没恢复过的备份只是一个假设。
- 拿实际观察到的增长重新核对容量，尤其是 `identity_stats_hourly` 和 ClickHouse。
- 检查令牌过期时间，吊销已经没有节点在用的。
- 如果制度要求，或者有掌握密钥的人离职，就轮换 KEK。

---

## 故障处置手册

每一份都包含：怎么确认、怎么止血、怎么修、怎么防。从现象出发的排查（包括完整的错误原因对照表）在
[故障排查](./18-troubleshooting.md)；这里是那些会把你叫起来的情况下的**运维动作**。

### 1. 身份池在被抽干

节点收到 `429 no_identity_available`，可用身份的占比一直往下掉。

**确认。** `spinneret_identities_available{site,group}` 趋向 0，同时
`spinneret_acquire_total{result="exhausted"}` 在涨。`spinneret_identities{state=…}` 告诉你它们去哪了：
`banned`、`quarantined`、`expired` 还是 `disabled`。响应头 `Spinneret-Retry-After-Ms` 是服务端估计还要多久。
在控制台里，**冷却热力图**直接给出答案：整列不可用是某个端点组的问题，整行是某个身份的问题，整张网格都不可
用就是某条规则的问题。

**止血。** 先判断池子是**真的**用完了，还是某条规则在吃它。如果是动作在不停触发，把动作策略切成 `shadow`
模式发布 —— 它仍然评估、仍然记录，但不再改状态。如果是站点那边在主动敌对，就把站点暂停
（`SetSitePaused`），而不是让所有身份挨个被封禁。

**修。** 如果是规则误伤，把损失回滚：`RevertActions`，带上 `time_range`（start 必填）、干了这事的
`policy_id` 和 `rule`，先用 `dry_run: true` 看影响范围。如果连续失败计数本身就是错的，再加上 `reset_failures`
和 `reset_health`。如果池子是真的用完了，答案是更多身份、更长的 `reuse_interval`、或者更少的并发节点 ——
不是把封禁规则放松。

**防。** 新的动作策略先用 `shadow` 模式发布，比较它**本来会**做什么和你希望它做什么。告警按端点组看
`spinneret_identities_available`，不要只看总数。

### 2. 某个站点在被整体封禁

对某一个站点使用过的身份，几分钟内全都回来带着封禁。

**确认。** `spinneret_report_total{site,outcome=~"banned|forbidden|captcha"}` 在该站点占主导，
`spinneret_actions_total{site,action="ban"}` 在爬。控制台里该站点的**风险事件**会显示触发的规则和背后的标记。

**止血。** 暂停站点。站点开关就是为这个存在的：节点收到 `503 site_paused`、重试，在你思考的时候不再继续烧
身份。另一种选择 —— 让它继续跑 —— 会把对端的一次变更变成一个永久受损的身份池。

**修。** 改策略之前先把上报读一遍。如果对端确实开始拒绝了，改策略没有用，要改的在节点侧：换代理、换身份类
型、降速率。如果是信号规则把一种新的响应形态**误读**成了封禁，那就修规则、发布，然后对这些规则生效的那个时
间窗用 `RevertActions` 把它造成的封禁回滚。

**防。** 把"unknown"的那条信号规则收窄到足够窄，让一个不认识的响应不会被分类成封禁。对
`spinneret_report_total{outcome="unknown"}` 的占比设告警，这样响应形态的变化会先以"未分类"的形式暴露出来，
而不是先以整体封禁的形式暴露出来。

### 3. 熔断器在抖

某个端点组熔断、关闭、又熔断。

**确认。** 同一个 `site`/`group` 上 `increase(spinneret_breaker_transitions_total{to="open"}[30m])` 大于 3，
且 `spinneret_breaker_state` 在 `2` 和 `0` 之间来回。控制台**熔断器**页列出每次转换及其原因。

**止血。** 手动把熔断器打开（`OpenBreaker`，带一个时长），让这个组别再往一个还没恢复的对端半开试探。一直保
持熔断对节点来说是干净的 `503 circuit_open`；抖动则是一串间歇性失败，还会不停搅动身份状态。

**修。** 抖动几乎都意味着熔断器的恢复条件相对于故障太急了：半开探针成功，全量流量回来，失败率又越过阈值。
熔断策略里有四个字段，按这个顺序试：加长 `open_duration`（并让 `max_open_duration` 对反复熔断做退避）、抬高
`half_open.close_min_samples` 让关闭前必须有远多于几次的成功探针、抬高
`half_open.close_success_ratio_gte`、放宽 `window` 或抬高 `min_requests` 让糟糕的一分钟不足以触发熔断。全部
字段见[策略](./08-policies.md)。

**防。** 对转换**速率**告警，不要对状态告警 —— 熔断五分钟和半小时里熔断六次是两种不同的故障。

### 4. 上报滞后在变大

调度器决策依据的状态越来越旧。

**确认。** `spinneret_report_lag_seconds` p99 在涨，`sum(spinneret_stream_pending)` 是在增长而不是在震荡。
然后一步把原因分开：

| 检查 | 说明 |
| --- | --- |
| `sum(spinneret_stream_owned_shards)` < `SPINNERET_REPORT_SHARDS` | 有分片没人拥有：worker 实例不够，或者其中一个卡死了 |
| 等于分片数，worker 很忙 | worker 是瓶颈 —— 加 worker 实例 |
| 等于分片数，pending 均匀分布，CPU 空闲 | 相对实例数来说分片太少了 |
| `spinneret_report_process_duration_seconds` p99 升高 | 单条上报的处理变慢了 —— 通常是 PostgreSQL |
| `spinneret_db_write_batches_total{result="error"}` 在涨 | 问题在 PostgreSQL，不在 worker |

**止血。** 加能跑 worker 的实例。`SPINNERET_STREAM_MAXLEN`（每分片 100 万）限制了积压最多能留多少 —— 超过
之后条目会被静默丢弃，所以一个在增长的积压是有截止时间的。

**修。** 每个 worker 实例保持 2–4 个分片。单实例在 200 ms 目标内能应用约 20,000 条上报/秒；节点侧批量上报是
免费的吞吐，因为 200 条一批，每个在批里出现的流分片只花一次 `ingest.lua` 调用（最多
`SPINNERET_REPORT_SHARDS` 次，还是流水线发出的），不是 200 次。

**防。** 对 `sum(spinneret_stream_pending)` 和"`sum(spinneret_stream_owned_shards)` 低于分片数"设告警。后者
正是那条能在任何人察觉之前抓住"上报被接收了，但什么都没发生"的告警。

### 5. acquire 延迟高，或者服务端在卸载

节点看到 acquire 变慢，或者收到 `503 overloaded`。

**确认。** 按这个顺序一起读三条序列：

| `spinneret_acquire_script_seconds` p99 | `spinneret_acquire_total` | 含义 |
| --- | --- | --- |
| 正常 | `overloaded` 在涨，Redis CPU 还有余量 | 准入预算比 Redis 能承受的更窄 |
| 有尖刺 | `overloaded` 在涨 | **Redis 卡住了。** 这时抬预算只会更糟 |
| 任意 | `exhausted` 在涨，**同时** Redis CPU 很高 | 已经过了拐点 —— 拥塞崩塌 |

**`overloaded` 不是故障。** 它是准入控制在服务端、在任何 Redis 命令发出之前把负载卸掉，以免整个集群崩塌。
稳定的低比例 `overloaded` 应该被当成容量信号，不是事故。

**止血。** 如果已经过了拐点，必须让实际负载降到拐点之下这个循环才会解开 —— 它一旦开始就不会自己恢复。暂停
最忙的站点，或者把节点限速。它的特征极其明确：崩塌时一次 acquire 发出了 **220 条 Redis 命令而不是 49 条**，
`EVALSHA` 平均 **157 µs 而不是 27 µs**，Valkey 烧掉 3.5–3.7 核去服务健康时同等负载所需命令量的四倍。

**修。** 看你匹配到表里哪一行：抬 `SPINNERET_ACQUIRE_FLEET_INFLIGHT`，或者去找 Redis 卡顿的来源（AOF 重写、
一次 fork、邻居吵闹），或者加 Redis 容量并降低实际负载。动预算之前先确认 `spinneret_acquire_peers` 是对的 ——
peer 计数卡住会让每个实例都按陈旧的份额放行。完整分析见
[性能与调优 → 准入控制](./17-performance.md#准入控制)。

**防。** 把实际负载压在实测拐点的 80 % 以内。准入控制保持开启。给每个 API 实例互不相同的
`SPINNERET_INSTANCE_ID`。`overloaded` 的占比要和其他 acquire 失败**分开**告警。

### 6. Redis 内存耗尽

**确认。** Valkey 拒绝写入，或者退出码 137 的重启循环，且
`docker inspect --format '{{.State.OOMKilled}}' <container>` 返回 `true`。*管理 → 7* 会帮你检查 ClickHouse、
Valkey 和 PostgreSQL。

```bash
./spnrctl exec -T valkey valkey-cli info memory | grep -E 'used_memory_human|maxmemory_human'
```

**止血。** 给它更多内存。淘汰**不是**选项：`noeviction` 是有意设的，淘汰这部分状态会静默地把身份池弄坏，而
不是明确地失败。如果实在加不了内存，就压缩流量项：调小 `SPINNERET_REPORT_DEDUP_TTL`（最小 `1m`）并重启实
例 —— 已存在的标记仍按旧 TTL 过期，所以缓解是在接下来一小时里逐步到来的，不是立刻。

**修。** 用[容量规划](#容量规划)里的公式算出工作集，按两倍准备。再拿 `SPINNERET_STREAM_MAXLEN` 对照你想扛过
的积压：`速率 × 秒数 / 分片数`。

**防。** 对 Valkey 的 `used_memory` 相对其上限设告警。保留自带的那几项 Valkey 设置 —— `--save ""` 加
`appendonly yes`，以及抬高的 AOF 重写阈值 —— 因为 AOF 重写是这套系统里最大的一个稳定性发现：用默认值时
4,000 周期/秒下每约 52 秒就重写一次，每次 fork 都是一次多秒级的 I/O 停顿，而在拐点之下那个亚稳区里，一次停
顿就足以启动拥塞循环。

### 7. PostgreSQL 连接数或磁盘耗尽

**确认（连接数）。** 日志里出现 `FATAL: sorry, too many clients already`，或者：

```bash
./spnrctl exec -T postgres psql -U spinneret -c \
  "SELECT count(*), (SELECT setting FROM pg_settings WHERE name='max_connections') FROM pg_stat_activity"
```

**止血。** 调小 `SPINNERET_DATABASE_MAX_CONNS` 并重启副本，或者抬高服务端的 `max_connections`。必须成立的算
式是 `副本数 × max_conns + 余量 < max_connections`；Compose 默认是 300 和 32。

**确认（磁盘）。** 用[急着腾磁盘时](#急着腾磁盘时)里的表大小查询，再加上
`spinneret_job_runs_total{job="partition_manager",result="error"}`。

**止血。** 用 `spinneret_drop_partitions_before` 立刻丢弃过期分区。如果冷却相关的行在 `state_events` 里占主
导而你又不需要它们，把 `SPINNERET_RECORD_COOLDOWN_EVENTS=false`。

**修。** 设成你磁盘负担得起的保留期，并确认 `partition_manager` 在 leader 上跑得干净。

**防。** 对 `spinneret_job_runs_total{job="partition_manager",result="error"}` 和数据卷磁盘设告警。失败的分区
管理器是一个安静的磁盘泄漏：在磁盘满之前什么都不坏，满了之后什么都坏。

### 8. ClickHouse 在拒绝查询

**确认。** 请求明细页报错或者什么都没有，而 acquire、上报、策略和配置全都照常工作 —— ClickHouse 不在控制平
面路径上。服务端日志里会有 `clickhouse batch insert failed`。最常见的原因是
`MEMORY_LIMIT_EXCEEDED`。整个过程中容器还是**健康**的，因为它的健康检查只问 `/ping`。

**止血。** 如果是内存问题：`deploy/compose/config/clickhouse-limits.xml` 里的 `max_server_memory_usage` 必须
**高于**进程的空闲常驻内存（alpine 镜像约 1.2 GiB）。设得比它低，ClickHouse 不会缩下去适应 —— 每次 `INSERT`
都失败，上报 worker 就卡在一个本该是可选的存储上。抬高它，或者给主机更多内存，然后重启该服务。

如果是磁盘问题，丢弃分区（见上文）。如果是控制台一次查询开得太宽，把时间窗收窄 —— 请求明细自己有限制，会
返回 `query_too_large` 或 `query_timeout`，不会把服务端搞垮；见
[可观测性 → 查询限制，以及过宽查询如何干净地失败](./12-observability.md#查询限制以及过宽查询如何干净地失败)。

**修。** 限的是各类缓存而不是总量：光是默认的 mark cache 就是 5 GiB，那是给专用分析机准备的尺寸。
`SPINNERET_CLICKHOUSE_TTL_DAYS` 要按你有的磁盘来定。

**防。** 对 ClickHouse 的插入失败告警，不要对它的健康检查告警。记住 ClickHouse 故障损失的只有历史数据，别把
它升级成控制平面事故。

### 9. 某个实例卡死了

在运行、没在排空、但没在正确地提供服务。

**确认。** `/healthz` 返回 `200` 但 `/readyz` 不是 `ok`；或者 `/readyz` 正常，但该实例一个分片都不拥有（其
他实例在扛负载时它的 `spinneret_stream_owned_shards` 为 0）；或者它的
`spinneret_acquire_peer_beat_age_seconds` 已经爬过 30 秒。`./spnrctl ps` 显示它是 `running` 而不是
`healthy`。

**止血。** 把它摘出去换掉。它是无状态的，什么都不会丢。

```bash
docker rm -f <container-id>
./spnrctl up -d --no-recreate --wait --no-deps spinneret
```

它持有的分片锁在 10 秒内过期，其他实例接管。acquire 预算：如果该实例是优雅关闭，它会主动注销，
预算立即重新分配；如果是崩溃或卡死，存活实例最多 60 秒内仍按旧数量分摊——结果是它们比实际需要的
更窄，而不是更宽。

**修。** 如果来得及，在删掉它**之前**先取证：`./spnrctl logs --tail 200 spinneret`、`/readyz` 的响应体（它会
指名失败的那项检查），以及 —— 如果开了 profiling —— 从 `SPINNERET_PPROF_ADDR` 抓一份 goroutine dump。起来了
但一直不健康的副本，几乎总是有个依赖它连不上，或者热状态没有 epoch。

**防。** 既对 `up{job="spinneret"} == 0` 告警，**也**对就绪状态告警，因为卡死的实例是能被抓取的。确认负载均
衡器探的是 `/readyz` 而不是 `/healthz`。

### 10. 发布了一个坏策略或坏配置

**确认。** 损害是从一次发布开始的。控制台**策略**和**配置中心**都有带作者和时间戳的版本历史；**审计日志**里
有这次发布。把版本的时间和 `spinneret_actions_total` 或 `spinneret_acquire_total{result!="ok"}` 斜率变化的
时间对上。

**止血。** 把版本回滚。这两个子系统都带版本和回滚，而且都不需要重启、也不需要重新部署节点：策略回滚通过事件
总线到达整个集群，配置回滚是一个新版本、订阅者会收到 —— 在**空闲**实例上实测从发布到订阅者被唤醒
**p99 46 ms**，而出事故的时候实例并不空闲。

**修。** 然后把**后果**撤掉，回滚本身不会撤：`RevertActions`，范围限定在那个坏版本生效期间的
`policy_id`/`rule` 和 `time_range` 上，先 `dry_run: true`。

**防。** 动作策略先用 `shadow` 模式对比再改成强制执行。发布之前用规则调试器拿真实上报试一遍。把变更窗口留得
窄一些，这样 `time_range` 容易写准；并且把发布权限从不需要它的账号上摘掉。

### 11. 凭据泄露了

令牌、密钥值、身份载荷，或者 KEK 本身。

**API 令牌。** 吊销它（控制台**令牌**，或 `RevokeToken`）。一个 `token.revoked` 事件会立刻把它从每个实例的
校验缓存里删掉；没有事件总线时，在 `SPINNERET_TOKEN_CACHE_TTL`（30 秒）内生效。节点的下一次调用就会失败并
拿到 `token_revoked`，没有宽限期，所以先把替换令牌签出来。然后按那个 token id 去查 `payload_access_minutely`
和审计日志，看它取过什么。

**密钥值。** 先在对端加上新凭据，再存一个新版本，等读取方收敛，最后才在对端撤销旧凭据。单个版本不能删除 ——
如果某个泄露的版本必须变得不可读，就删掉整个密钥再重建，然后把所有引用它的地方重新指向或重新发布。顺序和
收敛时间见[密钥保管库 → 轮换密钥](./10-secrets.md#轮换密钥)。

**身份载荷。** 把该身份禁用或退役并替换掉。`payload_access_minutely` 记录了每分钟哪个令牌取了哪种身份类型的
凭据，请求明细则显示这个身份实际被用来做了什么。

**KEK。** 轮换它：[密钥轮换](#密钥轮换)。重新包装会把每个数据密钥都用新 KEK 重新封装，所以泄露的那把密钥再
也解不开任何当前数据 —— 但它仍然能解开**轮换之前取的每一份备份**。重新包装结束之后把备份重新做一遍，并把旧
dump 视为已泄露。

**所有情况都一样。** 读审计日志 —— 它是"谁明文查看了哪个密钥"的唯一记录，保留期是
`SPINNERET_RETENTION_AUDIT`（默认 365 天）。然后查清它是怎么泄的：令牌进了代码仓库、`.env` 进了镜像、某个本
来不该有这个权限的账号做了明文查看。加固清单见[安全](./19-security.md)。

---

## 呼人还是开单

完整的起步规则文件（带真实 PromQL）在
[可观测性 → 告警规则起步模板](./12-observability.md#告警规则起步模板)。这里回答的是分级问题 —— 谁该被叫起来
—— 以及一条警告。

### 该呼人

| 告警 | 为什么要呼人 |
| --- | --- |
| `up{job="spinneret"} == 0` 持续 2 分钟 | 有一个实例没了 |
| `sum(spinneret_stream_owned_shards)` 低于 `SPINNERET_REPORT_SHARDS` 持续 5 分钟（规则里要把这个数写成字面量） | 上报被接收了但永远不会被应用。无声，而且越来越糟 |
| `sum(spinneret_stream_pending) > 50000` 持续 5 分钟 | 积压是有截止时间的：`SPINNERET_STREAM_MAXLEN` 会静默丢弃超出上限的条目 |
| `max(spinneret_breaker_state) by (site,group) == 2` 持续 5 分钟 | 某个组已经熔断五分钟了 |
| `increase(spinneret_db_write_batches_total{result="dropped"}[15m]) > 0` 或 `increase(spinneret_state_writer_dropped_changes_total[15m]) > 0` | 有写入被丢弃了。这是数据丢失 |
| 有流量的端点组上 `spinneret_identities_available` 为 `0` | 那个组完全无法工作 |
| 三个数据卷中任何一个磁盘超过你的阈值 | 从磁盘满里恢复远比预防痛苦 |

### 开单就行

| 告警 | 为什么不用呼人 |
| --- | --- |
| acquire 失败占比高于 5 %（不含 `overloaded`） | 要的是策略或身份供给上的决定，不是一个通宵 |
| acquire **卸载**占比高于 5 % | 这是容量信号。见下面的警告 |
| acquire p99 高于你的预算持续 10 分钟 | 是变慢，不是不可用 |
| 上报滞后 p99 高于 5 秒持续 10 分钟 | 决策变旧了，不是变错了 |
| 单个组 30 分钟内熔断超过 3 次 | 抖动。需要清醒的头脑去改策略 |
| 风险判定占比高于 10 % 持续 15 分钟 | 是一个要调查的趋势 |
| `unknown` 判定占比高于 20 % | 信号规则没在分类。对正确性紧急，对可用性不紧急 |
| 被放弃的租约超过 `ok` acquire 的 5 % | 节点侧的 bug |
| `spinneret_acquire_peer_beat_age_seconds > 30` | 预算分摊卡住了。只有随之出现卸载时才呼人 |
| `spinneret_notify_deliveries_total{result!="ok"}` | 你的告警链路坏了 —— 这也正是它不能是你**唯一**告警链路的原因 |
| `spinneret_job_runs_total{result="error"}`（按 job） | 除了 `partition_manager`，那是一个缓慢的磁盘泄漏 |

### 一条警告：卸载不是失败

`spinneret_acquire_total{result="overloaded"}` 和 `spinneret_acquire_admission_total{result=~"shed_.*"}` 表示
准入控制在正常工作。把它们并进一条笼统的"acquire 在失败"告警里，你就会派人去给一个容量问题补身份 —— 同时真
正的失败模式 `exhausted` 和 `circuit_open` 被稀释到阈值以下。要**分开**告警，并把排查顺序写进告警自己的
description 里：先看 `spinneret_acquire_script_seconds` 的 p99，因为如果它有尖刺，抬预算只会更糟。

反过来的错误是把 `exhausted` 当成容量问题。它的含义是那个组的身份池真的空了 —— 那是供给或策略问题，不是硬件
问题。

---

## 下一步

- [可观测性与告警](./12-observability.md) —— 本页出现的每个指标，以及完整的告警规则。
- [故障排查](./18-troubleshooting.md) —— 同样这些故障，但从现象出发，另附完整的错误原因对照表。
- [性能与调优](./17-performance.md) —— 本页每个数字背后的实测上限。
- [配置参考](./03-configuration.md) —— 这些流程会改到的每个变量。
- [命令行工具](./15-cli.md) —— 每个 `spnr` 命令和参数，以及服务端的信号。
- [安装与部署](./02-installation.md) —— 这些流程所操作的那套栈。
- [安全](./19-security.md) —— 加固清单，以及审计日志里该复查什么。
