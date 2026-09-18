# 参与贡献

**如何搭建 Spinneret 的开发环境、熟悉仓库结构、重新生成代码、运行各层测试，以及让一次改动通过全部质量门禁。
提交第一个 Pull Request 之前请先读这一页。**

[English](../en/20-contributing.md)

---

## 目录

- [开发环境](#开发环境)
- [仓库结构](#仓库结构)
- [本地开发循环](#本地开发循环)
- [端口](#端口)
- [代码生成](#代码生成)
- [数据库迁移](#数据库迁移)
- [测试](#测试)
- [Makefile 目标](#makefile-目标)
- [质量门禁](#质量门禁)
- [代码约定](#代码约定)
- [文档](#文档)
- [提交与 Pull Request](#提交与-pull-request)
- [提出较大的改动](#提出较大的改动)
- [下一步](#下一步)

---

## 开发环境

Spinneret 由一个 Go 服务端（内嵌 React 控制台）、一个 Python SDK、一个 Go SDK 和一套 Docker Compose
栈组成。改一处代码并不需要把全部工具装齐：只改后端需要 Go 和 Docker，只改控制台需要 Node 和一个能连上的
服务端。

| 工具 | 版本 | 用途 | 安装 |
| --- | --- | --- | --- |
| Go | 1.27.1 及以上（`go.mod`） | 服务端、CLI、Go SDK、全部 Go 测试 | <https://go.dev/dl/> |
| Docker + Compose v2 | 任意当前版本 | 测试基础设施、完整栈、端到端与压测套件 | Docker Desktop 或 Docker Engine |
| Node.js | 22.13 及以上（`web/package.json` 的 `engines`） | 控制台 | <https://nodejs.org/> 或版本管理器 |
| pnpm | 10.27.0（`web/package.json` 的 `packageManager`） | 控制台 | `corepack enable` 会自动使用锁定的版本 |
| buf | v1.73.0（CI 锁定） | 重新生成 protobuf 代码 | `go install github.com/bufbuild/buf/cmd/buf@v1.73.0` |
| protoc-gen-go | latest | 生成的 Go 消息类型 | `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest` |
| protoc-gen-connect-go | latest | 生成的 Connect handler 与 client | `go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest` |
| sqlc | v1.31.1（CI 锁定） | 重新生成数据库查询代码 | `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1` |
| golangci-lint | v2.13.2（CI 锁定） | Go 代码检查门禁 | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2` |
| Python | 3.9 及以上（CI 覆盖 3.9、3.12、3.13） | Python SDK、示例节点、演练脚本 | 系统自带的 Python |
| Playwright 的 Chromium | 与 `@playwright/test` 1.63.0 匹配 | 控制台端到端用例 | `cd web && pnpm exec playwright install chromium` |
| k6 | — | 压测套件 | 不需要装在宿主机：通过 Compose 的 `loadtest` profile 使用 `grafana/k6` 镜像 |

**说明。** 不需要安装 goose 命令行。迁移就是普通的 goose 格式 SQL 文件，被编译进二进制里
（`github.com/pressly/goose/v3` 只作为库使用），由 `spnr migrate` 执行。见[数据库迁移](#数据库迁移)。

一次装齐：

```bash
# Go 工具链
go version                      # 必须是 go1.27.1 或更高

# 代码生成与静态检查工具，版本与 CI 锁定的一致
go install github.com/bufbuild/buf/cmd/buf@v1.73.0
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
export PATH="$(go env GOPATH)/bin:$PATH"

# 控制台
corepack enable
cd web && pnpm install --frozen-lockfile && cd ..

# Python SDK
cd sdk/python
python3 -m venv .venv && . .venv/bin/activate
pip install -e '.[dev]'
cd ../..
```

`Makefile` 会给每个目标把 `$(go env GOPATH)/bin` 加进 `PATH`，`scripts/buf-generate.sh`、
`scripts/sqlc-generate.sh` 和 `web/scripts/gen.mjs` 也各自会把 `$HOME/go/bin` 前置，所以即使当前 shell
没有导出这个路径，`go install` 装的工具也能被找到。Python SDK 用任何兼容 pip 的安装器都可以，CI 用的
是 `pip`。

---

## 仓库结构

```text
Spinneret/
├── cmd/
│   ├── spinneret-server/    服务端入口（命令行参数、信号、角色选择）
│   ├── spnr/                管理 CLI（cobra）：migrate、admin、token、rebuild、kek、seed、healthcheck、version、config
│   └── internal/buildinfo/  两个二进制共用的版本与构建信息
├── proto/spinneret/v1/      API 的唯一事实来源（.proto，包名 spinneret.v1），其中的注释具有规范效力
├── gen/go/spinneret/v1/     生成的 Go 消息类型与 Connect handler/client —— 已提交入库，禁止手改
├── internal/                全部服务端代码（见下表）
├── web/                     React 控制台；构建到 web/dist，由 web/embed.go 内嵌进服务端
├── sdk/go/spinneret/        Go SDK，属于主模块
├── sdk/python/              Python SDK（httpx + pydantic v2），独立的包，有自己的 pyproject.toml
├── examples/fastapi-crawler/  可直接运行的示例节点，使用 Python SDK
├── deploy/compose/          Compose 栈、各个 overlay、.env.example 与服务配置
├── deploy/docker/           `Dockerfile`（多阶段：控制台 → 二进制 → distroless）和模拟目标站点与代理用的 `mocktarget.Dockerfile`
├── install/                 一键安装脚本
├── scripts/                 开发脚本：代码生成加锁、compose 初始化、演练、示例快速启动
├── test/                    不属于某个包的测试（见下表）
├── tools/deps.go            用构建标签隔离的 import，把工具依赖固定在 go.mod 里
├── documents/               发布的文档：en/、zh/ 和 images/
├── CONTRIBUTING.md          本页的简版，双语同文件；仓库首页链接的就是它
├── SECURITY.md              支持的版本，以及如何私下上报漏洞
├── CHANGELOG.md             Keep a Changelog 格式；用户可见的改动追加到 [Unreleased] 一节
├── Makefile                 全部开发任务（`make help` 会列出来）
├── buf.yaml / buf.gen.yaml  buf 工作区与 Go 生成模板
├── sqlc.yaml                根目录的 sqlc 配置（每个有查询的包还有一份自己的）
├── .golangci.yml            静态检查配置
└── .github/                 CI 与发布工作流、Pull Request 模板和 issue 模板
```

仓库根目录的 `CONTRIBUTING.md` 是本页的简版 —— 一屏之内讲完开发循环、项目约定和提交规范 ——
其余内容由它链接到这里。`SECURITY.md` 是漏洞上报流程。两份文件都把中英文放在同一个文件里。

工作副本里还会有一个 `docs/`。那是本地设计笔记，已被 `.gitignore` 排除，不属于发布的项目，
`documents/` 里的任何内容都不得链接进去。

### `internal/`

| 包 | 职责 |
| --- | --- |
| `appconfig` | 加载并校验每一个 `SPINNERET_*` 变量及其默认值 |
| `apperr` | 带 Connect code、机器可读 reason 和重试提示的类型化应用错误 |
| `authz` | 权限、角色、权限范围、主体与资源级校验 —— 纯逻辑，不做 I/O |
| `auth` | API 令牌校验、控制台用户（Argon2id）、会话、登录限流、CSRF |
| `tenancy` | 租户与命名空间，删除约束，新命名空间的默认策略初始化 |
| `pkg/` | 共用小工具：`admit`（准入闸门：有界并发上限加一个 FIFO 等待区）、`idgen`、`durationx`、`glob`、`netx`、`textdiff` |
| `observability` | `slog` 日志、全部 Prometheus 指标向量、OpenTelemetry 链路 |
| `store/postgres` | pgx 连接池、内嵌的 goose 迁移、事务、分区管理、sqlc 产物 |
| `store/redis` | rueidis 客户端工厂、键构造器、Lua 脚本加载器与共享前置脚本 |
| `store/clickhouse` | ClickHouse 客户端、建表迁移与批量写入器 |
| `vault` | 信封加密：KEK、DEK、DEK 缓存、密钥服务、重加密任务 |
| `events` | 集群事件总线：进程内广播 + Redis Pub/Sub |
| `audit` | 带缓冲的批量审计日志写入器 |
| `catalog` | 每个命名空间的不可变内存快照，热路径每次请求都会读它 |
| `site` / `sitesvc` | 站点、端点组、URI 规则与 URI 匹配器；以及它们的 PostgreSQL 服务 |
| `identity` / `identitysvc` | 身份类型、载荷、下发渲染、导入、账号；以及它们的服务层 |
| `proxy` | 代理池：导入、绑定、URL 渲染、健康检查 |
| `policy` / `policysvc` | 四种策略的规格、YAML、校验与解析；草稿、版本、绑定 |
| `hotstate` | Redis 热状态的 Go 侧：物化、同步、重建、快照 |
| `scheduler` | 租约热路径 —— Acquire、AcquireBatch、Renew、Release 以及租约回收任务 |
| `peers` | 存活 API 实例的注册表，靠 Redis 里的心跳维护；acquire 的准入闸门用全集群并发预算除以这个数量 |
| `signal` | 上报接入：校验、鉴权、去重、写入 stream |
| `worker` | stream 分片归属、上报消费者、统计与事件写入 |
| `action` | 动作执行器、生命周期状态写入器、人工与批量操作 |
| `breaker` | 熔断器窗口评估、状态机与人工操作 |
| `configcenter` | 配置项、草稿、版本、发布与回滚、订阅中心、密钥引用 |
| `notify` | 通知渠道、告警判定与异步投递 |
| `analytics` | 基于 PostgreSQL、Redis 和 ClickHouse 的仪表盘查询 |
| `stats` | 内存中的租约与上报聚合，及其向 PostgreSQL 和 ClickHouse 的落盘 |
| `jobs` | 周期任务调度器与基于 PostgreSQL 咨询锁的主节点选举 |
| `api` | 每个服务一个包，放 Connect handler，以及共用的拦截器 |
| `server` | 装配：基础设施客户端、领域服务、后台循环、HTTP 服务器、内嵌控制台 |
| `testutil` | PostgreSQL、Redis、ClickHouse 的共享集成测试夹具 |
| `version` | 链接期注入的构建信息 |

### `test/`

| 目录 | 内容 |
| --- | --- |
| `test/e2e/` | 针对已部署栈的 Go 端到端场景，带 `e2e` 构建标签 |
| `test/contract/` | 节点 API 的协议契约测试与管理 API 的校验测试 |
| `test/mocktarget/` | 端到端、压测和示例运行所用的模拟目标站点与模拟 HTTP 代理 |
| `test/load/` | k6 场景脚本、指标快照工具与调参脚本 |
| `test/perf/` | 热路径 Redis 侧微基准，带 `perf` 构建标签 |

---

## 本地开发循环

### 1. 启动基础设施

```bash
make infra-up
```

它以 Compose 项目名 `spinneret-infra` 运行 `deploy/compose/docker-compose.infra.yml`：只有
PostgreSQL、Valkey 和 ClickHouse，端口都是非默认的，因此可以和同机上的完整栈共存。PostgreSQL 容器把
数据放在 `tmpfs` 且关闭了 `fsync` —— 它只用于开发和测试，绝不能放任何需要保留的数据。
`make infra-down` 连同数据卷一起删除。

### 2. 从源码运行服务端

```bash
export SPINNERET_DATABASE_URL='postgres://spinneret:spinneret@localhost:45432/spinneret?sslmode=disable'
export SPINNERET_REDIS_URL='redis://localhost:46379/0'
export SPINNERET_CLICKHOUSE_URL='clickhouse://spinneret:spinneret@localhost:49000/default'
export SPINNERET_KEKS="k1:$(openssl rand -base64 32)"
export SPINNERET_KEK_CURRENT=k1
export SPINNERET_LOG_FORMAT=text

go run ./cmd/spnr migrate up
printf '%s\n' 'change-me' | go run ./cmd/spnr admin init --username admin --password-stdin
go run ./cmd/spinneret-server
```

服务端监听 `SPINNERET_HTTP_ADDR`（默认 `:8080`），提供 Connect API、`/healthz`、`/readyz`、事件流，
以及 —— 如果 `web/dist` 已构建 —— 控制台。全部变量见[配置参考](./03-configuration.md)，全部 CLI 命令见
[命令行工具](./15-cli.md)。

**说明。** 没有执行过 `cd web && pnpm build` 时，二进制里只内嵌了一个占位文件，所以
`http://localhost:8080` 打不开控制台。只改后端时这没关系：用 Vite 开发服务器即可。

### 3. 运行控制台开发服务器

```bash
cd web
pnpm install --frozen-lockfile     # 首次，或依赖变更之后
pnpm dev
```

Vite 监听 `http://localhost:5173`，把 `/spinneret.v1.*`、`/api`、`/healthz` 和 `/readyz` 代理到
`SPINNERET_API_URL`（默认 `http://localhost:8080`），因此它既能对接本地 `go run` 起的服务端，也能对接
任何已部署的栈。

### 4. 或者直接跑整套栈

```bash
./scripts/compose-init.sh    # 生成 deploy/compose/.env 和 KEK 文件，可重复执行
make up                      # docker compose up -d --build --wait
```

这就是文档里部署的那套栈：两个服务端副本在负载均衡后面，加上 PostgreSQL、Valkey 和 ClickHouse。
Go 端到端套件、Playwright 用例、故障切换演练和压测场景都针对它运行。服务、profile 和数据卷见
[安装与部署](./02-installation.md)。

---

## 端口

| 端口 | 用途 |
| --- | --- |
| `8080` | 服务端（`SPINNERET_HTTP_ADDR`，默认 `:8080`）；Compose 栈里由负载均衡发布 `SPINNERET_PORT`，默认 `8080` |
| `5173` | Vite 开发服务器（`pnpm dev`） |
| `45432` | 基础设施 PostgreSQL（`make infra-up`） |
| `46379` | 基础设施 Valkey |
| `49000` / `48123` | 基础设施 ClickHouse，原生协议 / HTTP |
| `9090` | 栈的 `observability` profile 里的 Prometheus（`PROMETHEUS_PORT`） |
| `19090` / `19091` | 模拟目标站点 / 模拟 HTTP 代理（`MOCK_TARGET_PORT`、`MOCK_PROXY_PORT`） |
| `18000` | 示例爬虫节点（`EXAMPLE_PORT`） |

基础设施的端口刻意与完整栈错开，两者可以同时运行。

---

## 代码生成

仓库里有三类生成代码，并且**都提交入库**。改动了输入文件的提交必须把重新生成的产物放在同一个提交里。
CI 只校验其中两类 —— Go 的 protobuf 产物和 sqlc 产物，用 `git diff --exit-code -- gen internal`。CI
里没有任何步骤执行 `pnpm gen`，也没有任何步骤看 `web/src/gen`，所以控制台侧过期的 protobuf 代码会
静默合入：让它跟上 `.proto` 是作者的责任，没有门禁替你兜住。

### Protobuf → Go

`proto/spinneret/v1/*.proto` 是线上 API 的唯一事实来源。改完 `.proto` 之后：

```bash
make proto          # = ./scripts/buf-generate.sh（先 buf lint，再 buf generate）
```

产物是 `gen/go/spinneret/v1/*.pb.go`（消息类型）和
`gen/go/spinneret/v1/spinneretv1connect/*.connect.go`（handler 与 client），由 `buf.gen.yaml` 配置。
校验规则用 protovalidate（`buf.validate`）编写，依赖从 `buf.lock` 解析。脚本会加一把目录锁，两次并发
执行不会互相干扰。

### Protobuf → TypeScript

控制台有自己的一份生成客户端代码，**不会**被 `make proto` 带出来：

```bash
cd web && pnpm gen
```

它用 `web/buf.gen.yaml` 调 buf，把 protobuf-es 代码写进 `web/src/gen`，并且使用与 Go 生成器相同的锁
目录。请在改 `.proto` 的同一个提交里重新生成它。

### SQL → Go

查询文件在 `<包>/queries/*.sql`，sqlc 读取的 schema 是 `internal/store/postgres/migrations`。仓库根
目录有一份 `sqlc.yaml`，每个拥有查询的包还各有一份（`internal/auth`、`internal/proxy`、
`internal/policysvc` 等，共十六份），每一份都有自己的输出目录：根配置写 `internal/store/postgres/db`，
`internal/auth/sqlc.yaml` 写 `internal/auth/authdb`，以此类推。

**`make sqlc` 只会重新生成十六份中的一份。** `scripts/sqlc-generate.sh` 是在仓库根目录执行一条裸的
`sqlc generate`，它只读根目录那份 `sqlc.yaml`，因此只覆盖 `internal/store/postgres/db`。CI 的做法不同：
它遍历每一份配置。改了查询文件，或改了某条查询会读到的列所在的迁移之后，请执行 CI 用的那个循环：

```bash
# 只生成 internal/store/postgres/db —— 根目录的 sqlc.yaml
make sqlc           # = ./scripts/sqlc-generate.sh

# 全部十六份 —— CI 的做法；只要你改过某个包的查询，就该用这条
for f in $(find . -name sqlc.yaml -not -path './web/*'); do sqlc generate -f "$f"; done
```

每一份 `sqlc.yaml` 都固定了自己产物的形态：`pgx/v5`、带 JSON tag、空切片而不是 nil、可空列用指针、
`timestamptz` 映射为 `time.Time`、`jsonb` 映射为 `json.RawMessage`。区别只在包名 —— 根目录是 `db`，
各个包是 `<领域>db`。

`make generate` 执行 protobuf 那一步加 `make sqlc`，因此同样只覆盖十六份中的一份；`make all` 先生成
再构建两个二进制。

### 自检

```bash
make proto
for f in $(find . -name sqlc.yaml -not -path './web/*'); do sqlc generate -f "$f"; done
(cd web && pnpm gen)
git diff --exit-code -- gen internal web/src/gen
```

CI 会用同样的循环，自行执行其中 `gen internal` 的那一半。拿 `make generate` 顶替是不等价的：十六份
sqlc 产物里有十五份不会被重新生成，于是本地自检通过，而 CI 的 `git diff --exit-code -- gen internal`
会失败。

---

## 数据库迁移

迁移是 `internal/store/postgres/migrations` 下的 goose 格式 SQL 文件，通过
`//go:embed migrations/*.sql` 编译进二进制。没有独立的迁移工具，也不需要 goose 命令行：由
`spnr migrate` 驱动，并用 PostgreSQL 会话级咨询锁在进程之间串行化，所以多个服务端实例可以同时启动。

| 命令 | 作用 |
| --- | --- |
| `spnr migrate up` | 执行全部待应用的迁移，并打印结果版本号 |
| `spnr migrate down` | 回滚最近一条已应用的迁移 |
| `spnr migrate down --to N` | 回滚到 schema 版本等于 `N` 为止（`0` 表示全部移除） |
| `spnr migrate status` | 打印已应用的版本和二进制内嵌的版本 |

新增一条迁移：

1. 按五位序号新建 `internal/store/postgres/migrations/00006_<简短名称>.sql`。当前最新的是
   `00005_api_token_name_unique_active.sql`。
2. 上下两个方向都要写。现有每条迁移都有 `-- +goose Up` 和 `-- +goose Down` 两段，`spnr migrate down`
   和测试夹具都依赖它。凡是 goose 无法按分号切分的语句 —— 函数体、`DO` 块 —— 用
   `-- +goose StatementBegin` / `-- +goose StatementEnd` 包起来，参考 `00002_partitioned.sql`。
3. 如果改动涉及某条查询读取的表，重新生成 sqlc，并把重新生成的 `db` 和 `<领域>db` 包一起提交。请用
   循环，而不是 `make sqlc`：
   `for f in $(find . -name sqlc.yaml -not -path './web/*'); do sqlc generate -f "$f"; done`。
4. 在本地的基础设施数据库上执行一次，然后跑 Go 测试：`internal/testutil` 的每个测试库都是从一个刚迁移
   过的模板库克隆出来的，所以有问题的迁移会立刻让大片测试失败。

**警告。** 绝不要修改已经发布过的迁移。它已经在部署环境里执行过，goose 不会再执行一遍，你的改动只会
存在于全新安装的环境中。请新增一条迁移。

---

## 测试

一共九层测试。下表前四行是同一套 Go 测试的四种跑法，也是日常开发时跑的；其余几层在改动热路径、控制台
或部署方式时才需要跑。

| 层次 | 命令 | 前置条件 | 大致耗时 |
| --- | --- | --- | --- |
| Go，仅单元测试 | `make test-short` | 无 | 20 秒 |
| Go，单元 + 集成 | 先 `make infra-up`，再 `make test` | 基础设施栈 | 30 秒 |
| Go，竞态检测 | `make test-race` | 基础设施栈 | 45 秒 |
| Go，覆盖率 | `make cover` | 基础设施栈 | 与 `make test` 相当，外加生成报告 |
| 控制台单元测试 | `cd web && pnpm test` | Node 与 pnpm | 10 秒（93 个测试文件） |
| Python SDK | `make python-test` | 已激活 SDK 的 venv | 5 秒（375 个用例） |
| 示例节点 | `make example-test` | `examples/fastapi-crawler/.venv` | 数秒 |
| Compose 端到端 | `make e2e` | Docker | 数分钟，首次还要加镜像构建时间 |
| 故障切换演练 | `make e2e-failover` | 运行中的栈 | 约 2 分钟 |
| 控制台端到端 | `make e2e-web` | 运行中的栈、Chromium | 十分钟量级（31 个用例、15 个文件、串行） |
| k6 压测 | `make load` 或 `test/load/run.sh` | 运行中的栈与种子数据 | 取决于场景的 `DURATION` |
| Redis 微基准 | `go test -tags perf …` | 一个可连的 Valkey | 完整 `-bench .` 需要数十分钟 |

以上耗时来自一台开发笔记本，构建缓存已预热、基础设施栈已启动。

### Go 测试

`internal/testutil` 为每个集成测试提供一个从迁移过的模板库克隆出来的独立 PostgreSQL 数据库、一个
Redis 键前缀和一个 ClickHouse 数据库。连接串取自 `SPINNERET_TEST_DATABASE_URL`、
`SPINNERET_TEST_REDIS_URL` 和 `SPINNERET_TEST_CLICKHOUSE_URL`，`Makefile` 已按基础设施栈的端口导出：

```text
postgres://spinneret:spinneret@localhost:45432/spinneret?sslmode=disable
redis://localhost:46379/0
clickhouse://spinneret:spinneret@localhost:49000/default
```

变量未设置时，夹具会用 testcontainers-go 启动一次性容器，每个测试二进制一次。所有夹具在 `-short` 下都
会 `t.Skip`，所以 `make test-short` 什么都不依赖，是写代码时最合适的循环。推送之前跑一次 `make test`
或 `make test-race`。

### Compose 端到端套件

```bash
make e2e
```

会构建镜像，叠加 `deploy/compose/docker-compose.e2e.yml` 启动栈（代理健康检查间隔缩短到 5 秒，并带上
模拟目标），然后在 Compose 网络内部执行 `go test -tags e2e ./test/e2e/...`。没有任何环节是假的：租约
经过负载均衡获取，请求真的经模拟 HTTP 代理打到模拟站点，上报真的穿过 Redis stream 进入 worker，断言
读的是管理 API、Redis 热状态和 ClickHouse。

每次运行都通过管理 API 创建自己的命名空间、站点、身份类型、身份、代理、策略、令牌和通知渠道，因此不同
运行之间不会冲突，也不依赖任何种子数据；成功的运行会把自己建的东西全部删掉。`SPINNERET_E2E_KEEP=1`
可以保留，失败的运行总是保留。套件读取的全部环境变量记录在 `test/e2e/doc_test.go`，场景清单在
`test/e2e/README.md`。仅爬虫模拟一项就会跑满 `SPINNERET_E2E_CRAWL_DURATION`（默认 60 秒）。

还有两个演练用同一套栈：`make e2e-failover` 在 acquire/report 负载下停掉一个副本，错误率或上报积压超过
阈值就失败；`make example` 种下示例站点并驱动 FastAPI 示例节点。

### 控制台测试

```bash
cd web
pnpm test                          # Vitest、jsdom、src/**/*.test.{ts,tsx}
pnpm typecheck && pnpm lint        # tsc -b --noEmit、ESLint
```

Playwright 用例驱动真实控制台，针对一个运行中的部署：

```bash
make up                            # 栈必须处于运行状态
make e2e-web                       # 先安装匹配的 Chromium，再跑套件
make e2e-web ARGS='-g "sites"'     # 只跑一条
make e2e-web ARGS='--headed'       # 可视化观察
```

`make e2e-web` 从 `deploy/compose/.env` 读取管理员凭据；把 `SPINNERET_UI_URL` 指向别处就能测别的部署，
包括 5173 端口上的 Vite 开发服务器。套件串行执行（`workers: 1`），因为各 spec 共用一个命名空间；每个
spec 都创建唯一命名的资源并在结束时删除，所以可以反复对同一个长期部署运行。每个 spec 覆盖什么，见
`web/e2e/README.md`。其中 `screenshots.spec.ts` 负责生成 `documents/images/` 下的截图。

### Python SDK

```bash
cd sdk/python
. .venv/bin/activate
pytest -q --cov=spinneret
ruff check . && ruff format --check .
mypy src
```

`make python-test` 在 `sdk/python` 下执行 `python3 -m pytest -q`，所以要先激活虚拟环境，否则会用到没有
测试依赖的系统解释器。测试使用 `respx` 和 `httpx.MockTransport`，完全不访问网络。SDK 必须在 Python 3.9
上继续可用。

### 压测与性能

`test/load/` 放着 acquire/report 循环、上报接入和 `WatchConfig` 长轮询的 k6 场景，以及 `run.sh` —— 它在
场景前后各取一次服务端指标快照，把全部结果写进 `.loadtest/<名称>/`。`test/perf/` 放着带 `perf` 构建标签
的 Redis 侧微基准，因此 `go build ./...` 和 `go test ./internal/...` 永远不会编译它们：

```bash
export SPINNERET_TEST_REDIS_URL='redis://localhost:46379/0'
go test -tags perf -timeout 60m ./test/perf/ -run XXX -bench BenchmarkAcquire \
  -benchtime 1x -perf.ops 3000 -perf.slowlog=false
```

两个目录都有 README，讲清楚怎样才能测出有意义的数字 —— 先预热站点、两次之间要等系统沉降、哪个仪器才是
权威。实测结果在[性能与调优](./17-performance.md)。改动 Lua 热路径的提交应当附上一份来自 `test/perf`
的前后对照表，在一台空闲机器上背靠背测出来 —— 同一份代码两次运行之间的漂移就有约 ±8%。

---

## Makefile 目标

| 目标 | 执行内容 |
| --- | --- |
| `make help` | 默认目标：分组列出全部任务，每个一行 |
| `make all` | 先 `generate`，再 `build` |
| `make generate` | `proto` 与 `sqlc` |
| `make proto` | `./scripts/buf-generate.sh` —— `buf lint` 加 `buf generate` |
| `make sqlc` | `./scripts/sqlc-generate.sh` —— 在根目录执行 `sqlc generate`，因此只覆盖 `internal/store/postgres/db` |
| `make build` | 构建静态的 `bin/spinneret-server` 和 `bin/spnr`，并写入版本号 |
| `make test` | `go test -count=1 ./...` |
| `make test-short` | `go test -short -count=1 ./...` —— 集成夹具自行跳过 |
| `make test-race` | `go test -race -count=1 ./...` |
| `make cover` | 对 `./internal/...` 生成 `coverage.out` 并打印总覆盖率 |
| `make vet` | `go vet ./...` |
| `make lint` | `golangci-lint run ./...` |
| `make fmt` | 对 `gen/` 以外所有纳入版本控制的 `.go` 文件执行 `gofmt -w` |
| `make infra-up` / `make infra-down` | 启动（并等待健康）或连同数据卷销毁 PostgreSQL + Valkey + ClickHouse 测试栈 |
| `make web-install` | 在 `web/` 下执行 `pnpm install --frozen-lockfile` |
| `make web` | 在 `web/` 下执行 `pnpm build` —— 服务端内嵌控制台的前提 |
| `make web-test` | 控制台门禁：在 `web/` 下执行 `pnpm typecheck`、`pnpm lint`、`pnpm format:check` 和 `pnpm test` |
| `make docker` | 构建镜像，打上 `spinneret:$(VERSION)` 和 `spinneret:local` 两个标签 |
| `make up` / `make down` | 启动 / 停止完整 Compose 栈 |
| `make e2e` | 在 Compose 网络内运行 Go 端到端套件 |
| `make e2e-web` | 运行 Playwright 控制台套件（`ARGS=…` 透传 Playwright 参数） |
| `make e2e-failover` | 副本故障切换演练 |
| `make example` | 种下示例站点并对栈运行 FastAPI 示例节点 |
| `make example-test` | 示例节点的单元测试 |
| `make load` | 通过 `loadtest` profile 运行一个 k6 场景 |
| `make python-test` | Python SDK 测试 |
| `make clean` | 删除 `bin`、`dist` 和 `coverage.out` |

`VERSION` 默认取 `git describe --tags --always --dirty`；`GOBIN` 默认是 `$(go env GOPATH)/bin`，并被
前置到 `PATH`。

---

## 质量门禁

`.github/workflows/ci.yml` 在每个 Pull Request 以及推送到 `main` 时运行。它有四个 job，全部通过才能
合并。

### `go`

以 PostgreSQL 17、Valkey 8 和 ClickHouse 25.8 作为服务容器运行，`SPINNERET_TEST_*` 变量指向它们。

| 步骤 | 命令 |
| --- | --- |
| 检查生成代码是否最新 | 安装 `buf@v1.73.0`、`sqlc@v1.31.1`、`protoc-gen-go@latest`、`protoc-gen-connect-go@latest`，执行 `buf lint && buf generate`，对 `web/` 以外的每个 `sqlc.yaml` 执行 `sqlc generate`，最后 `git diff --exit-code -- gen internal` |
| Vet | `go vet ./...` |
| Lint | golangci-lint v2.13.2，参数 `--build-tags e2e --timeout 10m` |
| Test | `go test -race -count=1 -skip 'TestStart.*Container' ./...` |

被跳过的是 `internal/testutil` 里那三个验证 testcontainers 回退路径的测试；CI 已经直接提供了服务，它们
无事可做。注意 lint 步骤带上了 `e2e` 构建标签，所以 `test/e2e` 也在检查范围内 —— 本地也要这样跑。

### `web`

在 `web/` 下，使用 Node 22 与 pnpm：`pnpm install --frozen-lockfile`，然后 `pnpm typecheck`、
`pnpm lint`、`pnpm test`、`pnpm build`。其中不包含 `pnpm format:check` —— 跑它的是 `make web-test`，
而 Pull Request 模板要求控制台有改动时执行 `make web-test`，所以 Prettier 的格式漂移要靠你自己发现，
而不是靠 CI。

### `python-sdk`

在 `sdk/python/` 下，在 Python 3.9、3.12、3.13 上：`pip install -e '.[dev]'`、`pytest -q`、
`ruff check . && ruff format --check .`，以及仅在 3.12 上执行的 `mypy src`。

### `image`

在 `go` 和 `web` 通过之后：用 buildx 构建 `deploy/docker/Dockerfile`，打标签 `spinneret:ci`，不推送。

发布是另一个工作流。`.github/workflows/release.yml` 由 `v*` 标签触发，用同一份 Dockerfile 构建
amd64 与 arm64 两个架构并推送到 `ghcr.io/tikhub/spinneret`；仓库里只有它会发布镜像。Pull Request 和
推送到 `main` 都不会推送任何东西。

### CI 不跑什么

Compose 端到端套件、故障切换演练、Playwright 用例、k6 压测场景和 `perf` 基准都需要一套已部署的栈，
**不在** CI 里。你的改动可能影响哪一项，就在本地跑哪一项，并在 Pull Request 里写明。

### 推送之前

```bash
make proto
for f in $(find . -name sqlc.yaml -not -path './web/*'); do sqlc generate -f "$f"; done
(cd web && pnpm gen) && git diff --exit-code -- gen internal web/src/gen
make vet
golangci-lint run --build-tags e2e ./...
make test-race
make web-test && (cd web && pnpm build)
(cd sdk/python && . .venv/bin/activate && ruff check . && ruff format --check . && mypy src && pytest -q)
```

---

## 代码约定

### 注释里的 `spec §N` 是什么

`internal/` 里的注释会引用小节编号 —— `spec §6.6`、`design doc §8.5`。它们指向 Spinneret 据以实现的
工程规格文档，而那份文档**没有发布**：它在仓库作者本地的 `docs/` 目录里，被 `.gitignore` 排除。已发布
的代码树不依赖它，你改代码也完全不需要它。

把这些标记当成它本来的样子：作者用来让上百个 Lua 和 Go 文件保持一致的一套稳定速记。当你需要某个标记
所指的行为时，已发布的[文档](../README.zh-CN.md)里有 —— 模型看[核心概念](./04-concepts.md)，
规则看[策略](./08-policies.md)，协议看[节点 API 参考](./13-node-api.md)。你自己写注释时，优先描述
不变量而不是引用小节编号，并且永远不要加指向 `docs/` 的链接。

### 通用

- **注释、标识符、提交信息和日志文本一律用英文。** 产品是双语的，源码不是。仓库里唯一的中文是面向用户
  的文案：`zh-CN` 控制台语言文件、`documents/zh/`，以及各个 `*.zh-CN.md`。
- **不出现任何品牌或平台名称。** Spinneret 与具体站点无关。示例一律使用中性占位名 —— `example-site`、
  `search`、`detail`、`partner-api`。代码、夹具、种子数据、测试和文档同样适用。
- **绝不记录密钥。** 载荷字段、代理凭据、API 令牌、会话 ID 和 KEK 材料不得出现在任何一行日志、任何一个
  指标标签、任何一条错误信息或任何一份测试夹具输出里。

### Go

- 不使用包级可变全局变量，指标注册和内嵌文件除外。
- 凡是做 I/O 的导出函数，第一个参数都是 `context.Context`。
- 用 `%w` 加上下文包装错误：`fmt.Errorf("load site %s: %w", id, err)`。会被客户端看到的错误统一走
  `internal/apperr`，它携带 Connect code、通过 `Spinneret-Reason` 响应头返回的机器可读 reason，以及重试
  提示。新增一个 reason 意味着同时改 `apperr`、[故障排查](./18-troubleshooting.md)和
  [节点 API 参考](./13-node-api.md)里的错误表。
- 构造函数显式接收依赖。不用服务定位器，不用全局注册表。接口要小，并且由使用方定义，而不是由实现方
  定义。
- 优先使用不可变值。catalog 快照就是范本：构建一次、永不修改，失效时整体替换，因此读取方不需要加锁。
  凡是能被多个 goroutine 触及的东西，要么不可变，要么显式加同步。
- 保持文件聚焦。仓库里手写的 Go 文件没有超过约 700 行的；接近这个量级时，请按职责拆分包，而不是把文件
  养大。生成文件（`gen/`、`*db/`）不在此列，并且从不手改。
- 测试写成表驱动并使用 `testify/require`。集成测试使用 `internal/testutil`，测试之间不共享状态。
- `gofmt` 和 `goimports` 由 golangci-lint 的 formatter 强制执行；`make fmt` 可以一次格式化整棵树。
  除标准检查集外，`.golangci.yml` 还启用了 `bodyclose`、`errorlint`、`gosec`、`misspell`、`nilerr`、
  `noctx`、`rowserrcheck`、`sqlclosecheck`、`unconvert` 和 `wastedassign`。`gen/` 和生成的 `*db/` 包被
  排除，`_test.go` 免检 `gosec`、`noctx` 和 `bodyclose`，`test/` 免检 `gosec` 和 `noctx`。

### 控制台

完整约定在 `web/README.md`，第一次改动最容易踩到的几条：

- 只通过 `@/lib/clients` 里的类型化 client 调用 API，并且放在 React Query 里；每个命名空间级请求都要带上
  当前命名空间名。
- 用 `useScopedQueryKey()` 构造 query key，事件与缓存失效才能命中正确的数据；分页列表用
  `useScopedPlaceholder()`，切换范围时才不会短暂显示另一个租户的数据。
- 每一条用户可见文案都走 i18next。新增一个 key 必须**同时**在
  `web/src/i18n/locales/en/<区域>.json` 和 `web/src/i18n/locales/zh-CN/<区域>.json` 的同一文件、同一路径
  下加上。十七个语言文件两边都存在，结构必须保持一致。
- 测试中通过 role、label 或可访问名称定位组件。无法这样定位的控件是组件本身的缺陷，不是使用 CSS
  选择器的理由。

### Python SDK

Ruff（行宽 100，`target-version = "py39"`）、`ruff format`，以及对 `src` 开启 pydantic 插件的 `mypy`
严格模式。`pytest` 配置了 `filterwarnings = ["error"]`，因此新出现的警告会让测试失败。公开接口必须在
Python 3.9 到 3.13 上都能原样工作。

---

## 文档

发布的文档是 `documents/` 加上根目录的 `README.md` 和 `README.zh-CN.md`。`docs/` 是本地工作笔记，不纳入
git，也绝不能被任何随项目发布的内容链接。

**对照规则：`documents/en/NN-x.md` 和 `documents/zh/NN-x.md` 在同一个提交里一起改。** 这两份文件是同一
篇文档的两种语言 —— 相同顺序的相同章节、相同行的相同表格、相同的命令和相同的示例。中文页不是英文页的
机器翻译；命令名、路径、环境变量、字段名、代码和 URL 在中文页里保持原样。仓库里每一份 `*.zh-CN.md` 同样
适用这条规则：根 README、`sdk/go/README.md`、`sdk/python/README.md` 和示例节点的 README。

改变行为的改动要在同一个 Pull Request 里更新文档。特别是：

| 你改了 | 同时要改 |
| --- | --- |
| 某个 `SPINNERET_*` 变量或其默认值 | [配置参考](./03-configuration.md) |
| `.proto` 的消息、字段或 RPC | [节点 API 参考](./13-node-api.md)，以及两个 SDK |
| 某个 `spnr` 命令或参数 | [命令行工具](./15-cli.md) |
| `apperr` 里的某个错误 reason | [故障排查](./18-troubleshooting.md)和[节点 API 参考](./13-node-api.md) |
| 某个 Prometheus 指标 | [可观测性与告警](./12-observability.md) |
| 某个 Compose 服务、端口或 profile | [安装与部署](./02-installation.md) |
| 某个控制台页面或其导航项 | [控制台总览](./05-console-overview.md)和对应的功能页 |
| 新的概念或术语 | [核心概念](./04-concepts.md)和[常见问题与术语表](./21-faq.md)里的术语表 |

`documents/images/` 下的截图由 `web/e2e/screenshots.spec.ts` 以 1440×900 生成，不是手工截的。需要更新时
重新跑那个 spec，不要自己裁一张新图。

---

## 提交与 Pull Request

提交标题遵循 conventional commits：

```text
<type>: <祈使语气的简要说明，小写开头，结尾不加句号>

<正文：为什么需要这次改动、它做了什么，按 72 列折行。
列出涉及的模块。说明迁移、新增的环境变量，以及运维人员会察觉到的行为变化。>
```

在用的 type：`feat`、`fix`、`refactor`、`perf`、`docs`、`test`、`chore`、`ci`。标题控制在约 72 个字符
以内，一个提交只做一件事 —— 一次重构和它所支撑的功能应当是两个提交。

一个 Pull Request 应当：

- 从主题分支提向 `main`，只覆盖一个主题；
- 先讲问题，再讲方案；
- 列出你跑过的门禁；如果改动可能影响 CI 不跑的那几项（`make e2e`、`make e2e-web`、
  `make e2e-failover`、`make load`、`perf` 基准），明确点名说明；
- 输入文件有变更时，附带重新生成的 `gen/`、`internal/**/db/` 和 `web/src/gen` 产物；
- 附带配套的 `documents/en/` 与 `documents/zh/` 更新；
- 只要运维人员或节点能感知到这次改动 —— 新变量、新 RPC、默认值变化、修掉的 bug —— 就在 `CHANGELOG.md`
  的 `## [Unreleased]` 一节下加一条。该文件遵循 Keep a Changelog，所以条目归入 `Added`、`Changed`、
  `Fixed` 或 `Removed`；
- 用单独一段说明新增的迁移、新增的 `SPINNERET_*` 变量、新增的权限或变更的默认值，因为这些是运维人员
  必须采取行动的部分；
- 控制台可见变化附前后截图，热路径改动附前后基准对照表。

`.github/pull_request_template.md` 会替你把描述填好，它的三节正好对应上面那些要点：*What and why*
是先问题后方案，*How it was verified* 是跑过的门禁清单，*Checklist* 是双语文档、changelog 条目和
重新生成的代码。没跑过的项就留着不勾，别勾一个你没做的 —— 诚实的空缺是可以评审的，勾错的不是。

贡献以仓库的 Apache 2.0 许可证（`LICENSE`）提交。

---

## 提出较大的改动

凡是改变数据模型、线上 API、热路径或安全模型的改动，先开 issue，而不是直接提 Pull Request。要开的表单是
`.github/ISSUE_TEMPLATE/feature_request.yml`，它问的就是下面这几项，顺序也一样。请说明：

1. **问题**，用「运维人员或节点今天做不到什么」来描述。
2. **模型变化** —— 新增的实体、策略种类、RPC 或权限 —— 以及它如何融入[核心概念](./04-concepts.md)里
   已有的词汇体系。
3. **协议影响。** `buf.yaml` 声明了 `breaking: use: FILE`：API 必须保持向后兼容。新增字段或 RPC 可以；
   重命名、改字段号、删除都不可以，改变某个已有取值的含义同样不可以。现场的节点跑的是旧版 SDK。
4. **Schema 影响** —— 迁移本身、能否在线执行、以及部署回滚时会发生什么。
5. **热路径影响。** 改动 `internal/scheduler`、`internal/worker` 或任何 `*.lua` 脚本要靠测，不靠讲：
   写明你会报告哪些 `test/perf` 区块、会跑哪个压测场景。基线见[性能与调优](./17-performance.md)。
6. **安全影响** —— 是否引入了凭据、密钥或载荷离开系统的新途径。见[安全](./19-security.md)。

当前版本刻意不做的功能列在根目录 `README.md` 末尾；提议之前先看一眼，如果你认为某一项应当提前，请说明
是什么变了。

安全漏洞不要发在 issue 里。按[安全](./19-security.md)描述的方式上报。

---

## 下一步

- [核心概念](./04-concepts.md) —— 读代码之前，先掌握代码使用的词汇
- [命令行工具](./15-cli.md) —— 开发过程中会用到的每一个 `spnr` 命令
- [配置参考](./03-configuration.md) —— 服务端读取的每一个变量
- [节点 API 参考](./13-node-api.md) —— `.proto` 定义的线上契约
- [性能与调优](./17-performance.md) —— 任何热路径改动都要对照的基线
- [安装与部署](./02-installation.md) —— 你的测试所针对的那套栈
