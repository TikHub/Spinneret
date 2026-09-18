# SDK 与示例

**爬虫节点与 Spinneret 通信所用的客户端库：Python SDK、Go SDK、可直接运行的示例节点，以及你的语言没有 SDK 时该怎么办。**

[English](../en/14-sdks.md)

---

## 目录

- [选择客户端](#选择客户端)
- [Python SDK](#python-sdk)
  - [安装](#安装)
  - [连接配置](#连接配置)
  - [一个完整的节点](#一个完整的节点)
  - [租约](#租约)
  - [归还语义](#归还语义)
  - [上报](#上报)
  - [后台上报器](#后台上报器)
  - [配置中心](#配置中心)
  - [密钥](#密钥)
  - [错误](#错误)
  - [重试与超时](#重试与超时)
  - [错误类别](#错误类别)
  - [进程与事件循环](#进程与事件循环)
  - [API 索引](#api-索引)
- [Go SDK](#go-sdk)
  - [安装](#安装-1)
  - [选项](#选项)
  - [一个完整的节点](#一个完整的节点-1)
  - [协议](#协议)
  - [租约](#租约-1)
  - [凭据](#凭据)
  - [上报](#上报-1)
  - [后台上报器](#后台上报器-1)
  - [配置中心](#配置中心-1)
  - [密钥](#密钥-1)
  - [错误](#错误-1)
  - [重试与超时](#重试与超时-1)
  - [API 索引](#api-索引-1)
- [示例爬虫](#示例爬虫)
- [没有 SDK 时如何自己写客户端](#没有-sdk-时如何自己写客户端)
- [下一步](#下一步)

---

## 选择客户端

节点只需要两样东西：服务端 URL 和 API 令牌。其余一切都在请求时由 Spinneret 下发。有三种方式去取：

| 客户端 | 位置 | 传输 | 适用场景 |
| --- | --- | --- | --- |
| Python SDK | `sdk/python` | Connect over HTTP + JSON | Python 3.9+ 节点，同步或 asyncio |
| Go SDK | `sdk/go/spinneret` | Connect JSON（默认）或 gRPC | Go 1.27+ 节点 |
| 纯 HTTP + JSON | — | Connect over HTTP + JSON | 其他任何语言 |

两个 SDK 覆盖的正是四个节点服务 —— `LeaseService`、`ReportService`、`ConfigService` 和
`SecretService`，详见[节点 API 参考](./13-node-api.md)。它们都不暴露管理侧 API；那是控制台和
[`spnr`](./15-cli.md) 的职责。

---

## Python SDK

源码：`sdk/python`。版本 0.1.0。要求 Python 3.9+、`httpx>=0.27` 和 `pydantic>=2.6`。

### 安装

从仓库检出目录安装：

```bash
pip install ./sdk/python                # SDK 本体
pip install './sdk/python[crypto]'      # 追加：对引用密钥的配置项做加密快照
```

发行包名为 `spinneret`，因此发布到任意索引源后可直接 `pip install spinneret`。若 Spinneret 下发
SOCKS 代理，还需要 `pip install 'httpx[socks]'`。

### 连接配置

`Settings` 优先使用显式参数，未给出的回落到环境变量。

| 变量 | 参数 | 含义 | 默认值 |
| --- | --- | --- | --- |
| `SPINNERET_URL` | `url` | 服务端基址，如 `https://spinneret.internal`（允许带路径前缀；带查询串、片段或内嵌凭据会被拒绝） | 必填 |
| `SPINNERET_TOKEN` | `token` | 节点 API 令牌（`spn_...`） | 必填 |
| `SPINNERET_NODE` | `node` | 以 `X-Spinneret-Node` 发送的节点名；`[A-Za-z0-9._:@-]` 之外的字符替换为 `-`，截断到 128 字符 | 主机名 |
| `SPINNERET_CACHE_DIR` | `cache_dir` | 配置快照目录 | `~/.spinneret/cache` |

```python
import spinneret

client = spinneret.Client()                                    # 读取环境变量
client = spinneret.Client("https://spinneret.internal", "spn_xxx", node="crawler-a-03")
```

URL 或令牌缺失、非法时，构造函数立即抛出 `ConfigurationError`；构造过程不访问服务端。每次调用都会带上
`Authorization: Bearer <token>`、`X-Spinneret-Node`、`Connect-Protocol-Version: 1` 和
`User-Agent: spinneret-python/0.1.0 httpx/<version>`。

`AsyncClient` 参数完全相同，方法改为 await，另有 `aclose()`。

### 一个完整的节点

`sdk/python/examples/basic_usage.py` 的完整脚本（仅缩短了模块 docstring）：订阅配置项、租借、发请求、上报。

```python
"""Spinneret Python SDK 基础用法。"""

from __future__ import annotations

import json
import logging
import time

import httpx

import spinneret

SITE = "example-site"
CLIENT = "web"
TARGET = "https://target.example.com/api/v1/search?keyword=spinneret"

logger = logging.getLogger("example")


def detect_markers(response: httpx.Response) -> list[str]:
    """识别服务端看不到的响应特征（响应体不会上传）。"""
    markers: list[str] = []
    if "verify" in str(response.url) or "captcha" in response.text[:2048]:
        markers.append("captcha_page")
    try:
        payload = response.json()
    except ValueError:
        return markers
    if isinstance(payload, dict) and not payload.get("data"):
        markers.append("empty_list")
    return markers


def crawl_once(client: spinneret.Client) -> None:
    """租借一个身份，发一次请求，上报结果。"""
    try:
        with client.lease(site=SITE, client=CLIENT, uri=TARGET, session_key="task-8842") as lease:
            with httpx.Client(timeout=15, **lease.httpx_kwargs()) as http:
                try:
                    response = http.get(TARGET)
                except httpx.HTTPError as exc:
                    lease.report_exception(exc)
                    return
            lease.report_response(response, markers=detect_markers(response))
    except spinneret.NoIdentityAvailable as exc:
        logger.info("暂无可用身份，%.1fs 后重试", exc.retry_after or 1.0)
        time.sleep(exc.retry_after or 1.0)
    except (spinneret.CircuitOpen, spinneret.SitePaused) as exc:
        logger.warning("端点已被 Spinneret 暂停（%s），退避", exc.reason)
        time.sleep(exc.retry_after or 30.0)


def main() -> None:
    logging.basicConfig(level=logging.INFO)
    with spinneret.Client() as client:
        watcher = client.config_watcher(["crawler/search.json"], on_change=log_change)
        with watcher:
            item = watcher.get("crawler", "search.json")
            settings = json.loads(item.content) if item is not None else {}
            for _ in range(int(settings.get("requests", 3))):
                crawl_once(client)
        stats = client.reporter.stats
        logger.info("上报 sent=%d dropped=%d", stats.sent, stats.dropped)


def log_change(item: spinneret.ConfigItem) -> None:
    """配置变更回调（切勿打印内容：其中可能含已解析的密钥）。"""
    logger.info("配置 %s/%s 现为版本 %d", item.group, item.key, item.version)


if __name__ == "__main__":
    main()
```

```bash
export SPINNERET_URL=https://spinneret.internal
export SPINNERET_TOKEN=spn_xxx
python sdk/python/examples/basic_usage.py
```

asyncio 写法结构一致：

```python
async with spinneret.AsyncClient() as client:
    async with client.lease(site="example-site", client="web", uri="/api/v1/feed") as lease:
        async with httpx.AsyncClient(**lease.httpx_kwargs()) as http:
            response = await http.get("https://target.example.com/api/v1/feed")
        lease.report_response(response)
```

异步租约上的 `report`、`report_response`、`report_exception` 同样是普通（非阻塞）方法；只有
`renew()`、`release()` 和客户端调用需要 await。

### 租约

```python
client.lease(site, client, uri="", *, endpoint_group="", session_key="", wait_ms=0,
             flush_on_exit=False, raise_on_release_error=False)
```

返回一个上下文管理器，进入时调用 `LeaseService/Acquire`。参数与 RPC 一一对应：`endpoint_group`
优先于 `uri`，`session_key` 驱动会话粘性，`wait_ms`（0..5000）是服务端可以等待空闲身份的时长。

| 成员 | 说明 |
| --- | --- |
| `lease_id`、`identity_id`、`info`、`expires_at`、`hints` | 租约元数据（`info.probe`、`info.sticky`、`info.identity_type`、`info.endpoint_group`、`hints.renew_before_ms`） |
| `credential` | `cookies`、`cookie_header`、`headers`、`query`、`json_value`（JSON 键为 `json`）、`values` |
| `proxy` | `proxy_id`、`url`、`kind`、`region`，无代理时为 `None` |
| `response` | 完整的 `AcquireResponse` |
| `httpx_kwargs(headers=, params=, cookies=)` | 供 `httpx.Client(...)` 使用的 `dict(headers, cookies, params, proxy)` |
| `report(status, *, latency_ms, markers, business_code, error_kind, release, **fields)` | 入队一条上报 |
| `report_response(response, *, markers, business_code, release, **fields)` | 从 `httpx.Response` 生成上报 |
| `report_exception(exc, *, markers, release, **fields)` | 上报失败请求，`error_kind` 由 `classify_exception` 判定 |
| `renew(extend_ms=0)` | 续约（`0` 表示策略的租约 TTL）；异步租约需 `await` |
| `release()` | 立即归还；异步租约需 `await` |
| `acquired`、`released` | 状态标志 |

`httpx_kwargs()` 把凭据和代理合并成客户端参数。凭据的请求头、查询参数和 cookie 先生效，你显式传入的值
覆盖它们（请求头名不区分大小写）。凭据没有 cookie 映射时，`cookie_header` 会作为 `Cookie` 请求头发送；
若同时传了 `cookies=`，则先解析成 cookie 再合并。`proxy` 必须交给客户端构造函数：httpx 不支持逐请求
指定代理。

上下文管理器包装的每个调用也可直接使用：`acquire`、`acquire_batch(site, client, count, ...)`
（1..50 个身份）、`renew`、`release`，以及 `report(reports)`（1..500 条，同步发送）。

### 归还语义

这是手写客户端最容易出错的地方，请完整读一遍。

- 上报会入队到客户端的后台上报器。**最近一条上报会被扣留**，直到下一条上报或代码块结束，以便由它携带
  `release: true`。
- 退出时，最后一条上报带 `release: true` 发出，**代码块抛异常时同样如此**。如果一条都没上报，则用
  `LeaseService/Release` 归还租约；SDK 不会凭空编造上报，所以若希望服务端统计失败请求，请自己调用
  `report_exception`。
- `report(..., release=True)` 立即归还；之后再上报会抛 `LeaseReleased`。
- `flush_on_exit=True` 会等待队列送达（最多 `ReporterOptions.close_timeout`）。
- 归还失败默认不会传播出代码块。原因为 `lease_released`、`lease_unknown` 或 `lease_expired` 的
  `Release` 错误表示租约已经结束，按 debug 级别记录；其他错误按 warning 级别记录。设置
  `raise_on_release_error=True` 后，这些「其他错误」会从 `with` 语句和 `release()` 抛出 —— 除非代码块
  自身已经抛了异常，此时绝不会被替换。
- 一个租约可以服务多次请求（翻页场景）：每次请求都上报，最后一条负责归还。

### 上报

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `report_id` | 随机 UUID4 | 幂等键，`^[A-Za-z0-9_.:-]{1,64}$` |
| `lease_id` | 当前租约 | |
| `uri` | 租借时的 URI | 会被缩减为请求路径，最长 2048 字符 |
| `method` | `""` | 最长 16 字符 |
| `http_status`（参数名 `status=`） | `0` | 0..999；未收到响应时为 0 |
| `business_code` | `""` | 数字会被接受并转成字符串，最长 64 字符 |
| `error_kind` | `""` | 必须为空或属于[错误类别](#错误类别) |
| `markers` | `[]` | 最多 32 个标记，每个 1..64 字符 |
| `outcome_hint` | `""` | 最长 32 字符；判定结果由信号规则决定，这里只是建议 |
| `latency_ms` | 推导 | 省略时由 `finished_at - started_at` 得出 |
| `response_bytes` | `0` | |
| `started_at` | `finished_at - latency_ms` | |
| `finished_at` | 当前时间 | |
| `release` | `false` | |

上报的 `uri` 总会被缩减为请求路径：去掉查询串和片段，截断到 2048 字符。端点组只按路径匹配，而查询参数
经常携带带签名的凭据值，不应进入请求分析数据。`407` 响应会带 `error_kind="proxy_auth"` 上报。

**注意。** 只用 `endpoint_group`、不带 `uri` 租借时，上报 URI 会为空。SDK 自己的 `Report` 模型要求它非空
（`min_length=1`），所以 `report()` 会在本地抛出 `pydantic.ValidationError`，什么都不会入队；服务端同样
会拒绝。这种情况请在 `report()` 中显式传入 `uri=`。

### 后台上报器

`client.reporter` 是 `Reporter`（守护线程）；在 `AsyncClient` 上则是 `AsyncReporter`（asyncio 任务）。
两者都把上报批量打包进 `ReportService/Report` 调用。

| 选项 | 默认值 | 含义 |
| --- | --- | --- |
| `flush_interval` | `0.2` | 一条上报在发送前最多等待的秒数 |
| `batch_size` | `100` | 触发立即发送的队列长度 |
| `max_batch_size` | `500` | 单次调用的上报数上限（服务端限制） |
| `max_queue_size` | `10_000` | 队列上限；超出后丢弃最旧的上报 |
| `backoff` | `Backoff(initial=0.5, maximum=30.0)` | 发送失败之间的退避 |
| `close_timeout` | `5.0` | `close()` 用于投递队列的秒数 |
| `on_rejected` | `None` | 服务端拒绝每条上报时的回调 |

```python
options = spinneret.ReporterOptions(
    flush_interval=0.2,
    batch_size=100,
    max_queue_size=10_000,
    on_rejected=lambda r: log.warning("被拒绝 %s: %s", r.report_id, r.reason),
)
client = spinneret.Client(reporter_options=options)
```

- 传输错误以及 `unavailable`、`internal`、`deadline_exceeded`、`aborted`、`resource_exhausted`
  响应会以带抖动的指数退避重试；`circuit_open` 和 `site_paused` 不重试。其他失败直接丢弃整批。
- 被服务端拒绝的上报会被丢弃、记录日志，并传给 `on_rejected`。
- `flush(timeout)` 立即发送队列；`close(timeout)`（`client.close()` 也会调用）尽力投递后停止。同步
  上报器还会在解释器退出时关闭；asyncio 场景请始终 `await client.aclose()`。
- 若工作者已不再运行（`os.fork()` 产生的子进程中的线程，或事件循环已结束的任务），下一次 `submit`、
  `flush` 或 `close` 会将其重启。
- `client.reporter.stats` 返回 `submitted`、`sent`、`accepted`、`duplicated`、`rejected`、
  `dropped`、`failed_sends` 和 `queued`。请把 `dropped` 接入自己的监控：它是节点正在丢失上报的唯一信号。

### 配置中心

```python
def on_change(item: spinneret.ConfigItem) -> None:
    reload_settings(item.content)


with client.config_watcher(
    ["crawler/search.json", ("_runtime", "breakers")], on_change=on_change
) as watcher:
    item = watcher.get("crawler", "search.json")
    changed = watcher.wait_for_change("crawler", "search.json", timeout=60)
```

配置项可写成 `"group/key"`、`(group, key)` 或 `ConfigKey(group=..., key=...)`；每个订阅者 1..200 项。

- `start()` 用 `BatchGetConfig` 加载配置项；随后由线程（或 asyncio 任务）长轮询 `WatchConfig`，
  `timeout_ms` 为 30 000（HTTP 读超时为 `timeout_ms + 5 s`），保存最新值并对每个变更项调用回调，
  包括首次取到的初始值。订阅者停止或客户端关闭时循环结束。
- 每个成功取回的非密钥配置项都会原子写入（临时文件、`fsync`、rename，文件权限 `0600`、目录 `0700`）到
  `<cache_dir>/<host>/<namespace>/<group>/<key>.json`，各段做百分号编码。订阅者未设置命名空间时，
  `<namespace>` 就是字面量 `_token_namespace` —— 这是常态，因为节点令牌本身已经隐含了命名空间。
- 启动时服务端不可用（传输错误、`unavailable`、`internal`、`deadline_exceeded`、
  `resource_exhausted` 或其他 5xx），则改为加载本地快照，`watcher.from_snapshot` 保持 `True` 直到
  服务端响应为止。鉴权类和校验类错误会由 `start()` 抛出。
- **含密钥的配置项绝不会明文落盘。** 服务端在下发前解析 `${secret:...}` 引用，并为所有发布内容含此类
  引用的配置项设置 `has_secret_refs`；订阅者只把这类项保存在内存中，并删除已有的旧快照。内容中仍残留
  未解析 `${secret:` 标记的也按密钥处理。
- `treat_as_secret` 是额外的兜底判定，用于那些直接内嵌敏感值而非引用密钥的配置项，例如
  `treat_as_secret=lambda item: item.group == "signing"`。它只能增加密钥项，不能豁免服务端已标记的项；
  判定函数抛异常时视为 true。
- `cache_secrets=True` 会用 AES-256-GCM 加密写入密钥项，密钥由 API 令牌经 HKDF-SHA256 派生（每个文件
  使用新的 salt 和 nonce）。这需要 `cryptography` 包（`spinneret[crypto]`）；缺少它时
  `cache_secrets=True` 会抛 `ConfigurationError`。令牌轮换后，用旧令牌加密的快照无法读取。
- `snapshots=False` 关闭缓存；`cache_dir=` 覆盖目录。

其他成员：`items()`、`add_listener()`、`remove_listener()`、`running`、`snapshot_store`、
`stop(timeout)`。

不使用订阅者的一次性读取：`get_config(group, key)`、`batch_get_config(items)`、
`watch_config(items, timeout_ms=30_000)`。

### 密钥

```python
secret = client.get_secret("signing/api_key")          # version=0 读取当前版本
value = secret.value                                    # 在 repr 中被隐藏
pinned = client.get_secret("signing/api_key", version=3)
```

`GetSecretResponse` 含 `path`、`version`、`value` 和 `expires_at`。在轮换周期允许的范围内可以缓存在
内存中，但绝不要落盘。详见[密钥保管库](./10-secrets.md)。

### 错误

所有错误都继承 `SpinneretError`，带 `code`、`reason`、`message`、`retry_after_ms`
（`retry_after` 为秒）和 `http_status`。

| 异常 | 代码 / 原因 | 节点应当怎么做 |
| --- | --- | --- |
| `Unauthenticated` | `unauthenticated`（`token_invalid`、`token_expired`、`token_revoked`、`ip_not_allowed`） | 停机并告警 |
| `PermissionDenied` | `permission_denied`（`scope_missing`） | 停机并告警 |
| `InvalidArgument` | `invalid_argument`（`site_unknown`、`client_unknown`、`uri_invalid` 等） | 修调用方 |
| `NoIdentityAvailable` / `NoProxyAvailable` | `resource_exhausted` | 等待 `retry_after` 后重试 |
| `ResourceExhausted` | `resource_exhausted`（`rate_limited` 等） | 等待 `retry_after` |
| `CircuitOpen` / `SitePaused` | `unavailable` | 暂停该端点组 / 站点 |
| `Unavailable` | `unavailable`（`rebuilding` 等） | 稍后重试 |
| `LeaseUnknown` | `not_found`（`lease_unknown`） | 重新租借 |
| `LeaseReleased` / `LeaseExpired` | `failed_precondition` | 重新租借 |
| `NotFound`、`AlreadyExists`、`Aborted`、`DeadlineExceeded`、`Unimplemented`、`InternalError` | 对应代码 | |
| `TransportError`（`Unavailable` 的子类） | 无响应；带 `error_kind` | 稍后重试 |
| `ConfigurationError` | SDK 配置无效 | 修配置 |
| `ReporterClosedError` | 关闭后仍提交上报 | |
| `FailedPrecondition`（原因 `client_closed`） | 在已关闭的客户端上调用 | 新建客户端 |

没有 Connect 错误体的响应（例如负载均衡器返回的页面）按 Connect 协议从 HTTP 状态映射
（401 → `unauthenticated`，403 → `permission_denied`，404 → `unimplemented`，
429/502/503/504 → `unavailable`）。

### 重试与超时

- 超时（`spinneret.Timeouts`）：连接 3 s，读取 10 s（acquire 另加 `wait_ms`），写入 10 s，连接池
  10 s，长轮询另加 `watch_grace` 5 s。
- `RetryPolicy(max_retries=2, backoff=Backoff(0.1, 2.0, 2.0), max_retry_after=5.0)`：传输错误和
  `unavailable` 响应以带抖动的指数退避重试；`circuit_open` 和 `site_paused` 永不重试；服务端重试提示
  超过 `max_retry_after` 时直接抛错而不等待。
- `Acquire` 和 `AcquireBatch` 不是幂等的：只有在请求可证明从未到达服务端（连接被拒、连接超时、连接池
  超时），或服务端明确回答 `unavailable` 时才重试。含糊的失败（读超时、连接被重置）一律抛出。
- `overloaded`（`unavailable`）表示服务端已到自己的租借并发上限，在真正尝试之前就把这次调用甩掉了。
  它没有发出任何 Redis 命令，因此与其他 `unavailable` 一样可以重试：重试会遵守
  `Spinneret-Retry-After-Ms`，服务端会把它抖动到 100–200 毫秒。它不是 `no_identity_available`——
  身份池根本没有被查询——并且不需要 SDK 做任何改动。
- `spinneret.NO_RETRY` 关闭重试。

### 错误类别

`spinneret.classify_exception(exc)` 把请求异常映射为上报的 `error_kind`：

| 取值 | 含义 |
| --- | --- |
| `timeout` | 请求未在时限内完成 |
| `conn_reset` | 连接被重置、中止，或响应中途断开 |
| `conn_refused` | 连接被拒绝，主机或网络不可达 |
| `proxy_auth` | 代理拒绝凭据（`407` 响应同样如此） |
| `tls` | TLS 握手或证书失败 |
| `dns` | 域名解析失败 |
| `other` | 其他 |

它能识别 httpx 异常（含其 cause 链）、socket/SSL 错误和常见错误信息。`httpx.HTTPStatusError` 返回
`""` —— 应改为上报其状态码 —— 但 `407` 除外。

### 进程与事件循环

请在**派生工作进程之后**创建客户端，例如在 gunicorn 或 uvicorn 的 worker 启动钩子中：`httpx` 的连接池
不能跨进程共享。`AsyncClient` 属于它所在的那个事件循环，每个循环创建一个。

SDK 通过 `logging.getLogger("spinneret")` 记录日志（子 logger 为 `spinneret.reporter`、
`spinneret.lease`、`spinneret.config`），并已安装 `NullHandler`。它绝不记录令牌、凭据、代理 URL、
配置内容和密钥值，相应模型的 `repr` 也会隐藏这些字段。

### API 索引

| 符号 | 类型 | 用途 |
| --- | --- | --- |
| `Client` / `AsyncClient` | 类 | 节点 API 客户端；`acquire`、`acquire_batch`、`renew`、`release`、`lease`、`report`、`get_config`、`batch_get_config`、`watch_config`、`config_watcher`、`get_secret`、`close` / `aclose` |
| `ManagedLease` / `AsyncManagedLease` | 类 | 上下文管理的租约（见[租约](#租约)） |
| `Reporter` / `AsyncReporter` | 类 | 后台批量上报；`submit`、`flush`、`close`、`stats`、`options`、`closed` |
| `ConfigWatcher` / `AsyncConfigWatcher` | 类 | 长轮询订阅循环；`start`、`stop`、`get`、`items`、`wait_for_change`、`add_listener`、`remove_listener`、`from_snapshot`、`running`、`snapshot_store` |
| `Settings` | dataclass | `url`、`token`、`node`、`cache_dir`、`host`、`from_env()` |
| `Timeouts` | dataclass | `connect`、`read`、`write`、`pool`、`watch_grace` |
| `RetryPolicy`、`Backoff`、`NO_RETRY` | dataclass / 常量 | 一元调用的重试参数 |
| `ReporterOptions`、`ReporterStats` | dataclass | 上报器参数与计数 |
| `WatchOptions` | dataclass | 订阅循环的 `timeout_ms`、`backoff` |
| `SnapshotStore`、`SecretPredicate`、`ChangeCallback`、`AsyncChangeCallback`、`ConfigKeyLike` | 类 / 类型 | 快照布局、密钥判定函数、两种变更回调的签名，以及哪些写法算配置项键（`"group/key"`、`(group, key)` 或 `ConfigKey`） |
| `classify_exception` | 函数 | 异常 → `error_kind` |
| `ErrorKind`、`Outcome`、`SECRET_REF_MARKER` | 常量 | 上报 `error_kind` 取值、判定结果类别、`${secret:` 标记 |
| `AcquireRequest`、`AcquireResponse`、`Lease`、`Credential`、`Proxy`、`Hints`、`Report`、`ReportResponse`、`RejectedReport`、`ConfigItem`、`ConfigKey`、`WatchItem`、`GetSecretResponse` 等 | pydantic 模型 | 每个节点 API 消息一个；不可变，忽略未知字段 |
| `SpinneretError` 及其子类 | 异常 | 见[错误](#错误) |

开发：

```bash
cd sdk/python
python3 -m venv .venv && . .venv/bin/activate
pip install -e '.[dev]'
pytest -q --cov=spinneret
ruff check . && ruff format --check .
mypy src
```

测试使用 `respx` 和 `httpx.MockTransport`，不访问网络。

---

## Go SDK

源码：`sdk/go/spinneret`。版本 0.1.0。要求 Go 1.27+。运行期依赖只有 `connectrpc.com/connect` 和
`google.golang.org/protobuf`。

### 安装

该包位于主模块中：

```bash
go get github.com/TikHub/Spinneret@latest
```

```go
import "github.com/TikHub/Spinneret/sdk/go/spinneret"
```

### 选项

`spinneret.New(spinneret.Options{...})` 校验选项，不访问服务端。`BaseURL`、`Token` 和 `Node` 为空时
回落到环境变量。

| 选项 | 变量 | 含义 | 默认值 |
| --- | --- | --- | --- |
| `BaseURL` | `SPINNERET_URL` | 服务端 URL，如 `https://spinneret.internal`（允许带路径前缀） | 必填 |
| `Token` | `SPINNERET_TOKEN` | 节点 API 令牌（`spn_...`） | 必填 |
| `Node` | `SPINNERET_NODE` | 以 `X-Spinneret-Node` 发送的节点名，清洗为 `[A-Za-z0-9._:@-]` 且不超过 128 字符 | 主机名 |
| `UseGRPC` | | 使用 gRPC（二进制 protobuf、HTTP/2）而非 Connect JSON | `false` |
| `Timeout` | | 一元调用每次尝试的时限 | `10s` |
| `HTTPClient` | | 自定义 `connect.HTTPClient`（TLS 根证书、出网代理等） | 已调优的 `http.Client` |
| `Retry` | | `*RetryPolicy`；`spinneret.NoRetry()` 关闭重试 | 重试 2 次 |
| `Reporter` | | `client.Reporter()` 的 `ReporterOptions` | 见下文 |
| `Logger` | | `*slog.Logger` | `slog.Default()` |
| `UserAgent` | | 前置于 `spinneret-go/0.1.0` | |

```go
client, err := spinneret.New(spinneret.Options{
	BaseURL: "https://spinneret.internal",
	Token:   os.Getenv("SPINNERET_TOKEN"),
	Node:    "crawler-a-03",
})
if err != nil {
	return err
}
defer client.Close(context.Background()) // 投递队列中的上报
```

默认 HTTP 客户端使用 3 s 拨号超时，遵守 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`，在 TLS 上协商 HTTP/2；
启用 `UseGRPC` 时对 `http://` URL 使用明文 HTTP/2（h2c）。自定义客户端的 `http.Client.Timeout` 不得
小于 `WatchConfig` 的等待时长（默认 35 s）：逐调用的时限是通过请求 context 施加的。

### 一个完整的节点

`sdk/go/examples/basic/main.go` 的核心 —— 租借、经代理发请求、上报：

```go
// crawlOnce 租借一个身份，发一次请求，并上报结果。
func crawlOnce(ctx context.Context, client *spinneret.Client, cfg config, logger *slog.Logger) error {
	lease, err := client.Lease(ctx, &spinneret.AcquireRequest{
		Site:   cfg.site,
		Client: cfg.client,
		Uri:    cfg.target,
		WaitMs: 500,
	})
	switch {
	case spinneret.IsNoIdentity(err), spinneret.IsNoProxy(err):
		sleep(ctx, waitFor(err, time.Second)) // 等待容量
		return nil
	case spinneret.IsCircuitOpen(err), spinneret.IsSitePaused(err):
		sleep(ctx, waitFor(err, 30*time.Second)) // 端点组或整个站点被关停
		return nil
	case err != nil:
		return fmt.Errorf("acquire: %w", err)
	}
	defer func() {
		// 即使 ctx 已被信号取消，也要归还租约。
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		if err := lease.Close(closeCtx); err != nil {
			logger.Warn("release", slog.String("lease_id", lease.ID()), slog.String("error", err.Error()))
		}
	}()

	transport, err := lease.Transport(nil) // http.DefaultTransport 的副本，走租到的代理
	if err != nil {
		return err
	}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: requestTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.target, nil)
	if err != nil {
		return err
	}
	lease.Apply(req) // 凭据中的 cookie、请求头和查询参数
	started := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return lease.ReportError(err, spinneret.ReportInput{StartedAt: started})
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return lease.ReportError(err, spinneret.ReportInput{StartedAt: started, HTTPStatus: resp.StatusCode})
	}
	return lease.ReportResponse(resp, spinneret.ReportInput{
		StartedAt:     started,
		ResponseBytes: int64(len(body)),
		Markers:       detectMarkers(body), // 例如 "captcha_page"、"empty_list"
	})
}
```

运行完整示例：

```bash
export SPINNERET_URL=http://localhost:8080 SPINNERET_TOKEN=spn_xxx
go run ./sdk/go/examples/basic \
  -site example-site -client web \
  -target https://target.example.com/api/v1/search \
  -config crawler/search.json
```

参数：`-site`、`-client`、`-target`、`-requests`（默认 3）、`-config`（要订阅的 `group/key`，可选）、
`-grpc`。

### 协议

| | Connect JSON（默认） | gRPC（`UseGRPC: true`） |
| --- | --- | --- |
| 传输 | HTTP/1.1 或 HTTP/2 | 仅 HTTP/2（TLS 上走 ALPN，`http://` 走 h2c） |
| 编码 | JSON，snake_case 字段名；忽略响应中的未知字段 | 二进制 protobuf |
| 错误原因 / 重试提示 | 响应头 | 响应 trailer |

两种协议产生完全相同的 `*spinneret.Error`。使用 gRPC 时负载均衡器必须端到端透传 HTTP/2，而 Compose 中的
负载均衡器做不到：`deploy/compose/config/Caddyfile` 给 `reverse_proxy` 配的 `http` transport 没有写
`versions h2c 2`，因此 Caddy 会终止客户端的 HTTP/2，改用 HTTP/1.1 转发给明文的 `spinneret:8080` 上游。
也就是说栈的 8080 端口只能走 Connect JSON。要在 Compose 栈上使用 `UseGRPC: true`，要么在该 transport
块中加上 `versions h2c 2`，要么直连某个实例 —— `spinneret` 服务没有映射宿主机端口，需要你自己映射一个。
这也是 live 测试要单独提供 `SPINNERET_LIVE_GRPC_URL` 的原因。

### 租约

`client.Lease(ctx, *AcquireRequest)` 调用 `Acquire` 并返回 `*Lease`；
`client.LeaseBatch(ctx, *AcquireBatchRequest)` 包装 `AcquireBatch` 的每个租约；
`client.NewLease(resp, uri)` 包装从别处取得的响应。`*Lease` 可并发使用。

| 成员 | 说明 |
| --- | --- |
| `ID()`、`IdentityID()`、`Info()`、`Hints()`、`Response()`、`URI()` | 租约元数据（`Info().GetProbe()`、`GetSticky()`、`GetIdentityType()`、`GetEndpointGroup()`） |
| `ExpiresAt()`、`Released()` | 过期时间（`Renew` 后更新）与状态 |
| `Credential()` | `cookies`、`cookie_header`、`headers`、`query`、`json`、`values` |
| `Proxy()`、`ProxyURL()` | 分配到的代理（无则为 `nil`）；URL 内含凭据，切勿打印 |
| `Transport(base)` | `base`（或 `http.DefaultTransport`）的副本，走该代理 |
| `Apply(req)` | 把凭据合并进 `*http.Request` |
| `Report(ReportInput)` | 入队一条上报 |
| `ReportResponse(resp, ReportInput)` | 从 `*http.Response` 生成上报（状态、方法、路径、`Content-Length`；407 置 `proxy_auth`） |
| `ReportError(err, ReportInput)` | 上报失败请求；`error_kind` 由 `ClassifyError` 判定，方法和路径取自 `*url.Error` |
| `Renew(ctx, extend)` | 续约（`0` 表示策略的租约 TTL） |
| `Close(ctx)` | 归还租约（幂等） |

归还语义与 Python SDK 一致：

- 上报入队到 `client.Reporter()`。最近一条上报被扣留到下一条上报或 `Close`，以便携带 `release: true`。
- `Close` 会把扣留的那条以 `release: true` 发出。一条都没上报时则调用 `LeaseService/Release`；SDK 不会
  凭空编造上报，所以若希望服务端统计失败请求，请自己调用 `ReportError`。
- `Close` 返回 `Release` 调用的错误，但表示租约已结束的原因除外（`lease_released`、`lease_unknown`、
  `lease_expired`、`lease_lifetime_exceeded`）。若上报器已关闭，最后一条上报会直接同步投递。
- `ReportInput{Release: true}` 立即归还；之后的上报会以 `IsLeaseGone(err)` 失败。
- 一个租约可以服务多次请求：每次请求都上报，由 `Close` 归还。
- `client.Reporter().Flush(ctx)` 等待队列中的上报送达。

### 凭据

`Apply(req)`（以及自由函数 `spinneret.ApplyCredential(req, cred)`）绝不覆盖请求上已有的内容：

- 请求头仅在请求尚无该头时设置（跳过 `Host` —— net/http 从 URL 取主机）；
- cookie 合并进单个 `Cookie` 请求头，跳过请求已带的同名 cookie；若凭据没有 cookie 映射且请求也没有
  cookie，则原样发送 `cookie_header`，不做重新编码；
- 查询参数仅在缺失时追加，并逐字节保留已有查询串，确保带签名的参数依然有效。

`credential.GetValues().AsMap()` 和 `credential.GetJson().AsInterface()` 用于取出结构化值，例如设备
参数或请求体片段。`http.Transport` 支持 `http://`、`https://` 和 `socks5://` 代理 URL。

### 上报

`ReportInput` 字段：`HTTPStatus`、`Method`、`URI`、`BusinessCode`、`ErrorKind`、`Markers`、
`OutcomeHint`、`Latency`、`ResponseBytes`、`StartedAt`、`FinishedAt`、`ReportID`、`Release`。

- `ReportID` 默认为随机 UUID，`FinishedAt` 默认为当前时间，`StartedAt` 默认为
  `FinishedAt - Latency`，`Latency` 默认为 `FinishedAt - StartedAt`。
- `URI` 默认取租借时的 URI，并被缩减为请求路径（去掉查询串和片段，最长 2048 字符）。只用
  `endpoint_group` 租借的租约必须显式给出 `URI`。
- 输入按服务端的规则本地校验（`error_kind` 取值、最多 32 个 1..64 字符的标记、状态码 0..999、
  `report_id` 模式、延迟非负等）。输入非法时返回 `invalid_argument` 错误，且不会入队任何内容。
- `ReportResponse` 看不到分块响应体的大小：读完响应体后自行传入 `ResponseBytes`。

### 后台上报器

`client.Reporter()`（或 `spinneret.NewReporter(send, options, logger)`）：

| 选项 | 默认值 | 含义 |
| --- | --- | --- |
| `FlushInterval` | `200ms` | 一条上报在发送前最长等待时间 |
| `BatchSize` | `100` | 触发立即发送的队列长度 |
| `MaxBatchSize` | `500` | 单次调用的上报数上限（服务端限制） |
| `MaxQueueSize` | `10000` | 队列上限；超出后丢弃最旧的上报 |
| `InitialBackoff` | `500ms` | 发送失败后的首个延迟 |
| `MaxBackoff` | `30s` | 失败投递之间延迟的上限 |
| `CloseTimeout` | `5s` | context 无 deadline 时 `Close` 的时限 |
| `OnRejected` | `nil` | 每条被拒上报的回调，在上报器 goroutine 上执行 |

```go
client, err := spinneret.New(spinneret.Options{
	Reporter: spinneret.ReporterOptions{
		FlushInterval: 200 * time.Millisecond,
		BatchSize:     100,
		MaxQueueSize:  10_000,
		OnRejected: func(r *spinneret.RejectedReport) {
			log.Printf("rejected %s: %s", r.GetReportId(), r.GetReason())
		},
	},
})
stats := client.Reporter().Stats() // Submitted、Sent、Accepted、Duplicated、Rejected、Dropped、FailedSends、Queued
```

满足 `IsRetryable(err)` 的失败 —— 传输错误、`unavailable`（`circuit_open` 和 `site_paused` 除外）、
`internal`、`unknown`、`data_loss`、`deadline_exceeded`、`aborted`、`resource_exhausted` —— 会以带
抖动的指数退避重试，且至少等待服务端给出的重试提示。其他失败直接丢弃整批。`Submit` 不会阻塞在网络上，
并会在 `report_id`、`finished_at`、`started_at` 为空时补齐。`Close(ctx)`（由 `client.Close` 调用）在
`ctx` 结束前尽力投递，随后中断进行中的调用；若有上报被丢弃则返回错误。

### 配置中心

```go
watcher, err := client.NewConfigWatcher(spinneret.WatcherOptions{
	Items:       []spinneret.ConfigKey{{Group: "crawler", Key: "search.json"}, {Group: "_runtime", Key: "breakers"}},
	SnapshotDir: "/var/cache/spinneret",
	OnChange:    func(item *spinneret.ConfigItem) { reload(item.GetContent()) },
})
if err != nil {
	return err
}
if err := watcher.Start(ctx); err != nil { // ctx 结束、Stop 或 client.Close 时循环停止
	return err
}
item, ok := watcher.Get("crawler", "search.json")
versions := watcher.Versions()
changed, err := watcher.WaitForChange(ctx, "crawler", "search.json")
```

`WatcherOptions`：`Items`（1..200）、`Namespace`、`Timeout`（默认 30 s，最大 60 s）、`SnapshotDir`、
`TreatAsSecret`、`OnChange`、`InitialBackoff`（1 s）、`MaxBackoff`（30 s）。
`spinneret.ParseConfigKey("group/key")` 可从字符串构造 `ConfigKey`。

- `Start` 用 `BatchGetConfig` 加载配置项，并启动一个 goroutine，带着已知版本长轮询 `WatchConfig`，
  保存最新值并对每次变更调用监听器，包括初始值。监听器按顺序执行，不得长时间阻塞，且必须把配置项视为
  只读；监听器 panic 会被记录，但不会让订阅者停止。`Stop` 取消进行中的长轮询。
- 一次长轮询若在 200 ms 内就返回且没有任何变更，循环会额外睡 200 ms（`watchMinPollInterval`），这样中间
  设备配置错误、立即返回长轮询时也不会把订阅变成空转。调小 `Timeout` 时要记得这一点。
- 轮询失败按退避重试。启动时服务端不可用（`IsRetryable` 类错误）则改为加载快照，`FromSnapshot()` 保持
  `true` 直到服务端响应。鉴权类和校验类错误由 `Start` 返回，之后可以再次调用 `Start`。
- 配置 `SnapshotDir` 后，每个取回的配置项都会原子写入（临时文件、`fsync`、rename；文件 `0600`、目录
  `0700`）到 `<dir>/<host>/<namespace>/<group>/<key>.json`，各段做百分号编码 —— 布局和格式与 Python
  SDK 完全一致。`Namespace` 为空时（常态），`<namespace>` 就是字面量 `_token_namespace`。
- 密钥材料绝不落盘：带 `has_secret_refs` 的项、内容中仍含 `${secret:` 的项，以及被 `TreatAsSecret`
  命中的项只保存在内存中，并删除它们已有的旧快照。`TreatAsSecret` 只能增加密钥项；判定函数 panic 时
  视为 true。Python SDK 写出的加密快照会被忽略。

其他成员：`Keys()`、`Items()`、`AddListener()`、`Done()`。

一次性读取：`GetConfig`、`BatchGetConfig` 和 `WatchConfig`（`timeout_ms` 为 0 表示 30 s）。

### 密钥

```go
secret, err := client.GetSecret(ctx, &spinneret.GetSecretRequest{Path: "signing/api_key"})
if err != nil {
	return err
}
value := secret.GetValue()

pinned, err := client.GetSecret(ctx, &spinneret.GetSecretRequest{
	Path:    "signing/api_key",
	Version: 3,
})
```

`Path` 相对于令牌所属的命名空间，最长 256 字符，且必须匹配 `^[a-z0-9][a-z0-9_./-]*$`。`Version: 0`
读取当前版本。令牌需要一个覆盖 `<namespace>/<path>` 的 `secret:read` 权限范围，且每次读取都会写入审计日志。
`GetSecretResponse` 含 `path`、`version`、`value` 和 `expires_at`（不过期时为 `nil`）。在轮换周期允许的
范围内可以缓存在内存中，但绝不要落盘。详见[密钥保管库](./10-secrets.md)。

### 错误

每个调用都返回 `*spinneret.Error`（用 `spinneret.AsError(err)` 或 `errors.As` 取出）：

| 字段 / 方法 | 含义 |
| --- | --- |
| `Code` | `connect.Code`，如 `connect.CodeResourceExhausted` |
| `Reason` | `Spinneret-Reason`，如 `no_identity_available`；未收到响应时为 `transport` |
| `Message` | 服务端消息 |
| `RetryAfter` | `Spinneret-Retry-After-Ms`，以 `time.Duration` 表示（缺失时为 0） |
| `Procedure` | 失败的 RPC |
| `ErrorKind`、`Transport()` | 传输失败的分类 |
| `FromServer()` | 错误确实由服务端发出（而非客户端从传输失败或裸 HTTP 状态合成） |
| `Unwrap()` | 底层 `*connect.Error` / `*url.Error` |

| 辅助函数 | 代码 / 原因 | 节点应当怎么做 |
| --- | --- | --- |
| `IsUnauthenticated` | `unauthenticated`（`token_invalid` 等） | 停机并告警 |
| `IsPermissionDenied` | `permission_denied`（`scope_missing`） | 停机并告警 |
| `CodeOf(err) == connect.CodeInvalidArgument` | `site_unknown`、`uri_invalid` 等 | 修调用方 |
| `IsNoIdentity`、`IsNoProxy` | `resource_exhausted` | 等待 `RetryAfterOf(err)` 后重试 |
| `IsCircuitOpen`、`IsSitePaused` | `unavailable` | 暂停该端点组 / 站点 |
| `IsLeaseGone` | `lease_unknown`、`lease_released`、`lease_expired`、`lease_lifetime_exceeded` | 重新租借 |
| `IsTransport` | 原因 `transport` | 稍后重试 |
| `IsRetryable` | 见上报器一节 | 后台投递稍后重试 |
| `ReasonOf(err) == spinneret.ReasonClientClosed` | `failed_precondition` | 新建客户端 |

没有 Connect 错误体的响应保留由 HTTP 状态推导出的代码，原因为空。该包把每个原因都导出为常量
（`spinneret.ReasonNoIdentityAvailable`、`spinneret.ReasonCircuitOpen` 等）。

### 重试与超时

- 每次尝试受 `Timeout`（10 s）约束；`Acquire`/`AcquireBatch` 另加 `wait_ms`；`WatchConfig` 使用
  `timeout_ms`（为 0 时按 30 s）再加 5 s。`ctx` 上更短的 deadline 优先。发给服务端的 deadline 比客户端
  超时晚一秒，这样超时的尝试总会被判定为传输超时，而不会与由同一 deadline 引发的 `deadline_exceeded`
  回答赛跑。
- `RetryPolicy{MaxRetries: 2, InitialBackoff: 100ms, MaxBackoff: 2s, MaxRetryAfter: 5s}`：传输失败和
  `unavailable` 回答以带抖动的指数退避重试；`circuit_open` 和 `site_paused` 永不重试；服务端提示超过
  `MaxRetryAfter` 时直接返回错误而不等待；证书错误永不重试。
- `Acquire` 和 `AcquireBatch` 不是幂等的：只有在失败可证明发生在请求发出之前（拨号和 DNS 错误），或
  服务端自己回答 `unavailable` 时才重试。含糊的失败（逐调用超时、连接被重置、负载均衡器返回的裸
  502/503/504）一律返回。
- `overloaded`（`unavailable`）表示服务端已到自己的租借并发上限，在真正尝试之前就把这次调用甩掉了。
  它没有发出任何 Redis 命令，因此与其他 `unavailable` 一样可以重试：重试会遵守
  `Spinneret-Retry-After-Ms`，服务端会把它抖动到 100–200 毫秒。它不是 `no_identity_available`——
  身份池根本没有被查询——并且不需要 SDK 做任何改动。
- 后台上报器和配置订阅者不使用该策略，它们各自用自己的退避重试。

`spinneret.ClassifyError(err)` 把请求错误映射为与 Python SDK 相同的 `error_kind` 取值（`timeout`、
`conn_reset`、`conn_refused`、`proxy_auth`、`tls`、`dns`、`other`；`nil` 返回 `""`）。它能识别
`*url.Error` 及其包装的 `net`、`syscall`、`crypto/tls`、`crypto/x509` 错误、HTTP 代理 `CONNECT` 被拒
（`Proxy Authentication Required`）和 SOCKS5 认证失败，并在最后回落到错误消息匹配。普通 HTTP 代理的
`407` 是一个响应而不是错误：用 `ReportResponse` 上报，它会自动置 `proxy_auth`。

### API 索引

| 符号 | 类型 | 用途 |
| --- | --- | --- |
| `New`、`Options`、`Client` | 函数 / 结构体 | 客户端构造与节点 RPC：`Acquire`、`AcquireBatch`、`Renew`、`Release`、`Report`、`GetConfig`、`BatchGetConfig`、`WatchConfig`、`GetSecret`、`Lease`、`LeaseBatch`、`NewLease`、`NewConfigWatcher`、`Reporter`、`Close`；访问器 `BaseURL()`、`Node()`、`Logger()`、`Closed()` |
| `Lease`、`ReportInput` | 结构体 | 租约辅助与上报构建 |
| `ApplyCredential`、`ParseCookieHeader`、`ProxyURL`、`ReportURI`、`NewReportID` | 函数 | 凭据合并、把 `Cookie` 请求头解析为「名 → 值」映射、代理 URL、URI 缩减、幂等键 |
| `Reporter`、`ReporterOptions`、`ReporterStats`、`NewReporter`、`SendFunc` | 结构体 / 函数 | 后台批量上报 |
| `ConfigWatcher`、`WatcherOptions`、`ConfigKey`、`ParseConfigKey` | 结构体 / 函数 | 长轮询订阅循环 |
| `Error`、`AsError`、`CodeOf`、`ReasonOf`、`RetryAfterOf`、`Is*` | 结构体 / 函数 | 类型化错误 |
| `RetryPolicy`、`DefaultRetryPolicy`、`NoRetry` | 结构体 / 函数 | 重试参数 |
| `ClassifyError`、`ErrorKind*` | 函数 / 常量 | 错误类别 |
| `SanitizeNodeName`、`DefaultNodeName`、`Version`、`Env*`、`Header*`、`Reason*`、`Max*` | 函数 / 常量 | 节点命名、限额、请求头与原因常量 |
| `AcquireRequest`、`AcquireResponse`、`LeaseInfo`、`Credential`、`ProxyAssignment`、`Hints`、`Report`、`ReportResponse`、`RejectedReport`、`ConfigItem`、`ConfigRef`、`WatchItem`、`GetSecretResponse` 等 | 类型别名 | 生成的消息类型再导出，调用方无需 import 生成包 |
| `LeaseService()`、`ReportService()`、`ConfigService()`、`SecretService()` | 方法 | 底层生成的客户端：带鉴权，但不做重试和错误转换 |

开发：

```bash
go vet ./sdk/go/...
go test -race -count=1 -cover ./sdk/go/...
golangci-lint run ./sdk/go/...
```

单元测试用 `httptest` 服务端配合 fake 提供生成的 handler，覆盖 Connect JSON 和 gRPC（h2c），不访问外部
网络。Live 测试针对真实部署运行，例如带 mock target 的 Compose 栈：

```bash
export SPINNERET_LIVE_ADMIN_PASSWORD=...   # 该栈的管理员密码
go test -tags live -race -count=1 -run TestLive ./sdk/go/spinneret
```

它会创建名为 `gosdk-<run>` 的命名空间，包含站点、身份、令牌、配置项和密钥，在两种协议下执行，最后全部
删除。`SPINNERET_LIVE_URL`、`SPINNERET_LIVE_TARGET`、`SPINNERET_LIVE_GRPC_URL`、
`SPINNERET_LIVE_TENANT` 和 `SPINNERET_LIVE_ADMIN_USER` 可覆盖默认值。

---

## 示例爬虫

`examples/fastapi-crawler` 是一个小而完整的节点：前面是 FastAPI，后面是 Python SDK。它是不写一行代码
就看到整条链路 —— 租借、经代理请求、上报、Spinneret 作出反应 —— 的最快方式。

对该爬虫的每次 HTTP 调用都会：

1. 为即将抓取的 URI **租借**一个身份（cookie + User-Agent）和一个代理，
   `AsyncClient.lease(site, client="web", uri=...)`；
2. 用 `httpx.AsyncClient(**lease.httpx_kwargs())` **请求**目标 —— 凭据和代理已合并进客户端参数；
3. 用 `lease.report_response(response, markers=[...])` **上报**它观察到的事实：状态、延迟、大小，以及
   `captcha_page`、`login_redirect` 等页面标记。`async with` 块结束时，这条上报顺带归还租约。

Spinneret 把这些上报变成判定结果（信号规则）、冷却、封禁和失效（动作策略）以及熔断。爬虫从不自行判断
某个身份已经报废。它还用 `ConfigWatcher` 订阅配置项 `crawler/example.json`。

演示中的目标是仓库自带的 mock target（`test/mocktarget`），它提供 `/site/...` 页面和一个需要认证的
HTTP 代理，并通过 `X-Mock-Marker` 与 `X-Mock-Business-Code` 响应头声明页面特征。真实爬虫应当解析页面
本身。

### 端点

| 方法与路径 | 说明 |
| --- | --- |
| `GET /crawl/search?q=<text>` | 抓取 `/site/search?q=<text>`（端点组 `search`） |
| `GET /crawl/item/{id}` | 抓取 `/site/item/{id}`（端点组 `detail`） |
| `GET /config` | 配置订阅者中 `crawler/example.json` 的当前版本与内容 |
| `GET /healthz` | 存活检查，并返回配置订阅者是否在运行 |

抓取成功返回 `200`，内容为 `{"ok", "status", "identity_id", "proxy_id", "endpoint_group", "markers",
"business_code", "data"}`。租借失败会被映射为 HTTP 响应：`circuit_open` / `site_paused` / `overloaded` 返回 `503`，
所有 `resource_exhausted`（`no_identity_available`、`no_proxy_available`、`rate_limited`）返回
`429` —— 这两类响应都带上由服务端提示生成的 `Retry-After` —— 其他 Spinneret 错误以及根本没拿到响应的
请求返回 `502`（并按其 `error_kind` 上报）。

### 在 Compose 栈上运行

前置条件：`deploy/compose` 中的栈已初始化并运行（见[安装与部署](./02-installation.md)），宿主机上有
`python3` 和 `curl`。

```bash
scripts/example-quickstart.sh            # 或：make example
```

该脚本是幂等的。它会：

1. 在不重建已运行容器的前提下启动整个栈，并启动 mock target（Compose profile `example`）；
2. 通过 API 以管理员身份登录，在命名空间 `default` 中创建：站点 `example`（端点组 `search`，前缀
   `/site/search`；端点组 `detail`，模板 `/site/item/{id}`）、身份类型 `example_web_cookie`、20 个身份、
   2 个 mock 代理、已发布的策略 `example-rotation`（`bind_identity` 代理，1 s 复用间隔）和
   `example-signal`，以及配置项 `crawler/example.json`；
3. 创建一个节点令牌，权限范围为 `lease:acquire:example`、`report:write:example`、
   `config:read:crawler`，并写入 `deploy/compose/.env` 的 `EXAMPLE_TOKEN`（已有的有效令牌会被复用）；
4. 构建并启动 `example-crawler` 服务（`http://localhost:18000`），然后调用 `/crawl/search`、
   `/crawl/item/42` 和 `/config`。

`scripts/example-quickstart.sh --reset` 会先删除示例站点及其令牌、代理、策略和配置。

```bash
curl 'http://localhost:18000/crawl/search?q=shoes'
curl  http://localhost:18000/crawl/item/42
curl  http://localhost:18000/config
```

让目标站点「出问题」，然后在控制台的身份、熔断器、请求明细页面观察 Spinneret 的反应：

```bash
curl -X PUT localhost:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'
for i in $(seq 1 60); do curl -s -o /dev/null 'http://localhost:18000/crawl/search?q=x'; done
curl -X DELETE localhost:19090/_admin/rules
```

### 配置

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `SPINNERET_URL` | —（Compose：`http://lb:8080`） | Spinneret 基址，由 SDK 读取 |
| `SPINNERET_TOKEN` | —（Compose：`EXAMPLE_TOKEN`） | 节点令牌，由 SDK 读取 |
| `SPINNERET_NODE` | 主机名（Compose：`example-crawler`） | 每次调用发送的节点名 |
| `SPINNERET_CACHE_DIR` | 镜像中为 `/tmp/spinneret-cache` | 配置快照目录 |
| `MOCK_TARGET_URL` | `http://mocktarget:9090` | 被抓取站点的基址 |
| `EXAMPLE_SITE` / `EXAMPLE_CLIENT` | `example` / `web` | 租借时使用的站点与客户端 |
| `EXAMPLE_CONFIG_GROUP` / `EXAMPLE_CONFIG_KEY` | `crawler` / `example.json` | 订阅的配置项 |
| `EXAMPLE_LEASE_WAIT_MS` | `2000` | Acquire 等待空闲身份的时长（0..5000） |
| `EXAMPLE_REQUEST_TIMEOUT_S` | `10` | 目标请求超时（1..120） |
| `EXAMPLE_PORT`（Compose） | `18000` | 爬虫在宿主机上的端口 |

### 哪些部分值得搬进真实节点

`app/crawler.py` 是最值得照搬的部分 —— 四十行里就是完整的范式：

```python
async with client.lease(settings.site, client=settings.client, uri=path, wait_ms=settings.lease_wait_ms) as lease:
    async with httpx.AsyncClient(
        **lease.httpx_kwargs(),
        timeout=settings.request_timeout,
        follow_redirects=False,  # 登录跳转是要上报的信号，不是要跟进的页面
    ) as http:
        try:
            response = await http.get(settings.mock_target_url + path, params=dict(params or {}))
        except httpx.HTTPError as exc:
            lease.report_exception(exc)
            raise UpstreamError(f"request to the target failed: {type(exc).__name__}") from exc
    markers = markers_from(response.headers)
    business_code = response.headers.get(BUSINESS_CODE_HEADER, "")
    lease.report_response(response, markers=markers, business_code=business_code)
```

`app/main.py` 展示了进程级的接线方式：在 FastAPI lifespan 中创建一个 `AsyncClient` 和一个
`ConfigWatcher` 并在关闭时释放，以及一个把 `SpinneretError` 子类映射为自家 API HTTP 响应的小函数。

示例中有四个习惯值得在每个节点里保留：

- **每进程、每事件循环一个客户端。** 在 lifespan 中创建（在派生 worker 之后），关闭时
  `await client.aclose()`，这样排队的上报 —— 包括租约归还 —— 才会被投递出去。
- **不要盲目跟随跳转。** 登录跳转是要上报的事实，不是要抓的页面。
- **只上报事实。** 把判定结果的分类留在信号规则里，那里可以不重新部署节点就修改和验证
  （`PolicyAdminService/DebugReport`，或控制台里的规则调试器）。见[策略](./08-policies.md)。
- **绝不打印凭据或代理 URL。** SDK 的模型已在 `repr` 中隐藏它们，你自己的日志不要把这层保护抹掉。

一个实现细节：示例为每个租约新建一个 `httpx.AsyncClient`，因为 httpx 把代理绑定在客户端上。繁忙的节点
应当按代理 URL 维护一个小的客户端池。

### 在 Docker 之外运行示例

```bash
cd examples/fastapi-crawler
python3 -m venv .venv
.venv/bin/pip install -r requirements-dev.txt -e ../../sdk/python
.venv/bin/python -m pytest -q                                   # 单元测试（respx，不联网）
EXAMPLE_URL=http://localhost:18000 .venv/bin/python -m pytest -q tests/test_integration.py
SPINNERET_URL=http://localhost:8080 SPINNERET_TOKEN=spn_... MOCK_TARGET_URL=http://localhost:19090 \
  .venv/bin/uvicorn app.main:app --port 8000
```

在 Docker 之外运行时，Spinneret 中存的 mock 代理（`mocktarget:9091`）在宿主机上无法解析：请使用
Compose 服务，或在 `/etc/hosts` 中加入 `127.0.0.1 mocktarget` 并把 9091 端口映射出来。

---

## 没有 SDK 时如何自己写客户端

节点 API 是 Connect over HTTP + JSON：就是一次普通的 `POST`，body 为 JSON，路径由服务名和方法名拼出。
任何能发 HTTP 的东西都可以成为节点。完整的消息定义在[节点 API 参考](./13-node-api.md)；本节只给出让你
跑起来的最小集合。

### 每个调用的形状

```
POST <base-url>/spinneret.v1.<Service>/<Method>
Content-Type: application/json
Accept: application/json
Connect-Protocol-Version: 1
Authorization: Bearer spn_xxx
X-Spinneret-Node: crawler-a-03
```

成功是 `200`，body 为 JSON 形式的响应消息。失败是非 2xx 状态，body 中带 `code` 和 `message`，另有两个
响应头：`Spinneret-Reason`（机器可读的原因）和 `Spinneret-Retry-After-Ms`（存在时为重试提示）。

### 四个调用组成的完整节点

租借一个租约：

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Acquire" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: crawler-a-03' \
  -d '{"site":"example-site","client":"web","uri":"/api/v1/search","wait_ms":500}'
```

```json
{
  "lease": {
    "lease_id": "lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
    "identity_id": "idt_0199c1e8b4417a2c9d0e3f5a6b7c8d9e",
    "identity_type": "example_web_cookie",
    "endpoint_group": "search",
    "expires_at": "2026-09-18T09:31:05.412Z",
    "sticky": false,
    "probe": false
  },
  "credential": {
    "cookies": {"sid": "…"},
    "cookie_header": "sid=…",
    "headers": {"User-Agent": "…"},
    "query": {},
    "json": null,
    "values": {}
  },
  "proxy": {"proxy_id": "pxy_0199c1d2f3a45b6c7d8e9f0a1b2c3d4e", "url": "http://user:pass@proxy.internal:8080", "kind": "datacenter", "region": "US"},
  "hints": {"renew_before_ms": 30000}
}
```

然后自己经 `proxy.url` 发出请求，应用 `credential.headers`、`credential.cookie_header`（或
`credential.cookies`）和 `credential.query`。接着上报发生了什么，并用同一个调用归还租约：

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ReportService/Report" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: crawler-a-03' \
  -d '{"reports":[{
        "report_id":"2f1c9a6e-9d2b-4a1f-8a0e-1c6d4f2b7e35",
        "lease_id":"lse_0199c1f4a2b07c3d8e5f6a1b2c3d4e5f_1k3_07",
        "uri":"/api/v1/search",
        "method":"GET",
        "http_status":200,
        "markers":[],
        "latency_ms":412,
        "response_bytes":21840,
        "started_at":"2026-09-18T09:30:35.000Z",
        "finished_at":"2026-09-18T09:30:35.412Z",
        "release":true
      }]}'
```

```json
{"accepted": 1, "duplicated": 0, "rejected": []}
```

读取一个配置项：

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ConfigService/GetConfig" \
  -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"group":"crawler","key":"search.json"}'
```

订阅它的变更 —— 该调用会阻塞到有变更或 `timeout_ms` 到期（0 表示服务端默认的 30 000 ms，最大
60 000 ms），且只返回发生变化的项：

```bash
curl -sS --max-time 40 -X POST "$SPINNERET_URL/spinneret.v1.ConfigService/WatchConfig" \
  -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"items":[{"group":"crawler","key":"search.json","version":7}],"timeout_ms":30000}'
```

返回空的 `{"items":[]}` 表示等待超时、没有变更：带着你手上的版本号再发一次同样的请求。HTTP 读超时要设
为 `timeout_ms` 再加几秒 —— 两个 SDK 都加了 5 s。

### SDK 替你做了、现在得你自己做的事

| 行为 | 手写客户端要注意什么 |
| --- | --- |
| **按重试提示重试** | 遇到 `resource_exhausted`/`no_identity_available` 和 `unavailable`（包括 `unavailable`/`overloaded`——那是服务端在请求到达 Redis 之前就把它甩掉了，因此重试总是安全的）时，等待 `Spinneret-Retry-After-Ms` 再重试。`circuit_open` 和 `site_paused` 绝不重试 —— 它们是决策而非瞬时故障；应当暂停该端点组或站点。含糊的失败（读超时、连接被重置）之后绝不重发 `Acquire`：它不是幂等的，重发会白白占住一个身份直到租约到期。只有在请求可证明从未离开你的进程时，或服务端自己回答 `unavailable` 时，重试才是安全的。 |
| **续约** | 租约在 `lease.expires_at` 到期。如果你的请求可能活得比它久，就在剩余时间不足 `hints.renew_before_ms` 毫秒时调用 `LeaseService/Renew`，参数 `{"lease_id": …, "extend_ms": 0}`。续约受轮换策略的生命周期上限约束：`lease_lifetime_exceeded` 意味着要重新租借。 |
| **用最后一条上报归还** | 不要每次请求后都调 `Release`。扣留最后一条上报，带 `"release": true` 发出；只有在完全没有可上报内容时才调用 `LeaseService/Release`。未归还的租约不会丢失 —— 它会过期 —— 但在此之前该身份一直处于占用状态。 |
| **上报批量化** | 一次 `Report` 调用可带 1..500 条上报。在后台工作者中缓冲，按短间隔（200 ms）或批量大小（100）刷出，并设置有界队列，避免服务端变慢时内存无限增长。队列满时丢弃最旧的上报，并对丢弃计数。 |
| **上报幂等** | 把 `report_id` 设成你自己生成的 UUID。这样重发整批就是免费的：重复项会出现在 `duplicated` 里，而不会被重复计数。 |
| **订阅重连** | 订阅循环是：带上你手上的版本发 `WatchConfig` → 应用返回的项 → 立刻再发一次。失败时退避（1 s 翻倍到 30 s，带抖动）后重连，不要空转。整个过程中继续使用最后已知的值提供服务。 |
| **快照兜底** | 把每个取回的非密钥配置项写到本地文件，这样控制平面宕机时节点仍能启动。`has_secret_refs` 为 true 的项，以及内容中仍含 `${secret:` 的项，绝不能持久化。 |
| **上报 URI 的卫生** | 只发请求**路径**：去掉查询串和片段，截断到 2048 字符。端点组按路径匹配，而查询参数经常携带带签名的凭据值，你不会希望它们进入请求分析数据。 |
| **错误分类** | 失败请求的上报要带 `error_kind`，取值为 `timeout`、`conn_reset`、`conn_refused`、`proxy_auth`、`tls`、`dns`、`other` 之一。把你所用语言的网络异常映射过去；信号规则依赖它们。`407` 响应是一个响应 —— 上报 `http_status: 407` 并置 `error_kind: "proxy_auth"`。 |
| **上报时间戳** | `started_at` 和 `finished_at` 是必填项，RFC 3339 UTC 格式。如果你只量到了延迟，就用 `started_at = finished_at - latency_ms` 算出来。 |

还有两条不属于 SDK、但同样关键的规则：绝不打印代理 URL（其中含凭据）或凭据本身；把判定结果的分类留在
服务端。上报你看到的 `markers` —— `captcha_page`、`empty_list`、`login_redirect` —— 让信号规则去决定
它们意味着什么。

---

## 下一步

- [节点 API 参考](./13-node-api.md) —— SDK 所封装的每个消息、字段和错误原因。
- [配置中心](./09-config-center.md) —— 配置项、版本和订阅协议在服务端是怎么运作的。
- [密钥保管库](./10-secrets.md) —— `${secret:...}` 解析成什么，以及节点被允许怎样读取密钥。
- [策略](./08-policies.md) —— 节点发出的标记和上报究竟在哪里被解释。
- [租户、用户与令牌](./11-access-control.md) —— 节点令牌需要哪些权限范围。
- [故障排查](./18-troubleshooting.md) —— 症状 → 原因 → 处理，含完整的错误原因表。
