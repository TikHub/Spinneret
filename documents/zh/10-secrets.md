# 密钥保管库

**用于存放 API Key、密码、证书，以及任何节点或配置项需要、但绝不该提交进代码仓库的值。本页讲清加密设计、KEK、密钥的创建与版本管理、节点如何读取，以及审计记录。**

[English](../en/10-secrets.md)

---

## 目录

- [保管库是什么](#保管库是什么)
- [信封加密](#信封加密)
- [KEK（密钥加密密钥）](#kek密钥加密密钥)
- [密钥：路径、值与版本](#密钥路径值与版本)
- [在控制台中管理密钥](#在控制台中管理密钥)
- [节点如何读取密钥](#节点如何读取密钥)
- [权限与权限范围](#权限与权限范围)
- [明文查看](#明文查看)
- [轮换密钥](#轮换密钥)
- [轮换 KEK](#轮换-kek)
- [哪些操作会被审计，哪些不会](#哪些操作会被审计哪些不会)
- [过期与告警](#过期与告警)
- [限制](#限制)
- [下一步](#下一步)

---

## 保管库是什么

保管库是按命名空间划分、以路径寻址的加密值存储。一个密钥包含路径（`signing/api_key`）、描述、标签、可选的过期时间，以及一串不可变的版本。默认只提供当前版本；旧版本在密钥被删除之前一直可读。

值对外**只写不读**。一旦写入，控制台只显示掩码（`••••abcd`），别无其他。明文离开服务端只有三条路径：

1. 节点调用 `SecretService.GetSecret`，其 API 令牌的 `secret:read` 通配符覆盖该路径。这是设计中的正常机制，不是泄漏。
2. 节点读取一个包含 `${secret:...}` 引用的配置项；服务端解析引用并返回替换后的内容 —— 同样要求令牌的 `secret:read` 通配符覆盖被引用的路径。
3. 拥有 `secret:reveal` 权限的控制台用户调用 `SecretAdminService.RevealSecret`。此操作会被审计，控制台在 60 秒后自动隐藏。

还有第四条内部路径：身份载荷中类型为 `secret_ref` 的字段，会在为租约渲染凭据时被解析。这条路径不单独做鉴权、也不单独写审计，因为鉴权发生在引用被**写入**载荷的那一刻 —— 见[间接：通过身份载荷](#间接通过身份载荷)与[哪些操作会被审计，哪些不会](#哪些操作会被审计哪些不会)。

同一套信封机制不只服务于保管库密钥。身份载荷、代理 URL、通知渠道配置和内部系统密钥都使用同一个 KEK 和同样的按记录数据密钥，因此 KEK 轮换是一次覆盖全部的操作。

---

## 信封加密

每个加密值都用一把全新的随机 32 字节**数据加密密钥（DEK）**封装，这把 DEK 再由服务端在内存中持有的**密钥加密密钥（KEK）**加密（“包装”）。两层都是 AES-256-GCM，随机 12 字节 nonce 前置于密文。

```text
密文        = nonce(12) || AES-256-GCM(DEK, 明文, AAD)
包装后的 DEK = nonce(12) || AES-256-GCM(KEK, DEK, "spinneret-dek:" + <kek id>)
```

每条记录存三样东西：`ciphertext`、`wrapped_dek` 和 `kek_id`。KEK 本身绝不入库。

### 附加认证数据（AAD）

两层都用 AES-GCM 的附加认证数据（AAD）绑定到各自的上下文：

| 层 | AAD | 作用 |
| --- | --- | --- |
| 值 | `<记录 id>` + `\x00` + `<字段>` —— 密钥版本为 `<secret id>\x00v<version>` | 密文被复制到另一行、另一列或另一个版本号下都无法解密 |
| 包装后的 DEK | `spinneret-dek:<kek id>` | 被改标为另一个 KEK id 的包装 DEK 无法解开，即使两个 id 持有相同的密钥材料 |

### 分别存在哪里

| 表 | 加密列 | 包装后的 DEK | KEK id |
| --- | --- | --- | --- |
| `secret_versions` | `ciphertext` | `wrapped_dek` | `kek_id` |
| `identity_payloads` | 载荷 | `wrapped_dek` | `kek_id` |
| `proxies` | 代理 URL | `url_wrapped_dek` | `url_kek_id` |
| `notification_channels` | 渠道配置 | `config_wrapped_dek` | `config_kek_id` |
| `system_keys` | 内部密钥（例如去重 pepper） | `wrapped_dek` | `kek_id` |

密钥的**元数据**以明文存放在 `secrets` 表中：`path`、`description`、`tags`、`current_version`、`expires_at`、`last_accessed_at`、`created_by` 以及各时间戳。

### 拿到数据库但没有 KEK 的攻击者能得到什么

一份不含 KEK 的 PostgreSQL 副本能给出：

- 每个密钥的**路径**、描述、标签、版本数量、创建时间和最近访问时间；
- 每条密文和每个包装后的 DEK，两者都打不开；
- 每条记录使用的 KEK **id** —— 这是一个标识符，不是密钥材料。

它拿不到任何一条明文，也无法把密文从一条记录挪到另一条，因为 AAD 已把密文绑定到具体的记录和字段。

**把路径当作公开信息。** 不要把值、账号或客户信息编进路径里。

### DEK 缓存

如果每次读取都要解开一次 DEK，KEK 就会变成热点。因此，每个解开的 DEK 派生出的 AES-GCM 实例会被放进 LRU 缓存，键是 KEK id 与包装后 DEK 的 SHA-256；原始 DEK 字节在派生出密钥编排后立即清零。缓存大小由 `SPINNERET_DEK_CACHE_SIZE` 控制（默认 `100000`），条目在 `SPINNERET_DEK_CACHE_TTL` 后过期（默认 `10m`）。见[配置参考](./03-configuration.md)。

---

## KEK（密钥加密密钥）

### 格式

一把 KEK 是 32 字节随机数据（AES-256），以 base64 编码。KEK id 须匹配 `^[a-zA-Z0-9_-]{1,32}$`。

服务端从两个配置项加载并合并密钥：

| 变量 | 含义 |
| --- | --- |
| `SPINNERET_KEK_FILE` | 存放密钥的文件路径，最大 1 MiB |
| `SPINNERET_KEKS` | 以逗号分隔的内联列表：`id:base64,id2:base64` |
| `SPINNERET_KEK_CURRENT` | 用于包装新 DEK 的 KEK id；默认取最后列出的那把（文件条目在前，`SPINNERET_KEKS` 条目在后） |

`SPINNERET_KEK_FILE` 与 `SPINNERET_KEKS` 至少要设置一个，否则服务端拒绝启动并报 `SPINNERET_KEK_FILE or SPINNERET_KEKS is required`。

KEK 文件按行组织：

```text
# 注释行和空行会被忽略；开头的 BOM 会被跳过。
k1:zvR7t0nQ2mS8k3F1xYbA5cD9eG4hJ6lN8pQ0rT2uV4w=
k2:8pQ0rT2uV4wzvR7t0nQ2mS8k3F1xYbA5cD9eG4hJ6lN=
```

如果文件里只有一行不带 `id:` 前缀的裸 base64，也会被接受，并被赋予 id `k1`。这种简写形式只在它是文件中唯一一把密钥时有效。每把密钥必须解码为恰好 32 字节；标准与 URL-safe base64、带填充与不带填充都可以。同一个 id 出现两次，只有密钥材料完全一致时才允许。

密钥材料绝不会出现在错误信息或日志中。格式错误的 id 也不会被回显，因为这种错误通常意味着密钥材料放错了位置。

### 生成一把 KEK

```bash
# 打印一行可直接粘贴进 KEK 文件的内容。
spnr kek generate --id k1

# 或者，手边没有这个二进制时：
printf 'k1:%s\n' "$(openssl rand -base64 32)"
```

`spnr` 是管理用 CLI；它与服务端打包在同一个容器镜像里，`make build` 也会把它放进 `bin/`。在源码检出目录中，`go run ./cmd/spnr …` 效果完全相同。如何对着 Compose 部署运行它，见[命令行工具](./15-cli.md)。

Compose 栈已经替你做好了：`scripts/compose-init.sh` 会把 `deploy/compose/secrets/kek.key` 写成一行 `k1:<base64>`，权限 `0644`（容器以非 root 用户运行，必须能读取这个绑定挂载的 secret），各服务拿到 `SPINNERET_KEK_FILE: /run/secrets/kek`。见[安装与部署](./02-installation.md)。

### 备份

**警告。** 用某把 KEK 加密的数据，没有这把 KEK 就无法恢复。一份没有 KEK 的数据库备份，能还原出全部路径、描述和标签，却还原不出任何一个值。请在写入第一个密钥之前，就把这把密钥与数据库分开备份，并通过解码验证备份是否可用：

```bash
# 必须输出 32。
cut -d: -f2 deploy/compose/secrets/kek.key | base64 -d | wc -c
```

把备份放在数据库备份之外的地方 —— 离线的密码管理器、硬件令牌、封存的信封。两者同时丢失，是本系统中唯一不可恢复的故障模式。

只要还有记录引用某把已退役的 KEK，就绝不能把它从配置里删掉。服务端会把这些记录报告为不可解密：列表中显示 `••••` 而不是带后四位的掩码，读取会返回 internal 错误，`spnr kek status` 会把该密钥标记为 `NOT-CONFIGURED`。

---

## 密钥：路径、值与版本

### 路径

路径相对于命名空间，须匹配 `^[a-z0-9][a-z0-9_./-]{0,255}$`，不能包含空段、`.` 或 `..` 段，也不能以 `/` 结尾。规范形式很重要：令牌的权限范围通配符是针对 `<命名空间名>/<路径>` 匹配的，非规范路径会被直接拒绝，从而杜绝通过路径别名绕过通配符的可能。

`/` 在路径中只是一个普通字符，但控制台会据此把路径组织成目录树，所以保持一致的前缀约定很划算：

```text
signing/api_key
signing/api_secret
database/read_replica_password
partner-api/token
```

`(命名空间, 路径)` 唯一。删除后重建的同名路径是一个全新的密钥，拥有新的 ID（`sec_<32 位十六进制>`），版本号从 1 重新开始。

### 版本

每次写入的值都是一个不可变版本，从 1 开始编号。用新值更新密钥会追加版本 *n+1* 并移动 `current_version`；旧版本仍可解密，可按 id 与版本号读取。只修改描述、标签或过期时间不会产生新版本。

每个版本都记录了包装其数据密钥的 KEK id，以及写入它的主体（`user:<id>` 或 `token:<id>`）。

删除一个密钥会**立即永久**删除它的**全部**版本。引用它的配置项将无法在节点上解析。

### 值

值为 1 字节到 64 KiB 的合法 UTF-8。二进制材料必须先编码（base64、PEM）再存入。

---

## 在控制台中管理密钥

**密钥**位于导航的**配置**分组下。进入该页面需要 `secret:list`。对平台管理员，页面有两个标签页：**密钥**和**主密钥（KEK）**；其他用户看到的只有密钥列表，没有标签栏。列表每行一个密钥，显示掩码值、当前版本、过期时间、最近访问和标签；左侧是按路径构建的目录树（只取前 500 个路径，被截断时会有提示）；顶部可按搜索词和标签筛选。

点击某一行会打开详情面板，含三个标签页：

| 标签页 | 内容 |
| --- | --- |
| 概览 | 元数据、掩码值、过期时间、最近访问、创建人、时间戳 |
| 版本 | 版本号、KEK id、创建人与创建时间，最新在前 |
| 访问记录 | 已审计的读取、明文查看与变更，最新在前：时间、主体、操作、版本、结果、IP 地址 |

掩码规则：不超过 8 个字符的值显示为 `••••`，更长的值显示 `••••` 加上最后四个字符。无法解密的值（其 KEK 未配置）退化为 `••••`，服务端按操作汇总打印一条告警，而不是每个密钥一条。

创建和编辑需要 `secret:write`；**查看明文**操作需要 `secret:reveal`。当前登录用户不具备相应权限时，控制台会把对应操作置为禁用，并在提示气泡中说明缺少哪个权限。

---

## 节点如何读取密钥

### 直接调用 GetSecret

`SecretService.GetSecret` 接受一个相对于令牌命名空间的路径和一个版本号（`0` 读取当前版本）。

```bash
curl -s https://spinneret.example.internal/spinneret.v1.SecretService/GetSecret \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"path":"signing/api_key","version":0}'
```

```json
{
  "path": "signing/api_key",
  "version": 3,
  "value": "…",
  "expires_at": null
}
```

使用 SDK：

```python
value = client.get_secret("signing/api_key").value
pinned = client.get_secret("signing/api_key", version=3).value
```

```go
resp, err := client.GetSecret(ctx, &spinneret.GetSecretRequest{Path: "signing/api_key"})
```

请求中从不携带命名空间：命名空间就是令牌所绑定的那个。处理器会拒绝任何非 API 令牌的主体，返回 `permission_denied`，消息为 *"SecretService requires an API token; use SecretAdminService.RevealSecret"* —— 控制台会话无法通过节点 API 读取密钥。

### 间接：通过配置引用

配置项内容中可以写 `${secret:<path>}` 或 `${secret:<path>#<version>}`。服务端在把配置项下发给节点时替换成明文，因此进入配置版本历史的是引用，而不是值。

```yaml
# 配置分组 "crawler"，键 "http"
upstream:
  token: ${secret:partner-api/token}
  signing_key: ${secret:signing/api_key#3}
```

适用规则：

- 路径必须是规范形式，与存储密钥时完全一致；固定版本必须是正整数。
- 没有转义语法。每一处 `${secret:` 都必须构成合法引用，否则配置项校验失败。
- 每个配置项最多 **100 个不同**的引用。
- `json` 格式的内容在替换时会做 JSON 字符串转义；`yaml` 和 `text` 使用原始值。
- 发布时会校验每个被引用的路径 —— 以及每个固定版本 —— 在该命名空间中确实存在。否则返回 `failed_precondition` 和 *"referenced secrets do not exist in the namespace: …"*。
- `GetConfig`、`BatchGetConfig` 与 `WatchConfig` 下发的是替换后的内容，并附带一个标志表明该项含有引用。见[配置中心](./09-config-center.md)。

解析按请求进行：一次响应中每个不同的引用读取一次，每次读取写一条 `secret.read` 审计记录。服务端不缓存已解析的配置密钥值。

有两种失败值得认清：

| 场景 | 错误 |
| --- | --- |
| 令牌拥有该分组的 `config:read`，却没有匹配的 `secret:read` 通配符 | `permission_denied` —— *"reading config item `<group>/<key>` requires read access to secret `<path>`"* |
| 被引用的密钥在配置项发布之后被删除 | `failed_precondition` —— *"config item `<group>/<key>` references secret `<path>` which does not exist"* |

### 间接：通过身份载荷

身份类型可以声明类型为 `secret_ref` 的字段，其值是一个相对于命名空间的密钥路径；保管库会在为租约渲染凭据时解析它 —— 这样一把共享的 API Key 只需在保管库里存一份，而不必复制进每一个身份载荷。见[身份与账号](./06-identities.md)。

这条路径与上面两条有意做得不同：

- 渲染时它**不做**自己的权限检查。检查发生在**写入**时：如果一次载荷写入让某个 `secret_ref` 字段引入了该字段原本没有引用过的路径，写入方就必须具备读取该密钥的权限 —— 令牌需要匹配 `<命名空间名>/<路径>` 的 `secret:read` 权限范围，用户需要该命名空间上的 `secret:reveal` —— 并且该密钥必须存在于身份所属的命名空间中。否则写入被拒绝，返回 `permission_denied`（*"the payload references secret `<path>`, which requires secret:read"*，用户主体则是 `secret:reveal`）或 `invalid_argument`（*"… which does not exist in namespace `<ns>`"*）。同一字段中保持不变的引用不会被重新鉴权。也就是说，存一个引用等价于读一次密钥，这正是这条路径真正的安全控制点。
- 它**不写**按次读取的审计记录。
- 它前面有两层缓存。只要渲染过程用到了密钥，渲染出的凭据就会被缓存最多 **60 秒**（`SecretCredentialTTL`）；而这层缓存过期后的重新加载，又可能命中保管库自身的解析缓存 —— 该缓存按命名空间与路径保留解析结果 **30 秒**（LRU 4096 条，约 16 MiB），同一密钥的并发未命中共享一次数据库读取。发生变更的那个实例会立即清除解析缓存。因此最坏情况下，身份的 `secret_ref` 在轮换之后仍可能提供约 **90 秒**的旧值。

---

## 权限与权限范围

| 权限 | 持有者 | 允许 |
| --- | --- | --- |
| `secret:list` | viewer、operator、admin、owner | 列出密钥及其掩码值、读取元数据、列出版本、查看访问记录 |
| `secret:write` | admin、owner | 创建、更新（包括写入新版本）、删除 |
| `secret:reveal` | admin、owner；也可作为附加权限单独授予某个角色绑定 | 在控制台查看明文；非令牌主体通过配置解析读取密钥时同样需要它 |
| `secret:read` | **仅令牌**，通过 `secret:read` 权限范围获得 | 节点经 `GetSecret` 及 `${secret:...}` 解析读取 |
| `kek:manage` | 仅平台管理员 | 查看 KEK 状态、启动重新包装 |

`secret:read` 是节点专属的：没有任何控制台角色包含它，也无法授予用户。反过来，令牌只有通过 `admin` 权限范围才能拿到 `secret:list`、`secret:write` 和 `secret:reveal`。

### secret:read 权限范围

权限范围写作 `secret:read:<glob>`，参数是**必填**的。通配符针对 `<命名空间名>/<路径>` 匹配：

- `*` 匹配任意字符序列，**包括** `/`。
- `?` 精确匹配一个字符。
- 其余一切字符，包括 `[`、`]` 和 `\`，都是字面量。

由于一个令牌只绑定一个命名空间，通配符的命名空间部分必须是该命名空间的名字（或能覆盖它的通配符）。

| 权限范围 | `prod/signing/api_key` | `prod/db/password` | 令牌位于命名空间 `staging` |
| --- | --- | --- | --- |
| `secret:read:prod/*` | 允许 | 允许 | 拒绝 |
| `secret:read:prod/signing/*` | 允许 | 拒绝 | 拒绝 |
| `secret:read:prod/signing/api_key` | 允许 | 拒绝 | 拒绝 |
| `secret:read:prod/db/?` | 拒绝 | 拒绝（`password` 多于一个字符） | 拒绝 |
| `secret:read:*` | 允许 | 允许 | 允许，但仅限 `staging` 内 |
| `secret:read:db/*`（缺少命名空间部分） | 拒绝 | 拒绝 | 拒绝 |

前缀取巧无效：位于命名空间 `prod-evil` 的令牌不会被 `prod/*` 匹配；`public/../private/key`、`db//pw` 这类非规范请求路径在通配符求值之前就被拒绝。

请授予能用的最小通配符：

```bash
spnr token create \
  --tenant default --namespace prod --name crawler-hk \
  --scope lease:acquire --scope report:write \
  --scope config:read:crawler \
  --scope secret:read:prod/signing/* \
  --expires 720h
```

读取含 `${secret:...}` 配置项的节点，**同时**需要该分组的 `config:read` 权限范围，以及覆盖每个被引用路径的 `secret:read` 通配符。见[租户、用户与令牌](./11-access-control.md)。

---

## 明文查看

在控制台中，密钥行或详情面板上的**查看明文**会弹出确认框，标明版本号，并直白地说明本次查看会以你的账号、IP 地址、版本和时间记录进审计日志。确认后调用 `SecretAdminService.RevealSecret`。

明文只保存在组件状态里 —— 绝不进入查询缓存 —— 并在 **60 秒**后自动隐藏，或者在点击**立即隐藏**、关闭对话框时立刻隐藏。界面提供**复制值**，免得为了复制而反复查看。

鉴权分两步，两者的区别很重要：

- 可见性只由 `secret:list` 决定。对该密钥没有 `secret:list` 的主体得到 `not_found`，因此无法通过 ID 探测其他租户或命名空间的密钥 —— 只持有 `secret:reveal` 并不能让一个原本不可见的密钥变得可见。**不写审计记录** —— 在系统看来，什么都没有被寻址。
- 能看到该密钥但缺少 `secret:reveal` 的主体得到 `permission_denied`，该次尝试以 `secret.reveal`、结果 `denied` 记入审计。

传入版本号即可查看当前版本以外的版本；`0` 表示当前版本。

---

## 轮换密钥

轮换一个值就是一次带新值的 `UpdateSecret`。它追加一个版本并移动当前版本指针；绝不改写已有版本。

只要按正确顺序轮换，运行中的节点不会中断：

1. 先**在目标端添加新凭据**，让新旧两份同时有效。
2. 把新值**存为新版本**（控制台：*编辑* → *新值*；API：带 `value` 的 `UpdateSecret`）。
3. **等待读取方取到新值。**
   - 调用 `GetSecret` 的节点在下一次调用时就能拿到新值 —— 这条路径上没有服务端缓存。
   - 订阅了含**未固定版本** `${secret:<path>}` 引用的配置项的节点**不会**收到新的下发：配置版本没有变化，而 `WatchConfig` 是由配置版本驱动的。它们会在下一次 `GetConfig`，或该配置项被重新发布时取到新值。如果密钥轮换必须立即到达订阅方，请重新发布引用它的配置项。
   - 身份的 `secret_ref` 字段在各实例上于约 **90 秒**内收敛：用到密钥的凭据会被缓存最多 60 秒，而它的重新加载又可能由保管库 30 秒的解析缓存提供。
4. 只有在所有读取方都切换之后，才**在目标端吊销旧凭据** —— 对使用 `secret_ref` 的身份来说，要等满约 90 秒，而不是 30 秒。

固定版本的引用（`${secret:<path>#3}`）会一直提供版本 3，直到该配置项被编辑。当某次变更必须与配置变更一起灰度、而不是按自己的节奏生效时，这就是对应的手段。

**注意。** 无法单独删除某个旧版本 —— 只能删除整个密钥。如果某个泄漏的版本必须变为不可读，请删除该密钥并重建，然后把所有引用它的地方重新指向或重新发布。

---

## 轮换 KEK

轮换会用新的 KEK 重新包装所有数据密钥。它**不会**重新加密数据：只有很小的“包装后 DEK”列发生变化，因此任务开销很低，可以在系统正常承载流量时安全运行。

完整流程：

```bash
# 1. 生成新密钥。
spnr kek generate --id k2

# 2. 把这一行加进 KEK 文件（保留 k1！），并让 k2 成为当前密钥。
#    要么把 k2 放在文件最后一行，要么设置 SPINNERET_KEK_CURRENT=k2。
cat deploy/compose/secrets/kek.key
# k1:…
# k2:…

# 3. 重启所有实例，使它们都同时持有两把密钥。KEK 集合只在进程启动时读取一次，
#    没有重载信号；而密钥文件是绑定挂载的 Compose secret —— 只改文件内容时
#    `up -d` 不会重建任何容器。所以要显式重启进程。
docker compose -f deploy/compose/docker-compose.yml restart spinneret

# 4. 确认两把密钥都已加载，并查看待处理的工作量。
spnr kek status
# current kek: k2
#   k1               wrapped_records=18422
#   k2               wrapped_records=0 current
# rewrap: idle (0/0)

# 5. 全部重新包装到 k2 并等待完成。
spnr kek rewrap
# kek rewrap started
# progress: 5000/18422 records re-wrapped to k2
# ...
# kek rewrap finished

# 6. 只有当 status 显示 k1 的 wrapped_records=0 之后，才从配置中移除 k1
#    并重启。退役的密钥请留一份冷备份。
```

这两个 CLI 命令都需要 `SPINNERET_DATABASE_URL` 和 KEK 相关配置。

`spnr kek status` 为每把密钥打印一行，含其包装的记录数；当前包装用的密钥标注 ` current`，仍被记录引用但本进程未持有的密钥标注 ` NOT-CONFIGURED`。控制台在**密钥 → 主密钥（KEK）**下展示同样的信息，包括进度条和**开始重新包装**按钮；两者都需要 `kek:manage`，只有平台管理员持有。

任务的工作方式：

| 属性 | 取值 |
| --- | --- |
| 覆盖的表 | `identity_payloads`、`secret_versions`、`proxies`、`notification_channels`、`system_keys` |
| 批大小 | 500 行 |
| 并发 | 同一时间只有一个实例执行，通过 PostgreSQL 咨询锁 `spinneret:kek:rewrap` 保证 |
| 进度 | 持久化在 `system_settings` 的 `kek_rewrap_status` 键中，因此每个实例和控制台都能报告 |
| 心跳 | 每 2 秒一次；实例超过 30 秒不再心跳，任务会被报告为已中断 |
| 安全性 | 每次更新都基于旧 KEK id 与旧包装 DEK 做 compare-and-swap，因此并发重新封装过的记录绝不会被覆盖 |

如果另一个实例上已有任务在跑，`spnr kek rewrap` 会跟随该任务的进度而不是再启一个；任务报错时命令以非零码退出。中断 CLI 会停止它自己启动的任务；再次运行会从仍未迁移到当前 KEK 的记录继续。

当 KEK 可能已泄露、持有它的人员离职，或按你的策略排期时，就该轮换 KEK。见[运维手册](./16-operations.md)。

---

## 哪些操作会被审计，哪些不会

审计记录写入 `audit_logs`，包含主体（`user`、`token` 或 `system`）、主体 id 与名称、租户、命名空间、资源类型 `secret`、密钥 id 与路径、结果、客户端 IP、User-Agent，以及一个 JSON 详情对象。它们可在密钥的**访问记录**标签页和租户审计日志中查看（[租户、用户与令牌](./11-access-control.md)）。

### 会被审计

| 操作 | 写入时机 | 结果 | 详情 |
| --- | --- | --- | --- |
| `secret.create` | 创建密钥 | `ok` | `version`（恒为 1） |
| `secret.update` | 修改元数据或写入新版本 | `ok` | `version`、`value_changed`、`description_changed`、`tags_changed`、`expiry_changed` |
| `secret.delete` | 删除密钥及其所有版本 | `ok` | `version`（删除前的当前版本） |
| `secret.reveal` | 能看到该密钥的主体尝试在控制台查看明文 | `ok`、`denied`、`error` | `version` |
| `secret.read` | 节点读取密钥 —— `GetSecret`，或解析 `${secret:...}` 引用 | `ok`、`denied`、`error` | `version`、`purpose`，失败时还有 `error`（`not_found`、`unavailable`、`decrypt`） |
| `kek.rewrap_start` | 请求重新包装（资源类型为 `kek`） | `ok`、`error` | `started` |

`purpose` 用于区分读取的缘由：直接调用 `GetSecret` 为 `api`，为下发配置项而解析引用为 `config:<group>/<key>`。它会被清洗为至多 64 字节，并去掉空白与控制字符。

被拒绝或失败的读取同样会被审计，服务端会反查密钥 id，使这次尝试即便什么都没返回也出现在该密钥的访问记录中。当一次配置读取因缺少 `secret:read` 通配符而被拒绝时，会写入两条记录：`secret.read` 的拒绝记录，以及带 `secret_path` 和 `secret_version` 的 `config.read` 拒绝记录。

### 不会被审计

| 操作 | 原因 |
| --- | --- |
| `ListSecrets`、`GetSecret`（元数据）、`ListSecretVersions`、`ListSecretAccessLogs` | 元数据读取，从不暴露值。它们仍然要求 `secret:list` |
| `GetKEKStatus` | 只读状态；需要 `kek:manage` |
| 渲染凭据时解析身份的 `secret_ref` | 已在租借租约时鉴权；每个租约一条读取记录会淹没审计日志。租约与上报本身就是记录 |
| 命中身份解析 30 秒缓存的读取 | 根本没有发生读取 |
| 完全看不到该密钥（对它没有 `secret:list`）的主体发起的**明文查看**尝试 | 返回 `not_found`；没有任何东西被寻址。被拒绝的**读取**则不同 —— 它一定会被审计（见上） |

### 最近访问

密钥上的 `last_accessed_at` 与审计日志分开维护：读取时把时间戳缓冲在内存里，后台每 10 秒批量写入一次（进程退出时再刷一次）。缓冲区可容纳 100000 个密钥；超出之后，对尚未进入缓冲区的密钥的访问会被丢弃。该字段仅供参考 —— 任何需要精确的场景，请使用访问记录，而不是这个时间戳。

---

## 过期与告警

`expires_at` 是可选的，且只起提示作用：已过期的密钥**仍会**返回给节点，服务端只打印一条告警（`expired secret was read`），带上命名空间、密钥 id 和过期时间。不会有任何东西自行停止工作 —— 过期是提醒，不是强制机制。

告警规则对 7 天内即将过期的密钥每天触发一次，同时覆盖过去 7 天内已经过期的密钥，事件类型为 `secret_expiring`。请在**通知**中把它路由到某个渠道；见[可观测性与告警](./12-observability.md)。

控制台标注同一窗口：列表中的 **7 天内过期**与**已过期**标记。

---

## 限制

| 项 | 限制 |
| --- | --- |
| 密钥路径 | 1–256 字节，`^[a-z0-9][a-z0-9_./-]{0,255}$`，须为规范段 |
| 密钥值 | 1 字节 – 64 KiB，合法 UTF-8 |
| 描述 | 1024 个字符 |
| 标签 | 每个密钥 64 个，每个标签 64 个字符 |
| 列表的标签筛选 | 32 个标签 |
| 搜索词 | 256 个字符 |
| 分页大小 | 默认 50，最大 500 |
| 每个密钥的版本数 | 2 147 483 647（超出返回 `failed_precondition`） |
| 每个配置项中不同的 `${secret:...}` 引用数 | 100 |
| KEK | 恰好 32 字节，id 须匹配 `^[a-zA-Z0-9_-]{1,32}$` |
| KEK 文件 | 1 MiB |
| 控制台明文显示时长 | 60 秒 |

---

## 下一步

- [配置中心](./09-config-center.md) —— 配置项、版本，以及本页所解析的 `${secret:...}` 引用。
- [租户、用户与令牌](./11-access-control.md) —— 如何签发带有恰当 `secret:read` 通配符的令牌。
- [节点 API 参考](./13-node-api.md) —— `GetSecret` 的完整请求与响应，以及错误原因。
- [身份与账号](./06-identities.md) —— `secret_ref` 载荷字段。
- [运维手册](./16-operations.md) —— KEK 备份、轮换排期与恢复演练。
- [安全](./19-security.md) —— 本设计所应对的威胁模型，以及仍由你负责的部分。
