# 命令行工具

**Spinneret 两个可执行程序的完整参考：管理用 CLI `spnr`，以及控制平面实例 `spinneret-server`。涵盖每一个命令、参数、默认值、退出码和信号。**

[English](../en/15-cli.md)

---

## 目录

- [两个可执行程序](#两个可执行程序)
- [spnr](#spnr)
  - [spnr 如何获取配置](#spnr-如何获取配置)
  - [全局参数](#全局参数)
  - [退出码](#退出码)
  - [命令一览](#命令一览)
- [spnr migrate](#spnr-migrate)
- [spnr admin](#spnr-admin)
- [spnr token](#spnr-token)
- [spnr config](#spnr-config)
- [spnr kek](#spnr-kek)
- [spnr rebuild](#spnr-rebuild)
- [spnr seed](#spnr-seed)
- [spnr healthcheck](#spnr-healthcheck)
- [spnr version](#spnr-version)
- [spnr completion](#spnr-completion)
- [对已部署环境执行 spnr](#对已部署环境执行-spnr)
- [spinneret-server](#spinneret-server)
  - [参数](#参数)
  - [环境变量](#环境变量)
  - [角色](#角色)
  - [启动过程](#启动过程)
  - [信号、关停与排空](#信号关停与排空)
  - [服务端退出码](#服务端退出码)
- [我想做某件事](#我想做某件事)

---

## 两个可执行程序

| 程序 | 作用 | 位置 |
| --- | --- | --- |
| `spinneret-server` | 运行一个控制平面实例：节点 API、控制台、后台 worker。常驻进程。 | 容器内 `/usr/local/bin/spinneret-server`（镜像的 `ENTRYPOINT`）；`make build` 之后为 `bin/spinneret-server` |
| `spnr` | 管理 CLI：数据库迁移、第一个管理员、API 令牌、热状态重建、密钥加密密钥、压测数据、容器健康检查。一次性执行。 | 同一镜像内的 `/usr/local/bin/spnr`；`make build` 之后为 `bin/spnr` |

两者由同一份代码构建、打进同一个镜像，所以任何 Spinneret 容器都能执行其中任何一个。两者读取同一套 `SPINNERET_*` 环境变量。

```bash
# 把两个程序构建到 ./bin
make build
```

---

## spnr

```text
spnr administers a Spinneret deployment: database migrations, the first administrator,
API tokens, hot-state rebuilds, key-encryption keys and load-test data.

It reads the same SPINNERET_* environment variables as spinneret-server.

Usage:
  spnr [command]

Available Commands:
  admin       Administer console users
  completion  Generate the autocompletion script for the specified shell
  config      Inspect the server configuration
  healthcheck Probe an HTTP health endpoint (exit 0 on 2xx)
  help        Help about any command
  kek         Manage key-encryption keys
  migrate     Manage the PostgreSQL schema
  rebuild     Rebuild the Redis hot state from PostgreSQL
  seed        Create load-test and end-to-end test data (idempotent)
  token       Manage API tokens
  version     Print the spnr version
```

### spnr 如何获取配置

`spnr` 没有配置文件，也没有连接参数。它读取自身进程的 `SPINNERET_*` 环境变量——和 `spinneret-server` 读取的完全是同一套，详见[配置参考](./03-configuration.md)。

各命令需要的配置多少不同。大多数只需要数据库，因此在整套环境还没起来之前就能执行：

| 命令 | 必需 | 可选 |
| --- | --- | --- |
| `migrate up` / `down` / `status` | `SPINNERET_DATABASE_URL` | — |
| `admin init` | `SPINNERET_DATABASE_URL` | `SPINNERET_REDIS_URL` 或 `SPINNERET_REDIS_ADDRS`、`SPINNERET_REDIS_PREFIX`（用于通知运行中的实例） |
| `token create` | `SPINNERET_DATABASE_URL` | — |
| `kek status` / `kek rewrap` | `SPINNERET_DATABASE_URL`，以及 `SPINNERET_KEK_FILE` 或 `SPINNERET_KEKS` | `SPINNERET_KEK_CURRENT` |
| `rebuild` | `SPINNERET_DATABASE_URL`，以及 `SPINNERET_REDIS_URL` 或 `SPINNERET_REDIS_ADDRS` | `SPINNERET_REDIS_PREFIX` |
| `config check` | 完整的服务端配置 | — |
| `seed` | 完整的服务端配置（数据库、Redis 和 KEK 设置） | — |
| `kek generate`、`healthcheck`、`version`、`completion` | 无 | — |

只连数据库的命令使用一个宽松的加载器，有两个细节值得注意：

- 这类命令的连接池只开 **4** 条连接，而不是服务端的 32 条。`SPINNERET_DATABASE_MAX_CONNS` 对**每一个**只连数据库的命令都生效——`migrate`、`admin init`、`token create`、`kek status`、`kek rewrap` 和 `rebuild` 都走同一个建池辅助函数——取值必须是 `>= 2` 的整数。
- `SPINNERET_REDIS_PREFIX` 的默认值是 `sp`，与服务端一致。前缀填错的 `rebuild` 会重建出一份没人读的热状态。

`config check` 和 `seed` 加载并校验**完整**配置，因此任何一个变量不合法它们都会失败——这正是 `config check` 适合在部署前跑一遍的原因。

### 全局参数

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `--log-level` | `warn` | 写到 **stderr** 的诊断日志级别：`debug`、`info`、`warn`、`error`。日志为纯文本；命令结果始终写到 **stdout**。 |
| `-h`、`--help` | — | 打印该命令的帮助，退出码 0。 |

`-v` / `--version` **不是**全局参数：它只存在于根命令上。`spnr --version` 会打印 `spnr <version>` 并以 0 退出，但 `spnr migrate --version` 会报 `spnr: unknown flag: --version` 并以 1 退出。脚本里要取版本号请用 `spnr version`。

因为结果走 stdout、诊断走 stderr，令牌或 JSON 结果可以安全地捕获：

```bash
TOKEN="$(spnr token create --name crawler-a --scope lease:acquire --scope report:write)"
```

### 退出码

| 退出码 | 含义 |
| --- | --- |
| `0` | 成功。`--help` 以及 `admin init` 发现管理员已存在时也是 0。 |
| `1` | 任何失败：参数不合法、命令不存在、环境变量缺失、连接失败、`healthcheck` 探测失败、`kek rewrap` 带错误结束。 |

任何失败都会在 stderr 打印一行以 `spnr: ` 开头的信息：

```text
spnr: SPINNERET_DATABASE_URL is required
```

```text
spnr: invalid configuration:
SPINNERET_DATABASE_URL is required
SPINNERET_REDIS_URL or SPINNERET_REDIS_ADDRS is required
SPINNERET_KEK_FILE or SPINNERET_KEKS is required
```

### 命令一览

| 命令 | 用途 |
| --- | --- |
| `spnr migrate up` | 应用所有待执行的迁移。 |
| `spnr migrate down [--to N]` | 回滚一个迁移，或回滚到版本 `N`。 |
| `spnr migrate status` | 比较已应用的库表版本与程序内置的版本。 |
| `spnr admin init` | 创建第一个平台管理员，以及一个租户和一个命名空间。 |
| `spnr token create` | 创建 API 令牌并打印其明文。 |
| `spnr config check` | 校验环境变量，并以脱敏形式打印出来。 |
| `spnr kek generate` | 打印一行新的随机 KEK。 |
| `spnr kek status` | 显示已配置的 KEK、各自还包裹着多少条记录，以及重新包裹的进度。 |
| `spnr kek rewrap` | 用当前 KEK 重新包裹所有数据密钥。 |
| `spnr rebuild` | 从 PostgreSQL 重建 Redis 热状态。 |
| `spnr seed` | 创建带身份、策略和节点令牌的合成压测站点。 |
| `spnr healthcheck` | 探测一个 HTTP 健康端点，2xx 时退出码为 0。 |
| `spnr version` | 打印构建版本。 |
| `spnr completion <shell>` | 打印 shell 补全脚本。 |

---

## spnr migrate

应用、回滚或查看**内置在程序里**的数据库迁移。没有额外的迁移目录需要随程序分发。并发执行由 PostgreSQL 咨询锁串行化，所以多个实例或多个运维人员同时发起迁移是安全的。

只需要 `SPINNERET_DATABASE_URL`。

下面示例里的版本号是本次发布的真实数字：程序内置 **5** 个迁移，所以迁移完毕的数据库处于版本 5。之后每个新增迁移的版本都会把这个数字往上抬；用 `spnr migrate status` 查看你手上这个程序期望的版本。

### `spnr migrate up`

应用所有待执行的迁移，并打印结果版本。

```bash
spnr migrate up
```

```text
database schema is at version 5
```

没有待执行迁移时它什么也不做，并打印同样一行。

### `spnr migrate down`

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `--to` | 未设置 | 一直回滚到库表版本等于该数字。`0` 表示移除全部迁移。必须 `>= 0`，且不能高于当前版本。 |

不带 `--to` 时只回滚一个迁移——最后应用的那个。

```bash
spnr migrate down           # 回退一步
spnr migrate down --to 4    # 回退到版本 4
```

```text
database schema is at version 4
```

当目标版本等于当前版本时，它打印 `database schema is already at version N` 且不做任何改动。`--to` 高于当前版本会报错：

```text
spnr: --to 9 is above the current schema version 5 (use migrate up)
```

**警告。** 回滚会删除表和其中的数据。先做备份，见[运维手册](./16-operations.md)。

### `spnr migrate status`

打印一行，描述已应用的库表版本与程序内置版本的关系：

```text
database schema version 5 (up to date)
database schema version 3, binary version 5 (2 pending: run spnr migrate up)
database schema version 6 is newer than this binary (5)
```

第三种情况出现在滚动降级过程中；此时服务端只记一条告警并继续运行，不会拒绝启动。

---

## spnr admin

只有 `init` 一个子命令。

### `spnr admin init`

创建**第一个**平台管理员，同时创建一个租户和一个命名空间，并为该命名空间安装默认策略。这是全新部署的引导步骤；之后的用户都在控制台里创建，见[租户、用户与令牌](./11-access-control.md)。

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `--username` | — | 管理员用户名。必填。会被转小写并去除首尾空白；必须是 3–64 个 `a-z`、`0-9`、`.`、`_` 或 `-` 字符，且以字母或数字开头。 |
| `--password-env` | — | 存放密码的环境变量名。 |
| `--password-stdin` | `false` | 从 stdin 的第一行读取密码。 |
| `--tenant` | `default` | 要创建或复用的租户。2–63 个 `a-z`、`0-9` 或 `-` 字符，且必须以字母或数字开头。 |
| `--namespace` | `default` | 要创建或复用的命名空间。规则同租户。 |

`--password-env` 与 `--password-stdin` 互斥，且必须二选一。密码长度为 10–1024 个字符。故意没有提供 `--password` 参数：命令行上的密码会留在 shell 历史和 `ps` 里。

**幂等。** 当平台管理员已存在时，命令打印 `already initialized` 并以 **0** 退出。这使它可以安全地放进安装脚本、Compose 一次性服务或重启循环中。

当设置了 `SPINNERET_REDIS_URL`（或 `SPINNERET_REDIS_ADDRS`）时，运行中的实例会被立即通知重新加载这个新命名空间。没有 Redis 命令同样成功，实例会在一分钟内自行发现。

```bash
# 从环境变量读取
SPINNERET_ADMIN_PASSWORD='a-long-passphrase' \
  spnr admin init --username admin --password-env SPINNERET_ADMIN_PASSWORD

# 从 stdin 读取
printf '%s\n' "$PASSWORD" | spnr admin init --username admin --password-stdin
```

```text
created platform administrator admin (018f2c1e-1c1c-7c9e-9a1e-9f1b2c3d4e5f) in tenant default, namespace default
```

---

## spnr token

只有 `create` 一个子命令。令牌也可以在控制台里创建；提供 CLI 是为了让安装脚本或 CI 任务只凭一个数据库 URL 就能签出节点令牌。

### `spnr token create`

创建一个绑定到某个租户与命名空间的 API 令牌，并在 stdout 上**只打印明文令牌**。明文不会被存储，之后也无法再取回——当场记下来，否则就丢了。

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `--tenant` | `default` | 租户名。 |
| `--namespace` | `default` | 命名空间名。 |
| `--name` | — | 令牌名称，在该命名空间的可用令牌中唯一。必填。 |
| `--scope` | — | 授予该令牌的一个权限范围。可重复，至少一个。重复项会被拒绝。 |
| `--expires` | `720h` | 有效期。支持 Go 时长（`720h`、`90m`）以及开头的天数分量（`30d`、`30d12h`）。`0`、`never`、`permanent` 或空值表示永不过期。 |
| `--description` | 空 | 自由描述，最多 512 字节。 |

权限范围：

| 权限范围 | 参数 | 授予的能力 |
| --- | --- | --- |
| `lease:acquire` | 可选，站点名 | `Acquire`、`Renew`、`Release` |
| `report:write` | 可选，站点名 | `Report`、`ReportBatch` |
| `config:read` | 可选，配置分组 glob | `GetConfig`、`WatchConfig` |
| `config:publish` | 可选，配置分组 glob | 配置项的读、写、发布 |
| `secret:read` | **必填** `<namespace>/<path glob>` | `GetSecret` |
| `identity:write` | 可选，站点名 | 身份的读、写、运维操作 |
| `proxy:write` | 无 | 代理的读、写、运维操作 |
| `admin` | 无 | 完整的管理员权限集 |

参数规则：参数跟在权限范围名后面，以冒号分隔，不能为空，最多 256 字节合法 UTF-8，不含空白和控制字符。站点名参数**不能**包含通配符 `*` 或 `?`——省略参数即表示允许所有站点。整个权限范围字符串最多 512 字节。每项权限具体覆盖什么，见[租户、用户与令牌](./11-access-control.md)。

```bash
spnr token create --tenant default --namespace default --name crawler-hk \
  --scope lease:acquire --scope report:write --scope config:read --expires 720h
```

```text
spn_0Xk9mQ2pZ7wR4tL1vB8nH5sD3fG6jC0aY9eU2iO7qK4
```

明文令牌的形状固定：`spn_` 加 43 个 base62 字符，总长 47。数据库里只保存前 12 个字符和一个 SHA-256 摘要，控制台的令牌列表显示的就是那 12 个字符。

只对 `example-site` 生效、且永不过期的节点令牌：

```bash
spnr token create --name node-example-site \
  --scope lease:acquire:example-site \
  --scope report:write:example-site \
  --scope 'config:read:crawler/*' \
  --expires never \
  --description 'nodes in rack 4'
```

---

## spnr config

只有 `check` 一个子命令。

### `spnr config check`

加载并校验完整的 `SPINNERET_*` 环境变量，然后以 JSON 打印出来，凭据部分脱敏。配置合法时退出码为 0，否则为 1 并列出所有问题。它不连接任何东西——这是一次纯粹的配置检查，所以在一台还没跑起来的机器上，它是最该先执行的命令。

```bash
spnr config check
```

```json
{
  "clickhouse_url": "clickhouse://spinneret:xxxxx@clickhouse:9000/spinneret",
  "database_url": "postgres://spinneret:xxxxx@postgres:5432/spinneret?sslmode=disable",
  "http_addr": ":8080",
  "instance_id": "node-1-bba039",
  "kek_current": "",
  "kek_file": "/run/secrets/kek",
  "keks": "",
  "late_window": "10m0s",
  "log_level": "info",
  "metrics_addr": "",
  "otlp_endpoint": "",
  "payload_cache": "true",
  "pprof_addr": "",
  "redis_addrs": "",
  "redis_prefix": "sp",
  "redis_url": "redis://valkey:6379/0",
  "report_dedup_ttl": "1h0m0s",
  "report_shards": "16",
  "role": "all",
  "tls": "false",
  "ui_enabled": "true"
}
```

上面这份示例来自 Compose 栈内的一次执行——它总是会设置 `SPINNERET_CLICKHOUSE_URL`。在没有配置 ClickHouse 的机器上，`clickhouse_url` 是空字符串，服务端启动时会打印 `clickhouse disabled: raw report events are not stored`。

URL 里的密码会被替换成 `xxxxx`，`keks` 只显示是否配置了内联密钥材料，绝不显示密钥本身。这份输出是运维最容易配错的那些设置的摘要，不是全部变量；完整清单见[配置参考](./03-configuration.md)。

---

## spnr kek

管理密钥加密密钥（KEK）——它包裹着密钥保管库以及每一份加密身份载荷所用的数据加密密钥（DEK）。概念部分见[密钥保管库](./10-secrets.md)；本节只讲命令。

### `spnr kek generate`

按 KEK 文件使用的 `id:base64` 格式打印一行新的随机密钥：32 个随机字节，标准 base64。完全不需要任何环境变量。

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `--id` | `k1` | 密钥 id。必须匹配 `^[a-zA-Z0-9_-]{1,32}$`。 |

```bash
spnr kek generate --id k2 >> deploy/compose/secrets/kek.key
```

```text
k2:sS1aF7AmODDr3+J1nT5oZIuUU2AnQ+/P3c8RM0E680Y=
```

KEK 文件每行一个密钥；空行和以 `#` 开头的行会被忽略。未设置 `SPINNERET_KEK_CURRENT` 时，列在**最后**的那个密钥就是当前密钥。

**警告。** 用某个 KEK 加密的数据，离开这个 KEK 就无法恢复。请把 KEK 文件与数据库备份分开备份，绝不要放在同一个地方。

### `spnr kek status`

显示已配置的 KEK、每个 KEK 目前还包裹着多少条存储记录，以及重新包裹作业的状态。需要 `SPINNERET_DATABASE_URL` 和 KEK 设置。

```bash
spnr kek status
```

```text
current kek: k2
  k1               wrapped_records=1043
  k2               wrapped_records=28711 current
rewrap: idle (28711/29754)
last finished: 2026-03-04T09:12:44Z
```

怎么读：`k2` 是当前密钥，已经包裹了 28711 条记录；还有 1043 条记录仍被 `k1` 包裹。`rewrap:` 那一行是作业状态——`idle` 或 `running`——后面括号里是正在运行或上一次作业的 `done/total`：`total` 是该作业启动时发现的待处理记录数，`done` 是它已经重新包裹的条数。这里上一次作业在 29754 条中重新包裹了 28711 条，所以 `k1` 上还剩 1043 条；再执行一次 `spnr kek rewrap` 就能收尾。

标记为 `NOT-CONFIGURED` 的密钥仍在数据库里包裹着记录，但已经不在 `SPINNERET_KEK_FILE` / `SPINNERET_KEKS` 中了。在把这个密钥放回去之前，那些记录无法解密。

### `spnr kek rewrap`

启动重新包裹作业——或者跟随另一个实例上已在运行的作业——并每秒打印一次进度，直到结束。

```bash
spnr kek rewrap
```

```text
kek rewrap started
progress: 4096/29754 records re-wrapped to k2
progress: 12288/29754 records re-wrapped to k2
progress: 29754/29754 records re-wrapped to k2
kek rewrap finished
```

如果另一个实例已经在跑，第一行会是 `kek rewrap already running on another instance; following its progress`。

作业报错结束时，命令以 1 退出并打印 `kek rewrap finished with errors: <message>`。用 Ctrl-C 中断命令会**连带停止作业**——它跑在这个进程里——并打印 `kek rewrap interrupted (the job stops with this process)`。重新执行会从中断处继续。

完整的轮换流程：

```bash
# 1. 加入新密钥并让它成为当前密钥
spnr kek generate --id k2 >> deploy/compose/secrets/kek.key
#    设置 SPINNERET_KEK_CURRENT=k2，或者不设置它、把 k2 放在文件最后一行

# 2. 重启所有实例，让它们都认识 k2

# 3. 重新包裹
spnr kek rewrap

# 4. 只有当这条命令显示 k1 的 wrapped_records=0 之后，才从文件里删掉 k1
spnr kek status
```

---

## spnr rebuild

从 PostgreSQL 重建 Redis 热状态——也就是调度器在每次 `Acquire` 时读取的那份工作集。PostgreSQL 始终是唯一事实来源。

需要 `SPINNERET_DATABASE_URL` 和 `SPINNERET_REDIS_URL`（或 `SPINNERET_REDIS_ADDRS`），并且 `SPINNERET_REDIS_PREFIX` 要与该部署一致。

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `--tenant` | `default` | `--site` 所属的租户。 |
| `--namespace` | `default` | `--site` 所属的命名空间。 |
| `--site` | 空 | 只重建这一个站点。 |

不带 `--site` 时，热状态的 epoch 键会被删除，**所有**站点都会重建。在此期间 API 实例会报告未就绪（`/readyz` → `hotstate: epoch missing (rebuild pending)`），负载均衡器会把它们摘掉。带 `--site` 时只重新物化那一个站点，部署的其余部分继续提供服务。

只给 `--tenant` 或 `--namespace` 而不给 `--site` 会被拒绝——它们只用来定位站点重建所在的命名空间：

```text
spnr: --tenant and --namespace select the namespace of --site; add --site for a site rebuild
```

```bash
# 全部重建（维护窗口）
spnr rebuild
```

```text
rebuilt hot state of every site in 12.418s
```

```bash
# 单站点，在线执行
spnr rebuild --tenant default --namespace default --site example-site
```

```text
rebuilt hot state of site default/default/example-site in 1.902s
```

什么时候用：恢复了 Redis 备份之后、Redis 被清空之后、改了前缀之后，或者热状态与数据库明显不一致时。操作流程见[运维手册](./16-operations.md)。

---

## spnr seed

创建——或补全——一个用于压测和端到端测试的合成站点。除节点令牌之外，其余对象都是幂等的；节点令牌每次都会重建（同名的旧令牌被吊销）。

需要**完整**的服务端配置，并且数据库库表版本必须是最新的。版本落后时命令会拒绝执行：

```text
spnr: database schema is at version 3, binary expects 5: run spnr migrate up
```

| 参数 | 默认值 | 取值范围 | 含义 |
| --- | --- | --- | --- |
| `--tenant` | `default` | — | 租户；不存在时创建。 |
| `--namespace` | `default` | — | 命名空间；不存在时创建。 |
| `--site` | `loadtest` | — | 站点名。 |
| `--client` | `web` | — | 客户端类型。 |
| `--groups` | `50` | 0–1000 | 端点组数量。 |
| `--identities` | `100000` | 0–5000000 | 身份数量。 |
| `--proxies` | `0` | 0–100000 | 代理数量；`0` 表示不创建代理。 |
| `--proxy-url` | `http://loadtest-{i}:loadtest@mocktarget:9091` | — | 代理 URL 模板，`{i}` 替换为代理序号。`--proxies > 1` 时必须包含 `{i}`，因为代理按 URL 去重。 |
| `--token-name` | 站点名 | — | 节点令牌的名称。 |

上面这张表列出了所有**可见**参数。还有一个参数因为只影响吞吐而被隐藏：`--chunk-size`（默认 `5000`，取值 1–50000）是每次导入调用的身份条数。数据库较弱时调小，较快时调大。

`--tenant`、`--namespace`、`--site`、`--client`、`--token-name` 的取值必须非空，且不含空格和斜杠。只要 `--proxies > 0`，`--proxy-url` 就必须非空；只要 `--proxies > 1`，它就必须包含 `{i}`。

它会创建：

| 对象 | 内容 |
| --- | --- |
| 端点组 | `g0` … `g{N-1}`，各带一条 URI 前缀规则 `/api/g<i>/` |
| 身份类型 | `loadtest_cookie`——字段 `cookies`（cookie map，必填，敏感）和 `user_agent`，按 `cookies.sessionid` 去重，立即激活 |
| 身份 | 确定性的合成数据，分批导入，`create_only` 模式 |
| 轮换策略 | `loadtest-rotation`——加权随机、候选采样 32、租约 TTL 60s、每个身份 1 个并发租约；`--proxies > 0` 时代理模式为 `pool`，否则为 `none` |
| 熔断策略 | `loadtest-breaker`——最小请求数设得极高，压测期间永远不会熔断 |
| 配置项 | `crawler/loadtest.json`，已发布 |
| 代理 | 按模板导入，kind 为 `datacenter`、provider 为 `loadtest`、标签 `loadtest` |
| 节点令牌 | 名称取自 `--token-name`，权限范围 `lease:acquire`、`report:write`、`config:read`，永不过期 |

进度写到 **stderr**，结果以 JSON 写到 **stdout**：

```bash
spnr seed --site loadtest --identities 100000 --groups 50
```

```json
{
  "token": "spn_0Xk9mQ2pZ7wR4tL1vB8nH5sD3fG6jC0aY9eU2iO7qK4",
  "site": "loadtest",
  "groups": 50,
  "identities": 100000
}
```

还有第五个字段 `proxies`，只在确实创建了代理时才出现——`--proxies` 为 `0` 时它会被省略：

```bash
spnr seed --site loadtest --proxies 100 --proxy-url 'http://lt-{i}:secret@mocktarget:9091'
```

```json
{
  "token": "spn_0Xk9mQ2pZ7wR4tL1vB8nH5sD3fG6jC0aY9eU2iO7qK4",
  "site": "loadtest",
  "groups": 50,
  "identities": 100000,
  "proxies": 100
}
```

只取出令牌用于一次压测：

```bash
LOADTEST_TOKEN="$(spnr seed --site loadtest 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')"
```

**注意。** `spnr seed` 写入的是真实的命名空间。请把它指向你愿意被合成数据填满的租户和命名空间。

---

## spnr healthcheck

发送一次 `GET`，响应状态是 2xx 就以 0 退出，否则以 1 退出。它之所以存在，是因为运行时镜像基于 distroless：没有 shell、没有 `curl`、没有 `wget`——容器 `HEALTHCHECK` 需要一个可执行程序。

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `--url` | `http://127.0.0.1:8080/readyz` | 要探测的 URL。必须是绝对的 `http` 或 `https` URL。 |
| `--timeout` | `3s` | 请求超时。必须为正。 |

不跟随重定向——健康端点从不重定向，跟着跳到别的主机会让检查失去意义。

```bash
spnr healthcheck --url http://127.0.0.1:8080/readyz
echo $?   # 0
```

失败时打印原因，URL 中的凭据已脱敏：

```text
spnr: probe http://127.0.0.1:8080/readyz: status 503
spnr: probe http://127.0.0.1:8080/readyz: Get "http://127.0.0.1:8080/readyz": dial tcp 127.0.0.1:8080: connect: connection refused
```

镜像里声明的正是这条命令：

```dockerfile
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/spnr", "healthcheck", "--url", "http://127.0.0.1:8080/readyz"]
```

---

## spnr version

```bash
spnr version
spnr --version
spnr -v
```

```text
spnr v0.1.0
```

发布构建在链接期注入版本号。没有注入的构建会退回到 Go 工具链记录的模块版本，再退回到 `dev-<提交号前 12 位十六进制>[-dirty]`，什么都拿不到时则为 `dev`。`spinneret-server --version` 以及服务端启动日志报告的是同一个字符串。

---

## spnr completion

打印 `bash`、`zsh`、`fish` 或 `powershell` 的 shell 补全脚本。

```bash
# zsh，当前用户
spnr completion zsh > "${fpath[1]}/_spnr"

# bash，当前 shell
source <(spnr completion bash)
```

`spnr completion <shell> --help` 会说明各个 shell 期望把文件放在哪里。

---

## 对已部署环境执行 spnr

### 在 Compose 栈内执行

用 Spinneret 镜像构建出来的每个服务都同时带着两个程序。执行一次性 `spnr` 命令时，请挑 **`migrate`** 这个服务：它与服务端共用镜像、`SPINNERET_*` 环境变量和 KEK secret，但只依赖 PostgreSQL，因此不会为一条根本用不到它们的命令把 Valkey 和 ClickHouse 也拉起来。它的 entrypoint 本来就是空的，但仍然要传 `--entrypoint`，这样才能替换掉该服务自带的 `spnr migrate up` 命令：

```bash
cd /path/to/Spinneret

# 用栈的环境变量，在一个新容器里执行一次性命令
docker compose -f deploy/compose/docker-compose.yml \
  run --rm --entrypoint /usr/local/bin/spnr migrate kek status

# 在已经在跑的副本里执行（distroless 没有 shell，必须写绝对路径）
docker compose -f deploy/compose/docker-compose.yml \
  exec --index 1 spinneret /usr/local/bin/spnr kek status
```

需要 `--index` 是因为 `spinneret` 服务会起 `SPINNERET_REPLICAS` 个副本（默认 2 个）。

`spinneret` 服务同样可以这么用——`run --rm --entrypoint /usr/local/bin/spnr spinneret <command>`——但它 `depends_on` `migrate`、`valkey` 和 `clickhouse`，Compose 会先把整套环境拉起来。只有确实需要这些依赖时才用它。

有两条命令已经做成了服务，不需要覆盖 entrypoint：

```bash
# spnr migrate up —— 服务端启动前也会自动跑一遍
docker compose -f deploy/compose/docker-compose.yml run --rm migrate

# spnr admin init，用 .env 里的 SPINNERET_ADMIN_USERNAME / SPINNERET_ADMIN_PASSWORD
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

### 在宿主机上执行

自己构建程序并提供环境变量。Compose 栈默认不把数据库端口发布到宿主机，因此宿主机上的常规做法是连你自己部署环境映射出来的端口，或者干脆按上面的方式在容器里跑 CLI。

```bash
make build

export SPINNERET_DATABASE_URL='postgres://spinneret:<password>@127.0.0.1:5432/spinneret?sslmode=disable'
export SPINNERET_REDIS_URL='redis://127.0.0.1:6379/0'
export SPINNERET_KEK_FILE=/etc/spinneret/kek.key

./bin/spnr migrate status
```

对于需要完整配置的命令，可以直接加载服务端用的那个 `.env`：

```bash
set -a; . ./deploy/compose/.env; set +a
```

**注意。** `deploy/compose/.env` 里放的是 `PG_PASSWORD` 和 `CLICKHOUSE_PASSWORD`，不是拼好的 URL——Compose 文件用它们拼出 `SPINNERET_DATABASE_URL`。在宿主机上你得自己拼这个 URL。

---

## spinneret-server

一个常驻的控制平面实例。它提供节点 API、管理 API、控制台，并根据角色运行后台 worker。

### 参数

```text
Usage: spinneret-server [--role all|api|worker] [--migrate] [--version]

Runs a Spinneret instance configured through SPINNERET_* environment variables.

Flags:
  -migrate
    	apply pending database migrations at startup
  -role string
    	instance role: all, api or worker (overrides SPINNERET_ROLE)
  -version
    	print the version and exit
```

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `--role` | 空 | `all`、`api` 或 `worker`。设置之后会覆盖 `SPINNERET_ROLE`。 |
| `--migrate` | `false` | 启动时应用待执行的迁移，而不是拒绝启动。 |
| `--version` | `false` | 打印 `spinneret-server <version>` 并以 0 退出。 |
| `-h`、`--help` | — | 打印上面那段用法并以 0 退出。 |

这些是 Go 风格的参数：`-role api`、`--role api` 和 `--role=api` 都可以。没有位置参数——传入任何位置参数都是错误。

**注意。** `--migrate` 对单实例部署很方便，对多实例部署则是个坑：多个实例同时启动会各自尝试迁移。迁移由咨询锁串行化，所以并不会出错，但干净的做法——也是 Compose 栈采用的做法——是用一个独立的 `spnr migrate up` 步骤，并要求它先于服务端完成。

### 环境变量

其余一切都来自 `SPINNERET_*` 环境变量。最小必需集合是：

| 变量 | 说明 |
| --- | --- |
| `SPINNERET_DATABASE_URL` | PostgreSQL 连接 URL。 |
| `SPINNERET_REDIS_URL` **或** `SPINNERET_REDIS_ADDRS` | Valkey/Redis。 |
| `SPINNERET_KEK_FILE` **或** `SPINNERET_KEKS` | 至少一个密钥加密密钥。 |

其余变量都有默认值或可选。`SPINNERET_HTTP_ADDR` 默认 `:8080`，`SPINNERET_ROLE` 默认 `all`，`SPINNERET_LOG_LEVEL` 默认 `info`，`SPINNERET_LOG_FORMAT` 默认 `json`，`SPINNERET_SHUTDOWN_TIMEOUT` 默认 `30s`。完整表格见[配置参考](./03-configuration.md)；`spnr config check` 可以校验它。

配置不合法时，会在连接任何东西之前把问题列出来：

```text
spinneret-server: invalid configuration:
SPINNERET_ROLE must be one of all, api, worker (got "leader")
SPINNERET_REPORT_SHARDS must be between 1 and 255
```

### 角色

| 角色 | 提供 API 与控制台 | 运行后台 worker |
| --- | --- | --- |
| `all`（默认） | 是 | 是 |
| `api` | 是 | 否 |
| `worker` | 否 | 是 |

`worker` 实例仍然会监听 `SPINNERET_HTTP_ADDR`，并提供 `GET /healthz`、`GET /readyz`，以及（`SPINNERET_METRICS_ADDR` 为空时）`GET /metrics`。除此之外什么都不提供：节点 API、管理 API 和控制台都不会挂载。所以 worker 的健康检查和指标抓取方式与 API 实例完全一样。

worker 指的是上报消费者和定时任务。一个部署里至少要有一个角色包含 worker 的实例，否则上报会堆在 Redis 流里，永远不会被判定。把两种角色拆开可以让它们各自独立扩缩容，见[运维手册](./16-operations.md)。

### 启动过程

1. 解析参数，然后加载并校验环境变量。
2. 打开 PostgreSQL 连接池，紧接着把库表版本与程序内置的迁移比较：
   - 相等——继续；
   - 数据库更新——记一条告警并继续（滚动降级）；
   - 数据库落后且未设置 `--migrate`——**启动失败**，报 `database schema is at version N, binary expects M: run spnr migrate up`；
   - 数据库落后且设置了 `--migrate`——先应用迁移，再继续。

   随后确保当前时间窗口的分区存在。这一整段都发生在接触 Redis 和 ClickHouse **之前**，所以库表版本不匹配会快速失败，而且这个失败与栈里其他组件无关。
3. 打开 Redis 连接；设置了 `SPINNERET_CLICKHOUSE_URL` 时再打开 ClickHouse（没设置则打印 `clickhouse disabled: raw report events are not stored`）；最后加载密钥加密密钥。
4. 在运行 worker 的实例上确保 Redis 热状态存在：epoch 键缺失时先从 PostgreSQL 重建。在此之前就绪状态一直为否。
5. 监听器启动，实例打印 `spinneret started`，带上自己的角色、地址和版本。

有两个 HTTP 端点报告状态，都不需要鉴权，且每种角色都会提供：

| 端点 | 行为 |
| --- | --- |
| `GET /healthz` | 只要进程还能提供 HTTP 服务就永远返回 `200 {"status":"ok"}`。用作存活探针。 |
| `GET /readyz` | 在 2 秒预算内并发执行 `postgres`、`redis`、`catalog`、`hotstate` 四项检查。全部通过返回 `200 {"status":"ok","checks":{…}}`；否则返回 `503 {"status":"unavailable","checks":{…}}`，逐项给出失败原因；关停期间返回 `503 {"status":"draining"}`。用作就绪探针和负载均衡器的健康检查。 |

### 信号、关停与排空

`spinneret-server` 处理 **SIGINT** 和 **SIGTERM**，两者都触发同一套优雅关停。**第二个**信号会立即终止进程——第一个信号之后默认处理方式就被恢复了，所以卡住的关停总能被强行打断。

关停流程，整体受 `SPINNERET_SHUTDOWN_TIMEOUT`（默认 `30s`）约束：

1. **就绪状态翻转为 draining。** `/readyz` 返回 `503 {"status":"draining"}`。`/healthz` 仍然返回 `200`，这样存活探针不会在排空中途把进程杀掉。
2. **停止 keep-alive。** 空闲的连接池连接被关闭，排空窗口内的每个响应都带 `Connection: close`，这样负载均衡器或 SDK 客户端就不会把请求发到一条本实例即将关闭的连接上。
3. **排空延迟。** 实例等待 `min(5s, SPINNERET_SHUTDOWN_TIMEOUT/4)`——默认超时下是 5 秒——给负载均衡器时间发现就绪检查失败并停止转发。如果关停是由监听器故障而非信号触发的，这段延迟会被跳过。
4. **HTTP 关停。** 进行中的请求跑完。长轮询（`WatchConfig`）和事件流会被取消，以免它们把窗口一直撑着。HTTP 分到剩余预算的一半；到期仍未结束的连接被关闭。
5. **后台循环按层停止**，让写入方把服务产出的东西刷盘——例如排队中的代理绑定会有最后 10 秒的刷写窗口。
6. 关闭指标与 pprof 服务，然后打印 `shutdown complete` 和耗时。

给编排器的宽限时间要长于 `SPINNERET_SHUTDOWN_TIMEOUT`，否则它会在第 4 步中间把进程杀掉。Compose 栈针对默认的 30 秒超时设置了 `stop_grace_period: 40s`；无论你用哪种编排器，都要把宽限时间设得比 `SPINNERET_SHUTDOWN_TIMEOUT` 更长。

```bash
# 手动排空一个实例
docker compose -f deploy/compose/docker-compose.yml stop --timeout 40 spinneret
```

### 服务端退出码

| 退出码 | 含义 |
| --- | --- |
| `0` | 收到信号后干净关停。`--help` 和 `--version` 也是 0。 |
| `1` | 配置不合法、启动失败（依赖不可达、库表版本落后且未加 `--migrate`），或运行中监听器出错。 |
| `2` | 命令行错误：未知参数或多余的位置参数。 |

`2` 属于部署配置错误——命令行不改，进程永远起不来，所以不要让守护进程无限重试。

---

## 我想做某件事

| 我想做 | 命令 |
| --- | --- |
| 在全新部署上建库表 | `spnr migrate up` |
| 升级前确认库表版本与程序是否匹配 | `spnr migrate status` |
| 把库表回滚一个版本 | `spnr migrate down` |
| 创建第一个控制台管理员 | `spnr admin init --username admin --password-env SPINNERET_ADMIN_PASSWORD` |
| 给爬虫节点签一个令牌 | `spnr token create --name <node> --scope lease:acquire --scope report:write --scope config:read` |
| 签一个只对某站点生效、永不过期的令牌 | `spnr token create --name <node> --scope lease:acquire:<site> --scope report:write:<site> --expires never` |
| 启动服务端之前校验这台机器的环境变量 | `spnr config check` |
| 生成第一个密钥加密密钥 | `spnr kek generate --id k1 > /etc/spinneret/kek.key` |
| 查看哪个 KEK 还包裹着多少数据 | `spnr kek status` |
| 完成一次 KEK 轮换 | `spnr kek rewrap` |
| 不停机地重建某个站点的热状态 | `spnr rebuild --site <site>` |
| Redis 丢失后重建全部热状态 | `spnr rebuild` |
| 为压测造一个站点和一个令牌 | `spnr seed --site loadtest --identities 100000` |
| 在容器内部检查实例的就绪状态 | `spnr healthcheck --url http://127.0.0.1:8080/readyz` |
| 确认线上跑的是哪个版本 | `spnr version` / `spinneret-server --version` |
| 让服务端自己在启动时做迁移 | `spinneret-server --migrate` |
| 只提供 API 的实例 | `spinneret-server --role api` |
| 只处理上报和定时任务的实例 | `spinneret-server --role worker` |
| 优雅地排空并停止一个实例 | 发送 `SIGTERM`，等待时间长于 `SPINNERET_SHUTDOWN_TIMEOUT` |
| 对 Compose 栈执行任意 `spnr` 命令 | `docker compose -f deploy/compose/docker-compose.yml run --rm --entrypoint /usr/local/bin/spnr migrate <command>` |

---

## 下一步

- [安装与部署](./02-installation.md) —— 这两个程序跑在哪里，整套环境如何串起来。
- [配置参考](./03-configuration.md) —— 两个程序读取的每一个 `SPINNERET_*` 变量。
- [租户、用户与令牌](./11-access-control.md) —— `spnr token create` 的权限范围到底授予了什么。
- [密钥保管库](./10-secrets.md) —— `spnr kek` 背后的密钥层级。
- [运维手册](./16-operations.md) —— 把升级、重建、KEK 轮换和排空写成操作流程。
- [故障排查](./18-troubleshooting.md) —— 这些命令失败时怎么办。
