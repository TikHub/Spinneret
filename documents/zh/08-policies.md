# 策略

**策略决定了 Spinneret 如何挑选身份、如何读懂请求结果、以及结果出来之后做什么。本页覆盖四类策略的完整 YAML、草稿与发布流程、绑定层级、规则调试器和影子模式。**

[English](../en/08-policies.md)

---

## 目录

- [什么是策略](#什么是策略)
- [生命周期：草稿、发布、版本、回滚](#生命周期草稿发布版本回滚)
- [绑定与生效解析](#绑定与生效解析)
- [所有策略共有的字段](#所有策略共有的字段)
- [轮换策略](#轮换策略)
- [信号策略](#信号策略)
- [动作策略](#动作策略)
- [熔断策略](#熔断策略)
- [规则调试器](#规则调试器)
- [安全地试策略：影子模式](#安全地试策略影子模式)
- [排查：我的策略好像没生效](#排查我的策略好像没生效)

---

## 什么是策略

一条策略就是一份带名字、带版本的 YAML 文档，它属于某一个命名空间，属于某一个种类。编辑时它是**草稿**，满意之后**发布**成一个不可变的版本号，再**绑定**到命名空间层级中的某个位置才真正开始起作用。

种类正好四种，任何时刻每个端点组都各有一条在生效：

| 种类 | 控制台名称 | 控制什么 |
| --- | --- | --- |
| `rotation` | 轮换 | 哪些身份类型可用、如何挑选、租约多久、配额、并发、会话粘性，以及配哪一个代理。 |
| `signal` | 识别 | 一组有序规则，把上报的原始事实（状态码、业务码、错误类型、标记、URI、方法、延迟、大小）归类成十二种**结果分类**之一并判定**归因**。 |
| `action` | 处置 | 每种结果分类做什么：冷却、封禁、失效、隔离、健康分、封禁升级阶梯和交叉归因。 |
| `breaker` | 熔断 | 端点组什么时候熔断打开、打开多久、如何探测恢复、打开时最近的冷却怎么处理。 |

每条上报都会经过其中三类，顺序是：

```text
上报  ──信号策略──▶  结果分类 + 归因  ──动作策略──▶  计划的处置
                          │
                          └──▶ 熔断窗口计数 ──熔断策略──▶ 熔断 / 半开 / 关闭
```

轮换策略在另一侧，在 `Acquire` 时运行，消费的正是动作策略和熔断策略写出来的状态。完整的请求链路见[核心概念](./04-concepts.md)。

每个新建的命名空间都会自带四条内置默认策略——`default-rotation`、`default-signal`、`default-action`、`default-breaker`——以版本 1 发布并绑定在命名空间级。也就是说，你一条策略都还没写，系统就已经能跑了。

### 四条内置默认策略

它们就是一个全新命名空间真正在跑的东西，也是绑定层级最底下那个 `builtin`。`default-rotation` 的内容正好是下面[轮换策略字段表](#轮换策略)里的全部默认值，`default-breaker` 正好是[熔断策略字段表](#熔断策略)里的全部默认值。另外两条值得在写自己的策略之前先读一遍。

`default-signal`——11 条规则，第一条匹配的胜出，`trust_outcome_hint: false`：

| 规则 | 条件 | 结果分类 |
| --- | --- | --- |
| `proxy-error` | `error_kind: [proxy_auth, conn_refused]` | `proxy_error` |
| `network-error` | `error_kind: [timeout, conn_reset, tls, dns]` | `network_error` |
| `captcha` | `markers: [captcha_page]` | `captcha` |
| `login-redirect` | `markers: [login_redirect]` | `auth_invalid` |
| `rate-limited` | `http_status: [429]` | `rate_limited` |
| `target-error` | `http_status: { gte: 500 }` | `target_error` |
| `empty-list` | `http_status: [200]` 且 `markers: [empty_list]` | `empty` |
| `client-error` | `http_status: [400]` | `client_error` |
| `auth-invalid` | `http_status: [401]` | `auth_invalid` |
| `forbidden` | `http_status: [403]` | `forbidden` |
| `success` | `http_status: { gte: 200, lt: 300 }` | `success` |

一条都匹配不上的上报就是 `unknown` + 归因 `none`。`captcha_page`、`login_redirect`、`empty_list` 这三个标记是默认策略期待你的节点上报上来的——没有谁会替你识别它们。

`default-action`——7 条规则，每条匹配的都会求值：

| 规则 | 响应什么 | 处置 | 范围 | 时长参数 |
| --- | --- | --- | --- | --- |
| `rate-limited-cooldown` | `rate_limited` | `cooldown` | `identity_endpoint` | `base: 60s`、`multiplier: 2`、`max: 30m` |
| `empty-cooldown` | `empty`，`10m` 内 3 次 | `cooldown` | `identity_endpoint` | `base: 5m`，恒定 |
| `captcha-cooldown` | `captcha` | `cooldown` | `identity_site` | `base: 30m`、`multiplier: 2`、`max: 6h` |
| `captcha-ban` | `captcha`，`24h` 内 3 次 | `ban` | `identity` | `12h` |
| `auth-invalid-expire` | `auth_invalid` | `expire` | `identity` | — |
| `banned-account` | `banned` | `ban` | `account` | `permanent` |
| `proxy-error-cooldown` | `proxy_error` | `cooldown` | `proxy_site` | `base: 2m`、`max: 30m`，恒定 |

再加上升级阶梯（`7d` 内 2 次封禁 → `72h`，`30d` 内 3 次封禁 → `permanent`）、[动作策略](#动作策略)一节里列出的那套健康分默认值、`ban_expiry_state: pending`，以及 `cross_attribution` 开启且两个阈值都是 3。

注意里面**没有**什么：没有任何规则响应 `network_error`、`forbidden`、`target_error`、`client_error` 或 `unknown`。`forbidden` 仍然会拉低健康分（`network_error` 在归因包含身份时也会），所以这两个还能通过 `health.endpoint_low` 和 `health.quarantine` 影响到身份——但除此之外，你不写规则就什么都不会发生。

![策略](../images/policies.png)

策略在控制台的**调度 → 策略**下。页面有四个视图：**策略**（编辑器）、**绑定**、**生效解析**和**规则调试器**。

---

## 生命周期：草稿、发布、版本、回滚

一条策略同时有两份内容：**草稿**（可改，是你正在做的工作）和**当前已发布版本**（不可变，是系统真正在跑的东西）。

```text
创建 ──▶ 草稿 ──保存草稿──▶ 草稿 ──发布──▶ v1 ──▶ 草稿 ──发布──▶ v2
                                            │
                                            └──回滚到 v1──▶ v3（内容 = v1 的 YAML）
```

| 操作 | 权限 | 发生了什么 |
| --- | --- | --- |
| 创建 | `policy:write`（若同时发布还需 `policy:publish`） | 解析并校验 YAML；`name`、`description` 和可选的 `bind` 块都取自文档本身。存为草稿，或直接发布成版本 1。 |
| 保存草稿 | `policy:write` | YAML 必须能解析并通过同一种类的校验，且 `name` 不能改。此时**不**检查 `extends` 引用和 `bind` 目标。 |
| 发布 | `policy:publish` | 校验草稿，`extends` 链必须能在**已发布**的策略中解析出来并编译通过，文档成为版本 `当前 + 1`，草稿被清空。 |
| 回滚 | `policy:publish` | 把某个旧版本的 YAML 重新发布成一个**新**版本。草稿保持不动。回滚到已经是当前版本的那一版会被拒绝。 |
| 删除 | `policy:write` **且** `policy:publish` | 策略、草稿和全部版本一起永久删除。若仍有已发布策略 `extends` 它则拒绝；若是绑定在命名空间级的 `default-<kind>` 策略也拒绝。 |

另外几个常用操作：

- **校验**（`policy:read`）只解析不存储，返回问题清单（每条形如 `path: message`，例如 `rules[2].scope: ban does not support scope identity_endpoint (allowed: identity|account|proxy)`），或者返回**规范化 YAML**——把所有默认值都填好的文档。想知道某个默认值到底是多少，这是最快的办法。
- **对比**比较两侧内容。在 API 里 `from_version: 0` 表示当前已发布版本，`to_version: 0` 表示草稿（没有草稿时是当前已发布版本）；响应里带两份文档和一份三行上下文的 unified diff。控制台里对应**版本**标签页：选两个版本，或者一个版本和草稿。
- **乐观并发**：发布时把 `expected_version` 设成一个正数，若策略实际不在那个版本上就以冲突失败。两个人同时改同一条策略时用它。

每次写入都受这些限制：

| 限制 | 取值 |
| --- | --- |
| YAML 文档大小 | 1 MiB |
| 发布备注 | 1024 个字符 |
| 单条策略的版本数 | 最多 2147483647 |
| 策略名 | `^[a-z0-9][a-z0-9._-]{0,63}$`，在命名空间内按种类唯一，**创建后不可修改** |
| `extends` 链深度 | 含叶子共 5 条策略 |

发布不是延迟生效的。事务提交后，本实例的命名空间目录快照立即失效，并通知其他实例（它们在 100 ms 去抖后重载），同时通过事件流向控制台推送一条 `policy.published` 事件。另外每 60 秒还有一次兜底全量重载。实际上，一条策略发布后一两秒内就会在所有实例上生效。

每次写入都会记审计：`policy.create`、`policy.save_draft`、`policy.publish`、`policy.rollback`、`policy.delete`、`policy.bind`、`policy.unbind`——见[租户、用户与令牌](./11-access-control.md)。

---

## 绑定与生效解析

策略不绑定就什么都不做。一条**绑定**把一条策略挂到一个*目标*上——命名空间、某个站点、某个站点 + 客户端，或者某个站点 + 客户端 + 端点组——同一个目标、同一种类最多只能有一条绑定。也就是说：每种类在命名空间级一条，每个站点一条，每个站点 + 客户端一条，每个站点 + 客户端 + 端点组一条。两个站点完全可以各有一条自己的站点级动作绑定；一个层级不是只有一个位置。

| 层级 | 绑定指定什么 | 压过谁 |
| --- | --- | --- |
| `endpoint_group` | 站点 + 客户端 + 端点组 | 下面所有层级 |
| `client` | 站点 + 客户端 | 站点、命名空间、内置 |
| `site` | 站点 | 命名空间、内置 |
| `namespace` | 什么都不指定 | 内置 |
| `builtin` | — | 没有；这是编译进服务端的兜底 |

解析从最精确往最宽泛走，取第一个有该种类绑定的层级。解析是按种类分别做的：同一个端点组完全可以从端点组绑定拿轮换策略，同时从命名空间绑定拿熔断策略。

有两条规则容易踩坑：

- 绑定行里写了客户端或端点组但没写站点，永远匹配不上任何目标。
- 万一同一层级有多行，取第一行。

### 一个完整的例子

命名空间 `crawlers` 下有站点 `example-site`，站点有客户端 `web` 和 `mobile`，`web` 下有端点组 `search` 和 `detail`。假设存在这些动作策略绑定：

| 绑定 | 层级 | 策略 |
| --- | --- | --- |
| （命名空间） | `namespace` | `default-action` |
| `example-site` | `site` | `site-strict` |
| `example-site` + `web` | `client` | `web-lenient` |
| `example-site` + `web` + `search` | `endpoint_group` | `search-aggressive` |

那么实际生效的动作策略是：

| 目标 | 胜出层级 | 策略 |
| --- | --- | --- |
| `example-site` / `web` / `search` | `endpoint_group` | `search-aggressive` |
| `example-site` / `web` / `detail` | `client` | `web-lenient` |
| `example-site` / `mobile` / 任意 | `site` | `site-strict` |
| 命名空间内的另一个站点 | `namespace` | `default-action` |

再把命名空间绑定也删掉，最后一行又落回 `default-action`——但这次是**内置**默认策略，而不是同名的那条存储策略。

### 怎么看到底哪条在生效

三种方式，权威性递增：

1. **控制台 → 策略 → 生效解析。**选站点、客户端和端点组，面板按种类各列一行：策略名、版本、胜出层级，还可以展开看**生效 YAML**。
2. **API** `ResolvePolicies`，也就是那个面板调用的接口。它按轮换、信号、动作、熔断的顺序返回四条 `ResolvedPolicy`——`kind`、`policy_id`、`name`、`version`、`level`、`yaml`。
3. **规则调试器**，它不仅解析，还会拿一条上报真的跑一遍。

对端点组来说，`ResolvePolicies` 报的是目录快照里的引用——也就是真正在生效的策略，而不是临时重算的结果。信号和动作策略返回的 YAML 是**展平**过的：`extends` 链已经应用，规则按根在前的顺序拼接，文件头的注释写明整条链。

**注意。**如果某个已存储的版本已经解析不了，或者它的 `extends` 链解析不出来，服务端会退回该种类的内置默认策略、记一条告警日志，并在 `ResolvePolicies` 里报内置默认。这是唯一一种"明明有绑定，控制台却显示 `builtin`"的情况。

### 从控制台绑定，还是在 YAML 里绑定

三个地方都可以绑：

- **策略 → 绑定 → 新建绑定**，或者某条策略的**绑定**标签页。需要目标上的 `policy:publish`（命名空间级绑定看命名空间，站点／客户端／端点组级绑定看站点），以及该策略的 `policy:read`。策略必须已经有发布版本。
- YAML 里可选的 **`bind` 块**（见下文）。它在策略第一次发布时应用，之后只有当它与当前版本的 `bind` 块不同时才再次应用。所以重复发布一个没改过的 `bind` 块，绝不会把你期间手工改过的绑定覆盖回去。
- API：`SetBinding` / `DeleteBinding`。

删掉一条绑定后，下一级不那么精确的绑定开始生效——如果那已经是最后一条，就落到内置默认。

---

## 所有策略共有的字段

| 字段 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `name` | 字符串 | — | 必填。`^[a-z0-9][a-z0-9._-]{0,63}$`。在命名空间内按种类唯一。策略创建后不可修改。 |
| `description` | 字符串 | `""` | 自由文本，最多 1024 个字符。显示在策略列表里。 |
| `bind` | 对象 | 无 | 可选的绑定目标，发布时应用。写了这个块就必须有 `bind.site`；`bind.client` 需要有站点；`bind.endpoint_group` 需要有客户端。每个值最多 128 个字符。 |
| `extends` | 字符串 | `""` | 仅信号和动作策略。同命名空间同种类的另一条策略名，它的规则在本策略的规则**之前**执行。 |

YAML 解析是严格的：未知字段直接报错，一份文档只能有一个 YAML document，空文档报错。时长写作 `500ms`、`30s`、`10m`、`24h`、`7d`、`1h30m`、`1d12h`、`0`，允许永久值的地方还可以写关键字 `permanent`。

没写的字段都会取默认值。但对那些"零值本身就是一个合法设置"的字段——`enabled: false`、某个熔断阈值写 `0`、`reuse_interval: 0s`、`max_rebinds_per_day: 0`——只有字段完全缺失时才填默认值，显式写出零值会被保留。

---

## 轮换策略

轮换策略回答 `Acquire` 提出的问题：*用哪个身份、租多久、走哪个代理？*

### 字段表

| 字段 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `identity_types` | 字符串列表 | `[]` | 本端点组可用的身份类型。为空表示该站点 + 客户端下的全部身份类型。每个名字最多 64 个字符。 |
| `rotation.strategy` | 枚举 | `weighted_random` | `weighted_random`、`least_recently_used`、`round_robin`、`best_health`。 |
| `rotation.candidate_sample` | 整数 | `32` | 每次租借从就绪集合中采样的候选数。1–256。 |
| `rotation.lease_ttl` | 时长 | `2m` | 一次 `Acquire` 或 `Renew` 授予的租约时长。5s–30m。 |
| `rotation.max_lease_lifetime` | 时长 | `30m` | 续约也不能把租约延长到超过这个值。不小于 `lease_ttl`，最大 24h。 |
| `rotation.max_concurrent_leases` | 整数 | `1` | 同一身份在本端点组上的并发租约数。`1` 表示独占。1–10000。 |
| `rotation.reuse_interval` | 时长 | `0s` | 同一身份两次使用之间的最小间隔。`0s` 表示不限制。 |
| `rotation.reuse_anchor` | 枚举 | `released` | 间隔从租约的 `acquired`（领取）还是 `released`（归还）时刻算起。 |
| `rotation.reuse_scope` | 枚举 | `endpoint_group` | 间隔只在本 `endpoint_group` 内生效，还是在整个 `site` 上生效。 |
| `rotation.quota` | `{limit, window}` 列表 | `[]` | 单身份在本端点组每个窗口内的请求数上限，按滑动窗口估算。`limit` ≥ 1，`window` ≥ 1s，窗口长度不能重复。 |
| `rotation.sticky.enabled` | 布尔 | `false` | `Acquire` 带上 `session_key` 时是否固定到同一个身份。 |
| `rotation.sticky.ttl` | 时长 | `10m` | 会话 → 身份映射的存活时间。至少 1s。 |
| `rotation.warmup.duration` | 时长 | `0s` | 身份激活后被视为"新身份"的时长。`0s` 表示不做预热。 |
| `rotation.warmup.quota_factor` | 浮点 | `1` | 预热期内配额的缩放系数。大于 0，不超过 1。 |
| `rotation.probe.weight_factor` | 浮点 | `0.1` | `weighted_random` 下，既是 `pending` 身份权重的缩放系数，**也是**这种候选被挑中之后真正被接受的概率。大于 0，不超过 1。 |
| `rotation.probe.max_leases` | 整数 | `2` | `pending` 身份的并发租约上限；比 `max_concurrent_leases` 小时取它。至少 1。 |
| `proxy.mode` | 枚举 | `none` | `none`、`pool`、`bind_identity`、`region_match`。 |
| `proxy.kinds` | 列表 | `[]` | 过滤：`datacenter`、`residential`、`mobile`、`tunnel`。为空表示不过滤。 |
| `proxy.tags` | 列表 | `[]` | 按代理标签过滤。 |
| `proxy.providers` | 列表 | `[]` | 按代理供应商过滤。 |
| `proxy.regions` | 列表 | `[]` | 按代理地区过滤。 |
| `proxy.region_match` | 布尔 | `false` | 要求代理地区与身份地区一致。 |
| `proxy.rebind_tolerance` | 时长 | `5m` | `bind_identity` 模式下，绑定代理正在冷却时最多等多久，超过就换绑。 |
| `proxy.max_rebinds_per_day` | 整数 | `3` | `bind_identity` 模式下，一个身份每天最多换绑几次。`0` 表示禁止换绑。 |

代理模式的完整说明见[代理池](./07-proxies.md)。

四种挑选策略：

| 策略 | 怎么挑 | 看健康分吗 |
| --- | --- | --- |
| `weighted_random` | 随机挑，权重是候选衰减后健康分的**平方**（分值先夹到 5–100）。所以 100 分被挑中的概率是 50 分的四倍、是 5 分的 400 倍。状态在变差的身份是慢慢淡出流量，而不是被一刀切掉。 | 看 |
| `best_health` | 挑衰减后分数最高的那个。半开探测不管策略写的是什么，一律强制用这个。 | 看 |
| `least_recently_used` | 挑上次使用时间最早的那个。 | 不看 |
| `round_robin` | 挑本端点组上次挑过的那个 id 之后最小的身份 id，到头了就绕回本次采样里最小的 id。游标按端点组分别保存。 | 不看 |

### 这些参数在租借时如何互相作用

- 先看熔断器。熔断打开时直接拒绝租借；半开时每 10 秒最多放出 `half_open.probe_leases_per_10s` 个探测租约，而且探测一律用 `best_health` 并忽略会话粘性。
- 如果开了会话粘性、请求带了 session key、而且这次**只**要一个租约，就先试固定的那个身份，并且允许它跳过复用间隔。一次要多个租约的批量租借会完全忽略 session key。
- 否则调度器从就绪集合中采样最多 `candidate_sample` 个身份，按上面那张表交给策略挑。`weighted_random` 下，`pending` 身份的权重会乘上 `probe.weight_factor`，*而且*被挑中之后还只以同样的概率被接受，所以它实际拿到的流量大约是原本的 `weight_factor²`。
- 被挑中的候选再走过滤：身份状态、冷却、复用间隔、并发上限（`pending` 身份用 `probe.max_leases`）、配额窗口，以及 `bind_identity` 模式下的绑定代理。过不了的候选会在就绪集合里被推到它下一次可用的时刻，策略再挑一个。
- 身份激活后处在 `warmup.duration` 之内时，配额窗口会乘上 `warmup.quota_factor`，下限为 1 个请求。

### 示例：独占身份 + 小时级配额

```yaml
name: search-rotation
description: 每个身份同时只租一次，每小时 60 个请求，不走代理。
bind:
  site: example-site
  client: web
  endpoint_group: search
identity_types: [cookie]
rotation:
  strategy: weighted_random
  candidate_sample: 32
  lease_ttl: 90s
  max_lease_lifetime: 10m
  max_concurrent_leases: 1        # 独占
  reuse_interval: 45s             # 两次使用之间让身份歇一会儿
  reuse_anchor: released
  reuse_scope: site               # 间隔在整个站点上生效
  quota:
    - { limit: 60, window: 1h }
    - { limit: 600, window: 24h }
  warmup:
    duration: 24h                 # 新身份头一天只给 10% 的配额
    quota_factor: 0.1
  probe:
    weight_factor: 0.05
    max_leases: 1
proxy:
  mode: none
```

### 示例：住宅代理池上的会话粘性

```yaml
name: detail-sticky
description: 把一个会话固定到一个身份和一个住宅代理地区。
bind:
  site: example-site
  client: mobile
  endpoint_group: detail
identity_types: [device, cookie]
rotation:
  strategy: best_health
  candidate_sample: 64
  lease_ttl: 5m
  max_lease_lifetime: 30m
  max_concurrent_leases: 4        # 四个 worker 可以共用一个身份
  reuse_interval: 0s
  sticky:
    enabled: true
    ttl: 30m                      # 一个 session_key 半小时内保持同一身份
proxy:
  mode: bind_identity             # 同一身份始终从同一个代理出口
  kinds: [residential]
  regions: [eu-west]
  region_match: true
  rebind_tolerance: 2m
  max_rebinds_per_day: 1
```

---

## 信号策略

信号策略是一串有序规则。每条规则是一组条件加一个结果分类。**第一条匹配的规则胜出**，它的结果分类和归因就是这条上报的分类结论。

### 字段表

| 字段 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `trust_outcome_hint` | 布尔 | `false` | 没有规则匹配时，如果节点自己给的 `outcome_hint` 是一个合法结果分类就采用它。在 `extends` 链中取叶子策略的值。 |
| `rules` | 列表 | `[]` | 规则，按求值顺序排列。 |
| `rules[].name` | 字符串 | `""` | 可选。与策略名同样的命名规则，在策略内唯一。没名字的规则标记为 `<policy>.rules[i]`。 |
| `rules[].when` | 对象 | `{}` | 条件。**写出来的条件必须全部成立。**没有条件的规则匹配任何上报。 |
| `rules[].outcome` | 枚举 | — | 必填。下面十二种结果分类之一。 |
| `rules[].blame` | 枚举 | — | 可选，覆盖该结果分类的默认归因：`none`、`identity`、`proxy`、`both`。 |

### 条件语言

| 条件 | 写法 | 何时匹配 |
| --- | --- | --- |
| `http_status` | 单个整数、整数列表，或范围 `{gte, gt, lte, lt}` | 列表形式精确匹配上报的状态码；列表里的 `0` 匹配**没有**状态码的上报。范围形式只会匹配到真实存在的状态码（缺失状态码永远匹配不上范围）。取值 0–999。十进制字面量不允许有前导零。 |
| `business_code` | 字符串或字符串列表 | 上报的业务码非空且在列表里。数字会规范化成十进制字符串形式，所以 `[10001]` 和 `["10001"]` 等价。 |
| `error_kind` | 列表 | 节点报的传输层错误类型在列表里：`timeout`、`conn_reset`、`conn_refused`、`proxy_auth`、`tls`、`dns`、`other`。 |
| `markers` | 列表 | 节点上报的标记中**任意一个**在列表里。 |
| `uri` | `{prefix: /path}` 或 `{regex: ...}` | 二选一。`prefix` 必须以 `/` 开头，对规范化后的请求路径做前缀匹配。`regex` 是 RE2，在路径上做包含匹配。各自最多 1024 个字符。 |
| `method` | 列表 | 请求方法在列表里，大小写不敏感。只能是字母，每个 token 最多 16 个字符。 |
| `latency_ms` | 范围 `{gte, gt, lte, lt}` | 上报的延迟（毫秒）落在区间内。 |
| `response_bytes` | 范围 `{gte, gt, lte, lt}` | 上报的响应体大小落在区间内。 |

范围至少要有一个边界；`gte`/`gt` 与 `lte`/`lt` 互斥，空区间会校验失败。

### 结果分类

正好十二种。`blame` 决定这次结果算在身份头上、代理头上、两者都算还是都不算——动作规则是按它来放行的。

| 结果分类 | 默认归因 | 算风控类 | 推进身份失败连击数 |
| --- | --- | --- | --- |
| `success` | `none` | 否 | 否 |
| `empty` | `identity` | 否 | 是 |
| `rate_limited` | `both` | 是 | 是 |
| `captcha` | `identity` | 是 | 是 |
| `auth_invalid` | `identity` | 否 | 是 |
| `forbidden` | `identity` | 是 | 是 |
| `banned` | `identity` | 是 | 是 |
| `proxy_error` | `proxy` | 否 | 否 |
| `network_error` | `proxy` | 否 | 仅当归因到身份时 |
| `target_error` | `none` | 否 | 否 |
| `client_error` | `none` | 否 | 否 |
| `unknown` | `none` | 否 | 否 |

**风控类**结果分类会计入熔断器的风险比。最后一列说的是**身份**失败连击数，它是 `identity_endpoint`、`identity_site`、`identity`、`account` 冷却做指数退避的依据。

**注意。****代理**连击数是另一套规则：只要归因包含代理，不仅上面那些"算失败"的结果分类会推进它，`proxy_error` 和 `network_error` 也会。所以 `multiplier` 大于 1 的 `proxy_site` 或 `proxy` 冷却，在传输层错误上确实会指数退避，哪怕上表说它们不算身份失败。

没有规则匹配时，上报被判为 `unknown`、归因 `none`——除非开了 `trust_outcome_hint` 且节点给了合法的 `outcome_hint`，那就用这个提示值和它的默认归因。

### 继承一条信号策略

`extends` 做的是规则列表拼接：父策略的规则先跑，然后才是子策略的规则。因为第一条匹配的胜出，子策略只能**追加**兜底规则，无法覆盖父策略里更靠前的规则。整条链只在已发布策略中解析，最深 5 条，不允许成环。`trust_outcome_hint` 取自叶子策略。

### 示例：靠业务码传递信号的站点

```yaml
name: partner-api-signal
description: 该 API 用 200 + 业务码返回错误；验证码只能靠标记识别。
bind:
  site: partner-api
  client: web
trust_outcome_hint: false
rules:
  - name: proxy-error
    when: { error_kind: [proxy_auth, conn_refused] }
    outcome: proxy_error
  - name: network-error
    when: { error_kind: [timeout, conn_reset, tls, dns] }
    outcome: network_error
  - name: captcha
    when: { markers: [captcha_page] }
    outcome: captcha
  - name: quota-exhausted
    when: { http_status: [200], business_code: ["10429"] }
    outcome: rate_limited
  - name: token-expired
    when: { http_status: [200], business_code: ["10401", "10403"] }
    outcome: auth_invalid
  - name: account-banned
    when: { http_status: [200], business_code: ["10900"] }
    outcome: banned
  - name: empty-page
    when: { http_status: [200], markers: [empty_list] }
    outcome: empty
  - name: target-error
    when: { http_status: { gte: 500 } }
    outcome: target_error
  - name: success
    when: { http_status: { gte: 200, lt: 300 } }
    outcome: success
```

### 示例：在默认策略之上做端点级细化

```yaml
name: search-signal
description: 软封锁时 search 端点返回 200 但响应体是空的。
extends: default-signal          # 默认策略的 11 条规则先跑
bind:
  site: example-site
  client: web
  endpoint_group: search
rules:
  - name: soft-block
    when:
      http_status: [200]
      uri: { prefix: /api/search }
      response_bytes: { lt: 512 }
    outcome: empty
    blame: identity
  - name: slow-degraded
    when:
      http_status: [200]
      latency_ms: { gte: 15000 }
    outcome: target_error
```

**警告。**父策略里的 `success` 规则（`http_status: {gte: 200, lt: 300}`）已经能匹配 200 响应，所以上面这两条规则在这条链里永远轮不到。把细化规则放在父策略里、把宽泛的兜底规则放在叶子策略里，或者干脆写一条独立策略。到底是哪条规则匹配的，用规则调试器一看便知。

---

## 动作策略

动作策略把已分类的上报变成**计划的处置**。和信号规则不同，**每一条**匹配的动作规则都会被求值；最后每个对象最多留下一个处置。

### 字段表

| 字段 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `mode` | 枚举 | `enforce` | `enforce` 真正执行计划的处置；`shadow` 只记录。 |
| `ban_expiry_state` | 枚举 | `pending` | 临时封禁到期后身份进入的状态：`pending`（需重新验证）或 `active`。 |
| `rules` | 列表 | `[]` | 动作规则。 |
| `escalation` | 列表 | `[]` | 让反复的临时封禁越来越长的升级阶梯。 |
| `health.*` | 对象 | 见下 | 健康分及其驱动的处置。 |
| `cross_attribution.*` | 对象 | 见下 | 在身份与代理之间重新分配风险归因。 |

#### `rules[]`

| 字段 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `name` | 字符串 | `""` | 可选，策略内唯一。`lifecycle.activate`、`health.endpoint_low`、`health.quarantine` 是保留名。 |
| `when.outcome` | 结果分类或列表 | — | 必填。本规则响应的结果分类，不能重复。 |
| `when.count.gte` | 整数 | — | 可选。要求该对象在窗口内至少有这么多次该结果分类。至少为 1。 |
| `when.count.within` | 时长 | — | 计数条件的滑动窗口。至少 1s。 |
| `action` | 枚举 | — | `cooldown`、`expire`、`quarantine`、`ban`。 |
| `scope` | 枚举 | — | `identity_endpoint`、`identity_site`、`identity`、`account`、`proxy_site`、`proxy`。 |
| `base` | 时长 | — | **仅 cooldown。**必填，至少 1ms。连击中的第一次冷却时长。 |
| `multiplier` | 浮点 | `1` | **仅 cooldown。**有限数，至少为 1。 |
| `max` | 时长 | `24h` | **仅 cooldown。**计算出的冷却时长上限；不小于 `base`。 |
| `max_exponent` | 整数 | `10` | **仅 cooldown。**指数上限，0–64。因为 `0` 表示"未设置"，要写一个恒定冷却应该用 `multiplier: 1`，而不是 `max_exponent: 0`。 |
| `failure_reset_after` | 时长 | `1h` | **仅 cooldown。**多久没有失败就重置连击数。至少 1s。 |
| `duration` | 时长 | — | **仅 ban 与 quarantine。**必填。至少 1s；`ban` 还接受 `permanent`。`cooldown` 和 `expire` 上不允许写。 |

#### 每种处置做什么

| 处置 | 效果 | 允许的范围 |
| --- | --- | --- |
| `cooldown` | 对象在该范围内暂时不可用，直到冷却结束。时长随失败连击数指数增长。 | `identity_endpoint`、`identity_site`、`identity`、`account`、`proxy_site`、`proxy` |
| `quarantine` | 身份（或代理）被移出轮换固定一段时间；身份结束后变为 `pending`。 | `identity`、`proxy` |
| `expire` | 身份被标记为 `expired`，在载荷更新之前一直不可用。没有时长。 | `identity` |
| `ban` | 对象被封禁一段时间或 `permanent`。临时的身份封禁和账号封禁会走升级阶梯。 | `identity`、`account`、`proxy` |

范围从窄到宽的顺序是 `identity_endpoint` < `identity_site` < `identity`，以及 `proxy_site` < `proxy`。写成 `scope: identity` 的 `cooldown` 在编译时会被规范化成 `identity_site`——身份级这个范围留给更重的处置。

#### 求值顺序

1. `pending` 状态的身份上报 `success` 时，生命周期给出一个 `activate` 处置（规则名 `lifecycle.activate`）。
2. 所有 `when.outcome` 包含当前结果分类的规则都参与。带 `count` 条件的规则，要求 `(该范围对应的对象, 结果分类, 窗口)` 的计数器达到 `gte`。
3. **归因放行。**身份级和账号级的规则只有在归因包含身份时才触发。代理级的规则只有在归因包含代理**并且**这次确实用了代理时才触发。
4. **账号降级。**如果规则是账号级的但身份没有账号，处置回落到 `identity_site`（冷却）或 `identity`（其他）。
5. **健康分处置**：当归因包含身份、或者结果分类属于失败类时追加——低分端点冷却（`health.endpoint_low`）和隔离（`health.quarantine`），阈值见下。
6. **每个对象只留一个。**计划出来的处置按对象（身份、账号、代理）各自按严重度取一个：`activate` 0 < `cooldown` 1 < `quarantine` 2 < `expire` 3 < 临时 `ban` 4 < 永久 `ban` 5。同级比时长更长的，再比范围更宽的，再比规则下标更小的。因此只要还有别的身份处置，激活总会被丢掉。

#### 冷却时长的算法

```text
时长 = min(base × multiplier^min(连击数 − 1, max_exponent), max) × 抖动
```

连击数取对应范围的连续失败计数：`identity_endpoint` 取身份 × 端点组的连击数，`proxy_site` 和 `proxy` 取代理连击数，其余取身份 × 站点的连击数。抖动取自 `[0.8, 1.2]`。结果四舍五入到整毫秒，且不低于 1ms。

连击数是双向走的：一次失败加一，一次 `success` 把它**减半**（整数除法，5 变成 2），距上次失败超过 `failure_reset_after` 就归零。热路径实际使用的重置时长是整条链中冷却规则里**最长**的那个 `failure_reset_after`。

用 `base: 60s, multiplier: 2, max: 30m`，阶梯就是 60s、2m、4m、8m、16m、30m、30m、……

#### 升级阶梯

```yaml
escalation:
  - when: { bans: { gte: 2, within: 7d } }
    duration: 72h
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
```

只在计划**临时的身份封禁或账号封禁**时求值（代理封禁永远不升级，因为封禁历史是按身份记录的）。`bans` 统计该身份在窗口内此前的封禁次数，再加上正在施加的这一次。命中的阶梯里阈值最高的胜出；同阈值取时长更长的，而且阶梯永远不会**缩短**封禁。各级必须按 `gte` 严格递增排列且时长不递减，`within` 最长 30 天。在 `extends` 链里，升级阶梯取叶子策略（如果它定义了），否则取最近一个定义了它的祖先。

#### 健康分

每个身份都有一个按端点组的指数加权分和一个全局分，取值 0–100。

| 字段 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `health.alpha` | 浮点 | `0.1` | 新观测值在 EWMA 中的权重。大于 0，不超过 1。 |
| `health.baseline` | 浮点 | `70` | 新身份的初始分，也是分数衰减回归的目标。0–100。 |
| `health.tau` | 时长 | `6h` | 衰减时间常数：`score ← baseline + (score − baseline)·e^(−Δt/tau)`。至少 1s。 |
| `health.observations` | 结果分类 → 0–100 的映射 | `{}` | 覆盖某个结果分类的观测值。内置值：`success` 100、`empty` 60、`network_error` 50、`rate_limited` 30、`forbidden` 10、`captcha` 0。其余结果分类不影响分数。 |
| `health.endpoint_low_score` | 浮点 | `15` | 端点分低于此值就计划一次冷却。0–100。 |
| `health.endpoint_low_min_samples` | 整数 | `10` | 端点样本数达到多少之后才应用上一条。至少 1。 |
| `health.endpoint_low_cooldown` | 时长 | `6h` | 这次冷却的时长。已有不短于这个长度的冷却在跑时不会重复施加。至少 1s。 |
| `health.quarantine_score` | 浮点 | `20` | 全局分低于此值就隔离身份。0–100。 |
| `health.quarantine_min_samples` | 整数 | `10` | 全局样本数达到多少之后才应用上一条。至少 1。 |
| `health.quarantine_duration` | 时长 | `24h` | 隔离时长。至少 1s。 |

`success` 一定会更新身份分；其他有观测值的结果分类只有在归因包含身份时才更新。代理有自己的按站点分数，观测值是固定的：`success` 100、`proxy_error` 0、`network_error` 40、`rate_limited` 30，且只在归因包含代理时应用。

#### 交叉归因

| 字段 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `cross_attribution.enabled` | 布尔 | `true` | 是否在身份与代理之间重新分配风险归因。 |
| `cross_attribution.window` | 时长 | `10m` | 配对历史的滑动窗口。至少 1s。 |
| `cross_attribution.proxy_distinct_identities` | 整数 | `3` | 窗口内同一个代理上有这么多个不同身份出问题，就归因到代理。至少 1。 |
| `cross_attribution.identity_distinct_proxies` | 整数 | `3` | 窗口内同一个身份在这么多个不同代理上出问题，就归因到身份。至少 1。 |

只有"风控类结果分类 + 同时有身份和代理"的上报才会走这一步，而且它在动作规则看到归因**之前**就改写了归因：若代理阈值满足而身份阈值不满足，归因变成 `proxy`；若身份阈值满足，身份被加进归因（于是 `proxy` 变成 `both`）。

### 示例：对一个容易封号的端点收紧

```yaml
name: search-aggressive
description: 被限流就大幅退避，一天三次验证码直接停用身份。
bind:
  site: example-site
  client: web
  endpoint_group: search
mode: enforce
rules:
  - name: rate-limited-cooldown
    when: { outcome: rate_limited }
    action: cooldown
    scope: identity_endpoint
    base: 2m
    multiplier: 3
    max: 2h
    max_exponent: 5
    failure_reset_after: 2h
  - name: captcha-cooldown
    when: { outcome: captcha }
    action: cooldown
    scope: identity_site
    base: 30m
    multiplier: 2
    max: 6h
  - name: captcha-ban
    when: { outcome: captcha, count: { gte: 3, within: 24h } }
    action: ban
    scope: identity
    duration: 12h
  - name: auth-invalid-expire
    when: { outcome: auth_invalid }
    action: expire
    scope: identity
  - name: banned-account
    when: { outcome: banned }
    action: ban
    scope: account
    duration: permanent
  - name: proxy-error-cooldown
    when: { outcome: [proxy_error, network_error] }
    action: cooldown
    scope: proxy_site
    base: 2m
    max: 30m
escalation:
  - when: { bans: { gte: 2, within: 7d } }
    duration: 72h
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
health:
  alpha: 0.2
  baseline: 70
  tau: 3h
  observations:
    empty: 40                     # 这里返回空比默认的 60 分更严重
  endpoint_low_score: 25
  endpoint_low_min_samples: 20
  endpoint_low_cooldown: 4h
  quarantine_score: 20
  quarantine_min_samples: 20
  quarantine_duration: 24h
ban_expiry_state: pending
cross_attribution:
  enabled: true
  window: 10m
  proxy_distinct_identities: 3
  identity_distinct_proxies: 3
```

### 示例：在共享基础策略之上的宽松策略

```yaml
name: web-lenient
description: 浏览类端点用更软的冷却；继承共享的基础规则。
extends: shared-base-action
bind:
  site: example-site
  client: web
mode: enforce
rules:
  - name: empty-cooldown
    when: { outcome: empty, count: { gte: 5, within: 15m } }
    action: cooldown
    scope: identity_endpoint
    base: 90s
    multiplier: 1                 # 恒定时长，不退避
  - name: forbidden-quarantine
    when: { outcome: forbidden, count: { gte: 2, within: 6h } }
    action: quarantine
    scope: identity
    duration: 12h
health:
  alpha: 0.05                     # 反应慢一点
  baseline: 75
  tau: 12h
  endpoint_low_score: 10
  endpoint_low_min_samples: 30
  endpoint_low_cooldown: 2h
  quarantine_score: 15
  quarantine_min_samples: 30
  quarantine_duration: 12h
ban_expiry_state: active
```

**注意。**在 `extends` 链里，`mode`、`health`、`cross_attribution` 和 `ban_expiry_state` 一律取自叶子策略。只有 `rules` 是拼接的，也只有 `escalation` 会回落到最近的祖先。

---

## 熔断策略

熔断器保护的是一整个端点组。它盯着一个滑动窗口的上报；当窗口里的数据足够糟糕时它就**打开**，该端点组的所有 `Acquire` 都会被拒绝，直到它探测着恢复过来。

![熔断器](../images/breakers.png)

### 字段表

| 字段 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `enabled` | 布尔 | `true` | 关掉之后熔断器永不触发。自动打开和半开状态会在下一次评估时关闭；手动打开会保留——带时长的保留到时长到期，无限期的一直保留到你手工关闭。 |
| `window` | 时长 | `60s` | 上报的滑动窗口。至少 1s。 |
| `buckets` | 整数 | `12` | 窗口切成多少个桶。1–60。必须能把窗口整除成整毫秒，且每个桶至少 1s。 |
| `min_requests` | 整数 | `50` | 窗口内上报数达到这个值之后才考虑触发条件。至少 1。 |
| `trip.risk_ratio_gte` | 浮点 | `0.4` | `风控数 / 总数` 达到此值就触发。0–1；`0` 关闭该条件。 |
| `trip.distinct_captcha_identities_gte` | 整数 | `10` | 窗口内有这么多个不同身份遇到验证码就触发。至少 0；`0` 关闭该条件。 |
| `trip.success_ratio_lte` | 浮点 | `0.2` | `成功数 / 总数` 跌到此值或以下就触发。0–1；`0` 关闭该条件。 |
| `open_duration` | 时长 | `2m` | 第一次打开的时长。至少 1s。 |
| `max_open_duration` | 时长 | `1h` | 打开时长翻倍后的上限。不小于 `open_duration`。 |
| `reset_open_count_after` | 时长 | `30m` | 保持关闭这么久之后，连续打开次数重置为 1。至少 1s。 |
| `half_open.probe_leases_per_10s` | 整数 | `5` | 半开时每 10 秒放出的租约数。至少 1。 |
| `half_open.close_min_samples` | 整数 | `5` | 做出半开判断前需要收到的探测上报数。至少 1。 |
| `half_open.close_success_ratio_gte` | 浮点 | `0.8` | 关闭所需的探测成功率。大于 0，不超过 1。 |
| `revert_recent_cooldowns` | 枚举 | `endpoint` | `none`、`endpoint` 或 `all`。也接受布尔值：`true` = `endpoint`，`false` = `none`。 |

熔断器启用时，三个触发条件中至少要有一个非零。

### 状态机

```text
            总数 ≥ min_requests 且某个触发条件成立
   closed ──────────────────────────────────────────────▶ open
     ▲                                                     │ 打开时段到期
     │ 探测成功率 ≥ close_success_ratio_gte                  ▼
     └───────────────────────── half_open ◀────────────────┘
                                   │ 探测成功率 < close_success_ratio_gte
                                   └────────────────────────▶ open（时长翻倍）
```

- **打开时长**：`min(open_duration × 2^(连续打开次数 − 1), max_open_duration)`。熔断器保持关闭达到 `reset_open_count_after` 之后，连续打开计数回到 1。用默认值时阶梯是 2m、4m、8m、16m、32m、1h、1h、……
- **半开**：该端点组每 10 秒最多放出 `half_open.probe_leases_per_10s` 个租约。探测一律用 `best_health` 选身份并忽略会话粘性，所以探测不会浪费在状态差的身份上。收到 `close_min_samples` 条探测上报之后，若成功率达到 `close_success_ratio_gte` 就关闭，否则重新打开（计数加一、时长翻倍）。
- **窗口清理**：在上次关闭之前就已开始的桶会被忽略，所以刚关闭的熔断器不会立刻被"当初触发它的那批样本"再次触发。
- **评估节奏**：每个实例每 5 秒跑一次扫描。候选是最近 2 分钟内有租借活动的端点组，外加所有非关闭状态的熔断器；每个端点组都有一把独立的锁，两个实例不会同时评估同一个组。

状态变更会带一条人可读的原因，写明条件和数字，例如 `risk_ratio 0.55 >= 0.40; captcha_identities 12 >= 10` 或 `probe success_ratio 0.60 < 0.80`。原因会随熔断事件一起存下来，在控制台显示，也会发到事件流上。

### 熔断打开时撤销冷却

熔断器自动打开时，此前因为站点自身抽风而施加的冷却通常是冤枉的——身份并没有做错什么。`revert_recent_cooldowns` 决定撤销哪些：

| 取值 | 效果 |
| --- | --- |
| `none` | 什么都不撤销。 |
| `endpoint` | 窗口内**由系统自己**施加到这个端点组上的身份 × 端点组冷却全部撤销，不管当初是哪种结果分类造成的——`empty` 和 `captcha` 一样撤。身份的失败连击数也一并还原。 |
| `all` | `endpoint` 做的都做，再加上窗口内施加的身份 × 站点冷却，于是该身份在这个站点的每个端点组里都重新可选。*其他*端点组的身份 × 端点组冷却不动。 |

你手工施加的冷却不会被碰；封禁、失效和隔离永远不会被撤销；**手动**打开也永远不撤销任何东西。

### 手动操作

手工打开或关闭熔断器不算改策略——它是对实时状态的操作，在**调度 → 熔断器**页面或熔断管理 API 上做，需要站点上的 `breaker:operate` 权限。

- 带时长的打开表现与自动打开一致，时长到期后进入半开。时长最长 365 天。
- 不带时长（或写 `permanent`）的打开是无限期的，永远不会自己离开打开状态。
- 关闭会立刻恢复租借，不经过半开探测。
- 每一次手动操作都会带上你填的原因写入审计日志。

### 示例：给脆弱端点用的快速灵敏熔断器

```yaml
name: search-breaker
description: 窗口短、样本门槛低、恢复快。
bind:
  site: example-site
  client: web
  endpoint_group: search
enabled: true
window: 30s
buckets: 6                        # 每桶 5 秒
min_requests: 20
trip:
  risk_ratio_gte: 0.25
  distinct_captcha_identities_gte: 5
  success_ratio_lte: 0.5
open_duration: 30s
max_open_duration: 10m
reset_open_count_after: 10m
half_open:
  probe_leases_per_10s: 3
  close_min_samples: 5
  close_success_ratio_gte: 0.8
revert_recent_cooldowns: endpoint
```

### 示例：给批量端点用的迟钝宽容熔断器

```yaml
name: bulk-breaker
description: 只在持续性崩溃时触发；打开时间长，不撤销任何冷却。
bind:
  site: partner-api
  client: partner
enabled: true
window: 10m
buckets: 20                       # 每桶 30 秒
min_requests: 500
trip:
  risk_ratio_gte: 0.6
  distinct_captcha_identities_gte: 0   # 这个 API 没有验证码，关掉该条件
  success_ratio_lte: 0.1
open_duration: 5m
max_open_duration: 2h
reset_open_count_after: 1h
half_open:
  probe_leases_per_10s: 1
  close_min_samples: 20
  close_success_ratio_gte: 0.9
revert_recent_cooldowns: none
```

---

## 规则调试器

调试器拿一条上报，在某个站点、客户端、端点组真正解析出来的策略上回放一遍。它什么都不碰：计数器不动，不写冷却，不发事件。需要站点上的 `policy:read` 权限。

入口在**策略 → 规则调试器**（命名空间级），或者某条策略的**调试器**标签页。

### 你要给它什么

| 输入 | 说明 |
| --- | --- |
| 站点和客户端 | 必填。 |
| 端点组，或者一个用来匹配的 URI | 直接选端点组，或者给一个 URI 让站点的 URI 规则去挑端点组。两者都不给时，用上报自己的 `uri` 去匹配。 |
| 上报 | `uri`、`method`、`http_status`、`business_code`、`error_kind`、`markers`、`outcome_hint`、`latency_ms`、`response_bytes`。控制台支持按节点发送的格式粘贴一条真实上报的 JSON。 |
| 身份状态 | `pending`、`active`、`expired`、`banned`、`quarantined`、`disabled`、`retired`。留空表示 `active`。 |
| `has_account`、`has_proxy` | 身份是否属于某个账号、这次是否用了代理——归因放行和账号降级都依赖它们。 |
| 计数器 | 键的格式是 `<subject>:<outcome>:<window>`，例如 `identity:captcha:24h`，值**包含**当前这条上报。规则需要但你没给的计数器一律按 1 计。最多 256 条。 |
| 封禁次数 | 按窗口作键，例如 `30d`，统计该身份此前的封禁次数，不含正在评估的这次。最多 32 条。 |
| 分数、样本数和连击数 | `endpoint_score`、`endpoint_samples`、`global_score`、`global_samples`（分数 0–100），`endpoint_streak`、`site_streak`（填 0 表示沿用端点连击数）、`proxy_streak`。 |
| 剩余端点冷却 | 例如 `5m`。已有冷却在跑时不会再叠加低分冷却。 |
| 草稿策略 | 可选，填一条信号**或**动作策略的 id，用它的草稿（没有草稿时用已发布 YAML）替换该种类解析出来的策略。这就是在发布之前试改动的方法。 |

### 它返回什么

| 输出 | 含义 |
| --- | --- |
| `outcome` | 判定出的结果分类。 |
| `blame` | `none`、`identity`、`proxy` 或 `both`。 |
| `matched_rule_index` / `matched_rule_name` | 胜出的信号规则；下标 `-1` 表示没有规则匹配，走了兜底。 |
| `actions[]` | 每个存活下来的处置一条：`action`、`scope`、`duration`（永久封禁是 `permanent`，激活为空）、`permanent`、`severity`、`rule_name`、`source`、`rule_index`。 |
| `counters[]` | 动作规则针对这个结果分类会读取的计数器——正是上面可以填的那些键。 |
| `mode` | `enforce` 或 `shadow`，取自解析出来的动作策略。 |

`source` 说明处置的来源：`rule`（动作规则）、`escalation`（封禁时长被阶梯抬高的动作规则）、`health`（`health.endpoint_low` 或 `health.quarantine`）、`lifecycle`（`lifecycle.activate`）。

### 怎么读结果

常见的排查节奏：

1. 先原样跑一遍这条上报。如果结果分类不对，那就是信号策略的问题——看 `matched_rule_name`，再看它**上面**那些你以为会匹配的规则。
2. 如果结果分类对了但什么都没发生，先看 `blame`。一条归因到代理的上报，身份级的规则本来就不会触发。
3. 带 `count` 条件的规则不触发时，看 `counters[]`，把它列出的那个计数器填成一个真实的值。
4. 如果两条规则都匹配却只回来一个处置，那就是"每个对象只留一个"的归并。`severity` 那一列会告诉你为什么是它。

---

## 安全地试策略：影子模式

给动作策略设 `mode: shadow` 再发布。之后这条策略照样被完整求值——规则照样匹配、冷却时长照样算、升级阶梯照样跑——但**什么都不会被执行**：不写冷却、不封身份、不发通知。

你仍然能拿到的东西：

- 每个计划的处置都有一条 `shadow = true` 的状态事件，能在身份历史和状态事件数据里看到。冷却事件只有在服务端的 `SPINNERET_RECORD_COOLDOWN_EVENTS` 打开时才记录，而它默认就是打开的。
- `spinneret_actions_total` 计数器带上 `mode="shadow"` 递增，于是你可以把"这条策略本来会做什么"和"线上策略实际做了什么"画在一张图上。

一次有风险的改动，通常这么走：

1. 把线上策略复制成一个新名字，改掉要改的部分，设 `mode: shadow`，发布并绑定到你想测的层级。
2. 跑够一个有代表性的时段，把 `spinneret_actions_total{mode="shadow"}` 和生效策略的计数对比一下，再挑几个身份读读它们的影子状态事件。
3. 改成 `mode: enforce` 再发布。万一出问题，回滚到上一个版本——旧的 YAML 一直都在。

如果只是想看一条上报，根本不需要影子模式：在规则调试器里设上 `draft_policy_id` 就能拿到答案，什么都不用发布。

影子模式只有动作策略才有。想安全地试信号策略就用调试器；想安全地试熔断策略，就把阈值先放宽，而不是去影子它。

---

## 排查：我的策略好像没生效

按这个清单往下查，大致是按"实际最常见"排的序。

| 现象 | 原因 | 怎么办 |
| --- | --- | --- |
| 改完什么都没变 | 改动还停在草稿上。 | 发布它。编辑器里策略旁边有**草稿**标记；**版本**标签页显示真正发布出去的是什么。 |
| 策略发布了，但跑的是另一条 | 有更精确的绑定赢了。 | 用准确的站点、客户端、端点组去看**策略 → 生效解析**。它会写明胜出的策略名、版本和层级。 |
| 明明绑了策略，生效解析却显示 `builtin` | 已存储的版本解析不了，或者它的 `extends` 链解析不出来；服务端退回了内置默认并记了告警。 | 校验那个版本的 YAML，重新发布一份好的，或者修好父策略。 |
| 某条动作规则从不触发 | 归因放行。身份级规则要求归因包含身份；代理级规则要求归因包含代理**并且**这次确实用了代理。 | 在调试器里看 `blame`。如果该站点的默认归因不合适，就在信号规则上显式写 `blame:`。 |
| 带 `count` 的动作规则从不触发 | 计数在 `within` 内到不了 `gte`，或者计数器的对象跟你想的不是同一个——它是规则**声明的**范围对应的对象。 | 看调试器里的 `counters[]`，它列出了规则真正读取的键。 |
| 两条规则都匹配，却只出现一个处置 | 每个对象只留一个，按严重度取。 | 这是预期行为。要么让较弱的那条规则落在不同的对象上，要么接受它。 |
| 写了 `scope: identity`，冷却却落在 `identity_site` 上 | 范围为 `identity` 的冷却在编译时会规范化成 `identity_site`。 | 明确写 `identity_endpoint` 或 `identity_site`；`identity` 留给 `ban`、`expire` 和 `quarantine`。 |
| 某条信号规则永远轮不到 | 前面的规则已经匹配了——包括通过 `extends` 继承来的规则，它们跑在**最前面**。 | 调试器会报 `matched_rule_name`。调整顺序，或者收窄前面那条规则。 |
| `http_status` 范围从来匹配不上 | 范围只匹配真实存在的状态码。缺失状态码要用列表形式 `[0]` 去匹配。 | 传输层失败用 `http_status: [0]`，或者加一个 `error_kind` 条件。 |
| 处置被计划出来了，身份却毫发无损 | 动作策略是 `mode: shadow`。 | 看调试器结果里的 `mode` 或者生效 YAML。用 `mode: enforce` 重新发布。 |
| 熔断器从不触发 | `enabled: false`，三个触发条件全是 `0`，或者窗口内的量根本到不了 `min_requests`。 | 看生效 YAML。拿 `min_requests` 和该端点组在 `window` 内的真实流量比一比。 |
| 熔断器反复触发 | `window` 太短、`min_requests` 太低，或者对一个本来就很吵的端点来说阈值太紧。 | 放宽窗口、提高 `min_requests`，或者提高 `risk_ratio_gte`。 |
| 熔断器关不上 | 半开探测一直失败，或者探测太少、凑不够 `close_min_samples`。 | 看熔断器页面上的探测指标。降低 `close_min_samples`，或者提高 `probe_leases_per_10s` 让判断有机会做出来。 |
| 某个实例上生效很慢 | 目录失效在实例之间有 100 ms 去抖，再加上每 60 秒一次的兜底全量重载。 | 等几秒。如果超过一分钟还没生效，检查事件总线和服务端日志。 |
| 发布被冲突拒绝 | 期间有别人发布过，而你带了 `expected_version`。 | 重新加载策略，把你的改动重新应用一遍，再发布。 |
| 删除策略被拒绝 | 还有已发布策略 `extends` 它，或者它是绑定在命名空间级的 `default-<kind>` 策略。 | 先把子策略改绑或删掉；删默认策略之前先在命名空间级绑定另一条。 |

---

## 下一步

- [核心概念](./04-concepts.md) —— 一个请求从 `Acquire` 到 `Report` 再回来的完整链路。
- [身份与账号](./06-identities.md) —— 冷却、封禁、隔离、失效分别对身份做了什么。
- [代理池](./07-proxies.md) —— 轮换策略可以选择的几种代理模式。
- [可观测性与告警](./12-observability.md) —— 熔断器页面、冷却热力图、风险事件，以及本页提到的那些指标。
- [节点 API 参考](./13-node-api.md) —— 信号条件所匹配的那些 `Report` 字段。
- [故障排查](./18-troubleshooting.md) —— 那些其实不是策略问题的现象。
