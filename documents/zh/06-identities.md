# 身份与账号

**身份子系统的完整说明：什么是身份、如何用身份类型描述一类身份、身份如何进入 Spinneret、调度器如何使用它们，以及在控制台和 API 上可以对它们执行的每一种操作。**

[English](../en/06-identities.md)

---

## 目录

- [什么是身份](#什么是身份)
- [身份类型](#身份类型)
- [交付：从载荷到凭据](#交付从载荷到凭据)
- [一个完整示例](#一个完整示例)
- [创建与修改身份类型](#创建与修改身份类型)
- [导入身份](#导入身份)
- [身份列表](#身份列表)
- [身份详情页](#身份详情页)
- [状态机](#状态机)
- [健康分](#健康分)
- [人工操作](#人工操作)
- [撤销自动处置](#撤销自动处置)
- [账号](#账号)
- [冷却热力图](#冷却热力图)
- [池子的日常运营](#池子的日常运营)
- [权限](#权限)
- [限制](#限制)
- [下一步](#下一步)

---

## 什么是身份

身份就是**节点在发请求时借用的一份可复用凭证**：一组 Cookie、一台注册过的设备、一个 API Key、一个签名
Token——目标站点用来判断「谁在调用」的任何东西。Spinneret 负责存储它、加密它、决定谁能在什么时候用它，
并在 `Acquire` 时把渲染好的**凭据**交给节点。

身份的特征：

- **有类型。** 它的内容由**身份类型**声明，交付给节点的方式也由类型决定。
- **落盘加密。** 载荷使用信封加密封装（见[密钥保管库](./10-secrets.md)）；调用方没有 `identity:reveal`
  权限时，API 返回的是脱敏后的载荷。
- **有状态。** 它有生命周期状态、每个端点组上的健康分和一个全局健康分，以及多个层级的冷却。调度器读取的
  正是这些状态。
- **归属一个站点和一个客户端。** 两者都继承自身份类型。站点 `example-site`、客户端 `web` 的身份，永远不会
  被租借给别的站点或别的客户端。
- **会去重。** 同一份凭证导入两次只会得到一个身份，去重依据是类型的 `unique_by` 路径。

身份**不是**：

- 登录流程——Spinneret 从不登录、不过验证码、不做签名。它管理请求周围的状态，请求仍由节点自己发出；
- 代理——代理是独立的池子，有自己的状态（见[代理池](./07-proxies.md)），只有轮换策略要求时才会与身份绑定；
- 节点——节点是持有 API 令牌的进程，身份是节点借用的东西。

ID 前缀：身份 `idt_…`、身份类型 `ity_…`、账号 `acc_…`、状态事件 `evt_…`。

![身份](../images/identities.png)

---

## 身份类型

身份类型用 YAML 声明：

- 载荷包含哪些**字段**，各是什么类型；
- 哪些字段是**敏感字段**（控制台脱敏，API 也脱敏）；
- 用于去重的 **unique_by** 路径；
- 新导入身份的**激活方式**；
- 把载荷变成节点所收凭据的 **deliver** 映射。

```yaml
# Web 爬虫：Cookie 身份
name: web_cookie
site: example-site
client: web
description: 已登录的 Web Cookie
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
  signature:  { type: string, sensitive: true }
unique_by: [cookies.sessionid]
activation: probe
deliver:
  cookies: "{{ cookies }}"
  cookie_header: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
  values:
    signature: "{{ signature }}"
```

YAML 采用严格解码：出现未知键是错误而不是警告，并且整个文档必须是单个映射。

### 顶层键

| 键 | 必填 | 含义 |
| --- | --- | --- |
| `name` | 是 | 类型名，在站点内唯一，须匹配 `^[a-z0-9][a-z0-9_.-]{0,63}$`。 |
| `site` | 是 | 站点名。API 请求中的 `site` 始终优先，并会被写回存储的 YAML，所以在控制台里可以省略。 |
| `client` | 是 | 站点的客户端，`^[a-z0-9_-]{1,32}$`，必须是该站点已声明的客户端。 |
| `description` | 否 | 自由文本，最多 2048 字节。 |
| `fields` | 是 | 字段名 → 字段定义的映射，至少 1 个，最多 128 个。 |
| `unique_by` | 否 | 用于标识身份的字段路径。默认取所有必填字段并排序，最多 16 条。 |
| `activation` | 否 | `probe`（默认）或 `immediate`。 |
| `deliver` | 否 | 段名 → 模板的映射。省略则节点收到的是空凭据。 |

字段名须匹配 `^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`，且不能是导入格式的保留键 `payload`、`_account`、
`_region`、`_tags`、`_labels`。

### 字段类型

| `type` | 导入时接受 | 存储为 | 说明 |
| --- | --- | --- | --- |
| `string` | JSON 字符串 | 字符串 | 必须是合法 UTF-8。 |
| `number` | 数字或数字字符串（`"123"`、`"1.5e3"`） | float64 | 必须是有限数。 |
| `bool` | 布尔值，或 `strconv.ParseBool` 接受的字符串（`true`、`false`、`1`、`0`、`T`、`f` …） | bool | |
| `cookie_map` | 字符串对象、`Cookie` 请求头字符串，或 `{name, value}` 对象数组 | 名称 → 值的映射 | 最多 1024 个 Cookie，名称 ≤ 1024 字节，值 ≤ 16 KiB。 |
| `json` | 任意 JSON 值 | 原值，数字为 float64 | 可用点号子键寻址，包含数组下标。 |
| `secret_ref` | 命名空间内相对密钥路径，匹配 `^[a-z0-9][a-z0-9_./-]{0,255}$` | 字符串 | 载荷里存的是路径；密钥明文在交付时从保管库解析，不会写入载荷。 |

每个字段定义只接受 `type`、`required`、`sensitive`、`description`，其他键一律拒绝。

`cookie_map` 同时接受三种写法，因此浏览器导出的数据可以直接用：

```json
{"sessionid": "a1b2c3", "csrf_token": "xyz"}
"sessionid=a1b2c3; csrf_token=xyz"
[{"name": "sessionid", "value": "a1b2c3", "domain": ".example", "path": "/"}]
```

开头的 `Cookie:` 前缀会被去掉，键值对两端空白被裁剪，值内部的 `=` 保留，空的键值对被跳过，同名 Cookie
以最后一次出现为准。Cookie 名不能包含控制字符、空白、`=`、`;`、`,` 或 `"`；Cookie 值不能包含控制字符
（制表符除外）或 `;`。

### 敏感字段

`sensitive: true` 表示该值在任何展示场景都会脱敏。脱敏保留最后 4 个字符：`••••b2c3`；长度不超过 4 的值
整体脱敏为 `••••`。敏感的 `cookie_map` 逐个 Cookie 脱敏；敏感的 `json` 字段整体变成 `••••`。类型中未声明
的字段——只可能出现在类型改动之前存下的载荷里——整体脱敏，因为无法判断它是否敏感。

标记敏感只影响展示，不影响存储：整个载荷始终是加密的。

### unique_by 与去重

`unique_by` 是一组字段路径。路径是字段名，后面可以跟点号子键（仅 `cookie_map` 和 `json` 字段支持）：

```yaml
unique_by: [cookies.sessionid]      # cookie_map 中的某一个 Cookie
unique_by: [device_id]              # 整个 string 字段
unique_by: [extra.device.id]        # json 字段的嵌套键
```

对 `cookie_map`，子键就是 Cookie 名（名称本身可以包含点号）。对 `json` 字段，每一段点号分隔的子键是对象
键或非负数组下标。

Spinneret 把这些值组成规范化 JSON 数组，再用服务端的 pepper（保管库中一枚 32 字节系统密钥，用 KEK 加密）
计算 HMAC-SHA256 后存储，去重键明文从不落盘。两行数据的键相同就是同一个身份，导入时执行更新而非插入。
类型转换在计算哈希之前完成，所以对 `number` 字段来说 `"123"` 和 `123` 得到同一个键。

如果某条路径的值缺失、为 null 或为空（空字符串、空映射、空数组），该行会被拒绝：
`unique_by path "…" is missing from the payload`。

省略 `unique_by` 时默认取所有必填字段（排序后）。如果既没有必填字段也没有显式声明，类型会被拒绝。

### 激活方式

| `activation` | 新身份的初始状态 | 何时变为 `active` |
| --- | --- | --- |
| `probe`（默认） | `pending` | 节点在该身份的某次租约上上报 `success` |
| `immediate` | `active` | 导入时立即生效 |

`probe` 是更稳妥的默认值：刚导入的凭证以较低权重被租借（轮换策略的 `probe.weight_factor`，默认 `0.1`；
同时最多 `probe.max_leases` 个探测租约，默认 `2`），只有真正可用的凭证才会以正常权重进入池子。若凭证已在
别处验证过，或是合成身份，可以用 `immediate`。

---

## 交付：从载荷到凭据

凭据固定由六个段组成。`deliver` 只填你需要的段，其余留空。

| 段 | 在 `deliver` 中的类型 | 节点收到什么 | 控制台里的说明 |
| --- | --- | --- | --- |
| `cookies` | 单个 `{{ cookie_map 字段 }}` 占位符，或字符串模板映射 | `cookies`：名称 → 值 | 传给 HTTP 客户端的 Cookie |
| `cookie_header` | 字符串模板 | `cookie_header`：`"k1=v1; k2=v2"`，按名称排序 | Cookie 请求头 |
| `headers` | 字符串模板映射 | `headers`：名称 → 值 | 合并到请求头 |
| `query` | 字符串模板映射 | `query`：名称 → 值 | 合并到查询参数 |
| `json` | 任意 JSON 结构，其中的字符串是模板 | `json`：一个 JSON 值 | 合并到请求体 |
| `values` | 字符串模板映射 | `values`：名称 → 带类型的值 | 自定义取值，如签名所需的 Token |

其他段名一律拒绝。单个交付映射最多 256 项，单条模板字符串最多 8 KiB。

### 模板

模板只支持一种写法：`{{ path }}`，其中 `path` 是字段名，后面可选点号子键；花括号内允许空格和制表符。
没有管道、没有函数调用、没有表达式、没有条件分支——模板中出现 `|`、`(`、`)`、表达式内部的空格，或
`"'`` , + * / \ $ ! = < > &` 中任一字符，保存时就会被拒绝。这是刻意为之：既排除了模板注入，也让渲染
足够便宜，可以留在 `Acquire` 热路径上。

由此引出的几条规则：

- **整条模板正好是一个占位符时，值保留原类型。** `values: {retries: "{{ n }}"}` 配 `number` 字段，交付的
  是数字 `12` 而不是字符串 `"12"`。只要模板里还有别的文本，结果就是字符串。
- **整个 `cookie_map` 字符串化后是一个 `Cookie` 头的值**（`"a=1; b=2"`）。所以
  `cookie_header: "{{ cookies }}"` 可行；也正因如此，在 `cookies` 映射中把整个 Cookie 映射当作某一个
  Cookie 的值是被拒绝的——那里要写 `{{ cookies.名称 }}`。
- **缺失的可选字段渲染为空。** 渲染结果为空的请求头、查询参数、Cookie 或 value 条目会被省略，而不是以空值
  交付。`json` 段中缺失的占位符渲染为 `null`。
- **请求头与 Cookie 的语法会被校验**：保存时校验字面部分，渲染时校验最终结果。渲染结果中出现请求头不允许的
  字符会导致渲染失败。
- **`secret_ref` 字段在渲染时解析。** 交付的是密钥明文而不是路径。同一路径在一次渲染中最多解析一次；解析过
  密钥的凭据最多缓存 60 秒。保管库自身的解析缓存还在这一层后面，所以密钥轮换之后，节点最多还可能拿到约
  90 秒的旧值——完整的轮换窗口见[密钥保管库](./10-secrets.md)。

### 载荷校验

导入、`UpdateIdentityPayload` 和交付预览，都会走同样的两步：

1. **规范化。** 拒绝未声明字段；JSON `null` 视为未提供；解析 Cookie 映射；转换数字和布尔；深拷贝 `json`
   值；校验字符串是合法 UTF-8；必填字段必须存在。
2. **JSON Schema 校验。** 类型根据字段生成一份 JSON Schema（draft 2020-12）——`additionalProperties: false`、
   列出必填字段、敏感字段标注 `writeOnly: true`——再用它校验规范化后的载荷。该 Schema 通过 API 的
   `json_schema` 字段返回，外部导入程序可以在发送之前先自校验。

所有问题会合并成一个 `invalid_argument` 错误返回，每条都带字段名前缀，并且永远不会带上载荷内容。

---

## 一个完整示例

沿用上面的 `web_cookie` 类型。导入这一行：

```json
{"cookies": "sessionid=a1b2c3; csrf_token=xyz", "user_agent": "example-crawler/1.0", "signature": "s3cr3t-token"}
```

规范化把 Cookie 头字符串变成映射：

```json
{
  "cookies": {"csrf_token": "xyz", "sessionid": "a1b2c3"},
  "signature": "s3cr3t-token",
  "user_agent": "example-crawler/1.0"
}
```

去重键是 `["a1b2c3"]`（来自 `cookies.sessionid` 路径），它的 HMAC 就是身份的去重依据。

租借到该身份的节点收到：

```json
{
  "cookies": {"csrf_token": "xyz", "sessionid": "a1b2c3"},
  "cookie_header": "csrf_token=xyz; sessionid=a1b2c3",
  "headers": {"User-Agent": "example-crawler/1.0"},
  "query": {},
  "json": null,
  "values": {"signature": "s3cr3t-token"}
}
```

同一个身份，在没有 `identity:reveal` 权限的操作者的控制台里：

```json
{
  "cookies": {"csrf_token": "••••", "sessionid": "••••b2c3"},
  "signature": "••••oken",
  "user_agent": "example-crawler/1.0"
}
```

`user_agent` 不是敏感字段，原样显示；`csrf_token` 的值只有 3 个字符，整体脱敏。

### 交付预览

`PreviewDelivery` 用已有类型（`type_id`）或草稿定义（`spec_yaml`）渲染一份示例载荷，返回凭据、规范化后的
载荷以及错误列表，不保存任何数据。在控制台里它是身份类型编辑器的**交付预览**标签页，以及列表行操作
**交付预览**。

预览**从不**解析 `secret_ref`：它们渲染成字面占位符 `<secret:PATH>`。预览无法被用来读取密钥。

---

## 创建与修改身份类型

控制台：**身份类型** → **新建类型**。选择站点，编写 YAML（**插入示例**菜单提供 Cookie 类型和 App 设备类型
两个模板），在**交付预览**标签页确认效果，然后**创建**。打开已有类型是同一个编辑器，**保存新版本**会写入
下一个版本。

通过 API：

```bash
curl -sS https://spinneret.example.com/spinneret.v1.IdentityAdminService/CreateIdentityType \
  -H 'Authorization: Bearer spn_…' \
  -H 'Content-Type: application/json' \
  -d '{
        "namespace": "default",
        "site": "example-site",
        "spec_yaml": "name: web_cookie\nclient: web\nfields:\n  cookies: { type: cookie_map, required: true, sensitive: true }\nactivation: probe\ndeliver:\n  cookie_header: \"{{ cookies }}\"\n"
      }'
```

哪些可以改，哪些不能改：

| | 创建 | 更新 |
| --- | --- | --- |
| `site` | 取自请求（覆盖 YAML） | 不可改；YAML 中写另一个站点会报错 |
| `name` | 取自 YAML | 不可改 |
| `client` | 取自 YAML，必须是站点的客户端 | 不可改 |
| 字段、`unique_by`、`activation`、`deliver`、`description` | 取自 YAML | 整体替换 |
| 版本 | `1` | 每次更新递增 |

**修改 `unique_by` 会触发全量重算。** 更新会在同一个事务里重新计算该类型下每个身份的去重键，也就是解密
每一份当前载荷。如果某个身份在新路径上没有值，或者两个身份会撞键，更新以 `failed_precondition` 失败。
在大池子上这是一次缓慢且持锁的操作——请按数据迁移来安排。

删除身份类型要求它下面**一个身份都没有**（含已归档的），否则以 `failed_precondition` 失败。

并发通过类型上的咨询锁加版本校验来处理：导入或载荷更新若与定义变更相撞，会以 `conflict` 失败并要求重试，
而不会用过期的定义去规范化载荷。

创建、更新、删除类型需要 `site:write`；读取和预览需要 `identity:read` 或 `site:read`。

---

## 导入身份

导入是身份进入 Spinneret 的常规方式：既可以在控制台完成（**身份** → **导入**），也可以由凭证刷新服务用带
`identity:write[:<站点>]` 权限范围的 API 令牌调用 `ImportIdentities`。

一次导入只针对**一个站点和一个身份类型**。

### JSON Lines

每个非空行一个 JSON 对象，接受两种形态。

**扁平形态**——对象本身就是载荷，去掉保留键：

```json
{"cookies": "sessionid=a1b2c3; csrf=x", "user_agent": "example-crawler/1.0", "_account": "user-1", "_region": "US", "_tags": "pool-a,warm", "_labels": {"batch": "2026-03-01"}}
```

**信封形态**——对象带 `payload` 键：

```json
{"payload": {"cookies": {"sessionid": "a1b2c3"}}, "account": "user-1", "region": "US", "tags": ["pool-a", "warm"], "labels": {"batch": "2026-03-01"}}
```

信封只接受 `payload`、`account`、`region`、`tags`、`labels`，出现其他键该行被拒绝。`tags` 可以是字符串数组，
也可以是逗号分隔的字符串。空行会被跳过，开头的 UTF-8 BOM 会被忽略。

### CSV

必须有一行字段名表头。保留列承载元数据：

| 列 | 含义 |
| --- | --- |
| `_account` | 账号外部引用 |
| `_region` | 地区 |
| `_tags` | 标签，以 `;` 分隔 |
| `_labels` | 标注，写成 `k=v;k2=v2` |

表头中其他以 `_` 开头的列一律拒绝；重复列名和空列名也会被拒绝。空单元格视为未提供；以 `{` 或 `[` 开头且能
解析成功的单元格按 JSON 解码，其余保持字符串，由规范化阶段再做类型转换。

```csv
cookies,user_agent,_account,_region,_tags
"sessionid=a1b2c3; csrf=x",example-crawler/1.0,user-1,US,pool-a;warm
"{""sessionid"":""d4e5f6""}",example-crawler/1.0,user-2,DE,pool-b
```

### 模式、试运行与每一行的结果

| 字段 | 取值 | 作用 |
| --- | --- | --- |
| `mode` | `upsert`（默认）、`create_only` | `create_only` 不动已存在的身份 |
| `dry_run` | `true` / `false` | 只校验和计数，不写入任何数据 |

按去重哈希逐行处理：

| 情况 | `upsert` | `create_only` | 计数 |
| --- | --- | --- | --- |
| 去重键此前未出现 | 创建身份，载荷存为第 1 版，初始状态由 `activation` 决定 | 同左 | `created` |
| 已存在且载荷不同 | 写入新的载荷版本，重新验证生命周期，重置健康分与连续失败次数 | 跳过 | `updated` / `unchanged` |
| 已存在且载荷相同 | 应用该行设置的属性 | 跳过 | `unchanged` |
| 同一文件中去重键重复 | 后一行被拒绝：`duplicate unique key (same identity as line N)` | 同左 | `failed` |
| 行无法解析、载荷非法，或密钥引用被拒 | 连同行号一起拒绝 | 同左 | `failed` |

属性更新是局部的：`_account`/`_region` 为空、`_tags`/`_labels` 未提供时，保留原有值。`_account` 非空则把身份
挂到站点下该引用对应的账号上，账号不存在时自动创建。

载荷更新引起的状态变化与 `UpdateIdentityPayload` 相同（见[状态机](#状态机)），记录为 `payload_update`
状态事件。

**密钥引用。** 如果载荷通过 `secret_ref` 字段引用了某个密钥，写入它就等同于读取它——该身份的每一次租约都会
解析这个密钥。因此导入方必须对每一个**新引入**的路径有读取权限（用户需要命名空间上的 `secret:reveal`，
令牌需要匹配 `<命名空间>/<路径>` 的 `secret:read` 权限范围），**且**密钥必须已存在。已存载荷的同一字段上
原本就有的引用不会被重新鉴权。无论身份是新建还是已存在，未通过这项检查的行都只拒绝该行——带上行号计入
`failed`，同一次导入的其余行照常写入。只改动单个身份的 `UpdateIdentityPayload` 则不同：整个调用以
`permission_denied` 或 `invalid_argument` 失败。

### 结果与限制

```json
{"created": 812, "updated": 44, "unchanged": 9102, "failed": [{"line": 37, "message": "cookies: required field is missing"}]}
```

最多逐条返回 1000 个失败项，其余合并为一条 `line: 0`、消息为 `N more rows failed` 的汇总项。

| 限制 | 取值 |
| --- | --- |
| 单次导入行数 | 50 000 |
| 单次导入字节数 | 32 MiB |
| 每个事务的行数 | 500 |
| 逐条返回的失败数 | 1000 + 一条汇总 |
| 每个身份保留的载荷版本数 | 5 |

### 大规模导入

导入按 500 行一批写入，每批一个事务，超时 2 分钟。也就是说，中途出现基础设施故障时，先前提交的批次会保留
下来——它们同样会同步到热状态并写入审计。重跑同一个文件是安全的：没有变化的行计为 `unchanged`。

池子超过 50 000 时请拆分文件循环导入。刷新服务可以参考这个流程：

1. 先用一小段样本 `dry_run: true`，确认格式能被解析；
2. 按每个文件 10 000～50 000 行导入；
3. 从每次响应里读取 `created`/`updated`/`unchanged`/`failed`，对 `failed` 告警。

每次导入写入一条 `identity.import` 审计记录，包含站点、类型、格式、模式和各项计数。

做压测时，`spnr seed --site loadtest --identities 100000` 可以直接生成一个 Cookie 类型及其合成身份，
见[命令行工具](./15-cli.md)。

---

## 身份列表

控制台的**身份**页（`/identities`）列出当前命名空间下、你有读权限的站点上的全部身份。没有弹窗打开时，
列表每 5 秒自动刷新一次。

### 筛选条件

| 筛选项 | 字段 | 说明 |
| --- | --- | --- |
| 站点 | `filter.site` | 留空匹配所有可访问站点 |
| 身份类型 | `filter.type` | 类型名 |
| 状态 | `filter.states` | 多选；留空匹配除 `retired` 外的全部状态 |
| 搜索 | `filter.search` | 身份 ID **前缀**，或某个标注的精确值 |
| 标签 | `filter.tags` | 身份必须**同时**带有全部标签 |
| 账号 | `filter.account_ref` | 外部引用精确匹配 |
| 地区 | `filter.region` | 精确匹配 |
| 最低 / 最高评分 | `filter.min_score`、`filter.max_score` | 0～100，最低不得高于最高 |
| 包含已归档 | `filter.include_retired` | 仅在未选择任何状态时生效 |

标签、账号、地区、评分区间和「包含已归档」收在**更多筛选**里，按钮上的角标显示其中有几项处于启用状态。

### 列

| 列 | 内容 |
| --- | --- |
| ID | `idt_…`，点击进入详情页 |
| 站点 / 客户端 / 类型 | 来自身份类型 |
| 状态 | 状态徽标，附带原因以及封禁或隔离的截止时间 |
| 账号 | 外部引用；身份没有账号时为空 |
| 地区、标签 | 自由属性 |
| 全局评分 | 评分条加样本数 |
| 活跃租约 | 当前持有的租约数（见下方注意） |
| 载荷 | `v<n>`，当前载荷版本 |
| 最近使用 | 最后一次租借的时间 |
| 状态变更 | 最后一次状态变化的时间 |

排序键可选**创建时间**、**更新时间**、**状态变更时间**、**最近使用时间**、**全局评分**，支持升序降序；
分页基于 keyset，默认每页 50 行，最多 500 行。

**注意。** 列表里的评分来自 Spinneret 每 60 秒写入 PostgreSQL 的热状态快照，因此最多可能滞后一分钟；详情页
读的是 Redis 实时值。从未被观测过的身份显示基线分（70）。实时租约数同样只有 `GetIdentity` 会从 Redis 读取：
`ListIdentities` 返回的 `active_leases` 恒为 0，因此这一列请以详情页为准，不要看列表。

勾选行后可以使用操作菜单，单次调用最多 1000 个身份。**按筛选批量操作**则把同一操作应用到当前筛选匹配的
全部身份上（见[人工操作](#人工操作)）。

---

## 身份详情页

`/identities/<id>`（在**身份**页点击 ID 进入）。

![身份详情](../images/identity-detail.png)

**属性。** 站点、客户端、类型、账号、地区、标签、标注，以及创建 / 激活 / 更新时间。**编辑**可以修改地区、
标签、标注和账号引用——只提交你改过的部分，不影响载荷。填入不存在的账号引用会自动创建账号；清空则解除
关联。

**实时状态。** 直接从 Redis 读到的站点级热状态：调度器眼中的状态（与存储状态不一致时会标注）、站点冷却、
站点复用间隔、独占租约窗口、账号冷却和绑定的代理。如果该身份根本没有加载到热状态，卡片会明确提示——此时
在下一次重建或状态变更之前它不会被调度。

**端点组。** 该身份所属客户端的每一个端点组各一行，而不只是有状态的那些——没有热状态条目的端点组显示基线分
和 0 样本数。列包括：健康分、样本数、连续失败次数、冷却、复用间隔、最近使用、最早可租借时间，以及是否在
就绪队列中。状态列按优先级依次判定：**冷却中** → **复用间隔中** → **等待中** → **就绪**
（在队列中）或**可调度**（不在队列中）。

**载荷。** 当前载荷（脱敏）。**查看明文**需要站点上的 `identity:reveal` 权限，会以 `identity.reveal` 记入
审计日志，明文 60 秒后自动重新脱敏。**新版本**打开一个按类型 Schema 校验的 JSON 编辑器；脱敏值（`••••`）
无法保存，所以要先查看明文或手动替换它们。保存新版本会递增载荷版本号、重置健康分与连续失败次数，并应用
载荷更新的状态转换。Spinneret 为每个身份保留最近 5 个载荷版本，写入新版本时删除更旧的。

**状态时间线。** 该身份的状态事件，最新在前，包含动作、变更前后的状态、作用范围与端点组、临时处置的截止
时间、产生它的策略与规则、来源上报与租约、操作者和原因。影子模式策略产生的事件标记为**影子模式**：已记录
但未真正执行。`GetIdentity` 内联返回最近 20 条事件，时间线通过 `ListStateEvents` 继续向前翻页。

**近期风险事件。** 该身份最近 24 小时内的非成功上报——时间、端点组、判定结果、归因、HTTP 状态码、规则、
代理、节点和耗时。详见[可观测性与告警](./12-observability.md)。

本页右上角的**操作**菜单提供与列表相同的操作，只作用于当前这一个身份。

---

## 状态机

| 状态 | 含义 | 参与调度？ |
| --- | --- | --- |
| `pending` | 刚导入或刚重新验证，尚未被证明可用 | 参与，按探测权重 |
| `active` | 已被证明可用 | 参与 |
| `expired` | 凭证已无法通过认证，需要刷新服务更新 | 否 |
| `banned` | 在 `ban_until` 之前（或永久）移出调度 | 否 |
| `quarantined` | 在 `quarantine_until` 之前隔离待查 | 否 |
| `disabled` | 被操作者或其账号停用 | 否 |
| `retired` | 已归档；除非显式勾选，否则在列表中隐藏 | 否 |

只有 `pending` 和 `active` 参与调度。

### 人工转换

每个操作只接受特定的源状态；身份处于其他状态时以 `invalid_transition` 失败，消息为
`cannot <operation> an identity in state <state>`。

| 操作 | 源状态 | 目标状态 |
| --- | --- | --- |
| `ban` | `active`、`pending`、`quarantined`、`expired`、`disabled` | `banned` |
| `unban` | `banned` | `pending` |
| `quarantine` | `active`、`pending` | `quarantined` |
| `unquarantine` | `quarantined` | `pending` |
| `expire` | `active`、`pending`、`quarantined` | `expired` |
| `disable` | `pending`、`active`、`expired`、`banned`、`quarantined` | `disabled` |
| `enable` | `disabled` | `active` |
| `archive` | 除 `retired` 外的所有状态 | `retired` |
| `restore` | `retired` | `pending` |
| `activate` | `pending` | `active` |

`cooldown` 和 `reset_stats` 不是生命周期转换，它们只改热状态。冷却仅对 `retired` 身份被拒绝。

### 自动转换

| 触发条件 | 效果 | 记录为 |
| --- | --- | --- |
| `pending` 身份收到一次 `success` 上报 | → `active` | 规则 `lifecycle.activate` |
| 处置规则命中且 `action: expire` | → `expired` | 该规则名 |
| 处置规则命中且 `action: ban` | → `banned`，时长取自规则，可能被升级阶梯拉长 | 该规则名 |
| 处置规则命中且 `action: quarantine` | → `quarantined` | 该规则名 |
| 全局健康分低于 `health.quarantine_score`（默认 20）且样本数不少于 `health.quarantine_min_samples`（默认 10） | → `quarantined`，时长 `health.quarantine_duration`（默认 24h） | 规则 `health.quarantine` |
| 端点健康分低于 `health.endpoint_low_score`（默认 15）且样本数不少于 `health.endpoint_low_min_samples`（默认 10） | 端点冷却 `health.endpoint_low_cooldown`（默认 6h） | 规则 `health.endpoint_low` |
| 封禁到达 `ban_until` | → 动作策略的 `ban_expiry_state`：`pending`（默认）或 `active` | 原因 `ban_expired` |
| 隔离到达 `quarantine_until` | → `pending` | 原因 `quarantine_expired` |
| 账号被封禁或停用 | 成员身份随之变化 | 原因 `account_ban` / `account_disabled` |
| 载荷被替换 | 见下文 | 动作 `payload_update` |

封禁与隔离的到期由一个每 10 秒运行一次的 leader 任务处理。Redis 无响应时它拒绝释放任何对象，因此不会出现
「PostgreSQL 已提交释放、但调度器看不到」的状态。

有哪些规则、它们匹配什么、封禁如何升级，属于[策略](./08-policies.md)的内容。处于**影子模式**的策略只记录
它「本会」产生的状态事件，不真正改动身份。

### 载荷更新

替换载荷——无论来自导入还是 `UpdateIdentityPayload`——都会让身份重新接受验证：

| 更新前状态 | `activation: probe` 时 | `activation: immediate` 时 |
| --- | --- | --- |
| `active`、`pending`、`quarantined`、`expired` | `pending` | `active` |
| `banned`、`disabled`、`retired` | 保持不变 | 保持不变 |

身份以这种方式离开 `quarantined` 时隔离会被清除。热状态中的健康分与连续失败次数被重置。载荷与已存值完全
相同时既不产生新版本，也不改变状态。

这正是凭证刷新闭环：规则把失效的 Cookie 标记为 `expired`，外部服务通过 `ListIdentities` 看到
`state: expired`，重新登录后导入新的 Cookie，身份回到 `pending`，第一次探测成功后升为 `active`。

---

## 健康分

每个身份都带有 0～100 的健康分：每个端点组一个，**另有**一个全局分。它是带时间衰减、向基线回归的指数加权
移动平均。

每收到一次影响该身份的上报：

```text
decayed = baseline + (score - baseline) * exp(-Δt / tau)
score   = alpha * observation + (1 - alpha) * decayed
samples = samples + 1
```

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `health.baseline` | 70 | 身份闲置时回归的分数，也是无历史身份的分数 |
| `health.alpha` | 0.1 | 单次观测的权重 |
| `health.tau` | 6h | 衰减时间常数；经过 `tau` 后与基线的距离缩小到 1/e |
| `health.observations` | 见下表 | 单个判定结果贡献的观测值 |

默认观测值，可在动作策略中按判定结果覆盖：

| 判定结果 | 观测值 |
| --- | --- |
| `success` | 100 |
| `empty` | 60 |
| `network_error` | 50 |
| `rate_limited` | 30 |
| `forbidden` | 10 |
| `captcha` | 0 |

`success` 始终影响身份。其他判定结果只有在归因包含身份时才影响它——归因到代理的 `network_error` 不会动身份
的分数。表中没有的判定结果（`auth_invalid`、`banned`、`proxy_error`、`target_error`、`client_error`、
`unknown`）完全不影响健康分，它们由处置规则负责。

除分数外，每个层级还维护一个**连续失败计数**：每次失败加一（`empty`、`rate_limited`、`captcha`、
`auth_invalid`、`forbidden`、`banned`，以及归因到身份的 `network_error`），每次成功**减半**；如果下一次失败
距上一次失败超过 `failure_reset_after`，计数从 1 重新开始。默认值是 1h，实际生效的是评估该上报的动作策略中
所有冷却规则里最长的那个 `failure_reset_after`。后台不会主动清零，所以身份闲置期间，**连续失败**列仍然保留
原来的值。这个计数驱动冷却的指数退避。

如何读分数：

- **≥ 60** —— 健康（控制台显示为绿色）。
- **30～59** —— 劣化。
- **< 30** —— 不健康；低于 `quarantine_score`（20）且样本足够时会被自动隔离。
- **正好 70 且 0 样本** —— 完全没有历史：要么从未被使用过，要么没有加载到热状态。

分数只有在有样本支撑时才有意义：控制台始终在评分条旁显示样本数，自动处置也正是为此才设置了最小样本阈值。

`reset_stats` 把分数恢复到基线，并清空样本数、连续失败次数和计数器。它具体影响哪些层级取决于 `scope`，见
[作用范围](#作用范围)。

---

## 人工操作

可以从列表（对勾选项）、详情页，或通过 `OperateIdentities`（显式 ID）和 `BulkOperateIdentities`
（筛选匹配的全部身份）执行。所有操作都需要对每个身份所在站点拥有 `identity:operate`。

| 操作 | 时长 | 作用范围 | 效果 |
| --- | --- | --- | --- |
| `cooldown` | 必填，> 0，不接受 `permanent` | 有 | 暂停租借一段时间 |
| `ban` | 必填，> 0 或 `permanent` | 无 | 在封禁结束前把身份移出调度 |
| `unban` | 无 | 无 | 解除封禁，身份回到 `pending` |
| `quarantine` | 可选（留空用策略默认值），不接受 `permanent` | 无 | 将身份隔离待查 |
| `unquarantine` | 无 | 无 | 提前结束隔离 |
| `expire` | 无 | 无 | 标记凭证过期，交给刷新服务更新 |
| `disable` | 无 | 无 | 停止调度，直到重新启用 |
| `enable` | 无 | 无 | 使已停用的身份重新参与调度 |
| `archive` | 无 | 无 | 归档身份 |
| `restore` | 无 | 无 | 把已归档的身份恢复为 `pending` |
| `activate` | 无 | 无 | 无需等待探测，直接提升 `pending` 身份 |
| `reset_stats` | 无 | 有 | 重置健康分、连续失败次数和计数器 |

时长写法为 `30m`、`2h`、`7d`、`1d12h`——单位 `ms`、`s`、`m`、`h`、`d`——或者 `permanent`，后者只有 `ban`
接受。控制台提供 `30s`、`5m`、`10m`、`30m`、`1h`、`6h`、`1d`、`7d`、`30d` 预设，冷却默认 `30m`，封禁默认
`7d`。

### 作用范围

`cooldown` 和 `reset_stats` 需要选择层级：

| `scope` | 控制台标签 | `cooldown` 作用于 | `reset_stats` 作用于 |
| --- | --- | --- | --- |
| `identity_endpoint` | 单个端点组 | 一个端点组；必须提供 `endpoint_group_id`，且它要属于该身份的站点和客户端 | 同一个端点组 |
| `identity_site` | 整个站点 | 站点层级：该身份所属客户端的所有端点组 | 全局健康分、全局连续失败计数和判定结果计数器——**不含**各端点组上的条目 |
| `identity` | — | 等同于 `identity_site` | 等同于 `identity_site` |
| *（留空）* | 所有层级 | 等同于 `identity_site` | 全局健康分、全局连续失败计数、判定结果计数器，**以及**该身份所属客户端的每一个端点组 |

其他操作忽略作用范围：它们改变的是生命周期状态，本身就是站点级的。

### 重置开关

`unban`、`unquarantine`、`enable`、`restore`、`activate` 额外接受两个开关：

- `reset_failures` —— 同时清除连续失败次数；
- `reset_health` —— 同时把健康分重置为基线。

当身份是被「误伤」时请打开它们，否则它一复活就离再次被封只差一次坏上报。其他操作会忽略这两个开关。

### 原因

每个操作都接受自由文本 `reason`（最多 512 字节），会写入状态事件和审计日志。**一定要写**——六周之后，它
是唯一能解释「为什么当时封了三万个身份」的东西。

### 显式 ID

```bash
curl -sS https://spinneret.example.com/spinneret.v1.IdentityAdminService/OperateIdentities \
  -H 'Authorization: Bearer spn_…' -H 'Content-Type: application/json' \
  -d '{"ids": ["idt_01hx…", "idt_01hy…"],
       "operation": "cooldown", "scope": "identity_site",
       "duration": "30m", "reason": "目标站点维护窗口"}'
```

单次调用最多 1000 个 ID，需非空且不重复。响应是一个 `BulkResult`：

```json
{"result": {"matched": 2, "succeeded": 2, "failed": []}}
```

`matched` 是真正进入 Operator 的 ID 数：在此之前就被判为 `not_found` 或 `endpoint_group_unknown` 的 ID 会出现
在 `failed` 里，但不计入 `matched`。因此 `matched` 等于 `succeeded` 加上 Operator 自己上报的失败数。

逐个身份可能返回的失败原因：

| 原因 | 含义 |
| --- | --- |
| `not_found` | ID 不存在，或身份位于你无任何关联的命名空间 |
| `endpoint_group_unknown` | 该端点组不属于这个身份的站点 |
| `invalid_transition` | 身份当前状态不允许该操作 |
| `invalid_argument` | 端点组与身份的站点和客户端不匹配 |
| `not_in_hot_state` | 冷却或重置需要身份已加载到 Redis，而它没有 |
| `state_changed` | 身份被并发修改，请重试 |
| `internal` | 操作失败，可以重试 |

如果其中包含你无权操作的站点上的身份，**整个请求**以 `permission_denied` 失败；而不存在的 ID 只影响它自己
那一行。

### 按筛选批量操作

`BulkOperateIdentities` 接受与列表相同的 `IdentityFilter`，外加 `dry_run` 和 `limit`（默认值与上限均为
100 000）。在控制台里它是**按筛选批量操作**，必须先**预览数量**才会出现确认框。

```bash
curl -sS https://spinneret.example.com/spinneret.v1.IdentityAdminService/BulkOperateIdentities \
  -H 'Authorization: Bearer spn_…' -H 'Content-Type: application/json' \
  -d '{"namespace": "default",
       "filter": {"site": "example-site", "type": "web_cookie", "states": ["banned"], "tags": ["pool-a"]},
       "operation": "unban", "reset_failures": true, "reset_health": true,
       "reason": "2026-03-01 规则误判", "dry_run": true}'
```

试运行只返回 `matched`，不做任何改动。正式执行会分页解析匹配的 ID，再按每批 1000 个执行，所以如果执行过程中
有身份状态变化，`matched` 和 `succeeded` 可能不一致。每次批量执行都会写入一条 `identity.bulk_operate`
审计记录。

**警告。** 空筛选匹配命名空间内所有未归档的身份。控制台会在确认前明确提示；通过 API 调用时没有任何东西拦你。

---

## 撤销自动处置

`RevertActions` 是规则误判之后的撤销按钮。它在一个时间范围内查找系统写入的状态事件（不含影子事件，也不含
人工操作），并回滚其中效果仍然生效的那些。

| 字段 | 含义 |
| --- | --- |
| `namespace` | 必填 |
| `site` | 留空覆盖所有你有权操作的站点 |
| `policy_id` | 留空匹配任意策略 |
| `rule` | 规则名；留空匹配任意规则 |
| `actions` | `ban`、`quarantine`、`expire`、`cooldown` 的任意组合；留空表示全部四种 |
| `time_range.start` | **必填**——撤销永远不会悄悄覆盖整个历史 |
| `time_range.end` | 留空表示当前时间 |
| `reset_failures`、`reset_health` | 作用于被恢复回来的身份 |
| `dry_run` | 只列出受影响的身份，不做任何改动 |

它的行为：

- 仍处于该事件所产生状态、且此后未再变化的身份，回到 `pending`，原因记为 `reverted`；
- 此后已经变过状态的身份原样保留（连计数都不计入）；
- 撤销 `ban` 时，该范围内被自动封禁的账号会被解封；
- 仍在生效的身份冷却会被清除。

响应是一个 `BulkResult` 加上最多 1000 个受影响身份 ID；单次调用最多考察 50 000 条候选事件。每个被撤销的
身份会得到一条 `revert` 状态事件，其中记录了被撤销的那条事件。控制台对话框（身份列表上的**撤销处置**）
强制先试运行，并提供快捷时间范围。

撤销不会修改策略。请同时修好规则，否则下一次上报又会把它执行一遍——见[策略](./08-policies.md)。

---

## 账号

**账号**把同一站点下属于同一个上游用户账号的身份归到一起，由站点加**外部引用**（用户名、UID，或任何在站点
内稳定且唯一的标识）唯一确定。

账号存在的意义是：一个决策可以覆盖多份凭证。上游某个账号被风控时，由它派生出来的每一组 Cookie 在同一瞬间
就都作废了——而且目标站点的限额往往是按账号而不是按会话计算的。

| 账号状态 | 含义 |
| --- | --- |
| `active` | 正常 |
| `banned` | 封禁至 `ban_until`，或永久封禁 |
| `disabled` | 被操作者停用 |

账号还带有**冷却**（`cooldown_until`）、地区、标签和操作备注。

### 关联身份

- 导入时用 `_account` 列或信封的 `account` 键，账号不存在则自动创建；
- 详情页**编辑**中的账号字段，规则相同，清空则解除关联；
- `UpsertAccount` 按站点加外部引用创建或更新账号本身（地区、标签、备注）。

一个身份最多属于一个账号。

### 账号操作

`OperateAccount` 需要 `identity:operate`，支持 `ban`、`unban`、`cooldown`、`disable`、`enable`。
`ban` 与 `cooldown` 需要时长，只有 `ban` 接受 `permanent`。

| 操作 | 账号 | 成员身份 |
| --- | --- | --- |
| `ban` | `active`、`disabled`、`banned` → `banned` | 处于 `active`、`pending`、`quarantined`、`expired` 的成员，*因该账号被停用*的成员，以及自身封禁更早结束的成员 → `banned`，原因 `account_ban` |
| `unban` | `banned` → `active` | 原因为 `account_ban` 的被封成员 → `pending` |
| `disable` | `active` → `disabled` | 处于 `active` 或 `pending` 的成员 → `disabled`，原因 `account_disabled` |
| `enable` | `disabled` → `active` | 原因为 `account_disabled` 的已停用成员 → `active` |
| `cooldown` | 状态不变 | 不改变状态；账号级冷却在结束前阻止其所有成员身份被租借 |

正是这个「原因标记」让操作可逆而不误伤：解封账号只会释放被账号封禁牵连的身份，不会释放操作者单独封禁的
那些。

响应包含账号本身，以及一个针对身份的 `BulkResult`，其中 `matched` 是成员身份总数，`succeeded` 是实际发生
变化的数量。`reset_failures` 和 `reset_health` 作用于重新变得可调度的成员。

账号冷却会在身份详情页显示为**账号冷却**，并计入热力图绘制的剩余冷却时间。

控制台页面是**账号**（`/accounts`）：按站点、状态或外部引用筛选，添加账号，执行操作，或点击**查看身份**
跳转到按该账号过滤的身份列表。

---

## 冷却热力图

**冷却热力图**（`/heatmap`）把**一个站点、一个客户端**下的身份画成行，把它的端点组画成列。它能飞快地回答
一个问题：*是整个端点组出了问题，还是只有少数身份出了问题？* 它属于仪表盘数据而不是身份数据，因此需要站点上的
`dashboard:read` 而不是 `identity:read`。

![冷却热力图](../images/heatmap.png)

控制项：站点、客户端、指标、状态筛选、每页行数（100、250 或 500）和图表 / 表格视图。页面每 10 秒刷新一次。

| 指标 | 单元格颜色 |
| --- | --- |
| **剩余冷却** | 该身份在该端点组上还要被阻塞多久，综合端点、站点和账号冷却以及封禁；永久封禁单独成一档 |
| **健康分** | 该身份在该端点组上衰减后的健康分 |

两个值在响应中始终都有，所以切换指标不会重新请求数据。

怎么看：

- **状态筛选默认只看 `active` + `pending`。** 放宽它才能看到被封禁、隔离、过期或停用的身份。即使选「全部」
  也不包含 `retired`。
- **整列发暗** —— 某个端点组在全池范围内冷却。这是目标站点或规则层面的问题，不是凭证的问题。检查该端点组的
  熔断器和规则（[策略](./08-policies.md)、[可观测性与告警](./12-observability.md)）。
- **若干行发暗** —— 个别身份被打废了。看看它们共用的账号和代理。
- **整张网格很淡、几乎没有单元格** —— 这些身份在这些端点组上没有热状态：从未在那里被租借过，或者热状态还没
  重建。没有热状态的单元格显示基线分，可用性按身份的生命周期状态推断。
- **点击单元格**可直接打开对应身份。**表格**视图以文本呈现同样的数据，也是无障碍视图。

行是分页的，分页器显示「第 from–to 个身份，共 total 个」。

---

## 池子的日常运营

### 容量估算

决定池子大小的不只是请求量，更是「一个身份多久能用一次」。对某一个端点组：

```text
可持续 RPS  ≈  可用身份数 / max(reuse_interval, 单次请求平均耗时)
可用身份数 = active + pending，减去此刻正在冷却、被封禁或被隔离的部分
```

`reuse_interval`、`max_concurrent_leases`（默认 1，即独占）和 `quota` 来自绑定到该端点组的轮换策略，见
[策略](./08-policies.md)。当 `max_concurrent_leases: 1`、`reuse_interval` 为 60 秒时，600 个身份最多支撑
10 RPS——而且还要求它们都没在冷却。

所以要按**最糟的那天**来估算：拿你需要的吞吐除以策略允许的单身份速率，再为「通常不可用的那部分池子」留出
余量。这部分占比多少，看热力图。

如果节点开始收到 `no_identity_available`（一个带 `retry_after_ms` 的 `resource_exhausted` 错误），说明池子
相对当前负载太小、太冷或被打废了——见[故障排查](./18-troubleshooting.md)。

### 识别一个被打废的池子

症状通常按这个顺序出现：

1. 身份列表上**全局评分**的分布整体下移，越来越多的行变黄变红；
2. 热力图在**剩余冷却**指标下大片行同时变暗；
3. 概览面板上 `banned` 和 `quarantined` 的数量上涨；
4. 节点开始收到 `no_identity_available`。

先诊断，再动手：

- 把身份列表按**状态 = 封禁、隔离**过滤，并按**状态变更时间**降序排序。如果大量身份在同一个几分钟窗口内
  改变了状态，那就是某条规则在一波异常里集中触发，而不是缓慢劣化。
- 打开其中一个，看**状态时间线**：规则名和上报会告诉你是哪条策略、基于什么判定结果做的决定。
- 看**账号**列。如果被打废的身份集中在少数几个账号上，那是上游账号被风控了，不是 Cookie 的问题。
- 看详情页上绑定的代理。如果它们共用同一个代理或同一个网段，那是代理问题，去看[代理池](./07-proxies.md)。

然后再处理：

- 规则误判 → 对该规则和时间范围执行**撤销处置**，勾上 `reset_failures` 和 `reset_health`，然后修规则；
- 上游确实封了 → 保持封禁状态，导入替换凭证；
- 账号层面的问题 → 封账号，而不是一个个封身份；
- 代理层面的问题 → 去操作代理池。

### 预热

刚导入的凭证是池子里最脆弱的东西。有两套机制保护它，都配置在轮换策略里：

- **探测。** `activation: probe` 时新身份停留在 `pending`，以正常身份 `probe.weight_factor`（默认 0.1）的
  权重被选中，同时最多 `probe.max_leases`（默认 2）个探测租约。一次成功上报即可升为 `active`。
- **预热配额。** `warmup.duration`（默认 `0s`，即关闭）和 `warmup.quota_factor`（默认 1）在身份变为活跃后的
  一段时间内缩放它的配额。设置 `warmup: {duration: 24h, quota_factor: 0.25}`，新身份第一天只有正常请求预算
  的四分之一。

新批次的实用预热流程：

1. 用 `activation: probe` 导入，并打上形如 `batch-2026-03-01` 的标签，方便之后按批次筛选和操作；
2. 让它跑起来。在身份列表按该标签过滤、按**全局评分**排序观察这个批次；
3. 一天之后，把这个批次的评分分布与池子其余部分对比。如果整批都明显更差，那是来源有问题，不是运气问题；
4. 如果某个批次确实不行，按筛选把整批 `archive` 掉，而不是删除——归档的身份保留历史，随时可以恢复。

不要为了「省时间」而把一大批身份导入后用 `activate` 直接设为活跃。那样你丢掉了探测，而目标站点看到的第一幕
就是几千份未经验证的凭证以全速涌入。

### 退役

系统里没有删除。`archive` 把身份移到 `retired`：它们从列表中消失（除非勾选**包含已归档**）、从热状态中移除、
不再被调度。`restore` 把它们恢复为 `pending`。已归档的身份仍会阻止删除身份类型，也仍然保存着加密载荷——数据
保留策略见[运维手册](./16-operations.md)。

---

## 权限

| 操作 | 权限 |
| --- | --- |
| 列出 / 获取身份、类型、账号、状态事件、热状态 | `identity:read`（类型与预览也接受 `site:read`） |
| 导入、更新载荷、更新属性、创建或更新账号 | `identity:write` |
| 所有人工操作、批量操作、撤销、账号操作 | `identity:operate` |
| 明文查看载荷 | `identity:reveal`（记入审计） |
| 打开冷却热力图 | `dashboard:read` |
| 创建、更新、删除身份类型 | `site:write` |
| 保存引用了新密钥的载荷 | `secret:reveal`（用户）或匹配的 `secret:read` 权限范围（令牌） |

以上权限都按站点校验。带 `identity:write[:<站点>]` 权限范围的 API 令牌，在该站点上同时具备
`identity:read`、`identity:write` 和 `identity:operate`——这正是凭证刷新服务所需要的。见
[租户、用户与令牌](./11-access-control.md)。

---

## 限制

| 项目 | 限制 |
| --- | --- |
| 每个身份类型的字段数 | 128 |
| `unique_by` 路径数 | 16 |
| 每个交付段的条目数 | 256 |
| 单条交付模板 | 8 KiB |
| 类型 YAML | 1 MiB |
| 预览示例载荷 | 1 MiB |
| 单个 `cookie_map` 的 Cookie 数 | 1024（名称 ≤ 1 KiB，值 ≤ 16 KiB） |
| 每个身份的标签数 | 32（每个 ≤ 64 字节，`^[a-zA-Z0-9_.:-]{1,64}$`） |
| 每个身份的标注数 | 32（键 ≤ 64 字节，值 ≤ 256 字节） |
| 地区 | 64 字节 |
| 账号外部引用 | 256 字节 |
| 账号备注 | 4096 字节 |
| 操作原因 | 512 字节 |
| 单次 `OperateIdentities` 的 ID 数 | 1000 |
| 单次 `BulkOperateIdentities` 的身份数 | 100 000 |
| 撤销返回的受影响 ID 数 | 1000 |
| 单次导入行数 / 字节数 | 50 000 / 32 MiB |
| 保留的载荷版本数 | 5 |
| 分页大小 | 默认 50，最大 500 |

---

## 下一步

- [策略](./08-policies.md) —— 决定冷却、封禁和隔离的规则，也就是本页教你如何撤销的那些处置的来源。
- [代理池](./07-proxies.md) —— 租约的另一半；一整批身份同时变坏时，它通常是嫌疑人。
- [节点 API 参考](./13-node-api.md) —— 节点如何租借身份并上报结果。
- [密钥保管库](./10-secrets.md) —— `secret_ref` 字段指向的东西。
- [可观测性与告警](./12-observability.md) —— 比节点更早告诉你池子在劣化的仪表盘、风险事件与告警。
- [运维手册](./16-operations.md) —— 热状态重建、数据保留，以及本页各项操作背后的日常运维流程。
