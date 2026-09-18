# 性能压测报告

[English](benchmarks.md) · [简体中文](benchmarks.zh-CN.md)

本文记录对设计文档 §18.4 中 v0.1 性能目标的实测结果，测试脚本位于 `test/load/`，被测对象是
`deploy/compose/` 的 Docker Compose 全栈。所有数字都可用[复现步骤](#复现步骤)重跑；产生这些数字的
那一轮原始快照写在 `.loadtest/<run>/`（已被 git 忽略）。

每张表都带一列**优化前**：同一台机器上、在 scheduler 与 worker 两条 Lua 热路径优化之前、
并且在 [Valkey 配置](#valkey-配置)调整之前测得的同一场景。优化前后相隔数月，机器后台负载不可能完全
一致，因此个位数百分比请当作噪声；下面的变化幅度是 30%–120%。

## 结论汇总

| 目标（§18.4） | 优化前 | 优化后 | 判定 |
| --- | --- | --- | --- |
| Acquire 服务端延迟 p99 < 5 ms | 单实例 2,000 循环/秒时 0.95 ms | **单实例 4,500 循环/秒时 1.86 ms**；仅 Acquire 4,993/s 时 4.32 ms | **达标** |
| Acquire 吞吐（单实例）≥ 5,000 次/秒 | 稳定 2,000 循环/秒；仅 Acquire 峰值 4,614/s | **仅 Acquire 稳定 4,993/s**，p99 4.32 ms（峰值 7,792/s）。完整 acquire→report 循环：`max_concurrent_leases: 4` 时 **5,500/s**，独占租约时 **4,500/s** | **有条件达标** —— Acquire 本身达标，非独占轮换策略下完整循环也达标；独占租约下为 4,500/s。见[逐项判定](#逐项判定) |
| Report 接收吞吐（单实例）≥ 20,000 条/秒 | 29,584 条/秒 | **44,437 条/秒**，零拒绝 | **达标** |
| 上报到状态更新完成 p99 < 200 ms | 9,846 条/秒时 21.5 ms | **19,761 条/秒时 30.7 ms**，1,200,200 条全部落库；9,905 条/秒时 9.5 ms | **达标**，且速率翻倍 |
| 配置变更感知 < 1 秒 | 空载 p99 55.4 ms | **空载 p99 46.1 ms** | **达标** |
| 并发长轮询（单实例）≥ 10,000 | 保持 10,001 个 | 采样瞬间保持 9,994 个，零失败，0.05 核 | **达标** |

按 Valkey 线程折算，一次 acquire→report 循环消耗 **168.3 µs** 的 Redis CPU（原 271.2 µs）——
**每个 Redis 线程 5,940 循环/秒，原为 3,690**（逐脚本测量见[时间都花在哪](#时间都花在哪)）。

### 逐项判定

单实例 Acquire 目标**已达标**，条件如下：

* **仅 Acquire**（§18.4 点名的那个接口）：单实例 4,993/s，服务端 p99 4.32 ms，Valkey 占用 0.75 核。
  允许延迟放开时峰值 7,792/s。
* **完整 acquire→report 循环**（Acquire + Report + worker 落库并结束租约，共 5 个 Lua 脚本）：
  轮换策略 `max_concurrent_leases: 4` 时单实例 **5,500/s**，acquire p99 4.49 ms、上报滞后 p99 62 ms。
* **独占租约**（`max_concurrent_leases: 1`，即 `spnr seed` 的配置，也是配置空间里最贵的一点）：
  **4,500 循环/秒**，acquire p99 1.86 ms、上报滞后 p99 9.3 ms。这是唯一一个差 5,000/s 的数字。
* **以上全部在单个 Valkey 实例上取得**，符合设计文档的测试条件。达到这些数字没有用到、也不需要
  Redis Cluster。

负载均衡器后面挂两个实例时总吞吐是 **3,000 循环/秒**，比单实例自己还低。这是现在的主要瓶颈，
而且它不是 Redis CPU —— 见[为什么两个实例反而不如一个](#为什么两个实例反而不如一个)。

## 测试环境

所有组件 —— 服务端副本、PostgreSQL、Valkey、ClickHouse、负载均衡器**以及 k6 压测客户端** ——
都跑在一台笔记本上的同一个 Docker Desktop 虚拟机里。这是下面每个数字都要加的诚实限定：压测客户端
在和被测系统抢资源。

| 组件 | 配置 |
| --- | --- |
| 宿主机 | macOS（Darwin 25.6.0），Docker Desktop 29.4.0 |
| Docker 虚拟机 | 16 CPU、7.75 GiB 内存，所有容器与 k6 共享 |
| 服务端 | `spinneret:local`（本仓库），distroless，`SPINNERET_REPORT_SHARDS=16`，`SPINNERET_DATABASE_MAX_CONNS=32` |
| Redis | `valkey/valkey:8-alpine`（8.1.10），单实例 —— 见 [Valkey 配置](#valkey-配置) |
| PostgreSQL | `postgres:17-alpine`，`shared_buffers=512MB`、`max_connections=300` |
| ClickHouse | `clickhouse/clickhouse-server:25.8-alpine`，由 `config/clickhouse-limits.xml` 限制内存 |
| 负载均衡 | `caddy:2-alpine`，`deploy/compose/config/Caddyfile` |
| 压测客户端 | 同一虚拟机内的 `grafana/k6:latest`（k6 2.2.0），compose `loadtest` profile |

设计文档的测试条件是「服务实例 4 vCPU / 8 GB，单个 Redis 实例」。在可持续速率下，单个服务实例用掉
16 核中的 1.4–1.6 核，Valkey 用 1.6–1.9 核（其中约 1 核是执行命令的主线程），两者都没有被设计假设的
规格饿死。

### Valkey 配置

相对「优化前」有三项设置变了，全部位于 `deploy/compose/docker-compose.yml`，每一项都有下面的实测依据。

| 设置 | 优化前 | 优化后 | 原因 |
| --- | --- | --- | --- |
| `io-threads` | 1 | **4** | 吞吐只 +3%，但拐点处长尾差别巨大 —— 6,000 循环/秒时 acquire p99 从 18.9 ms 降到 4.1 ms。见 [Valkey io-threads](#valkey-io-threads) |
| `save`（RDB） | 默认 `3600 1 300 100 60 10000` | **关闭**（`--save ""`） | 持久化由 AOF 负责。RDB 相当于为同一批数据再 fork 一次，压测时每分钟一回 |
| `auto-aof-rewrite-percentage` / `-min-size` | 100 / 64 MiB | **300 / 1 GiB** | 用默认值时，4,000 循环/秒下平均每 52 秒就触发一次重写。见 [AOF 重写把系统推下悬崖](#aof-重写把系统推下悬崖) |

`appendonly yes` 与 `appendfsync everysec` 未改：这里没有拿持久性换性能。

ClickHouse 新增了 `config/clickhouse-limits.xml` 限制各类缓存（mark cache 默认 5 GiB）。不加限制时它
涨到 1.1 GiB 以上，在一次 5,000/s 压测中被内核 OOM killer 选中，冻结了整个虚拟机。注意
`max_server_memory_usage` 比较的是进程 RSS，而该镜像无论缓存怎么调，空载 RSS 都在 1.2 GiB 左右；
把它设到这个值**以下**并不会让 ClickHouse 变小，只会让每次 INSERT 都以 `MEMORY_LIMIT_EXCEEDED`
失败，进而卡住上报 worker。现在设为 1.5 GiB，作为安全网。

## 数据集

即 §18.4 的测试条件 —— 单站点 10 万身份、50 个端点组：

```bash
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate \
  seed --site loadtest --identities 100000 --groups 50
```

幂等地创建：站点 `loadtest`（客户端 `web`）、匹配 `/api/g<i>/` 的端点组 `g0…g49`、身份类型
`loadtest_cookie` 及 10 万个合成 Cookie 身份、已发布的轮换策略 `loadtest-rotation`
（`weighted_random`、`candidate_sample: 32`、`lease_ttl: 60s`、`max_concurrent_leases: 1`、
`reuse_interval: 0s`、无代理）、一个永不触发的熔断策略、配置项 `crawler/loadtest.json`，
以及一个节点令牌。

`max_concurrent_leases: 1` 意味着每个租约都是**独占**的：一个身份被租出去时，它在全部 50 个端点组里
都不可用。这是配置空间里最贵的一端，而 seed 默认就是这么配的。

### 刚 seed 出来的站点必须先预热再测

seed（以及 `spnr rebuild`）会给**每个端点组里的每个身份写同一个就绪队列分值**。于是 50 个组都从队列
同一个头部取候选，在独占租约下必然互相撞车：某个组取到一个已被租出的身份，就要把它的分值推到租约到期
时刻 —— 这是预热之后的站点根本不会做的 Redis 工作。在本数据集上实测：冷站点在 2,500 循环/秒就崩进
`resource_exhausted`，而同一个站点预热之后可以扛住 4,500/s。

流量本身会让这 50 个队列逐渐去相关。[复现步骤](#复现步骤)里的阶梯 —— 1,500、2,500、3,500、4,000
循环/秒各跑 45 秒，步骤之间留出沉降时间 —— 就够了，约一百万次 acquire。**下面每个吞吐数字都是在预热后的
站点上测的**，这也是真实部署运行时所处的稳态。冷启动行为本身是值得知道的真实特性（重建热态之后短时间内
会更贵），但不是做容量规划时该引用的那个数字。

## 测量方法

`test/load/run.sh` 在每个场景前后各打一次服务端指标快照：

1. 抓取每个副本的 `/metrics`（副本不暴露宿主端口，所以经由 `lb` 容器），以及 Valkey 的 `INFO` /
   `INFO commandstats` → `before.json`；
2. 在 compose `loadtest` profile 里跑 k6 场景；
3. 运行中途再采一次仪表值 → `mid.json`；
4. 结束后再采一次 → `after.json`，并 diff → `delta.json`。

延迟取自**服务端**直方图（`spinneret_acquire_duration_seconds`、`spinneret_report_lag_seconds`），
分位数在 Prometheus 桶内插值，因此不含 Docker 网络与 Caddy。吞吐取自 k6 自己的计数器（快照窗口比场景
长约 3 秒，会低估）。Valkey CPU 取 `used_cpu_user + used_cpu_sys` 在快照窗口上的增量，各命令归因取
`INFO commandstats` 增量 —— 它把 Lua 脚本内部发出的调用也算在内。

`spinneret_acquire_duration_seconds` 最大的桶是 0.25 秒；下文中打印为 `inf` 的 p99 表示「大于 250 ms」，
即该轮已经过载。

相对「优化前」，测量方法有两处变化，两处都影响数字的含义：

* **`MID_STATS=0`。** 中途快照原本会调用 `docker stats`，它会遍历整台机器上的所有容器。在 Docker
  Desktop 上这个开销大到足以扰动被测系统：4,000 循环/秒时它让系统停顿约 5 秒（1.9 万次迟到迭代），
  把本来 1.9 ms 的 p99 拉到 11.9 ms。下文所有吞吐数据都用 `MID_STATS=0` 测；每容器 CPU/内存仪表值
  单独在远离拐点的轮次里采集。
* **两轮之间要沉降。** 过载结束的一轮会留下 60 秒 TTL 的租约，以及被推到未来最多 60 秒的就绪队列分值。
  不等它们排空就开下一轮，测的是恢复过程而不是系统本身。[复现步骤](#复现步骤)里的流程会等到就绪队列
  重新到期。

## 场景 A —— Acquire → Report

`test/load/acquire_report.js`：一次 `LeaseService/Acquire`，随后一次带 `release: true` 的
`ReportService/Report`，按固定到达率发起，端点组从 50 个中均匀抽取。释放是异步的：上报进 Redis Stream，
由 worker 结束租约，所以一次迭代会跑满 5 个 Lua 脚本（acquire、ingest、lease_retain、observe、release）。

### 单副本 + seed 策略（`max_concurrent_leases: 1`）—— 单实例数字

| 目标速率 | 实际达成 | Acquire p50 | Acquire p99 | 滞后 p50 | 滞后 p99 | 服务端 CPU | Valkey CPU |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,500/s | 1,500/s | 0.12 ms | **0.50 ms** | 2.50 ms | 4.96 ms | 0.52 | 0.42 |
| 2,500/s | 2,500/s | 0.18 ms | **0.94 ms** | 2.51 ms | 4.97 ms | 0.80 | 0.73 |
| 3,500/s | 3,500/s | 0.28 ms | **1.74 ms** | 2.53 ms | 6.72 ms | 1.09 | 1.17 |
| 4,000/s | 4,000/s | 0.31 ms | **1.98 ms** | 2.55 ms | 8.50 ms | 1.23 | 1.37 |
| **4,500/s（2 分钟）** | **4,500/s** | 0.32 ms | **1.86 ms** | 2.56 ms | 9.32 ms | 1.41 | 1.61 |
| 5,000/s | 1,746/s + 534/s 耗尽 | 189 ms | > 250 ms | > 10 s | > 10 s | 0.51 | **2.02** |

4,500/s 跑满 2 分钟是单实例的头条结果：**54 万次 acquire、54 万次 report，零错误、零耗尽**，
acquire p99 1.86 ms，上报滞后 p99 9.3 ms。优化前同样一个实例只能稳定在 **2,000/s**，3,000/s 即崩溃。

| | 优化前 | 优化后 | 变化 |
| --- | ---: | ---: | ---: |
| 单实例独占租约下可持续 acquire→report 循环 | 2,000/s | **4,500/s** | **+125%** |
| 可持续速率下的 Acquire p99 | 0.95 ms | 1.86 ms | 速率为 2.25 倍 |
| 每循环 Valkey 命令数 | 51.5（2,000/s 时 102,987 cmd/s） | **49.3**（4,500/s 时 215,163 cmd/s） | −4% |
| 一次循环 5.1 个脚本的 `EVALSHA` 均值 | 43.7 µs | **24.5 µs** | **−44%** |

### 单副本 + `max_concurrent_leases: 4`

| 目标速率 | 实际达成 | Acquire p50 | Acquire p99 | 滞后 p50 | 滞后 p99 | 服务端 CPU | Valkey CPU |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 5,000/s（2 分钟） | 4,999/s | 0.36 ms | **4.35 ms** | 2.73 ms | 91.4 ms | 1.60 | 1.77 |
| **5,500/s（2 分钟）** | **5,500/s** | 0.40 ms | **4.49 ms** | 3.00 ms | 62.4 ms | 1.61 | 1.88 |
| 6,000/s（2 分钟） | 5,982/s | 0.68 ms | 18.0 ms | 4.93 ms | > 10 s | 1.58 | 1.94 |

5,500/s 是**两个** §18.4 延迟目标同时仍然成立的最高速率（acquire p99 < 5 ms、上报到状态更新
p99 < 200 ms）。到 6,000/s 吞吐还在（目标 6,000 达成 5,982），但上报 worker 跟不上了，先破的是
滞后指标。优化前同样配置只能稳定 3,000/s。

### 双副本（经负载均衡器）

| 目标速率 | 实际达成 | Acquire p99（实例 1 / 2） | 滞后 p99 | 每实例服务端 CPU | Valkey CPU | Valkey cmd/s |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,500/s | 1,500/s | 0.55 / 0.58 ms | 4.96 ms | 0.30 | 0.45 | 70,253 |
| 2,500/s | 2,500/s | 1.76 / 0.99 ms | 4.98 ms | 0.46 | 0.83 | 117,501 |
| **3,000/s（2 分钟）** | **3,000/s** | **1.36 / 1.61 ms** | 4.98 ms | 0.59 | 1.17 | 143,854 |
| 3,500/s（45 秒） | 3,500/s | 1.91 / 1.36 ms | 6.1 ms | 0.61 | 1.33 | 160,039 |
| 3,500/s（2 分钟） | 1,288/s + 946/s 耗尽 | > 250 ms | > 10 s | 0.25 | **3.53** | 438,650 |
| 4,000/s | 1,743/s + 477/s 耗尽 | > 250 ms | > 10 s | 0.25 | **3.71** | 462,282 |
| 4,500/s | 2,117/s + 342/s 耗尽 | > 250 ms | > 10 s | 0.33 | **3.59** | 457,091 |

双副本可持续 **3,000 循环/秒** —— 和优化前一模一样，而且比单副本自己还少 1,500 循环/秒。3,500/s 能撑
45 秒但撑不过 2 分钟。3,000/s 时延迟确有改善（p99 1.72 → 1.36 ms，滞后 p99 6.75 → 4.98 ms），
Valkey 主线程余量也大得多，但天花板没有抬起来。见
[为什么两个实例反而不如一个](#为什么两个实例反而不如一个)。

### 过载行为（拥塞崩溃）

越过拐点后系统仍然不是优雅降级而是崩溃，而且循环与第一轮测量时完全一样：

1. Valkey 饱和 → 上报 worker 落后 → 租约无法释放；
2. 身份一直处于租出状态，于是另外 49 个组的 acquire 抽到它们、拒绝它们，并把它们的就绪队列分值往后推 ——
   每次一个 `HGET` 加一个 `ZADD`；
3. 失败的 acquire 要比成功的多走很多候选：在双副本 4,000/s 的崩溃点实测，
   **每次 acquire 220 条 Redis 命令而不是 49 条**，`EVALSHA` 均值 **157 µs 而不是 27 µs**；
4. 这又把 Valkey 推得更深。

变化的是这个循环从哪里开始、以及一旦开始有多贵。失败 acquire 的单次成本比以前低（157 µs 对以前测到的
212 µs），但正反馈机制原封不动，本文里每一个拐点都是被它决定的 —— 而不是被 Valkey 的 CPU 上限决定。
在双副本崩溃点，Valkey 在它的多个 I/O 线程上烧掉 3.5–3.7 核，服务的命令数是同样负载健康时的四倍。

运维上：把实际负载压在实测拐点的 80% 以下，对
`spinneret_acquire_total{result="exhausted"}` 和 `spinneret_report_lag_seconds` 设告警，
并参考[调优建议](#调优建议)里能彻底去掉第 2 步的配置。

## 场景 B —— 只做 Acquire

`REPORT_MODE=none` 跳过上报，于是只跑 `acquire.lua`（单副本，`max_concurrent_leases: 4`，
否则堆积的租约会让身份变成独占）。这样能孤立出 §18.4 点名的 Acquire 接口，但它不是真实负载：
租约会一直堆到 60 秒 TTL 到期，污染就绪队列。

| 目标速率 | 优化前：达成 / p99 | 优化后：达成 | 优化后 p50 | 优化后 p99 | 服务端 CPU | Valkey CPU |
| ---: | --- | ---: | ---: | ---: | ---: | ---: |
| 3,000/s | 2,975/s / 9.22 ms | — | — | — | — | — |
| 4,000/s | 3,926/s / 209 ms | 3,994/s | 0.47 ms | **2.00 ms** | 0.43 | 0.58 |
| 5,000/s | 4,436/s / > 250 ms | **4,993/s** | 0.65 ms | **4.32 ms** | 0.54 | 0.75 |
| 6,000/s | — | 5,779/s | 0.91 ms | > 250 ms | 0.56 | 1.09 |
| 8,000/s | 4,614/s / > 250 ms | **7,792/s** | 4.42 ms | > 250 ms | 0.72 | 1.49 |

**单实例、单 Valkey 上的 Acquire 峰值吞吐 7,792/s**，比 4,614/s 提升 **69%**；而且 §18.4 要求的
5,000/s 现在是在 p99 < 5 ms 目标**之内**达成的（4,993/s、4.32 ms），而优化前 5,000/s 的目标速率只能
跑出 4,436/s 且 p99 超过 250 ms。

## 场景 C —— 上报接收

`test/load/report_ingest.js`：针对 400 个长租约批量上报，单副本。「接收」是服务端自己的
`spinneret_report_ingest_total{result="accepted"}`；「落库」是 `spinneret_report_lag_seconds` 的
计数，即 worker 真正写进热态的条数。

| 批大小 × 速率 | 接收 | 拒绝 | 落库 | 滞后 p50 | 滞后 p99 | 服务端 CPU | Valkey CPU |
| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: |
| 100 × 100/s | **9,905/s** | 0 | 600,100 / 600,100 | 2.73 ms | **9.5 ms** | 0.53 | 0.93 |
| 200 × 100/s | **19,761/s** | 0 | 1,200,200 / 1,200,200 | 6.10 ms | **30.7 ms** | 0.88 | 1.53 |
| 200 × 150/s | **29,640/s** | 0 | 1,311,096 / 1,800,200 | 8.3 s | > 10 s | 0.94 | 1.71 |
| 300 × 150/s | **44,437/s** | 0 | 1,090,595 / 2,700,300 | > 10 s | > 10 s | 0.95 | 1.65 |

| | 优化前 | 优化后 | 变化 |
| --- | ---: | ---: | ---: |
| 零拒绝下的接收能力 | 29,584/s | **44,437/s** | **+50%** |
| 滞后 p99 < 200 ms 时的落库能力 | 约 9,846/s（p99 21.5 ms） | **19,761/s（p99 30.7 ms）** | **+101%** |
| 约 10,000 条/秒时的滞后 p99 | 21.5 ms | **9.5 ms** | **−56%** |

两条路径里更窄的仍然是落库，但它翻倍了：单实例现在能在 200 ms 目标内把 **约 20,000 条/秒** 写进热态，
而优化前接收 19,702 条/秒时，一轮结束还剩 42.9 万条没落库。再往上 Stream 积压增长，滞后随之上升。

## 场景 D —— 并发配置 watcher

`test/load/watch_config.js`，单副本，直连实例（原因见下面的负载均衡器说明）。watcher 必须持有*当前*
版本，否则服务端会立即返回；脚本在 `setup()` 里读取已发布版本。副本上的
`spinneret_config_watchers` 是持有数量的权威来源。

| watcher 数 | 服务端仪表读数 | 失败 | 服务端 CPU | goroutine | 服务端 RSS | Valkey cmd/s |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 10,000（优化前） | 10,001 | 0 | 0.09 | 20,198 | 749 MiB | 52 |
| 10,000（优化后） | **9,994** | **0** | **0.05** | 20,110 | 504 MiB | 206 |

目标达标：单实例约 1 万个阻塞中的 `WatchConfig` 调用，零失败，0.05 CPU 核，而且阻塞中的 watcher
完全不消耗 Redis。（9,994 而不是 10,001 是采样瞬间的差异而非失败 —— 有 6 个 k6 轮询处在两次 poll
之间；该轮 `watch_polls` 零错误。）

第一轮记录的负载均衡器陷阱依然成立、依然值得一读：1 万个连接在同一毫秒到达会打爆 Caddy 的
`dial_timeout 2s`，而 `max_fails 1` 会让一次失败拨号就摘掉唯一的上游，在服务端 CPU 只有 0.8% 时造成
完全不可用。随仓库发布的 `Caddyfile` 现在用 `max_fails 3`；请把 `dial_timeout` 设到自己 LB 上连接
风暴的最坏 accept 延迟之上，并让节点重启错峰。

## 场景 E —— 配置变更感知

`test/load/config_awareness.py` 反复发布 `crawler/loadtest.json` 的新版本，并测量自己那批 watcher
的唤醒延迟。发布方和 watcher 在同一个进程里，两个时间戳来自同一个时钟。`total` 从发起 publish RPC
之前开始计（保守值），`from_commit` 从 `PublishConfig` 返回开始计。

| 场景 | 唤醒数 | total p99（最差一轮） | total 最大值 | from_commit p99 |
| --- | ---: | ---: | ---: | ---: |
| 空载实例、200 个探针 watcher、6 次发布 —— 优化前 | 1,200 / 1,200 | 55.4 ms | 57.2 ms | 44.0 ms |
| 空载实例、200 个探针 watcher、6 次发布 —— 优化后 | **1,200 / 1,200** | **46.1 ms** | **46.8 ms** | **38.4 ms** |

零轮询错误，`woken_ratio` 1.0。比 1 秒的目标低两个数量级。

## 分析

### 时间都花在哪

每个 Acquire→Report 循环是 5 个 Lua 脚本。在约 1,000 循环/秒的负载下，对全部 Valkey 命令采样 0.3 秒
（`SLOWLOG` 配 `slowlog-log-slower-than 0`，再按脚本 SHA 归类 `EVALSHA` 条目）即可归因。与「优化前」
一列同方法、同数据集、同机器：

| 脚本 | 每循环调用次数 | 优化前（µs） | 优化后（µs） | 变化 |
| --- | ---: | ---: | ---: | ---: |
| `acquire.lua` | 1 | 104.2 | **68.3** | **−34%** |
| `observe.lua`（worker 状态更新） | 每条上报 1 次 | 71.7 | **43.5** | **−39%** |
| `release.lua`（租约结束） | 每个被释放租约 1 次 | 67.0 | **34.0** | **−49%** |
| `ingest.lua` | 每批 1 次 | 16.3 | **12.2** | **−25%** |
| `lease_retain.lua` | 每个请求每站点 1 次 | 12.0 | **10.3** | **−14%** |
| **每循环合计** | | **271.2** | **168.3** | **−38%** |
| **每 Redis 线程循环/秒** | | **3,690** | **5,940** | **+61%** |

同一个采样窗口里还抓到两个不在循环内的脚本：`apply.lua` 49.8 µs（一次自动冷却动作）、
`breaker_eval.lua` 47.2 µs（按组的巡检，受 `NotifyMinInterval` 限频，不是每条上报都跑）。

支撑这些改动的逐脚本、逐 Lua 辅助函数的交错微基准（基线源码冻结在 `test/perf/testdata/`）见
[`test/perf/README.md`](../test/perf/README.md)。那些基准直连开发机上的 Valkey，绝对值比本虚拟机便宜
约 1.4 倍；上表是在虚拟机里的端到端确认。

实测的每 Redis 线程 5,940 循环/秒是**脚本**给出的上限。场景 A 的拐点更低（单实例 4,500–5,500、
双实例 3,000），是因为拥塞循环在线程跑满之前就启动了，而不是因为线程跑满了。

### Valkey io-threads

Valkey 在主线程上执行每一条命令 —— 包括 Lua —— 所以额外的 I/O 线程只搬运 socket 读写。第一轮 profiling
根据一个闭环微基准得出「不值得」的结论。在本栈上端到端实测：它换来的吞吐很少，换来的长尾很多：

| 单实例、`max_concurrent_leases: 4`、60 秒 | io-threads=1 | io-threads=4 |
| --- | ---: | ---: |
| 6,000/s 目标 → 达成 | 5,955/s | **5,999/s** |
| 6,000/s → acquire p99 | 18.9 ms | **4.09 ms** |
| 6,000/s → 上报滞后 p99 | 4,656 ms | **41 ms** |
| 6,000/s → Valkey CPU | 0.91 核 | 1.92 核 |
| 7,000/s 目标 → 达成 | 6,765/s | **6,976/s** |
| 7,000/s → acquire p50 / p99 | 40.0 ms / 237 ms | **0.87 ms / 9.88 ms** |
| 7,000/s → Valkey CPU | 0.95 核 | 1.91 核 |

`io-threads 4` 现在是 compose 的默认值。这笔交易是：多花约 1 核 Valkey CPU，换取在曲线上再往上
1,000–2,000 循环/秒都还留在 §18.4 目标之内的长尾；在 Redis 有富余核的机器上值得。
`VALKEY_IO_THREADS=1` 可以退回旧行为。它并不怎么提升**可持续**速率：拐点处主线程仍是那堵墙，
这也是上面 5,500/s 和 4,500/s 只比 io-threads=1 高一点的原因。

### AOF 重写把系统推下悬崖

这是本轮最大的稳定性发现。用 Valkey 默认值（`auto-aof-rewrite-percentage 100`、
`auto-aof-rewrite-min-size 64mb`）外加默认 RDB `save` 点，4,000 循环/秒的压测
**20 分钟内触发了 23 次 AOF 重写 —— 平均每 52 秒一次**，每次 fork 一个约 1 GiB 的进程、写出约
290 MiB，之上还叠加后台 RDB 保存。Spinneret 的热态以短命键为主（租约哈希、上报去重标记、Stream
条目），所以 AOF 的增长速度远快于它所描述的数据集，重写触发条件被持续满足。

在笔记本虚拟机里，每次重写都是数秒级的 I/O 停顿；而拐点下方那一段是**亚稳的**：一次停顿就足以点燃
拥塞循环，而且再也回不来。表现出来就是：一小时前还干净的速率，这一轮从第一秒就崩。改成 `--save ""`、
`--auto-aof-rewrite-percentage 300`、`--auto-aof-rewrite-min-size 1gb` 之后，本来在 2,500 循环/秒
就崩的同一条阶梯一路干净跑到 4,000/s，4,500/s 的两分钟测试也通过了。两次之间没有改动任何别的东西。

持久性没有变：AOF 仍然开着，仍然是 `appendfsync everysec`。被去掉的是重复劳动（AOF 之外再来一份 RDB）
和过于急躁的重写触发条件。

### 为什么两个实例反而不如一个

单实例可持续 4,500 循环/秒，Caddy 后面两个实例只有 3,000。这个结果可复现，而且不是 Redis CPU 的问题：
在双副本 3,500/s 的崩溃点上，*健康状态下* Valkey 大约只需要 4 核 io-threads 预算中的 1.4 核，
而实际观察到的是 3.5 核花在四倍数量的命令上 —— 也就是拥塞循环，只是提前启动了。

同样的目标速率下，两个实例有什么不同：

* **对同一个 Valkey 的在途并发翻倍。** 每个实例有自己的连接池，且没有跨实例的准入控制，所以同样的到达率
  变成大约两倍的并发请求。Valkey 排队更深，每次 acquire 更慢，独占租约因此被持有更久，争用上升，
  崩溃循环的第 2 步在更低的目标速率就启动了。
* **上报分片被拆开。** 每个实例拥有 16 个 Stream 分片中的 8 个，于是维持身份池的租约释放依赖于
  *两个* worker 都跟得上；任何一个掉队，崩溃就开始。
* **不是 Caddy 的锅。** 同样的双副本负载直连实例
  （`SPINNERET_URL=http://spinneret:8080`，走 Docker DNS 轮询）在 3,500/s 以同样方式崩溃，
  此时 Caddy CPU 只有 0.5%。

下一步的杠杆，按大概率收益排序：

1. **给每个实例的在途 acquire 设上限**（Acquire 路径上的准入控制），让超过拐点的负载在服务端排队，
   而不是在 Redis 侧把并发翻倍。这是把崩溃变成优雅降级的关键。
2. **让失败的 acquire 变便宜。** 它现在约是成功 acquire 的 5 倍（220 条命令对 49 条）；一个因为独占
   被拒的候选，每次被 50 个组中任何一个抽到都要付一次 `HGET` 加一次 `ZADD`。在租约剩余生命周期内记住
   这次拒绝，或者用一个已经排除掉「已租出」身份的方式抽样，就能拆掉这个放大器。
3. **给 Redis 分片。** 所有键都按站点或上报分片打了 hash tag，所以 Redis Cluster 能把每线程
   5,940 循环/秒按主节点数量倍增。这是修好循环之后抬天花板的杠杆；单独用它只是把崩溃点挪高。

### Redis 容量测算

在真实数据集上用 `MEMORY USAGE` 实测（Valkey 8，10 万身份、50 个端点组）：

| 结构 | 键 | 实测 | 单位成本 |
| --- | --- | ---: | --- |
| 就绪队列 | `P:T:rdy:<eg>`，10 万成员 | 6,457,568 B | **每（身份 × 端点组）64.6 B** → **每 100 万条就绪队列项约 62 MiB** |
| 健康状态 | `P:T:hs:<eg>` | 1,177 项共 84,072 B | 每个已预热的（身份 × 组）约 71 B |
| 身份 | `P:T:id:<i>` | 400 个样本平均 201 B、最大 296 B | 每个身份（原 264 B） |
| 上报去重标记 | `P:R:dd:<reportId>` | 80 B | 每条上报，存活 `SPINNERET_REPORT_DEDUP_TTL`（1 小时） |
| 已结束租约 | `P:T:ls:<leaseId>` | 约 290 B | 每个租约，结束后再保留 `max(迟到窗口, 去重 TTL)` |
| 上报 Stream 条目 | `P:R:stream`（16 分片） | 约 440 B | 每条 |

刚重建好的 §18.4 数据集，Valkey 内存合计 **351 MiB**。生产环境里真正占大头的是*与流量成正比*的状态，
而不是数据集本身：

```
Redis 工作集 ≈ 350 MiB（数据集）
             + 最多 345 MiB                      （健康状态，等到每个身份在每个组都预热之后）
             + 上报/秒   × 去重TTL × 80 B        （去重标记）
             + acquire/秒 × 去重TTL × 290 B      （已结束租约哈希）
             + 积压条数 × 440 B                   （上报 Stream）
```

按上面实测的可持续 4,500 循环/秒和默认 1 小时去重 TTL，这就是 1.2 GiB 去重标记加 4.2 GiB 已结束租约
哈希 —— 远超数据集本身。把 `SPINNERET_REPORT_DEDUP_TTL` 按节点的实际重试行为来设，是内存上最大的单一
杠杆；见[调优建议](#调优建议)。

本虚拟机里的压测直接印证了这点：Valkey 从重建后的 351 MiB 涨到 4,500 循环/秒跑两分钟后的约 950 MiB，
多出来的每一个字节都是在等它那一个小时的去重标记和已结束租约哈希。

### 租约结束的优化（历史，发生在「优化前」那一列之前）

保留这一节，是因为它解释了上面每张表所对比的那个基线，也因为它描述的成本形状正是下一个杠杆要攻击的。

`release.lua`/`reap.lua` 过去在每次租约结束时，会去恢复该身份在其客户端**每一个端点组**里的就绪队列
分值 —— 因为租约期间可能有别的组把分值推到了独占租约的到期时刻。这意味着每次租约结束、每个组一次
`ZSCORE`（命中时再加一次 `HGET`），不管有没有组真的推过：在 50 组数据集上正好是**每次 acquire 50.00 次
`ZSCORE`**（在其他条件相同的 5 组站点上是 5.00），`release.lua` 152.6 µs 对 5 组站点的 51.3 µs ——
**每个端点组 +2.25 µs**，使它成为系统里最贵的脚本。

修复方式是把这个事实记在身份上而不是每次去重新发现：某次 acquire 因为别的组的独占租约而推走一个身份时，
在身份哈希上写 `xg`，租约结束时只遍历 `xg` 点名的那些组。在 50 组数据集上、1,000 循环/秒下实测：

| 1,000 循环/秒、50 组下的指标 | 修复前 | 修复后 |
| --- | ---: | ---: |
| Valkey 命令/秒 | 95,793 | 49,491 |
| Valkey CPU（commandstats） | 0.34 核 | 0.26 核 |
| `EVALSHA` 均值 | 54.9 µs | 43.7 µs |
| 每次 acquire 的 `ZSCORE` | 50.00 | 0 |
| 可持续 acquire→report 速率（双实例） | 1,678/s | 3,000/s |

回归测试：`internal/scheduler/lease_test.go:TestExclusivePushMarkerScopesTheRestore`。
worker track 后来把 `xg` 进一步收窄，从「所有组」变成「实际推过的那些组」。右列的 43.7 µs `EVALSHA`
均值就是[场景 A](#单副本--seed-策略max_concurrent_leases-1--单实例数字) 那张表里的「优化前」数字；
现在是 24.5 µs。

### 哪些是环境限制，哪些不是

| 观察 | 环境还是产品？ |
| --- | --- |
| 单实例停在 4,500–5,500 循环/秒 | **产品**：拥塞循环在 Valkey 主线程跑满之前就启动了（脚本本身允许每线程 5,940 循环/秒） |
| 两个实例不如一个 | **产品**：每实例对单个 Redis 的并发没有上限，见上文 |
| 失败的 acquire 约是成功的 5 倍 | **产品**：本文所有崩溃的放大器 |
| 服务实例从未超过 16 核中的 1.7 核 | 余量 |
| 相同参数的两轮，一轮干净一轮崩溃 | **环境**，但触发器与产品相关且已修复：笔记本虚拟机里每 52 秒一次的 AOF 重写 fork。已在 `deploy/compose` 调优 |
| 5,000/s 时 ClickHouse 被 OOM kill 并冻结虚拟机 | **环境**（7.75 GiB 虚拟机同时跑 k6、三个 ClickHouse 和一套无关的应用栈），通过限制缓存解决 |
| 冷（刚 seed 或刚 rebuild）站点在 2,500 循环/秒就崩 | **产品**，程度较轻：统一的初始就绪队列分值让 50 个组完全相关。约一百万次 acquire 的流量即可消除 |
| 1 万 watcher 经 Caddy 失败 | **环境 / LB 配置**，不是服务端 |
| 高速率轮次里 k6 和 Caddy 各占约 1 核 | **环境**：压测客户端和被测系统共享虚拟机 |

## 调优建议

**副本数。** 在单个 Redis 是瓶颈时，增加服务实例并不会增加 Acquire 吞吐 —— 而且如上实测，在做出
每实例准入控制之前，两个副本的总吞吐还不如一个。副本用来提高可用性、提高长轮询容量（每实例 1 万个
watcher）、提高上报*处理*能力（每个实例拥有 16 个 Stream 分片中的一份）；Acquire 吞吐靠扩 Redis，
以及把每个实例的实际速率压在它的拐点以下。

**Redis。** 所有键都按站点（`{s<key>}`）或上报分片（`{r<n>}`）打了 hash tag，`SPINNERET_REDIS_ADDRS`
会让客户端进入 cluster 模式，所以扩展路径就是 Redis Cluster（或按站点分组一 Redis 一组）。容量预算：
独占租约下大约**每 4,500 acquire→report 循环/秒一个 Redis 主节点**，`max_concurrent_leases: 4` 时
**5,500/s** —— 脚本成本允许每线程 5,940 循环/秒，差额是你应该留着的拥塞余量。内存按
[Redis 容量测算](#redis-容量测算)的公式。

**Valkey 配置。** 用 `deploy/compose/docker-compose.yml` 里的这套：`io-threads 4`（长尾）、
`--save ""` 配 `appendonly yes`（不要为两套持久化机制各付一次钱）、
`auto-aof-rewrite-percentage 300` / `auto-aof-rewrite-min-size 1gb`（短命键为主的负载否则会持续重写
AOF）。见 [Valkey 配置](#valkey-配置)。

**`SPINNERET_REPORT_DEDUP_TTL`。** 默认 1 小时，每条上报 80 B，*并且*让每个已结束租约哈希
（约 290 B）也多活一小时 —— 在 4,500 循环/秒下是 5.4 GiB 的 Redis。节点重试是以秒计而不是以小时计的：
5–15 分钟通常就够，能把这部分内存降到 1/4–1/12。compose 文件已经把它暴露为
`SPINNERET_REPORT_DEDUP_TTL`。

**`SPINNERET_STREAM_MAXLEN`。** 分片所有者会把自己的 Stream 裁到消费组位置，所以每分片 100 万的默认值
是「worker 排不完的积压」的上限，而不是常驻缓冲。按你想扛过的 worker 停机时长来定：
`速率 × 容忍秒数 / 分片数`。20,000 条/秒、容忍 60 秒积压就是每分片 75,000；Redis 内存紧张时调低，
因为超过上限的条目会被静默丢弃。

**`max_concurrent_leases`。** 仍然是站点级最大的杠杆，而且现在在优化后的代码上量化了：`1`（独占）
单实例可持续 4,500 循环/秒，`4` 可持续 5,500 —— 而且 `4` 的降级要优雅得多，因为被租出的身份不再需要
从该客户端所有其他组的就绪队列里被推走，而那正是崩溃的放大器。只在站点确实要求「同一身份同时只能有一个
请求」时才用 `1`。

**端点组。** 每个身份的状态是按端点组存的：每（身份 × 组）约 65 B 就绪队列加约 71 B 健康状态。
按*策略*需要来划分端点组，而不是按 URL 数量 —— 50 组 × 10 万身份已经是 500 万条就绪队列项（约 310 MiB）。

**`candidate_sample`。** 建议不变，但理由变了。scheduler track 让 acquire 改为按需读取候选状态，
而不是一次性批量加载整个样本，于是 `acquire.lua` 对 K 的依赖从 K=1 到 K=32 之间约 13 µs 降到约 4 µs
（`test/perf/README.md`）。现在把它从 32 调低几乎没有收益，而在争用下还会多几轮。保持默认值。

**数据库连接池。** 每实例 `SPINNERET_DATABASE_MAX_CONNS=32` 从来不是因素：所有场景里 PostgreSQL 的
CPU 都在 3% 以下，因为热路径只走 Redis，PostgreSQL 只看到批量写入。

**ClickHouse。** 在共享主机上限制它的缓存（`config/clickhouse-limits.xml`）；默认 5 GiB 的 mark cache
是给专用分析机准备的。`max_server_memory_usage` 要设在进程空载 RSS（alpine 镜像约 1.2 GiB）**之上** ——
设在它之下会让插入失败并卡住上报 worker。

**负载均衡器。** 见[场景 D](#场景-d--并发配置-watcher)：上游很少时，`max_fails 1` + `fail_duration 5s`
会让一次慢拨号变成全站不可用。

**性能剖析。** 设置 `SPINNERET_PPROF_ADDR`（例如 `127.0.0.1:6060`）可在独立监听端口上提供
`/debug/pprof/`。它默认关闭，也从不挂在 API 或指标监听端口上，因为这些端点没有鉴权、会暴露堆内容和
goroutine 栈 —— 永远不要把这个端口对外开放。

## 复现步骤

前置条件：`deploy/compose` 的全栈已经跑起来（先 `scripts/compose-init.sh`，再
`docker compose … up -d --wait`），宿主机上有 Python 3。

```bash
cd <repo>
COMPOSE="docker compose -f deploy/compose/docker-compose.yml"

# 1. 播种 §18.4 数据集并拿到节点令牌（JSON 打到 stdout）。
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  seed --site loadtest --identities 100000 --groups 50 > /tmp/seed.json
export LOADTEST_TOKEN=$(python3 -c "import json;print(json.load(open('/tmp/seed.json'))['token'])")

# 2. 单实例数字。SPINNERET_REPLICAS 必须对*每一条* compose 命令都导出，
#    否则下一条命令会把服务缩回 .env 里的值。
export SPINNERET_REPLICAS=1
$COMPOSE up -d --wait

# 3. 预热站点（见「刚 seed 出来的站点必须先预热再测」）。步骤之间要沉降：
#    过载结束的一轮会留下 60 秒租约和被推后的就绪分值。
for r in 1500 2500 3500 4000; do
  MID_STATS=0 test/load/run.sh "warm-$r" acquire_report.js ACQUIRE_RATE=$r DURATION=45s
  sleep 70
done

# 4. 单实例 acquire->report 拐点（2 分钟，头条数据）。
MID_AFTER=70 MID_STATS=0 test/load/run.sh acq1-4500 acquire_report.js ACQUIRE_RATE=4500 DURATION=2m

# 5. 只做 Acquire。先 `tune.py --max-concurrent-leases 4`，
#    否则没人释放的租约会堆满整个 TTL。
python3 test/load/tune.py --max-concurrent-leases 4
for r in 4000 5000 6000 8000; do
  MID_STATS=0 test/load/run.sh "acqonly-$r" acquire_report.js \
    ACQUIRE_RATE=$r DURATION=45s REPORT_MODE=none
  sleep 70
done
python3 test/load/tune.py --restore

# 6. 上报接收。
MID_AFTER=30 MID_STATS=0 test/load/run.sh ing1-20k report_ingest.js \
  BATCH=200 REPORT_RATE=100 DURATION=60s LEASES=400

# 7. 并发 watcher（直连实例，见场景 D）。
MID_AFTER=45 test/load/run.sh watch1-10000 watch_config.js \
  WATCHERS=10000 DURATION=90s RAMP_S=25 SPINNERET_URL=http://spinneret:8080

# 8. 配置变更感知（发布方与 watcher 在同一进程）。
python3 test/load/config_awareness.py --watchers 200 --publishes 6 --interval 3

# 9. 逐脚本成本：在约 1,000 循环/秒的负载下采样全部命令 0.3 秒，
#    再按脚本 SHA 归类 EVALSHA 条目（「时间都花在哪」那张表）。
MID_STATS=0 test/load/run.sh slowlog acquire_report.js ACQUIRE_RATE=1000 DURATION=60s &
sleep 40 && $COMPOSE exec -T valkey valkey-cli config set slowlog-log-slower-than 0
# ... 0.3 秒后：slowlog get，然后把 slowlog-log-slower-than 恢复成 10000。

# 10. 恢复常规拓扑。
unset SPINNERET_REPLICAS
$COMPOSE up -d --wait
```

每一轮都会在 `.loadtest/<name>/` 下留下 `before.json`、`mid.json`、`after.json`、`delta.json`、
`gauges.json`、`k6.txt` 和 `k6.json`。`test/load/metrics.py diff a.json b.json` 可以打印任意两个快照
之间的服务端增量，包括各 Valkey 命令的 CPU 归因。

其他工具：

* `test/load/tune.py --max-concurrent-leases 4` 用不同取值重新发布 seed 的轮换策略
  （`--restore` 恢复 seed 的值）。
* `MID_STATS=0` 去掉中途快照里的 `docker stats` 部分；靠近拐点的每一轮都应该加上（见[测量方法](#测量方法)）。

### 清理

压测会留下与流量成正比的状态（去重标记、已结束租约哈希、Stream 条目），它们会在
`SPINNERET_REPORT_DEDUP_TTL` 内自行过期，另外还有不会过期的 ClickHouse 行。在小机器上值得在两轮之间
回收一下：

```bash
# 裁剪上报 Stream（仅限测试数据；会丢掉未处理的积压）。
$COMPOSE exec -T valkey valkey-cli eval \
  "local n=0 for i=0,15 do n=n+redis.call('XTRIM','sp:{r'..i..'}:stream','MAXLEN',5000) end return n" 0
# 删除上报去重标记。
$COMPOSE exec -T valkey sh -c \
  "valkey-cli --scan --pattern 'sp:{r*}:dd:*' --count 5000 > /tmp/k; xargs -a /tmp/k -n 1000 valkey-cli unlink"
# 压测产生的分析行：跑一下午大约 2 GiB。
$COMPOSE exec -T clickhouse clickhouse-client --user spinneret --password "$CLICKHOUSE_PASSWORD" \
  -q "TRUNCATE TABLE spinneret.report_events; TRUNCATE TABLE spinneret.lease_events"
```

> **永远不要 unlink `sp:{<site>}:ls:*`。** 租约哈希正是租约结束时用来把身份的 `al`（活跃租约数）
> 减回去的东西。删掉仍然存活的租约哈希会让这个计数器泄漏，该身份从此永远不可用 —— 在这里干过一次，
> 结果 10 万个身份里有 97,520 个被永久标记为已租出，之后每一轮都崩进 `resource_exhausted`。
> 裁剪一个还存有未处理 `release: true` 上报的 Stream 也是同样的后果。真出了这种事，修复办法是把整个
> 站点前缀 unlink 掉再重建：
>
> ```bash
> $COMPOSE exec -T valkey sh -c \
>   "valkey-cli --scan --pattern 'sp:{<site-tag>}:*' --count 5000 > /tmp/k; xargs -a /tmp/k -n 1000 valkey-cli unlink"
> $COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate rebuild --site loadtest
> ```
>
> 注意 `spnr rebuild --site` 本身**不会**重置运行时计数器（这是设计使然 —— 重建不能把存活的租约弄丢），
> 清掉它们的是那次 unlink。另外重建出来的站点是冷的，测之前要再预热一遍。

跳过清理的后果，就是把一次本来干净的重跑变成崩溃：7.75 GiB 的虚拟机里堆积的键和 Valkey 内存，
最终 Valkey 在压测中途被 OOM kill 并重启。
