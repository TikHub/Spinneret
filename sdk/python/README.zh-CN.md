# Spinneret Python SDK

[English](README.md)

[Spinneret](https://github.com/Evil0ctal/Spinneret) 的 Python 客户端。Spinneret 是爬虫与 API 节点的控制面：
向节点租出身份（Cookie、设备参数、账号）与代理，根据请求上报执行冷却、封禁与熔断，并下发配置与密钥。

- 基于 `httpx` 的同步客户端 `Client` 与 asyncio 客户端 `AsyncClient`
- 覆盖全部节点接口消息的 pydantic v2 类型模型
- 租约上下文管理器：把凭证和代理合并成 `httpx` 参数，并用最后一次上报释放租约（没有上报时调用 `LeaseService/Release`）
- 后台批量上报（每 200 ms 或攒满 100 条发送一次，有界队列）
- 配置监听：长轮询、变更回调、原子写入的本地快照
- 携带服务端原因与建议等待时间的类型化异常

要求 Python 3.9+、`httpx>=0.27`、`pydantic>=2.6`。

## 安装

```bash
pip install spinneret                # SDK
pip install 'spinneret[crypto]'      # 额外支持加密缓存含密钥引用的配置
```

若 Spinneret 下发 SOCKS 代理，需要 `pip install 'httpx[socks]'`。

## 配置

| 环境变量 | 含义 | 默认值 |
| --- | --- | --- |
| `SPINNERET_URL` | 服务地址，如 `https://spinneret.internal` | 必填 |
| `SPINNERET_TOKEN` | 节点令牌（`spn_...`） | 必填 |
| `SPINNERET_NODE` | 节点名，作为 `X-Spinneret-Node` 请求头 | 主机名 |
| `SPINNERET_CACHE_DIR` | 配置快照目录 | `~/.spinneret/cache` |

显式参数优先于环境变量：

```python
client = spinneret.Client("https://spinneret.internal", "spn_xxx", node="crawler-hk-03")
```

## 快速开始

```python
import httpx
import spinneret

with spinneret.Client() as client:
    with client.lease(site="shop", client="web", uri="/api/v1/search") as lease:
        with httpx.Client(**lease.httpx_kwargs()) as http:
            response = http.get("https://target.example.com/api/v1/search")
        lease.report_response(
            response, markers=["empty_list"] if not response.json().get("data") else []
        )
```

异步：

```python
async with spinneret.AsyncClient() as client:
    async with client.lease(site="shop", client="web", uri="/api/v1/feed") as lease:
        async with httpx.AsyncClient(**lease.httpx_kwargs()) as http:
            response = await http.get("https://target.example.com/api/v1/feed")
        lease.report_response(response)
```

完整示例见 [`examples/basic_usage.py`](examples/basic_usage.py)。

## 租约

`client.lease(site, client, uri="", *, endpoint_group="", session_key="", wait_ms=0,
flush_on_exit=False, raise_on_release_error=False)` 返回上下文管理器，进入时调用 `LeaseService/Acquire`。

代码块内可用：

| 成员 | 说明 |
| --- | --- |
| `lease_id`、`identity_id`、`info`、`expires_at` | 租约信息（`info.probe`、`info.sticky` 等） |
| `credential` | `cookies`、`cookie_header`、`headers`、`query`、`json_value`（JSON 字段名 `json`）、`values` |
| `proxy` | `proxy_id`、`url`、`kind`、`region`，无代理时为 `None` |
| `httpx_kwargs(headers=, params=, cookies=)` | 返回 `dict(headers, cookies, params, proxy)`，用于 `httpx.Client(...)`；显式参数覆盖凭证值；凭证没有 cookie 映射时 `cookie_header` 作为 `Cookie` 请求头 |
| `report(status, *, latency_ms, markers, business_code, error_kind, release, **fields)` | 排队一条上报（可选字段 `uri`、`method`、`outcome_hint`、`response_bytes`、`started_at`、`finished_at`、`report_id`） |
| `report_response(response, *, markers, business_code, release)` | 根据 `httpx.Response` 上报（状态码、方法、路径、耗时、大小） |
| `report_exception(exc, *, markers, release)` | 上报失败请求，`error_kind` 由 `classify_exception` 得出 |
| `renew(extend_ms=0)` | 续租（异步租约需 `await`） |
| `release()` | 立即结束租约（等同于无异常地离开代码块；异步租约需 `await`） |

释放语义：

- 上报进入客户端的后台上报器。最近一条上报会暂存，直到下一条上报或代码块结束，以便它携带 `release: true`。
- 离开代码块时（包括代码块抛出异常时），最后一条上报带 `release: true` 发送。若没有任何上报，
  则调用 `LeaseService/Release`（`{"lease_id": ...}`）释放租约；SDK 不会伪造上报，失败的请求如需计入统计，
  请自行调用 `report_exception` 上报。
- `report(..., release=True)` 立即释放，之后再上报会抛出 `LeaseReleased`。
- `flush_on_exit=True` 会等待上报队列发送完成（最长 `ReporterOptions.close_timeout`）。
- 默认情况下释放失败不会从代码块抛出。`Release` 返回原因 `lease_released`、`lease_unknown` 或 `lease_expired`
  表示租约已经结束，只记录 debug 日志；其他错误（以及直接投递上报失败）记录 warning 日志。
  设置 `raise_on_release_error=True` 后，这些其他 `Release` 错误会从 `with` 语句和 `release()` 抛出，
  但代码块自身抛出异常时不会抛出（不会覆盖代码块的异常）。
- 一个租约可服务多次请求（如翻页）：每次请求各上报一次，最后一次负责释放。

`report_id` 默认随机 UUID4，`finished_at` 默认当前时间，`started_at` 默认 `finished_at - latency_ms`；
`business_code` 接受数字或字符串；`error_kind` 必须为空或[错误类型分类](#错误类型分类)中列出的值。
上报的 `uri` 只保留请求路径（去掉查询参数与片段，最长 2048 个字符）：服务端只按路径匹配端点组，
而查询参数经常包含带签名的凭证值。`407` 响应会带上 `error_kind="proxy_auth"`。

也可直接调用：`acquire`、`acquire_batch(site, client, count, ...)`、`renew`、`release`、
`report(reports)`（同步发送 1..500 条）。

## 后台上报

`client.reporter`（同步为线程 `Reporter`，异步为任务 `AsyncReporter`）批量发送：

- 攒满 100 条或最早一条等待 200 ms 即发送，单次最多 500 条；
- 队列上限 10 000 条，满时丢弃最旧的上报，计入 `reporter.stats.dropped` 并限频告警；
- 传输错误以及 `unavailable`、`internal`、`deadline_exceeded`、`resource_exhausted` 按带抖动的指数退避重试（0.5 s .. 30 s），其他错误丢弃整批；
- 被服务端拒绝的上报会丢弃、记录日志并回调 `on_rejected`；
- `flush(timeout)` 立即发送；`close(timeout)`（`client.close()` 会调用）在 `close_timeout`（5 s）内尽量发送后停止。
  同步上报器在解释器退出时也会关闭；asyncio 下务必 `await client.aclose()`。
- 不再运行的工作线程（例如 `os.fork()` 创建的子进程中）或已结束事件循环上的任务，会在下一次
  `submit`、`flush` 或 `close` 时重新启动。

```python
options = spinneret.ReporterOptions(
    flush_interval=0.2,
    batch_size=100,
    max_queue_size=10_000,
    on_rejected=lambda r: log.warning("rejected %s", r.reason),
)
client = spinneret.Client(reporter_options=options)
```

## 配置中心

```python
def on_change(item: spinneret.ConfigItem) -> None:
    reload_settings(item.content)


with client.config_watcher(
    ["crawler/search.json", ("_runtime", "breakers")], on_change=on_change
) as watcher:
    item = watcher.get("crawler", "search.json")
    changed = watcher.wait_for_change("crawler", "search.json", timeout=60)
```

- `start()` 先用 `BatchGetConfig` 加载，然后由线程（或 asyncio 任务）长轮询 `WatchConfig`
  （`timeout_ms` 默认 30 000，HTTP 读超时为 `timeout_ms + 5 s`），保存最新内容，并对每个变化项（包括初始值）调用回调。
  客户端关闭后监听循环随之结束。
- 每次成功获取的非密钥配置以原子方式（临时文件 + `fsync` + 重命名，权限 `0600`）写入
  `<cache_dir>/<host>/<namespace>/<group>/<key>.json`。
- 启动时服务不可用（传输错误、`unavailable`、`internal`、`deadline_exceeded`、`resource_exhausted` 或其他 5xx）
  则加载本地快照，`watcher.from_snapshot` 为 `True`，直到服务端响应。鉴权和参数错误由 `start()` 直接抛出。
- 含密钥的配置绝不会以明文写入磁盘：默认只保存在内存中，并删除该配置的旧快照。服务端下发前会解析
  `${secret:...}` 引用，并对已发布内容含有此类引用的配置设置 `ConfigItem.has_secret_refs`（JSON 字段 `has_secret_refs`），
  监听器据此自动识别含密钥的配置。内容中仍残留未解析的 `${secret:` 标记时同样视为含密钥。
- `treat_as_secret` 是额外的覆盖规则，用于直接内嵌敏感值、而非引用密钥的配置，
  例如 `treat_as_secret=lambda item: item.group == "signing"`。它只能追加含密钥的配置，不能豁免服务端已标记的配置；
  判定函数抛异常时视为含密钥。
- 开启 `cache_secrets=True` 后，含密钥的配置使用 AES-256-GCM 加密落盘，密钥由节点令牌经 HKDF-SHA256 派生（每个文件独立的盐和随机数）。
  标准库没有 AES-GCM，因此需要安装 `cryptography`（`spinneret[crypto]`）；未安装时 `cache_secrets=True`
  会抛出 `ConfigurationError`。令牌轮换后无法解密旧令牌加密的快照。
- `snapshots=False` 关闭快照，`cache_dir=` 指定目录。

单次读取：`get_config(group, key)`、`batch_get_config(items)`、`watch_config(items)`
（`timeout_ms=0` 表示使用服务端默认的 30 s 等待）、`get_secret(path, version=0)`。

## 错误

所有异常继承自 `SpinneretError`，包含 `code`、`reason`、`message`、`retry_after_ms`（`retry_after` 为秒）和 `http_status`。

| 异常 | 错误码 / 原因 | 节点应对 |
| --- | --- | --- |
| `Unauthenticated` | `unauthenticated`（`token_invalid` 等） | 停止并告警 |
| `PermissionDenied` | `permission_denied`（`scope_missing`） | 停止并告警 |
| `InvalidArgument` | `invalid_argument`（`site_unknown`、`uri_invalid` 等） | 修正调用代码 |
| `NoIdentityAvailable` / `NoProxyAvailable` | `resource_exhausted` | 等待 `retry_after` 后重试 |
| `ResourceExhausted` | `resource_exhausted`（`rate_limited` 等） | 等待 `retry_after` |
| `CircuitOpen` / `SitePaused` | `unavailable` | 暂停该端点组或站点 |
| `Unavailable` | `unavailable`（`rebuilding` 等） | 稍后重试 |
| `LeaseUnknown` | `not_found`（`lease_unknown`） | 重新领取 |
| `LeaseReleased` / `LeaseExpired` | `failed_precondition` | 重新领取 |
| `NotFound`、`AlreadyExists`、`Aborted`、`DeadlineExceeded`、`Unimplemented`、`InternalError` | 对应错误码 | |
| `TransportError`（`Unavailable` 子类） | 未收到响应，附带 `error_kind` | 稍后重试 |
| `ConfigurationError` | SDK 配置错误 | 修正配置 |
| `ReporterClosedError` | 上报器关闭后仍提交上报 | |
| `FailedPrecondition`（原因 `client_closed`） | 客户端关闭后仍发起调用 | 创建新的客户端 |

没有 Connect 错误体的响应（例如负载均衡返回的页面）按 Connect 协议由 HTTP 状态码映射。

## 重试与超时

- 超时：连接 3 s，读取 10 s（Acquire 额外加 `wait_ms`），写入 10 s，连接池 10 s（`spinneret.Timeouts`）。
- `RetryPolicy(max_retries=2)`：传输错误与 `unavailable` 响应按带抖动的指数退避重试；`circuit_open` 与
  `site_paused` 从不重试；服务端建议等待超过 `max_retry_after`（5 s）时直接抛出。
- `Acquire` 与 `AcquireBatch` 不是幂等操作：只有确定请求未到达服务端（连接被拒、连接或连接池超时）或服务端明确返回
  `unavailable` 时才重试；读超时、连接重置等不确定的失败直接抛出。
- `spinneret.NO_RETRY` 关闭重试。

## 错误类型分类

`spinneret.classify_exception(exc)` 把请求异常映射为上报的 `error_kind`：`timeout`、`conn_reset`、
`conn_refused`、`proxy_auth`、`tls`、`dns` 或 `other`。支持 httpx 异常（含其原因链）、socket/SSL 异常和常见错误信息。
`httpx.HTTPStatusError` 返回 `""`（应上报状态码），407 除外（`proxy_auth`）。

## 进程与事件循环

请在 fork 出工作进程之后再创建客户端（例如在 gunicorn 或 uvicorn 的工作进程启动钩子中）：`httpx` 连接池不能在进程间共享。
`AsyncClient` 属于使用它的事件循环，每个事件循环各自创建一个。

## 日志与安全

SDK 通过 `logging.getLogger("spinneret")`（子记录器 `spinneret.reporter`、`spinneret.lease`、`spinneret.config`）
输出日志，并默认挂载 `NullHandler`。SDK 从不记录令牌、凭证、代理地址、配置内容或密钥值，模型的 `repr` 也会隐藏这些字段。

## 开发

```bash
cd sdk/python
python3 -m venv .venv && . .venv/bin/activate
pip install -e '.[dev]'
pytest -q --cov=spinneret
ruff check . && ruff format --check .
mypy src
```

测试使用 `respx` 与 `httpx.MockTransport`，不访问网络。
