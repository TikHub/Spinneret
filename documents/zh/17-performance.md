# 性能与调优

**这套系统实际能做到什么，全部是实测：acquire、上报和配置的单实例吞吐与延迟，时间花在 Redis 的哪里，
过了拐点之后它如何表现，以及真正有用的那些调优手段 —— 按收益排序。**

[English](../en/17-performance.md)

---

## 目录

- [结论](#结论)
- [逐项目标的判定](#逐项目标的判定)
- [测试环境](#测试环境)
- [测试方法](#测试方法)
- [这些数字不包含什么](#这些数字不包含什么)
- [数据集](#数据集)
- [刚播种或刚重建的站点必须先预热](#刚播种或刚重建的站点必须先预热)
- [Acquire → Report](#acquire--report)
- [只测 Acquire](#只测-acquire)
- [上报摄入](#上报摄入)
- [并发配置长轮询](#并发配置长轮询)
- [配置变更感知](#配置变更感知)
- [时间花在哪里](#时间花在哪里)
- [过了拐点：拥塞崩塌](#过了拐点拥塞崩塌)
- [准入控制](#准入控制)
- [Valkey 设置](#valkey-设置)
- [Valkey io-threads](#valkey-io-threads)
- [Redis 容量估算](#redis-容量估算)
- [调优手段，按收益排序](#调优手段按收益排序)
- [哪些是环境的限制，哪些不是](#哪些是环境的限制哪些不是)
- [复现](#复现)
- [清理](#清理)
- [下一步](#下一步)

---

## 结论

用 `test/load/` 里的 k6 场景、针对 `deploy/compose/` 的编排，对照 v0.1 的性能目标实测。一个服务端实例、
一个 Valkey 实例，没有 Redis Cluster。

| 目标 | 结果 | |
| --- | --- | --- |
| Acquire 服务端 p99 < 5 ms | **4,500 cycles/s 下 1.86 ms**；4,993 Acquire/s 下 4.32 ms | 达成 |
| 单实例 Acquire 吞吐 ≥ 5,000/s | 稳定 **4,993 Acquire/s**、p99 4.32 ms，峰值 7,792/s。完整 acquire→report 闭环：`max_concurrent_leases: 4` 下 **5,500/s**，独占租约下 **4,500/s** | 达成，附条件 |
| 单实例上报摄入 ≥ 20,000/s | 接收 **44,437 reports/s**，零拒绝 | 达成 |
| 上报 → 状态更新 p99 < 200 ms | **19,761 reports/s 下 30.7 ms**，全部应用完成；9,905/s 下 9.5 ms | 达成，且在两倍速率下 |
| 配置变更感知 < 1 s | **p99 46.1 ms** | 达成 |
| 单实例并发长轮询 ≥ 10,000 | **保持住约 10,000 个**，零失败，0.05 核 | 达成 |

一次 acquire→report 闭环消耗 **168.3 µs** 的 Redis CPU，也就是**每个 Redis 线程 5,940 cycles/s**。

四条限定条件，说一次，对下面每个数字都成立：

- **单实例、单 Redis。** 达到这些数字没有用 Redis Cluster，也不需要。
- **完整闭环是五个 Lua 脚本**，不是一个：`acquire`、`ingest`、`lease_retain`、`observe`、`release`。
  "cycles/s" 指的是完整往返，包含 worker 应用上报并结束租约。
- **拐点是一个反馈回路，不是 CPU 上限。** 脚本本身允许每 Redis 线程 5,940 cycles/s；拐点比它低，
  是因为失败的 acquire 会放大负载。见[过了拐点](#过了拐点拥塞崩塌)。
- **这些表格都早于单实例 acquire 准入控制。** 本页每一次运行都是在 acquire 准入门出现之前测的 ——
  这也正是两副本数字那么差的原因。这里公开的数字没有一个是开着准入门测的。
  [准入控制](#准入控制)一节说明这项改动做了什么、你自己怎么测，并且明确区分哪些说法来自实现、
  哪些来自实测。

---

## 逐项目标的判定

单实例 Acquire 目标是达成的，附带下面这些条件：

- **只测 Acquire**（目标点名的那个端点）：单实例 4,993/s，服务端 p99 4.32 ms，Valkey 占 0.75 核。
  允许延迟放开时峰值 7,792/s。
- **完整 acquire→report 闭环**（Acquire + Report + worker 应用上报并结束租约，共五个 Lua 脚本）：
  轮换策略 `max_concurrent_leases: 4` 时单实例 **5,500/s**，acquire p99 4.49 ms、上报滞后 p99 62 ms。
- **独占租约下**（`max_concurrent_leases: 1`，也就是 `spnr seed` 配出来的、配置空间里最贵的那一点）：
  **4,500 cycles/s**，acquire p99 1.86 ms、上报滞后 p99 9.3 ms。这是唯一一个没到 5,000/s 的数字。
- **上报摄入是两个天花板，不是一个。** 接收能到 44,437/s 且零拒绝；而**处理** —— worker 在 200 ms
  目标之内把上报写进热状态 —— 大约 20,000/s。处理是窄的那个，也是做容量规划该用的那个。
- **长轮询和变更感知**都留出了两个数量级的余量，而且一个阻塞中的 watcher 完全不消耗 Redis。

唯一比单实例更差的数字是负载均衡后面的两个副本：合计 **3,000 cycles/s**，比单实例自己还低。
这正是[准入控制](#准入控制)要回答的那个测量结果，而至今没有一个开门后的数字被公开出来替换它。

---

## 测试环境

所有东西 —— 服务端、PostgreSQL、Valkey、ClickHouse、负载均衡**以及压测客户端** —— 都跑在一台笔记本上的
同一个 Docker VM 里。这是本页每个数字上都必须说清楚的限定：压测客户端和被测系统在抢资源。

| | |
| --- | --- |
| 宿主 | macOS（Darwin 25.6.0），Docker Desktop 29.4.0 |
| Docker VM | 16 CPU、7.75 GiB 内存，被所有容器和压测客户端共用 |
| 服务端 | 本仓库的镜像，distroless，`SPINNERET_REPORT_SHARDS=16`，`SPINNERET_DATABASE_MAX_CONNS=32`，载荷缓存保持默认（`SPINNERET_PAYLOAD_CACHE=true`、`SPINNERET_PAYLOAD_CACHE_SIZE=200000`，所以 10 万身份的数据集整个装得下） |
| Redis | `valkey/valkey:8-alpine`（8.1.10），单实例，配置见 [Valkey 设置](#valkey-设置) |
| PostgreSQL | `postgres:17-alpine`，`shared_buffers=512MB`，`max_connections=300` |
| ClickHouse | `clickhouse/clickhouse-server:25.8-alpine`，缓存由 `config/clickhouse-limits.xml` 约束 |
| 负载均衡 | `caddy:2-alpine`，`deploy/compose/config/Caddyfile` |
| 压测客户端 | k6，同一个 VM，Compose profile `loadtest` |

Spinneret v0.1 的目标规格是每个服务实例 4 vCPU / 8 GiB，加一个单 Redis 实例。在这些稳定速率下，一个服务端实例用 16 核里的
1.4–1.6 核，Valkey 用 1.6–1.9 核（其中约一核是执行命令的主线程），所以这个规格并没有让谁挨饿 ——
而且在所有场景里服务端实例都没超过 1.7 核，所以限制这些数字的不是服务端。

这是笔记本级别的硬件。请把这些**形状**当成产品本身 —— 拐点相对于脚本成本的位置、崩塌长什么样、
哪个手段能动哪个数字 —— 而把绝对速率当成一个下限：在独占的服务器上你应该能超过它。

---

## 测试方法

`test/load/run.sh` 把每个场景包起来：

1. 抓取每个副本的 `/metrics`（经由 `lb` 容器，因为副本不发布宿主端口），加上 Valkey 的 `INFO` 和
   `INFO commandstats` → `before.json`；
2. 在 Compose 的 `loadtest` profile 里跑 k6 场景；
3. 运行中再取一份快照，用来看负载下的 gauge → `mid.json`；
4. 结束时再取一份 → `after.json`，并做差 → `delta.json`。

每个量的来源：

| 量 | 来源 |
| --- | --- |
| Acquire 延迟 | **服务端**直方图 `spinneret_acquire_duration_seconds`，在 Prometheus 桶内插值出分位 |
| 上报滞后 | `spinneret_report_lag_seconds` —— 从收到上报到 worker 处理 |
| 吞吐 | k6 自己的计数器在场景窗口上的统计（快照窗口比它长约 3 秒，会低估） |
| 上报接收 / 应用 | `spinneret_report_ingest_total{result="accepted"}` 和 `spinneret_report_lag_seconds` 的 count |
| Valkey CPU | `used_cpu_user + used_cpu_sys` 在快照窗口上的差值 |
| 按命令、按脚本的成本 | `INFO commandstats` 差值，其中包含 Lua 脚本内部发出的命令 |
| 保持住的 watcher 数 | 副本上的 `spinneret_config_watchers` |

有两条方法上的规则值得单独写出来，因为忽略任一条测出来的数字都没有意义：

- **拐点附近的运行一律用 `MID_STATS=0`。** 否则运行中那次快照会调用 `docker stats`，它会遍历机器上
  所有容器。在 Docker Desktop 上这贵到足以扰动它正在测的那次运行：4,000 cycles/s 下它把被测系统卡住了
  约 5 秒（19,000 次迟到迭代），把本来 1.9 ms 的 p99 变成了 11.9 ms。本页每一行吞吐数据都是用
  `MID_STATS=0` 测的；按容器的 CPU 和内存 gauge 是单独采的，在离拐点很远的运行里。
- **运行之间要静置。** 一次以过载结束的运行会留下按完整 TTL 持有的租约，以及被推后到那个 TTL 之后的
  就绪分。在它们排干之前开始下一次运行，测到的是恢复过程，不是这个系统。

---

## 这些数字不包含什么

- **Docker 网络和负载均衡。** 延迟是服务端侧的。k6 自己的延迟把两者都算进去：在 5,500 cycles/s 那次干净
  运行里，k6 的 acquire 中位数比服务端 p50 高约 0.3 ms（0.70 ms 对 0.40 ms）。它适合在同一环境里看
  **变化**，不适合当绝对值。
- **节点做的一切。** 没有访问任何目标站点，没有计算签名，没有解析 HTML。一个"闭环"是围绕一次请求的
  控制平面工作量，不是那次请求本身。
- **直方图最后一个桶之上的任何东西。** `spinneret_acquire_duration_seconds` 到 0.25 s 桶为止，
  `spinneret_report_lag_seconds` 到 10 s 为止，所以表里写 `> 250 ms` 或 `> 10 s` 意思是"超出直方图顶端"，
  实际上就等于那次运行处于过载。

---

## 数据集

一个站点、10 万身份、50 个端点组：

```bash
COMPOSE="docker compose -f deploy/compose/docker-compose.yml"

$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  seed --site loadtest --identities 100000 --groups 50 > /tmp/seed.json
```

这里的 `migrate` 是带着 `spnr` 的那个 **Compose 服务名**，`seed` 才是命令。seed 是幂等的 ——
已存在的对象保留，节点令牌会重建 —— 并在 stdout 打印
`{"token": …, "site": …, "groups": N, "identities": N}`。

它会创建站点 `loadtest`（客户端 `web`）、匹配 `/api/g<i>/` 的端点组 `g0…g49`、带 10 万个合成 cookie
身份的身份类型 `loadtest_cookie`、已发布的轮换策略 `loadtest-rotation`、永不跳闸的宽松熔断策略
`loadtest-breaker`、配置项 `crawler/loadtest.json`，以及一个节点令牌。不给 `--proxies` 就不创建代理。

数据集里真正吃重的部分是这条轮换策略：

| 字段 | 取值 | 效果 |
| --- | --- | --- |
| `strategy` | `weighted_random` | 按健康分加权的随机挑选 |
| `candidate_sample` | `32` | 每次 acquire 采样 32 个候选 |
| `lease_ttl` | `60s` | 产品默认 `120s` 的一半 |
| `max_concurrent_leases` | `1` | **独占租约** |
| `reuse_interval` | `0s` | 同一身份两次使用之间不强制间隔 |

`max_concurrent_leases: 1` 让每个租约都是独占的：一个身份被租走时，它在全部 50 个组里都不可用。
这是配置空间里最贵的一端，也是 seed 的默认值。`test/load/tune.py --max-concurrent-leases 4`
会用不同取值重新发布同一条策略，`--restore` 把播种出来的那条放回去。

---

## 刚播种或刚重建的站点必须先预热

播种 —— 以及 `spnr rebuild` —— 会给**每个端点组里的每个身份写同一个就绪分**。于是 50 个组都会提出同一个
队首候选，而在独占租约下它们会在这个候选上撞车：采样到一个已被租走的身份的组，会把它的分数推到租约过期
时间之后，而这是一个已预热站点不会做的 Redis 工作。

在这个数据集上实测：冷站点在 **2,500 cycles/s** 就崩塌进 `resource_exhausted`，而同一个站点预热之后能扛
**4,500/s**。

流量自己就会把 50 个队列打散 —— 大约一百万次 acquire，或者[复现](#复现)里那道阶梯。
**本页每一个吞吐数字都是在预热过的站点上测的**，那也是真实部署运行的稳态。冷启动行为值得知道
（刚重建的热状态会短暂地更贵一些，这在[重建热状态](./16-operations.md#重建热状态)之后有意义），
但它不是用来做容量规划的数字。

---

## Acquire → Report

`test/load/acquire_report.js`：一次 `Acquire`，接一次带 `release: true` 的 `Report`，固定到达率，
端点组在 50 个里均匀选取。释放是异步的 —— 上报进 Redis 流、由 worker 结束租约 ——
所以一次迭代会跑到五个 Lua 脚本。

**单副本、独占租约（`max_concurrent_leases: 1`）—— 单实例数字：**

| 施加 | 实际 | Acquire p50 | Acquire p99 | 滞后 p50 | 滞后 p99 | 服务端 CPU | Valkey CPU |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,500/s | 1,500/s | 0.12 ms | **0.50 ms** | 2.50 ms | 4.96 ms | 0.52 | 0.42 |
| 2,500/s | 2,500/s | 0.18 ms | **0.94 ms** | 2.51 ms | 4.97 ms | 0.80 | 0.73 |
| 3,500/s | 3,500/s | 0.28 ms | **1.74 ms** | 2.53 ms | 6.72 ms | 1.09 | 1.17 |
| 4,000/s | 4,000/s | 0.31 ms | **1.98 ms** | 2.55 ms | 8.50 ms | 1.23 | 1.37 |
| **4,500/s（2 分钟）** | **4,500/s** | 0.32 ms | **1.86 ms** | 2.56 ms | 9.32 ms | 1.41 | 1.61 |
| 5,000/s | 1,746/s + 534/s exhausted | 189 ms | > 250 ms | > 10 s | > 10 s | 0.51 | **2.02** |

4,500/s 那次两分钟运行是最有代表性的结果：54 万次 acquire、54 万次上报，零错误、零 exhausted，
acquire p99 1.86 ms，上报滞后 p99 9.3 ms。

**单副本、`max_concurrent_leases: 4`：**

| 施加 | 实际 | Acquire p50 | Acquire p99 | 滞后 p50 | 滞后 p99 | 服务端 CPU | Valkey CPU |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 5,000/s（2 分钟） | 4,999/s | 0.36 ms | **4.35 ms** | 2.73 ms | 91.4 ms | 1.60 | 1.77 |
| **5,500/s（2 分钟）** | **5,500/s** | 0.40 ms | **4.49 ms** | 3.00 ms | 62.4 ms | 1.61 | 1.88 |
| 6,000/s（2 分钟） | 5,982/s | 0.68 ms | 18.0 ms | 4.93 ms | > 10 s | 1.58 | 1.94 |

5,500/s 是两个延迟目标**同时**还成立的最高速率。到 6,000/s，吞吐还在 —— 施加 6,000 拿到 5,982 ——
但上报 worker 跟不上了，先失守的是滞后目标。

**负载均衡后面两个副本、准入控制关闭。** 这些运行早于 acquire 准入门，它们也正是准入门存在的理由：

| 施加 | 实际 | Acquire p99（实例 1 / 2） | 滞后 p99 | 每实例服务端 CPU | Valkey CPU | Valkey cmd/s |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,500/s | 1,500/s | 0.55 / 0.58 ms | 4.96 ms | 0.30 | 0.45 | 70,253 |
| 2,500/s | 2,500/s | 1.76 / 0.99 ms | 4.98 ms | 0.46 | 0.83 | 117,501 |
| **3,000/s（2 分钟）** | **3,000/s** | **1.36 / 1.61 ms** | 4.98 ms | 0.59 | 1.17 | 143,854 |
| 3,500/s（45 秒） | 3,500/s | 1.91 / 1.36 ms | 6.1 ms | 0.61 | 1.33 | 160,039 |
| 3,500/s（2 分钟） | 1,288/s + 946/s exhausted | > 250 ms | > 10 s | 0.25 | **3.53** | 438,650 |
| 4,000/s | 1,743/s + 477/s exhausted | > 250 ms | > 10 s | 0.25 | **3.71** | 462,282 |
| 4,500/s | 2,117/s + 342/s exhausted | > 250 ms | > 10 s | 0.33 | **3.59** | 457,091 |

两个副本稳定在 **3,000 cycles/s** —— 比单副本还低 1,500 —— 而 3,500/s 能活过 45 秒但活不过两分钟。
3,000/s 下的延迟比单实例更好，Valkey 主线程也更有余量，但天花板往错的方向动了。同样的施加速率下有三点
不同，而只有第一点是原因：

- **对同一个 Valkey 的在飞并发翻倍。** 每个实例有自己的连接池，而且没有共享的上限，所以同样的到达率
  变成了大约两倍的并发请求。Valkey 排队更深、每次 acquire 更慢、独占租约因此被持有更久、争用上升，
  崩塌回路在更低的施加速率上就开始了。
- **上报分片被切开了。** 每个实例拥有 16 个流分片里的 8 个，所以维持池子的那些租约释放依赖**两个**
  worker 都跟得上；任一个掉队，崩塌就开始。
- **负载均衡不是原因。** 把同样的两副本负载直接打到实例上（`SPINNERET_URL=http://spinneret:8080`，
  用 Docker DNS 轮询）在 3,500/s 以同样的方式崩塌，而 Caddy 只有 0.5% CPU。

---

## 只测 Acquire

`REPORT_MODE=none` 跳过上报，所以只跑 `acquire.lua`（单副本，`max_concurrent_leases: 4`，
这样不断累积的租约不会让身份变成独占）。它隔离出了这个端点，但不是一个真实工作负载：
租约会按完整的 60 秒 TTL 堆积并污染就绪队列。

| 施加 | 实际 | p50 | p99 | 服务端 CPU | Valkey CPU |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 4,000/s | 3,994/s | 0.47 ms | **2.00 ms** | 0.43 | 0.58 |
| **5,000/s** | **4,993/s** | 0.65 ms | **4.32 ms** | 0.54 | 0.75 |
| 6,000/s | 5,779/s | 0.91 ms | > 250 ms | 0.56 | 1.09 |
| 8,000/s | **7,792/s** | 4.42 ms | > 250 ms | 0.72 | 1.49 |

单实例 + 单 Valkey 的 Acquire 峰值吞吐：**7,792/s**，而且 5,000/s 这个目标是在 p99 < 5 ms 的预算
**之内**达到的。

---

## 上报摄入

`test/load/report_ingest.js`：针对一个 400 个长期租约的池子批量上报，单副本。"接收"是服务端收下的量，
"应用"是 worker 真正写进热状态的量。

| 批量 × 速率 | 接收 | 拒绝 | 应用 | 滞后 p50 | 滞后 p99 | 服务端 CPU | Valkey CPU |
| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: |
| 100 × 100/s | **9,905/s** | 0 | 600,100 / 600,100 | 2.73 ms | **9.5 ms** | 0.53 | 0.93 |
| 200 × 100/s | **19,761/s** | 0 | 1,200,200 / 1,200,200 | 6.10 ms | **30.7 ms** | 0.88 | 1.53 |
| 200 × 150/s | **29,640/s** | 0 | 1,311,096 / 1,800,200 | 8.3 s | > 10 s | 0.94 | 1.71 |
| 300 × 150/s | **44,437/s** | 0 | 1,090,595 / 2,700,300 | > 10 s | > 10 s | 0.95 | 1.65 |

接收和处理是两个不同的天花板，而窄的那个是处理：单实例在 200 ms 目标之内大约能应用
**20,000 reports/s**。再往上，流积压开始增长、滞后跟着走 —— 这正是 `spinneret_stream_pending` 和
`spinneret_report_lag_seconds` 的用途。

在节点侧批量上报基本上是免费的吞吐：一批 200 条只花一次 `ingest.lua`，而不是 200 次。它没能让
`observe.lua` 变免费 —— 那是 worker 里每条上报跑一次的 —— 这正是接收能比处理走远那么多的原因。

---

## 并发配置长轮询

`test/load/watch_config.js`，单副本，直连实例。watcher 必须持有**当前**配置版本，否则服务端会立刻回答，
场景就变成了请求洪水；脚本在 `setup()` 里读出已发布的版本。

| watcher 数 | 保持住（`spinneret_config_watchers`） | 失败 | 服务端 CPU | goroutine | 服务端 RSS | Valkey cmd/s |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 10,000 | **9,994** | **0** | **0.05** | 20,110 | 504 MiB | 206 |

目标达成：单实例上约 10,000 个阻塞中的 `WatchConfig` 调用，零失败，0.05 核，而且**一个阻塞中的
watcher 完全不消耗 Redis**。（9,994 而不是 10,000 是采样瞬间的问题，不是失败 ——
有几个轮询者正处在两次轮询之间；这次运行的轮询错误数是零。）

10,000 是实测到的数字，不是上限：`SPINNERET_MAX_WATCHERS`（每实例默认 **20,000**）才是阻塞中
`WatchConfig` 调用的硬上限，超出之后到达的 watcher 会以 `resource_exhausted` / 原因 `rate_limited`
被拒绝，而不是排队。

**这里的风险点在负载均衡，而不是服务端。** 10,000 个连接在同一毫秒到达时超出了 Caddy 的拨号超时，
失败的拨号把唯一的上游摘掉了 —— 在服务端只有 0.8% CPU 的情况下产生了一次彻底不可用。
自带 `Caddyfile` 里有三项设置正是因为这次运行而存在：

| 设置 | 取值 | 为什么 |
| --- | --- | --- |
| `dial_timeout` | `5s` | 高于连接风暴时最坏的 accept 延迟。一次超时的拨号会被算作失败，把一个只是忙的副本摘掉 |
| `fail_duration` | `2s` | 副本仍然是第一次拨号失败就被摘除（`max_fails 1`，`lb_policy least_conn` 需要它，否则它会一直挑那个已经死掉的副本），但摘除窗口很短。用 `fail_duration 5s` 时一次压测测出了 76,857 次"没有可用上游"的 503 |
| `lb_retries` / `lb_try_duration` / `lb_try_interval` | `20` / `10s` / `250ms` | 重试循环比摘除窗口更长，所以一次被动摘除的代价是延迟而不是错误 |

放到你自己的负载均衡上：把拨号超时调到高于你最坏连接风暴的 accept 延迟，让摘除窗口短于重试预算，
并且让节点重启错峰，免得 10,000 个节点在同一毫秒重连。

---

## 配置变更感知

`test/load/config_awareness.py` 反复发布 `crawler/loadtest.json` 的新版本，并测量它自己那些 watcher 的
唤醒延迟。发布者和 watcher 在同一个进程里，所以两个时间戳来自同一个时钟。`total` 从发起发布 RPC 之前
开始算 —— 保守值；`from_commit` 从发布返回的那一刻开始算。

| 情形 | 唤醒的 watcher | total p99（最差一轮） | total 最大 | from_commit p99 |
| --- | ---: | ---: | ---: | ---: |
| 空闲实例、200 个探针 watcher、6 次发布 | **1,200 / 1,200** | **46.1 ms** | **46.8 ms** | **38.4 ms** |

零轮询错误，每个 watcher 都被唤醒。比 1 秒目标小两个数量级。这里测的那套协议在
[配置中心 → 订阅协议](./09-config-center.md)。

---

## 时间花在哪里

每一次 acquire→report 闭环是五个 Lua 脚本。在约 1,000 cycles/s 的负载下采样 0.3 秒内的所有 Valkey
命令 —— `SLOWLOG` 配 `slowlog-log-slower-than 0`，再按脚本 SHA 给 `EVALSHA` 条目分组 ——
就能把成本归因。"之前"一列是同一台机器上、热路径优化之前的同一个场景：

| 脚本 | 每闭环调用次数 | 之前（µs） | 现在（µs） | | |
| --- | ---: | ---: | ---: | ---: | --- |
| `acquire.lua` | 1 | 104.2 | **68.3** | −34 % | 挑选、过滤、写租约 |
| `observe.lua` | 每条上报 1 次 | 71.7 | **43.5** | −39 % | worker 更新状态 |
| `release.lua` | 每个被释放的租约 1 次 | 67.0 | **34.0** | −49 % | 结束租约 |
| `ingest.lua` | 每批 1 次 | 16.3 | **12.2** | −25 % | 追加到流分片 |
| `lease_retain.lua` | 每请求每站点 1 次 | 12.0 | **10.3** | −14 % | 让被上报的租约在 worker 处理前保持可读 |
| **每闭环合计** | | **271.2** | **168.3** | **−38 %** | |
| **每 Redis 线程 cycles/s** | | **3,690** | **5,940** | **+61 %** | |

同一窗口里还采到两个闭环之外的脚本：`apply.lua` 49.8 µs（一次自动冷却动作）和 `breaker_eval.lua`
47.2 µs（按组的巡检，有频率限制 —— 不是每条上报都跑）。

每 Redis 线程 5,940 cycles/s 是**脚本本身**给出的上限。本页每一个拐点都比它低，
因为拥塞回路在主线程被填满之前就开始了。

### 造出这些数字的两轮优化

两轮都是用 `test/perf/` 里的微基准工具证明的 —— 它直接对着 Valkey 测脚本，用 `INFO commandstats`
差值、对每个 Lua 原语做差分校准、以及 `SLOWLOG` 分位。它的 A/B 块在同一个进程里交替跑已发布脚本和冻结
的旧脚本，因为同一份代码在同一份数据上的运行间漂移是 ±8%，顺序跑"先测之前、再测之后"判定不了比这更小的
差异。

**调度器路径** —— `acquire.lua`、`release.lua`、`renew.lua`、`reap.lua` 和共享前导。
服务端每次调用 µs，10 万 × 50 数据集上三轮交替各 3,000 次调用取最好的一轮：

| 块 | 之前 | 之后 |
| --- | ---: | ---: |
| `acquire.lua`，`candidate_sample: 32`，干净池 | 85.1 | **59.3**（−30.3 %） |
| `acquire.lua`，`candidate_sample: 32`，50% 候选被过滤 | 102.7 | **72.2**（−29.7 %） |
| `acquire.lua`，`candidate_sample: 1`，干净池 | 67.6 | **55.6**（−17.8 %） |
| `release.lua`，没有独占推后标记 | 53.6 | **29.2**（−45.6 %） |
| `release.lua`，标记被另一个组设置过 | 96.3 | **60.1**（−37.6 %） |
| `reap.lua`，批量 100 时每个过期租约 | 20.7 | **14.1**（−31.8 %） |

一次 acquire 加一次 release —— 热路径每个请求都要跑的那一对 —— 之前 138.7 µs，之后 **88.4 µs**：−36%。
另外注意 `candidate_sample` 有多便宜：acquire 现在是按需读取候选状态，而不是把整个采样集批量载入，
所以 K=1 和 K=32 的差距约 4 µs。

**worker 路径** —— `observe.lua`、`lease_retain.lua`、`apply.lua`、`breaker_eval.lua`：

| 块 | 之前 | 之后 |
| --- | ---: | ---: |
| `observe.lua`，success，无计数器 | 46.9 | **31.0**（−33.9 %） |
| `observe.lua`，`rate_limited`，2 个计数器 + 2 个封禁窗口 | 61.8 | **41.6**（−32.6 %） |
| `lease_retain.lua`，同一站点 100 个租约时每个租约 | 9.2 | **2.0**（−78.2 %） |
| `apply.lua`，自动的身份 × 端点冷却 | 47.3 | **34.4**（−27.2 %） |
| `breaker_eval.lua`，`eval` 模式，12 桶窗口 | 54.3 | **44.1**（−18.8 %） |

一批 100 条上报里的一条，Valkey CPU 从 58.7 µs 降到 **35.6 µs**：−39%。

还有一个更早的修复，在"之前"这一列之前，它解释了这两者的形状。`release.lua` 过去在每次租约结束时都要
恢复该身份在**它客户端下每一个端点组**里的就绪分，因为在租约被持有期间，别的组可能已经把分数推到了
独占租约的过期时间之后。在这个 50 组数据集上，那就是每次 acquire 恰好 **50 次 `ZSCORE`**，
不管有没有哪个组真的推过；它让 `release.lua` 成了系统里最贵的脚本，每多一个端点组多 2.25 µs。
修复的做法是把这件事记在身份上而不是每次去重新发现：因为别的组的独占租约而推后某个身份的那次 acquire
会在身份上打标记，租约结束时只走标记点出的那些组。这把 1,000 cycles/s 下的 Valkey 命令量从 95,793 降到
49,491，把两实例的稳定速率从 1,678/s 提到 3,000/s。

`test/perf/` 的绝对值比 Compose VM 便宜约 1.4 倍，因为它跑在开发机上的 Valkey 上；上面那张端到端表格
是在 VM 里的确认。两组数字都存在是有意的：一个证明改动有效，另一个证明它在集成之后还有效。

---

## 过了拐点：拥塞崩塌

过了拐点，这个系统不是优雅退化，而是崩塌。认出它的特征比记住任何单个数字都重要。

**特征。** Acquire p99 跳一个数量级，`spinneret_report_lag_seconds` 从几十毫秒变成几秒，
`spinneret_acquire_total{result="exhausted"}` 陡升，而 Redis CPU 很高的同时**有效**吞吐在下降。

**回路**，一步一步：

1. Redis 饱和，于是上报 worker 落后；
2. 租约因此没被释放，身份一直处于被租走状态；
3. 另外 49 个组里的 acquire 采样到这些被租走的身份、拒绝它们、把它们的就绪分往后推 ——
   每次一个 `HGET` 加一个 `ZADD`；
4. 一次失败的 acquire 会走过远比成功那次更多的候选：在两副本 4,000/s 的崩塌上实测，
   **每次 acquire 220 条 Redis 命令而不是 49 条**，`EVALSHA` 平均 **157 µs 而不是 27 µs**；
5. 这又把 Redis 推得更饱和。

**它的代价。** 崩塌时 Valkey 在各 I/O 线程上烧掉 3.5–3.7 核，去处理健康状态下同样施加负载所需命令量的
约四倍 —— 而交付出来的有效吞吐只有三分之一。一次失败 acquire 的单次命令成本比过去便宜了
（97 µs，优化前是 212 µs），但反馈本身完好无损，而它才是本页每一个拐点的成因，不是 Valkey 的 CPU 上限。

**拐点略下方是亚稳的。** 在略低于拐点的那一段，系统是稳的，直到有什么东西把它卡住一次 ——
一次 AOF 重写 fork、一次 `docker stats` 遍历、一个吵闹的邻居 —— 然后它就再也不会自己恢复。
回路一旦开始，只有把施加负载降到拐点以下它才会解开。这也是 [Valkey 设置](#valkey-设置)里那些 AOF
配置为什么那么重要。

**运维上：** 把施加负载控制在实测拐点的 **80%** 以内，对
`spinneret_acquire_total{result="exhausted"}` 和 `spinneret_report_lag_seconds` 设告警，
并且让准入控制保持开启。同一个特征从症状那一端看、以及完整的指标清单，在
[故障排查 → 延迟升高或吞吐崩塌](./18-troubleshooting.md#延迟升高或吞吐崩塌)。

---

## 准入控制

准入控制就是把那个回路变成普通背压的机制。每个实例限制自己在 Redis 上的 `acquire.lua` 在飞数量，
多出来的**在服务端**就丢掉，一条 Redis 命令都不发。它的全部意义在于：超过拐点的施加负载会以一个可重试的
错误被便宜而迅速地丢掉，而不是把并发成倍加到 Redis 上。

### 测了什么，没测什么

下面是开门后的实测数字。测试环境和数据集同前（单站点、10 万身份、50 个端点组、已预热），
`SPINNERET_ACQUIRE_FLEET_INFLIGHT=64`，每个臂都是一次两分钟的运行，数字取自服务端直方图。

| 副本 | 施加负载 | 门 | 实际 cycles/s | acquire p99 | 上报滞后 p99 | 卸载 | Valkey |
| ---: | ---: | --- | ---: | ---: | ---: | ---: | --- |
| 1 | 4,500/s | 开（上限 64） | **4,499/s** | 1.97 ms | 10.9 ms | 0.4/s | 1.65 核、215k cmd/s |
| 2 | 4,000/s | 关 | **4,000/s** | 1.66 ms | 8.4 ms | — | 1.71 核、192k cmd/s |
| 2 | 4,000/s | 开（每实例 32） | **3,995/s** | 2.99 ms | 15.8 ms | 4.3/s | 1.79 核、193k cmd/s |
| 2 | 4,500/s | 开（每实例 32） | **2,418/s** | 89 ms | 超出最高桶 | 1,881/s | 2.37 核、335k cmd/s |

这三张表说的是三件事：

1. **单副本上，门不要任何代价。** 在单实例的标称速率下，它放行了施加的 4,500 cycles/s 中的 4,499，
   只卸载 0.4/s，acquire p99 为 1.97 ms。此时上限是 64 —— 整个 fleet 预算，因为它就是整个 fleet。
2. **在容量点上，双副本开门和不开门表现一致。** 两者都服务了约 4,000 cycles/s。门的代价体现在尾部：
   acquire p99 2.99 ms 对 1.66 ms，上报滞后 p99 15.8 ms 对 8.4 ms。这是在一条健康服务时间不到一毫秒的
   路径上做许可记账的成本，也是这笔账诚实的支出一侧。
3. **超过容量后，门把过载变成负载卸载。** 在 4,500/s（超过双副本在单 Valkey 上能稳住的量）时，它服务了
   2,418 cycles/s，以 `unavailable`/`overloaded` 卸载 1,881/s，并把 acquire p99 保持在有限的 89 ms。
   吞吐下降了，但没有崩塌，而且没有任何客户端看到超时。

**双副本的天花板变了，而且不是因为门。** 本文档早先的几轮记录的是双副本稳住 3,000 cycles/s、
3,500 就崩溃。这个结果复现不出来：从重建并预热过的池子重测，双副本开门和不开门都能服务 4,000 cycles/s。
最可能的原因是 Lua 热路径的优化把拐点推上去了；无论原因是什么，早先那个「双副本还不如单副本」的结论
至少部分是测量假象——而把门宣传成它的解药，本来是很容易做到的事。

**没有测到：双副本、超过容量、关门。** 每一次尝试都被判为无效，而这些无效的原因值得写下来，
因为它们就是这套压测工具的陷阱：

- **崩过的一轮会污染下一轮。** 以过载收尾的运行会留下按完整 TTL 持有的租约，以及被推到租约到期的
  就绪队列分数。睡一百秒远远不够；这样起跑的运行，`exhausted` 计数会比正常高一个数量级，这就是破绽。
  上表每个臂之前都先跑了 `spnr rebuild` 并重新预热。
- **Valkey 重启看起来和拥塞崩溃一模一样。** 有一组测试中 Valkey 在运行途中重启了五次。每次重启它都要
  重新加载 3.2 GB、980 万个 key 的数据集，耗时 **153 秒**，期间不接受任何连接——于是那轮记录到的是
  acquire p50 高达数百毫秒、来自 2 秒 Redis 超时的 `deadline_exceeded`、吞吐接近零。识别方法是
  `delta.json` 里 Valkey 的命令数或 CPU 增量为**负值**，因为计数器被重置了。在相信一次崩溃之前先查这个。
- **A/B 的那一侧会自己翻回去。** `compose run k6` 会解析整个 compose 文件来满足 k6 的 `depends_on`
  链，而运行中容器的环境与解析结果不一致的服务会被重建——所以只把参数传给 `up` 那一次调用，会在运行
  中途悄悄恢复默认值。`run.sh` 现在用 `export`，而 `off` 一侧那条 `acquire_shed: ['count<1']` 阈值
  正是为了抓这种情况而存在的。

所以本页为这个门给出的结论就是上面三条，不多一条。「它能阻止不开门时的崩溃」从机制和
`internal/pkg/admit/` 的实现来看是合理的预期，但那不是这里测出来的东西，下面的步骤是去测它的方法。

### 两个变量

| 变量 | 默认 | 含义 |
| --- | --- | --- |
| `SPINNERET_ACQUIRE_FLEET_INFLIGHT` | `64` | **整个 fleet** 允许在 Redis 上在飞的 acquire 脚本数，0–65536。每个实例放行的是它除以自己看到的存活 API 实例数，并限制在 `[4, 4096]`。`0` 完全不构造门，恢复无上限的行为 |
| `SPINNERET_ACQUIRE_MAX_INFLIGHT` | `0`（推导） | 直接钉住本实例的上限而不去分摊 fleet 预算，0–4096。设成正数还会停掉统计 peers 的心跳 |

两者都在[配置参考 → Acquire 准入控制](./03-configuration.md#acquire-准入控制)。

### 一次尝试是怎么被判定的

- 许可是在**任何 CPU 工作之前**取的，所以一次丢弃不产生任何分配，也不发任何 Redis 命令。
- 许可按 `count` 加权，所以一次 `AcquireBatch` 大致按它真正的 Lua 工作量计费，而不是算作一次调用。
- 没有空闲许可时，带等待预算的调用方会进一个 FIFO 等待室，深度是**上限的 4 倍**、下限 32。
  新到达者永远不会插到已在等待的调用方前面。
- 一个排队中的调用方最多等 **50 ms**，并且绝不超过它自己剩余的 `wait_ms`。因为所有 SDK 默认
  `wait_ms = 0`，常见情形是根本不排队：有空闲许可就放行，没有就丢弃，只花一次加锁往返。
- 被丢掉的调用返回原因 `overloaded`（code `unavailable`，HTTP 503），带一个均匀抖动到
  **100–200 ms** 的重试提示。抖动很重要：两个 SDK 都会自动重试 `unavailable`，
  不抖动的提示会让被丢掉的那一批整齐地同时回来。
- 一次被丢掉的尝试会消耗 acquire 等待阶梯的一级，并且在门**外面**睡，而它等许可花掉的时间会抵扣到
  那一级上 —— 所以同样的 `wait_ms` 开着门和不开门买到的尝试次数是一样的。
- 只有在**没有任何**一次尝试到达 Redis 时，丢弃才会表现为 `overloaded`。一旦脚本已经回答过
  `EXHAUSTED` 或 `NO_PROXY`，那个原因会保留下来：开着门时，`exhausted` 只意味着池子空了，
  绝不意味着过载。这个区分是主要的诊断收益。

### 预算是怎么分摊的

每个 API 实例每 **2 秒**把自己的时间戳记进 Redis 里的一个有序集合，剔除心跳早于 **60 秒**存活窗口的成员，
再读回还剩多少个。这里没有反馈信号，也没有会振荡的东西。然后每个实例把自己的上限设为
`SPINNERET_ACQUIRE_FLEET_INFLIGHT / 存活数`，并限制在 `[4, 4096]` —— 整数截断是有意的，
这样 fleet 总量永远不会超过配置的预算。

**存活窗口是 30 个心跳周期，这是刻意的。** 心跳需要 Redis，而准入控制真正起作用的时刻恰恰是 Redis
饱和的时刻——那时心跳本身就最容易超时。这一点是实测出来的：双副本过载、窗口设为 10 秒时，一个实例
漏掉的心跳足以被另一个实例剪除，后者随即用 1 去除全局预算，把整个预算都放给了自己。闸门在最该守住
的时候放宽了，这正是它本要阻止的那个崩溃的正反馈回路。优雅关闭会让实例主动注销自己，所以长窗口只是
延后发现**崩溃**；而崩溃实例残留的成员身份只会让存活实例更窄——安全的方向。
`spinneret_acquire_peer_beat_age_seconds` 就是用来看注册表是否已经不再收敛的。

注册表的其余行为仍是尽力而为的。Redis 故障时，本实例保留上次观察到的数量，所以它自己的上限只会保持
或变窄；故障只记日志、不会返回到请求路径上：注册表的问题不能有能力把一个实例弄挂。实例关闭时会把
自己摘掉，于是存活的实例在下一次心跳就扩大自己的份额，而不用等满存活窗口。上限变化只在变化时记一行日志。

有三个后果值得提前规划：

- **每个 API 实例都需要各自唯一的 `SPINNERET_INSTANCE_ID`**，否则它们会坍缩成一个注册表成员，
  每个都放行**整份**预算。默认值是由主机名加一个随机后缀拼出来的（`<hostname>-<3 个随机字节>`），
  所以在 Compose 下天然互不相同；另外，peer 注册表如果真的收到一个空 id，会替换成随机 id 并打一条警告。
- **要么每个实例都钉，要么都不钉。** 被钉住的实例不注册，于是推导的那些实例除以了一个偏小的数，
  **而且**被钉的配额还要加在上面。
- **单实例下限 4 最终会赢。** 在默认预算下超过 16 个副本之后，除法会被截断到下限，fleet 总量又会按
  4 × 副本数增长。拿 `sum(spinneret_acquire_inflight_limit)` 跟配置的预算比：16 个副本时这个和正好等于
  预算 —— 64/16 = 4，所以 16 × 4 = 64 —— 17 个副本时是 17 × 4 = 68。一旦
  `sum(spinneret_acquire_inflight_limit)` 超过 `SPINNERET_ACQUIRE_FLEET_INFLIGHT`，
  就说明下限已经盖过了预算，该做的是给 Redis 分片或者降低 fleet 预算。

因为预算是在存活实例之间分摊的，所以增加副本带来的是**服务端**容量 —— 长轮询容量、上报处理、可用性
—— 而不会增加对 Redis 的并发。能增加 acquire 吞吐的是 Redis 容量。

### 自己去测

`test/load/run.sh` 有一个 A/B 开关，在运行前切换准入门，所以这一对是同一个镜像、只差一个服务端变量的
两次运行 —— 不重新构建，也没有机器漂移：

```bash
export LOADTEST_TOKEN=$(python3 -c "import json;print(json.load(open('/tmp/seed.json'))['token'])")

# 基线一侧：完全不构造门。
ADMISSION=off test/load/run.sh acq-5000-off acquire_report.js ACQUIRE_RATE=5000 DURATION=2m
# 开门一侧：fleet 预算，按存活实例数分摊。
ADMISSION=on  test/load/run.sh acq-5000-on  acquire_report.js ACQUIRE_RATE=5000 DURATION=2m
```

`ADMISSION=on` 会导出 `SPINNERET_ACQUIRE_FLEET_INFLIGHT`（取自 `ACQUIRE_FLEET_INFLIGHT`，默认 `64`），
`off` 导出 `0`；两侧都用 `--no-build` 重建 `spinneret` 服务，并等 `ADMISSION_SETTLE` 秒
（默认 15）让负载均衡重新解析副本。这个开关会改一个阈值：`ADMISSION=off` 时出现丢弃会让这次运行失败，
这能抓住"其实没切过去"的情况。开着门时，除非设了 `SHED_MAX`，丢弃永远不会让运行失败 ——
`SHED_MAX=0` 就是用来断言"这个速率必须完全不丢弃地服务完"的：

```bash
ADMISSION=on SHED_MAX=0 test/load/run.sh acq-5000-gated acquire_report.js \
  ACQUIRE_RATE=5000 DURATION=2m REPORT_MODE=none
```

场景会读 `Spinneret-Reason` 头，所以一次丢弃（`acquire_shed`）和真正的池子空了
（`acquire_exhausted`）、熔断打开、以及真正的失败是分开计数的。服务端侧，`run.sh` 会快照
`spinneret_acquire_admission_total{result}`、`spinneret_acquire_admission_wait_seconds`、
`spinneret_acquire_script_seconds`，以及门的 gauge `spinneret_acquire_inflight`、
`spinneret_acquire_queued`、`spinneret_acquire_inflight_limit` 和 `spinneret_acquire_peers`。

怎么读它们：**`queued` 在涨而丢弃还是零，是拐点临近的最早警报** —— 但这只对传了 `wait_ms > 0`
的客户端成立，因为没有等待预算的调用方根本不会排队。`overloaded` 在涨、而 Redis CPU **还没到顶**
且 `spinneret_acquire_script_seconds` p99 正常，说明上限比 Redis 能承受的更窄：提高 fleet 预算，
或者按实测值钉死。`overloaded` 在涨、Redis CPU 很低但脚本 p99 **飙了**，说明 Redis 是卡住了而不是窄了
—— 去修 Redis，不要提高预算，那会把崩塌回路重新打开。完整的判断表在
[故障排查](./18-troubleshooting.md#延迟升高或吞吐崩塌)。

---

## Valkey 设置

`deploy/compose/docker-compose.yml` 里有五项设置。前三项各有下面的实测依据；后两项是安全余量：

| 设置 | Valkey 默认 | 这里 | 为什么 |
| --- | --- | --- | --- |
| `io-threads` | 1 | **4**（`VALKEY_IO_THREADS`） | 吞吐提升很小，尾延迟改善很大 —— 见[下文](#valkey-io-threads) |
| `save`（RDB） | `3600 1 300 100 60 10000` | **关闭**（`--save ""`） | 持久化靠 AOF。RDB 落点会为同一份数据再 fork 一次，负载下每分钟一次 |
| `auto-aof-rewrite-percentage` / `-min-size` | 100 / 64 MiB | **300 / 1 GiB** | 用默认值时，4,000 cycles/s 下每约 52 秒就触发一次重写 |
| `maxmemory-policy` | `noeviction` | **`noeviction`** | 显式写出来。淘汰掉一个租约哈希或一个就绪队列会静默破坏热状态；这里的 Redis 是工作集，不是缓存 |
| `maxclients` | 10,000 | **20,000** | 给若干服务端实例的连接池加一个 CLI 会话留余量。节点的长轮询不占 Redis 连接 —— 一个阻塞中的 watcher 一条都不占 —— 所以这是余量，不是实测出来的需求 |

`appendonly yes` 和 `appendfsync everysec` 没有改：这里不拿持久性做交换。

**AOF 重写是最大的一项稳定性发现。** Spinneret 的热状态大多是短命键 —— 租约哈希、去重标记、流条目 ——
所以 AOF 增长远快于它所描述的数据集，重写触发条件被不断满足：**4,000 cycles/s 下 20 分钟内 23 次重写**，
每约 52 秒一次，每次 fork 一个约 1 GiB 的进程并写出约 290 MiB，上面还压着一次后台 RDB 保存。
每次重写在笔记本 VM 里都是一次多秒级的 I/O 停顿，而拐点下方那一段是亚稳的：一次停顿就足以启动拥塞回路，
而它不会自己恢复。当时的症状是：一小时前还干净的速率，现在从第一秒就崩。改成 `--save ""`、
`--auto-aof-rewrite-percentage 300` 和 `--auto-aof-rewrite-min-size 1gb` 之后，
之前在 2,500 cycles/s 就崩塌的同一道阶梯干净地跑到了 4,000/s，4,500/s 的两分钟运行也通过了。
那两次运行之间没有改任何别的东西。

**在共用主机上 ClickHouse 必须限制。** 开箱状态下它按整台机器给自己定规格 ——
`max_server_memory_usage` 是内存的 90%，单是 mark cache 就允许 5 GiB。在这个 VM 里，这让它涨过 1.1 GiB
并在一次 5,000/s 的运行中被内核 OOM killer 挑中，把整个 VM 冻住了。
`deploy/compose/config/clickhouse-limits.xml` 设了：

| 设置 | 取值 | 为什么 |
| --- | --- | --- |
| `max_server_memory_usage` | 2.5 GiB | 这是安全网，不是减肥。它是跟进程 RSS 比的，而这个镜像不管缓存怎么设都会在约 1.2 GiB 空转 —— 设得**低于**它，ClickHouse 不会变小，只会让每次 `INSERT` 都以 `MEMORY_LIMIT_EXCEEDED` 失败、上报 worker 卡住。1.5 GiB 太紧了：约 1,600 万行的请求明细查询会以 241 号错误失败 |
| `mark_cache_size` | 64 MiB | 默认 5 GiB。热路径是写入；mark 重读很便宜 |
| `uncompressed_cache_size`、`mmap_cache_size`、`compiled_expression_cache_size` | 0 | 它们只对这个部署不会跑的"重复大扫描"有收益 |
| `jemalloc_enable_background_threads` | true | 把释放出来的 arena 还给操作系统，而不是留着复用 |

`background_pool_size` 是故意不动的：把它降到 4 会让 ClickHouse 拒绝启动，因为 MergeTree 的合法性检查
要求 `background_pool_size × background_merges_mutations_concurrency_ratio` 超过
`number_of_free_entries_in_pool_to_execute_mutation`。内存真正在的地方是那些缓存。

---

## Valkey io-threads

Valkey 在主线程上执行每一条命令（包括 Lua），所以额外的 I/O 线程只把 socket 读写挪走。端到端实测下，
它们买到的吞吐很少，买到的尾延迟很多：

| 单实例、`max_concurrent_leases: 4`、60 秒 | io-threads=1 | io-threads=4 |
| --- | ---: | ---: |
| 施加 6,000/s → 实际 | 5,955/s | **5,999/s** |
| 6,000/s → acquire p99 | 18.9 ms | **4.09 ms** |
| 6,000/s → 上报滞后 p99 | 4,656 ms | **41 ms** |
| 6,000/s → Valkey CPU | 0.91 核 | 1.92 核 |
| 施加 7,000/s → 实际 | 6,765/s | **6,976/s** |
| 7,000/s → acquire p50 / p99 | 40.0 ms / 237 ms | **0.87 ms / 9.88 ms** |
| 7,000/s → Valkey CPU | 0.95 核 | 1.91 核 |

这笔交易是：多花大约一核 Valkey CPU，换来在曲线上再往上 1,000–2,000 cycles/s 的范围内尾延迟仍然守在
目标之内。在 Redis 有核可用的主机上值得换；`VALKEY_IO_THREADS=1` 可以恢复单线程行为。
它对**稳定**速率提升不大：在拐点处主线程仍然是那面墙，这也是为什么 4,500/s 和 5,500/s 这两个数字
只比 `io-threads 1` 高一点。

一开始有个闭环微基准得出的结论是 I/O 线程不值得。它测的是吞吐 —— 而吞吐几乎不变。
是端到端那次运行才看出它们真正买到了什么。

---

## Redis 容量估算

在真实数据集上用 `MEMORY USAGE` 测得（Valkey 8，10 万身份，50 个端点组）：

| 结构 | 实测 | 单位成本 |
| --- | ---: | --- |
| 就绪队列，10 万成员 | 6,457,568 B | **每（身份 × 端点组）64.6 B** → 每 100 万条约 62 MiB |
| 健康状态 | 1,177 条共 84,072 B | 每个已预热的（身份 × 端点组）约 71 B |
| 身份 | 采样 400 个，平均 201 B、最大 296 B | 每个身份 |
| 上报去重标记 | 80 B | 每条上报，保留 `SPINNERET_REPORT_DEDUP_TTL` |
| 已结束的租约 | 约 290 B | 每个租约，结束后再保留 `max(迟到上报窗口, 去重 TTL)` |
| 上报流条目 | 约 440 B | 每条 |

这个规模的数据集刚重建完，总共测得 **351 MiB**。生产环境里占主导的不是数据集，而是与流量成正比的那部分：

```text
工作集 ≈ 350 MiB                              （数据集）
       + 最多 345 MiB                          （健康条目，等到每个身份在每个组都预热之后）
       + reports/s  × 去重 TTL × 80 B          （去重标记）
       + acquires/s × 去重 TTL × 290 B         （已结束的租约哈希）
       + 积压条目数 × 440 B                     （上报流）
```

在 4,500 cycles/s、默认 1 小时去重 TTL 下，这就是 1.2 GiB 去重标记加 4.4 GiB 已结束租约哈希 ——
远超数据集本身。压测运行直接体现了这一点：重建后 351 MiB，4,500 cycles/s 跑两分钟后约 950 MiB，
这个差值的每一个字节都在等它那一个小时过去。

**按节点的重试行为来设 `SPINNERET_REPORT_DEDUP_TTL`，是单项收益最大的内存手段。**
举个算例：2,000 cycles/s、去重 TTL 15 分钟、10 万 × 50 的数据集：

```text
350 MiB  数据集
345 MiB  完全预热后的健康条目
137 MiB  去重标记        （2,000 × 900 s × 80 B）
498 MiB  已结束租约哈希  （2,000 × 900 s × 290 B）
  ~0     流，只要 worker 跟得上
------
≈ 1.3 GiB，所以按 3 GiB 准备，2 GiB 告警
```

请按算出来的值的至少两倍准备：一次 worker 故障会把流那一项从几乎没有变成 `积压条目数 × 440 B`，
而 `noeviction` 意味着 Redis 会拒绝写入，而不是悄悄丢掉状态。

---

## 调优手段，按收益排序

1. **`max_concurrent_leases`** —— 站点级最大的手段。`1`（独占）单实例稳定 4,500 cycles/s；
   `4` 稳定 5,500 **而且**退化优雅得多，因为被租走的身份不再需要被推出同一客户端下其他每一个组的
   就绪队列 —— 而那正是崩塌的放大器。**代价：**同一身份上最多四个并发请求，有些站点不能接受。
   **怎么验证：**固定施加速率下的实际速率，以及 acquire p99 还能在多高的速率上守在 5 ms 内。
   它住在[轮换策略](./08-policies.md)里。
2. **`SPINNERET_ACQUIRE_FLEET_INFLIGHT`** —— fleet 在 Redis 上的 acquire 并发。**买到：**超过拐点的
   施加负载会在服务端以一个可重试的 `overloaded` 被丢掉，而不是把 Redis 压崩，而且 `exhausted`
   重新只表示池子空了。**代价：**预算设得太低会把 Redis 本来能做的活丢掉。
   **怎么验证：**`spinneret_acquire_admission_total{result=~"shed.*"}` 对照 Redis CPU 和
   `spinneret_acquire_script_seconds` p99 —— Redis CPU 低、脚本 p99 正常却在丢弃，说明预算太小。
   从默认 64 开始，在 Redis 还有余量时逐级提高；测出一个值之后，在**每个** API 实例上钉
   `SPINNERET_ACQUIRE_MAX_INFLIGHT`。
3. **`SPINNERET_REPORT_DEDUP_TTL`** —— 默认 1 小时，每条上报 80 B，而且让每个已结束租约哈希
   （约 290 B）也活满这一小时。节点的重试是秒级而不是小时级。**买到：**设成 5–15 分钟能让 Redis 内存
   少 4 到 12 倍。**代价：**一个节点若在 TTL 之后才重试上报，那条上报会被应用两次。
   **怎么验证：**前后对比 `INFO memory`，并确认
   `spinneret_report_ingest_total{result="duplicated"}` 仍然非零 —— 正是这个计数器证明窗口还接得住
   你的节点的重试。
4. **要 acquire 吞吐就加 Redis 容量，而不是加副本。** 每个键都按站点或上报分片做了 hash tag，
   `SPINNERET_REDIS_ADDRS` 会让客户端进入集群模式，所以 Redis Cluster 能把每线程 5,940 cycles/s
   乘到多个主节点上。这是抬高**天花板**的那个手段。粗略估算：独占租约下每 **4,500** acquire→report
   cycles/s 一个 Redis **主节点**，`max_concurrent_leases: 4` 下 **5,500/s** —— 脚本成本允许 5,940，
   差出来的那部分是你该留着的拥塞余量。**代价：**要运维一个集群。
   **怎么验证：**每个主节点的 Valkey CPU，以及拐点有没有移动。
5. **用自带的 Valkey 设置。** `io-threads 4`、`--save ""` 配 `appendonly yes`、那两个 AOF 重写阈值、
   `maxmemory-policy noeviction`。**买到：**同一道阶梯从"2,500 cycles/s 就崩"变成"干净跑到 4,000"。
   **怎么验证：**`INFO persistence` —— `aof_rewrite_in_progress`，以及一小时负载里的重写次数。
6. **`SPINNERET_REPORT_SHARDS`** —— 它限定上报**处理**的并行度，因为一个分片只有一个所有者。
   保持每个 worker 实例 2–4 个分片。**代价：**它**在一个部署的整个生命周期里是固定的** ——
   租约 id 编码了自己的分片，改动它会让在飞的租约无处上报，需要一个维护窗口
   （[运维手册](./16-operations.md#修改分片数)）。**怎么验证：**`spinneret_stream_pending` 增长
   而 worker 很忙，说明 worker 是瓶颈；待处理条目分布均匀而 worker CPU 空闲，说明分片太少。
7. **`SPINNERET_STREAM_MAXLEN`** —— 分片所有者会裁剪到消费位点，所以默认的每分片 1,000,000 是
   "worker 排不掉时的积压上限"，不是常驻缓冲。按你想扛过多长的故障来定：`速率 × 秒数 / 分片数`。
   20,000 reports/s、想扛 60 秒的话是每分片 75,000。**代价：**超过上限的条目会被静默丢弃，
   所以设得太低会正好在你最需要那些上报的那次故障里丢掉它们。
   **怎么验证：**故意重启一次 worker，看 `spinneret_stream_pending`。
8. **租约 TTL。** `lease_ttl`（轮换策略默认 `120s`，范围 `5s`–`30m`）决定一个崩掉的节点占着的身份要多久
   才重新可用，也决定一次过载运行要多久才停止污染就绪队列。短一点恢复更快，代价是更多 `Renew`
   流量和更多回收工作；长一点更省，但恢复更慢。`max_lease_lifetime`（默认 30 分钟）给续约设上限。
   `SPINNERET_LATE_REPORT_WINDOW`（默认 `10m`）决定一个已结束租约哈希 —— 约 290 B ——
   要保留多久，好让迟到的上报还能被记录。**怎么验证：**`spinneret_lease_reaped_total{kind}`；
   `expired` 占比很大说明 TTL 比你的节点真实持有时间长。
9. **端点组按策略需要划分，不按 URL 数量划分。** 每个身份的状态是按端点组存的：各约 65 B 就绪队列加
   约 71 B 健康。50 个组 × 10 万身份就是 500 万条就绪队列条目（约 310 MiB），而且在独占租约下，
   每多一个组就多一个抢同一个身份的竞争者。
10. **副本用来换可用性、长轮询容量和上报处理能力** —— 每实例实测 10,000 个 watcher
    （`SPINNERET_MAX_WATCHERS` 把上限压在 20,000），而且每个实例拥有一部分流分片。更细的手段是
    `SPINNERET_ROLE`：`worker` 实例加的是上报处理能力，`api` 实例加的是 acquire 和长轮询能力，
    所以你可以只长一边（[运维手册 → api / worker 拆分](./16-operations.md#api--worker-拆分)）。
    开着准入控制时增加副本不增加 Redis 并发；关着时它们会减少吞吐。
11. **`candidate_sample` 保持 32。** acquire 是按需读取候选状态，而不是把整个采样集批量载入，
    所以 K=1 和 K=32 的差距约 4 µs。降低它几乎没有收益，却会在争用时多花几轮。
12. **载荷缓存保持开启。** `SPINNERET_PAYLOAD_CACHE`（默认 `true`）配 `SPINNERET_PAYLOAD_CACHE_SIZE`
    （默认 `200000`）是本页每一个"每次 acquire 成本"数字背后一个没写出来的前提：实测数据集是 10 万身份，
    整个工作集都装在默认缓存里，所以没有一次实测的 acquire 付过解密的成本。**代价：**设 `false`
    可以让解密后的载荷不留在进程内存里 —— 这是一种加固选择 —— 代价是每次 acquire 一次解密；
    容量设得小于工作集，则每次未命中都要付同样的成本。**怎么验证：**固定施加速率下改动前后的服务端 CPU
    和 acquire p50（[配置参考](./03-configuration.md)）。
13. **数据库连接池从来不是瓶颈。** 所有场景下 PostgreSQL 都低于 3% CPU：热路径只碰 Redis，
    PostgreSQL 只看到批量写入。不过 `SPINNERET_DATABASE_MAX_CONNS` 仍然要随副本数一起提高，
    并保证 `副本数 × max_conns + 余量 < max_connections`（Compose 编排里是 300）。
14. **共用主机上的 ClickHouse 限制** —— `config/clickhouse-limits.xml`，并且让
    `max_server_memory_usage` 高于镜像的空闲 RSS。它不在热路径上，但一个被 OOM 杀掉的 ClickHouse
    会让上报 worker 卡住。
15. **负载均衡的健康检查。** 上游很少时，第一次失败就摘除需要配一个短于重试预算的摘除窗口，
    否则一次慢拨号就变成一次不可用 —— 见[并发配置长轮询](#并发配置长轮询)。

**Profiling。** `SPINNERET_PPROF_ADDR`（例如 `127.0.0.1:6060`）会在单独的监听器上提供
`/debug/pprof/`。默认关闭，永远不会挂在 API 或指标监听器上，而且如果这个地址等于那两个之一，
配置会被直接拒绝。这些端点没有认证，而且会暴露堆内容和 goroutine 栈：永远不要把这个端口发布出去。

---

## 哪些是环境的限制，哪些不是

免得有人把这台笔记本当成软件本身：

| 观察到的现象 | 环境还是产品？ |
| --- | --- |
| 单实例停在 4,500–5,500 cycles/s | **产品**：拥塞回路在 Valkey 主线程被填满之前就开始了（脚本允许每线程 5,940 cycles/s） |
| 两个实例反而比一个低 | **产品**：对同一个 Redis 的单实例并发没有上限。这正是[准入控制](#准入控制)要处理的 |
| 一次失败的 acquire 成本约是成功那次的 5 倍 | **产品**：本页每一次崩塌的放大器 |
| 一个冷（刚播种或刚重建）的站点在 2,500 cycles/s 崩塌 | **产品**，程度轻：统一的初始就绪分让 50 个组完全相关。流量会在大约一百万次 acquire 内修好它 |
| 同样的运行之间时有时无的崩塌 | **环境**，但触发器与产品相关且已修：笔记本 VM 里每 52 秒一次 AOF 重写 fork。已在 `deploy/compose` 里调好 |
| 5,000/s 运行中 ClickHouse 被 OOM 杀掉、冻住整个 VM | **环境**：7.75 GiB 的 VM 同时跑着压测客户端和一套无关的服务。通过限制它的缓存修好了 |
| 10,000 个 watcher 经负载均衡失败 | **环境 / 负载均衡配置**，不是服务端 |
| 高速率运行时压测客户端和负载均衡各占约一核 | **环境**：压测客户端和被测系统共用这个 VM |
| `docker stats` 把 1.9 ms 的 p99 变成 11.9 ms | **环境 / 测量**：用 `MID_STATS=0` |
| 服务端实例从未超过 16 核里的 1.7 核 | 余量 |

---

## 复现

前提：Compose 编排已经跑起来，宿主上有 Python 3 供工具使用。路径相对仓库根目录，而且整个压测会话都用
下面这条仓库根目录的 Compose 命令，因为 `test/load/run.sh` 自己拼出来的就是这一条。`./spnrctl`
不能替代它：那个包装脚本只存在于安装脚本创建出来的部署里，并且钉死了那个部署自己的 Compose project。

```bash
COMPOSE="docker compose -f deploy/compose/docker-compose.yml"

# 1. 播种数据集并拿到节点令牌。
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  seed --site loadtest --identities 100000 --groups 50 > /tmp/seed.json
export LOADTEST_TOKEN=$(python3 -c "import json;print(json.load(open('/tmp/seed.json'))['token'])")

# 2. 单实例数字需要一个副本。SPINNERET_REPLICAS 必须在这个会话的*每一条* compose 命令上导出，
#    否则下一条会把服务缩回 .env 里的值（2）。
export SPINNERET_REPLICAS=1
$COMPOSE up -d --wait

# 3. 预热站点。步骤之间要静置：一次以过载结束的运行会留下 60 秒的租约，
#    以及被推后到 60 秒之后的就绪分。
for r in 1500 2500 3500 4000; do
  MID_STATS=0 test/load/run.sh "warm-$r" acquire_report.js ACQUIRE_RATE=$r DURATION=45s
  sleep 70
done

# 4. 单实例 acquire->report 拐点：作为代表的那次两分钟运行。
MID_AFTER=70 MID_STATS=0 test/load/run.sh acq1-4500 acquire_report.js ACQUIRE_RATE=4500 DURATION=2m

# 5. 只测 Acquire。先提高 max_concurrent_leases，因为没有东西释放时租约会按完整 TTL 堆积。
python3 test/load/tune.py --max-concurrent-leases 4
for r in 4000 5000 6000 8000; do
  MID_STATS=0 test/load/run.sh "acqonly-$r" acquire_report.js \
    ACQUIRE_RATE=$r DURATION=45s REPORT_MODE=none
  sleep 70
done
python3 test/load/tune.py --restore

# 6. 上报摄入。
MID_AFTER=30 MID_STATS=0 test/load/run.sh ing1-20k report_ingest.js \
  BATCH=200 REPORT_RATE=100 DURATION=60s LEASES=400

# 7. 并发 watcher，直连实例（这里的瓶颈是负载均衡）。
MID_AFTER=45 test/load/run.sh watch1-10000 watch_config.js \
  WATCHERS=10000 DURATION=90s RAMP_S=25 SPINNERET_URL=http://spinneret:8080

# 8. 配置变更感知：发布者和 watcher 在一个进程里，所以只有一个时钟。
python3 test/load/config_awareness.py --watchers 200 --publishes 6 --interval 3

# 9. 按脚本的成本：在约 1,000 cycles/s 的负载下采样 0.3 秒内的所有命令，
#    再按脚本 SHA 给 EVALSHA 条目分组（就是"时间花在哪里"那张表）。
MID_STATS=0 test/load/run.sh slowlog acquire_report.js ACQUIRE_RATE=1000 DURATION=60s &
sleep 40 && $COMPOSE exec -T valkey valkey-cli config set slowlog-log-slower-than 0
# ... 0.3 秒之后：`slowlog get`，然后把 slowlog-log-slower-than 恢复成 10000。

# 10. 回到正常拓扑。
unset SPINNERET_REPLICAS
$COMPOSE up -d --wait
```

每次运行会把 `before.json`、`mid.json`、`after.json`、`delta.json`、`gauges.json`、`k6.txt` 和
`k6.json` 写到 `.loadtest/<name>/`（被 git 忽略）。`test/load/metrics.py diff a.json b.json`
会打印任意两份快照之间的服务端侧差值，包括按 Valkey 命令的 CPU 归因。
`test/load/README.md` 记录了每个场景和它的变量；`test/perf/README.md` 记录了按脚本的微基准
以及它们对照的那些冻结基线。

"先预热再测量"的流程，三句话说完，因为这正是大家会跳过的部分：

1. **预热** —— 第 3 步那道阶梯，大约一百万次 acquire，直到 50 个就绪队列不再相关。
2. **静置** —— 等到就绪队列重新到期
   （`ZCOUNT sp:{s<站点 key>}:rdy:<端点组 key> -inf <now>` 回到基数），
   并且 `ZCARD sp:{s<站点 key>}:lsexp` 接近零。这两个键都是用**数字**站点 key 和数字端点组 key 拼的，
   不是它们的名字；[故障排查](./18-troubleshooting.md#身份池被抽干且再也恢复不了)里写了数字站点 key
   去哪里读。
3. **测量** —— 在你打算引用的那个速率上跑一次两分钟，带 `MID_STATS=0`。

---

## 清理

一次压测会在 Redis 里留下与流量成正比的状态 —— 去重标记、已结束租约哈希、流条目 ——
它们会在 `SPINNERET_REPORT_DEDUP_TTL` 内自行过期；但 ClickHouse 里的行不会：一个下午的测试大约
2 GiB。在小机器上，两次运行之间要把它们回收掉。跳过这一步，就是让一次原本干净的重跑变成崩塌，
而且 Valkey 会在运行中被 OOM 杀掉。

```bash
# 裁剪上报流（仅限测试数据；会丢掉任何未处理的积压）。
$COMPOSE exec -T valkey valkey-cli eval \
  "local n=0 for i=0,15 do n=n+redis.call('XTRIM','sp:{r'..i..'}:stream','MAXLEN',5000) end return n" 0

# 删掉上报去重标记。
$COMPOSE exec -T valkey sh -c \
  "valkey-cli --scan --pattern 'sp:{r*}:dd:*' --count 5000 > /tmp/k; xargs -a /tmp/k -n 1000 valkey-cli unlink"

# 压测产生的分析行。
$COMPOSE exec -T clickhouse clickhouse-client --user spinneret --password "$CLICKHOUSE_PASSWORD" \
  -q "TRUNCATE TABLE spinneret.report_events; TRUNCATE TABLE spinneret.lease_events"
```

**警告。永远不要 unlink 租约哈希**（`sp:{s<站点 key>}:ls:*`）。租约结束时，正是这个哈希把身份的活跃租约
计数减回去；删掉一个还活着的，那个计数就会泄漏，这个身份永远不会再变为可用。这里做过一次，
结果 10 万个身份里有 97,520 个被永久占用，之后每一次运行都崩塌进 `resource_exhausted`。
裁剪一个还存有未处理的 `release: true` 上报的流也是同样的问题。

修复办法是 unlink 整个站点前缀然后重建 —— `spnr rebuild --site` 本身**不会**重置运行时计数器，
这是有意的，因为一次重建不能丢掉还活着的租约：

```bash
$COMPOSE exec -T valkey sh -c \
  "valkey-cli --scan --pattern 'sp:{<site-tag>}:*' --count 5000 > /tmp/k; xargs -a /tmp/k -n 1000 valkey-cli unlink"
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate rebuild --site loadtest
```

重建出来的站点是冷的，测量之前要重新预热。

---

## 下一步

- [配置参考](./03-configuration.md) —— 这一页提到的每一个变量，以及它的取值范围。
- [运维手册 → 扩容](./16-operations.md#扩容) —— 副本、角色、分片，以及该盯什么。
- [可观测性与告警](./12-observability.md) —— 指标，以及值得设阈值告警的那些。
- [故障排查 → 延迟升高或吞吐崩塌](./18-troubleshooting.md#延迟升高或吞吐崩塌) ——
  同一个特征，从症状那一端看，附完整的准入控制指标表。
- [策略](./08-policies.md) —— `max_concurrent_leases`、`lease_ttl` 和 `candidate_sample` 住在这里。
- [参与贡献](./20-contributing.md) —— 怎么跑 `test/perf/`、怎么证明一次优化。
