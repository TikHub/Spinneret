# 运维手册

[English](operations.md) · [部署指南](deployment.zh-CN.md) · [API 参考](api.zh-CN.md)

日常运维：部署跑起来之后如何组织资源、每个开关的作用，以及出问题时怎么办。下面所有操作在控制台里都能完成，
同时给出对应的 RPC 名称，便于脚本化。

- [租户、命名空间、用户与角色](#租户命名空间用户与角色)
- [API 令牌与作用域](#api-令牌与作用域)
- [站点、端点组与 URI 规则](#站点端点组与-uri-规则)
- [身份类型与导入](#身份类型与导入)
- [策略](#策略)
- [熔断与站点开关](#熔断与站点开关)
- [人工处置与回滚](#人工处置与回滚)
- [代理](#代理)
- [配置中心](#配置中心)
- [密钥管理](#密钥管理)
- [告警与 Webhook](#告警与-webhook)
- [监控](#监控)
- [热状态重建](#热状态重建)
- [KEK 轮换](#kek-轮换)
- [数据保留](#数据保留)

---

## 租户、命名空间、用户与角色

```
租户 tenant                隔离边界；用户通过角色绑定加入
└── 命名空间 namespace      拥有站点、代理、配置、密钥、令牌、策略
    └── 站点 site           拥有端点组、身份类型、身份、账号
```

`spnr admin init` 创建第一个平台管理员，以及租户 `default`、命名空间 `default` 和它的四条默认策略。
之后的一切在控制台（**Admin → Tenants**、**Access → Users**）或通过 `TenantAdminService` /
`AccessAdminService` 完成。

**租户**用于团队隔离：负责平台 A 的租户完全看不到平台 B。租户内部用命名空间区分环境（`prod`、`staging`）
或共用同一批运维人员的项目。控制台请求通过 `X-Spinneret-Tenant` 头携带当前租户，命名空间按**名称**寻址。

**角色**逐级累加：

| 角色 | 权限 |
| --- | --- |
| `viewer` | 全部 `*:read`，外加 `secret:list`、`dashboard:read`、`audit:read`、`notify:read` |
| `operator` | viewer + `identity:write` `identity:operate` `proxy:write` `proxy:operate` `policy:write` `config:write` `breaker:operate` |
| `admin` | operator + `site:write` `policy:publish` `config:publish` `secret:write` `secret:reveal` `identity:reveal` `token:read` `token:write` `notify:write` `namespace:read` |
| `owner` | admin + `namespace:write` `user:read` `user:write` |

一条角色绑定把用户固定到一个租户，并可选地固定到**一个命名空间**和**一组站点**。受站点限制的绑定无法操作
命名空间级资源（代理、配置、密钥、令牌、渠道）——唯一的例外是 `proxy:read`，以便运维仍能看到某次领取用了哪个代理。
`extra_permissions` 可以在不提升角色的前提下追加单项权限（`config:publish`、`secret:reveal`、
`identity:reveal`、`policy:publish`）。

平台管理员（`is_platform_admin`，只能由 `spnr admin init` 创建）通过所有权限检查，也是唯一能管理租户和 KEK 的角色，
数量越少越好。

实际做法：爬虫工程师给 `operator` 并绑定到各自的站点，团队负责人给命名空间级的 `admin`，`owner` 留给管理账号的人。
系统没有删除用户的 RPC，请改为禁用（**Access → Users → Disable**）——该用户的现有会话会在下一次请求时失效
（`session_invalid`）。重置口令同样会结束该用户的全部会话。

---

## API 令牌与作用域

节点使用 `Authorization: Bearer spn_…` 认证。令牌只属于一个命名空间，因此节点请求从不需要指定命名空间。

```bash
docker compose -f deploy/compose/docker-compose.yml exec spinneret \
  spnr token create --tenant default --namespace default --name crawler-hk \
    --scope lease:acquire:shop --scope report:write:shop \
    --scope config:read:crawler --scope 'secret:read:default/signing/*' \
    --expires 720h
```

明文只打印一次且不会存储——数据库里只有 SHA-256 哈希和 12 个字符的前缀。控制台（**Access → Tokens**）同样只展示一次。

| 作用域 | 授予 |
| --- | --- |
| `lease:acquire[:<site>]` | `Acquire`、`AcquireBatch`、`Renew`、`Release` |
| `report:write[:<site>]` | `Report` |
| `config:read[:<group glob>]` | `GetConfig`、`BatchGetConfig`、`WatchConfig` 以及管理侧的配置读取 |
| `config:publish[:<group glob>]` | 配置的读 + 写 + 发布 |
| `secret:read:<"命名空间/路径" 的 glob>` | `GetSecret`，以及解析 `${secret:…}` 和 `secret_ref` 字段 |
| `identity:write[:<site>]` | 身份的读、写和处置（供刷新服务使用） |
| `proxy:write` | 代理的读、写和处置 |
| `admin` | 令牌所属命名空间内的 `admin` 角色 |

不带 `:<site>` / `:<glob>` 后缀的作用域覆盖整个命名空间。请尽量收窄：只爬一个站点的节点不应该能领取另一个站点的身份。

令牌还可以配置 **IP 白名单**（CIDR）和**每实例速率限制**（次/秒），两者都在控制台设置。校验结果会缓存
`SPINNERET_TOKEN_CACHE_TTL`（30 秒），但吊销会发布事件立即清除缓存。

轮换方式：用临时名字创建新令牌 → 灰度下发 → 吊销旧的（**Access → Tokens → Revoke**）。令牌名只在**可用**令牌之间
按命名空间唯一，因此旧令牌一旦吊销，它的名字就重新可用，新令牌可以改名（或重建）沿用该名字。已吊销的令牌仍以原名
列出，作为审计记录。

---

## 站点、端点组与 URI 规则

一个**站点**代表一个目标平台（`shop`），下有一个或多个**客户端类型**（`web`、`app`）。站点可以被暂停，
此时所有 `Acquire` 都会返回 `site_paused`。

**端点组**是轮换、冷却和熔断的最小单位。每个 站点+客户端 自动拥有 `_default`；当不同接口需要不同待遇时再新建——
一个会激进限流的搜索接口不应该拖累信息流接口。

**URI 规则**把请求路径映射到端点组，匹配优先级固定：

| 类型 | 模式 | 示例 |
| --- | --- | --- |
| `exact` | 完整路径 | `/api/v1/feed` |
| `template` | 带 `{param}` 段的路径 | `/api/v1/item/{id}` |
| `prefix` | 路径前缀，最长者胜 | `/api/v1/search` |
| `regex` | RE2，按 position 顺序求值 | `^/api/v[0-9]+/user/[0-9]+$` |

优先级：`exact` > `template` > `prefix`（最长）> `regex`（按顺序）> `_default`。只匹配**路径**，查询串被忽略
（它常常携带签名值）。节点可以传完整 URL、路径，或直接传 `endpoint_group` 跳过匹配。

在依赖一套规则之前，先用 **Sites → URI tester**（`SiteAdminService/TestURI`）验证：它会显示命中的规则、
最终选中的端点组，以及为它解析出的四条策略。

为端点组设置**低水位**（low watermark），当可用身份数低于它时会触发 `identity_low_watermark` 告警。

> 冷却的粒度是端点组。如果某个具体 URI 需要独立冷却，就为它建一个带 `exact` 规则的端点组——这是设计上预留的粒度出口。

---

## 身份类型与导入

**身份类型**声明身份的载荷字段以及如何交付给节点，它属于某个站点和客户端类型。

```yaml
name: web_cookie
site: shop
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
  signature:  { type: string, sensitive: true }
unique_by: [cookies.sessionid]     # 默认：全部必填字段
activation: probe                  # probe | immediate
deliver:
  cookies: "{{ cookies }}"
  cookie_header: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
  values:
    signature: "{{ signature }}"
```

```yaml
name: app_device
site: shop
client: app
fields:
  device_id:  { type: string, required: true }
  install_id: { type: string, required: true }
  cookies:    { type: cookie_map, sensitive: true }
  extra:      { type: json }
unique_by: [device_id]
activation: immediate
deliver:
  query:
    device_id: "{{ device_id }}"
    install_id: "{{ install_id }}"
  cookie_header: "{{ cookies }}"
  json: "{{ extra }}"
```

字段类型：`string`、`number`、`bool`、`cookie_map`、`json`、`secret_ref`。标记 `sensitive: true` 的字段加密存储、
在控制台脱敏（除非调用者有 `identity:reveal`）、且永不写日志。`secret_ref` 字段存的是 **Vault 路径**，在领取时
解析进凭证——写入这类载荷需要对该密钥有读权限。

交付模板只支持占位符，不支持表达式。值恰好是 `{{ path }}` 时输出原始类型值，否则按字符串插值。
`cookie_map` 渲染进 `cookie_header` 会拼接成 `k1=v1; k2=v2`（键排序以保证确定性）。生成的凭证始终包含同样的
六个段——`cookies`、`cookie_header`、`headers`、`query`、`json`、`values`——因此节点无需了解身份类型细节。

导入前先用 **Identity types → Delivery preview**（`PreviewDelivery`）看看样例载荷渲染出的凭证。

### 导入身份

控制台：**Identities → Import**。API：`IdentityAdminService/ImportIdentities`。单次最多 50 000 行或 32 MiB，
任何时候都可以先 **dry run**。

**JSON Lines** —— 每行一个对象。带 `payload` 键时其余为属性；不带时整个对象就是载荷（去掉保留键）：

```jsonl
{"payload":{"cookies":{"sessionid":"abc","csrftoken":"x1"},"user_agent":"Mozilla/5.0"},"account":"acct-1","region":"HK","tags":["batch-09"],"labels":{"vendor":"a"}}
{"cookies":{"sessionid":"def"},"user_agent":"Mozilla/5.0","_account":"acct-2","_region":"SG","_tags":["batch-09"]}
```

**CSV** —— 表头是字段名；`_account`、`_region`、`_tags`（用 `;` 分隔）为保留列。`cookie_map` 单元格接受
`Cookie` 头字符串，`json` 单元格接受 JSON：

```csv
cookies,user_agent,_account,_region,_tags
"sessionid=abc; csrftoken=x1",Mozilla/5.0,acct-1,HK,batch-09;web
```

`cookie_map` 同样接受浏览器导出的 `{"name":…,"value":…}` 数组。

语义：每行按字段定义生成的 JSON Schema 校验，失败行连同行号和原因一并返回。去重依据 `unique_by`。
命中已有身份且载荷**有变化**时会新增一个载荷版本（保留最近 5 个）、健康分重置为基线，并把
`active`/`pending`/`quarantined`/`expired` 的身份改回 `pending`（`activation: immediate` 则为 `active`）；
`banned`、`disabled`、`retired` 状态保持不变。载荷未变化则计为 `unchanged`。

### 刷新失效身份

身份进入 `expired` 时 Spinneret 会发出 `identity_expired` 告警。刷新服务可以订阅该 Webhook 或轮询
`ListIdentities(state=expired)`，拿到新凭证后调用 `UpdateIdentityPayload`——身份回到 `pending`，由探针租约重新验证。

### 账号

多个身份可以共用同一个 `account_id`（"这五份 Cookie 属于同一个账号"）。账号级封禁对其下全部身份生效，
处置规则也可以把身份的封禁沿账号传播。

---

## 策略

四类策略决定了服务端自动做的一切。每类都是 YAML，带版本，编辑草稿、发布后生效。策略属于命名空间，通过**绑定**生效。

```
绑定到 (站点, 客户端, 端点组)     最具体
      > (站点, 客户端)
      > (站点)
      > 命名空间默认（无站点）
      > 内置默认                  最不具体
```

解析按端点组、按类别分别进行：一个站点可以使用命名空间的轮换策略，而某个端点组只覆盖熔断策略。
**Policies → Resolve** 会显示任意端点组实际生效的四条策略。`signal` 和 `action` 还支持
`extends: <名称>`（父规则在前，子规则追加；深度 ≤ 5，拒绝成环）。

工作流：编辑草稿（`policy:write`）→ **Validate** → **Publish**（`policy:publish`，生成一个版本）→
必要时 **Rollback**（把旧版本的 YAML 作为新版本再发布一次）。发布会使目录快照失效，改动约一秒内生效。
每个新命名空间都自带 `default-rotation`、`default-signal`、`default-action`、`default-breaker` 并绑定在命名空间级。

时长支持 `ms`、`s`、`m`、`h`、`d` 和 `permanent`。未知字段会被拒绝——拼错字段会校验失败，而不是被默默忽略。

### 轮换策略 rotation

决定领取谁、租多久、多久能再用、配什么代理。

```yaml
name: web-search-rotation
identity_types: []                # 空 = 该站点+客户端的全部身份类型
rotation:
  strategy: weighted_random       # weighted_random | least_recently_used | round_robin | best_health
  candidate_sample: 32            # 每次领取采样的候选数（1..256）
  lease_ttl: 2m                   # 5s..30m
  max_lease_lifetime: 30m         # 续租不得超过此上限
  max_concurrent_leases: 1        # 1 = 独占使用
  reuse_interval: 30s             # 同一身份两次使用之间的最小间隔
  reuse_anchor: released          # acquired | released
  reuse_scope: endpoint_group     # endpoint_group | site
  quota:
    - { limit: 60, window: 1h }   # 按身份、按端点组
  sticky:
    enabled: true                 # 相同 session_key 复用同一身份
    ttl: 10m
  warmup:
    duration: 24h                 # 新身份缓慢放量
    quota_factor: 0.2
  probe:
    weight_factor: 0.1            # pending / 半开探针被选中的概率
    max_leases: 2
proxy:
  mode: bind_identity             # none | pool | bind_identity | region_match
  kinds: [residential]            # datacenter | residential | mobile | tunnel
  regions: [HK, SG]
  region_match: false             # 要求代理地区与身份地区一致
  rebind_tolerance: 5m            # 绑定的代理不可用多久后才换绑
  max_rebinds_per_day: 3
```

`reuse_interval` 是最有效的反检测开关：配合 `reuse_anchor: released`，计时从租约结束开始，慢请求不会缩短间隔。
`reuse_scope: site` 让间隔跨站点内所有端点组生效。

### 识别策略 signal

把节点上报的事实归类为 12 种**结果**之一：`success`、`empty`、`rate_limited`、`captcha`、`auth_invalid`、
`forbidden`、`banned`、`proxy_error`、`network_error`、`target_error`、`client_error`、`unknown`。
首条命中的规则生效；无命中则为 `unknown`。

```yaml
name: web-signals
trust_outcome_hint: false         # true 允许节点自行提议结果
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
  - name: business-code-risk
    when: { business_code: ["2154", "8"] }
    outcome: captcha
  - name: rate-limited
    when: { http_status: [429] }
    outcome: rate_limited
  - name: search-empty
    when: { http_status: [200], markers: [empty_list], uri: { prefix: /api/v1/search } }
    outcome: empty
    blame: identity               # 可选：identity | proxy | none
  - name: target-error
    when: { http_status: { gte: 500 } }
    outcome: target_error
  - name: success
    when: { http_status: { gte: 200, lt: 300 } }
    outcome: success
```

`when` 支持的键：`http_status`（列表，或 `{gte,gt,lte,lt}`；`0` 表示"没有响应"）、`business_code`（列表）、
`error_kind`（列表）、`markers`（任一命中即可）、`uri`（`{prefix}` 或 `{regex}`，RE2）、`method`（列表）、
`latency_ms` 与 `response_bytes`（区间）。同一条规则内的所有键必须同时满足。

发布前用 **Policies → Rule debugger**（`PolicyAdminService/DebugReport`）拿真实上报验证规则。

### 处置策略 action

把结果转化为冷却、封禁、失效和隔离，并维护健康分。

```yaml
name: web-search-actions
mode: enforce                     # enforce | shadow（只评估和记录，不真正生效）
rules:
  - name: rate-limited-cooldown
    when: { outcome: rate_limited }
    action: cooldown
    scope: identity_endpoint
    base: 60s
    multiplier: 2                 # 连续失败时指数增长
    max: 30m
    max_exponent: 10
    failure_reset_after: 1h
  - name: empty-cooldown
    when: { outcome: empty, count: { gte: 3, within: 10m } }
    action: cooldown
    scope: identity_endpoint
    base: 5m
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
    when: { outcome: proxy_error }
    action: cooldown
    scope: proxy_site
    base: 2m
    max: 30m
escalation:                       # 施加临时封禁时评估
  - when: { bans: { gte: 2, within: 7d } }
    duration: 72h
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
health:
  alpha: 0.1                      # 每次观测的 EWMA 权重
  baseline: 70
  tau: 6h                         # 向基线回归的衰减
  observations: {}                # 覆盖默认：success 100、empty 60、network_error 50、
                                  # rate_limited 30、forbidden 10、captcha 0
  endpoint_low_score: 15
  endpoint_low_min_samples: 10
  endpoint_low_cooldown: 6h
  quarantine_score: 20
  quarantine_min_samples: 10
  quarantine_duration: 24h
ban_expiry_state: pending         # 封禁到期后身份回到的状态
cross_attribution:
  enabled: true
  window: 10m
  proxy_distinct_identities: 3    # 同一代理上 3 个身份出问题 → 归因到代理
  identity_distinct_proxies: 3    # 同一身份在 3 个代理上出问题 → 归因到身份
```

合法的 动作/层级 组合：`cooldown` → `identity_endpoint`、`identity_site`、`account`、`proxy_site`、`proxy`；
`ban` → `identity`、`account`、`proxy`；`expire` → `identity`；`quarantine` → `identity`、`proxy`。
所有命中的规则都会被评估，同一对象取最严厉的动作。

引入或修改规则时**先用 `mode: shadow`**。影子模式照常评估并记录"本来会做什么"（可在风控事件和
`spinneret_actions_total{mode="shadow"}` 中看到），但不会真的冷却或封禁。

### 熔断策略 breaker

按端点组、滑动窗口、三态。

```yaml
name: search-breaker
enabled: true
window: 60s
buckets: 12                       # 5 秒粒度
min_requests: 50                  # 低于此请求数不评估
trip:                             # 任一条件满足即打开；填 0 表示关闭该条件
  risk_ratio_gte: 0.4
  distinct_captcha_identities_gte: 10
  success_ratio_lte: 0.2
open_duration: 2m                 # 反复打开时翻倍
max_open_duration: 1h
reset_open_count_after: 30m
half_open:
  probe_leases_per_10s: 5
  close_min_samples: 5
  close_success_ratio_gte: 0.8
revert_recent_cooldowns: endpoint # none | endpoint | all
```

`revert_recent_cooldowns` 很关键：整个端点崩掉时，撞上它的身份未必真的被风控了。`endpoint`（默认）撤销触发窗口内
施加的身份×端点冷却，`all` 还会撤销身份×站点冷却，`none` 保留全部。

---

## 熔断与站点开关

**熔断页**（`BreakerAdminService`）显示每个端点组的状态（`closed` / `open` / `half_open`）、当前窗口计数和状态变更历史。

- `closed → open`：触发条件满足时自动发生，也可手动执行（**Open**，指定时长和原因，`OpenBreaker`）。
- `open → half_open`：`open_duration` 之后自动发生。此时只发放探针租约（`probe_leases_per_10s`），
  它们的上报决定下一步走向。
- `half_open → closed`：`close_min_samples` 条探针达到 `close_success_ratio_gte` 时关闭；否则回到 `open`，
  并延长 `open_duration`。
- **Close**（`CloseBreaker`）立即强制关闭——在真正修复根因之后使用。

熔断打开期间，该端点组的 `Acquire` 返回 `503` / `circuit_open` 并附带重试建议，同站点的其他端点组不受影响。

**站点开关**（`SetSitePaused`，同在熔断页）停止某站点的全部领取（`503` / `site_paused`）。适用于维护、
平台级故障，或在不动节点的前提下叫停失控的任务。站点暂停期间，已有租约的上报仍会被接受。

两者都是实时可见的：控制台通过 SSE 订阅 `breaker.transition` 事件，每次状态变更都可以触发告警
（`breaker_opened`、`breaker_reopened`、`breaker_closed`）。

---

## 人工处置与回滚

**单个与批量处置**（`OperateIdentities`、`BulkOperateIdentities`、`OperateAccount`、`OperateProxies`）：
按层级和时长冷却、封禁（定时或永久）、解封、隔离、失效、禁用、退役、启用。每次操作都必须填**原因**，
会写入审计日志和该身份的状态事件，并出现在控制台的身份时间线中。

**`RevertActions`** 是规则误判的解药。它撤销某个时间范围内记录的自动处置：

| 字段 | 含义 |
| --- | --- |
| `time_range` | 必填；必须指定 `start`，避免一次回滚悄悄覆盖全部历史 |
| `site`、`policy_id`、`rule` | 收窄到某个站点、产生这些处置的策略，或单条规则名 |
| `actions` | 要撤销的动作；留空表示 `ban`、`quarantine`、`expire` 和 `cooldown` |
| `reset_failures` | 同时清除连续失败计数（退避从 `base` 重新开始） |
| `reset_health` | 同时把健康分重置为基线 |
| `dry_run` | 只列出受影响的身份，不做任何改动 |

典型事故：识别规则把目标站的 500 风暴误判为 `captcha`，封禁了几百个身份。

1. 修正识别策略并发布。
2. 用 `dry_run: true` 加上事故时间范围和 `rule: captcha-ban` 执行 `RevertActions`——核对数量和身份列表。
3. 改为 `dry_run: false`，并加上 `reset_failures: true`、`reset_health: true` 再执行一次。
4. 在身份页确认：被撤销的身份回到处置前的状态。

---

## 代理

代理属于命名空间，通过轮换策略的 `proxy` 段提供给站点。属性包括：URL（加密存储）、`kind`
（`datacenter` / `residential` / `mobile` / `tunnel`）、`region`、`city`、`provider`、`tags`、
`max_concurrency`，以及用于轮换会话网关的可选 `session_template`。

**分配模式**（在轮换策略中）：`none`（不分配代理）、`pool`（每次领取从过滤后的池中取一个）、
`bind_identity`（身份固定使用同一个代理，只有当它不可用超过 `rebind_tolerance` 时才换绑，每天最多
`max_rebinds_per_day` 次）、`region_match`（代理地区必须与身份地区一致）。

### 导入

**Proxies → Import**（`ImportProxies`），三种格式，支持 dry run，按 URL 去重：

```text
# lines：每行一个 URL，后面可跟空格分隔的 key=value
http://user:pass@1.2.3.4:8080 kind=residential region=HK tags=pool-a,fast
socks5://user:pass@1.2.3.5:1080 kind=datacenter max_concurrency=4
```

```jsonl
{"url":"http://user:pass@1.2.3.4:8080","kind":"residential","region":"HK","provider":"acme","tags":["pool-a"],"max_concurrency":2}
```

```csv
url,kind,region,city,provider,tags,max_concurrency,session_template
http://user:pass@1.2.3.4:8080,residential,HK,Hong Kong,acme,pool-a;fast,2,
```

`lines` 格式忽略空行和以 `#` 开头的行。请求中的 `ProxyDefaults` 用于填充行内未指定的属性。

### 健康检查

每隔 `SPINNERET_PROXY_CHECK_INTERVAL`（60 秒），系统会通过每个代理访问 `SPINNERET_PROXY_CHECK_URL`
（默认 `http://example.com/`），超时为 `SPINNERET_PROXY_CHECK_TIMEOUT`（10 秒）。
失败的代理转为 `dead`，成功则恢复。设置了 `SPINNERET_PROXY_EXIT_IP_URL` 时还会记录出口 IP，
配合 `SPINNERET_GEOIP_DB` 可以用该 IP 补全空的地区字段。

代理行上的 **Check now**（`CheckProxy`）会立即执行一次探测并显示结果——节点报 `no_proxy_available` 时先试它。
在无外网的主机上，务必把 `SPINNERET_PROXY_CHECK_URL` 指向可达地址，否则整个代理池都会变成 `dead`。

代理同样会因处置规则进入冷却（`proxy_site`、`proxy` 层级），并在 **Proxies → Providers**
（`GetProviderStats`）中按供应商展示成功率统计。

---

## 配置中心

版本化的配置下发给节点，让爬虫设置无需重新发布即可变更。

配置项在命名空间内由 `group` + `key` 寻址（`crawler` / `search.json`），有格式（`json`、`yaml`、`text`）、
草稿和已发布版本。版本号对同一 命名空间/分组/键 永不重复，删除后重建也会接着往上走。

流程（**Config** 页或 `ConfigAdminService`）：编辑草稿 → **Publish**（`config:publish`）→ 用差异视图对比版本 →
**Rollback** 把旧版本作为新版本重新发布。

节点用 `GetConfig` / `BatchGetConfig` 读取，用 `WatchConfig` 长轮询跟随变更（默认等待 30 秒，最长 60 秒）。
两个 SDK 都把它封装为带变更回调和本地原子快照的 `ConfigWatcher`，控制平面短暂不可达时节点仍能启动。

**密钥引用。** 内容中可以写 `${secret:<path>}` 或 `${secret:<path>#<version>}`，服务端在下发时解析，
这要求调用方的令牌持有匹配的 `secret:read:<命名空间>/<路径>` 作用域。解析过的配置项会被标记
`has_secret_refs: true`，SDK 会拒绝把它写入明文快照。

**`_runtime` 分组**是保留的只读分组，暴露 `breakers` 和 `site_switches` 的当前状态，节点可以监听它，
在下一次 `Acquire` 失败之前就先行退避。

---

## 密钥管理

Vault 用 AES-256-GCM 信封加密存放共享密钥（签名密钥、API Key、账号口令）。密钥在命名空间内有一个路径
（`signing/api_key`，小写，字符集 `[a-z0-9_./-]`）、多个版本和可选的过期时间。

| 操作 | 权限 | 说明 |
| --- | --- | --- |
| 列出（仅元数据） | `secret:list` | 列表响应中永远不含值 |
| 创建 / 更新（新版本） | `secret:write` | |
| 在控制台查看明文 | `secret:reveal` | 界面要求输入确认文本；审计记为 `secret.read` |
| 节点读取 | 令牌作用域 `secret:read:<ns>/<path glob>` | 审计记为 `secret.read` |

三种不用到处粘贴密钥值的用法：

1. 在配置项中写 `${secret:path}`；
2. 在身份类型中使用 `secret_ref` 字段，领取时解析进凭证；
3. 节点调用 `SecretService/GetSecret`。

密钥过期前 7 天会触发 `secret_expiring` 告警。**Secrets → Access logs** 列出每一次读取的人、时间和来源。

写入引用了某个密钥的身份载荷需要对该密钥有读权限——否则运维可以把身份字段指向任意密钥，再通过领取把它导出。

---

## 告警与 Webhook

**渠道**（`NotificationAdminService`，**Notifications** 页）属于租户，可以限制到指定站点。类型有：`webhook`、
`feishu`、`dingtalk`、`wecom`、`telegram`。每个渠道订阅一组事件类型，可以发送测试消息（**Test alert**），
也可以只禁用而不删除。

告警类型：`breaker_opened`、`breaker_reopened`、`breaker_closed`、`identity_low_watermark`、
`proxy_low_watermark`、`ban_spike`、`report_backlog`、`unknown_ratio_high`、`client_error_spike`、
`identity_expired`、`secret_expiring`、`test`。级别为 `info`、`warning`、`critical`。相同告警默认去重 10 分钟。

### Webhook 负载

`POST`，`Content-Type: application/json`：

```json
{
  "id": "alt_01a0b0ff449e78a49c2d8cc240515316",
  "kind": "breaker_opened",
  "severity": "critical",
  "title": "Breaker opened: shop/search",
  "message": "risk_ratio 0.62 over 60s (min_requests 50)",
  "tenant": "default",
  "namespace": "default",
  "site": "shop",
  "details": { "endpoint_group": "search", "risk_ratio": 0.62, "open_duration": "2m" },
  "created_at": "2026-09-17T20:11:54.243Z"
}
```

### 校验签名

渠道配置了 secret 时，每次投递会带两个请求头：

```http
X-Spinneret-Timestamp: 1758140314
X-Spinneret-Signature: sha256=6f1c…
```

签名为 `"sha256=" + hex(HMAC_SHA256(secret, timestamp + "." + 原始请求体))`。请在 JSON 解析**之前**对原始请求体
校验，并拒绝时间戳过旧的请求：

```python
import hashlib, hmac, time

def verify(secret: str, timestamp: str, body: bytes, signature: str, tolerance: int = 300) -> bool:
    if abs(time.time() - int(timestamp)) > tolerance:
        return False
    expected = "sha256=" + hmac.new(
        secret.encode(), timestamp.encode() + b"." + body, hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(expected, signature)
```

```go
func verify(secret, timestamp string, body []byte, signature string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(signature))
}
```

飞书和钉钉使用各自的签名算法，由渠道 secret 自动计算。每个渠道可以添加自定义请求头，但不能覆盖上述两个签名头。

投递结果（`ok`、错误、重试次数）随每条告警一并保存，可在 **Notifications → Alert history** 查看。

---

## 监控

`/healthz`（存活）、`/readyz`（PostgreSQL、Redis、热状态、目录；退出期间为 `draining`）和 `/metrics`（Prometheus）。
Compose 的 `observability` profile 会抓取两个副本。

| 指标 | 标签 | 用途 |
| --- | --- | --- |
| `spinneret_acquire_total` | `site`、`group`、`result` | 领取次数及失败原因 |
| `spinneret_acquire_duration_seconds` | `site` | 服务端领取延迟（目标 p99 < 5 ms） |
| `spinneret_report_ingest_total` | `result`（accepted/duplicated/rejected） | 上报接收健康度 |
| `spinneret_report_total` | `site`、`group`、`outcome` | 结果分布——最主要的风控信号 |
| `spinneret_report_lag_seconds` | — | 接收到处理的延迟 |
| `spinneret_report_process_duration_seconds` | — | 单条上报的处理耗时 |
| `spinneret_identities` | `site`、`type`、`state` | 生命周期分布 |
| `spinneret_identities_available` | `site`、`group` | 各端点组的可用容量 |
| `spinneret_actions_total` | `site`、`action`、`scope`、`mode` | 施加的冷却/封禁（`mode=shadow` 为演练） |
| `spinneret_breaker_state` | `site`、`group` | 0 关闭、1 半开、2 打开 |
| `spinneret_breaker_transitions_total` | `site`、`group`、`to` | 识别熔断抖动 |
| `spinneret_proxies` | `site`、`state` | 代理池健康度 |
| `spinneret_stream_pending` | `shard` | 各分片未确认的上报条目 |
| `spinneret_stream_owned_shards` | — | 本实例拥有的分片数（各实例之和 = `SPINNERET_REPORT_SHARDS`） |
| `spinneret_lease_reaped_total` | `site`、`kind`（expired/abandoned） | 从不上报的节点 |
| `spinneret_config_watchers` | — | 本实例上活跃的长轮询数 |
| `spinneret_http_requests_total` / `_duration_seconds` | `procedure`、`code` | 各 RPC 的流量与延迟 |
| `spinneret_notify_deliveries_total` | `kind`、`result` | 告警投递失败 |
| `spinneret_db_write_batches_total` | `writer`、`result` | 批量持久化失败 |
| `spinneret_job_runs_total` / `_duration_seconds` | job | 后台任务健康度 |
| `spinneret_state_writer_pending_changes` / `_spilled_changes_total` / `_dropped_changes_total` | — | 状态持久化的背压（spilled 表示改为同步写入，dropped 表示丢弃） |

建议的告警：

| 告警 | 表达式示意 | 原因 |
| --- | --- | --- |
| 实例宕机 | `up{job="spinneret"} == 0 for 1m` | |
| 未就绪 | `/readyz` 非 2xx 持续 2m | 依赖故障或重建卡住 |
| 领取失败率 | `rate(spinneret_acquire_total{result!="ok"}[5m]) / rate(spinneret_acquire_total[5m]) > 0.05 for 10m` | 容量或熔断问题 |
| 领取延迟 | `histogram_quantile(0.99, rate(spinneret_acquire_duration_seconds_bucket[5m])) > 0.02 for 10m` | Redis 或 CPU 饱和 |
| 上报积压 | `sum(spinneret_stream_pending) > 50000 for 10m`，或 `report_lag_seconds` p99 > 60s | Worker 跟不上 |
| 分片无人消费 | `sum(spinneret_stream_owned_shards) < SPINNERET_REPORT_SHARDS for 5m` | 某个分片没有消费者 |
| 熔断打开 | `max(spinneret_breaker_state) by (site,group) == 2 for 5m` | 目标站事故 |
| 熔断抖动 | `increase(spinneret_breaker_transitions_total{to="open"}[1h]) > 5` | 阈值过紧 |
| 容量不足 | `spinneret_identities_available < <低水位> for 10m` | 需要补充身份 |
| 风控激增 | `rate(spinneret_report_total{outcome=~"captcha|banned|rate_limited"}[5m])` 高于基线 | 被目标站识别 |
| unknown 占比高 | `rate(spinneret_report_total{outcome="unknown"}[15m]) / rate(spinneret_report_total[15m]) > 0.1` | 识别规则与现实脱节 |
| 租约被遗弃 | `rate(spinneret_lease_reaped_total{kind="abandoned"}[15m])` 上升 | 节点在上报前崩溃 |
| 告警投递失败 | `rate(spinneret_notify_deliveries_total{result!="ok"}[15m]) > 0` | 你收不到告警了 |

其中一部分 Spinneret 自身也会作为通知告警发出（`report_backlog`、`identity_low_watermark`、
`proxy_low_watermark`、`ban_spike`、`unknown_ratio_high`、`client_error_spike`）——两者都用：
Prometheus 盯基础设施，Spinneret 告警盯业务事件。

控制台方面，**Overview** 给出各站点的 QPS、结果分布和熔断数，**Heatmap** 展示身份 × 端点组的可用性与分数，
**Requests** 是按上报逐条查询的浏览器（需要 ClickHouse），**Risk events** 列出每一次自动处置及其规则与归因。

---

## 热状态重建

Redis 只保存派生状态，PostgreSQL 才是真相源。Redis 丢数据、恢复备份后，或热状态与数据库不一致时执行重建。

```bash
# 全量：删除热状态 epoch，重新物化所有站点
docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr rebuild

# 只重建单个站点，不需要全局停机
docker compose -f deploy/compose/docker-compose.yml exec spinneret \
  spnr rebuild --tenant default --namespace default --site shop
```

全量重建会获取 PostgreSQL advisory lock（同一时刻只有一个在跑），写入站点元数据，从周期性的
`hot_state_snapshots` 恢复健康分，重新物化代理和身份，把就绪时间均匀打散到接下来的 60 秒以避免惊群，
清理失效的队列成员，最后发布新的 epoch。期间 API 实例返回 `503` / `rebuilding`，节点会自行重试。

实例在启动时若发现 epoch 键缺失也会自动重建——丢失 Redis 不需要人工介入，只需要耐心等待。

---

## KEK 轮换

完整流程见[部署指南 → KEK 管理](deployment.zh-CN.md#kek-管理)。简要步骤：

```bash
spnr kek generate --id k2 >> deploy/compose/secrets/kek.key   # 追加并成为当前密钥
docker compose ... up -d --wait spinneret                     # 重启以加载
spnr kek rewrap                                               # 重新包装全部 DEK，跟踪进度
spnr kek status                                               # 旧密钥记录数为 0 → 删除它
```

控制台的 **Secrets → KEK**（仅平台管理员）提供同样的能力：显示已配置的 KEK、各自包装的记录数，以及正在运行的
rewrap 进度。轮换是在线的——配置列表中的每个密钥都仍可解包，因此请求全程不受影响。

---

## 数据保留

| 数据 | 变量 | 默认 | 存储 |
| --- | --- | --- | --- |
| 风控事件 | `SPINNERET_RETENTION_RISK_EVENTS` | 30 天 | PostgreSQL，按天分区 |
| 分钟级统计 | `SPINNERET_RETENTION_MINUTE_STATS` | 30 天 | PostgreSQL |
| 小时级统计 | `SPINNERET_RETENTION_HOUR_STATS` | 180 天 | PostgreSQL |
| 状态事件 | `SPINNERET_RETENTION_STATE_EVENTS` | 365 天 | PostgreSQL |
| 审计日志 | `SPINNERET_RETENTION_AUDIT` | 365 天 | PostgreSQL |
| 原始请求事件 | `SPINNERET_CLICKHOUSE_TTL_DAYS` | 90 天 | ClickHouse |

每小时执行的 `partition_manager` 任务（仅领导者）预先创建分区，并删除完全超出保留期的分区；ClickHouse 自行执行 TTL。
其他有上限的数据：身份载荷版本（保留最近 5 个）、封禁历史（30 天）、上报去重键
（`SPINNERET_REPORT_DEDUP_TTL`，1 小时）、上报 Stream 条目（每个分片持有者每隔几秒会按消费组位点裁剪自己的
Stream，因此只保留真实积压，上限为每分片 `SPINNERET_STREAM_MAXLEN` 条）。

调大保留期只影响此后写入的数据；调小则在下一次任务执行时删除分区。审计日志的保留期请不低于合规要求——
它是"谁查看了哪个密钥"的唯一记录。
