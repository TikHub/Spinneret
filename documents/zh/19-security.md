# 安全

**Spinneret 保护什么、怎么保护，以及哪些部分仍然由你负责。在把真实凭据放进系统之前请先读这一页；在把控制台暴露到私有网络之外之前，再读一遍。**

[English](../en/19-security.md)

---

## 目录

- [威胁模型](#威胁模型)
- [信任边界](#信任边界)
- [静态加密](#静态加密)
- [KEK 由你负责](#kek-由你负责)
- [控制台认证与会话](#控制台认证与会话)
- [API 令牌安全](#api-令牌安全)
- [会交出明文的操作](#会交出明文的操作)
- [审计日志](#审计日志)
- [传输安全与反向代理](#传输安全与反向代理)
- [多租户隔离](#多租户隔离)
- [输入校验与请求限制](#输入校验与请求限制)
- [依赖与供应链](#依赖与供应链)
- [加固清单](#加固清单)
- [漏洞报告](#漏洞报告)

---

## 威胁模型

Spinneret 本质上是一个带调度器的凭据仓库。它保存 Cookie、设备参数、账号密码、带凭据的代理 URL、
签名密钥和 API Key，并且每秒要把它们通过网络交给机器几百次。这就是要保护的资产。本页的所有内容，
都是为了不让这份资产从你没打算开的门里流出去。

### 攻击者是谁

| 攻击者 | 假定的能力 | 软件层面的应对 |
| --- | --- | --- |
| 网络攻击者 | 能窃听或篡改节点与服务端、浏览器与服务端之间的流量 | 软件本身不做任何事 —— 除非你提供证书或在前面放一个 TLS 终止层，否则 Spinneret 走明文 HTTP。见[传输安全](#传输安全与反向代理) |
| 被窃取的节点令牌 | 持有从被攻陷的爬虫节点上拿到的一个明文 API 令牌 | 令牌绑定到单一租户与单一命名空间，受权限范围限制，可选 IP 白名单与限速，并且可以在数秒内在所有实例上吊销 |
| 好奇或恶意的控制台用户 | 拥有一个低权限角色的有效登录 | 每次服务调用都做权限检查且失败即拒绝；每次明文查看都被审计；viewer 无法读取密钥明文或身份载荷 |
| 拿到数据库的攻击者 | 拿到 PostgreSQL 数据副本（备份、硬盘、只读副本），但没有 KEK | 身份载荷、代理 URL、密钥值和通知渠道配置都是 AES-256-GCM 密文，无法还原 |
| 同时拿到 KEK 的攻击者 | 数据和密钥文件都拿到了 | 全部可还原。KEK 文件是单点，必须按单点来保护 |
| 浏览器侧攻击者 | 对已登录的控制台用户尝试 XSS、点击劫持或跨站请求 | 会话 Cookie 带 `HttpOnly` + `SameSite=Strict`；非安全方法必须带 CSRF 请求头；严格的 CSP 且脚本不允许 `unsafe-inline`；`frame-ancestors 'none'` |
| 撞库攻击者 | 对控制台登录接口尝试密码 | Argon2id 哈希、按用户名与按 IP 的限流、所有失败返回同一个错误 |

### Spinneret 明确不防御什么

这些必须如实告诉批准你上线的人：

- **被攻陷的主机。** 任何拿到服务器 root、或能读取进程内存的人，都能读到 KEK、DEK 缓存里所有已解开
  的数据密钥，以及正在流转的明文。
- **被攻陷的节点。** 节点本来就会以明文收到 Cookie、账号凭据和代理 URL。被接管的节点把它们留下来，
  没有任何机制能阻止。请收紧令牌权限范围、给节点地址加白名单，并把节点失陷直接当作凭据失陷处理。
- **持有 KEK 的运维人员。** 没有密钥托管、没有 HSM 集成、没有知识分割。能读到
  `SPINNERET_KEK_FILE` 的人就能解密数据库里的一切。
- **平台管理员。** 平台管理员在所有租户里拥有全部权限，包括 `kek:manage`。没有任何配置能限制他们。
- **来自已认证调用方的拒绝服务。** 有按令牌的限速和请求体上限，但没有全局准入控制，无法阻止一个繁忙
  的租户在共享实例上挤占另一个租户。
- **目标站点相关的一切。** Spinneret 不做签名、不做登录流程、不做验证码。节点拿到凭据之后做什么，
  不在它的模型之内。

---

## 信任边界

| 边界 | 由谁跨越 | 认证方式 | 说明 |
| --- | --- | --- | --- |
| 节点 → 服务端 | `LeaseService`、`ReportService`、`ConfigService`、`SecretService` | `Authorization: Bearer spn_…` | 租户与命名空间固定；其余由权限范围决定 |
| 浏览器 → 服务端 | 控制台服务（`*AdminService`、`AuthService`、`DashboardService`） | `spinneret_session` Cookie + `X-Spinneret-CSRF: 1` | 活动租户由 `X-Spinneret-Tenant` 选择，并对照用户的角色绑定校验 |
| 浏览器 → 服务端（SSE） | `GET /api/v1/events/stream` | 只有 `spinneret_session` Cookie —— `GET` 属于安全方法，不需要 CSRF 请求头 | 活动租户来自 `?tenant=` 查询参数，因为 `EventSource` 设置不了请求头；校验方式与请求头完全相同 |
| CLI → 数据库 | `spnr admin init`、`spnr token create`、`spnr kek …`、`spnr migrate`、`spnr seed`、`spnr rebuild` | 直连 PostgreSQL（`SPINNERET_DATABASE_URL`） | **完全绕过 API 及其权限检查。** 能对数据库运行 `spnr` 的人等同于平台管理员 |
| 服务端 → PostgreSQL / Valkey / ClickHouse | 每一次请求 | 连接串里带什么就是什么 | Spinneret 自身不加密这些连接；请用 `sslmode=verify-full` 并做网络隔离 |
| 服务端 → KEK 文件 | 只在进程启动时 | 文件系统权限 | 只读取一次并转成 AES-GCM 实例，之后每次解包都用内存里的实例，不再碰文件系统；派生完密钥编排后原始字节即被清零 |
| 无需认证的暴露面 | `GET /healthz`、`GET /readyz`、`GET /metrics`、控制台静态资源 | 无 | `/metrics` 会暴露按 procedure 的计数与内部状态；请放到 `SPINNERET_METRICS_ADDR`，不要留在对外监听器上 |

**注意。** 控制台用户的 `Principal.TenantID` 是一次 UI 选择，不是安全边界，角色绑定才是。把
`X-Spinneret-Tenant` 设成你没有绑定的租户会返回 `permission_denied` —— 平台管理员例外，他们可以选择
任何已存在的租户；而且无论请求头是什么，每次权限检查都会重新读取绑定。

---

## 静态加密

所有敏感值都用**信封加密**封装（`internal/vault`）：

```text
ciphertext  = nonce(12) || AES-256-GCM(DEK, nonce, plaintext, AAD)
wrapped DEK = nonce(12) || AES-256-GCM(KEK, nonce, DEK, "spinneret-dek:" + kekID)
```

- 每个被封装的值都会生成一把全新的 32 字节随机 **DEK**。它从不以明文存储，库里只有被包裹后的形式，
  与密文以及包裹它的 KEK id 放在一起。
- 数据 **AAD** 是 `<记录 ID> || 0x00 || <字段>`，例如 `idt_…\x00payload:v3`。把密文复制到另一行或
  另一列会认证失败。
- DEK 的 AAD 把被包裹的密钥绑定到 KEK id 上，所以即使两个 id 恰好用了相同的密钥材料，给一个包裹后的
  DEK 换个 id 标签也解不开。
- 所有解密失败都包装同一个哨兵错误 `vault: decryption failed`，到达 API 调用方时只会是一个通用的
  `internal` 错误。服务端的日志信息确实会区分"密文认证失败"和"包裹后的 DEK 解不开"，方便排查；而进程
  不持有的 KEK id 是另一个独立错误（`vault: unknown kek id`）。

### 哪些字段被加密

| 表 | 加密列 | 内容 |
| --- | --- | --- |
| `identity_payloads` | `ciphertext`、`wrapped_dek`、`kek_id` | Cookie、令牌、设备参数、账号密码 |
| `proxies` | `url_ciphertext`、`url_wrapped_dek`、`url_kek_id` | 含用户名与密码的完整代理 URL |
| `secret_versions` | `ciphertext`、`wrapped_dek`、`kek_id` | 每个密钥的每个版本的值 |
| `notification_channels` | `config_ciphertext`、`config_wrapped_dek`、`config_kek_id` | Webhook 地址与渠道凭据 |
| `system_keys` | `ciphertext`、`wrapped_dek`、`kek_id` | 服务端自己生成的内部密钥（例如上报去重用的 pepper） |

### 哪些字段没有加密

请假定任何能访问数据库的人都能读到下面这些：

- **各处的元数据**：身份 ID、状态、健康分、站点与客户端名称、标签、冷却与封禁时间戳、代理的
  `display_url`（只有主机和端口，凭据已剥离）、密钥路径、描述与过期时间。
- **配置项。** 配置值以明文存储。敏感内容请放进密钥保管库，再用 `${secret:...}` 引用 ——
  见[配置中心](./09-config-center.md)。
- **审计日志**，包括每条记录的 IP 地址、User-Agent 和 `details`。
- **ClickHouse 里的分析数据**：请求判定结果、延迟、状态码、标记。
- **密码和 API 令牌**不是被加密，而是被哈希 —— 这是正确的做法，也意味着它们根本无法还原。

---

## KEK 由你负责

密钥加密密钥在进程启动时从 `SPINNERET_KEK_FILE`（文件）或 `SPINNERET_KEKS`（内联的
`id:base64,id2:base64` 列表）加载，由 `SPINNERET_KEK_CURRENT` 决定哪一把用于包裹新数据。一把 KEK 正好
32 字节、base64 编码；id 必须匹配 `^[a-zA-Z0-9_-]{1,32}$`。

```bash
# 生成一行可直接写进 KEK 文件的密钥。
spnr kek generate --id k1
```

软件保证的部分：

- 密钥材料绝不出现在错误信息或日志里。格式错误的 id 不会被回显，因为格式错误的 id 往往就是放错位置的
  密钥材料。
- 启动横幅里 `keks` 只报告"有没有"，不报告值。
- KEK 文件读取上限为 1 MiB，路径配错会立刻失败而不是耗尽内存。
- 派生出 AES 密钥编排之后立即清零原始 DEK 字节。DEK 缓存里只存派生出的 AEAD 实例
  （`SPINNERET_DEK_CACHE_SIZE`，默认 `100000`；`SPINNERET_DEK_CACHE_TTL`，默认 `10m`），
  且只要 TTL 为正就会有一个清理协程按时清除，密钥材料不会超期驻留。

你必须做的部分：

1. **备份密钥，并且放在数据库之外的机器上。** 用丢失的 KEK 加密的数据无法恢复，没有任何补救路径。
2. **密钥备份与数据库备份分开存放。** 同时包含两者的备份包，等价于一份未加密的数据库。
3. **收紧文件权限。** `scripts/compose-init.sh` 生成的 `deploy/compose/secrets/kek.key` 权限是
   `0644`，目的是让 distroless 镜像里的 `nonroot` 用户能读到这个 bind mount。真实部署里请把它收紧到
   服务运行的 uid，或改用平台的密钥存储并以只读方式挂载。
4. **有计划地轮换。** 加入新密钥 → 设为 current → 重启 → 运行 `spnr kek rewrap`，等到
   `spnr kek status` 显示没有记录仍由旧密钥包裹，才移除旧密钥。移除仍在包裹数据的 KEK 会让那部分数据
   不可读（`vault: unknown kek id`）。完整步骤见[运维手册](./16-operations.md)与
   [密钥保管库](./10-secrets.md)。

---

## 控制台认证与会话

### 密码

| 属性 | 取值 | 来源 |
| --- | --- | --- |
| 算法 | Argon2id | `internal/auth/password.go` |
| 参数 | m = 64 MiB，t = 3，p = 2，16 字节 salt，32 字节 key | `DefaultArgon2Params` |
| 存储形式 | `users.password_hash` 中的 PHC 串 `$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>` | |
| 比较方式 | 常数时间（`crypto/subtle`） | |
| 最短长度 | 10 个字符 | `MinPasswordLength` |
| 最长长度 | 1024 个字符 | `MaxPasswordLength` |
| 字符规则 | 合法 UTF-8；不做复杂度要求 | `ValidatePassword` |

不设复杂度要求是有意为之 —— 长度才是控制手段。并发的 Argon2id 计算由信号量限流（每次需要 64 MiB），
所以一波登录高峰不会耗尽机器内存。对不存在的用户名尝试登录时，服务端仍会与一个占位哈希做一次比较，
因此时间差不会泄露账号是否存在；所有失败都返回同一条消息：`invalid username or password`
（用户名或密码错误）。

### 登录限流

| 维度 | 上限 | 窗口 | 错误 |
| --- | --- | --- | --- |
| 按用户名 | 5 次失败 | 15 分钟 | `login_throttled`（`resource_exhausted`），带重试提示 |
| 按客户端 IP | 20 次失败 | 15 分钟 | 同上 |

计数器存在 Redis 里，并且**在验证密码之前就先占位**，因此并发猜测无法突破上限。登录成功会清空该用户名
的计数器并归还 IP 名额；管理员重置密码也会清空该用户名的计数器。`ChangePassword` 走同一套限流，所以
修改密码表单不能被拿来爆破当前密码。

### 会话

| 属性 | 取值 |
| --- | --- |
| Cookie 名 | `spinneret_session` |
| 值 | 32 字节随机数，base64url 无填充（43 个字符） |
| 存储形式 | 以 Cookie 值的十六进制 SHA-256 为键的 Redis 哈希 —— 原始值从不落库 |
| 生命周期 | `SPINNERET_SESSION_TTL`，默认 `12h`，滑动续期 |
| 续期时机 | 剩余时间不足一半时延长，并重新下发 Cookie |
| 属性 | `HttpOnly`、`SameSite=Strict`、`Path=/`、`Max-Age = SessionTTL` |
| `Secure` | `SPINNERET_COOKIE_SECURE`：`auto`（默认 —— 请求走 TLS 到达，或经受信代理并带 `X-Forwarded-Proto: https` 时置位）、`true`、`false` |

每个会话记录用户 ID、创建与过期时间、客户端 IP、截断后的 User-Agent，以及一个**密码代次**（`cred`），
即该用户 `password_changed_at` 的微秒值。代次与用户当前值不一致的会话会被拒绝，即使它侥幸没被删除。
下面这张表就是靠这个机制实现的。

### 什么会终结会话

| 事件 | 对会话的影响 | 审计动作 |
| --- | --- | --- |
| 退出登录 | 结束当前这一个会话 | `auth.logout` |
| 自己修改密码 | 保留发起修改的那个会话，**结束该用户的其它所有会话** | `user.change_password` |
| 管理员重置密码 | **结束该用户的所有会话**，并清空该用户名的登录限流 | `user.reset_password` |
| 账号被禁用 | 认证立即失败（`session_invalid`，"account is disabled"） | `user.update` |
| 会话 TTL 到期 | Redis 自动删除该哈希 | — |

执行操作的那个实例上立即生效，同时会发出 `user.changed` 事件，其它实例立刻丢弃缓存副本；如果没有事件
总线，其它实例会在 5 秒的用户缓存 TTL 内收敛。

### CSRF

使用 Cookie 认证、方法不是 `GET`、`HEAD`、`OPTIONS` 的请求，必须带上：

```text
X-Spinneret-CSRF: 1
```

缺少该头会以 `csrf_missing`（`permission_denied`）被拒绝。跨站的表单或图片请求设置不了这个头，而
`SameSite=Strict` 本身已经阻止 Cookie 被跨站发送；这个头是第二把锁。Bearer 令牌的请求不需要它 ——
它们不依赖环境凭据。

---

## API 令牌安全

### 格式与存储

| 属性 | 取值 |
| --- | --- |
| 形态 | `spn_` + 43 个 base62 字符（共 47 个字符），由 32 字节随机数生成 |
| 存储 | `api_tokens.token_hash` 中的 SHA-256 摘要（唯一），外加 `token_prefix` 里前 12 个字符用于识别 |
| 明文 | 只在创建它的那次调用中返回一次，既不存储也不会再显示 |
| 格式非法的凭据 | 在查缓存和数据库之前就按形态拒绝 |

控制台在创建对话框里说的那句话是真的：令牌丢了就吊销并重建，没有找回的办法。

### 权限范围

令牌的权限是其所有权限范围的并集。每个权限范围都会针对本次调用的资源做匹配，未知或结构非法的权限范围
不授予任何权限。

| 权限范围 | 参数 | 授予的权限 |
| --- | --- | --- |
| `lease:acquire` | 可选，精确站点名 | `lease:acquire` |
| `report:write` | 可选，精确站点名 | `report:write` |
| `config:read` | 可选，配置分组通配符 | `config:read` |
| `config:publish` | 可选，配置分组通配符 | `config:read`、`config:write`、`config:publish` |
| `secret:read` | **必填**，对 `<命名空间名>/<路径>` 的通配符 | `secret:read` |
| `identity:write` | 可选，精确站点名 | `identity:read`、`identity:write`、`identity:operate` |
| `proxy:write` | 无 | `proxy:read`、`proxy:write`、`proxy:operate` |
| `admin` | 无 | `admin` 角色的全部权限 |

解析器强制执行的规则：权限范围最长 512 字节、参数最长 256 字节，不允许空白字符、控制字符和非法 UTF-8；
站点名参数不得包含 `*` 或 `?`（想放开所有站点就省略参数）；`secret:read` 不带参数会被拒绝；不允许重复；
每个令牌最多 64 个权限范围。密钥路径通配符只对规范的相对路径做匹配 —— 含空段、`.` 或 `..` 段的路径
永不匹配，因此无法通过路径别名绕过通配符。

`admin` 是需要格外小心的那一个。它在令牌所属命名空间内授予整个 `admin` 角色，包括 `secret:reveal` 和
`token:write`。节点永远不需要它。

### IP 白名单、限速与过期

| 控制项 | 行为 | 错误 |
| --- | --- | --- |
| IP 白名单 | 最多 256 个 IP 地址或 CIDR 网段，留空表示任意地址。比对的是*解析出的客户端 IP* —— 见[受信代理](#传输安全与反向代理) | `ip_not_allowed`（`permission_denied`） |
| 限速 | `rate_limit_rps`，0（不限）到 1,000,000。令牌桶，突发容量为 1 秒，**按单个服务实例计算** —— 3 副本部署总体大约允许 3 倍配置值 | `rate_limited`（`resource_exhausted`），带重试提示 |
| 过期 | 可选的 `expires_at`，设置时必须是将来时间 | `token_expired`（`unauthenticated`） |
| 吊销 | 写入 `revoked_at`；`token.revoked` 事件让所有实例立即从缓存中丢弃该令牌 | `token_revoked`（`unauthenticated`） |

验证结果缓存 `SPINNERET_TOKEN_CACHE_TTL`（默认 `30s`），未知令牌哈希缓存 5 秒。因此在事件总线不可用时，
吊销会在缓存 TTL 内在其它实例上生效，而不是立刻生效。最近使用时间与 IP 在内存里缓冲、每 30 秒落盘一次，
所以"最近使用"的读数最多会滞后这么久。

### 令牌无法提权

当一个 API 令牌去创建另一个令牌时（通过 `admin` 权限范围获得 `token:write`），子令牌会对照父令牌在数据库
中的*当前*记录做检查 —— 认证之后被吊销或过期的父令牌创建不了任何东西：

- 子令牌不能被授予 `admin` 权限范围。
- 父令牌有过期时间时，子令牌必须设置 `expires_at`，且不得晚于父令牌。
- 父令牌有 IP 白名单时，子令牌的白名单必须非空，且每个网段都必须落在父令牌的某个网段之内。
- 父令牌有限速时，子令牌必须限速，且不得高于父令牌。

这四种失败都返回 `permission_denied`。

---

## 会交出明文的操作

这是审计评审要盯的清单。除此之外的接口返回的都是元数据或掩码后的值。

| 操作 | 谁能执行 | 审计动作 | 说明 |
| --- | --- | --- | --- |
| `SecretAdminService.RevealSecret` | `secret:reveal`（`admin` 及以上角色，或作为绑定上的额外权限） | `secret.reveal` —— 成功、拒绝与错误都记 | 控制台显示 60 秒，并提示本次查看会被记录 |
| `SecretService.GetSecret` | 持有匹配 `secret:read:<命名空间>/<通配符>` 权限范围的 **API 令牌**；用户会话会被直接拒绝 | `secret.read` —— 含版本、用途、结果与客户端 IP | 仅供节点使用 |
| `IdentityAdminService.GetIdentity` 且 `reveal` | `identity:reveal`（`admin` 角色，或作为额外权限） | `identity.reveal` —— 含站点与载荷版本 | 没有该权限时，敏感字段返回 `••••` 加最后四个字符；未声明的字段完全掩码 |
| `LeaseService.Acquire` / `AcquireBatch` | 令牌权限范围 `lease:acquire` | **不审计**（热路径 —— 通过请求明细和租约状态观察） | 返回渲染后的凭据，以及**带凭据**的完整代理 URL |
| `AccessAdminService.CreateToken` | `token:write`（`admin` 及以上角色） | `token.create` —— 记录权限范围、白名单、限速、前缀、过期，绝不记明文 | 明文只返回一次 |
| `AccessAdminService.ResetPassword` | `user:write`（`owner` 角色）；平台管理员才能管理平台管理员 | `user.reset_password` | 新密码由调用方指定，不发邮件也不自动生成 |
| `spnr token create` | 任何能用 `SPINNERET_DATABASE_URL` 连上数据库的人 | **不审计** —— 行记录里创建者是 `system:cli`，但不写审计条目 | 把明文打印到 stdout |
| `spnr admin init` | 同上 | **不审计**；创建的对象归属 `system:bootstrap` | 创建第一个平台管理员；已存在时直接退出 |

有两点值得明确写进评审结论：**CLI 是一条不被审计的管理通道**，以及**持有 `lease:acquire` 的节点按设计
就会收到真实凭据**。

代理 URL 从不带凭据返回给控制台 —— `Proxy.display_url` 只有主机和端口；代理健康检查的错误信息在落库前
也会把凭据抹掉。

---

## 审计日志

审计条目写入分区表 `audit_logs`，在控制台的 **审计日志** 页面可读（需要 `audit:read`，`viewer` 及以上角色
都有，范围限于读者能看到的命名空间）。

### 一条记录包含什么

`created_at`、`tenant_id`、`namespace_id`、`actor_kind`（`user` / `token` / `system`）、`actor_id`、
`actor_name`、`action`、`resource_kind`、`resource_id`、`resource_name`、`result`
（`ok` / `denied` / `error`）、`ip`、`user_agent`，以及一个 JSON 的 `details` 对象。

访问控制与保管库层记录的动作包括 `auth.login`、`auth.logout`、`user.change_password`、`user.create`、
`user.update`、`user.reset_password`、`role_binding.create`、`role_binding.delete`、`token.create`、
`token.revoke`、`secret.create`、`secret.update`、`secret.delete`、`secret.reveal`、`secret.read` 和
`identity.reveal`。其它领域（身份、代理、策略、配置、站点、租户操作）各自还有自己的动作。

### 它保证什么

- **拒绝也会被记录，不只是成功。** 被拒绝的明文查看会以 `result = "denied"` 写入，试探性的用户会留下
  痕迹。
- 条目在入库前会被清洗：文本字段与 `details` 内部的非法 UTF-8 和 NUL 字符都替换为 U+FFFD，恶意字符串
  无法弄坏写入器。
- 被数据库拒绝的批次会被不断拆分，直到定位出问题条目；一条坏数据只丢它自己。
- 除了分区级的保留期清理，应用代码不会更新或删除任何一行。

### 它不保证什么

- **写入是缓冲的、尽力而为的。** 条目进入进程内 50,000 槽位的缓冲区，每秒按 500 条一批刷出。缓冲区溢出，
  或数据库在重试预算耗尽后仍不可用时，条目会被**丢弃并计数**，而不是阻塞 API。审计日志绝不能把控制平面
  拖垮 —— 这是有意的取舍，也意味着这份日志不是防篡改账本。
- **数据库层面不是只追加的。** 任何拥有 PostgreSQL 写权限的人都能改动它。需要防篡改就把条目转发到一个
  一次写入的外部存储。
- **保留期有限。** `SPINNERET_RETENTION_AUDIT` 默认 `8760h`（365 天），旧分区会被删除。

---

## 传输安全与反向代理

### TLS

Spinneret **默认提供明文 HTTP**。支持两种形态：

1. **在前面终止 TLS**（推荐）。Compose 栈用 Caddy 做这件事，任何反向代理都可以。记得设置
   `SPINNERET_TRUSTED_PROXIES`，服务端才会相信转发头。
2. **在服务端终止。** 同时设置 `SPINNERET_TLS_CERT_FILE` 和 `SPINNERET_TLS_KEY_FILE`（只设其中一个
   属于配置错误）。密钥对在启动时加载，证书有问题会让启动失败，而不是拖到第一个请求。最低版本 TLS 1.2，
   启用 TLS 时同时开启 HTTP/2。没有证书时监听器走 HTTP/1.1 与明文 HTTP/2。

### 正确拿到客户端 IP

有两样东西依赖解析出的客户端 IP 是对的：**令牌 IP 白名单**和**按 IP 的登录限流**。这里配错，两个方向
都会失效。

`SPINNERET_TRUSTED_PROXIES` 是逗号分隔的 CIDR 网段列表。规则是：

- TCP 对端**不在**受信网段内时，转发头被完全忽略，直接用对端地址。直连的攻击者无法伪造
  `X-Forwarded-For`。
- TCP 对端**在**受信网段内时，从右往左遍历 `X-Forwarded-For` 的各跳，跳过本身是受信代理的那些，取第一个
  非受信跳。如果所有跳都受信，取最左边那个。如果 `X-Forwarded-For` 里拿不到可用地址，则尝试唯一的
  `X-Real-IP`，最后回退到对端地址。

**警告。** 在反向代理后面把 `SPINNERET_TRUSTED_PROXIES` 留空，会让所有请求看起来都来自代理：IP 白名单
匹配的是代理而不是节点，按 IP 的登录限流会把整个部署当成一个地址来限。设得太宽 —— 比如信任
`0.0.0.0/0` —— 则任何客户端都能伪造自己的地址。只信任代理所在的那个网段。

来自受信对端的 `X-Forwarded-Proto: https` 也会把请求标记为安全，这正是 `SPINNERET_COOKIE_SECURE=auto`
在终止型代理后面能置上 `Secure` 属性的原因。如果你的代理不发这个头，请显式设置
`SPINNERET_COOKIE_SECURE=true`。

Compose 栈自带的是 `SPINNERET_TRUSTED_PROXIES: 172.16.0.0/12,10.0.0.0/8,192.168.0.0/16` —— 对私有
Docker 网络是对的，但只要服务端还能被其它私有网络访问到，它就太宽了。真实部署请收窄。

### 安全响应头与 CSP

控制台的静态处理器会在 HTML 与静态资源响应上设置：

| 响应头 | 值 |
| --- | --- |
| `X-Content-Type-Options` | `nosniff` |
| `X-Frame-Options` | `DENY` |
| `Referrer-Policy` | `same-origin` |
| `Content-Security-Policy` | 见下 |

```text
default-src 'self'; script-src 'self' <每段内联脚本的 sha256>;
style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:;
connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'self';
form-action 'self'
```

这些哈希在启动时从构建出的 `index.html` 计算得出，因此脚本不需要 `unsafe-inline`。`style-src` 确实允许
`'unsafe-inline'`，这是 UI 组件库的要求。`frame-ancestors 'none'` 与 `X-Frame-Options: DENY` 一起阻止
被嵌入 iframe。

这些头只由控制台处理器设置。API 响应不带它们 —— 那是 JSON，不会被当作文档渲染。如果前面有代理，在代理上
加一条 `Strict-Transport-Security` 是值得的；Spinneret 自己不设置它。

### CORS

`SPINNERET_ALLOWED_ORIGINS` 默认为空，未配置任何来源时 CORS 中间件根本不会装上：不会有任何跨域请求拿到
放行的响应头。确实需要配置时（控制台开发服务器需要），列出的来源会得到
`Access-Control-Allow-Credentials: true`；通配符 `*` 允许任意来源但**不带**凭据。不要把你不掌控的生产
域名列进去。

### 其它监听器

| 端点 | 认证 | 建议 |
| --- | --- | --- |
| `GET /healthz`、`GET /readyz` | 无 | 暴露给负载均衡器没问题；它们只暴露依赖名称与就绪状态 |
| `GET /metrics` | 无 | 绑到内网接口上的 `SPINNERET_METRICS_ADDR`。否则它会挂在主监听器上 |
| `SPINNERET_PPROF_ADDR` | **无** | 默认关闭。这些端点会暴露堆内容与协程栈 —— 其中包含明文。只在回环或私有地址上开启，开启时服务端会打一条警告日志 |

---

## 多租户隔离

隔离由一个不做任何 I/O 的纯函数包（`internal/authz`）强制执行，所有服务都调用它。它失败即拒绝：未知权限、
未知角色、未知权限范围、未知主体类型、空主体和格式错误的资源，一律拒绝。

- **租户**是硬边界。用户必须在某个租户里有角色绑定，才能看到该租户里的任何东西。令牌在创建时被钉死在
  一个租户*和*一个命名空间上，永远无法访问别的。
- **命名空间**在租户内部做划分。绑定可以钉到某个命名空间；令牌则一定被钉住。
- **站点**进一步收窄绑定（最多 500 个站点 ID）。受站点限制的绑定在命名空间层面额外只得到 `proxy:read`
  和 `namespace:read`，而且仅当该绑定钉在那个命名空间上时才有 —— 除此之外没有别的。
- **跨租户查询返回 not_found 而不是 permission_denied**：查询别的租户里的令牌或用户返回 `not_found`，
  不会确认某个 ID 是否存在。权限错误只说明缺哪个权限，绝不提资源。
- **绑定上的额外权限**被限定在一个固定集合里：`config:publish`、`secret:reveal`、`identity:reveal`、
  `policy:publish`。角色之上不能加别的。
- **平台专属权限**（`tenant:manage`、`kek:manage`）只属于平台管理员和内部系统主体，任何角色、权限范围或
  额外权限都授予不了。

### 已知的限制

- **隔离是逻辑上的，不是物理上的。** 所有租户共用一个 PostgreSQL 数据库、一个 Redis 键空间、一个
  ClickHouse 数据库。数据库层没有行级安全；一个 SQL 缺陷或直连数据库就能一次性跨越所有边界。
- **资源是共享的。** 按令牌的限速是唯一配额。同一实例上，一个租户的负载会影响另一个租户的延迟。租户之间
  必须互不影响时，请分开部署。
- **平台管理员看得到一切。** 包括通过 `secret:reveal` 看到每个租户的密钥。
- **KEK 是全局的。** 没有按租户的密钥。一次 KEK 泄漏就是所有租户的泄漏。
- **事件总线与热状态是共享的**，只按键前缀（`SPINNERET_REDIS_PREFIX`）划分，而不是按凭据。

完整的权限与角色表见[租户、用户与令牌](./11-access-control.md)。

---

## 输入校验与请求限制

校验发生在边界上、handler 运行之前：`protovalidate` 执行写在 `proto/spinneret/v1/*.proto` 里的约束，
服务层在 Go 里再校验一遍。非法输入返回 `invalid_argument`，不会被静默纠正。

| 限制 | 值 | 位置 |
| --- | --- | --- |
| 节点请求体 | 8 MiB | `nodeMaxRequestBytes` |
| 管理端请求体 | `SPINNERET_ADMIN_MAX_REQUEST_BYTES`，默认 64 MiB | 导入需要这个余量 |
| HTTP 请求头字节数 | 1 MiB | |
| 请求头读取超时 | 10 秒 | |
| 请求体读取超时 | 6 分钟 | 长轮询与 SSE 不设写超时 |
| 空闲连接超时 | 120 秒 | |
| 一元 handler 截止时间 | 60 秒；导入与批量操作 5 分钟；客户端更短的 `Connect-Timeout-Ms` 优先 | `deadlineInterceptor` |
| `WatchConfig` 长轮询 | 客户端超时上限 60 秒，另加 10 秒余量 | |
| 密钥值 | 64 KiB | `MaxSecretValueBytes` |
| 每个令牌的权限范围数 | 64 个；单个权限范围 ≤ 512 字节，参数 ≤ 256 字节 | |
| IP 白名单条目 | 256 条 | |
| 每个角色绑定的站点数 | 500 个 | |
| 用户名 | 3–64 个字符，`^[a-z0-9][a-z0-9._-]{2,63}$` | |
| 租户 / 命名空间名 | 2–63 个字符，`^[a-z0-9][a-z0-9-]{1,62}$` | |
| `X-Spinneret-Node` | 清洗为 `[A-Za-z0-9._:@/-]`，最长 128 字节 | 其余字符变成 `_` |
| 存储的 User-Agent | 截断到 512 字节（会话记录里 256 字节） | |
| 活动租户请求头 | 64 字节 | |
| SSE 订阅数 | `SPINNERET_MAX_WATCHERS`，默认 20000 | |

**查询成本。** 分析查询带着显式的 `max_execution_time` 下发到 ClickHouse。当 ClickHouse 因为超过内存或
行/字节配额而拒绝查询时，服务端返回 `query_too_large`（`resource_exhausted`）；超时返回 `query_timeout`
（`deadline_exceeded`）。两个错误信息都会提示运维缩小时间范围或增加筛选，而不是抛出一个内部错误。请把
ClickHouse 侧的限制设成你的硬件扛得住的值 —— Compose 栈自带了一个 `clickhouse-limits.xml`。

**错误信息卫生。** 内部错误在返回客户端之前统一转成通用的 `internal` 消息，原因连同操作者记录在服务端
日志里。SQL、驱动和调用栈细节绝不越过 API 边界。handler 的 panic 会被捕获、连同栈打印到日志，并以
`internal` 返回。

---

## 依赖与供应链

仓库里真实存在的检查，来自 `.github/workflows/ci.yml` 和 `.golangci.yml`：

| 检查 | 工具 | 说明 |
| --- | --- | --- |
| 生成代码与源文件一致 | `buf generate`、`sqlc generate`，再 `git diff --exit-code` | 被篡改或过期的生成文件会让 CI 失败 |
| 静态检查 | `go vet` | |
| 安全 lint | 启用 `gosec` 的 `golangci-lint` | 还有 `errorlint`、`bodyclose`、`noctx`、`nilerr`、`rowserrcheck`、`sqlclosecheck` |
| 测试 | `go test -race -count=1 -skip 'TestStart.*Container' ./...`，对接真实的 PostgreSQL、Valkey、ClickHouse 服务容器 | 被跳过的是那些自己启动容器的测试，CI 里已经由服务容器提供 |
| Web 控制台 | `pnpm typecheck`、`pnpm lint`、`pnpm test`、`pnpm build`，并使用 `--frozen-lockfile` | |
| Python SDK | 在 3.9 / 3.12 / 3.13 上跑 `pytest`、`ruff check`、`ruff format --check`；`mypy src` 只在 3.12 上跑 | |
| 容器构建 | `docker/build-push-action` 构建 `deploy/docker/Dockerfile`（不推送） | |
| 工作流权限 | `permissions: contents: read` | 工作流无法写入仓库 |

构建本身的性质：

- **工具链部分固定。** Go 版本取自 `go.mod`；工作流里把 `buf` 固定在 v1.73.0、`sqlc` 固定在 v1.31.1。
  但同一步骤里的 `protoc-gen-go` 和 `protoc-gen-connect-go` 是按 `@latest` 安装的，因此生成代码的 diff
  检查跑在一条浮动的工具链上 —— 如果你需要这项检查可复现，请把它们也固定住。
- **依赖锁定。** Go 用 `go.sum`，控制台用 `pnpm-lock.yaml` 配合 `--frozen-lockfile`。
- **最小运行时镜像。** `gcr.io/distroless/static-debian12:nonroot` —— 没有 shell、没有包管理器 ——
  以 `nonroot:nonroot` 身份运行 `CGO_ENABLED=0` 的静态二进制。控制台被嵌进二进制里，所以没有可写的
  Web 根目录。
- **运行时不产生额外出网流量**，除了 PostgreSQL、Redis/Valkey、ClickHouse、你的代理、代理健康检查 URL
  和你的通知渠道。

目前 CI 里**没有**、需要你自己补上的：依赖漏洞扫描（`govulncheck`、Dependabot）、容器镜像扫描、SBOM 生成
与产物签名。仓库根目录已经有 `SECURITY.md`（内容就是[漏洞报告](#漏洞报告)一节所述的流程）和 Apache-2.0
的 `LICENSE`。

---

## 加固清单

第一次上生产之前，请逐条走一遍。

**密钥与凭据**

- [ ] `SPINNERET_KEK_FILE` 只有服务运行的那个 uid 可读，或者来自平台的密钥存储。
- [ ] KEK **与数据库备份分开**备份，并且已经演练过恢复。
- [ ] 已运行 `scripts/compose-init.sh`（或等价操作）：PostgreSQL、ClickHouse 和引导管理员都不再使用
      默认密码。
- [ ] 引导管理员的密码是通过 `--password-env` 或 `--password-stdin` 提供的，不是命令行参数，并且首次
      登录后已修改。
- [ ] 数据库、Redis 和 ClickHouse 的凭据是本次部署专用的，连接串没有进版本库。

**网络**

- [ ] TLS 在服务端前面或内部终止；没有任何明文链路穿过不受信网络。
- [ ] `SPINNERET_TRUSTED_PROXIES` 精确指向反向代理所在网段。
- [ ] `SPINNERET_COOKIE_SECURE` 为 `true`，或为 `auto` 且代理会发送 `X-Forwarded-Proto: https`。
- [ ] 生产环境的 `SPINNERET_ALLOWED_ORIGINS` 为空，或只列出你掌控的来源。
- [ ] `SPINNERET_METRICS_ADDR` 绑在内网接口上；`/metrics` 对外不可达。
- [ ] `SPINNERET_PPROF_ADDR` 为空。
- [ ] PostgreSQL、Redis/Valkey 和 ClickHouse 只能从应用网络访问；不同机器时 PostgreSQL 启用 TLS
      （`sslmode=verify-full`）。

**访问控制**

- [ ] 每个人都有自己的账号，不共用控制台登录。
- [ ] 角色按需分配：只读用 `viewer`，日常运维用 `operator`，确实需要 `secret:reveal` 和 `token:write`
      时才给 `admin`，管理用户的少数人才给 `owner`。
- [ ] `secret:reveal` 和 `identity:reveal` 以额外权限的形式挂在具体绑定上，而不是把人提成 `admin`。
- [ ] 平台管理员数量一只手数得过来，并且定期复核。
- [ ] 节点令牌只带够用的最小权限范围 —— 用 `lease:acquire:<站点>` 和 `report:write:<站点>` 而不是不带
      参数的形式，绝不用 `admin`。
- [ ] 节点出口地址稳定的场景下，节点令牌都设置了 `expires_at` 和 IP 白名单。
- [ ] 节点令牌都设置了 `rate_limit_rps`，并按单实例口径估算。
- [ ] 定期清点令牌；长期没有"最近使用"记录的令牌予以吊销。
- [ ] 严格限制对机器和 `SPINNERET_DATABASE_URL` 的访问 —— 对数据库执行 `spnr` 是一条不被审计的管理通道。

**运维**

- [ ] PostgreSQL 的备份不只是"做了"，而是实际恢复验证过。
- [ ] `SPINNERET_RETENTION_AUDIT` 符合你的合规要求。
- [ ] 需要防篡改时把审计条目转发到外部存储；审计写入器的 `Dropped()` 计数没有被忽略。
- [ ] 对反复出现的 `permission_denied`、`login_throttled` 和 `secret.reveal` 配置了告警 ——
      见[可观测性与告警](./12-observability.md)。
- [ ] 完整演练过一次 KEK 轮换（`spnr kek rewrap`、`spnr kek status`）。
- [ ] 升级路径与回滚方案已经写下来 —— 见[运维手册](./16-operations.md)。

---

## 漏洞报告

Spinneret 由 **TikHub** 维护并开源，仓库地址：<https://github.com/TikHub/Spinneret>。

**请不要为安全问题创建公开 issue。** 请使用仓库的 GitHub 私密漏洞报告功能 —— *Security* 标签页 →
*Report a vulnerability* —— 它会创建一条只有维护者可见的私密安全公告。如果你无法使用该功能，请通过
<https://github.com/TikHub> 私下联系维护者，不要把细节发到任何公开位置。

请尽量包含：

- 你测试的版本或提交（`spnr version`，或镜像标签）。
- 涉及的组件：服务端、控制台、CLI、某个 SDK，还是部署资产。
- 攻击者能获得什么，以及需要什么前提（一个账号？一个令牌？某个网络位置？）。
- 复现步骤，或一个最小的概念验证。
- 哪些配置会让问题可达或不可达。

请给维护者一个合理的修复窗口再公开披露。如果你报告的内容最终属于本页已记录的限制 —— 比如被攻陷的节点
保留了它本来就应当收到的凭据 —— 你会得到一个如实说明的答复，同时我们会把这一页写得更清楚。

如果问题出在*你自己的部署*而不是软件里：先轮换，再排查。吊销受影响的令牌、重置受影响的密码、密钥材料
可能泄露时轮换 KEK，然后翻查该时间窗口的审计日志。

---

## 下一步

- [租户、用户与令牌](./11-access-control.md) —— 完整的权限、角色与权限范围表。
- [密钥保管库](./10-secrets.md) —— 密钥的创建、版本、读取与轮换。
- [安装与部署](./02-installation.md) —— 在实际场景中配置反向代理与 TLS。
- [配置参考](./03-configuration.md) —— 本页提到的每一个变量。
- [运维手册](./16-operations.md) —— KEK 轮换、备份与故障处置预案。
