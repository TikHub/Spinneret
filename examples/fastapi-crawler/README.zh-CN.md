# FastAPI 示例爬虫

[English](README.md)

一个基于 [FastAPI](https://fastapi.tiangolo.com/) 与 [Spinneret Python SDK](../../sdk/python) 的小型爬虫节点。
每次调用爬虫接口时，它会：

1. 为即将抓取的 URI 向 Spinneret **领取租约**，获得身份（Cookie + User-Agent）和代理
   （`AsyncClient.lease(site, client="web", uri=...)`）；
2. 使用 `httpx.AsyncClient(**lease.httpx_kwargs())` **请求**目标站点，凭证与代理已合并进客户端参数；
3. **上报**观察到的事实（`lease.report_response(response, markers=[...])`）：状态码、耗时、响应大小，以及
   `captcha_page`、`login_redirect` 等页面特征。`async with` 块结束时，最后一条上报会同时释放租约。

结果分类（识别规则）、冷却、封禁、过期（处置规则）以及熔断全部由 Spinneret 根据上报决定，爬虫自身从不判断
身份是否失效。爬虫还通过 `ConfigWatcher`（长轮询）监听配置项 `crawler/example.json`。

演示中的目标站点是 Spinneret 的 **mock target**（`test/mocktarget`）：它提供 `/site/...` 页面和一个带认证的
HTTP 代理，并通过响应头 `X-Mock-Marker` / `X-Mock-Business-Code` 声明页面特征（真实爬虫应解析页面内容）。

## 接口

| 方法与路径 | 说明 |
| --- | --- |
| `GET /crawl/search?q=<text>` | 抓取 `/site/search?q=<text>`（端点组 `search`） |
| `GET /crawl/item/{id}` | 抓取 `/site/item/{id}`（端点组 `detail`） |
| `GET /config` | 配置监听器中 `crawler/example.json` 的当前版本与内容 |
| `GET /healthz` | 存活检查 |

抓取成功返回 `200` 与 `{"ok", "status", "identity_id", "proxy_id", "endpoint_group", "markers",
"business_code", "data"}`。租约失败会映射为 HTTP 响应：`circuit_open` / `site_paused` 返回 `503`，
`no_identity_available` / `no_proxy_available` 返回 `429`（两者都带有来自服务端提示的 `Retry-After`），其他
Spinneret 错误以及未收到响应的请求返回 `502`（后者会带着 `error_kind` 上报）。

## 快速开始（Docker Compose）

前置条件：`deploy/compose` 中的栈已完成初始化（`scripts/compose-init.sh`、`docker compose ... up`、
`init-admin`），主机上有 `python3` 与 `curl`。

```bash
scripts/example-quickstart.sh            # 或：make example
```

脚本是幂等的，它会：

1. 在不重建已运行容器的前提下启动整个栈和 mock target（profile `example`）；
2. 通过 API 以管理员身份登录，在命名空间 `default` 中创建：站点 `example` 及端点组 `search`（前缀
   `/site/search`）和 `detail`（模板 `/site/item/{id}`）、身份类型 `example_web_cookie`、20 个身份、2 个 mock
   代理、已发布的策略 `example-rotation`（`bind_identity` 代理、1 秒复用间隔）与 `example-signal`、配置项
   `crawler/example.json`；
3. 创建节点令牌（`lease:acquire:example`、`report:write:example`、`config:read:crawler`），并以
   `EXAMPLE_TOKEN` 写入 `deploy/compose/.env`（已有的有效令牌会被复用）；
4. 构建并启动 `example-crawler` 服务（`http://localhost:18000`），依次调用 `/crawl/search`、`/crawl/item/42`
   和 `/config` 验证整条链路。

`scripts/example-quickstart.sh --reset` 会先删除示例站点及其令牌、代理、策略和配置。

```bash
curl 'http://localhost:18000/crawl/search?q=shoes'
curl  http://localhost:18000/crawl/item/42
curl  http://localhost:18000/config
```

让目标站点返回异常，然后在控制台（身份、熔断、请求明细）中观察 Spinneret 的反应：

```bash
curl -X PUT localhost:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'
for i in $(seq 1 60); do curl -s -o /dev/null 'http://localhost:18000/crawl/search?q=x'; done
curl -X DELETE localhost:19090/_admin/rules
```

## 配置

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `SPINNERET_URL` | 无（compose：`http://lb:8080`） | Spinneret 地址（由 SDK 读取） |
| `SPINNERET_TOKEN` | 无（compose：`EXAMPLE_TOKEN`） | 节点令牌（由 SDK 读取） |
| `SPINNERET_NODE` | 主机名（compose：`example-crawler`） | 每次调用携带的节点名 |
| `SPINNERET_CACHE_DIR` | 镜像内为 `/tmp/spinneret-cache` | 配置快照目录 |
| `MOCK_TARGET_URL` | `http://mocktarget:9090` | 被抓取站点的地址 |
| `EXAMPLE_SITE` / `EXAMPLE_CLIENT` | `example` / `web` | 领取租约使用的站点与客户端类型 |
| `EXAMPLE_CONFIG_GROUP` / `EXAMPLE_CONFIG_KEY` | `crawler` / `example.json` | 监听的配置项 |
| `EXAMPLE_LEASE_WAIT_MS` | `2000` | 领取时等待可用身份的时长（0..5000） |
| `EXAMPLE_REQUEST_TIMEOUT_S` | `10` | 目标请求超时（1..120） |
| `EXAMPLE_PORT`（compose） | `18000` | 爬虫在主机上的端口 |

## 开发

```bash
cd examples/fastapi-crawler
python3 -m venv .venv
.venv/bin/pip install -r requirements-dev.txt -e ../../sdk/python
.venv/bin/python -m pytest -q                                   # 单元测试（respx，无网络访问）
EXAMPLE_URL=http://localhost:18000 .venv/bin/python -m pytest -q tests/test_integration.py
SPINNERET_URL=http://localhost:8080 SPINNERET_TOKEN=spn_... MOCK_TARGET_URL=http://localhost:19090 \
  .venv/bin/uvicorn app.main:app --port 8000
```

在 Docker 之外运行时，Spinneret 中保存的 mock 代理地址（`mocktarget:9091`）在主机上无法解析；请使用 compose
服务，或在 `/etc/hosts` 中添加 `127.0.0.1 mocktarget` 并确保 9091 端口已映射。

## 排查

- `429 no_proxy_available`：mock 代理被代理健康检查判定为 `dead`。健康检查会通过每个代理访问
  `SPINNERET_PROXY_CHECK_URL`（未设置时使用服务端内置的检测地址）。若主机无法访问外网，请在
  `deploy/compose/.env` 中设置 `SPINNERET_PROXY_CHECK_URL=http://mocktarget:9090/healthz` 并重启 `spinneret` 服务。
- `503 site_paused`：站点开关被关闭（`BreakerAdminService/SetSitePaused` 或控制台）。
- `503 circuit_open`：端点组熔断器已打开，目标站点恢复后会自动关闭。

## 生产环境建议

- 每个进程、每个事件循环使用一个 `spinneret.AsyncClient`；在 lifespan 中（worker 进程 fork 之后）创建，并在
  关闭时 `await client.aclose()`，确保排队中的上报（租约释放）送达。
- httpx 的代理绑定在客户端上，因此示例为每个租约创建一个 `httpx.AsyncClient`；高并发节点可以按代理 URL
  维护一个小型客户端池。
- 只上报事实。结果分类放在识别策略中，可以随时修改并通过 `PolicyAdminService/DebugReport` 测试，无需重新
  部署节点。
- 不要记录租约凭证或代理 URL；SDK 的模型在 `repr` 中已隐藏这些字段。
