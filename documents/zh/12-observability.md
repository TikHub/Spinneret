# 可观测性与告警

**如何看清 Spinneret 正在做什么：控制台仪表盘、请求明细、Prometheus 指标、健康检查端点，以及在客户发现问题之前先通知你的告警渠道。**

[English](../en/12-observability.md)

---

## 目录

- [数据从哪里来](#数据从哪里来)
- [概览](#概览)
- [冷却热力图](#冷却热力图)
- [请求明细](#请求明细)
- [风险事件](#风险事件)
- [熔断器](#熔断器)
- [通知：渠道、规则与历史](#通知渠道规则与历史)
- [Prometheus 指标](#prometheus-指标)
- [告警规则起步模板](#告警规则起步模板)
- [控制台事件流](#控制台事件流)
- [健康检查端点](#健康检查端点)
- [出问题时先看什么](#出问题时先看什么)
- [下一步](#下一步)

---

## 数据从哪里来

Spinneret 的历史数据分布在三套存储里——Valkey/Redis、PostgreSQL 和 ClickHouse——控制台的每个页面只读下表中的一到两项。搞清楚谁读哪一项，就能理解为什么有的页面是瞬时的，而有的页面带查询限制。

| 存储 | 存了什么 | 谁在读 | 保留时长 |
| --- | --- | --- | --- |
| Valkey / Redis（热状态） | 就绪队列、健康分、冷却、封禁、熔断器状态、上报流积压 | 概览的实时计数、热力图、熔断器 | 仅工作集，可重建 |
| PostgreSQL 分钟聚合 | `acquire_stats_minutely`、`outcome_stats_minutely`、`node_stats_minutely` | 概览的速率与占比、趋势图、节点表 | `SPINNERET_RETENTION_MINUTE_STATS`（默认 30 天） |
| PostgreSQL 小时聚合 | `identity_stats_hourly`——按身份、端点组、判定结果 | 暂时没人读：表在写，但没有任何控制台页面或 RPC 读它 | `SPINNERET_RETENTION_HOUR_STATS`（默认 180 天） |
| PostgreSQL `risk_events` | 每条非成功上报一行 | 风险事件页 | `SPINNERET_RETENTION_RISK_EVENTS`（默认 30 天） |
| ClickHouse `report_events` | 每条已处理上报一行，包含成功 | 请求明细页 | `SPINNERET_CLICKHOUSE_TTL_DAYS`（默认 90） |
| ClickHouse `lease_events` | 每个租约生命周期事件一行 | 暂时没人读：可用于临时 SQL 查询 | `SPINNERET_CLICKHOUSE_TTL_DAYS`（默认 90） |

`identity_stats_hourly` 和 `lease_events` 目前确实是只写的：它们一直保持最新，这样将来某个页面需要按身份、按租约的历史时数据已经在那里；但就今天而言，把 `SPINNERET_RETENTION_HOUR_STATS` 调短不会让控制台少任何东西。

上面这些控制台页面由 `DashboardService` 提供（`dashboard:read`），例外是熔断器页面（`BreakerAdminService`，`breaker:read`）和通知页面（`NotificationAdminService`，`notify:read`）。三者都只返回调用者有权读取的站点的数据。ClickHouse 是可选的：不配置 `SPINNERET_CLICKHOUSE_URL` 时请求明细页被禁用，其余页面照常工作。

Prometheus 指标是另一条独立的路径：它描述的是**服务进程**而不是命名空间，也是最适合做告警的地方。上面提到的所有变量见[配置参考](./03-configuration.md)。

---

## 概览

控制台路径 `/`，导航项 **概览**。需要 `dashboard:read`；“打开的熔断器”卡片额外需要 `breaker:read`。页面上每个查询每 10 秒刷新一次。

![概览](../images/overview-zh.png)

### 统计窗口选择器

页头的选择器提供 **1m**、**5m**、**15m**、**1h**，默认 5m。它只影响顶部指标卡和站点卡里的速率与占比。速率统计的是当前时刻之前**已完整结束**的分钟——当前这个尚未写完的分钟被排除在外，所以 1m 窗口永远是一个完整的分钟，而不是半个。两张趋势图和节点表始终覆盖最近 1 小时，与选择器无关。

### 顶部指标卡

| 指标卡 | 数值 | 下方提示 |
| --- | --- | --- |
| 可用身份 | 当前可租借的身份数，按站点求和 | — |
| 获取 QPS | 窗口内每秒 `Acquire` 调用数 | 获取失败率 |
| 上报 QPS | 窗口内每秒上报数 | 待处理上报（上报流积压） |
| 成功率 | 判定为 `success` 的上报占比 | 未知率 |
| 风险率 | 风险判定结果的上报占比 | — |
| 打开的熔断器 | 处于熔断状态的端点组数 | 半开的熔断器数量 |

读这些数字时需要注意的细节：

- **可用身份**不是“存在的身份数”。服务端按客户端取该客户端各端点组中最大的就绪数量，再按客户端求和。一个身份在某个端点组冷却、在另一个端点组空闲，仍然只算一次。
- **风险率**统计四种风险判定结果：`rate_limited`、`captcha`、`forbidden`、`banned`。它刻意不包含 `network_error`、`proxy_error` 和 `target_error`——这些通常意味着传输或目标站问题，而不是被识别。
- **获取失败率**统计 `exhausted`、`circuit_open`、`site_paused`、`no_proxy` 四种获取结果占全部获取尝试的比例。`error` 结果不计入这里的失败。
- **待处理上报**是集群级而非命名空间级的：它是所有上报流分片上 Worker 消费组的 pending 加 lag 之和（若某分片还没有 Worker 消费过，则用流长度代替）。
- 指标卡会变色：有上报流量时成功率低于 80 % 变琥珀色，风险率达到 10 % 变红，打开的熔断器大于 0 变红。

### 趋势图

两张图，都覆盖最近 1 小时，都来自分钟聚合：

- **获取速率（最近 1 小时）**—— `acquire_rate` 指标，每秒获取数。
- **成功率与风险率（最近 1 小时）**—— `success_ratio` 与 `risk_ratio` 指标，取值 0–1。

底层的 `GetTimeSeries` RPC 的能力比概览用到的更强。它支持指标 `acquire_rate`、`acquire_results`、`outcomes`、`success_ratio`、`risk_ratio`、`latency_avg`；步长 `1m`、`5m`、`15m`、`1h`、`6h`、`1d`，留空则自动选择；时间范围最长 31 天，默认最近 1 小时；以及可选的 `site`、`client`（需要同时指定站点）和 `endpoint_group_id` 过滤。若某步长会产生超过 720 个桶，会自动提升到下一个受支持的步长；连 `1d` 都放不下时调用返回 `invalid_argument`。没有数据的桶返回 0，而不是省略。

### 站点卡片

每个可读站点一张卡片，按站点名排序。每张卡片包含：

- 展示名及其下方的站点名，以及 **已暂停**、熔断器打开、半开的徽标；
- **可用**（该站点可租借的身份数），旁边是 **获取** 与 **上报** 速率；
- 四格数据条：**成功**、**风险**、**未知**、**获取失败**，颜色阈值与顶部指标卡一致（获取失败率从 5 % 起变琥珀色）；
- 按生命周期状态堆叠的身份柱状条——可用、待验证、已过期、已隔离、已封禁、已禁用、已归档——图例中带数量；
- 当该站点有端点组低于低水位时，底部出现一行提示。

状态条是判断**哪个站点在恶化**的最快方式：已过期段变大说明载荷需要刷新，已封禁段变大说明目标站在反制。

### 下方两张卡片

**打开的熔断器**最多列出 20 个处于 `open` 或 `half_open` 的熔断器，包含站点、端点组、状态、打开至和原因。**低水位告警**列出所有可用身份低于配置低水位的端点组，包含客户端、可用数量和阈值。没有内容时两者都显示空状态。

### 节点（最近 1 小时）

来自 `node_stats_minutely` 的按节点计数，按获取次数倒序，最多 1000 个节点：**获取次数**、**上报次数**、**未上报租约**（租约过期但从未上报）、**被拒绝** 和 **未上报比例**（`abandoned / acquires`）。节点名来自节点发送的 `X-Spinneret-Node` 头；没有该头的上报归入 `_`。

某个节点的未上报比例偏高，就是该节点崩溃、被杀掉或忘记调用 `Report` 的信号。这是命名空间级数据，因此该页面要求命名空间范围的 dashboard 访问权限。

---

## 冷却热力图

控制台路径 `/heatmap`，导航项 **冷却热力图**。需要 `dashboard:read`。每 10 秒刷新一次。

![冷却热力图](../images/heatmap.png)

热力图一次只看一个站点的一个客户端：行是身份，列是该客户端按名称排序的端点组，每个单元格是该身份在该端点组中的热状态。

| 控件 | 取值 |
| --- | --- |
| 客户端 | 站点的某个客户端类型（必选） |
| 指标 | **剩余冷却** 或 **健康分**——两个值在数据中始终都有，指标只决定单元格用哪个值上色 |
| 状态 | `pending`、`active`、`expired`、`banned`、`quarantined`、`disabled`、`retired` 的任意组合。控制台默认选中「可用 + 待验证」；它的「除已归档外全部」也正是请求未指定状态时服务端使用的默认值 |
| 每页身份数 | 100、250 或 500（服务端默认 100，上限 500） |
| 视图 | **图表** 或 **表格**——表格视图是同样的数据的文字形式，便于读屏软件和复制 |

每个单元格带有衰减后的健康分（0–100）、以毫秒计的剩余冷却，以及该身份此刻能否在该端点组被租借。剩余冷却取端点级冷却、身份级冷却、账号级冷却和封禁四者的最大值；永久封禁上报为 int64 最大值，界面显示为 **永久封禁** 而不是一个时长。

矩阵是稀疏的。当身份在该端点组没有健康记录、没有冷却也没有封禁，**且**它的可用性与其生命周期状态所隐含的一致（`pending` 和 `active` 可租借，其余不可）时，服务端会省略该单元格。被省略的单元格按端点组的基线分绘制，标注为 **无热状态（基线分）**——那是一个还没有人碰过的健康身份，不是缺失数据。

行标签优先取身份的账号引用，其次是地域，最后是身份 ID 的后 8 位字符。点击单元格打开该身份的详情页。翻页按身份 ID 进行，匹配身份的总数显示在网格上方。

热力图用一次流水线化的 Redis 往返读取全部数据，所以 500 行也很快——但它展示的是**此刻**，没有历史。要看历史请用请求明细。

---

## 请求明细

控制台路径 `/requests`，导航项 **请求明细**。需要 `dashboard:read`。开启自动刷新时第一页每 5 秒刷新；打开详情面板时暂停刷新。

![请求明细](../images/requests.png)

### 存了什么

每条已处理的上报——包括成功的——都会写入 ClickHouse 表 `report_events`，一条上报一行，列如下：

| 分组 | 列 |
| --- | --- |
| 时间 | `event_time`（请求结束时刻）、`received_at`（服务端接收上报时刻）、`started_at` |
| 范围 | `tenant_id`、`namespace_id`、`site_id`、`site`、`client`、`endpoint_group` |
| 主体 | `identity_id`、`identity_type`、`proxy_id`、`lease_id`、`report_id`、`node`、`token_id` |
| 请求 | `uri`、`method`、`http_status`（0 = 无响应）、`business_code`、`error_kind`、`markers` |
| 判定 | `outcome`、`outcome_hint`（节点给出的提示）、`blame`（`none`/`identity`/`proxy`/`both`）、`rule`（命中的信号规则） |
| 开销 | `latency_ms`、`response_bytes` |
| 标志 | `suppressed`、`late`、`probe` |

该表是按 `event_time` 天分区的 MergeTree，排序键为 `(namespace_id, site, endpoint_group, event_time)`，并在 `identity_id`、`proxy_id`、`lease_id`、`report_id` 上建有 bloom filter 跳数索引。正是这个排序键决定了：按站点和端点组过滤的窄窗口查询很便宜，而只按节点名过滤的 7 天查询很贵。

数据按 `TTL event_time + SPINNERET_CLICKHOUSE_TTL_DAYS 天`（默认 90）过期，并设置 `ttl_only_drop_parts = 1`，因此过期时整块丢弃天分区，而不是重写数据块。

### ClickHouse 的第二张表：lease_events

`report_events` 不是唯一一张被管理的表。Spinneret 还会创建并写入 `lease_events`，每个租约生命周期事件一行，列包含同样的范围与主体字段（`tenant_id`、`namespace_id`、`site_id`、`site`、`client`、`endpoint_group`、`identity_id`、`proxy_id`、`lease_id`、`node`、`token_id`），外加 `event`、`result`、`duration_us`、`probe` 和 `sticky`。它同样按 `event_time` 天分区，排序键同样是 `(namespace_id, site, endpoint_group, event_time)`，在 `identity_id` 和 `lease_id` 上建有 bloom filter 索引，并受同一个 `SPINNERET_CLICKHOUSE_TTL_DAYS` 控制过期。

目前没有任何控制台页面或 RPC 读它。但它有两点值得知道：它占用你的 ClickHouse 磁盘预算；而且它是事后唯一能查询租约时长以及会话粘性／探测决策的地方——直接用 SQL 查你已经在跑的那个 ClickHouse。

### 筛选条件

所有筛选条件都会同步到 URL，所以一个视图就是一条可分享的链接。

| 筛选项 | 说明 |
| --- | --- |
| 时间范围 | 预设 **最近 15 分钟**、**最近 1 小时**、**最近 6 小时**、**最近 24 小时**、**最近 7 天**，或自定义范围。默认最近 1 小时，最长跨度 7 天 |
| 站点 | 一个站点名 |
| 端点组 | 需要先选站点；该端点组必须属于该站点与客户端 |
| 结果 | 十二种判定结果的任意子集，包含 `success` |
| 身份 ID / 代理 ID / 节点 | 精确匹配 |
| HTTP 状态码 | 精确匹配；`0` 表示“无响应”；取值 0–999 |
| 最小耗时（毫秒） | 只保留耗时不低于该值的事件 |

RPC 还接受 `lease_id` 和 `report_id`（`QueryRequestEventsRequest.lease_id`、`report_id`）——当你从节点日志里拿到一个租约、想找出它产生的那条上报时很有用。

翻页使用 `(event_time, report_id, lease_id)` 倒序的 keyset 分页，默认每页 50 行，最多 500 行。预设时间范围会锚定到请求第一页的那一刻，因此同一次查询的每一页覆盖同一个窗口，不会边翻边滑动。

### 汇总条

设置 `include_summary`（控制台只在第一页设置）会附带一份对**全部匹配事件**的聚合，而不只是当前页：总数、按判定结果的数量、平均耗时，以及第 50、95、99 百分位耗时（近似分位数）。控制台在你往后翻页时保持这条汇总可见，并用环形图展示结果分布。

### 详情面板

点击一行打开侧边面板，展示完整事件：站点与端点组、身份（带跳转到身份页的链接）与身份类型、代理、节点、令牌、租约与上报 ID、URI 与方法、HTTP 状态码、业务码、错误类型、标记、判定结果与结果提示、归因、命中规则、耗时、响应大小、开始／结束／接收时间，以及三个标志：

- **已抑制**——熔断器处于打开状态，未更新健康度也未执行动作；
- **迟到**——上报在租约结束之后才到达；
- **探测**——该租约是熔断器半开状态下的探测请求。

### 查询限制，以及过宽查询如何干净地失败

单次控制台查询被刻意限制住，以免它把上报写入饿死。每次请求明细查询都带这些设置运行：`max_execution_time` 等于服务端的 ClickHouse 超时（30 秒）、`max_memory_usage` 为 512 MiB、`max_threads = 2`、`optimize_read_in_order = 1`。

当查询撞上其中某条限制时，失败会被翻译成可操作的客户端错误而不是内部错误，并带上 `Spinneret-Reason` 头：

| 情况 | 错误码 | reason | 消息 |
| --- | --- | --- | --- |
| 内存超限、行数或字节数超限 | `resource_exhausted` | `query_too_large` | “the analytics query needs more resources than ClickHouse allows: narrow the time range or add filters” |
| ClickHouse 或客户端超时、查询被取消 | `deadline_exceeded` | `query_timeout` | “the analytics query timed out: narrow the time range or add filters” |

两者在实践中含义相同：加一个站点或端点组过滤，或者缩短时间范围。其他 ClickHouse 错误仍然是内部错误，并带原因写入日志。

### 未配置 ClickHouse 时

`QueryRequestEvents` 返回 `unavailable`，reason 为 `failed_precondition`，控制台会把表格替换成一段说明、需要设置的变量——`SPINNERET_CLICKHOUSE_URL=clickhouse://user:password@clickhouse:9000/spinneret`——以及一个指向风险事件页的链接，后者仍然可用。

---

## 风险事件

控制台路径 `/risk-events`，导航项 **风险事件**。需要 `dashboard:read`。开启自动刷新时第一页每 5 秒刷新。

风险事件是同一件事在 PostgreSQL 一侧的记录：每条判定结果**不是** `success` 的上报都会在 `risk_events` 中留下一行，与 ClickHouse 无关。如果你不部署 ClickHouse，这里就是你的请求历史。

| 属性 | 取值 |
| --- | --- |
| 默认时间范围 | 最近 24 小时 |
| 最大时间范围 | 31 天 |
| 每页行数 | 默认 50，最多 500 |
| 翻页 | 按 `(created_at, id)` 倒序的 keyset 分页 |
| 保留时长 | `SPINNERET_RETENTION_RISK_EVENTS`，默认 30 天，通过丢弃天分区实现 |
| 筛选项 | 站点、端点组（需要先选站点）、单个非成功判定结果、身份 ID、代理 ID、节点、时间范围 |

列：时间、站点、端点组、结果、归因、规则、HTTP 状态码、业务码、错误类型、标记、耗时、身份、代理、节点、租约 ID、上报 ID。展开一行还会显示请求各时间点、响应大小、客户端、事件 ID（`rsk_…`）、令牌和端点组 ID。

注意这里与请求明细的不对称：本页的结果筛选拒绝 `success`，因为成功的上报按定义就不是风险事件。

---

## 熔断器

控制台路径 `/breakers`，导航项 **熔断器**。读取需要 `breaker:read`。每 5 秒刷新。

![熔断器](../images/breakers.png)

三个标签页：

- **熔断器**——每个端点组一行：状态（`closed`、`half_open`、`open`）、打开截止、连续打开次数、窗口统计（`N 次上报 · 成功 N · 风控 N`）、探测（`成功 N/N · 已发放 N`）、原因、最近打开／关闭时间，以及生效的策略（或 **内置默认策略**）。筛选项：站点、客户端、状态、端点组。
- **站点开关**——每个站点的暂停开关，含客户端、端点组、状态与暂停原因。
- **历史**——熔断器的状态变化，含时间、端点组、状态变化、触发方式（`自动`、`人工`、`探测`、`站点开关`）、原因、打开截止、操作者，以及状态变化时刻的指标。时间范围：最近 1 小时、6 小时、24 小时、7 天、30 天、全部时间。

从可观测性角度，有两个值得形成条件反射的判断：熔断器处于 `open` 时，该端点组的每次 `Acquire` 都以 `circuit_open` 失败，而已经发放出去的租约仍可上报，只是这些上报带 `suppressed` 标志；熔断器在 `half_open` 与 `open` 之间反复抖动，会体现在历史标签页里，并表现为反复的 `breaker_reopened` 告警。熔断机制本身、策略字段以及手动打开／关闭操作见[策略](./08-policies.md)。

---

## 通知：渠道、规则与历史

控制台路径 `/notifications`，导航项 **通知**。读取需要 `notify:read`，新建与编辑需要 `notify:write`。每 5 秒刷新。

渠道属于**租户**，可以再绑定到某一个命名空间，还可以进一步限制到该命名空间下的若干站点。

### 新建渠道

点 **新建渠道** 打开 **新建通知渠道** 表单。新建与编辑渠道需要 `notify:write`。

| 字段 | 填什么 |
| --- | --- |
| **范围** | 租户级，或某一个命名空间。创建后不可修改——*范围创建后不可修改* |
| **类型** | `webhook`、`feishu`、`dingtalk`、`wecom` 或 `telegram`。同样创建后不可修改 |
| **名称** | 1–64 个字符，首尾不能有空格 |
| 该类型的配置字段 | 下表列出的字段；表单只显示所选类型的那几个 |
| **事件类型** | 至少选一种告警类型——空列表会被拒绝 |
| **站点** | 可选，仅命名空间范围的渠道可用，最多 500 个站点。留空表示全部站点 |
| **最低级别** | `info`、`warning` 或 `critical`。留空等于 `warning` |
| **启用** | 停用的渠道除测试告警外什么都收不到 |

范围和类型不可修改，因为两者共同决定了已存储的配置该如何解释；要改其中任何一个，就新建一个渠道再删掉旧的。其余字段——名称、配置、事件类型、站点、最低级别、启用——之后都还能改。

渠道建好后立刻点一次 **发送测试告警**：这是唯一能确认地址、令牌和签名真的能用的办法。

### 渠道类型

已实现五种类型。每种只接受自己的配置字段，其余字段一律拒绝。

| 类型 | 配置字段 | 投递方式 |
| --- | --- | --- |
| `webhook` | `url`（必填）、`secret`、`headers` | 每条告警一次 JSON `POST` |
| `feishu` | `webhook_url`（必填）、`secret` | 文本消息；配置 secret 后在负载中附加 `timestamp` 与 `sign` |
| `dingtalk` | `webhook_url`（必填）、`secret` | markdown 消息；配置 secret 后在 URL 上附加 `timestamp` 与 `sign` 查询参数 |
| `wecom` | `webhook_url`（必填） | markdown 消息 |
| `telegram` | `bot_token`（必填）、`chat_id`（必填）、`api_base` | 以 HTML 文本调用 `sendMessage`；`api_base` 默认 `https://api.telegram.org` |

需要了解的校验规则：URL 必须是绝对的 `http`/`https`；机器人令牌必须形如 `<数字>:<密钥>`；额外请求头最多 20 个且名称必须合法，其中 `Host`、`Content-Length`、`Content-Type`、`Transfer-Encoding`、`Connection`、`Te`、`Upgrade`、`Trailer`、`X-Spinneret-Timestamp`、`X-Spinneret-Signature` 不允许覆盖。

配置在入库前用保管库密码器加密，并且**始终**以脱敏形式返回：URL 保留协议和主机、其余部分脱敏（Webhook 地址常在查询串里带令牌），而密钥、机器人令牌和疑似凭证的请求头（名称中含 `authorization`、`token`、`key`、`secret`、`password`、`cookie`、`signature`）返回为 `••••` 加最后四位字符。把脱敏值原样回传会保留已存储的密钥——但对于会被原样转发到目标的凭证（Webhook 的凭证请求头、Telegram 机器人令牌），只有在目标地址未变时才会恢复。一旦改了 URL，就必须重新完整提供这些值；否则一个能编辑渠道但看不到密钥的人，就能把密钥转发到自己指定的服务器。

### Webhook 负载与签名

`webhook` 渠道每条告警收到一次 `POST`，带 `Content-Type: application/json; charset=utf-8`、`User-Agent: Spinneret-Notify/<version>`、你配置的请求头，以及如下请求体：

```json
{
  "id": "alt_...",
  "kind": "breaker_opened",
  "severity": "critical",
  "title": "...",
  "message": "...",
  "tenant": "acme",
  "namespace": "prod",
  "site": "example-site",
  "details": { "site": "example-site", "endpoint_group": "search" },
  "created_at": "2026-01-01T12:00:00Z"
}
```

配置了 `secret` 时会额外附加两个请求头：

```text
X-Spinneret-Timestamp: 1767268800
X-Spinneret-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + 原始请求体)>
```

接收端对原始请求体重新计算 HMAC 即可校验投递来源。任何非 2xx 响应都算失败；重定向不跟随，同样算失败。300–499 中除 408 和 429 之外的状态码被视为永久失败，不会重试。

### 路由：哪条告警发给哪个渠道

一个渠道会收到本租户的某条告警，当且仅当以下条件**全部**成立：渠道已启用、订阅了该告警类型、告警级别达到渠道的最低级别（`info` < `warning` < `critical`，默认最低级别为 `warning`），且渠道范围覆盖该告警：

- 租户级渠道收到本租户的全部告警；
- 命名空间渠道收到本命名空间的告警**以及**租户级告警（例如 `report_backlog`）；
- 限定站点的渠道收到其站点的告警，外加不涉及具体站点的告警。

### 告警类型与触发条件

| 类型 | 级别 | 触发条件 | 范围 |
| --- | --- | --- | --- |
| `breaker_opened` | critical | 任何进入 `open` 的状态变化，只要来源不是 `half_open` | 站点 |
| `breaker_reopened` | critical | 从 `half_open` 变为 `open`（探测失败） | 站点 |
| `breaker_closed` | info | 从 `open` 或 `half_open` 变为 `closed` | 站点 |
| `identity_low_watermark` | warning | 端点组当前可用身份数低于其低水位 | 站点 |
| `proxy_low_watermark` | warning | 命名空间至少有 5 个代理，且可用代理占「可用+不可用」的比例低于 20 % | 命名空间 |
| `ban_spike` | warning | 站点最近 5 分钟的封禁数超过 `max(10, 3 × 前一小时每 5 分钟的平均封禁数)` | 站点 |
| `report_backlog` | critical | 所有分片上待确认加未投递的上报流条目之和超过 50 000 | 租户 |
| `unknown_ratio_high` | warning | 最近 5 分钟内，在至少 100 条上报的前提下，某站点超过 20 % 的上报被判定为 `unknown` | 站点 |
| `client_error_spike` | warning | 同样的窗口与最小样本数下，超过 10 % 为 `client_error` | 站点 |
| `identity_expired` | info | 身份变为 `expired` | 站点 |
| `secret_expiring` | warning | 密钥将在 7 天内过期（过期后 7 天内也会继续告警） | 命名空间 |
| `test` | info | 点击 **发送测试告警** | 渠道自身范围 |

三种熔断类型和 `identity_expired` 由事件总线驱动，秒级触发。其余来自 `notify_alert_evaluation` 领导者任务：每 30 秒运行一次，超时 25 秒；某条规则失败不影响其他规则；每条规则每轮最多存储 500 条告警，其余顺延到后续轮次。

关于 `identity_expired` 有两个细节值得知道：它的总线队列是有界的，因此评估任务还会扫描 `state_events` 中变为 `expired` 的状态变迁，把队列丢弃的那些告警补发出来，使用相同的去重键，窗口为 1 小时。

### 去重、投递与重试

每条带去重键的告警在入库前先在 Redis 上抢占一个去重标记：默认 10 分钟，`secret_expiring` 为 24 小时，`identity_expired` 为 1 小时。窗口内的重复告警被静默抑制——这就是熔断器抖动不会刷出上百条群消息的原因。

投递是异步的：默认 4 个 worker、10 000 条队列、每次投递最多 3 次尝试、从 2 秒开始指数退避、单次 HTTP 请求超时 10 秒。有两个安全阀会影响你在历史里看到的东西：某渠道最近 5 分钟内投递失败过，则在下一次成功之前每次投递只尝试一次（这样一个失效的端点不会长期占用共享 worker）；而放不进队列的投递会立即记为失败，错误为 `delivery queue full`。

投递结果保存在两处：追加到告警的 `deliveries` 数组，形如 `{"channel_id","channel_name","ok","error","attempts","at"}`；并汇总到渠道的 `last_delivery_at` 与 `last_delivery_status`（`ok` 或错误摘要）。第三方返回的错误消息在存储前会抹掉凭证，且永远不包含目标 URL。

### 测试按钮

**发送测试告警** 会在渠道所属的租户与命名空间下存入一条真实的 `test` 告警，并通过该渠道**同步**投递，只尝试**一次**，且忽略渠道的事件类型过滤、站点限制、最低级别，甚至忽略启用开关。控制台就地显示成功或错误，这次尝试会像其他投递一样出现在告警历史中，操作也会写入审计日志。用它来验证地址、令牌和签名是通的；它不能说明该渠道的过滤条件是否会匹配一条真实告警。

### 告警历史

**告警历史** 标签页按时间倒序列出已触发的告警，含时间、级别、类型、标题、范围，以及 `已投递 N/M` 徽标。展开一行显示消息正文、类型相关的详情，以及每次投递尝试的结果与错误各一行。筛选项：命名空间、站点（需要先选命名空间）、类型、级别，时间范围为最近 1 小时 / 24 小时 / 7 天 / 30 天 / 全部时间；每页 50 行，最多 500 行。

告警事件由 `partition_manager` 领导者任务在 **90 天** 后清理，每批 5000 条。删除渠道不会删除它的投递历史。

---

## Prometheus 指标

所有指标以 `spinneret_` 为前缀，以 Prometheus 文本格式暴露在 `GET /metrics`，无需认证，挂在主 HTTP 监听地址上——除非设置了 `SPINNERET_METRICS_ADDR`，此时 `/metrics` 只出现在该地址上，不再挂在 API 监听地址上。指标是按实例的，跨实例请求和。标签值会被规范化：空值变成 `_`，非法 UTF-8 会被替换，值截断到 128 字节。

仅 Worker 角色的实例（`SPINNERET_ROLE=worker`）只提供健康检查和指标两类端点——这恰好就是你抓取它们的理由。

### 热路径

| 指标 | 类型 | 标签 | 适合告警什么 |
| --- | --- | --- | --- |
| `spinneret_acquire_total` | counter | `site`、`group`、`result`（`ok`、`exhausted`、`circuit_open`、`site_paused`、`no_proxy`、`overloaded`、`error`） | 非 `ok` 结果占比上升——但 `overloaded` 要单独看（见下文） |
| `spinneret_acquire_duration_seconds` | histogram | `site` | p99 超过若干毫秒 |
| `spinneret_acquire_script_seconds` | histogram | — | p99 超过若干毫秒：这只包含 `acquire.lua` 的往返。它与 `spinneret_acquire_duration_seconds` 共用分桶，两者之差就是等待阶梯加渲染 |
| `spinneret_acquire_admission_total` | counter | `result`（`immediate`、`queued`、`shed_no_wait`、`shed_queue_full`、`shed_timeout`、`shed_canceled`） | `shed.*` 持续非零——每次*尝试*一条样本，因此甩请求比率是 `rate(…{result=~"shed.*"}[1m]) / rate(…[1m])`，分母取全部标签 |
| `spinneret_acquire_admission_wait_seconds` | histogram | — | p99 接近 50 ms：尝试把整个等待预算都花在排队等许可上了 |
| `spinneret_acquire_inflight` | gauge | — | 跨副本 `sum()` 就是整个集群压在 Redis 上的租借并发 |
| `spinneret_acquire_queued` | gauge | — | 上升：传了 `wait_ms > 0` 的调用方正在被挂起。SDK 默认的 `wait_ms = 0` 永不挂起，所以默认流量下它恒为 0 |
| `spinneret_acquire_inflight_limit` | gauge | — | `sum(spinneret_acquire_inflight_limit) > SPINNERET_ACQUIRE_FLEET_INFLIGHT`（默认 64）：超过 16 个副本后除法会被截断到单实例下限 4，于是这个和反超集群预算（16 × 4 = 64，接着 17 × 4 = 68）。此时应给 Redis 分片或调低 `SPINNERET_ACQUIRE_FLEET_INFLIGHT` |
| `spinneret_acquire_peers` | gauge | — | 与被抓取的 API 角色实例数不一致（自带 Compose 栈里就是 `count(up{job="spinneret"})`，它的 `spinneret` 任务只发现 API 副本）：这个不一致是区分“只有一个副本”和“分摊坏了”的唯一信号 |
| `spinneret_acquire_peer_beat_age_seconds` | gauge | — | 超过一个心跳周期（2 秒）还在增长：同伴计数被冻结了 |
| `spinneret_acquire_peer_beat_failures_total` | counter | — | 任何持续的增长：心跳失败期间同伴计数被冻结，这只会收窄上限，不会放宽 |
| `spinneret_report_ingest_total` | counter | `result`（`accepted`、`duplicated`、`rejected`） | `rejected` 上升 |
| `spinneret_report_total` | counter | `site`、`group`、`outcome`（十二种判定结果） | 风险占比与 `unknown` 占比 |
| `spinneret_report_lag_seconds` | histogram | — | p99 增长：Worker 处理跟不上 |
| `spinneret_report_process_duration_seconds` | histogram | — | p99 增长：单条上报的处理变贵了 |
| `spinneret_lease_reaped_total` | counter | `site`、`kind`（`expired`、`abandoned`） | `abandoned` 速率：节点没有上报 |

**这五组租借准入序列只在准入控制开启时存在。** 当 `SPINNERET_ACQUIRE_FLEET_INFLIGHT=0` 时不会构造
准入门，因此 `spinneret_acquire_inflight`、`_queued`、`_inflight_limit`、`_peers` 以及两个
`_peer_beat_*` 序列都不存在，`spinneret_acquire_admission_total` 也不会有样本。当
`SPINNERET_ACQUIRE_MAX_INFLIGHT` 钉住上限时 `spinneret_acquire_peers` 同样不存在，因为那时没有东西
需要分摊：序列缺失读作“不适用”，而硬编码的 `1` 会被读成“注册表坏了”。

**`exhausted` 不再是过载信号。** 在准入控制之前，`spinneret_acquire_total{result="exhausted"}` 是实际
可用的崩塌探测器。现在不是了：`exhausted` 表示身份池确实空了（增加身份、放宽轮换策略），而
`overloaded` 表示这个实例到了自己的租借并发上限（扩容，或降低负载）。两者要分别告警——参见
[性能与调优](./17-performance.md)与[运维手册](./16-operations.md)。

### 状态与供给

| 指标 | 类型 | 标签 | 适合告警什么 |
| --- | --- | --- | --- |
| `spinneret_breaker_state` | gauge | `site`、`group` | 值为 2（`open`）；1 是 `half_open`，0 是 `closed` |
| `spinneret_breaker_transitions_total` | counter | `site`、`group`、`to` | 变迁速率（抖动） |
| `spinneret_actions_total` | counter | `site`、`action`（`cooldown`、`expire`、`quarantine`、`ban`、`activate`）、`scope`（`identity_endpoint`、`identity_site`、`identity`、`account`、`proxy_site`、`proxy`）、`mode`（`enforce`、`shadow`） | `ban` 速率；试验策略时对比 `shadow` 与 `enforce` |
| `spinneret_proxies` | gauge | `site`、`state`（`active`、`disabled`、`dead`、`banned`、`quarantined`、`retired`） | `active` 下降——聚合请用 `max`，见下方注意 |
| `spinneret_identities` | gauge | `site`、`type`、`state` | 预留：目前没有代码写入，因此不会导出任何时间序列——身份数量请用概览接口 |
| `spinneret_identities_available` | gauge | `site`、`group` | 预留，同上 |

**注意。** 代理数量是按**命名空间**统计的，然后把该命名空间的数量分别写到它下面的每一个站点上。所以 `sum by (state) (spinneret_proxies)` 会把真实代理数乘上站点个数。请用 `max by (state) (spinneret_proxies)`，或者把查询限定到单个 `site`。

### 管道

| 指标 | 类型 | 标签 | 适合告警什么 |
| --- | --- | --- | --- |
| `spinneret_stream_pending` | gauge | `shard` | 各分片求和即上报积压 |
| `spinneret_stream_owned_shards` | gauge | — | 跨 Worker 求和 ≠ `SPINNERET_REPORT_SHARDS`：有分片无人认领 |
| `spinneret_config_watchers` | gauge | — | 接近 `SPINNERET_MAX_WATCHERS` |
| `spinneret_http_requests_total` | counter | `procedure`（如 `/spinneret.v1.LeaseService/Acquire`）、`code`（`ok` 或 Connect 错误码如 `resource_exhausted`） | 按 procedure 的错误率 |
| `spinneret_http_request_duration_seconds` | histogram | `procedure` | 按 procedure 的 p99 |
| `spinneret_notify_deliveries_total` | counter | `kind`、`result`（`ok`、`error`、`dropped`） | 出现任何 `dropped`；`error` 持续存在 |
| `spinneret_db_write_batches_total` | counter | `writer`（`state_events`、`risk_events`、`proxy_bindings`、`acquire_stats_minutely`、`outcome_stats_minutely`、`node_stats_minutely`、`identity_stats_hourly`、`payload_access_minutely`）、`result`（`ok`、`retry`、`error`、`dropped`） | 出现任何 `dropped`；`error` 持续存在 |
| `spinneret_state_writer_pending_changes` | gauge | — | 队列持续增长 |
| `spinneret_state_writer_dropped_changes_total` | counter | — | 只要增长就说明状态变更丢了 |
| `spinneret_state_writer_spilled_changes_total` | counter | — | 只要增长就说明非生命周期事件被丢弃 |
| `spinneret_job_runs_total` | counter | `job`、`result`（`ok`、`error`） | 各任务的 `error` 速率 |
| `spinneret_job_duration_seconds` | histogram | `job` | 某个任务逼近其超时 |
| `spinneret_loop_restarts_total` | counter | `loop` | 只要增长就要看 |

`job` 标签的取值为 `lease_reaper`、`action.expiry`、`breaker_evaluate`、`hotstate_snapshot`、`proxy_health_check`、`notify_alert_evaluation`、`partition_manager`。

注册表还带有标准的 Go 运行时与进程采集器，所以 `go_goroutines`、`go_memstats_*` 和 `process_resident_memory_bytes` 无需额外配置即可使用。

### 抓取

Compose 栈在 `observability` profile 下自带一个 Prometheus：

```bash
docker compose -f deploy/compose/docker-compose.yml --profile observability up -d prometheus
# http://localhost:9090（可在 .env 中用 PROMETHEUS_PORT 覆盖）
```

它的配置通过 Compose 的 DNS 记录发现 `spinneret` 服务的每个副本：

```yaml
global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  - job_name: spinneret
    metrics_path: /metrics
    dns_sd_configs:
      - names: ["spinneret"]
        type: A
        port: 8080
```

不用 Compose 时，把 `static_configs` 或你的服务发现指向每个实例的 HTTP 地址——如果你已经把 `/metrics` 挪到 `SPINNERET_METRICS_ADDR`，就指向那个地址。当 API 监听地址对部署之外可达时，后一种形态是推荐做法。

**注意。** `SPINNERET_PPROF_ADDR` 会在独立地址上开启 `net/http/pprof`。它默认关闭，永远不会挂到 API 或指标监听地址上，并且没有认证：它会暴露堆内容和 goroutine 栈。只能绑定到回环地址或内网地址。

---

## 告警规则起步模板

保存为 `deploy/compose/config/alerts.yml`，并在 `prometheus.yml` 中加上 `rule_files: ["alerts.yml"]`（`rule_files` 里的路径相对 `/etc/prometheus` 解析）。自带的 Compose 服务只挂载了 `prometheus.yml` 这一个文件，所以规则文件还需要在 `prometheus` 服务上再加一条挂载，否则容器里根本看不到它：

```yaml
  prometheus:
    volumes:
      - ./config/prometheus.yml:/etc/prometheus/prometheus.yml:ro
      - ./config/alerts.yml:/etc/prometheus/alerts.yml:ro
```

阈值只是起点——请按你自己部署跑出来的数字调整。

```yaml
groups:
  - name: spinneret
    rules:
      - alert: SpinneretInstanceDown
        expr: up{job="spinneret"} == 0
        for: 2m
        labels: { severity: critical }
        annotations:
          summary: "实例 {{ $labels.instance }} 无法抓取"

      # 这里刻意排除了 `overloaded`：主动甩负载是对过载的健康反应，用“超过 5% 的获取
      # 请求失败”去 page 会把运维引向“加身份”，而实际上这是容量问题。它在下面有自己的
      # 饱和度告警。
      - alert: SpinneretAcquireFailures
        expr: |
          sum(rate(spinneret_acquire_total{result!="ok",result!="overloaded"}[5m])) by (site)
            / clamp_min(sum(rate(spinneret_acquire_total[5m])) by (site), 0.001) > 0.05
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "站点 {{ $labels.site }} 超过 5% 的获取请求失败"

      - alert: SpinneretAcquireShedding
        expr: |
          sum(rate(spinneret_acquire_total{result="overloaded"}[5m])) by (site)
            / clamp_min(sum(rate(spinneret_acquire_total[5m])) by (site), 0.001) > 0.05
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "站点 {{ $labels.site }} 超过 5% 的获取请求被甩掉（饱和，不是故障）"
          description: >-
            准入控制正在甩请求。先看 spinneret_acquire_script_seconds 的 p99：如果它飙升了，
            说明 Redis 卡住了，调高 SPINNERET_ACQUIRE_FLEET_INFLIGHT 只会更糟。如果它正常
            而 Redis CPU 还有余量，那才是预算太窄。

      - alert: SpinneretAcquirePeerDivisionStuck
        expr: max(spinneret_acquire_peer_beat_age_seconds) by (instance) > 30
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "实例 {{ $labels.instance }} 的租借同伴心跳已过期"
          description: >-
            存活 API 实例计数被冻结，集群租借预算不再按真实副本数分摊。请把
            spinneret_acquire_peers 与 count(up{job="spinneret"})（或任何能统计 API 角色实例数的查询）对照。

      - alert: SpinneretAcquireSlow
        expr: |
          histogram_quantile(0.99,
            sum(rate(spinneret_acquire_duration_seconds_bucket[5m])) by (le, site)) > 0.05
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "站点 {{ $labels.site }} 的获取 p99 超过 50 ms"

      - alert: SpinneretBreakerOpen
        expr: max(spinneret_breaker_state) by (site, group) == 2
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "熔断器 {{ $labels.site }}/{{ $labels.group }} 已打开 5 分钟"

      - alert: SpinneretBreakerFlapping
        expr: sum(increase(spinneret_breaker_transitions_total{to="open"}[30m])) by (site, group) > 3
        labels: { severity: warning }
        annotations:
          summary: "熔断器 {{ $labels.site }}/{{ $labels.group }} 在 30 分钟内打开超过 3 次"

      - alert: SpinneretReportBacklog
        expr: sum(spinneret_stream_pending) > 50000
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "超过 5 万条上报等待处理"

      - alert: SpinneretReportLagHigh
        expr: |
          histogram_quantile(0.99,
            sum(rate(spinneret_report_lag_seconds_bucket[5m])) by (le)) > 5
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "上报处理延迟 p99 超过 5 秒"

      - alert: SpinneretShardsUnowned
        expr: sum(spinneret_stream_owned_shards) < 16
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "被认领的分片少于 SPINNERET_REPORT_SHARDS：有上报无人处理"

      - alert: SpinneretRiskRatioHigh
        expr: |
          sum(rate(spinneret_report_total{outcome=~"rate_limited|captcha|forbidden|banned"}[10m])) by (site)
            / clamp_min(sum(rate(spinneret_report_total[10m])) by (site), 0.001) > 0.1
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "站点 {{ $labels.site }} 超过 10% 的上报带风险判定结果"

      - alert: SpinneretUnknownRatioHigh
        expr: |
          sum(rate(spinneret_report_total{outcome="unknown"}[10m])) by (site)
            / clamp_min(sum(rate(spinneret_report_total[10m])) by (site), 0.001) > 0.2
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "信号规则无法判定站点 {{ $labels.site }}：unknown 超过 20%"

      - alert: SpinneretLeasesAbandoned
        expr: |
          sum(rate(spinneret_lease_reaped_total{kind="abandoned"}[15m])) by (site)
            / clamp_min(sum(rate(spinneret_acquire_total{result="ok"}[15m])) by (site), 0.001) > 0.05
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "站点 {{ $labels.site }} 超过 5% 的租约没有任何上报就结束"

      - alert: SpinneretWritesLost
        expr: |
          increase(spinneret_db_write_batches_total{result="dropped"}[15m]) > 0
            or increase(spinneret_state_writer_dropped_changes_total[15m]) > 0
        labels: { severity: critical }
        annotations:
          summary: "实例 {{ $labels.instance }} 丢弃了数据库写入"

      - alert: SpinneretAlertDeliveryFailing
        expr: sum(rate(spinneret_notify_deliveries_total{result!="ok"}[15m])) by (kind) > 0
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "{{ $labels.kind }} 类型渠道的告警投递持续失败"

      - alert: SpinneretJobFailing
        expr: sum(rate(spinneret_job_runs_total{result="error"}[15m])) by (job) > 0
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "后台任务 {{ $labels.job }} 持续失败"
```

如果你把 `SPINNERET_REPORT_SHARDS` 从默认的 16 改掉了，记得同步调整 `SpinneretShardsUnowned`。

---

## 控制台事件流

`GET /api/v1/events/stream?namespace=<name>` 是给控制台会话用的 Server-Sent Events 流。它像其他控制台请求一样需要认证，要求主体上有租户、且命名空间是调用者可读的。它是为界面实时性服务的**推送**通道——不是审计流，也不能当 Webhook 用。

命名空间频道上发布六种事件类型，每种由各自的权限把关：

| 事件 | 权限 | 负载 |
| --- | --- | --- |
| `breaker.transition` | `breaker:read` | `site`、`site_id`、`client`、`endpoint_group`、`endpoint_group_id`、`from`、`to`、`trigger`、`reason`、`open_until`、`consecutive_opens`、`manual`、`actor`、`metrics`（状态变化时刻的窗口与探测计数） |
| `identity.state` | `identity:read` | 主体类型与 ID、站点、`from`、`to`、动作、截止时间、原因 |
| `proxy.state` | `proxy:read` | 代理状态变化 |
| `alert` | `notify:read` | `id`、`kind`、`severity`、`title`、`message`、`namespace_id`、`site_id`、`created_at` |
| `config.published` | `config:read` | `item_id`、`group`、`key`、`version`、`source_version`（仅回滚时有）、`actor`——命名空间在事件信封的 `namespace_id` 里 |
| `policy.published` | `policy:read` | 已发布的策略 |

每一帧是 `event: <type>` 加上一行 JSON `data:`，内容为事件信封（`type`、`tenant_id`、`namespace_id`、`site_id`、`at`、`data`）。调用者无权查看的事件类型会被静默丢弃；未知类型永远不会转发。熔断或策略变更同时还会发布内部的运行时版本事件，它们走的是另一条频道，不在上面这张表里，所以控制台永远看不到它们——控制台改用重新拉取相关查询的方式跟进。

运行时形态：连接建立时服务端先发 `retry: 5000` 和一行注释，随后每 15 秒发一次 `: ping` 注释以穿过代理保活；单实例最多 5000 条并发流，超出后新连接得到 `503 too many event streams`；每个订阅者有 256 条事件的队列，消费过慢时丢弃事件而不是阻塞总线。投递是尽力而为的。实例进入排空时流会被取消，控制台以 1 秒起、最长 30 秒的带抖动指数退避重连，期间在壳层显示 **实时更新正在重连…**。

控制台用这条流按领域使缓存查询失效，并为熔断变迁和告警弹出提示。如果它断开了，各页面退回到自己的轮询间隔（5 秒或 10 秒）——你失去的是即时性，不是正确性。

---

## 健康检查端点

主 HTTP 监听地址上有两个无需认证的端点，所有角色都提供：

`GET /healthz`——存活检查。只要进程还能提供 HTTP 服务就返回 `200`：

```json
{"status": "ok"}
```

`GET /readyz`——就绪检查。并发执行全部依赖检查，总预算 2 秒：

```json
{
  "status": "ok",
  "checks": {
    "postgres": "ok",
    "redis": "ok",
    "catalog": "ok",
    "hotstate": "ok"
  }
}
```

全部检查通过时返回 `200`，`"status": "ok"`。任一检查失败返回 `503`，`"status": "unavailable"`，该项的值变成一个简短原因而不是 `ok`——PostgreSQL 或 Redis 为 `unreachable`，目录为 `not loaded`，热状态为 `building`、`unreachable`（Redis epoch 查询本身失败）或 `epoch missing (rebuild pending)`。详细原因写入服务端日志，不会出现在响应里。

停机期间该端点直接短路为 `503`：

```json
{"status": "draining"}
```

在整个停机窗口（`SPINNERET_SHUTDOWN_TIMEOUT`，默认 30 秒）内都保持这个状态，同时让进行中的请求跑完——这正是负载均衡器在进程消失之前停止转发新流量的依据。两个响应都带 `Cache-Control: no-store`。

把 `/readyz` 接到负载均衡器和容器健康检查上，把 `/healthz` 接到重启策略上：就绪失败的含义是“不要把流量发到这里”，而不是“重启它”。

容器镜像已经这样做了——`HEALTHCHECK` 每 10 秒执行一次：

```bash
spnr healthcheck --url http://127.0.0.1:8080/readyz   # 2xx 退出 0，否则退出 1
```

`spnr healthcheck` 接受 `--url`（默认即上面这个地址）和 `--timeout`（默认 3 秒），不跟随重定向，专为没有 shell 的镜像设计。参见[命令行工具](./15-cli.md)。

---

## 出问题时先看什么

针对真实会发生的三类故障，给一个简短的排查顺序。

**节点拿到的结果变少，或者完全没有。** 打开概览，先看获取失败率这张卡：

- 非零**且**打开的熔断器非零 → 有熔断器在拒绝发放租约；去熔断器页面看窗口统计和历史标签页，再看该端点组的风险事件，弄清是什么把它触发的。
- 非零但熔断器都是关闭的 → 供给问题。看低水位卡片和站点卡的状态条。已过期段变大说明载荷需要刷新（见[身份与账号](./06-identities.md)）；已封禁段变大说明目标站在反制。
- 为零，但吞吐仍然下降 → 节点根本没在要。看节点表里哪个节点的获取次数掉了，再去看那个节点自己的日志。

**结果不对，或者判定看起来有问题。** 看未知率。某个站点超过约 20 %，说明信号规则已经跟不上目标站返回的内容：打开请求明细，筛选该站点且 `结果 = unknown`，读几条事件的 `business_code`、`error_kind` 和 `markers`。这正是编写新信号规则所需要的输入（见[策略](./08-policies.md)）。

**整体变慢，或者上报延迟。** 按顺序看：

1. `sum(spinneret_stream_pending)` 和概览上的 **待处理上报** 提示——积压增长说明 Worker 跟不上。
2. 跨 Worker 的 `sum(spinneret_stream_owned_shards)` 与 `SPINNERET_REPORT_SHARDS` 对比——少了分片说明没人在消费它。
3. `spinneret_report_lag_seconds` 的 p99 对比 `spinneret_report_process_duration_seconds` 的 p99——延迟高但处理时间低是 Worker 不够；两者都高是单条上报的处理变重，通常是数据库。
4. `spinneret_db_write_batches_total{result="dropped"}` 与 `spinneret_state_writer_dropped_changes_total`——只要增长就说明有数据被丢弃，PostgreSQL 是瓶颈。
5. `spinneret_acquire_duration_seconds` 的 p99 和按 procedure 的 `spinneret_http_request_duration_seconds`——用来区分是热路径慢还是管理 API 慢。

确定是哪一类之后，[性能与调优](./17-performance.md)讲该改什么；[故障排查](./18-troubleshooting.md)有从症状到修复的对照表。

有两个习惯很划算：把 `report_backlog`、`breaker_opened` 和 `identity_low_watermark` 配到一个真的有人看的渠道上；以及在创建渠道的当天就点一次 **发送测试告警**，而不是等到需要它的那天。

---

## 下一步

- [故障排查](./18-troubleshooting.md)——症状 → 原因 → 修复，以及完整的错误 reason 对照表。
- [运维手册](./16-operations.md)——备份、升级、数据保留与事故处置流程。
- [性能与调优](./17-performance.md)——这些页面上的数字对容量意味着什么。
- [策略](./08-policies.md)——你刚读到的那些判定结果背后的信号、动作与熔断规则。
- [配置参考](./03-configuration.md)——本页提到的每一个 `SPINNERET_*` 变量。
