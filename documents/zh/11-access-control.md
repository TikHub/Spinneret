# 租户、用户与令牌

**Spinneret 完整的访问控制模型：租户和命名空间各自隔离什么、用户如何获得权限、API 令牌能做什么、会话如何工作，以及审计日志记录了什么。**

[English](../en/11-access-control.md)

---

## 目录

- [两层边界](#两层边界)
- [示例：一套服务承载两个团队](#示例一套服务承载两个团队)
- [平台管理员](#平台管理员)
- [用户与角色绑定](#用户与角色绑定)
- [角色](#角色)
- [限定站点的绑定](#限定站点的绑定)
- [权限清单](#权限清单)
- [API 令牌](#api-令牌)
- [令牌权限范围](#令牌权限范围)
- [IP 白名单、限速与有效期](#ip-白名单限速与有效期)
- [吊销](#吊销)
- [用令牌创建令牌](#用令牌创建令牌)
- [会话、Cookie 与 CSRF](#会话cookie-与-csrf)
- [审计日志](#审计日志)
- [加固清单](#加固清单)

---

## 两层边界

Spinneret 只有两层容器，且互相嵌套：

- **租户**是硬隔离边界——一家公司、一个客户或一个组织。除了服务进程、数据库和由运维统一管理的密钥加密密钥之外，租户之间不共享任何东西。
- **命名空间**是租户内部的划分——环境（`prod`、`staging`）或业务线。命名空间是日常工作的单位：几乎所有对象都恰好属于一个命名空间。

各类对象归属于哪一层：

| 对象 | 归属 | 说明 |
| --- | --- | --- |
| 命名空间 | 租户 | `namespaces (tenant_id, name)` 唯一 |
| 站点 | 命名空间 | 端点组、URI 规则和身份类型挂在站点下 |
| 身份、账号、载荷 | 站点 | 因而属于命名空间，进而属于租户 |
| 代理 | 命名空间 | 命名空间之间从不共享 |
| 策略与策略绑定 | 命名空间 | 绑定可以收窄到站点或端点组 |
| 配置项与版本 | 命名空间 | `(namespace_id, group_name, key)` 唯一 |
| 密钥与密钥版本 | 命名空间 | `(namespace_id, path)` 唯一 |
| API 令牌 | 命名空间 | 一个令牌永远只属于一个命名空间 |
| 用户账号 | 全局 | 只是一个登录凭证，不代表任何授权 |
| 角色绑定 | 租户 | 可选地收窄到一个命名空间及其部分站点 |
| 通知渠道 | 租户，可选命名空间 | 不带命名空间的渠道覆盖整个租户 |
| 审计记录 | 租户，通常还有命名空间 | 租户级操作的命名空间为空 |
| KEK、系统设置 | 平台 | 所有租户共用；只有平台管理员能操作 |

两条必须说清楚的结论：

1. **用户账号本身不等于访问权限。** 创建用户不授予任何权限，真正授权的是角色绑定。同一个账号可以在一个租户里是 `owner`，在另一个租户里什么都看不到。
2. **令牌永远不跨命名空间。** 租户和命名空间在创建时固定，之后不可更改。要让一个节点访问两个命名空间，就给它两个令牌。

这些对象在请求时如何配合，见[核心概念](./04-concepts.md)。

---

## 示例：一套服务承载两个团队

一个租户 `acme`，两个命名空间：`prod` 和 `staging`。两个用户：

```text
租户 acme
├── 命名空间 prod       站点：example-site、partner-api
└── 命名空间 staging    站点：example-site

用户 mei    绑定：owner（租户 acme，不限命名空间）    → 两个命名空间都可见
用户 dana   绑定：operator（仅命名空间 staging）      → 只有 staging
```

`dana`——一个被固定在 `staging` 的 operator——对 `prod` 能做什么、不能做什么：

| `prod` 中的对象 | `dana` 可以吗？ | 原因 |
| --- | --- | --- |
| 在命名空间切换器中看到 `prod` | 不行 | `namespace:read` 只在 `staging` 上被授予 |
| 列出站点、端点组、身份类型 | 不行 | 绑定的命名空间不匹配 |
| 列出或打开身份、账号 | 不行 | 站点级资源继承命名空间检查 |
| 查看身份凭据明文 | 不行 | `identity:reveal` 不属于 `operator`，且不在 `prod` 上 |
| 列出代理 | 不行 | 代理是 `prod` 的命名空间级对象 |
| 读取配置项、列出或查看密钥 | 不行 | `config:read` 和 `secret:list` 是在 `prod` 上检查的 |
| 列出或创建 API 令牌 | 不行 | `token:read` / `token:write` 是在 `prod` 上检查的 |
| 读取 `prod` 的审计记录 | 不行 | 审计查询被限制在可读的命名空间内 |
| 查看 `prod` 的请求与租约统计 | 不行 | `dashboard:read` 按命名空间检查 |
| 修改 `acme` 租户、添加用户、授予角色 | 不行 | 这些需要覆盖整个租户的绑定 |

在 `staging` 内部，`dana` 拥有 viewer 的全部读取权限，另外可以操作身份、代理和熔断器，编辑策略和配置草稿——但不能发布，不能写入或查看密钥明文，也不能管理令牌：那些属于 `admin`。见[角色](#角色)。

再加入第二个租户 `partner-co`。只在 `acme` 有绑定的用户根本无法寻址 `partner-co`：把它选为当前租户会在任何业务逻辑执行之前就以 `permission_denied` 失败，而且 `partner-co` 根本不会出现在租户切换器里。该租户内的一切——身份、载荷、代理、配置、密钥、令牌、请求、审计——都不可达。

**注意。** 隔离由服务端强制执行，而不是由控制台。每个 RPC 都会重新用主体的权限去检查目标资源；控制台只决定画什么。

---

## 平台管理员

平台管理员是设置了 `is_platform_admin` 的用户。他们：

- 在**所有租户**中通过**所有**权限检查，包括别人都拿不到的 `tenant:manage` 和 `kek:manage`；
- 在 `GetMe` 中看到所有租户，可以把任意已存在的租户设为当前租户；
- 可以列出所有用户（`all_users`），管理在自己没有绑定的租户中持有绑定的用户，也可以管理其他平台管理员；
- 创建已存在的用户名时会直接给该账号追加一条绑定，而租户 owner 在同样情况下会得到 `already_exists`。

平台管理员的当前租户只是界面上的选择，不是安全边界。**没有**选择任何租户的平台管理员依然有用：他们可以管理租户，并读取平台级审计记录（登录、KEK 操作，这些记录的租户为空）。

**第一个**平台管理员由 CLI 创建，并在同一个事务里连同租户、命名空间和内置默认策略一起建好：

```bash
SPINNERET_ADMIN_PASSWORD='…' spnr admin init \
  --username admin \
  --password-env SPINNERET_ADMIN_PASSWORD \
  --tenant default \
  --namespace default
```

该命令是幂等的：当平台管理员已存在时会打印 `already initialized` 并以 0 退出。**没有任何 API、RPC 或控制台操作可以把一个用户提升为平台管理员**，而 `spnr admin init` 也不会创建第二个——它只是报告 `already initialized`。要增加第二个平台管理员，只能直接操作数据库，这件事本身就应该被当作高危操作对待。见[命令行工具](./15-cli.md)。

---

## 用户与角色绑定

用户是全局的控制台账号：

| 字段 | 含义 |
| --- | --- |
| `id` | `usr_…` |
| `username` | 唯一、小写，3–64 个 `a-z 0-9 . _ -` 字符，以字母或数字开头 |
| `display_name` | 自由文本，最多 128 个字符 |
| `email` | 可选，最多 254 个字符 |
| `locale` | 控制台语言偏好（`en`、`zh-CN`）；留空跟随浏览器 |
| `is_platform_admin` | 见上文 |
| `disabled` | 无法登录；停用会立即结束其所有会话 |
| `last_login_at`、`created_at` | 时间戳 |

资料字段是全局的：在**用户**页面改显示名称，会在所有租户里同时生效。

真正授予访问权限的是**角色绑定**，它由四部分组成：

```text
绑定 = 用户 + 租户 + 角色 [+ 命名空间] [+ 站点] [+ 额外权限]
```

| 部分 | 作用 |
| --- | --- |
| `role` | `owner`、`admin`、`operator` 或 `viewer`——决定权限集合 |
| `namespace` | 留空 = 该租户的所有命名空间以及租户级资源；填写 = 仅该命名空间 |
| `sites` | 留空 = 所有站点；填写 = 仅该命名空间下的这些站点（最多 500 个，且必须先指定命名空间） |
| `extra_permissions` | 在角色之上单独追加的敏感权限 |

一个用户可以持有多条绑定，可以在同一个租户内，也可以跨租户；它们是叠加的，只要**任意一条**绑定允许，检查就通过。在某个租户中没有绑定的用户无法选择该租户。

只有四项权限可以作为额外权限追加——`config:publish`、`secret:reveal`、`identity:reveal` 和 `policy:publish`——这样就能在不把人提升为 admin 的前提下，精确地多给一项管理级权限。其他权限一律以 `invalid_argument` 拒绝。

### 管理用户与绑定

用户、角色绑定和租户级通知渠道属于**租户级资源**：只有覆盖整个租户的绑定（不限命名空间、不限站点）才能管理它们。被固定在某个命名空间的 owner 可以运营自己的命名空间，但连一个用户都添加不了。

服务端还会强制以下规则：

- 授予或移除 `owner` 绑定需要**覆盖整个租户的 owner** 绑定（或平台管理员）。
- **租户的最后一条覆盖全租户的 owner 绑定不能被删除**（`failed_precondition`）。
- 完全相同的绑定（角色、命名空间、站点、额外权限都一样）会以 `already_exists` 拒绝。
- 租户 admin 不能管理在自己管不到的租户里持有绑定的用户，也完全不能管理平台管理员。
- 不能停用自己的账号。
- 停用用户或重置其密码会结束该用户的所有会话。

相关 RPC：`AccessAdminService.ListUsers`、`CreateUser`、`UpdateUser`、`ResetPassword`、`ListRoleBindings`、`CreateRoleBinding`、`DeleteRoleBinding`。租户和命名空间由 `TenantAdminService` 管理。

---

## 角色

角色是包含关系：`viewer ⊂ operator ⊂ admin ⊂ owner`。

| 角色 | 相对下一级新增 | 一句话说明 |
| --- | --- | --- |
| `viewer` | — | 只读访问调度状态、配置、仪表盘、通知和审计日志；可列出密钥但看不到值。 |
| `operator` | `identity:write`、`identity:operate`、`proxy:write`、`proxy:operate`、`policy:write`、`config:write`、`breaker:operate` | 日常操作：导入和操作身份与代理，编辑策略和配置草稿，开启和关闭熔断器。 |
| `admin` | `site:write`、`policy:publish`、`config:publish`、`secret:write`、`secret:reveal`、`identity:reveal`、`token:read`、`token:write`、`notify:write` | 决定发布什么、并掌握凭据：站点与身份类型、发布、密钥、明文查看、API 令牌、通知渠道。 |
| `owner` | `namespace:write`、`user:read`、`user:write` | 经营租户：创建和删除命名空间，管理用户和角色绑定。 |

`viewer` 的完整权限：`namespace:read`、`site:read`、`identity:read`、`proxy:read`、`policy:read`、`breaker:read`、`config:read`、`secret:list`、`dashboard:read`、`audit:read`、`notify:read`。

有两项权限只有平台管理员能持有（`tenant:manage`、`kek:manage`），另有三项**只能**通过令牌权限范围授予、任何角色都不包含（`lease:acquire`、`report:write`、`secret:read`）。

---

## 限定站点的绑定

给绑定加上站点列表，就把它收窄到这些站点。典型场景是只负责共享命名空间中两个站点的外包人员或小组。

限定站点的绑定只在**列出的站点**上授予角色权限。对命名空间级对象——代理、配置项、密钥、API 令牌、命名空间本身——它什么都不授予，只有两个刻意保留的例外：

| 例外 | 原因 |
| --- | --- |
| `namespace:read` | 没有它，命名空间不会出现在切换器里，用户连自己的站点都进不去 |
| `proxy:read` | 身份是通过代理租借出去的；站点操作员必须能看到某个身份用了哪个代理 |

这两个例外**只在绑定被固定到某个命名空间时**生效。带站点列表但覆盖整个租户的绑定两者都得不到，因为服务端无法在不额外查库的情况下把站点列表和命名空间关联起来。

所以，`staging` 中限定在 `example-site` 的 operator 可以操作该站点的身份、可以查看代理池，但不能读取配置项、不能列出密钥、看不到令牌，也碰不到 `partner-api`。

**注意。** 列表接口会尽可能按站点做数据库过滤。当过滤条件无法表达授权时——例如令牌的权限范围以站点名限定——服务端会退化为逐条检查候选项，结果一致，只是计算方式不同。

---

## 权限清单

每项权限都是 `<资源>:<动作>` 形式的字符串。检查一律失败即拒绝：未知权限、未知角色、结构不合法的资源或缺失的主体都会被拒绝。

| 权限 | 允许做什么 | 控制台页面 |
| --- | --- | --- |
| `tenant:manage` | 创建、修改和删除租户 | 租户 |
| `kek:manage` | 查看 KEK 状态并发起重新封装 | 密钥（KEK 面板） |
| `namespace:read` | 看到某个命名空间并在切换器中选择它 | 所有页面 |
| `namespace:write` | 创建、修改和删除命名空间 | 租户 |
| `site:read` | 列出和打开站点、端点组、URI 规则、身份类型 | 站点 |
| `site:write` | 创建、编辑和删除站点、端点组、URI 规则和身份类型 | 站点、身份类型 |
| `identity:read` | 列出和打开身份与账号 | 身份、账号、身份类型 |
| `identity:write` | 导入、编辑和更新身份及其载荷，以及创建或更新账号 | 身份、账号 |
| `identity:operate` | 封禁、冷却、隔离、释放、回滚、批量操作身份与账号 | 身份、身份详情 |
| `identity:reveal` | 查看身份的解密载荷 | 身份详情 |
| `proxy:read` | 列出和打开代理 | 代理 |
| `proxy:write` | 导入、编辑和删除代理 | 代理 |
| `proxy:operate` | 启用、停用、冷却和健康检查代理 | 代理 |
| `policy:read` | 列出和打开策略、版本和绑定 | 策略 |
| `policy:write` | 创建策略并保存草稿 | 策略 |
| `policy:publish` | 发布、回滚、删除、绑定和解绑策略 | 策略 |
| `breaker:read` | 查看熔断器状态和历史 | 熔断器、概览 |
| `breaker:operate` | 熔断和恢复熔断器、暂停和恢复站点 | 熔断器 |
| `config:read` | 读取配置分组、配置项和版本 | 配置中心 |
| `config:write` | 创建和编辑配置项、保存草稿 | 配置中心 |
| `config:publish` | 发布、回滚和删除配置项与版本 | 配置中心 |
| `secret:list` | 列出密钥路径和元数据，看不到值 | 密钥 |
| `secret:write` | 创建、更新和删除密钥及其版本 | 密钥 |
| `secret:reveal` | 在控制台查看密钥明文 | 密钥 |
| `token:read` | 列出 API 令牌（永远看不到明文） | 令牌 |
| `token:write` | 创建和吊销 API 令牌 | 令牌 |
| `user:read` | 列出租户的用户和角色绑定 | 用户 |
| `user:write` | 创建和更新用户、重置密码、授予和移除绑定 | 用户 |
| `audit:read` | 查询审计日志 | 审计日志 |
| `notify:read` | 列出通知渠道和告警事件 | 通知 |
| `notify:write` | 创建、编辑、删除和测试通知渠道 | 通知 |
| `dashboard:read` | 读取仪表盘、冷却热力图、请求明细和风险事件 | 概览、冷却热力图、请求明细、风险事件 |
| `lease:acquire` | `Acquire`、`AcquireBatch`、`Renew`、`Release` | 仅节点使用——没有对应页面 |
| `report:write` | `Report`、`ReportBatch` | 仅节点使用——没有对应页面 |
| `secret:read` | `GetSecret` 以及配置中 `${secret:…}` 的解析 | 仅节点使用——没有对应页面 |

只要某项权限在当前命名空间的任意范围内被授予，对应的导航入口就会显示；**用户**页是唯一的例外，它需要覆盖整个租户的授权。见[控制台总览](./05-console-overview.md)。

---

## API 令牌

令牌是爬虫节点用来认证的东西。它绑定一个租户和一个命名空间，携带若干权限范围，范围之内允许，之外一律拒绝。

### 在控制台创建

**访问控制 › 令牌**，需要该命名空间上的 `token:write`。表单包含名称（在该命名空间未吊销的令牌中唯一，最多 64 个字符）、描述、权限范围、可选的 IP 白名单、可选的限速和有效期（表单默认 `90d`）。

响应中**只此一次**返回明文：

```text
spn_3Qw9…                       （47 个字符："spn_" + 43 个 base62 字符）
```

Spinneret 只保存令牌的 SHA-256 摘要，以及前 12 个字符（`token_prefix`，含 `spn_`）用于识别。明文无法再次显示。一旦丢失，请吊销该令牌并重新创建。

### 在命令行创建

适合在还没有人登录控制台之前给节点做初始化。它只需要 `SPINNERET_DATABASE_URL`，且不做任何权限检查——能运行它就意味着已经拿到了数据库：

```bash
spnr token create \
  --tenant default \
  --namespace default \
  --name crawler-node-01 \
  --scope lease:acquire \
  --scope report:write \
  --scope config:read \
  --expires 720h
```

标准输出只打印明文令牌。`--expires` 接受 `720h`、`30d` 这样的时长，默认 `720h`；`0` 或 `never` 表示永不过期。这样创建的令牌，`created_by` 记为 `system:cli`。

### 使用

```bash
curl -sS https://spinneret.example.com/spinneret.v1.NodeService/Acquire \
  -H 'Authorization: Bearer spn_3Qw9…' \
  -H 'Content-Type: application/json' \
  -H 'X-Spinneret-Node: crawler-node-01' \
  -d '{"site":"example-site","client":"web","uri":"/search"}'
```

`X-Spinneret-Node` 是可选的，用于在请求明细和按节点统计（租约与上报）里标识调用方实例。它**不会**被写入审计日志。见[节点 API 参考](./13-node-api.md)。

### 令牌字段

| 字段 | 含义 |
| --- | --- |
| `id` | `tok_…` |
| `namespace` | 令牌绑定的命名空间，创建后固定 |
| `name` | 在该命名空间未吊销的令牌中唯一——吊销后名称即可重新使用 |
| `token_prefix` | 前 12 个字符，用于在列表和审计详情中识别 |
| `scopes` | 见下文；至少 1 个，最多 64 个 |
| `ip_allowlist` | IP 或 CIDR；留空表示任意地址；最多 256 项 |
| `rate_limit_rps` | 按单个服务实例计算；`0` 表示不限；最大 1 000 000 |
| `expires_at` | 设置时必须是将来的时间；为空表示永不过期 |
| `revoked_at` | 未吊销时为空 |
| `last_used_at`、`last_used_ip` | 每 30 秒批量写入数据库，因此会略有延迟 |
| `created_by` | `user:<id>`、`token:<id>` 或 `system:cli` |

---

## 令牌权限范围

一个权限范围由名称构成，后面可选地跟一个 `:` 和一个参数。

| 权限范围 | 参数 | 授予的权限 |
| --- | --- | --- |
| `lease:acquire[:<站点>]` | 精确站点名，可选 | `lease:acquire` |
| `report:write[:<站点>]` | 精确站点名，可选 | `report:write` |
| `config:read[:<分组通配符>]` | 配置分组通配符，可选 | `config:read` |
| `config:publish[:<分组通配符>]` | 配置分组通配符，可选 | `config:read`、`config:write`、`config:publish` |
| `secret:read:<命名空间>/<路径通配符>` | **必填** | `secret:read` |
| `identity:write[:<站点>]` | 精确站点名，可选 | `identity:read`、`identity:write`、`identity:operate` |
| `proxy:write` | 无 | `proxy:read`、`proxy:write`、`proxy:operate` |
| `admin` | 无 | 令牌所在命名空间内 `admin` 角色的全部权限 |

解析器强制的规则：

- 省略可选参数会把权限范围放宽到**所有**站点或**所有**分组。
- 站点参数是**精确比较**，不能包含 `*` 或 `?`；想允许所有站点请直接省略参数。它还必须符合站点名模式 `^[a-z0-9_][a-z0-9_.-]{0,63}$`。
- `secret:read` **必须**带参数，其通配符匹配的是 `"<命名空间名>/<密钥路径>"`。命名空间部分必须是令牌自身所属的命名空间。
- 权限范围不能包含空白字符、控制字符或非法 UTF-8；重复的权限范围会被拒绝。
- 每个令牌最多 64 个权限范围；API 层面每个权限范围最多 256 个字符（解析器的硬上限是每个权限范围 512 字节、参数 256 字节）。

### 通配符语法

通配符只有两个元字符：

| 记号 | 匹配 |
| --- | --- |
| `*` | 任意长度的字符序列，包括空串和 `/` |
| `?` | 恰好一个字符 |

其余一切——包括 `[`、`]` 和 `\`——都是字面量。没有字符类，也没有转义。匹配绝不会指数级回溯——最坏情况是 O(模式长度 × 输入长度) 且不产生任何内存分配——因此恶意模式无法拖垮服务端。

```text
config:read:crawler*            匹配分组 crawler、crawler.hk、crawlerx
secret:read:prod/signing/*      匹配 prod/signing/api_key、prod/signing/a/b
secret:read:prod/*              匹配命名空间 prod 的所有密钥
lease:acquire:example-site      只匹配名为 example-site 的站点
lease:acquire                   匹配该命名空间的所有站点
```

密钥路径在匹配前会被规范化：空路径、以 `/` 开头或结尾、以及包含 `.` 或 `..` 段的路径一律拒绝，因此无法通过路径别名绕过通配符。

### 控制台中的预设

权限范围构建器提供三个起点：

| 预设 | 权限范围 |
| --- | --- |
| 爬虫节点 | `lease:acquire`、`report:write`、`config:read` |
| Cookie 刷新 | `identity:write` |
| CI 发布 | `config:publish` |

### 如何选择权限范围

为每个节点单独创建令牌，只授予它真正需要的权限范围。只租借身份并上报结果的令牌不需要 `config:publish`，几乎永远也不需要 `admin`。只给确实需要读取密钥的节点授予 `secret:read`——无论是直接调用 `GetSecret`，还是因为它的配置项里带 `${secret:…}` 引用、由服务端代为解析；通配符没覆盖到的路径一律拒绝。见[密钥保管库](./10-secrets.md)和[配置中心](./09-config-center.md)。

权限范围之外的调用以 `permission_denied` 失败，原因为 `scope_missing`。错误消息只提到权限名，绝不提及资源，因此无法被用来探测某个资源是否存在。

---

## IP 白名单、限速与有效期

**IP 白名单。** 每项可以是裸地址（`203.0.113.7`、`::1`，会变成单地址网段）或 CIDR 网段（`10.0.0.0/8`、`2001:db8::/32`）。留空表示允许任意地址。来自名单之外的请求会以 `permission_denied` / `ip_not_allowed` 拒绝。

客户端地址取 TCP 对端地址；若对端落在 `SPINNERET_TRUSTED_PROXIES` 内，则依次采信 `X-Forwarded-For`（从右向左遍历，跳过可信跳）和 `X-Real-IP`。**如果部署在反向代理之后却没有设置 `SPINNERET_TRUSTED_PROXIES`，所有白名单看到的都会是代理的地址，而不是客户端的地址。** 见[配置参考](./03-configuration.md)。

**限速。** `rate_limit_rps` 是**按单个服务实例**计算的令牌桶，突发容量为一秒。三个副本挂在负载均衡后面时，限速 100 在最坏情况下允许 300 次/秒。超限返回 `resource_exhausted`，原因为 `rate_limited`，并附带 `Spinneret-Retry-After-Ms` 响应头。`0` 表示不限速。

**有效期。** `expires_at` 必须是将来的时间。过期的令牌以 `unauthenticated` / `token_expired` 失败。有效期是限制令牌泄露损失最便宜的手段；宁可设置有限有效期并配套轮换流程，也不要用永久令牌。

**轮换。** 没有删除令牌的 RPC。轮换的做法是：先创建新令牌，灰度上线，再吊销旧令牌。由于唯一性约束只覆盖未吊销的令牌，旧令牌被吊销后，新令牌可以沿用同一个名称。

---

## 吊销

吊销令牌是立即生效且不可撤销的：

1. 数据库中写入 `revoked_at`。
2. 在 Redis/Valkey 事件总线上发布 `token.revoked` 事件。
3. 每个服务实例收到事件后，从自己的校验缓存中丢弃该令牌。
4. 携带它的请求以 `unauthenticated`、原因 `token_revoked` 失败。

正在运行的节点会在下一次调用时失败；没有宽限期，重试也没有用。节点应把 `token_revoked`、`token_expired` 和 `token_invalid` 视为致命错误并停止，而不是重试——见[节点 API 参考](./13-node-api.md)。

如果事件总线不可用，吊销仍会在各实例的缓存过期后生效：`SPINNERET_TOKEN_CACHE_TTL`，默认 30 秒。未知令牌哈希会被负缓存 5 秒。重复吊销已吊销的令牌不产生任何效果。

---

## 用令牌创建令牌

`admin` 权限范围包含 `token:write`，因此持有它的令牌可以再创建令牌。为了避免这成为一条提权路径，**由令牌创建的令牌**永远不能比父令牌更宽。创建时会从数据库重新读取父令牌那一行，因此校验依据的是它当前的限制，而且在认证之后才被吊销或过期的令牌无法创建任何东西。

| 规则 | 失败表现 |
| --- | --- |
| 子令牌不能被授予 `admin` 权限范围 | `permission_denied` |
| 父令牌已吊销或已过期 | `token_revoked` / `token_expired` |
| 父令牌有有效期时，子令牌必须也有，且不得晚于父令牌 | `permission_denied`，并给出父令牌的到期时间 |
| 父令牌有 IP 白名单时，子令牌的白名单不能为空，且每个网段都必须落在父令牌的某个网段之内 | `permission_denied`，并指出不合规的条目 |
| 父令牌有限速时，子令牌必须限速，且不得超过父令牌的速率 | `permission_denied`，并给出父令牌的限速值 |

父令牌白名单为空表示允许任意地址，因此此时任何子令牌白名单都会被接受。不同地址族的网段互不包含，所以在只有 IPv4 白名单的父令牌之下创建 IPv6 子令牌会被拒绝。

用户和 CLI 不受这套规则约束：控制台里的 `admin` 可以创建该命名空间允许的任意令牌。

---

## 会话、Cookie 与 CSRF

控制台通过 `AuthService.Login` 登录，这是唯一不需要认证的 RPC。

**密码。** 至少 10 个字符，最多 1024 个。使用 Argon2id 哈希——64 MiB 内存、3 次迭代、并行度 2、16 字节盐、32 字节密钥。校验在并发上限之下进行，避免突发登录耗尽内存；对不存在的用户名会用一个占位哈希做一次校验，使耗时不暴露账号是否存在。任何失败都返回同一条消息。

**登录节流。** 在 Redis/Valkey 中以 15 分钟为窗口计数：每个用户名 5 次失败，每个客户端 IP 20 次。超过任一上限返回 `resource_exhausted` / `login_throttled`，并给出重试等待时间。登录成功会清除用户名计数并释放 IP 名额；管理员重置密码同样会清除用户名计数。

**会话 Cookie。**

| 属性 | 值 |
| --- | --- |
| 名称 | `spinneret_session` |
| 值 | 43 个字符，32 字节随机数的 base64url 编码 |
| 属性 | `HttpOnly`、`SameSite=Strict`、`Path=/`、`Max-Age` = 会话 TTL |
| `Secure` | 由 `SPINNERET_COOKIE_SECURE` 决定：`auto`（默认——请求经 TLS 到达，或经可信代理并带 `X-Forwarded-Proto: https` 时置位）、`true`、`false` |
| 生命周期 | `SPINNERET_SESSION_TTL`，默认 12 小时，最小 1 分钟 |
| 存储 | 服务端保存在 Redis/Valkey 中，以 Cookie 值的 SHA-256 为键——Cookie 本身只是一串随机数 |

过期时间是**滑动**的：当剩余时间不足 TTL 的一半时，服务端会延长会话并在同一个响应里重新下发 Cookie。

**CSRF。** 所有使用 Cookie 认证、方法不安全（除 `GET`、`HEAD`、`OPTIONS` 之外）的请求都必须携带 `X-Spinneret-CSRF: 1`。缺失时以 `permission_denied`、原因 `csrf_missing` 拒绝。跨站表单无法设置自定义请求头，而 `SameSite=Strict` 本身已经阻止 Cookie 随跨站请求发出；这个请求头是第二道锁。API 令牌使用 `Authorization: Bearer` 认证，不受 CSRF 约束。

**当前租户。** 控制台请求用 `X-Spinneret-Tenant: <租户 ID>` 选择租户。无法设置请求头的 `GET` 请求（SSE 事件流）改用 `tenant` 查询参数。选择一个用户没有绑定的租户会以 `permission_denied` 失败。

**什么会结束会话。**

| 事件 | 影响 |
| --- | --- |
| 退出登录 | 仅当前会话 |
| 修改自己的密码 | 该用户的所有**其他**会话；当前会话保留 |
| 管理员重置密码 | 该用户的所有会话 |
| 停用账号 | 该用户的所有会话 |
| TTL 到期 | 该会话 |

密码变更有两重保障：一方面清空该用户的会话索引，另一方面每个会话都记录了自己创建时的密码代次，因此漏网的会话依然会被拒绝。

---

## 审计日志

所有与安全相关的操作都会被记录：管理类变更、密钥读取与明文查看、登录与登出，以及被拒绝的敏感操作尝试。写入经过缓冲并批量落库，处理请求的代码不会因为审计表而阻塞。

### 字段

| 字段 | 含义 |
| --- | --- |
| `id` | `aud_…` |
| `created_at` | 操作发生的时间 |
| `tenant_id` / `namespace` | 租户，以及操作涉及的命名空间；命名空间为空表示租户级操作 |
| `actor_kind` | `user`、`token` 或 `system`（后台任务） |
| `actor_id` | `usr_…`、`tok_…`，`system` 时为空 |
| `actor_name` | 用户名、令牌名称或任务名 |
| `action` | 例如 `secret.read`、`identity.bulk_operate`、`config.publish` |
| `resource_kind` / `resource_id` / `resource_name` | 被操作的对象 |
| `result` | `ok`、`denied` 或 `error` |
| `ip` | 客户端地址，按可信代理规则解析 |
| `user_agent` | 客户端 User-Agent |
| `details` | 操作相关的 JSON——绝不包含密钥值或身份载荷 |

### 动作

| 范围 | 动作 |
| --- | --- |
| 认证 | `auth.login`、`auth.logout`、`user.change_password` |
| 用户与访问 | `user.create`、`user.update`、`user.reset_password`、`role_binding.create`、`role_binding.delete`、`token.create`、`token.revoke` |
| 租户与命名空间 | `tenant.create`、`tenant.update`、`tenant.delete`、`namespace.create`、`namespace.update`、`namespace.delete` |
| 站点 | `site.create`、`site.update`、`site.delete`、`site.pause`、`site.resume`、`endpoint_group.create`、`endpoint_group.update`、`endpoint_group.delete`、`uri_rules.replace` |
| 身份 | `identity_type.create`、`identity_type.update`、`identity_type.delete`、`identity.import`、`identity.update`、`identity.update_payload`、`identity.reveal`、`identity.bulk_operate`、`identity.revert_actions`、`account.upsert` |
| 代理 | `proxy.import`、`proxy.update`、`proxy.delete`、`proxy.check`、`proxy.<操作>` |
| 策略 | `policy.create`、`policy.save_draft`、`policy.publish`、`policy.rollback`、`policy.delete`、`policy.bind`、`policy.unbind` |
| 配置 | `config.create`、`config.draft.save`、`config.publish`、`config.rollback`、`config.delete`、`config.read`（仅记录被拒绝的情况） |
| 密钥 | `secret.create`、`secret.update`、`secret.delete`、`secret.reveal`、`secret.read` |
| 熔断器 | `breaker.open`、`breaker.close` |
| 通知 | `notify.channel.create`、`notify.channel.update`、`notify.channel.delete`、`notify.channel.test` |
| 平台 | `kek.rewrap_start` |

每一次密钥读取和明文查看都会带上版本和客户端 IP 被记录下来，无论成功还是被拒绝；节点读取还会额外记下调用方声明的用途。事故发生后，你要找的就是这条记录。

### 如何查询

**访问控制 › 审计日志**，或调用 `AccessAdminService.ListAuditLogs`。命名空间、操作者、动作、资源类型、资源 ID、结果这些过滤条件都是**精确匹配**，不是子串匹配；`actor` 同时匹配操作者 ID 或操作者名称。API 对每个过滤条件都有长度上限：命名空间和资源类型最多 64 个字符，操作者、动作和资源 ID 最多 128 个字符；`result` 只能是 `ok`、`denied` 或 `error`。时间范围默认最近 24 小时，记录按时间倒序返回，页大小默认 50、上限 500。

谁能看到什么：

| 主体 | 可见范围 |
| --- | --- |
| 指定了命名空间过滤 | 需要该命名空间上的 `audit:read` |
| 拥有覆盖整个租户的 `audit:read` | 当前租户的所有记录，含租户级记录 |
| 只在部分命名空间拥有 `audit:read` | 这些命名空间的记录；一个都没有时返回 `permission_denied` |
| 未选择租户的平台管理员 | 租户为空的平台级记录（登录、KEK 操作） |

### 可靠性与保留期

记录先进入一个有界内存缓冲（50 000 条），每秒以 500 条为一批刷盘。缓冲写满时——例如数据库变慢而操作洪峰同时发生——记录会被**丢弃**而不是拖垮 API，丢弃会被计数并写入日志。数据库拒绝的批次会被不断二分，直到定位出问题的那一条，因此一条坏记录只会丢掉它自己。

`audit_logs` 按月分区。`partition_manager` 主节点任务每小时运行一次，创建即将用到的分区并删除已过期的分区。保留期由 `SPINNERET_RETENTION_AUDIT` 控制，默认 8760h（一年）。如果需要更长时间的记录，请在分区被删除之前导出——见[运维手册](./16-operations.md)。

---

## 加固清单

**谁该拥有什么**

- [ ] 平台管理员：人越少越好，最好两个。他们绕过所有租户边界，并持有 `kek:manage`。
- [ ] 覆盖整个租户的 `owner`：对租户访问权限负责的人。只有 `owner` 能授予 `owner`，且最后一个不能被移除。
- [ ] `admin`：负责发布、掌握凭据的人。`secret:reveal` 和 `identity:reveal` 随这个角色一起给出——这正是该角色的意义，也是不应随意发放的原因。
- [ ] `operator`：日常工作人员的默认角色。个别人偶尔需要发布配置时，给他加一项 `config:publish` 额外权限，而不是提升角色。
- [ ] `viewer`：其他所有人，包括只看仪表盘的相关方。
- [ ] 外包人员和小组：固定到一个命名空间并带站点列表的 `viewer` 或 `operator` 绑定。确认他们读不到配置和密钥——限定站点的绑定在命名空间级只授予 `namespace:read` 和 `proxy:read`。

**令牌**

- [ ] 每个节点、每个任务一个令牌——绝不使用共享的“集群令牌”。
- [ ] 权限范围收窄到实际使用的站点、配置分组和密钥路径。
- [ ] `admin` 权限范围：不用，除非某个工具确实需要管理整个命名空间。
- [ ] `secret:read` 只给真正读取密钥的地方，并使用能满足需求的最紧通配符。
- [ ] 每个令牌都设置有效期，并配套一个比有效期更早触发的轮换流程。
- [ ] 节点地址稳定的场景都配置 IP 白名单，同时确保 `SPINNERET_TRUSTED_PROXIES` 正确。
- [ ] 每个令牌都设置限速，并牢记它是按实例计算的。

**会话与运维**

- [ ] 任何可通过网络访问的部署都设置 `SPINNERET_COOKIE_SECURE=true`（或在 TLS 之后使用 `auto`）。
- [ ] 如果控制台暴露在可信网络之外，把 `SPINNERET_SESSION_TTL` 从 12 小时调短。
- [ ] 人员离职：停用账号（会结束其所有会话），并吊销他创建的令牌。
- [ ] 定期复查 `secret.read`、`secret.reveal` 和 `identity.reveal` 的审计记录。
- [ ] 审计保留期要覆盖你的事故处理流程；一年的默认值不够时，安排导出。

这些取舍背后的威胁模型，见[安全](./19-security.md)。

---

## 下一步

- [安全](./19-security.md) —— 威胁模型，软件负责什么、运维负责什么。
- [密钥保管库](./10-secrets.md) —— 信封加密、KEK 轮换，以及 `secret:reveal` 的代价。
- [节点 API 参考](./13-node-api.md) —— 节点如何认证，各个错误原因是什么意思。
- [命令行工具](./15-cli.md) —— `spnr admin init`、`spnr token create` 及其他命令。
- [控制台总览](./05-console-overview.md) —— 作用域切换器，以及哪些权限对应哪些页面。
