# 代理池

**出站代理池：一条代理记录包含什么、代理如何进入 Spinneret、调度器如何为租约挑选代理、健康状况如何被度量，以及节点收到 `no_proxy_available` 时该怎么办。**

[English](../en/07-proxies.md)

---

## 目录

- [代理池总览](#代理池总览)
- [一条代理记录包含什么](#一条代理记录包含什么)
- [代理状态](#代理状态)
- [导入代理](#导入代理)
- [分配模式](#分配模式)
- [调度器如何挑选代理](#调度器如何挑选代理)
- [会话模板与轮换网关](#会话模板与轮换网关)
- [身份与代理的绑定](#身份与代理的绑定)
- [健康检测](#健康检测)
- [健康分、冷却与交叉归因](#健康分冷却与交叉归因)
- [手动操作](#手动操作)
- [编辑与删除代理](#编辑与删除代理)
- [供应商统计](#供应商统计)
- [权限与 API](#权限与-api)
- [代理池运维](#代理池运维)
- [下一步](#下一步)

---

## 代理池总览

代理池属于**命名空间**。命名空间里的每个代理都会被物化到该命名空间**所有站点**的热状态中，因此一个池服务全部站点；
各站点之间的差异（健康分、活跃租约、冷却）保存在热状态里，而不是拆成多条记录。

Spinneret 自己不转发任何流量。它把代理 URL 连同租约一起交给节点，由节点通过它发出请求：

```text
Acquire(site, client, uri)  ->  身份 + 凭据 + proxy { proxy_id, url, kind, region } + 租约
Report(lease_id, outcome, …) ->  更新该代理的健康分、冷却与状态
```

租约是否携带代理、携带哪个代理，完全由绑定到端点组的**轮换策略**决定，见[分配模式](#分配模式)与
[策略](./08-policies.md)。

控制台页面是**调度 → 代理**（`/proxies`），包含“代理”和“供应商”两个标签页。

### 状态存放在哪里

| 层 | 保存什么 | 说明 |
| --- | --- | --- |
| PostgreSQL 表 `proxies` | 记录本体：加密后的 URL、属性、生命周期状态、检测结果 | 真实来源 |
| PostgreSQL 表 `proxy_bindings` | 身份 → 代理的绑定（`bind_identity` 模式） | 异步写入；路由以 Redis 为准 |
| Redis/Valkey 每站点哈希 `px:<代理 hkey>` | 实时的站点内状态：评分、样本数、活跃租约、冷却、失败连击 | 可从 PostgreSQL 重建 |
| Redis/Valkey 每站点有序集合 `pxrdy` | 就绪队列：按“可用时刻”打分的 active 代理 | 只有 `active` 代理是成员 |

**含凭据的完整代理 URL** 由保管库密封（AAD 为 `<代理 ID>` + `\x00` + `url`，例如 `pxy_…\x00url`）。
明文存储的只有 `display_url`（不含凭据）
和 `username_hint`（用户名的前四个字符加 `***`），API 永远不会返回凭据。见[密钥保管库](./10-secrets.md)。

---

## 一条代理记录包含什么

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `id` | `pxy_…` | 代理 ID |
| `display_url` | 文本 | `scheme://host:port`，绝不含凭据 |
| `scheme` | `http` \| `https` \| `socks5` | 连接代理的协议 |
| `host`、`port` | 文本、1–65535 | 归一化后的主机（小写域名或规范化 IP）与端口 |
| `username_hint` | 文本 | 用户名前四个字符加 `***`；无凭据时为空 |
| `kind` | `datacenter` \| `residential` \| `mobile` \| `tunnel` | 供轮换策略的 `proxy.kinds` 过滤使用 |
| `region` | ≤ 64 字节 | 出口地区，通常是 ISO 国家码；用于地区匹配和 `proxy.regions` 过滤 |
| `city` | ≤ 128 字节 | 出口城市，仅供参考 |
| `provider` | ≤ 128 字节 | 自由填写的供应商名；“供应商”标签页与 `proxy.providers` 过滤按它分组 |
| `max_concurrency` | 1…100000 | 该代理上的最大同时租约数 |
| `tags` | ≤ 64 个，每个 ≤ 64 字节 | 自由标签；轮换过滤 `proxy.tags` 要求**全部**命中 |
| `session_template` | ≤ 512 字节 | 轮换网关的用户名模板（见下文） |
| `state` | 见[代理状态](#代理状态) | 生命周期状态 |
| `state_reason`、`state_changed_at` | 文本、时间戳 | 最近一次状态变更的原因与时间 |
| `ban_until` | 时间戳 | 封禁**或**隔离的结束时间；封禁中为 `NULL` 表示永久 |
| `cooldown_until` | 时间戳 | 全局冷却的结束时间 |
| `url_version` | 整数 | 每次替换 URL 时递增 |
| `last_check_at`、`last_check_ok`、`last_latency_ms`、`exit_ip`、`consecutive_check_failures`、`next_check_at` | — | 健康检测结果 |
| `bound_identities` | 整数 | 绑定到该代理的身份数量（只读，实时计算） |

标签不能包含逗号、空白字符或控制字符——它们在热状态中以 `,tag1,tag2,` 的形式存储并按子串匹配。

**没有 weight 字段。** 代理的挑选权重由其实时健康分推导而来（见[调度器如何挑选代理](#调度器如何挑选代理)）；
`max_concurrency` 是唯一需要你手工设置的容量旋钮。

### 各站点状态

`GetProxy` 与控制台详情面板还会返回调用者有权读取的每个站点上的：

| 字段 | 含义 |
| --- | --- |
| `state` | 该站点调度器看到的代理状态 |
| `score` | 代理 × 站点健康分，0…100，已衰减到读取时刻 |
| `samples` | 该评分背后的观测样本数 |
| `active_leases` | 该站点上当前通过此代理持有的租约数 |
| `cooldown_until` | **站点级**冷却的结束时间 |

---

## 代理状态

| 状态 | 在池中？ | 如何进入 | 如何离开 |
| --- | --- | --- | --- |
| `active` | 是 | 导入后的默认值；`enable`、`unban`、`unquarantine`、`restore`；失效代理检测成功 | 下面任一操作 |
| `disabled` | 否 | `disable` | `enable` |
| `dead` | 否 | 连续 3 次健康检测失败 | 一次成功检测，或 `enable` |
| `banned` | 否 | `ban`，或动作策略中 `action: ban, scope: proxy` 的规则 | `unban`，或封禁到期 |
| `quarantined` | 否 | `quarantine`，或动作策略中 `action: quarantine, scope: proxy` 的规则 | `unquarantine`，或隔离到期 |
| `retired` | 否 | `archive` | `restore` |

列表不带状态筛选时会隐藏 `retired` 代理，其余状态都会显示。

冷却**不是**状态。冷却中的代理仍然是 `active`，仍留在就绪队列里，只是可用时刻被推到了未来，调度器在此之前会跳过它。
冷却有两种：站点级（`cd`）和全局级（`gcd`，由 `proxies.cooldown_until` 镜像而来）；代理的可用时刻是二者的
`max(cd, gcd)`。

`proxies` 表没有 `quarantine_until` 列：`ban_until` 同时承载封禁**或**隔离的结束时间，到期任务会把两者都释放回
`active`。

---

## 导入代理

`ImportProxies`（权限 `proxy:write`，控制台“代理 → 导入”）是创建代理的唯一途径。它支持三种格式，并按 URL 去重。

| 限制 | 取值 |
| --- | --- |
| 数据大小 | 32 MiB |
| 数据行数 | 100 000 |
| 返回的逐行失败条数 | 1000 条，随后附一条行号为 `0` 的汇总：`N more rows failed` |

### `lines` 格式

每行一个 URL，其后可跟以空白分隔的 `key=value`。空行和以 `#` 开头的行会被忽略。可用的键：`kind`、`region`、
`city`、`provider`、`tags`、`max_concurrency`、`session_template`。`tags` 按 `,` 或 `;` 拆分。由于字段按空白拆分，
取值中不能含空格。写成 `url=` 会被拒绝：URL 必须是第一个字段。

```text
# 机房代理池，单一地区
http://user:pass@203.0.113.10:8000 kind=datacenter region=US provider=provider-a tags=dc,primary
http://user:pass@203.0.113.11:8000 kind=datacenter region=US provider=provider-a tags=dc,primary
socks5://user:pass@203.0.113.12:1080 kind=residential region=DE provider=provider-b max_concurrency=2
```

### `jsonl` 格式

每行一个 JSON 对象，字段为 `url`、`kind`、`region`、`city`、`provider`、`tags`（数组）、`max_concurrency`
和 `session_template`。未知字段会被拒绝；`url` 必填。

```json
{"url": "http://user:pass@203.0.113.10:8000", "kind": "datacenter", "region": "US", "tags": ["dc"]}
{"url": "socks5://user:pass@203.0.113.12:1080", "kind": "residential", "region": "DE", "max_concurrency": 2}
```

### `csv` 格式

首行是表头，之后每行一个代理。允许的列：`url`（必需）、`kind`、`region`、`city`、`provider`、`tags`、
`max_concurrency`、`session_template`。出现未知列或重复列会导致整次导入被拒绝。空单元格表示“未设置”。
多个标签请用 `;` 分隔——逗号会把 CSV 字段切开。记录号即行号，表头算作第 1 行。

```text
url,kind,region,provider,tags,max_concurrency
http://user:pass@203.0.113.10:8000,datacenter,US,provider-a,dc;primary,4
http://user:pass@203.0.113.13:8000,mobile,DE,provider-b,mobile,1
```

### URL 规则

形式为 `scheme://[user[:password]@]host:port`，协议须为 `http`、`https` 或 `socks5`，总长不超过 2048 字节，
且必须**显式指定端口**。不允许路径、查询串或片段。IPv6 主机写在方括号内。凭据会做百分号解码，各自最长 255 字节。
解析错误绝不回显输入，因此坏掉的凭据不会出现在日志或错误信息里。

### 默认值、去重与合并

`defaults` 为未设置对应值的行提供取值：`kind`（留空即 `datacenter`）、`region`、`city`、`provider`、
`tags`（追加到每一行，与该行自身的标签取并集）、`max_concurrency`（0 即 1）和 `session_template`。

代理在命名空间内按 `HMAC-SHA256(pepper, 归一化后的 URL)` 去重。URL 已存在的行会**更新**现有代理：只有该行或默认值
显式设置的属性会被应用，其余属性保持原值。若没有任何变化，该行计入 *unchanged*。同一次导入内的重复项会以
`duplicate of line N` 被拒绝。

响应会给出 `created`、`updated`、`unchanged` 的计数，并按行号列出 `failed`，其消息绝不包含凭据。

**请务必先试运行。** 设置 `dry_run: true` 时不会存储任何数据，响应只报告“将会”新建和更新多少条。控制台要求先用当前输入
执行一次试运行，才会启用导入按钮。

导入不发布状态事件，因此解析器最多会把解密后的 URL、region、kind 和会话模板再留用 30 秒。而调度器用于过滤的属性——
`kind`、`region`、`provider`、`tags`、`max_concurrency`——由导入本身直接写入热状态，下一次 acquire 即生效。

---

## 分配模式

绑定到端点组的**轮换策略**中的 `proxy` 段决定了代理分配的全部行为：

```yaml
proxy:
  mode: pool                 # none | pool | bind_identity | region_match
  kinds: [datacenter]        # datacenter | residential | mobile | tunnel 中的任意若干
  tags: [primary]            # 代理必须带有全部这些标签
  providers: [provider-a]    # 任意命中其一
  regions: [US, DE]          # 任意命中其一
  region_match: false        # 额外要求 proxy.region == identity.region
  rebind_tolerance: 5m       # 仅 bind_identity
  max_rebinds_per_day: 3     # 仅 bind_identity
```

| 模式 | 作用 | 适用场景 |
| --- | --- | --- |
| `none`（默认） | 不分配代理；`AcquireResponse.proxy` 不存在 | 节点自带出口，或目标不需要代理 |
| `pool` | 每个租约都从站点的就绪队列中挑一个代理 | 机房或共享池，任一出口都同样可用 |
| `bind_identity` | 身份跨租约保持同一个代理，只有绑定的代理不可用时才换 | 会话与出口 IP 绑定的账号 |
| `region_match` | 与 `pool` 相同，但代理的 `region` 必须等于**身份的**地区 | 地域固定的账号，出口国家必须跟随账号 |

把 `region_match: true` 作为标志使用，可以给 `pool` 和 `bind_identity` 加上同样的地区约束；因此
`mode: region_match` 等价于 `mode: pool` 加 `region_match: true`。

**注意。** 地区匹配是严格的字符串相等。地区为空的身份因此只能匹配 `region` 同样为空的代理。要么给身份和代理成对设置地区，
要么干脆不开启地区匹配。

`kinds`、`providers`、`regions` 是“或”集合：代理命中其中任意一个即通过，空列表表示不作限制。`tags` 是“与”集合：
代理必须带有列出的每一个标签。

---

## 调度器如何挑选代理

代理的挑选发生在原子的 `Acquire` 脚本内部，在身份选定之后。

每个站点维护一个有序集合 `pxrdy`，成员是该命名空间的 `active` 代理，分值是该代理变为可用的毫秒时刻。挑选只在这个集合的
**到期区间**（`score <= now`）内进行。

1. `ZCOUNT pxrdy -inf now`。若没有任何代理到期，就无法给这个候选身份分配代理；当整次调用连一个租约都签不出时，
   调用最终以 `no_proxy_available` 结束（见下文）。
2. 候选按**每轮 16 个**的窗口采样，最多 **3 轮**。到期代理超过 48 个时窗口偏移随机取，否则窗口沿集合头部推进。
3. 每个被采样的代理读取一次（`st`、`kd`、`rg`、`pv`、`tg`、`mc`、`al`、`sc`、`sts`、`cd`、`gcd`、`pid`）。
   当前无法承载租约的代理会**离开到期区间**，以免同一次调用的后续采样和之后的调用反复撞上它：
   - 非 `active` → 从 `pxrdy` 移除；
   - 冷却中（`max(cd, gcd) > now`）→ 推到冷却结束时刻；
   - 已饱和（`al >= max_concurrency`）→ 推后 `min(lease_ttl, 5 秒)`。
4. 应用策略过滤：`kinds`、`providers`、`regions`、`tags`（须全部命中），以及开启地区匹配时的地区相等判断。
5. 幸存者按 `max(score, 5)²` 加权，其中 `score` 是衰减到当前时刻的代理 × 站点健康分，然后做轮盘抽取。
   评分平方化使健康的代理被强烈偏好，但绝不会把较弱的代理彻底排除。
6. 只要某一轮产生了至少一个候选，采样就停止。
7. 成功后代理的 `al` 加一。若达到 `max_concurrency`，该代理在 `pxrdy` 中被推到租约到期时刻（`max_concurrency` 为 1 时）
   或推后 `min(lease_ttl, 5 秒)`。归还、续约结束或被回收后它会恢复。

当没有候选幸存时，`Acquire` 会在 `wait_ms` 预算内重试（0–5000 毫秒，服务端上限 5 秒），随后以 `no_proxy_available`
失败，并带上由 `pxrdy` 中最早可用时刻
算出的 `retry_after_ms`（钳制在 50 毫秒…60 秒，队首已到期时取 1 秒）。见[节点 API 参考](./13-node-api.md)。

代理 ID 和 URL 随后在脚本之外解析：解析器解密 URL、渲染会话模板（若有），返回
`{proxy_id, url, kind, region}`。解密后的 URL 按代理 ID 缓存 30 秒（LRU，10 万条），任何代理状态事件都会立即让其失效，
因此编辑过的 URL 或模板会马上生效。

---

## 会话模板与轮换网关

许多供应商只暴露一个网关主机，通过**用户名**来选择出口：`user-<account>-session-<id>@gateway:8000`。
`session_template` 就是为每个租约渲染这个用户名的。

| 占位符 | 渲染结果 |
| --- | --- |
| `{username}` | 代理 URL 中存储的用户名 |
| `{password}` | 代理 URL 中存储的密码 |
| `{identity_id}` | 所租用身份的 ID |
| `{identity_hash}` | `sha256(identity_id)` 的前 12 位十六进制字符——同一身份保持不变 |
| `{lease_id}` | 租约 ID |
| `{random}` | 8 位随机十六进制字符，每个租约都不同 |

规则：模板最长 512 字节，大括号不能转义，未知占位符或未闭合的 `{` 都会导致校验失败。渲染结果**只替换用户名**，
密码仍取自存储的 URL。若模板渲染出空用户名，则保持存储的 URL 原样。

```text
# 每个身份粘住同一出口：同一身份始终拿到同一个 session id。
http://user:pass@tunnel.provider-a.example:9000 kind=tunnel session_template=user-{username}-session-{identity_hash}

# 每个租约一个全新出口。
http://user:pass@tunnel.provider-a.example:9000 kind=tunnel session_template=user-{username}-session-{random}
```

当网关对用户名长度有限制时请用 `{identity_hash}` 而不是 `{identity_id}`：它更短且稳定。希望轮换网关每次请求换一个 IP 时，
用 `{random}` 或 `{lease_id}`。轮换网关通常还应设置 `kind: tunnel`，并把 `max_concurrency` 设成你购买的套餐规格——
Spinneret 看不到这一个主机背后究竟有多少出口。

---

## 身份与代理的绑定

在 `bind_identity` 模式下，每个身份在热状态中带有 `px`（绑定的代理）、`rbd`（上次换绑的 UTC 日期，`YYYYMMDD`）
和 `rbn`（当天的换绑次数）。

每次 acquire 时，在把身份当作候选之前就会先检查它绑定的代理：

| 情形 | 结果 |
| --- | --- |
| 尚未绑定 | 从池中挑一个代理并绑定（`binding = b`） |
| 绑定代理 `active`、未冷却、有余量 | 直接使用绑定的代理，不做挑选 |
| 绑定代理 `active`、未冷却、已饱和 | **身份**被推后 `min(lease_ttl, 5 秒)`，改试其他身份 |
| 绑定代理 `active` 但冷却中，且冷却在 `rebind_tolerance` 内结束 | 身份被推到该冷却结束时刻——宁可等待也不换绑 |
| 绑定代理冷却时间超过 `rebind_tolerance`，或不是 `active` | 换绑：挑选新代理，排除旧代理（`binding = r`） |
| 需要换绑但当天 `rbn` 已达 `max_rebinds_per_day` | 身份被推后 10 分钟并跳过 |

默认值：`rebind_tolerance: 5m`、`max_rebinds_per_day: 3`。`max_rebinds_per_day: 0` 会完全禁止换绑——代理变坏的身份
只能等待。计数在 UTC 00:00 重置。

在批量 acquire 中，同一次调用内已经服务过租约的绑定代理，在再次派发前会重新对照 `max_concurrency` 检查一次。

绑定由异步写入器持久化到 `proxy_bindings`（`identity_id` 主键、`proxy_id`、`bound_at`、`rebind_day`、
`rebinds_today`）：队列 5 万条，每秒刷写一次，每批最多 1000 条，每批最多尝试 3 次。写入绝不阻塞 `Acquire`；
队列溢出时绑定会被丢弃并计数，因为路由仍以 Redis 为准，下一次 acquire 或热状态快照会重新写入。

删除代理会通过外键级联移除其绑定，被绑定的身份在下次 acquire 时会挑选新代理。

---

## 健康检测

一个独立的检测器会按计划**通过**每个代理去抓取一个 URL。它是除了操作员和上报之外，唯一能让代理在 `active` 与 `dead`
之间迁移的机制。

| 设置 | 变量 | 默认值 |
| --- | --- | --- |
| 检测 URL | `SPINNERET_PROXY_CHECK_URL` | `http://example.com/` |
| 同一代理两次检测的间隔 | `SPINNERET_PROXY_CHECK_INTERVAL` | `60s`（最小 `1s`） |
| 单次检测请求的超时 | `SPINNERET_PROXY_CHECK_TIMEOUT` | `10s`（最小 `100ms`） |
| 出口 IP 查询地址（可选） | `SPINNERET_PROXY_EXIT_IP_URL` | 空（关闭） |
| GeoIP 数据库（可选） | `SPINNERET_GEOIP_DB` | 空（关闭） |

单次运行内的并发固定为 64 个检测。见[配置参考](./03-configuration.md)。

### 一次运行做了什么

作业 `proxy_health_check` 在**每个**实例上按检测间隔运行。代理按 `xxhash64(proxy_id) % 存活 worker 数` 分片，
因此每个代理只由一个实例检测。一次运行按每页 500 条选出状态为 `active` 或 `dead` 且 `next_check_at` 已过的代理。

一次检测就是通过该代理对检测 URL 发起一次 HTTP `GET`，`User-Agent: spinneret-proxy-check/1`，禁用长连接，不跟随重定向。
拨号、TLS 握手、响应头以及整个请求都使用检测超时。传输错误或状态码 400 及以上算失败；其余算成功，往返时间写入
`last_latency_ms`。`socks5://` 代理通过 SOCKS5 拨号器连接，`http://` 与 `https://` 通过代理 URL 连接。

### 出口 IP 与 GeoIP

设置了 `SPINNERET_PROXY_EXIT_IP_URL` 时，检测成功后会通过同一代理再请求该地址，并解析响应体——`{"ip": "…"}`
或裸地址——写入 `exit_ip`。第二次请求失败只会记一条 debug 日志，**不会**让检测失败。

若 `SPINNERET_GEOIP_DB` 指向 `.mmdb` 格式的 GeoIP2/GeoLite2 City 或 Country 数据库，出口 IP 会被解析为国家 ISO 码和英文城市名。
它们**只用于补空**：仅当存储的 region 为空时才写入 region，仅当 region 与 city 都为空时才写入 city。手工设置或导入设置的
值绝不会被覆盖。

### 失败会导致什么

| 连续失败次数 | 效果 |
| --- | --- |
| 1、2 | `consecutive_check_failures` 递增；代理仍是 `active`，仍留在池中 |
| 3 | `active → dead`，`state_reason` 变为 `health check failed 3 times: <错误>`，代理离开 `pxrdy` |
| 4、5、… | 检测退避：`next_check_at = now + min(interval × 2^(失败次数 − 3), 1 小时)` |

`dead` 代理的第一次成功检测会把它改回 `active`，`state_reason` 为 `health check succeeded`，清零失败计数并让它回到池中。
其余状态不会被自动改变：`disabled`、`banned`、`quarantined`、`retired` 的代理既不会被检测，也不会被检测器复活。

每次检测还会向该命名空间**所有**站点的代理 × 站点健康分写入一次观测：成功 100，失败 0，EWMA 的 alpha 为 0.1，
基线 70，衰减常数 6 小时，失败连击在一小时没有失败后重置。只有已经存在该代理哈希的站点会被写入。

状态迁移会写入一条 `state_events` 记录，并发布 action 为 `health_check` 的 `proxy.state` 事件；仅补齐了 region/city 的检测
发布 action 为 `update` 的事件。两者在控制台都会立即可见。

记录在代理上的错误信息绝不包含代理凭据——用户名和密码在存储或记录之前会被脱敏，原文与百分号编码形式都会处理。

### 立即检测某个代理

`CheckProxy`（权限 `proxy:operate`，控制台行内或详情面板中的“立即检测”）会立刻执行同样的探测，并像定时检测一样完整记录结果——
包括失败计数以及可能的 `active → dead` 迁移。它返回 `{ok, latency_ms, exit_ip, region, error}`。

如果两个实例同时要为同一个代理记录检测结果，较晚的那次会被丢弃（加锁后的行已不再到期），因此失败不会被重复计数。

### 指标

`spinneret_proxies{site,state}` 是按站点和状态统计的代理数量仪表，每次检测运行后刷新。见
[可观测性与告警](./12-observability.md)。

---

## 健康分、冷却与交叉归因

健康检测告诉你代理*能不能用*；上报告诉你它*对目标好不好用*。两者汇入同一个代理 × 站点评分。

### 代理 × 站点评分

每一条带代理的上报都会更新该站点上代理哈希的 `sc`（评分）、`sts`（时间戳）、`sn`（样本数）以及失败连击 `nf`/`lf`，
使用动作策略 `health` 段中的 alpha、baseline 和 tau（默认 0.1 / 70 / 6h）。观测值是固定的：

| 判定结果 | 代理观测值 | 何时计入 |
| --- | --- | --- |
| `success` | 100 | 始终 |
| `proxy_error` | 0 | 归因包含代理时 |
| `network_error` | 40 | 归因包含代理时 |
| `rate_limited` | 30 | 归因包含代理时 |
| 其他 | — | 从不影响代理评分 |

两次观测之间评分会衰减回基线，因此闲置不用的代理会漂回 70，既不会获得也不会保住优势。

### 归因

每条上报都会被分类为一个判定结果和一个**归因**，后者决定由身份、代理、两者还是都不负责：

| 判定结果 | 默认归因 |
| --- | --- |
| `empty`、`captcha`、`auth_invalid`、`forbidden`、`banned` | 身份 |
| `rate_limited` | 两者 |
| `proxy_error`、`network_error` | 代理 |
| 其他 | 无 |

信号规则可以通过 `blame: none \| identity \| proxy \| both` 显式覆盖。作用域为代理的动作规则，在归因不含代理时会被跳过；
作用域为身份或账号的规则，在归因不含身份时会被跳过。

### 交叉归因

默认归因只是猜测。交叉归因用“哪些身份用过哪些代理”的滑动窗口来纠正它：

```yaml
cross_attribution:
  enabled: true
  window: 10m
  proxy_distinct_identities: 3
  identity_distinct_proxies: 3
```

对于及时上报、同时带有身份和代理的**风险判定结果**（`rate_limited`、`captcha`、`forbidden`、`banned`），系统会在窗口时长内
按站点维护两个集合：在这个代理上出问题的不同身份，以及这个身份在其上出问题的不同代理。然后：

- 该代理上的身份数 ≥ `proxy_distinct_identities` **且**该身份涉及的代理数 < `identity_distinct_proxies` →
  归因改为 `proxy`。三个不同账号在同一出口上都遇到验证码，那是出口的问题，不是账号的问题。
- 该身份涉及的代理数 ≥ `identity_distinct_proxies` → 把身份加入归因（变为 `identity`，若代理已被归因则为 `both`）。
  一个账号在三个不同出口上都失败，说明问题在它自己。

修正后的归因才是动作规则和健康观测看到的值。这正是为什么身份能在坏代理面前存活，以及为什么症状是验证码时坏代理仍会被冷却。

### 对代理的自动动作

动作策略规则可以直接作用于代理：

| `scope` | 效果 | 写在哪里 |
| --- | --- | --- |
| `proxy_site`（冷却） | 只在上报所在站点冷却该代理 | 该站点哈希的 `cd` |
| `proxy`（冷却） | 在命名空间的每个站点冷却该代理 | 每个站点的 `gcd`，并持久化到 `proxies.cooldown_until` |
| `proxy`（封禁） | 在所有站点封禁该代理 | 每个站点的生命周期状态，并持久化 |
| `proxy`（隔离） | 在所有站点隔离该代理 | 每个站点的生命周期状态，并持久化 |

内置的默认动作策略包含这样一条规则：

```yaml
- name: proxy-error-cooldown
  when: { outcome: proxy_error }
  action: cooldown
  scope: proxy_site
  base: 2m
  max: 30m
```

冷却时长随失败连击递增，直到 `max`。见[策略](./08-policies.md)。

---

## 手动操作

`OperateProxies`（权限 `proxy:operate`）对最多 **1000** 个代理 ID 应用一个操作。在控制台中：选中若干行后使用批量操作栏，
或使用行操作菜单。

| 操作 | 时长 | 可限站点 | 效果 |
| --- | --- | --- | --- |
| `disable` | — | 否 | → `disabled`。在重新启用前不用于新租约。对 `retired` 会失败（需先 `restore`） |
| `enable` | — | 否 | `disabled` 或 `dead` → `active`，清零失败计数并立刻重新检测。对 `banned`、`quarantined`、`retired` 会失败 |
| `ban` | 必填，允许 `permanent` | 否 | → `banned` 直到结束时刻；`permanent` 不会自行到期。对 `retired` 会失败（需先 `restore`） |
| `unban` | — | 否 | `banned` → `active`。其他状态会失败 |
| `cooldown` | 必填且 > 0 | 是 | 在结束前被调度器跳过。指定站点时只作用于该站点（`cd`）；不指定时作用于所有站点（`gcd`）并写入 `proxies.cooldown_until`。对 `retired` 会失败（需先 `restore`） |
| `quarantine` | 可选（默认 24 小时） | 否 | → `quarantined` 直到结束时刻。对已隔离的代理再次隔离会替换结束时间。对 `banned` 和 `retired` 会失败（后者需先 `restore`） |
| `unquarantine` | — | 否 | `quarantined` → `active`。其他状态会失败 |
| `archive` | — | 否 | → `retired`。不再被使用，且除非按“已退役”筛选否则不显示 |
| `restore` | — | 否 | `retired` → `active`，同时清除封禁、冷却与失败计数。其他状态会失败 |
| `reset_stats` | — | 是 | 清除评分、样本数与失败连击（`sc`、`sts`、`sn`、`nf`、`lf`）。不指定站点时还会清零 `consecutive_check_failures` |

时长写作 `10m`、`7d`、`500ms`，单位为 `ms|s|m|h|d`；封禁还可写 `permanent`。最长 512 字节的 `reason` 会记录到状态事件与
审计日志中。

每个操作都是批量操作，返回的是结果而不是错误：

```json
{
  "result": {
    "matched": 12,
    "succeeded": 10,
    "failed": [
      {"id": "pxy_…", "reason": "not_found",           "message": "proxy not found"},
      {"id": "pxy_…", "reason": "failed_precondition", "message": "proxy is retired; restore it first"}
    ]
  }
}
```

`reason` 取值为 `not_found`（不存在，或对你不可见）、`failed_precondition`（当前状态不允许该操作）、
`site_unknown`（该命名空间中没有这个站点）或 `internal`。一次调用可以混入多个命名空间的 ID，每个命名空间在各自的事务中处理。

真正产生了变化的操作才会写入一条 `state_events` 记录，并在命名空间频道上发布 `proxy.state` 事件（`unquarantine` 的
action 是 `activate`），因此控制台和任何 SSE 消费者都会在毫秒级看到变化。而审计条目 `proxy.<操作>` 即使是空操作也会写入——
例如对已禁用的代理执行 `disable`、对已退役的代理执行 `archive`。

---

## 编辑与删除代理

### 修改属性

`UpdateProxy`（权限 `proxy:write`）可修改 `kind`、`region`、`city`、`provider`、`max_concurrency`、
`tags`（需同时传 `set_tags: true`，空数组表示清空）与 `session_template`。未设置的字段保持不变。没有产生任何变化的请求不会写审计条目、也不会发事件，控制台会提示“没有任何修改”；
响应始终返回完整的代理对象。

### 替换 URL

传入 `url` 会替换**含凭据的**代理 URL。新 URL 会被重新密封，`url_hash` 重新计算，`display_url`、`host`、`port`、
`username_hint` 同步更新，`url_version` 递增。存储的 URL 永不显示，因此控制台要求你输入两次新 URL。
新租约立即使用新 URL（状态事件会让解析器缓存失效）；已经发出的租约仍使用它拿到的那份 URL。

### 删除还是归档

`DeleteProxies`（权限 `proxy:write`，最多 1000 个 ID）永久删除代理，连同它们的身份绑定、各站点热状态与热状态快照。
该操作不可撤销，历史也随之消失。

如果你以后可能还要这条记录，或者想保留痕迹，**请用归档而不是删除**：`archive` 让代理退役、保留全部数据，`restore` 可以把它带回来。

当选中的代理上绑定了身份时，控制台会在删除前发出警告。删除按命名空间在单个事务中完成，先移除热状态；若任何一步失败，
该命名空间不做任何改动，其热状态会尽力恢复，受影响的 ID 会以 `internal` 作为原因返回为失败项，因此重试是安全的。

---

## 供应商统计

“供应商”标签页（`GetProviderStats`，权限 `proxy:read`）按 `provider` 属性聚合代理池，时间范围为最近 1 小时、24 小时、7 天，
或任意显式区间：

| 列 | 含义 |
| --- | --- |
| `proxies` | 该供应商的代理数（不含已退役） |
| `active` / `dead` | 其中处于各状态的数量 |
| `requests` | 区间内经由该供应商代理的上报数 |
| `success_ratio` | 判定结果为 `success` 的比例 |
| `risk_ratio` | 判定结果为 `rate_limited`、`captcha`、`forbidden` 或 `banned` 的比例 |
| `avg_latency_ms` | 上报延迟的平均值 |

`GetProviderStats` 还接受 `site` 参数；`site` 为空时聚合你有权读取的所有站点，控制台发送的就是空值。

按请求数降序排列。没有供应商的代理归到空名称一组。只统计你有权读取的站点，因此两个用户看到不同的数字是正常的。

这张表回答的是“哪个供应商在消耗我的身份”。风险率高但检测通过率正常的供应商是被识别了，而不是坏了；检测通过率低的供应商只是挂了。

---

## 权限与 API

| 权限 | 允许 |
| --- | --- |
| `proxy:read` | `ListProxies`、`GetProxy`、`GetProviderStats` |
| `proxy:write` | `ImportProxies`、`UpdateProxy`、`DeleteProxies` |
| `proxy:operate` | `OperateProxies`、`CheckProxy` |

角色：`viewer` 拥有 `proxy:read`；`operator` 及以上追加 `proxy:write` 与 `proxy:operate`。API 令牌的权限范围
`proxy:write` 同时授予这三个权限。站点级绑定在命名空间层面只持有 `proxy:read`（以及 `namespace:read`）：它可以列出代理、
查看自己那些站点的站点内状态，但完全不能导入、编辑、删除或操作代理。`cooldown` 与 `reset_stats` 的 `site` 参数只是收窄
操作的作用范围，并不改变所需的权限。对于你无权读取的代理，
返回 `not_found` 而不是 `permission_denied`，以免泄露其他租户的代理 ID。见[租户、用户与令牌](./11-access-control.md)。

全部 8 个 RPC 都在 `ProxyAdminService` 上，可通过 Connect、gRPC 与 HTTP + JSON 访问，路径为
`/spinneret.v1.ProxyAdminService/<方法名>`：

```bash
curl -sS https://spinneret.example.com/spinneret.v1.ProxyAdminService/ListProxies \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"namespace":"default","states":["active"],"kinds":["residential"],"page_size":50}'
```

```bash
curl -sS https://spinneret.example.com/spinneret.v1.ProxyAdminService/ImportProxies \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"namespace":"default","format":"lines","dry_run":true,
       "defaults":{"kind":"datacenter","provider":"provider-a"},
       "data":"http://user:pass@203.0.113.10:8000 region=US\n"}'
```

列表支持按 `states`、`kinds`、`providers`、`regions`、`tags`（须全部命中）过滤，以及 `search` 字符串——匹配
display URL、主机或出口 IP 的子串，或 ID 前缀。每页默认 50 条，最多 500 条，用不透明的 `next_page_token` 继续翻页。

需要快速搭建压测池时，`spnr seed --proxies N --proxy-url 'http://user-{i}:secret@host:9091'` 会导入 N 个生成的代理，
并发布一条 `mode: pool` 的轮换策略。见[命令行工具](./15-cli.md)。

---

## 代理池运维

### 容量规划

从并发出发，而不是从代理数量出发。代理池必须提供

```text
峰值并发租约数  ≈  每秒请求数 × 平均请求时长（秒）
```

而每个代理只提供其中的 `max_concurrency`。要留出余量：任何时刻都会有一些代理在冷却、一些已失效，而且
`max_concurrency` 是调度器主动回避的上限——饱和的代理会被推出到期区间，因此按峰值精确配置的池在峰值时必然产生
`no_proxy_available`。

在 `bind_identity` 模式下，池的大小则要能让同时可调度的身份各自持有自己的绑定。粗略地说：
`代理数 × max_concurrency ≥ 你希望同时活跃的身份数`。

`max_concurrency` 应当反映供应商允许的并发，而不是机器能压出的并发。对轮换网关来说这个值是一个策略选择：网关会欣然接受更多请求，
而由此产生的并行出口正是让你被识别的原因。

### 混合多家供应商

- 每次导入都设置 `provider`。没有它，“供应商”标签页是空的，某一家劣化时你也分不清是谁。
- 除 kind、region、provider 之外需要路由的维度一律用 `tags`——`primary`、`backup`、`cheap`、合同名等。
  轮换过滤要求全部标签命中，因此标签可以自由组合。
- 保留第二家供应商已导入但用标签排除在生效策略之外。切换时只需改轮换策略里的一个 `proxy.tags` 列表，而不是在压力之下临时导入。
- 除非有意为之，否则不要在一个端点组里混合 kind。`residential` 出口和 `datacenter` 出口的表现不会一致，
  加权挑选会悄悄偏向当前评分更高的那一类。
- 住宅与移动代理的 `max_concurrency` 要比机房代理低。它们通常就是一户家庭宽带或一条手机线路。

### “没有可用的代理”

节点收到 `no_proxy_available`，意味着那一刻站点的就绪队列里没有可用的代理。按顺序排查：

1. **池里还有 active 的代理吗？** 控制台 → 代理，按状态 `active` 筛选。为 `0` 说明池空了、被禁用了或全部失效；
   `spinneret_proxies{state="dead"}` 持续上升则指向供应商或检测 URL。
2. **策略过滤还能命中什么吗？** 打开绑定的轮换策略，把 `proxy.kinds`、`proxy.providers`、`proxy.regions`、
   `proxy.tags` 与代理页面的筛选栏对照。一个标签拼写错误就会静默排除整个池——过滤条件之间是“与”，交集为空时不会有任何提示。
3. **是地区匹配卡住了吗？** 在 `mode: region_match` 或 `region_match: true` 下，没有地区的身份只能匹配没有地区的代理。
   把正在被调度的身份的地区和池中的地区对照一下。
4. **是不是全都在冷却？** 详情面板显示各站点冷却，全局冷却在记录本身上。大量 `proxy_error` 上报叠加默认的
   `proxy-error-cooldown` 规则，会让代理冷却长达 30 分钟。先解决根因；冷却是唯一可以手工撤销的一项——
   手动冷却会覆盖自动冷却，延长和缩短都可以。
5. **是不是全都饱和了？** 整个池的站点内 `active_leases` 都顶在 `max_concurrency`，说明你需要更多代理、更高的
   `max_concurrency`，或更少的并发租约。看节点收到的 retry-after：它是最早可用代理的等待时间，接近租约 TTL 说明是容量问题，
   而 60 秒说明近期没有任何代理会恢复。
6. **在 `bind_identity` 模式下：** 绑定代理已坏且当天换绑额度用尽的身份会被推后 10 分钟，看起来像身份不足。
   提高 `max_rebinds_per_day`，或者修好代理。

`no_proxy_available` 是带 retry-after 的 `RESOURCE_EXHAUSTED`；节点应当退避重试，而不是让任务失败。见
[故障排查](./18-troubleshooting.md)。

### 其他症状

| 症状 | 可能原因 |
| --- | --- |
| 代理在 `active` 与 `dead` 之间反复跳动 | 检测超时对该供应商的延迟来说太紧，或检测 URL 在对检测器限流。调大 `SPINNERET_PROXY_CHECK_TIMEOUT`，或把 `SPINNERET_PROXY_CHECK_URL` 指向你自己控制的主机 |
| 检测通过但请求全失败 | 检测 URL 经代理可达，而目标不可达。用真实目标 URL 验证一次 |
| 所有代理评分都是 70、样本数为 0 | 没有任何上报携带代理——该端点组的轮换策略多半还是 `mode: none` |
| 某家供应商的风险率远高于其他 | 该供应商的出口正在被识别。在它烧掉身份之前先冷却它或用标签排除它 |
| 导入结果全是 `unchanged` | 这些 URL 已存在，且行内没有设置任何与现值不同的属性。请显式设置要修改的属性，或通过 `defaults` 设置 |
| 出口 IP 一直为空 | 未设置 `SPINNERET_PROXY_EXIT_IP_URL`，或该地址经代理不可达 |

### 日常维护

- 定期重新导入供应商的当前列表。导入是幂等的：已存在的代理被更新而不是被重复创建，新的会被建出来。
- 不再续费的代理请用 `archive` 而不是删除；既保留历史，供应商统计在时间上也仍然可比。
- 供应商故障恢复后，对受影响的代理执行 `reset_stats`，清掉被压低的评分，让它们立刻重新公平竞争，而不必等 6 小时衰减。
- 备份 KEK。没有它就无法解密已存储的代理 URL。见[运维手册](./16-operations.md)。

---

## 下一步

- [策略](./08-policies.md) —— 决定分配模式与过滤条件的轮换策略，以及冷却和封禁代理的动作策略。
- [身份与账号](./06-identities.md) —— 租约的另一半，以及地区匹配所比较的身份地区。
- [节点 API 参考](./13-node-api.md) —— 节点在 `AcquireResponse.proxy` 中收到什么，以及如何上报代理故障。
- [可观测性与告警](./12-observability.md) —— `spinneret_proxies`、风险事件与请求明细。
- [配置参考](./03-configuration.md) —— `SPINNERET_PROXY_*` 与 `SPINNERET_GEOIP_DB` 变量。
- [故障排查](./18-troubleshooting.md) —— 完整的错误原因表。
