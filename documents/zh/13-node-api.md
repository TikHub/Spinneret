# 节点 API 参考

**爬虫节点会调用的每一个 RPC 的完整参考：传输方式、认证、请求与响应字段、真实示例、错误模型、重试规则与兼容性承诺。**

[English](../en/13-node-api.md)

---

## 目录

- [传输与 URL](#传输与-url)
- [认证](#认证)
- [JSON 约定](#json-约定)
- [服务一览](#服务一览)
- [LeaseService.Acquire](#leaseserviceacquire)
- [LeaseService.AcquireBatch](#leaseserviceacquirebatch)
- [LeaseService.Renew](#leaseservicerenew)
- [LeaseService.Release](#leaseservicerelease)
- [ReportService.Report](#reportservicereport)
- [ConfigService.GetConfig](#configservicegetconfig)
- [ConfigService.BatchGetConfig](#configservicebatchgetconfig)
- [ConfigService.WatchConfig](#configservicewatchconfig)
- [SecretService.GetSecret](#secretservicegetsecret)
- [错误模型](#错误模型)
- [幂等性](#幂等性)
- [推荐的客户端循环](#推荐的客户端循环)
- [限额与限流](#限额与限流)
- [版本与兼容性承诺](#版本与兼容性承诺)

---

## 传输与 URL

API 定义在 `proto/spinneret/v1/*.proto`（包名 `spinneret.v1`），由
[Connect](https://connectrpc.com) 提供服务。每个 RPC 都是一次一元 `POST`：

```text
POST <base-url>/spinneret.v1.<Service>/<Method>
```

没有 `/api` 前缀，也没有路径版本号：版本写在包名里。base URL 就是服务端（或它前面的反向代理）监听的地址——
用 Compose 栈部署时默认是 `http://localhost:8080`（`SPINNERET_HTTP_ADDR` 默认 `:8080`）。

四种线上协议指向同一批处理器：

| 协议 | `Content-Type` | 说明 |
| --- | --- | --- |
| 纯 HTTP + JSON | `application/json` | 一次带 JSON body 的普通 `POST`，不需要任何客户端库。 |
| Connect | `application/json` 加 `Connect-Protocol-Version: 1` | SDK 默认使用的方式。 |
| gRPC | `application/grpc` | 需要 HTTP/2。明文监听时服务端支持 h2c；设置了 `SPINNERET_TLS_CERT_FILE` 时支持 HTTP/2 over TLS。 |
| gRPC-Web | `application/grpc-web` | 面向浏览器，节点没有理由使用。 |

不带 `Connect-Protocol-Version` 头的纯 JSON `POST` 也会被接受。带上这个头可以让服务端在任何情况下都按
Connect 一元格式应答，这正是 SDK 所依赖的。

**注意。** 使用 gRPC 时，`Spinneret-Reason` 和 `Spinneret-Retry-After-Ms` 以响应**尾部（trailer）**
而不是响应头的形式返回；使用 Connect 和纯 JSON 时它们是普通的响应头。

### 请求头

| 请求头 | 是否必需 | 含义 |
| --- | --- | --- |
| `Authorization: Bearer spn_…` | 是 | API 令牌。 |
| `Content-Type: application/json` | 是 | JSON 传输方式下必需。 |
| `Connect-Protocol-Version: 1` | 否 | 选择 Connect 一元协议。 |
| `X-Spinneret-Node` | 否 | 节点实例名，用于按节点统计。会被清洗为最多 128 个 `A-Za-z0-9._:@/-` 字符，其余字符替换为 `_`。 |
| `Connect-Timeout-Ms` | 否 | 客户端截止时间，会传递给处理器。 |

`X-Spinneret-Tenant` 和 `X-Spinneret-CSRF` 是控制台使用的头。节点不发送它们：租户和命名空间由令牌决定。

### 服务端超时

| 约束 | 取值 |
| --- | --- |
| 默认单请求截止时间 | 60 秒 |
| `ConfigService/WatchConfig` 截止时间 | 请求里的 `timeout_ms`（`0` 表示默认 30 秒）按 60 秒截顶，再加 10 秒宽限 |
| 节点服务的请求体上限 | 8 MiB |
| 读头超时 / 空闲超时 | 10 秒 / 120 秒 |

超过 4096 字节的响应在客户端声明了可识别的压缩算法时会被压缩。

---

## 认证

每一次节点调用都通过 `Authorization` 头里的 API 令牌完成认证：

```http
Authorization: Bearer spn_3xAmpL3...
```

明文令牌是 `spn_` 加 43 个 base62 字符（共 47 个字符）。它只在创建时显示一次，之后再也看不到——服务端只保存
它的 SHA-256 摘要。令牌的创建与吊销见[租户、用户与令牌](./11-access-control.md)，`spnr token` 命令见
[命令行工具](./15-cli.md)。

令牌携带命名空间。节点从不发送命名空间：`AcquireRequest` 里根本没有命名空间字段；配置类 RPC 的
`namespace` 字段是可选的，只是让客户端声明自己以为在和哪个命名空间说话——一旦填写，它必须等于令牌的命名空间。

### 权限范围

每个 RPC 需要令牌带有相应的权限范围：

| RPC | 需要的权限范围 | 参数 |
| --- | --- | --- |
| `LeaseService/*` | `lease:acquire` | 可选的站点名（`lease:acquire:example-site`）。不带参数时覆盖命名空间下所有站点。 |
| `ReportService/Report` | `report:write` | 可选的站点名。只要令牌在命名空间内任意一处有 `report:write`，整批就被受理；随后每条上报都按其租约所属站点单独校验，不通过的以 `scope_missing` 单独拒绝。 |
| `ConfigService/*` | `config:read` | 可选的配置分组 glob（`config:read:crawler*`）。 |
| `SecretService/GetSecret` | `secret:read` | **必需**的 `"<namespace>/<path>"` glob，例如 `secret:read:prod/signing/*`。 |

解析配置项内容中的 `${secret:...}` 引用，还需要能匹配被引用密钥的 `secret:read` 权限范围；否则
`GetConfig` 以 `permission_denied` 失败，并记入审计日志。

令牌还可以带 IP 允许列表（IP 或 CIDR 前缀）和每秒速率限制，见[限额与限流](#限额与限流)。

---

## JSON 约定

以下规则适用于本页的每一个请求和响应。

- **字段名是 snake_case**，与 `.proto` 文件里写的完全一致：`lease_id`、`renew_before_ms`、
  `http_status`。服务端注册的 JSON 编解码器启用了 `UseProtoNames`，永远不会输出 `leaseId`。
- **零值总是输出。** 响应里会出现 `""`、`0`、`false`、`[]` 和 `{}`，而不是省略字段。未设置的**消息**是
  `null`——没有分配代理时 `"proxy": null`，永不过期的密钥 `"expires_at": null`。
- **未知请求字段被忽略**（`DiscardUnknown`），所以新版客户端可以和旧版服务端通信而不会失败。
- **时间戳**是 UTC 的 RFC 3339 字符串：`"2026-09-16T08:30:11.120Z"`。
- **时长**统一用以 `_ms` 结尾的整数毫秒字段表示。节点 API 里没有时长字符串。
- **整数**：节点 API 全部是 `int32`，因此计数和毫秒字段都是普通 JSON 数字。唯一的例外是
  `Report.response_bytes`，它是 `int64` 请求字段，`48213` 和 `"48213"` 都可以。
- **ID** 形如 `<前缀>_<32 位十六进制>`（`idt_…`、`pxy_…`、`sec_…`）。租约 ID 是不透明的；它内部的形状是
  `lse_<32 位十六进制>_<站点 key 的 36 进制>_<分片，2 位十六进制>`，客户端不得解析它。

---

## 服务一览

| 服务 | 过程 | 权限范围 |
| --- | --- | --- |
| `LeaseService` | `Acquire`、`AcquireBatch`、`Renew`、`Release` | `lease:acquire` |
| `ReportService` | `Report` | `report:write` |
| `ConfigService` | `GetConfig`、`BatchGetConfig`、`WatchConfig` | `config:read` |
| `SecretService` | `GetSecret` | `secret:read` |

没有单独的 `ReportBatch` 过程：`ReportService/Report` **本身就是**批量调用——一次请求携带 1 到 500 条上报。

下面的示例都假定：

```bash
export SPINNERET_URL=http://localhost:8080
export SPINNERET_TOKEN=spn_...
export SPINNERET_NODE=crawler-01
```

---

## LeaseService.Acquire

为一次针对某站点端点组的请求租借一个身份——附带渲染好的凭据，以及（当轮换策略要求时）一个代理。这是热路径上的
调用：它把"我想发一个请求"变成"用谁的身份发、走哪条代理、有效期多久"。

租约类操作**不写**审计日志；它们记入租借与租约统计。

### 请求

| 字段 | 类型 | 必需 | 含义 | 限制 |
| --- | --- | --- | --- | --- |
| `site` | string | 是 | 令牌命名空间内的站点名。 | 1–64 字符 |
| `client` | string | 是 | 站点声明的客户端类型，例如 `web`、`mobile`、`partner`。 | 1–32 字符 |
| `uri` | string | 否 | 请求路径（可带查询串）或绝对 URL，用于匹配端点组。设置了 `endpoint_group` 时被忽略。 | ≤ 2048 字节（固定的服务端上限，所有站点一致） |
| `endpoint_group` | string | 否 | 显式指定端点组名，优先级高于 `uri`。 | ≤ 64 字符 |
| `session_key` | string | 否 | 会话粘性键。当轮换策略启用会话粘性时，相同键的请求在身份仍可用期间复用同一个身份。 | ≤ 256 字符 |
| `wait_ms` | int32 | 否 | 服务端最多等待多久才可能等到可用身份，超时即失败。`0` 表示立即失败。 | 0–5000 |

`uri` 与 `endpoint_group` 都为空时，使用 `_default` 端点组。填了 `uri` 但没有任何 URI 规则命中时，同样回落到
`_default`；只有显式指定了一个不存在的 `endpoint_group` 才会失败，报 `endpoint_group_unknown`。

### 响应

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `lease` | object | 租约元数据（见下）。 |
| `credential` | object | 渲染后的凭据（见下）。 |
| `proxy` | object \| null | 分配的代理；轮换策略不分配代理时为 `null`。 |
| `hints` | object | 客户端提示。 |

`lease`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `lease_id` | string | 不透明的租约 ID。每条上报都要带上它。 |
| `identity_id` | string | 被租借身份的 ID（`idt_…`）。 |
| `identity_type` | string | 身份类型名。 |
| `endpoint_group` | string | 本次租约对应的端点组。 |
| `expires_at` | timestamp | 不续约的话租约到期的时间。 |
| `sticky` | bool | 通过 `session_key` 复用了同一身份时为 true。 |
| `probe` | bool | 该租约是探测租约时为 true——熔断器半开探测，或正在验证的 pending 身份。它的上报决定状态迁移，所以必须如实上报，且不能丢弃。 |

`credential` 是身份载荷的投递渲染结果，形状由身份类型的 `deliver` 段决定。未使用的段是空映射、空字符串或 `null`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `cookies` | map<string,string> | Cookie，名 → 值。 |
| `cookie_header` | string | 同样的 cookie 渲染成 `Cookie` 头的值（`k1=v1; k2=v2`）。 |
| `headers` | map<string,string> | 需要附加的请求头。 |
| `query` | map<string,string> | 需要附加的查询参数。 |
| `json` | any \| null | 任意 JSON 值，例如一段请求体片段。 |
| `values` | object \| null | 自由形态的类型化值：设备参数、签名密钥等。 |

`proxy`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `proxy_id` | string | 代理 ID（`pxy_…`）。 |
| `url` | string | 完整的代理 URL，**包含凭据**，例如 `http://user:pass@host:port`。切勿打日志。 |
| `kind` | string | `datacenter`、`residential`、`mobile` 或 `tunnel`。 |
| `region` | string | 地区，通常是 ISO 国家码；未知时为空。 |

`hints`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `renew_before_ms` | int32 | 距 `expires_at` 剩余不足这么多毫秒时就应该续约。它等于租约 TTL 的四分之一。 |

### 示例

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Acquire" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: $SPINNERET_NODE" \
  -d '{
    "site": "example-site",
    "client": "web",
    "uri": "/search?q=shoes",
    "session_key": "",
    "wait_ms": 500
  }'
```

```json
{
  "lease": {
    "lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
    "identity_id": "idt_0199c1e8b4417a2c9d0e3f5a6b7c8d9e",
    "identity_type": "web_cookie",
    "endpoint_group": "search",
    "expires_at": "2026-09-16T08:31:11.120Z",
    "sticky": false,
    "probe": false
  },
  "credential": {
    "cookies": {"sid": "a1b2c3d4"},
    "cookie_header": "sid=a1b2c3d4",
    "headers": {"User-Agent": "example-client/1.0", "Accept-Language": "en-US"},
    "query": {},
    "json": null,
    "values": {"device_id": "0f9c1a5e", "app_version": "12.4.0"}
  },
  "proxy": {
    "proxy_id": "pxy_0199c1d2f3a45b6c7d8e9f0a1b2c3d4e",
    "url": "http://user:pass@proxy.internal:8000",
    "kind": "residential",
    "region": "US"
  },
  "hints": {
    "renew_before_ms": 15000
  }
}
```

### 错误

| Connect code | reason | 含义 | 客户端应该怎么做 |
| --- | --- | --- | --- |
| `invalid_argument` | `site_unknown` | `site` 为空或不是本命名空间的站点。 | 修配置，不要重试。 |
| `invalid_argument` | `client_unknown` | 站点没有声明该客户端。 | 修配置，不要重试。 |
| `invalid_argument` | `endpoint_group_unknown` | `site/client` 下不存在该端点组。 | 修配置，不要重试。 |
| `invalid_argument` | `uri_invalid` | `uri` 为空、既不是以 `/` 开头的路径也不是绝对 `http(s)` URL、超过 2048 字节，或含有控制字符、空白字符或非法 UTF-8。 | 修正 URI，不要重试。 |
| `invalid_argument` | `invalid_argument` | 某字段违反校验规则（`wait_ms` 越界、`site` 过长等）。 | 修请求，不要重试。 |
| `resource_exhausted` | `no_identity_available` | `wait_ms` 内没有可用身份：全部在冷却、被封禁、被隔离或已被租出。 | 等待 `Spinneret-Retry-After-Ms`（被限制在 50 毫秒–60 秒）后重试。 |
| `resource_exhausted` | `no_proxy_available` | 有可用身份，但没有代理符合轮换策略。 | 等待提示时长后重试。持续出现就检查代理池。 |
| `unavailable` | `circuit_open` | 该端点组的熔断器处于熔断状态。 | 在提示时长内停止向该端点组发请求，不要猛冲。 |
| `unavailable` | `site_paused` | 运维人员暂停了该站点。重试提示为 30000 毫秒。 | 暂停整个站点。 |
| `unavailable` | `overloaded` | 服务端已到自己的租借并发上限，在真正尝试之前就把这次调用甩掉了。没有发出任何 Redis 命令，也没有消耗任何资源。 | 等待 `Spinneret-Retry-After-Ms`（在 100–200 毫秒之间抖动）后重试。重试总是安全的。 |
| `unauthenticated` | `token_invalid` / `token_expired` / `token_revoked` | 令牌不可用。 | 停下。必须由人来修令牌。 |
| `permission_denied` | `scope_missing` | 令牌对该站点没有 `lease:acquire`。 | 停下，修令牌的权限范围。 |
| `permission_denied` | `ip_not_allowed` | 节点地址不在令牌的 IP 允许列表内。 | 停下，修允许列表。 |
| `resource_exhausted` | `rate_limited` | 超过令牌的每秒速率限制。 | 等待提示时长并降速。 |

Acquire **不是幂等的**：重试一次就会多签发一个租约。Go SDK 只在故障可证明发生在请求发出之前（拨号、DNS 错误），
或服务端自己回了 `unavailable` 时才重试它。

**`overloaded` 和 `no_identity_available` 含义不同，现在可以区分开了。** `no_identity_available` 的含义
与以前完全一致：身份池被查过了，但没有可用的身份，所以解法是增加身份或放宽轮换策略。`overloaded` 表示
服务端根本没有去查身份池，因为它在 Redis 上在飞的租借脚本已经达到预算允许的上限；解法是扩容或降低负载。
被甩掉的请求发出零条 Redis 命令，这正是它用 `unavailable`（两个 SDK 都会自动重试）的原因，也是重试总是
安全的原因。预算和 `SPINNERET_ACQUIRE_*` 各项开关见[性能与调优](./17-performance.md)。

---

## LeaseService.AcquireBatch

一次往返租借最多 `count` 个不同的身份。当工作池一次要拿多个身份时用它，可以省掉 `count` 次 Acquire 的单次开销。

### 请求

字段与 `Acquire` 相同，另加：

| 字段 | 类型 | 必需 | 含义 | 限制 |
| --- | --- | --- | --- | --- |
| `count` | int32 | 是 | 请求的不同身份数量。 | 1–50 |

`session_key` 只在 `count` 为 1 时生效；会话粘性无法作用于一组不同的身份。`wait_ms` 约束的是等待**第一个**
租约的时间。

### 响应

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `leases` | AcquireResponse 数组 | 成功签发的租约，每个都带自己的凭据、代理和提示。可能少于 `count`。 |
| `requested` | int32 | 原样回显请求的 `count`。 |

部分成功是正常情况：可用身份不足时，返回能签发的那些。只有**一个都签发不出来**时才返回错误。

### 示例

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/AcquireBatch" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: $SPINNERET_NODE" \
  -d '{
    "site": "example-site",
    "client": "web",
    "endpoint_group": "detail",
    "count": 3,
    "wait_ms": 0
  }'
```

```json
{
  "leases": [
    {
      "lease": {
        "lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
        "identity_id": "idt_0199c1e8b4417a2c9d0e3f5a6b7c8d9e",
        "identity_type": "web_cookie",
        "endpoint_group": "detail",
        "expires_at": "2026-09-16T08:31:11.120Z",
        "sticky": false,
        "probe": false
      },
      "credential": {
        "cookies": {"sid": "a1b2c3d4"},
        "cookie_header": "sid=a1b2c3d4",
        "headers": {},
        "query": {},
        "json": null,
        "values": null
      },
      "proxy": null,
      "hints": {"renew_before_ms": 15000}
    },
    {
      "lease": {
        "lease_id": "lse_0199c1f4b1c28d4e9f60718293a4b5c6_1k3_12",
        "identity_id": "idt_0199c1e8c5528b3d0e1f4a6b7c8d9e0f",
        "identity_type": "web_cookie",
        "endpoint_group": "detail",
        "expires_at": "2026-09-16T08:31:11.121Z",
        "sticky": false,
        "probe": false
      },
      "credential": {
        "cookies": {"sid": "e5f6a7b8"},
        "cookie_header": "sid=e5f6a7b8",
        "headers": {},
        "query": {},
        "json": null,
        "values": null
      },
      "proxy": null,
      "hints": {"renew_before_ms": 15000}
    }
  ],
  "requested": 3
}
```

请求 3 个、签发 2 个；客户端就用拿到的这 2 个。

### 错误

与 [Acquire](#leaseserviceacquire) 的表格相同，另加 `count` 超出 1–50 时的 `invalid_argument` /
`invalid_argument`。和 Acquire 一样，它不是幂等的。

---

## LeaseService.Renew

延长一个活跃租约。当距 `expires_at` 剩余不足 `hints.renew_before_ms` 毫秒、而节点仍需要这个身份时调用它——
长翻页抓取、目标站点很慢、超时后重试等场景。

### 请求

| 字段 | 类型 | 必需 | 含义 | 限制 |
| --- | --- | --- | --- | --- |
| `lease_id` | string | 是 | 要续约的租约。 | 1–128 字符 |
| `extend_ms` | int32 | 否 | 从现在起延长的时长。`0` 表示使用轮换策略的租约 TTL。 | 0–1800000（30 分钟） |

新的到期时间永远不会超过轮换策略的**租约生命周期上限**。达到上限时调用以 `lease_lifetime_exceeded` 失败——
节点必须重新租借，而不能永远占着这个身份。

### 响应

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `expires_at` | timestamp | 租约新的到期时间。 |

### 示例

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Renew" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{
    "lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
    "extend_ms": 60000
  }'
```

```json
{"expires_at": "2026-09-16T08:32:11.480Z"}
```

### 错误

| Connect code | reason | 客户端应该怎么做 |
| --- | --- | --- |
| `failed_precondition` | `lease_released` | 租约已被归还。重新租借。 |
| `failed_precondition` | `lease_expired` | 续约请求到达前租约已失效。重新租借，并且下次更早续约。 |
| `failed_precondition` | `lease_lifetime_exceeded` | 达到轮换策略的生命周期上限。重新租借。 |
| `invalid_argument` | `invalid_argument` | `extend_ms` 超出 0–1800000，或 `lease_id` 格式错误。 |
| `not_found` | `lease_unknown` | 租约不存在、属于其他命名空间，或令牌对该租约所属站点没有 `lease:acquire`。重新租借。 |

**Renew 和 Release 会故意掩盖鉴权失败。** 令牌对该租约所属站点没有 `lease:acquire` 时，返回的是
`not_found` / `lease_unknown` 而不是 `scope_missing`，这样就不会泄漏其他命名空间和站点的租约 ID 是否存在。这两个
RPC 永远不会返回 `scope_missing`。

Renew 在"重试无害"的意义上是幂等的：最坏情况只是租约被多延长了一点。

---

## LeaseService.Release

提前结束租约，把身份还回池子。

**通常没必要显式归还。** 租约的最后一条上报可以设置 `"release": true`，随上报一起归还，省掉一次往返。只有在
节点租了一个租约却一个请求都没发时，才需要调用 `Release`。

### 请求

| 字段 | 类型 | 必需 | 含义 | 限制 |
| --- | --- | --- | --- | --- |
| `lease_id` | string | 是 | 要归还的租约。 | 1–128 字符 |

### 响应

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `released` | bool | 租约原本活跃、现已归还时为 `true`；已被归还或已失效时为 `false`。 |

### 示例

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Release" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07"}'
```

```json
{"released": true}
```

### 错误

这个调用是**幂等的**：租约记录还在、只是已经结束时，归还它会返回 `released: false` 而不是报错。等到租约记录本身
过期消失，Release 就返回 `not_found` / `lease_unknown`——租约 ID 格式错误、属于其他命名空间，或所在站点上令牌没有
`lease:acquire` 时也是这个结果（就是 Renew 那一节说的故意掩盖：Release 永远不会返回 `scope_missing`）。它仍然
可能因为上面几张表里的认证错误而失败。客户端绝不应该因为 Release 失败而让抓取失败——记一条日志，继续往下走。

---

## ReportService.Report

摄取节点用租约发出的请求的观测结果。这是热路径的另一半：Spinneret 的冷却、封禁、健康分和熔断，全部由节点在这里
上报的内容驱动。

**节点上报事实，不上报判断。** 判定结果、归因和随之而来的动作都由服务端的信号规则和动作策略决定。
`outcome_hint` 只是一个建议，仅当信号规则设置了 `trust_outcome_hint` 且取值是已知的判定结果时，服务端才会采用。

一次调用携带 1 到 500 条上报。上报是**逐条**校验的：无效的那条进入 `rejected`，其余的照常被接受。

### 请求

| 字段 | 类型 | 必需 | 含义 | 限制 |
| --- | --- | --- | --- | --- |
| `reports` | Report 数组 | 是 | 要摄取的上报。 | 1–500 条 |

每条 `Report`：

| 字段 | 类型 | 必需 | 含义 | 限制 |
| --- | --- | --- | --- | --- |
| `report_id` | string | 是 | 客户端生成的幂等键，推荐用 UUID。 | 1–64 个 `[A-Za-z0-9_.:-]` 字符 |
| `lease_id` | string | 是 | 发出该请求时使用的租约。 | 1–128 字符 |
| `uri` | string | 是 | 请求的路径，可带查询串。查询串里带凭据值时应当去掉。 | 1–2048 字符 |
| `method` | string | 否 | HTTP 方法，例如 `GET`。 | ≤ 16 字符 |
| `http_status` | int32 | 否 | 响应状态码。`0` 表示没有收到响应。 | 0–999 |
| `business_code` | string | 否 | 从响应体中解析出的业务状态码。 | ≤ 64 字符 |
| `error_kind` | string | 否 | 节点侧的传输错误。请求正常完成时为空。 | 取值为 `""`、`timeout`、`conn_reset`、`conn_refused`、`proxy_auth`、`tls`、`dns`、`other` 之一 |
| `markers` | string 数组 | 否 | 节点识别出的响应特征，例如 `captcha_page`、`login_redirect`、`empty_list`。 | ≤ 32 项，每项 1–64 字符 |
| `outcome_hint` | string | 否 | 节点提出的判定结果建议。 | ≤ 32 字符 |
| `latency_ms` | int32 | 否 | 请求耗时。 | ≥ 0 |
| `response_bytes` | int64 | 否 | 响应体大小。`48213` 或 `"48213"` 都可以。 | ≥ 0 |
| `started_at` | timestamp | **是** | 请求开始时间。 | 必填 |
| `finished_at` | timestamp | **是** | 请求结束时间。 | 必填，且不得早于 `started_at` |
| `release` | bool | 否 | 该上报处理完后归还租约。 | |

服务端可以分类出的、也是 `outcome_hint` 值得填写的已知判定结果是：`success`、`empty`、`rate_limited`、
`captcha`、`auth_invalid`、`forbidden`、`banned`、`proxy_error`、`network_error`、`target_error`、
`client_error`、`unknown`。

### 响应

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `accepted` | int32 | 被接受处理的上报数。 |
| `duplicated` | int32 | 因 `report_id` 已被摄取而忽略的上报数。 |
| `rejected` | RejectedReport 数组 | 未被接受的上报。始终存在，可能为空。 |

`RejectedReport`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `report_id` | string | 原样回显的 `report_id`——可能为空或非法。 |
| `reason` | string | `invalid_argument`、`lease_unknown` 或 `scope_missing`。 |
| `message` | string | 供人阅读的细节。 |

### 示例

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ReportService/Report" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: $SPINNERET_NODE" \
  -d '{
    "reports": [
      {
        "report_id": "0f0bb4b1-6c2e-4a35-9a6e-2f8c5d31a7c1",
        "lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
        "uri": "/search",
        "method": "GET",
        "http_status": 200,
        "business_code": "0",
        "error_kind": "",
        "markers": [],
        "outcome_hint": "success",
        "latency_ms": 412,
        "response_bytes": 48213,
        "started_at": "2026-09-16T08:30:11.120Z",
        "finished_at": "2026-09-16T08:30:11.532Z",
        "release": true
      },
      {
        "report_id": "1a7f2c94-8d31-4f0a-bb55-6d2e19c47f80",
        "lease_id": "lse_0199c1f4b1c28d4e9f60718293a4b5c6_1k3_12",
        "uri": "/detail/1024",
        "method": "GET",
        "http_status": 0,
        "error_kind": "timeout",
        "markers": [],
        "latency_ms": 10000,
        "response_bytes": 0,
        "started_at": "2026-09-16T08:30:11.100Z",
        "finished_at": "2026-09-16T08:30:21.100Z",
        "release": true
      }
    ]
  }'
```

```json
{
  "accepted": 2,
  "duplicated": 0,
  "rejected": []
}
```

其中一条租约有问题的批次长这样——好的那些照样进去了：

```json
{
  "accepted": 1,
  "duplicated": 0,
  "rejected": [
    {
      "report_id": "1a7f2c94-8d31-4f0a-bb55-6d2e19c47f80",
      "reason": "lease_unknown",
      "message": "lease is unknown or expired"
    }
  ]
}
```

### 逐条拒绝的原因

| `reason` | 起因 | 客户端应该怎么做 |
| --- | --- | --- |
| `invalid_argument` | 某字段违反规则：`report_id` 语法错误、缺 `started_at`/`finished_at`、`finished_at` 早于 `started_at`、某个 marker 超过 64 字符、`error_kind` 取值未知。 | 修正这条上报，永远不要原样重试。 |
| `lease_unknown` | 租约 ID 格式错误、属于其他命名空间，或租约已超出保留窗口。 | 丢弃这条上报。它描述的那次请求对系统而言已经丢失了。 |
| `scope_missing` | 令牌对该租约所属站点没有 `report:write`。 | 修令牌的权限范围；如果之后还能重试，就先留着这条上报。 |

### 调用级错误

| Connect code | reason | 含义 |
| --- | --- | --- |
| `invalid_argument` | `invalid_argument` | 一条上报都没有，或超过 500 条。 |
| `permission_denied` | `scope_missing` | 令牌在整个命名空间内都没有 `report:write`。 |
| `unauthenticated` | `token_invalid` / `token_expired` / `token_revoked` | |
| `resource_exhausted` | `rate_limited` | 超过令牌速率限制。 |
| `unavailable` | `internal` | 热状态不可达。重试提示为 1000 毫秒。整批重试即可——已接受的会以 `duplicated` 返回。 |

---

## ConfigService.GetConfig

返回一个已发布的配置项。只提供**当前已发布版本**：草稿对节点不可见。内容中的 `${secret:...}` 引用在投递前被解析，
所以读取这类配置项的令牌还需要相应的 `secret:read` 权限范围。

配置项、分组、版本和发布流程见[配置中心](./09-config-center.md)。

### 请求

| 字段 | 类型 | 必需 | 含义 | 限制 |
| --- | --- | --- | --- | --- |
| `namespace` | string | 否 | 填写时必须等于令牌的命名空间。 | ≤ 64 字符 |
| `group` | string | 是 | 分组名，例如 `crawler` 或保留分组 `_runtime`。 | 1–128 字符 |
| `key` | string | 是 | 配置项键，例如 `search.json`。 | 1–256 字符 |

### 响应

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `item` | ConfigItem | 已发布的配置项。 |

`ConfigItem`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `namespace` | string | 命名空间名。 |
| `group` | string | 分组名。 |
| `key` | string | 配置项键。 |
| `format` | string | `json`、`yaml` 或 `text`。 |
| `version` | int32 | 已发布版本号。每次发布**以及每次回滚**都会递增，且对同一 namespace/group/key 永不重复：新建配置项从 1 开始，删除后重建的配置项从删除前发布过的最后一个版本之上继续。 |
| `content` | string | 已发布内容，密钥引用已解析。 |
| `updated_at` | timestamp | 该版本的发布时间。 |
| `has_secret_refs` | bool | 内容中含有已解析的密钥引用时为 true。**不要以明文持久化这类内容**——不要写进本地快照，也不要写进日志。 |

### 示例

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ConfigService/GetConfig" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"group": "crawler", "key": "search.json"}'
```

```json
{
  "item": {
    "namespace": "prod",
    "group": "crawler",
    "key": "search.json",
    "format": "json",
    "version": 7,
    "content": "{\"page_size\":20,\"max_pages\":50,\"concurrency\":8}",
    "updated_at": "2026-09-16T07:55:02.000Z",
    "has_secret_refs": false
  }
}
```

### 保留分组 `_runtime`

`_runtime` 是只读、由系统维护的分组，其中两个配置项节点可以像读普通配置项一样读取：

| 键 | 内容 |
| --- | --- |
| `breakers` | 端点组熔断器状态：`{"namespace": …, "version": …, "sites": {"<site>": {"paused": bool, "groups": {"<client>/<group>": {"state": …, "open_until": …, "manual": bool, "reason": …}}}}}`。只列出非 closed 的熔断器。 |
| `site_switches` | 站点暂停开关：`{"namespace": …, "version": …, "sites": {"<site>": {"paused": bool, "reason": …, "paused_at": …}}}`。 |

只有当令牌的 `config:read` 分组 glob 能匹配 `_runtime` 时才读得到——不带参数的 `config:read` 就可以。订阅
`_runtime/breakers` 是节点在一秒内感知熔断、而不必等下一次 Acquire 失败才知道的办法。

### 错误

| Connect code | reason | 客户端应该怎么做 |
| --- | --- | --- |
| `not_found` | `not_found` | 配置项不存在或没有已发布版本。回退到默认值，不要在紧循环里重试。 |
| `permission_denied` | `scope_missing` | `config:read` 没覆盖该分组、`secret:read` 没覆盖被引用的密钥，或 `namespace` 填成了令牌命名空间以外的值。停下，修令牌或修请求。 |
| `failed_precondition` | `failed_precondition` | 已发布内容里引用的 `${secret:...}` 不存在，或本实例上密钥解析不可用。修配置项或修密钥，不要重试。 |
| `invalid_argument` | `invalid_argument` | 某字段越界。 |
| `unavailable` | `internal` | 数据库不可达。退避重试，同时继续使用上一份已知内容。 |

---

## ConfigService.BatchGetConfig

一次往返取回多个已发布配置项。不存在或未发布的项进入 `missing`，而不会让整个调用失败。

### 请求

| 字段 | 类型 | 必需 | 含义 | 限制 |
| --- | --- | --- | --- | --- |
| `namespace` | string | 否 | 填写时必须等于令牌的命名空间。 | ≤ 64 字符 |
| `items` | ConfigRef 数组 | 是 | 要取的配置项。重复项只返回一次。 | 1–200 项 |

`ConfigRef` 是 `{"group": string（1–128）, "key": string（1–256）}`。

### 响应

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `items` | ConfigItem 数组 | 已发布的配置项，按请求顺序返回。 |
| `missing` | ConfigRef 数组 | 不存在或没有已发布版本的请求项。 |

### 示例

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ConfigService/BatchGetConfig" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{
    "items": [
      {"group": "crawler", "key": "search.json"},
      {"group": "crawler", "key": "detail.json"},
      {"group": "_runtime", "key": "breakers"}
    ]
  }'
```

```json
{
  "items": [
    {
      "namespace": "prod",
      "group": "crawler",
      "key": "search.json",
      "format": "json",
      "version": 7,
      "content": "{\"page_size\":20,\"max_pages\":50,\"concurrency\":8}",
      "updated_at": "2026-09-16T07:55:02.000Z",
      "has_secret_refs": false
    },
    {
      "namespace": "prod",
      "group": "_runtime",
      "key": "breakers",
      "format": "json",
      "version": 12,
      "content": "{\"namespace\":\"prod\",\"version\":11,\"sites\":{\"example-site\":{\"paused\":false,\"groups\":{}}}}",
      "updated_at": "2026-09-16T08:30:00.000Z",
      "has_secret_refs": false
    }
  ],
  "missing": [
    {"group": "crawler", "key": "detail.json"}
  ]
}
```

### 错误

只要令牌对**任意一个**请求分组缺少 `config:read`，或对任意被引用的密钥没有读权限，整个请求就失败——没有部分授权。
合计内容超过服务端的节点读取大小预算（默认 32 MiB）时同样失败。其余与 `GetConfig` 相同，唯一的区别是永远不会出现
`not_found`：缺失的项进入 `missing`。

---

## ConfigService.WatchConfig

节点用来在一秒内感知配置变更、而不必轮询打转的长轮询。它会立刻返回那些已发布版本与调用方声称持有的版本不同的配置项；
否则一直阻塞，直到出现这样的变更、超时，或客户端取消。

### 请求

| 字段 | 类型 | 必需 | 含义 | 限制 |
| --- | --- | --- | --- | --- |
| `namespace` | string | 否 | 填写时必须等于令牌的命名空间。 | ≤ 64 字符 |
| `items` | WatchItem 数组 | 是 | 要订阅的配置项，以及当前持有的版本。 | 1–200 项 |
| `timeout_ms` | int32 | 否 | 服务端最多等待多久。`0` 选用服务端默认值 30000。 | 0–60000 |

`WatchItem`：

| 字段 | 类型 | 必需 | 含义 |
| --- | --- | --- | --- |
| `group` | string | 是 | 分组名（1–128 字符）。 |
| `key` | string | 是 | 配置项键（1–256 字符）。 |
| `version` | int32 | 否 | 调用方持有的版本。`0` 表示"一个都没有"，因此该项一旦有已发布版本就会被返回。必须 ≥ 0。 |

### 响应

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `items` | ConfigItem 数组 | 已发布版本与订阅版本不同的配置项。超时且无变更时为空。 |

值得了解的语义：

- 一个配置项算作"变更"的条件是它的已发布版本与持有版本**不同**。因此回滚同样会唤醒长轮询，因为回滚会发布一个新的、
  更高的版本号。
- 已删除和从未发布的项永远不会被返回，也不会结束这次长轮询。
- 如果调用方持有的版本比本实例缓存的**更新**（它从另一个副本读过，或一次总线事件丢了），服务端会先从真实来源重新读取。
  节点永远不会被退回到更旧的版本，也不会收到它已经持有的版本。
- 变更项合计超过响应大小预算时只返回一部分，剩下的在下一次轮询返回。

### 示例

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ConfigService/WatchConfig" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{
    "items": [
      {"group": "crawler", "key": "search.json", "version": 7},
      {"group": "_runtime", "key": "breakers", "version": 12}
    ],
    "timeout_ms": 30000
  }'
```

有变更时：

```json
{
  "items": [
    {
      "namespace": "prod",
      "group": "crawler",
      "key": "search.json",
      "format": "json",
      "version": 8,
      "content": "{\"page_size\":20,\"max_pages\":80,\"concurrency\":8}",
      "updated_at": "2026-09-16T08:41:19.000Z",
      "has_secret_refs": false
    }
  ]
}
```

超时内无变更时：

```json
{"items": []}
```

客户端的循环是：为每个配置项持有一个版本号，发起长轮询，应用返回的内容，更新持有版本，立刻再发起下一次轮询。HTTP 读
超时要设为 `timeout_ms` 再加几秒宽限——SDK 用 5 秒——因为服务端自己的截止时间还加了 10 秒宽限。

### 错误

| Connect code | reason | 客户端应该怎么做 |
| --- | --- | --- |
| `resource_exhausted` | `rate_limited` | 本实例上阻塞的订阅者太多（默认 20000，`SPINNERET_MAX_WATCHERS`）。退避后重连，并考虑减少每个节点的订阅者数量。 |
| `permission_denied` | `scope_missing` | `config:read` 没覆盖某个被订阅的分组、`secret:read` 没覆盖被引用的密钥，或 `namespace` 填成了令牌命名空间以外的值。 |
| `failed_precondition` | `failed_precondition` | 将要返回的配置项里引用的 `${secret:...}` 不存在，或本实例上密钥解析不可用。修配置项或修密钥，不要重试。 |
| `invalid_argument` | `invalid_argument` | 一项都没有、超过 200 项、`timeout_ms` 大于 60000，或 `version` 为负。 |
| `unavailable` | `internal` | 退避重试。 |

因服务端排空而中断的长轮询以取消结束，而不是值得告警的错误：重连即可。

---

## SecretService.GetSecret

读取令牌命名空间内某个密钥的明文。**每一次读取都会写入审计日志**，记录版本、用途（本 RPC 记为 `api`）、客户端 IP
和结果。

密钥的存储与版本管理见[密钥保管库](./10-secrets.md)。

### 请求

| 字段 | 类型 | 必需 | 含义 | 限制 |
| --- | --- | --- | --- | --- |
| `path` | string | 是 | 相对命名空间的密钥路径，例如 `signing/api_key`。 | 1–256 字符，须匹配 `^[a-z0-9][a-z0-9_./-]*$` |
| `version` | int32 | 否 | 要读取的版本。`0` 读当前版本。 | ≥ 0 |

### 响应

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `path` | string | 密钥路径。 |
| `version` | int32 | 实际读到的版本。 |
| `value` | string | 明文。 |
| `expires_at` | timestamp \| null | 过期时间；不过期时为 `null`。 |

**已过期的密钥仍然会返回**，服务端只记一条 warn 日志。如果你的节点必须拒绝使用过期密钥，请自行检查 `expires_at`。

### 示例

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.SecretService/GetSecret" \
  -H "Content-Type: application/json" \
  -H "Connect-Protocol-Version: 1" \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"path": "signing/api_key", "version": 0}'
```

```json
{
  "path": "signing/api_key",
  "version": 3,
  "value": "sk-live-7f3c9a21b845",
  "expires_at": null
}
```

### 错误

| Connect code | reason | 客户端应该怎么做 |
| --- | --- | --- |
| `not_found` | `not_found` | 密钥或该版本不存在。不要重试。 |
| `permission_denied` | `scope_missing` | `secret:read` 的 glob 匹配不上 `"<namespace>/<path>"`。这次尝试会被审计。修令牌。 |
| `permission_denied` | `permission_denied` | 调用方不是 API 令牌。`SecretService` 只面向节点；控制台走 `SecretAdminService.RevealSecret`。 |
| `invalid_argument` | `invalid_argument` | 路径不符合模式，或 `version` 为负。 |
| `unavailable` | `internal` | 退避重试。 |

**警告。** 密钥只在内存里缓存，时间越短越好，绝不要写到磁盘或日志行里。每次读取都被审计，所以一个每请求都去取密钥的
节点还会把审计日志刷爆。

---

## 错误模型

错误使用 Connect 的错误格式。响应体是：

```json
{"code": "resource_exhausted", "message": "no identity available for example-site/web/search"}
```

机器可读的细节放在响应头里，因此纯 JSON 客户端不必解析响应体：

```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 1200
```

| 响应头 | 含义 |
| --- | --- |
| `Spinneret-Reason` | 稳定的 snake_case 原因。处理器产生的错误上一定有它。请求解码阶段就失败的错误——JSON 格式错误，或请求体超过 8 MiB 上限——只带 Connect code，所以客户端必须容忍这个头缺失，并回退到 code 处理。 |
| `Spinneret-Retry-After-Ms` | 建议等待的毫秒数。只有服务端有可用数值时才出现；没有它表示"没有提示"，不等于"立即重试"。 |

内部错误永远不会泄漏成因：客户端看到的是
`{"code":"internal","message":"internal error"}`，真正的错误在服务端日志里。

### Connect code 与 HTTP 状态码

| Connect code | HTTP |
| --- | --- |
| `invalid_argument` | 400 |
| `failed_precondition` | 400 |
| `unauthenticated` | 401 |
| `permission_denied` | 403 |
| `not_found` | 404 |
| `aborted` | 409 |
| `already_exists` | 409 |
| `resource_exhausted` | 429 |
| `canceled` | 499 |
| `internal` | 500 |
| `unavailable` | 503 |
| `deadline_exceeded` | 504 |

### 全部 reason

| reason | code | 可重试 | 重试提示 | 含义与处理方式 |
| --- | --- | --- | --- | --- |
| `token_invalid` | `unauthenticated` | 否 | — | 令牌格式错误或未知。停下来修配置。 |
| `token_expired` | `unauthenticated` | 否 | — | 令牌的 `expires_at` 已过。签发新令牌。 |
| `token_revoked` | `unauthenticated` | 否 | — | 令牌已在控制台被吊销。签发新令牌。 |
| `ip_not_allowed` | `permission_denied` | 否 | — | 节点地址不在令牌的 IP 允许列表内。 |
| `session_invalid` | `unauthenticated` | 否 | — | 完全没有提供凭据。对节点而言就是缺少 `Authorization` 头。 |
| `scope_missing` | `permission_denied` | 否 | — | 令牌的权限范围不包含该 RPC 需要的权限。也会以逐条形式出现在 `Report` 的响应里。 |
| `permission_denied` | `permission_denied` | 否 | — | 调用方主体类型不适用于该 RPC（例如控制台用户调用 `SecretService`）。 |
| `site_unknown` | `invalid_argument` | 否 | — | `site` 为空或不是本命名空间的站点。 |
| `client_unknown` | `invalid_argument` | 否 | — | 站点没有声明该客户端。 |
| `endpoint_group_unknown` | `invalid_argument` | 否 | — | `site/client` 下不存在该端点组。 |
| `uri_invalid` | `invalid_argument` | 否 | — | `uri` 为空、既不是以 `/` 开头的路径也不是绝对 `http(s)` URL、超过 2048 字节，或含有控制字符、空白字符或非法 UTF-8。 |
| `invalid_argument` | `invalid_argument` | 否 | — | 某字段违反校验规则，message 会指出是哪个字段。 |
| `no_identity_available` | `resource_exhausted` | **是** | 50 毫秒 – 60 秒 | `wait_ms` 内没有可用身份。等提示时长后重试。长期出现说明池子太小，或太多身份在冷却。 |
| `no_proxy_available` | `resource_exhausted` | **是** | 50 毫秒 – 60 秒 | 没有代理符合轮换策略。等提示时长后重试。 |
| `circuit_open` | `unavailable` | **是，等提示之后** | 熔断器剩余的熔断时间 | 端点组熔断器处于熔断状态。停止向该端点组发请求，提示时长内不要重试。 |
| `site_paused` | `unavailable` | **是，等提示之后** | 30000 毫秒 | 运维人员暂停了站点。暂停整个站点。 |
| `overloaded` | `unavailable` | **是** | 100–200 毫秒（抖动） | 服务端已到租借并发上限并甩掉了这次调用。没有发出任何 Redis 命令，所以重试总是安全的；抖动可以避免被甩掉的那批请求整齐地一起回来。它*不是* `no_identity_available`：身份池根本没有被查询。 |
| `rebuilding` | `unavailable` | **是** | 500 毫秒 – 1 秒 | 热状态正在重建。节点 RPC 不会返回它——正在重建的实例过不了就绪检查，会被负载均衡摘掉。它出现在 `spnr rebuild` 这类管理调用上。退避重试。 |
| `lease_unknown` | `not_found` / 逐条 | 否 | — | 租约不存在、属于其他命名空间、已超出保留期，或令牌对该租约所属站点没有 `lease:acquire`。重新租借；上报直接丢弃。 |
| `lease_released` | `failed_precondition` | 否 | — | 租约已被归还。重新租借。 |
| `lease_expired` | `failed_precondition` | 否 | — | 租约已失效。重新租借，并且更早续约。 |
| `lease_lifetime_exceeded` | `failed_precondition` | 否 | — | 达到轮换策略的生命周期上限。重新租借。 |
| `rate_limited` | `resource_exhausted` | **是** | 令牌桶补满所需时间 | 超过 API 令牌的每秒限制，或配置订阅者数量上限。降速。 |
| `not_found` | `not_found` | 否 | — | 目标资源不存在。 |
| `already_exists` | `already_exists` | 否 | — | 唯一性冲突。仅出现在管理类调用。 |
| `failed_precondition` | `failed_precondition` | 否 | — | 系统状态不满足该操作的前置条件。 |
| `conflict` | `aborted` | 视情况 | — | 并发修改。重新读取后再试。仅出现在管理类调用。 |
| `internal` | `internal` 或 `unavailable` | **`unavailable` 时可以** | 有时有 | 意外故障，或依赖（PostgreSQL、Valkey/Redis）不可达。退避重试，并检查服务端日志。 |

`csrf_missing`、`login_throttled`、`query_too_large` 和 `query_timeout` 也存在，但只会出现在控制台，节点
永远碰不到。

### 客户端的经验法则

1. `unauthenticated` 或 `permission_denied` → 停下。重试没有用，必须有人来处理。
2. `invalid_argument` 或 `failed_precondition` → 不要原样重试。对租约类前置条件错误，重新租借。
3. `resource_exhausted` → 等 `Spinneret-Retry-After-Ms` 然后重试。
4. `unavailable` → 带抖动的指数退避重试，**但** `circuit_open` 和 `site_paused` 例外：正确做法是在提示时长
   内停止向该端点组或站点投喂请求。
5. 没有拿到响应的传输失败 → 只重试幂等调用。`Acquire` 和 `AcquireBatch` 不是幂等的，只有在故障可证明发生在请求
   发出之前（DNS、拨号被拒）时才重试。

Go SDK 的默认策略是：重试 2 次，初始退避 100 毫秒并采用等量抖动，单次延迟上限 2 秒，且拒绝等待超过 5 秒的服务端
提示。

---

## 幂等性

| RPC | 是否幂等 | 原因 |
| --- | --- | --- |
| `Acquire` | **否** | 重试会多签发一个租约、多占用一个身份。 |
| `AcquireBatch` | **否** | 同上。 |
| `Renew` | 是 | 最坏情况只是租约被多延长了一点。 |
| `Release` | 是 | 归还已结束的租约返回 `released: false`，不报错。 |
| `Report` | **是，依据 `report_id`** | 见下。 |
| `GetConfig`、`BatchGetConfig`、`WatchConfig` | 是 | 读操作。 |
| `GetSecret` | 是 | 读操作——但每次调用都写一条审计记录，所以重试是看得见的。 |

### 上报的幂等性

每条上报都带一个客户端生成的 `report_id`。服务端在去重窗口内记住已摄取的 ID——`SPINNERET_REPORT_DEDUP_TTL`，
默认 1 小时——重复的那条计入 `duplicated` 而不会被处理两次。这正是整批上报在超时或 503 之后可以安全重试的原因：
第一次已经进去的会以 `duplicated` 回来，其余的被接受。

客户端规则：

- `report_id` 在构造上报时**只生成一次**，每次重投递都复用它。每次尝试都换一个新 UUID 会让整套机制失效。
- 使用 UUID 或其他真正有熵的值。不要用节点重启后会从头开始的计数器。
- 被 `invalid_argument` 拒绝的上报不要重试：再发一次还是会被拒。
- 租约结束很久之后才到达的上报在摄取时仍会被接受，但只有"及时"的上报——租约仍活跃，或结束时间不超过
  `SPINNERET_LATE_REPORT_WINDOW`（默认 10 分钟）——才会让租约保持可解析。请尽快投递上报。

---

## 推荐的客户端循环

每个 SDK 实现的形状，也是在没有 SDK 的语言里应当照抄的形状：

```text
# 每个进程一次
config = GetConfig(group, key)            # 或 BatchGetConfig
后台启动: watch_loop(config)
后台启动: report_flusher()                # 攒批上报，按数量或间隔刷出

# 每个请求
function fetch(uri):
    attempt = 0
    loop:
        try:
            grant = Acquire(site, client, uri, wait_ms = 500)
        catch error:
            switch reason(error):
                case "no_identity_available", "no_proxy_available", "rate_limited":
                    sleep(retry_after(error) or backoff(attempt)); attempt += 1; continue
                case "circuit_open", "site_paused":
                    pause_endpoint_group(retry_after(error)); return ENDPOINT_PAUSED
                case "token_invalid", "token_expired", "token_revoked",
                     "scope_missing", "ip_not_allowed":
                    fatal(error)                     # 必须由人来修
                default:
                    if transport_failure(error) and happened_before_send(error):
                        sleep(backoff(attempt)); attempt += 1; continue
                    raise error

        report_id = new_uuid()
        started   = now()
        deadline_watcher:                            # 仅用于长任务或翻页任务
            if grant.lease.expires_at - now() < grant.hints.renew_before_ms:
                Renew(grant.lease.lease_id, extend_ms = 0)

        try:
            response = http_send(uri,
                                 headers  = grant.credential.headers,
                                 cookies  = grant.credential.cookies,
                                 query    = grant.credential.query,
                                 proxy    = grant.proxy?.url)
            markers = detect_markers(response)       # "captcha_page"、"empty_list" 等
            enqueue_report({
                report_id:      report_id,
                lease_id:       grant.lease.lease_id,
                uri:            strip_query(uri),
                method:         "GET",
                http_status:    response.status,
                business_code:  read_business_code(response),
                markers:        markers,
                latency_ms:     now() - started,
                response_bytes: len(response.body),
                started_at:     started,
                finished_at:    now(),
                release:        true                 # 最后一条上报顺便归还租约
            })
            return response
        catch transport_error as err:
            enqueue_report({
                report_id:   report_id,
                lease_id:    grant.lease.lease_id,
                uri:         strip_query(uri),
                method:      "GET",
                http_status: 0,
                error_kind:  classify(err),          # timeout | conn_reset | conn_refused
                                                     # | proxy_auth | tls | dns | other
                latency_ms:  now() - started,
                started_at:  started,
                finished_at: now(),
                release:     true
            })
            raise err

# 后台
function report_flusher():
    loop:
        batch = take_up_to(500, from = queue, or_after = 1s)
        if batch is empty: continue
        result = Report(batch)                        # unavailable 时整批重试
        for rejected in result.rejected:
            log(rejected.report_id, rejected.reason, rejected.message)

function watch_loop(held):
    loop:
        changed = WatchConfig(items = held.as_watch_items(), timeout_ms = 30000)
        for item in changed:
            apply(item)
            held[item.group, item.key] = item.version
```

这个循环做对了五件事，而手写客户端通常会做错：

1. **每个请求一个 `report_id`，在尝试之前就生成**，每次重投递都复用它。
2. **每个租约都要上报**，失败的也要。从不上报的租约不会教会服务端任何东西，只会一直挂到自己过期。
3. **最后一条上报带 `release: true`**，而不是单独调一次 `Release`。
4. **遵守重试提示。** `Spinneret-Retry-After-Ms` 是服务端在告诉你什么时候才会有东西可分配；无视它会把一次冷却
   变成一次踩踏。
5. **`circuit_open` 和 `site_paused` 要停下工作**，不能变成重试循环。

探测（`probe`）租约还多一条规则：一定要上报，而且要如实上报。探测租约的上报正是关闭半开熔断器或激活 pending 身份的
依据。

Python 和 Go SDK 已经替你做好了这一切——见 [SDK 与示例](./14-sdks.md)。

---

## 限额与限流

| 限额 | 取值 | 出处 |
| --- | --- | --- |
| `wait_ms` | 0–5000 | `AcquireRequest`、`AcquireBatchRequest` |
| `count` | 1–50 | `AcquireBatchRequest` |
| `extend_ms` | 0–1800000（30 分钟） | `RenewRequest` |
| 每次调用的上报条数 | 1–500 | `ReportRequest` |
| 每条上报的 marker 数 | ≤ 32，每个 1–64 字符 | `Report.markers` |
| 上报中的 `uri` | 1–2048 字符 | `Report.uri` |
| 每次调用的配置项数 | 1–200 | `BatchGetConfigRequest`、`WatchConfigRequest` |
| `timeout_ms` | 0–60000（0 即 30000） | `WatchConfigRequest` |
| 每实例阻塞的订阅者数 | 20000 | `SPINNERET_MAX_WATCHERS` |
| 单次读取的配置内容合计 | 32 MiB | 服务端默认值 |
| 节点服务的请求体 | 8 MiB | 服务端 |
| `X-Spinneret-Node` | 清洗后 ≤ 128 字符 | 服务端 |
| 上报去重窗口 | 1 小时 | `SPINNERET_REPORT_DEDUP_TTL` |
| 迟到上报窗口 | 10 分钟 | `SPINNERET_LATE_REPORT_WINDOW` |

### 令牌速率限制

每个 API 令牌可以带 `rate_limit_rps`（0 表示不限，最大 1000000）。它是一个容量为一秒突发量的令牌桶，
**按服务端实例分别计算**——一个限速 100 rps 的令牌打在三个副本上，最坏情况下能跑到 300 rps。超限时调用以
`resource_exhausted` / `rate_limited` 失败，`Spinneret-Retry-After-Ms` 等于下一个令牌补满所需的时间。

限制计的是**调用次数**，不是租约数或上报条数。因此攒批是最省限额的办法：一次 `Report` 带 500 条只算一次调用，
一次 `AcquireBatch` 拿 50 个也只算一次。

---

## 版本与兼容性承诺

线上契约是 `proto/spinneret/v1/*.proto`。那些文件里的注释具有规范效力：本页与 `.proto` 注释冲突时，以 `.proto`
为准。

**在 `spinneret.v1` 内不会改变的事情：**

- 字段不会被删除、改编号，也不会更换类型或含义。
- RPC 不会被删除或改名，其请求与响应消息类型保持不变。
- reason 字符串不会被挪作他用。已有的 reason 保持原意。
- 上面列出的 JSON 约定——snake_case 字段名、输出零值、RFC 3339 时间戳、以 `_ms` 结尾的毫秒整数——保持不变。

`buf.yaml` 里配置了 `breaking: use: FILE`，所以可以用 `buf breaking --against <ref>` 把一次 proto 改动和某个已
提交的版本做比对。这一步目前需要手动执行：CI 只跑 `buf lint` 和 `buf generate`，破坏性的 proto 改动本身不会让构建
失败。

**可能变化、客户端必须容忍的事情：**

- **新字段**可能被加进任何请求或响应。忽略你不认识的字段，不要因此失败。服务端对请求已经是这么做的
  （`DiscardUnknown`）。
- **新 RPC** 可能被加进已有的服务。
- **新 reason 字符串**可能出现。遇到不认识的 reason 时回退到 Connect code 处理——同时返回 code 和 reason 正是
  为了这个。
- **新取值**可能出现在开放字符串字段里，例如 `proxy.kind` 和 `identity_type`。
- **重试提示**是建议性的，其数值可能变化。
- 线下的**默认值**（租约 TTL、长轮询超时、去重窗口）由运维可调，不同部署各不相同。绝不要写死它们；读
  `hints.renew_before_ms`，而不要假设某个 TTL。

真正不兼容的变更会是一个新包 `spinneret.v2`，与 `v1` 并存提供服务。

**滚动升级。** 升级过程中，客户端可能在同一秒内既打到新实例又打到旧实例。由于未知请求字段会被丢弃、新响应字段是
增量的，两个方向都能正常工作。唯一需要留意的是配置版本：还没看到某次发布的实例可能仍然返回上一个版本，下一次
`WatchConfig` 会纠正它——当调用方持有的版本比实例缓存的更新时，服务端会从真实来源重新读取。

---

## 下一步

- [SDK 与示例](./14-sdks.md) —— 帮你实现本页全部内容的 Python 与 Go 客户端。
- [核心概念](./04-concepts.md) —— 租约、上报、信号和动作到底是什么。
- [配置中心](./09-config-center.md) —— `GetConfig` 与 `WatchConfig` 的另一半。
- [密钥保管库](./10-secrets.md) —— `GetSecret` 返回的那个值是怎么存储和轮换的。
- [租户、用户与令牌](./11-access-control.md) —— 如何创建权限范围正确的令牌。
- [故障排查](./18-troubleshooting.md) —— 某个 reason 反复出现时该怎么办。
