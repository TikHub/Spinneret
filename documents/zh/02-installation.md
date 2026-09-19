# 安装与部署

**部署参考手册：该走哪条安装路径、宿主机要什么、Compose 里每个服务是什么以及少了它会坏在哪、
一套部署拥有哪些文件、端口、反向代理与 TLS、横向扩容、升级、卸载，以及完全不用 Docker 怎么跑。**

[English](../en/02-installation.md)

---

## 目录

- [该走哪条路](#该走哪条路)
- [环境要求](#环境要求)
- [引导式安装脚本](#引导式安装脚本)
  - [它会问什么](#它会问什么)
  - [选项与环境变量](#选项与环境变量)
  - [它往磁盘上写了什么](#它往磁盘上写了什么)
  - [spnrctl：控制脚本](#spnrctl控制脚本)
  - [再跑一次：管理菜单](#再跑一次管理菜单)
- [手动 Docker Compose](#手动-docker-compose)
- [已发布镜像还是从源码构建](#已发布镜像还是从源码构建)
- [Compose 编排逐个服务](#compose-编排逐个服务)
  - [postgres](#postgres)
  - [valkey](#valkey)
  - [clickhouse](#clickhouse)
  - [migrate](#migrate)
  - [spinneret](#spinneret)
  - [lb](#lb)
  - [由 profile 控制的服务](#由-profile-控制的服务)
  - [怎么启用一个 profile](#怎么启用一个-profile)
- [一套部署拥有哪些文件](#一套部署拥有哪些文件)
- [密钥加密密钥](#密钥加密密钥)
- [Compose 覆盖文件](#compose-覆盖文件)
- [端口与网络](#端口与网络)
- [反向代理与 TLS](#反向代理与-tls)
- [横向扩容](#横向扩容)
- [一台机器上跑多套](#一台机器上跑多套)
- [多机与编排系统](#多机与编排系统)
- [升级](#升级)
- [卸载](#卸载)
- [不用 Docker 部署](#不用-docker-部署)
- [安装出问题时](#安装出问题时)
- [下一步](#下一步)

---

## 该走哪条路

Spinneret 的交付物是一个无状态二进制（`spinneret-server`）加一个管理 CLI（`spnr`），两者都在同一个
distroless 镜像里。到达那里有三条路，按"项目替你做多少事"从多到少排列。

| 路径 | 你得到什么 | 代价 |
| --- | --- | --- |
| **引导式安装脚本** —— `install/install.sh` | 一台机器上的完整部署：检查或安装 Docker，克隆仓库，生成口令和保管库密钥，按**这台机器**写好 Compose 覆盖文件，执行数据库迁移，启动并等待健康，创建第一个管理员。之后还有一个控制脚本和一个管理菜单 | 你接受它的布局：一个目录、Docker 命名卷、Caddy 作为网络内负载均衡。一个你应该先读再跑的脚本 |
| **仓库里的 Compose 文件** —— `deploy/compose/docker-compose.yml` | 同一套编排，手动一步步搭起来，什么都不藏。安装脚本驱动的就是它 | 有四件事每条命令都必须对，而且错了都不会报错：项目名、按顺序排的覆盖文件、工作目录、`.env` 从哪里读。安装脚本的 `spnrctl` 存在的唯一原因就是这四件事很容易搞错 |
| **不用 Docker** | 二进制跑在宿主机上，PostgreSQL 和 Valkey 你放哪里都行，systemd 或你自己的进程管理器 | 上面那些全变成你的活：装和调三个数据库、把前端工具链（Node 和 pnpm）和 Go 一起装好、投递密钥加密密钥、反向代理、健康检查、备份。没有脚本，将来也不会有。这条路是通的 —— 见[不用 Docker 部署](#不用-docker-部署) —— 但预算一天，不是一小时 |

对第三条路请诚实一点。二进制本身确实不需要容器：它开一个监听、连 PostgreSQL 和 Redis、读一个密钥文件，
不往本地磁盘写任何东西。Docker 在这里帮你做的是**另外四个进程和它们的调参** —— 本仓库里 Valkey 的持久化
设置、ClickHouse 的缓存上限和 PostgreSQL 的参数，全都是压测的结果；在手搭的机器上，复现它们这件事就归你了。

如果你还没装过第一次，请先看[快速开始](./01-quickstart.md)，需要某个细节时再回到这里。

---

## 环境要求

| 组件 | 版本 | 说明 |
| --- | --- | --- |
| Docker Engine | 任何自带 Compose v2 插件且插件版本 ≥ 2.24 的版本 —— 实际上就是 Engine 24 或更高 | 真正的要求在 Compose：`docker compose version` 必须是 **2.24 或更高**，这是第一个带 `!reset` 和 `!override` 合并标签的版本，而本项目写出的覆盖文件两个都用到了。没有任何地方检查 Engine 版本，安装脚本只卡 Compose。旧的独立版 `docker-compose`（v1）跑不了这套编排，脚本会识别出来并拒绝 |
| 架构 | `x86_64` 或 `arm64` | 已发布镜像只构建这两种。其他架构从源码构建，这条路是通的 |
| PostgreSQL | 17 | 唯一事实来源。编排里用 `postgres:17-alpine` |
| Redis 或 Valkey | Valkey 8 / Redis 7 或更高 | 热状态、租约、上报流、会话。必须持久化，而且**绝不能**淘汰键 |
| ClickHouse | 25.8，可选 | 请求明细背后的原始请求事件。把 `SPINNERET_CLICKHOUSE_URL` 留空就是不用它 |

用安装脚本时，宿主机上还需要：`bash`、`git`、`curl`、`awk`、`base64`、脚本真正会调用的那几个 coreutils
（`install`、`cmp`、`mktemp`、`find`、`head`、`sort`），以及可读的 `/dev/urandom` 或 `openssl`。Alpine
默认既没有 bash 也没有 git：`apk add bash git curl coreutils`。

| 规格 | vCPU | 内存 | 磁盘 |
| --- | --- | --- | --- |
| 评估用，关掉 ClickHouse | 2 | 2 GiB | 10 GiB |
| 默认编排 | 2 | **4 GiB** | 20 GiB |
| 宽裕 | 4 | 8 GiB | 40 GiB SSD |
| 单个服务端实例跑在设计负载上（约每秒 5,000 次 acquire） | 每实例 4 | 每实例 8 GiB | PostgreSQL 和 Valkey 用快盘 |

这些数字不是拍的；每一项都来自 `deploy/compose/docker-compose.yml` 和 `deploy/compose/config/` 里的
容器配置：

| 项 | 出处 | 大小 |
| --- | --- | --- |
| PostgreSQL 共享缓冲 | `postgres` 命令行上的 `-c shared_buffers=512MB` | 512 MiB，启动即分配 |
| PostgreSQL 连接槽位 | `-c max_connections=300`；每个服务端副本最多开 `SPINNERET_DATABASE_MAX_CONNS`（32）个 | 2 副本 × 32 = 300 里的 64 |
| ClickHouse 常驻内存 | 这个镜像不管缓存怎么设，常驻都在 1.2 GiB 左右（jemalloc arena）；`config/clickhouse-limits.xml` 把 `max_server_memory_usage` 压到 2.5 GiB、mark 缓存压到 64 MiB（ClickHouse 自己的默认是 5 GiB） | 空载 1.2 GiB，上限 2.5 GiB |
| Valkey 工作集 | `--maxmemory-policy noeviction`，而且**没有** `maxmemory`：它随热状态增长，主导项是上报去重标记加被固定的已结束租约哈希，生命周期是 `SPINNERET_REPORT_DEDUP_TTL`（默认 `1h`） | 随流量增长；要给 AOF 重写的 fork 留余量 |
| 服务端副本 | `deploy.replicas: ${SPINNERET_REPLICAS:-2}` | 每个大约 150–500 MiB |
| 负载均衡 | `caddy:2-alpine` | 几十 MiB |

把这些加起来，4 GiB 是下限而不是建议值。安装脚本知道这件事：低于约 7.6 GiB 时它会往
`compose.host.yml` 写 `mem_limit` 上限 —— ClickHouse 五分之二、PostgreSQL 四分之一、Valkey 五分之一、
服务端八分之一 —— 这样倒霉的时候 OOM killer 杀的是真正在涨的那个，而不是 PostgreSQL。

**每一项不够会发生什么：**

| 缺什么 | 会发生什么 |
| --- | --- |
| 内存 | OOM killer 挑一个容器杀。通常是 ClickHouse，而如果设了 `SPINNERET_CLICKHOUSE_URL`，它就是**硬启动依赖** —— 服务端会以 `connect clickhouse: …` 启动失败，而不是降级运行。Valkey 被杀会丢掉整个热状态，需要 `spnr rebuild`。`docker inspect --format '{{.State.OOMKilled}}' <容器>` 能确认是哪个 |
| 内存，没那么严重的情形 | 把 ClickHouse 的上限设到低于它的空载常驻内存并不会让它变小：每次 `INSERT` 都会以 `MEMORY_LIMIT_EXCEEDED` 失败，上报 worker 就卡住了。这正是 `max_server_memory_usage` 设在空载值**之上**而不是之下的原因 |
| CPU | 单核也能起，就是慢。两核才是真下限：安装脚本低于此会告警，并把副本数默认值压到 1 |
| 磁盘 | `chdata` 长得最快，按 `SPINNERET_CLICKHOUSE_TTL_DAYS`（默认 90）过期。`docker system df -v` 能看出空间去哪了 |
| PostgreSQL 连接 | 一旦 `副本数 × SPINNERET_DATABASE_MAX_CONNS + 余量 > 300`，压力下 `/readyz` 会报 `postgres: unreachable`。要么提高 `postgres` 命令行上的 `max_connections`，要么降低连接池 |
| 文件描述符 | ClickHouse 要 262,144 个软硬限制（`ulimits.nofile`）。宿主机硬限制更低时容器起不来，日志里会提到 `nofile`。查 `ulimit -Hn` |
| Compose 版本 | 覆盖文件会以"像是本项目有 bug"的方式失败。升级 Docker |

表里每个变量的取值范围和校验规则：[配置参考](./03-configuration.md)。

---

## 引导式安装脚本

`install/install.sh`（英文）和 `install/install.zh.sh`（中文）是同一个脚本，只有提示语言不同 ——
同样的选项、同样的环境变量、同样的菜单、同样的安全边界。只支持 Docker。它们既负责安装，之后也负责管理
装出来的东西。

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.zh.sh -o install.zh.sh
less install.zh.sh       # 两千五百行，每个决定都有注释；运行之前先读
bash install.zh.sh
```

推荐这个顺序，而且这不是走过场：任何你管道进 shell 的东西都以你的身份运行，而在装 Docker 那一步，
脚本还会问你要不要以 root 跑点东西。

管道运行时提问依然正常 —— 答案从 `/dev/tty` 读，而不是从标准输入读，因为 `curl | bash` 的时候标准输入
就是脚本本身：

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.zh.sh | bash
```

完全没有终端时（CI runner、没加 `-t` 的 `docker exec`），它会说明情况并停下来，而不是对"发布到哪个地址"
这类问题默默取默认值。真要无人值守，请显式表达：`| bash -s -- --yes`。

**它不会做的事。** 它只往你选的目录里写东西，加上 Docker 自己的命名卷。唯一例外是通往那个目录的路径：
缺失的上级目录会被创建 —— 先说明是哪个、用 `sudo`、并且把命令打印出来。它只在三件事上用 `sudo`，每件都
先打印命令：你同意时安装 Docker、启动 Docker 服务、以及安装目录需要 root 时创建它。它**不会**把你加进
`docker` 组 —— 在多数机器上那等于交出 root，所以它只打印命令，由你决定。它从不修改安装目录之外的文件、
从不加 cron、从不开防火墙端口、不问过你就不装任何东西、不覆盖已有的 `.env`（里面的口令是数据卷当初建立
时用的那一套），也永远不覆盖已有的 `kek.key`。

它拒绝装进 `/`、`/usr`、`/etc`、`/var`、`/bin`、`/sbin`、`/lib`、`/boot`、`/home`、`/root` 或 `/opt`
本身；拒绝装进别的项目的 git 检出 —— 判据是 `deploy/compose/docker-compose.yml` 和
`deploy/compose/.env.example` 到底在不在，而不是远端 URL 里写了什么；也拒绝装进一个已经有东西、但那东西
不是 Spinneret 安装的目录。

`install/README.md` 是完整参考，中英双语。

### 它会问什么

七个问题，每个都有可以直接回车接受的默认值。

| | 问题 | 默认 | 说明 |
| --- | --- | --- | --- |
| 1 | 安装目录 | root 是 `/opt/spinneret`，否则 `~/spinneret` | 检出、`.env`、保管库密钥和控制脚本住在这里。数据库不在这里 |
| 2 | 控制台是否只留在 `127.0.0.1`？ | 是 | 选否会发布到 `0.0.0.0` 并给你告警：控制台是管理面，而 `/metrics` 在同一个监听上且没有任何认证 |
| 3 | 用哪个端口 | `8080` | 按端口规则校验 |
| 4 | 管理员用户名 | `admin` | 按服务端自己的规则 `^[a-z0-9][a-z0-9._-]{2,63}$` 校验，所以名字不合法只花一次按键，而不是换来一次失败的引导 |
| 5 | 几个服务端副本 | 每两核一个，限制在 1–4，内存不足 4 GiB 时强制为 1 | 一个副本意味着升级时有一小段没人服务的窗口；两个就没有，代价是多约 150–500 MiB 内存 |
| 6 | 可观测性 profile | 关 | 打开会加上抓取服务端 `/metrics` 的 Prometheus，而且**只**发布在 `127.0.0.1` 上 —— 无论控制台绑在哪里，因为它没有认证 |
| 7 | 用已发布镜像还是从源码构建 | 能拉到就用已发布镜像 | 它在**提问之前**先对那个 tag 跑了 `docker manifest inspect`，所以问的时候已经知道答案会往哪边走 |

如果目录里已经有一份检出，它会多问一个：要不要更新到最新的 `main`（`fetch` 加 `merge --ff-only`，
绝不 `reset --hard`）。

然后它浅克隆仓库（`--depth 1 --branch main`；之所以是整个仓库，是因为源码构建这条路把仓库根目录当作构建
上下文），在**本机**生成两个数据库口令、管理员口令和密钥加密密钥，用 `awk` 从自带的 `.env.example` 生成
`.env`（所以示例里的每一条注释都会留在你将要读的那个文件里），写好这台机器的覆盖文件，写出 `spnrctl`，
拉取或构建镜像，把数据库迁移作为单独一步执行，用 `--wait` 把编排拉起来，轮询
`http://127.0.0.1:<端口>/readyz` 最多 300 秒直到四个依赖全部 `ok`，创建第一个管理员，最后打印控制台地址
和凭据。

### 选项与环境变量

```
--yes, -y   所有问题都取默认答案。
--check     识别这台机器并打印会发生什么，然后停下。
--manage    直接进入已有安装的管理菜单。
--help, -h  选项列表。
```

在陌生机器上先跑 `--check`：它会报告发行版、架构、核数和内存；Docker 在不在、跑不跑、版本够不够；`git`
和 `curl` 有没有（缺的会给出对应包管理器命令）；以及这个网络到底能不能拉到已发布镜像。它不改、不写、不问。

每个答案都能用环境变量预设 —— 这才是 `--yes` 能成为完整无人值守安装（而不只是"全取默认"）的原因。

| 变量 | 默认 | 预设什么 |
| --- | --- | --- |
| `SPINNERET_PROJECT` | `spinneret` | Compose 项目名，也是查找已有安装的依据。只能是字母、数字、短横线和下划线 |
| `SPINNERET_INSTALL_DIR` | root 是 `/opt/spinneret`，否则 `~/spinneret` | 装到哪里，或者 `--manage` 下去哪里找安装 |
| `SPINNERET_BIND_HOST` | `127.0.0.1` | `127.0.0.1` 或 `0.0.0.0`。写进 `.env`，由 `compose.host.yml` 读取 |
| `SPINNERET_PORT` | `8080` | 发布的端口 |
| `SPINNERET_ADMIN_USERNAME` | `admin` | 第一个管理员 |
| `SPINNERET_REPLICAS` | 由 CPU 和内存推导 | 服务端副本数 |
| `SPINNERET_ENABLE_OBSERVABILITY` | `0` | `1` 加上 Prometheus profile |
| `SPINNERET_USE_PUBLISHED` | `1` | `1` 拉已发布镜像，`0` 从检出构建 |
| `SPINNERET_IMAGE` | `tikhubio/spinneret` | 镜像仓库 —— 换成 `ghcr.io/tikhub/spinneret` 可以从 GitHub Packages 拉同一个构建（那边需要登录），也可以指向私有镜像站或 fork，都不用改脚本 |
| `SPINNERET_IMAGE_TAG` | `latest` | 镜像 tag。生产安装请钉一个确切的 |
| `NO_COLOR` | 未设置 | 设成任何值都会关掉颜色 |

```bash
SPINNERET_INSTALL_DIR=/srv/spinneret SPINNERET_PORT=9000 \
SPINNERET_REPLICAS=2 SPINNERET_IMAGE_TAG=v0.1.0 \
  bash install.zh.sh --yes
```

管理员口令和两个数据库口令一律在本机生成，绝不从环境变量里读取。

### 它往磁盘上写了什么

```text
<INSTALL_DIR>/                            # /opt/spinneret 或 ~/spinneret
├── .git/                                 # TikHub/Spinneret 的浅克隆
├── deploy/compose/
│   ├── docker-compose.yml                # 来自仓库，从不改动
│   ├── compose.image.yml                 # 在这里写 —— 已发布镜像的覆盖文件
│   ├── compose.build.yml                 # 在这里写 —— 仅在项目名非默认时
│   ├── compose.host.yml                  # 在这里写 —— 绑定地址、内存上限
│   ├── .env                              # 在这里写，权限 0600
│   ├── config/{Caddyfile,clickhouse-*.xml,prometheus.yml}
│   └── secrets/
│       └── kek.key                       # 在这里写，0644，放在 0700 的目录里
├── backups/<UTC 时间戳>/                  # 由「管理 → 10」写出
│   ├── postgres.dump
│   ├── kek.key
│   └── env
└── spnrctl                               # 在这里写，权限 0755
```

数据库本身住在 Docker 的命名卷里 —— `spinneret_pgdata`、`spinneret_valkeydata`、`spinneret_chdata` ——
不在这个目录里。

### spnrctl：控制脚本

`spnrctl` 是唯一入口。它带上了每条命令都必须对、而错了只会产生困惑而不是报错的四件事：Compose 项目名、
按正确顺序排的覆盖文件、让 `./config`、`./secrets` 和 `../..` 构建上下文正确解析的工作目录，以及
`COMPOSE_ENV_FILES`。名字之后的一切都原样传给 `docker compose`。

```bash
./spnrctl ps                        # 在跑什么
./spnrctl logs -f spinneret         # 跟随服务端日志
./spnrctl restart lb                # 重启单个服务
./spnrctl up -d --wait              # 启动并等到健康
./spnrctl down                      # 停止，保留数据
./spnrctl down -v                   # 停止并删除数据卷。不可逆。
```

管理 CLI 在镜像里是 `/usr/local/bin/spnr`，它只需要 PostgreSQL，所以请通过一次性的 `migrate` 服务来跑，
而不是通过一个可能并不健康的副本：

```bash
./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate migrate status
./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate \
    token create --name node-1 \
    --scope lease:acquire --scope report:write --scope config:read
```

镜像是 distroless 的，所以 `./spnrctl exec spinneret sh` 是不行的 —— 里面没有 shell。

### 再跑一次：管理菜单

再跑一次，脚本会自己判断在做哪件事。已有安装是通过问 Docker "这个 Compose 项目是从哪个目录启动的"找到的，
所以装在哪里都能找到；而且**每一次运行**都会找。

```
==> An install is already here
    ✓ /opt/spinneret
    ✓ Running v0.1.0
    ✓ Image tikhubio/spinneret:latest

      1  Status — versions, containers, schema, disk
      2  Move to another image tag (re-pull, migrate, restart)
      3  Manage — accounts, tokens, backups, health, disk
      4  Stop or remove this install
      q  Quit
```

`--manage` 直接进去。所有设置都先从这套安装自己的文件里重新读一遍，所以一套"一个副本、不开可观测性"的
安装绝不会被当成"两个副本、开着 profile"去升级。第 2 项会把在跑的版本和最新发布对比，而预发布后缀算更旧，
所以 `v1.2.0-rc1` 排在 `v1.2.0` 之前。

对已经存在的部署使用 `--yes`，它只打印安装在哪里，什么都不改。"所有问题都取默认答案"对一台新机器是对的，
对一台在跑的机器是错的 —— 那会意味着把源码构建的安装切成已发布的 `latest`、把那个镜像的迁移应用到生产
数据库、关掉可观测性 profile、并且把你手改过的 `compose.host.yml` 覆盖掉。

**管理**（第 3 项）覆盖的是大家最常问怎么做的那些事：

| | | |
| --- | --- | --- |
| 1 | 改管理员口令 | 以该账号登录并修改自己的口令，和控制台完全一样。该账号的其他所有会话都会结束。它会问要不要同时更新 `.env` 里的 `SPINNERET_ADMIN_PASSWORD` |
| 2 | 加管理员 | 创建一个带租户级 `admin` 角色绑定的用户。没有改名这回事：账号是创建出来的，口令是重置的 |
| 3 | 列出账号 | 用户名、id，以及是否平台管理员、是否被禁用 |
| 4 | 创建 API 令牌 | `spnr token create`。明文只打印一次 |
| 5 | 重建热状态 | `spnr rebuild`。会提醒：全量重建会先删掉 epoch 键，于是每个副本在重建完成前都报未就绪；用 `--site` 指定单个站点可以避免 |
| 6 | 显示配置 | `spnr config check` —— 整套 `SPINNERET_*` 环境，校验过，所有凭据都已脱敏 |
| 7 | 健康检查 | `/healthz` 和 `/readyz`、容器状态，以及 ClickHouse、Valkey、PostgreSQL 是否被 OOM 杀过 |
| 8 | 日志 | `spnrctl logs` |
| 9 | 重启服务 | 会问要不要一个一个地滚动重启副本 |
| 10 | 立即备份 | `pg_dump -Fc`，加上 `kek.key` 和 `env`，写进 `<安装目录>/backups/<UTC 时间戳>/`。dump 在 `umask 077` 下写出，两个拷贝都以 `0600` 装入；目录本身继承宿主机的 umask，机器是共用的就自己再收紧一下 |
| 11 | 恢复备份 | 要求输入 `restore`，对比备份里的密钥和当前在用的密钥，停掉副本，`pg_restore --clean --if-exists`，重建热状态，再把副本拉起来 |
| 12 | 清理磁盘 | **本** Compose 项目里没有容器引用的镜像，以及可选的宿主机构建缓存 —— 后者是整台机器共用的，提示里也这么写着 |

其中三项 —— 改口令、加用户、列用户 —— 在 `spnr` CLI 里根本不存在；它们是控制台的 RPC，脚本通过控制台
自己用的那个端点在回环地址上访问。口令读取不回显，并且通过标准输入交给 `curl`，绝不作为命令行参数，
因为 `argv` 是这台机器上每个进程都能读的。其余每一项都委托给 `spnrctl` 或镜像里的 `spnr`，所以菜单项
不可能和它所代表的命令走偏。

备份、恢复和密钥轮换的完整说明：[运维手册](./16-operations.md#备份)。

---

## 手动 Docker Compose

全部内容在 `deploy/compose/docker-compose.yml`，Compose 项目名 `spinneret`。

```bash
git clone https://github.com/TikHub/Spinneret.git
cd Spinneret
./scripts/compose-init.sh
```

`compose-init.sh` 只在文件不存在时创建：由 `.env.example` 生成的 `deploy/compose/.env`，其中
`PG_PASSWORD`、`CLICKHOUSE_PASSWORD` 和 `SPINNERET_ADMIN_PASSWORD` 为随机值；以及
`deploy/compose/secrets/kek.key`，里面是一把新的 32 字节密钥，形如 `k1:<base64>`，权限 `0644`。
它可以重复执行：已有文件会保留。两个文件都被 git 忽略，也被排除在 Docker 构建上下文之外。它需要
`PATH` 上有 `openssl`。

```bash
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

`--wait` 会等到每个带健康检查的容器都健康。`init-admin` 创建平台管理员（`SPINNERET_ADMIN_USERNAME`，
默认 `admin`）、租户 `default`、命名空间 `default` 及其默认策略；它是幂等的，第二次运行会打印
`already initialized`。

验证：

```bash
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate migrate status
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate config check
```

**一个能省掉一小时的提醒。** Compose 解析 `.env` 以及编排里所有相对路径（`./config`、`./secrets`、
`../..` 构建上下文）时，参照的是**编排文件所在的目录**，而不是你当前所在的目录。这恰好也是上面
`-f deploy/compose/docker-compose.yml` 这种写法能在仓库根目录跑通的原因：`.env` 仍然是在编排文件旁边找到
的，不是在你旁边。`cd deploy/compose` 再用裸的 `docker compose` 也是一样的道理，而安装脚本写出来的
`spnrctl` 直接把整条调用都带上了。所以 `required variable PG_PASSWORD is missing a value` 并不是说你目录
站错了 —— 它的意思是 `deploy/compose/.env` 还没生成（跑 `./scripts/compose-init.sh`），或者有一个显式的
`--env-file` / `COMPOSE_ENV_FILES` 指向了别处。

`make up` 和 `make down` 就是这两条命令，`-f` 已经带好了。

---

## 已发布镜像还是从源码构建

已发布的多架构镜像是 Docker Hub 上的 `tikhubio/spinneret`，这是不需要凭据就能拉的那一份。同一次构建也会推到
GitHub Packages 的 `ghcr.io/tikhub/spinneret`，digest 完全相同，但那边需要登录，所以只有在你有账号的情况下
才把 `SPINNERET_IMAGE` 指过去。两者都构建 `linux/amd64` 和 `linux/arm64`，每个版本 tag
（`v` 后面跟一个数字）都打上 `vX.Y.Z`、`X.Y.Z`、`X.Y` 和 `latest` —— `latest` 只给没有预发布后缀的 tag，
所以 `v1.2.0-rc1` 永远不会变成 `latest`。tag 会被烙进二进制，所以 `spnr version` 和控制台报出的就是当前在跑的那个构建。
拉取大约一分钟；构建要 5–15 分钟、首次约 2 GB 构建缓存，而且需要能访问 Go 和 Node 的包仓库。

已发布镜像这条路上，安装脚本会写 `compose.image.yml`：

```yaml
services:
  migrate:
    image: ${SPINNERET_IMAGE:-tikhubio/spinneret}:${SPINNERET_IMAGE_TAG:-latest}
    build: !reset null
  spinneret:
    image: ${SPINNERET_IMAGE:-tikhubio/spinneret}:${SPINNERET_IMAGE_TAG:-latest}
    build: !reset null
  init-admin:
    image: ${SPINNERET_IMAGE:-tikhubio/spinneret}:${SPINNERET_IMAGE_TAG:-latest}
    build: !reset null
```

跑服务端二进制的三个服务 —— `migrate`、`spinneret`、`init-admin` —— 共用一个镜像，所以只覆盖其中一个会
让另外两个还想去构建。`build: !reset null` 把 build 段整个去掉，于是一次误触的 `docker compose build`
不会悄悄盖掉刚拉下来的镜像，而且 Compose 也不再需要一个根本不会用的构建上下文真的存在。删掉这个文件就
回到从源码构建。

手动做同一件事：在 `.env` 里设 `SPINNERET_IMAGE` / `SPINNERET_IMAGE_TAG` 并加上那个覆盖文件，或者直接
`docker compose pull`。**生产安装请钉一个确切 tag**，而不是跟着会动的 tag：这样你才能说出现在跑的是哪个
构建，也才能把它放回去。

---

## Compose 编排逐个服务

十一个服务。六个是编排本体；五个由 profile 控制，不主动要求就什么都不做。

| 服务 | 镜像 | Profile | 发布端口 | 数据卷 |
| --- | --- | --- | --- | --- |
| `postgres` | `postgres:17-alpine` | — | 无 | `pgdata` |
| `valkey` | `valkey/valkey:8-alpine` | — | 无 | `valkeydata` |
| `clickhouse` | `clickhouse/clickhouse-server:25.8-alpine` | — | 无 | `chdata`，加两个只读配置挂载 |
| `migrate` | `spinneret:local` 或已发布镜像 | — | 无 | `kek` secret |
| `spinneret` | 同上 | — | 无 | `kek` secret |
| `lb` | `caddy:2-alpine` | — | `${SPINNERET_PORT:-8080}` → 8080 | `config/Caddyfile` 只读 |
| `init-admin` | 同上 | `init` | 无 | `kek` secret |
| `prometheus` | `prom/prometheus:v2.54.1` | `observability` | `${PROMETHEUS_PORT:-9090}` → 9090 | `config/prometheus.yml` 只读 |
| `mocktarget` | `spinneret-mocktarget:local` | `test`、`loadtest`、`example` | `${MOCK_TARGET_PORT:-19090}` → 9090，`${MOCK_PROXY_PORT:-19091}` → 9091 | 无 |
| `example-crawler` | `spinneret-example-crawler:local` | `example` | `${EXAMPLE_PORT:-18000}` → 8000 | 无 |
| `k6` | `grafana/k6:latest` | `loadtest` | 无 | `test/load` 只读 |

共享环境块（`x-spinneret-env`）同样应用到 `migrate`、`spinneret` 和 `init-admin`，所以 `migrate` 里的
CLI 看到的配置和服务端看到的完全一致。

### postgres

唯一事实来源：目录、站点、身份、代理、策略、配置项、密钥版本、用户、令牌和审计日志。启动时带四个非默认
参数 —— `max_connections=300`、`shared_buffers=512MB`、`effective_cache_size=1GB`、`wal_compression=on`
—— 数据放在命名卷 `pgdata` 里。健康检查：`pg_isready -U spinneret -d spinneret`，每 5 秒一次，重试 30 次，
这正是 `migrate` 在等的东西。

没有它什么都起不来：`migrate` 有 `depends_on: postgres: service_healthy`，而其他每个服务都在 `migrate`
下游。它也是整套部署里唯一无法从别处恢复的组件 —— 请[备份它](./16-operations.md#备份)。

### valkey

热状态：租约哈希、可用集合、冷却和封禁标记、熔断器状态、上报流、控制台会话、以及两个实例注册表。
`valkeydata` 放它的 AOF。

五个非默认设置，每一个都经过压测：

| 设置 | 值 | 为什么 |
| --- | --- | --- |
| `--appendonly yes --appendfsync everysec` | 开 | 持久化靠 AOF。这里没有拿持久性换性能 |
| `--save ""` | 关掉 RDB | RDB 存盘点会为同一批数据再 fork 一次 —— 在每秒 4,000 次 acquire→report 循环下，默认设置每分钟触发一次后台存盘，**同时**还每 40 秒做一次 AOF 重写 |
| `--auto-aof-rewrite-percentage 300 --auto-aof-rewrite-min-size 1gb` | 300 % / 1 GiB | 热状态大多是短命键，所以 AOF 涨得比它所描述的数据集快得多；用默认值时压测下每约 52 秒就重写一次，每次 fork 一个约 1 GiB 的进程 |
| `--io-threads ${VALKEY_IO_THREADS:-4}` | 4 | 吞吐提升很小，尾延迟改善很大。设 `VALKEY_IO_THREADS=1` 回到单线程行为 |
| `--maxmemory-policy noeviction` | noeviction | **承重设计。** 热状态虽然是派生数据，但**悄悄丢键**改变的是调度决策，而不是明确地报错。永远不要打开淘汰 |

`--maxclients 20000` 用来覆盖长轮询和流式连接数。健康检查：`valkey-cli ping`。

没有它服务端不会就绪：`/readyz` 会报 `redis: unreachable`。如果数据卷丢了或被清空，`/readyz` 会报
`hotstate: epoch missing (rebuild pending)`，`spnr rebuild` 会从 PostgreSQL 重新生成全部内容。
实测数据和容量估算方法见[性能与调优](./17-performance.md#valkey-设置)。

### clickhouse

原始请求事件，支撑请求明细和分析查询。数据库 `spinneret`，用户 `spinneret`，数据卷 `chdata`，两个只读
配置挂载 —— `config/clickhouse-listen.xml`（只监听 IPv4，因为容器通常没有 IPv6）和
`config/clickhouse-limits.xml`（缓存上限）。表结构和 TTL 由服务端启动时按
`SPINNERET_CLICKHOUSE_TTL_DAYS` 创建。`ulimits.nofile` 提到 262,144 软硬限制。健康检查：
`wget -qO- http://127.0.0.1:8123/ping`。

**可选，但不能可选一半。** 如果设了 `SPINNERET_CLICKHOUSE_URL` 而 ClickHouse 连不上，服务端会启动失败，
不会降级运行。要不用它，就把那个变量留空 —— 请求明细和原始事件没有了，约 1.2 GiB 内存回来了。其他一切
照常：身份、租约、策略、熔断器、按分钟和按小时的统计，全都在 PostgreSQL 和 Redis 里。

### migrate

一个一次性容器，跑 `/usr/local/bin/spnr migrate up` 然后退出。`restart: "no"`，健康检查关闭，
`depends_on: postgres: service_healthy`。迁移嵌在二进制里，并由 PostgreSQL 的 advisory lock 串行化，
所以多个实例同时执行是安全的。

`spinneret` 等的是 `service_completed_successfully`，这正是"没有任何副本会在它读不懂的表结构上提供服务"
这个保证的来源。它也是跑任何 `spnr` 命令的那个容器：只需要 PostgreSQL，而且永远不是在服务流量的那个。

没有它服务端根本不启动。注意 Compose 在一次性服务的容器规格没变时可能认为它已经满足了，这正是安装脚本
把 `run --rm migrate` 作为显式一步、而不是依赖 `depends_on` 链的原因。

### spinneret

服务端。`SPINNERET_REPLICAS` 个同一镜像的副本（默认 2），不发布任何端口 —— 负载均衡通过 Compose 网络上
的服务名找到它们。`kek` secret 挂在 `/run/secrets/kek`。镜像自带的 `HEALTHCHECK` 跑
`spnr healthcheck --url http://127.0.0.1:8080/readyz`，每 10 秒一次，启动宽限 20 秒，重试 3 次 ——
`--wait` 和 `lb` 的 `depends_on` 就是靠它知道一个副本可用了。

`stop_grace_period: 40s` 是故意的，而且大于服务端自己的 `SPINNERET_SHUTDOWN_TIMEOUT`（默认 30 秒）：
被要求停止的副本会先报 `draining`，然后把在途请求做完，再按层级依次停掉后台循环，让写入方把已经产出的
东西刷出去。Docker 绝不能在这中间 `SIGKILL` 它。

`depends_on` 是 `migrate: service_completed_successfully`、`valkey: service_healthy`、
`clickhouse: service_healthy`。

### lb

Caddy，网络内的负载均衡，也是唯一发布宿主机端口的服务。它带 `--watch` 启动，所以改
`config/Caddyfile` 会原地重载，不需要重启。它自己的健康检查是通过自己去探 `/healthz`。

在你把自己的代理放到它前面之前，值得先读一遍这个 Caddyfile；里面每一项设置都有注释说明是哪次测量产生的。
对任何要替换它的人来说，重要的是这几行：

| 设置 | 值 | 为什么 |
| --- | --- | --- |
| `dynamic a spinneret 8080`，`refresh 5s` | 基于 DNS 的上游 | 副本由 Compose DNS 发现，所以扩缩容不需要改配置 |
| `lb_policy least_conn` | 最少连接 | 让长轮询和事件流均匀分布 |
| `health_uri /readyz`、`health_interval 2s` | 就绪而非存活 | 正在排空的副本会在关闭监听之前就被踢出池子 |
| `fail_duration 2s`、`max_fails 1` | 一次拨号失败就把实例停放 2 秒 | 用 `least_conn` 时，死掉的副本反而会一直被选中 —— 它的活跃连接最少。窗口故意很短：更长会把一次连接洪峰变成 `503 no upstreams available` |
| `lb_retries 20`、`lb_try_duration 10s`、`lb_try_interval 250ms` | 重试预算 | 覆盖"副本已经没了"，也覆盖"**所有**上游都被停放"的那个窗口 |
| `request_buffers 128KiB` | 缓冲 POST 请求体 | 所有 RPC 都是 POST；代理只有拿着请求体才能把它重放到另一个上游。更大的请求体（身份、代理导入）是流式的，不会重试 |
| `flush_interval -1` | 不缓冲 | 长轮询（`WatchConfig`，最多 60 秒）和控制台事件流绝不能被缓冲或截断 |
| `dial_timeout 5s`、`read_timeout 90s`、`write_timeout 90s`、`keepalive 90s` | 传输层 | 拨号超时会被记为失败并停放一个其实只是忙的副本，所以拨号超时要远高于连接洪峰下最差的 accept 延迟 |

没有 `lb`，就没有任何东西发布到宿主机。你可以用自己的代理替换它 —— 见[反向代理与 TLS](#反向代理与-tls)
—— 但那八行就变成你要负责调对的配置了。

### 由 profile 控制的服务

| 服务 | 是什么 | 谁需要它 |
| --- | --- | --- |
| `init-admin`（`init`） | 一次性：`spnr admin init --username ${SPINNERET_ADMIN_USERNAME:-admin} --password-env SPINNERET_ADMIN_PASSWORD`。创建第一个平台管理员、租户 `default`、命名空间 `default` 及其默认策略。幂等。需要 `.env` 里有 `SPINNERET_ADMIN_PASSWORD`，否则 Compose 拒绝启动它 | 没有它，你有一套在跑的编排，但没有任何办法登录 |
| `prometheus`（`observability`） | `prom/prometheus:v2.54.1` 配 `config/prometheus.yml`：对名字 `spinneret` 做 DNS 服务发现，端口 8080，路径 `/metrics`，每 15 秒一次 —— 所以它直接抓每个副本，不走负载均衡 | 没有谁需要。指标本来就在服务端自己的监听上；这只是一个存放它们的地方。它**没有任何认证**，而它保存的时序描述了这套部署里每一个命名空间，所以请留在回环地址上 |
| `mocktarget`（`test`、`loadtest`、`example`） | 9090 上一个可脚本化的假目标站点（带管理 API），9091 上一个需要认证的 HTTP 正向代理。规则在运行时设置：`curl -X PUT 127.0.0.1:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'` | e2e 套件、压测套件和示例演练。生产里什么都不需要它 |
| `example-crawler`（`example`） | `examples/fastapi-crawler` 里的 FastAPI 示例节点，指向 `http://lb:8080` 和 `http://mocktarget:9090`。需要 `EXAMPLE_TOKEN`，由 `scripts/example-quickstart.sh` 写进 `.env`；没有令牌它拒绝启动 | 示例演练 |
| `k6`（`loadtest`） | `grafana/k6:latest`，用 `LOADTEST_TOKEN` 对 `http://lb:8080` 跑 `test/load/${K6_SCRIPT:-acquire_report.js}` | 压测套件。`spnr seed` 会打印一个可用的令牌 |

这五个都没有健康检查，所以 `--wait` 从来不会等它们。`init-admin` 是这组里的一次性容器：`restart: "no"`、
`healthcheck: disable: true`，以及 `depends_on: migrate: service_completed_successfully` —— 它跑在迁移过
的库上，跑完就退出。`prometheus`、`mocktarget` 和 `example-crawler` 是 `restart: unless-stopped`；`k6`
干脆没写重启策略，脚本跑一次就停在退出状态。

### 怎么启用一个 profile

profile 只是加容器，不改动核心编排的任何东西。

```bash
cd deploy/compose

# 一次性：引导第一个管理员
docker compose --profile init run --rm init-admin

# 常驻：加上 Prometheus 并保持运行
docker compose --profile observability up -d

# 同一件事，走安装脚本写的控制脚本
./spnrctl --profile observability up -d
```

第 6 个问题回答"是"时，安装脚本会把 `--profile observability` 烙进生成的 `spnrctl`，你不用记它。
环境变量 `COMPOSE_PROFILES=observability` 对裸的 `docker compose` 是同样效果。`make e2e`、`make load`
和 `make example` 会自己带上各自需要的 profile。

---

## 一套部署拥有哪些文件

四样东西，值得知道哪些丢得起。

| | 是什么 | 权限 | 能重建吗 |
| --- | --- | --- | --- |
| `deploy/compose/.env` | 两个数据库口令、第一个管理员口令、端口、副本数，以及你设的任何其他变量。由 `.env.example` 生成，后者给每个变量都配了注释 | `0600` | **实际上不能。** 数据库口令是 `pgdata` 和 `chdata` 这两个卷当初建立时用的那一套，新文件打不开它们。之后要改还需要在容器里执行 `ALTER ROLE` |
| `deploy/compose/secrets/kek.key` | 密钥加密密钥 —— 见下文 | `0644`，放在 `0700` 的目录里 | **永远不能。** 没有任何恢复途径 |
| `deploy/compose/config/` | `Caddyfile`、`clickhouse-listen.xml`、`clickhouse-limits.xml`、`prometheus.yml` | 来自仓库 | 能，它们来自检出。你的改动不能 —— `Caddyfile` 保存即重载，其他几个需要重启对应容器 |
| 数据卷 `pgdata`、`valkeydata`、`chdata` | PostgreSQL 数据目录、Valkey 的 AOF、ClickHouse 的存储 | Docker 管 | `pgdata` **只能靠备份**。`valkeydata` 是派生的：`spnr rebuild` 能从 PostgreSQL 重新生成。`chdata` 是按自己的 TTL 过期的分析数据 |

`.env` 和 `secrets/` 都被 git 忽略，也都被排除在 Docker 构建上下文之外，所以两者都不可能误入镜像。

这套部署的一份备份是三样东西，而且必须三样都有才算备份：`postgres.dump`、`kek.key` 和 `env`。
Valkey 和 ClickHouse 故意不在其中。命令和恢复演练见[运维手册 → 备份](./16-operations.md#备份)。

---

## 密钥加密密钥

`deploy/compose/secrets/kek.key` 里是一行或多行 `id:base64`，以 Docker secret `kek` 的形式挂到容器的
`/run/secrets/kek`（`SPINNERET_KEK_FILE`）。它是保管库的根：身份载荷、代理 URL、密钥版本和通知渠道凭据
各自用一把独立的数据密钥加密，而那把数据密钥再用这把密钥封装。数据库里只有被封装的密钥和密文，
KEK 从不落库。

**在往这套部署里放数据之前就把它备份出去，每次轮换之后再备份一次。** 没有它，数据库备份里每一个加密字段
都无法恢复；而用错的密钥去恢复一份导出，会失败在
`is the KEK the one used to initialize this database?` 上。除了那把正确的密钥，没有别的办法。

```bash
cp deploy/compose/secrets/kek.key ~/somewhere-safe/spinneret-kek-$(date -u +%Y%m%d).key
```

这个文件是 `0644`，而且**这是故意的**，不是一个需要"修正"的错误。Compose 把这个文件本身挂进容器，
而容器以 distroless 的 `nonroot` 用户（uid 65532）运行，宿主机上没有对应用户；`0600` 在那里读不到，
服务端会以 `vault: read kek file "/run/secrets/kek": … permission denied` 退出。保护应该落在目录上，
它是 `0700`。

生成、轮换、重新封装和退役密钥：[运维手册 → 密钥轮换](./16-operations.md#密钥轮换)，以及
`spnr kek --help`。相关变量：[配置参考 → 密钥](./03-configuration.md#密钥)。威胁模型：
[密钥保管库](./10-secrets.md)。

---

## Compose 覆盖文件

Compose 按 `-f` 的顺序合并，而安装脚本写的 `spnrctl` 已经按正确顺序传好了：`docker-compose.yml`，
然后 `compose.image.yml` 或 `compose.build.yml`，然后 `compose.host.yml`。两个合并标签值得了解，
它们都要求 Compose 2.24：

- `ports: !override` 会**替换**发布端口列表，而不是往上追加。普通合并会给同一个容器端口留下两条映射，
  第二条必然绑定失败。
- `build: !reset null` 移除 build 段，这样 Compose 不需要为一个根本不会用的构建上下文去要求仓库根目录
  存在。

`compose.host.yml` 归你随便改 —— 文件头自己就这么写着。它承载绑定地址，因为自带的编排文件发布的是
`"${SPINNERET_PORT:-8080}:8080"`（没有主机部分），而把 `127.0.0.1:8080` 塞进 `SPINNERET_PORT`
会把这个变量的其他使用者全都搞坏：

```yaml
services:
  lb:
    ports: !override
      - "${SPINNERET_BIND_HOST:-127.0.0.1}:${SPINNERET_PORT:-8080}:8080"
  prometheus:
    ports: !override
      - "127.0.0.1:${PROMETHEUS_PORT:-9090}:9090"
```

在内存低于约 7.6 GiB 的机器上，它还会带上 `clickhouse`、`postgres`、`valkey` 和 `spinneret` 的
`mem_limit`。那些是上限，不是预留。

其他任何你想改的东西，都应该放进你自己的、排在这些之后的覆盖文件里 —— 而不是改 `docker-compose.yml`，
升级会把后者从你脚下换掉。

```bash
docker compose -f docker-compose.yml -f compose.image.yml -f compose.host.yml -f compose.mine.yml up -d
```

---

## 端口与网络

| 端口 | 服务 | 变量 | 该不该发布 |
| --- | --- | --- | --- |
| 8080 | 控制台和节点 API，经 `lb` | `SPINNERET_PORT`，地址来自 `SPINNERET_BIND_HOST` | 该 —— 这就是这套部署。只要不只是本机访问，就放在 TLS 后面 |
| 9090 | Prometheus，profile `observability` | `PROMETHEUS_PORT` | **只发布在回环地址上。** 没有认证，而它的时序描述了每一个命名空间 |
| 19090 / 19091 | mock 目标站点 / 它的正向代理，profile `test`、`loadtest`、`example` | `MOCK_TARGET_PORT`、`MOCK_PROXY_PORT` | 测试机之外绝不 |
| 18000 | 示例爬虫，profile `example` | `EXAMPLE_PORT` | 测试机之外绝不 |

**不发布，而且设计上也不需要发布的：** PostgreSQL 5432、Valkey 6379、ClickHouse 9000 和 8123，
以及服务端副本自己的 8080。它们只在 Compose 网络里可达。
（`make infra-up` 确实会发布 45432、46379、49000 和 48123 —— 那是
`deploy/compose/docker-compose.infra.yml`，一个叫 `spinneret-infra` 的独立 Compose 项目，专门给测试套件
用，`fsync=off` 而且不持久化。永远不要把一套部署指向它。）

还有两个监听，默认都关着：

| 变量 | 打开什么 | 规则 |
| --- | --- | --- |
| `SPINNERET_METRICS_ADDR` | 把 `/metrics` 挪到独立监听上。留空（默认）时它在 API 监听上，**没有认证** | 如果 API 监听会被不该读你指标的人访问到，就把它设成回环或内网地址 |
| `SPINNERET_PPROF_ADDR` | `net/http/pprof` 调试端点 | 没有认证，而且会暴露堆内容和 goroutine 栈。校验规则拒绝把它设成 API 或指标地址。请让它对不可信网络不可达 |

出方向上，服务端需要访问：PostgreSQL、Redis、可选的 ClickHouse、`SPINNERET_PROXY_CHECK_URL` 指向的地址
（代理健康检查会**通过每个代理**去抓它 —— 在没有外网的机器上，要么把它指到可达的地方，要么每个代理都会被
标成死的）、你的通知渠道端点，除此之外没有别的。它自己从不访问目标站点。

---

## 反向代理与 TLS

服务端自己也能终止 TLS，用 `SPINNERET_TLS_CERT_FILE` 和 `SPINNERET_TLS_KEY_FILE`（必须同时设或都不设；
证书在启动时加载，所以坏证书会让启动失败而不是让第一个请求失败，最低版本是 TLS 1.2）。更常见的做法是放
反向代理：编排自带 Caddy 作为网络内负载均衡，你在它前面再放 Caddy、nginx 或云上的负载均衡 —— 也可以整个
替换掉它。

### 先把客户端 IP 搞对

这一条最容易踩。有三样东西依赖"请求真正来自哪个地址"：

- **令牌的 IP 白名单。** API 令牌可以限制到若干 CIDR；客户端 IP 错了，要么每个请求都被拒，要么这个限制
  根本不起作用。
- **限流**，包括登录限流。
- **审计日志**，它记录每一次管理操作来自哪个地址。

`SPINNERET_TRUSTED_PROXIES` 是一份 CIDR 和裸地址清单，只有来自这些地址的 `X-Forwarded-For` 和
`X-Real-Ip` 才被采信。它**默认为空**，意思是直接使用对端地址、忽略转发头 —— 这是安全的，但只要中间有
代理它就是错的。Compose 编排把它设成 `172.16.0.0/12,10.0.0.0/8,192.168.0.0/16`，覆盖 Docker 自己的网段；
如果你的边缘代理在别处，把它的地址加上。

```bash
SPINNERET_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12,192.168.0.0/16
```

条目可以是 CIDR（`10.0.0.0/8`、`2001:db8::/32`）或裸地址（`192.0.2.1`、`::1`）。地址在比较前会被归一化，
所以 `10.0.0.0/8` 也能匹配被报成 `[::ffff:10.1.2.3]` 的对端。设得太宽，任何客户端都能声称自己是任何
地址；设得太窄，每个客户端看起来都是你的代理。

### 前面那个代理必须做到什么

| 要求 | Caddy | nginx |
| --- | --- | --- |
| 透传 `X-Forwarded-For` 和 `X-Forwarded-Proto` | 默认就有 | `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` 和 `X-Forwarded-Proto $scheme;` |
| **不缓冲**长轮询和事件流。`WatchConfig` 会阻塞最多 60 秒（默认 30 秒），控制台的 `/api/v1/events/stream` 是无限流 | `flush_interval -1` | `proxy_buffering off;` 加 `proxy_read_timeout 120s;` |
| 健康检查用 **`/readyz`**，大约每 2 秒一次 —— 不是 `/healthz`，后者只说明进程活着 | `health_uri /readyz`、`health_interval 2s` | `health_check uri=/readyz interval=2s`（nginx Plus），或者用外部检查 |
| 想让 POST 可重试就缓冲请求体。所有 RPC 都是 POST | `request_buffers 128KiB` | `proxy_request_buffering on;`（默认） |
| 客户端用 gRPC 时端到端支持 HTTP/2 | 默认就有 | `grpc_pass` 配 `http2` |
| 空闲和读超时高于长轮询上限 | `read_timeout 90s` | `proxy_read_timeout 120s;` |

```nginx
location / {
    proxy_pass         http://spinneret_upstream;
    proxy_http_version 1.1;
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
    proxy_buffering    off;            # 长轮询和事件流
    proxy_read_timeout 120s;
    client_max_body_size 64m;          # 身份和代理导入
}
```

服务端自己的 HTTP 超时，供你围绕它调代理时参考：读请求头 10 秒、读完整个请求 6 分钟、空闲 120 秒、
没有写超时（长轮询和流会超过任何固定上限 —— 单次调用由一个 deadline 拦截器来约束）。节点请求上限 8 MiB，
管理类请求上限是 `SPINNERET_ADMIN_MAX_REQUEST_BYTES`（64 MiB）。

### 为什么是 `/readyz` 而不是 `/healthz`

被要求停止的副本会按顺序做四件事：把自己标成正在排空，于是 `/readyz` 回
`503 {"status":"draining"}`；关掉 HTTP keep-alive，于是排空窗口里每个响应都带 `Connection: close`，
没有对端会在一个即将关闭的复用连接上发新请求；等待 `min(5s, SPINNERET_SHUTDOWN_TIMEOUT/4)`
——默认 30 秒时就是 5 秒——**然后**才关闭监听器；最后才关掉监听并停掉后台循环。

第一个窗口正是滚动重启能对节点完全无感的全部原因：每 2 秒探一次 `/readyz` 的代理，在副本停止 accept
之前就已经把它从池子里摘掉了。探 `/healthz`、或者根本不探的代理，只会把请求切断。
每个副本 `503 {"status":"draining"}` 超过大约五秒，就说明前面那个东西没有遵守 `/readyz`。

### HTTP/2、h2c 和 websocket

没有 websocket。控制台的实时更新是 `/api/v1/events/stream` 上的 Server-Sent Events，节点的配置订阅是
长轮询的 `WatchConfig` 调用 —— 两者都是普通 HTTP 响应，只要求不被缓冲。真正需要注意的是 HTTP/2：
明文监听上服务端同时开启 HTTP/1.1 和非加密 HTTP/2（h2c），TLS 监听上开启 HTTP/1.1 和 HTTP/2。
Connect over JSON 在 HTTP/1.1 上就能跑，不需要任何特殊配置；Go SDK 的 gRPC 传输需要全链路的 h2c
或 TLS 上的 HTTP/2。

### Cookie 与来源

TLS 在边缘终止时，`SPINNERET_COOKIE_SECURE` 保持 `auto` —— 这是默认值，它在两种情况下给会话 Cookie 打上
Secure：请求本身走的是 TLS，**或者**一个**受信任的**代理送来了 `X-Forwarded-Proto: https`。"受信任"的意思是
直连对端命中 `SPINNERET_TRUSTED_PROXIES`，而这个列表默认为空：如果代理没写进去，`auto` 会悄悄判成不加
Secure。设成 `true` 就是强制打开，控制台一旦发布到回环地址以外就应该这么设。`false` 只适用于纯 HTTP 的
内网，别无他用。

如果控制台和 API 不同源，把那个来源写进 `SPINNERET_ALLOWED_ORIGINS`。默认布局下两者同源，所以它保持为空。

---

## 横向扩容

服务端实例是无状态的。它们不持有任何本地数据，所有需要协调的事情都通过 Redis 和 PostgreSQL 协调：
上报流分片归属、周期任务的选主（PostgreSQL advisory lock）、目录和配置失效（Redis pub/sub），
以及两个实例注册表。

```bash
SPINNERET_REPLICAS=4 ./spnrctl up -d --wait
```

### 什么能扩，什么不能

| | |
| --- | --- |
| **服务端副本** | 能，而且这是增加容量的正路。PostgreSQL 连接池是**每实例**的，加副本会成倍放大连接需求：要么提高 PostgreSQL 的 `max_connections`，要么降低 `SPINNERET_DATABASE_MAX_CONNS`，让 `副本数 × SPINNERET_DATABASE_MAX_CONNS + 余量 < max_connections` 依然成立（本编排是 300） |
| **PostgreSQL** | 纵向扩，以及你自己安排的只读副本。服务端只往一个主库写 |
| **Redis** | 单实例即可承载设计目标，也是 acquire 吞吐的上限。`SPINNERET_REDIS_ADDRS` 启用 Cluster 模式。**永远不要打开键淘汰** |
| **ClickHouse** | 独立扩。它只被批量写入，以及被控制台查询读取 |
| **`SPINNERET_REPORT_SHARDS`** | **在跑的部署上不能改。** 租约 ID 编码了它的上报该去哪个分片，所以改分片数会让每一个在途租约失效。这是一个维护窗口：[运维手册 → 修改分片数](./16-operations.md#修改分片数) |

### api / worker 角色拆分

`SPINNERET_ROLE`（或 `spinneret-server --role`）取 `all`、`api` 或 `worker`。`all` 是默认，两边都做。

| 角色 | 提供 | 运行 |
| --- | --- | --- |
| `api` | 节点 API、控制台、`/healthz`、`/readyz`、`/metrics` | 不跑后台循环 |
| `worker` | **只有** `/healthz`、`/readyz`、`/metrics` | 上报管道、熔断器评估、租约回收、代理健康检查、告警评估、热状态快照、分区维护 |
| `all` | 全部 | 全部 |

当"提供服务"和"处理数据"的扩容曲线不一样时就拆开 —— 因为长轮询数量而变大的集群并不需要更多上报 worker。
保持**至少两个 worker**，这样一个挂掉不会让管道停摆；另外记住 `worker` 实例也会回 `/readyz`，
所以不要让负载均衡把它放进流量池。

### 上报分片是怎么分的

每个 worker 实例每 2 秒在 Redis 里写一次心跳；心跳的存活窗口是 10 秒。从存活成员的有序列表里，每个实例
算出自己的目标份额 `ceil(分片数 / 存活数)`，从 `序号 × 分片数 / 存活数` 处开始探测（这样两个实例不会去抢
同一个分片），并对每个分片取一把 TTL 10 秒、每 3 秒刷新的锁。超出目标份额的分片会被释放。每个持有的分片
跑一个顺序消费者，消费者组是 `workers`，这正是"同一身份的上报按序处理"的来源。

对容量规划的推论：**一个分片只有一个所有者**，所以分片数就是上报并行度的硬上限。按每个 worker 实例
2–4 个分片来配。worker 实例多于分片数会让部分实例空转。两个信号要一起看：`spinneret_stream_pending`
在涨而 worker 都很忙，说明 worker 是瓶颈；pending 均匀分布而 CPU 空闲，说明分片太少。

### acquire 准入预算是怎么分的

这一节会改变你对副本数的看法，而且它是新加的 —— 扩容之前请先读。

一次失败的 `Acquire` 大约是一次成功的五倍开销，所以一个把所有请求都排队到 Redis 的集群，退化方式是崩塌
而不是变慢。准入控制给每个实例在 Redis 上在飞的 `acquire` 脚本数量设上限，超出的部分**在服务端**就丢掉，
一条 Redis 命令都不发。

**本页所有吞吐数字都是在没有这道闸门的情况下实测的** —— 包括规格表所依据的"每实例约每秒 5,000 次
acquire" —— 而且没有任何一个在闸门打开后重测过。把它们当作负载的形状，而不是可以精确到小数位的集群
规划依据 —— [性能与调优](./17-performance.md#准入控制)里有自己动手 A/B 测量的方法。

| 变量 | 默认 | 含义 |
| --- | --- | --- |
| `SPINNERET_ACQUIRE_FLEET_INFLIGHT` | `64`（0–65536） | **整个集群**允许在飞的并发 acquire 脚本数。每个 API 实例准入的是这个预算除以它看到的存活 API 实例数，并夹在 `[4, 4096]` 内。`0` 关闭准入控制 |
| `SPINNERET_ACQUIRE_MAX_INFLIGHT` | `0` = 推导（0–4096） | 钉住本实例的上限而不去除以集群预算，同时停掉统计对等实例的心跳 |

这个除法用的注册表和分片那套是同一类：每个 API 实例每 2 秒心跳一次，心跳存活窗口 10 秒，每次心跳都重新
除一遍。默认编排两个副本、预算 64，每个准入 32。扩到四个，每个准入 16 —— 集群总量不变。一次尝试最多等
50 毫秒拿许可，且不超过它自己剩余的 `wait_ms`；超出就以 `unavailable` 被丢掉，原因是 `overloaded`，
并附带一个抖动到 100–200 毫秒的重试提示。批量请求的许可按 `count` 加权。

所以：**增加副本增加的是服务端容量 —— 长轮询容量、上报处理能力、可用性 —— 而不增加 Redis 并发。**
这是刻意设计的，也正是"给一个已经到了 Redis 拐点的集群加副本没有用"的原因。

三个运维推论：

1. **每个 API 实例都必须有互不相同的 `SPINNERET_INSTANCE_ID`。** 这个 id 就是预算所除的那个注册表成员名，
   所以 N 个实例共用一个 id 会塌缩成一个成员，每个都报 `spinneret_acquire_peers = 1`，每个都准入完整预算
   —— Redis 上的并发是预期的 N 倍。默认值是主机名加一个随机后缀，在 Compose 和 Kubernetes 上天然互不相同，
   但在"既模板化主机名、又钉死 id"的部署里**不是**。
2. **`SPINNERET_ACQUIRE_MAX_INFLIGHT` 要么每个 API 实例都钉，要么一个都不钉。** 钉住的实例不会注册为
   acquire 方；那些走推导的实例于是除以一个偏小的数，集群总量超出预算的部分正好是那些钉死实例的全部配额。
   什么时候该钉：Redis 容量随主节点数增长时（填每主节点的数值）、你自己实测过上限时，或者为了可复现的压测。
3. **当 `overloaded` 和 `spinneret_acquire_queued` 在涨、而 Redis CPU 还没到顶时，要提高集群预算而不是
   副本数。** 如果 `exhausted` 是**伴随** Redis CPU 一起涨的，那你已经过了拐点，答案是降低进来的负载或者
   加 Redis。

默认值背后的实测数据，以及它所防止的拥塞行为：[性能与调优 → 准入控制](./17-performance.md#准入控制)。

---

## 一台机器上跑多套

区分两套部署的是 Compose 项目名：数据卷、网络和容器名都由它派生。安装脚本接受 `SPINNERET_PROJECT`
（默认 `spinneret`）并把它写进生成的 `spnrctl`，所以之后每一条命令都会落到正确的那一套上。

```bash
SPINNERET_PROJECT=staging SPINNERET_INSTALL_DIR=/srv/spinneret-staging \
SPINNERET_PORT=8081 bash install.zh.sh
```

有两点要知道。自带编排把构建出的镜像打成 `spinneret:local`，这个名字对这台机器上每个项目都是同一个；
所以项目名不是 `spinneret` 时，安装脚本会写 `compose.build.yml` 给它加前缀，这样一套里的构建就不会盖掉
另一套容器所引用的 tag。另外，如果两套部署共用一个 Redis，它们必须用不同的 `SPINNERET_REDIS_PREFIX`
（默认 `sp`）—— 不过 Compose 会给每个项目各自一个 Valkey，所以这只在手写部署里才会遇到。

---

## 多机与编排系统

本仓库里没有 Helm chart，也没有 Kubernetes 清单。也不需要有：这个容器就是一个普通的无状态 HTTP 服务，
而编排系统需要知道的东西很短。

**容器需要什么：**

| | |
| --- | --- |
| 环境变量 | `SPINNERET_DATABASE_URL`、`SPINNERET_REDIS_URL`（或 `SPINNERET_REDIS_ADDRS`），以及 `SPINNERET_KEK_FILE` / `SPINNERET_KEKS` 之一。其余都有默认值 —— 见[配置参考](./03-configuration.md) |
| 每个实例一个互不相同的 `SPINNERET_INSTANCE_ID` | 默认值（主机名加随机后缀）本来就互不相同；不要把它模板成一个共用的值 |
| 密钥加密密钥 | 以挂载文件形式（`SPINNERET_KEK_FILE`，Compose 编排就是这么做的，走 Docker secret）或以 `SPINNERET_KEKS` 环境变量形式。挂载文件更好：环境变量出现在更多地方 |
| 一个端口 | 默认 8080，`SPINNERET_HTTP_ADDR` |
| 一个就绪探针 | `GET /readyz`，每 2 秒一次。镜像自带的健康检查是 `spnr healthcheck --url http://127.0.0.1:8080/readyz` |
| 一个**大于 40 秒**的终止宽限期 | Compose 编排用的是 `stop_grace_period: 40s`，对应 30 秒的 `SPINNERET_SHUTDOWN_TIMEOUT`。编排系统里对应的设置不要更低 |
| 用 `SIGTERM` 停止 | 第二个信号会终止一个耗时过长的关闭流程，所以会升级信号的进程管理器没问题 |

**什么是无状态的：** 整个容器。它不往本地磁盘写任何东西，不需要数据卷，以 distroless 的 `nonroot`
用户（uid 65532）在只读根文件系统上也能正常跑。镜像里没有 shell，所以排查靠 `kubectl logs`、
`/metrics` 和 `/readyz`，而不是 `exec sh`。

**哪些东西必须跨机共享：** PostgreSQL、Redis、密钥加密密钥，以及用到时的 ClickHouse。这四样对每个实例
都必须是**同一个对象**，而不是每台机器一份。两个实例连不同的 Redis，那是两套碰巧共用一个数据库的部署，
它们会做出互相矛盾的调度决策。

**运维方自己要提供什么：** 三个数据库及其调参（从[Compose 编排逐个服务](#compose-编排逐个服务)里的设置
起步 —— 它们是压测出来的）、TLS 终止、一个会探 `/readyz` 且不缓冲流的负载均衡、PostgreSQL 和 KEK 的备份、
把 KEK 作为 secret 投递进去、指标抓取，以及迁移这一步。迁移请作为一次性任务在滚动新版本之前跑 ——
`spnr migrate up`，或者在一个实例上用 `spinneret-server --migrate` —— 不要作为每个副本的 init 容器；
advisory lock 让那样做是安全的，但没有意义。

---

## 升级

迁移嵌在二进制里。有流量在跑时的安全顺序，也就是安装脚本菜单第 2 项自动做的事：

1. **先备份。** 「管理 → 10」，或者[运维手册 → 备份](./16-operations.md#备份)里的命令。
2. `spnr migrate status` —— 它会打印已应用版本和二进制内嵌版本。升级前后各跑一次。
3. **先拉取或构建，后钉 tag。** 先把新的 `SPINNERET_IMAGE_TAG` 写进 `.env`、**然后**才发现拉不到，
   会让这套部署指向一个不存在的镜像。
4. **把迁移作为单独一步**，不要让它作为 `up` 的副作用发生。`migrate` 是一次性服务，容器规格没变时
   Compose 可能认为它已经满足了，而"表结构就是它原来那个样子"不是一件该靠推断的事。如果迁移在这里失败，
   旧容器还在跑，什么都没换。
5. **然后再替换副本**，能滚动就滚动。
6. worker 重启期间盯住每个副本的 `/readyz` 和 `spinneret_stream_pending`。

```bash
# 已发布镜像这条路
./spnrctl pull && ./spnrctl run --rm migrate && ./spnrctl up -d --wait

# 源码这条路
git -C . pull && ./spnrctl build && ./spnrctl run --rm migrate && ./spnrctl up -d --wait
```

**滚动替换副本。** Compose 会把一个有副本数的服务的所有副本一起重建，两个副本时这就是五到十秒的
"没有新东西启动"窗口。负载均衡的重试能盖住它，但没必要把重试花在这上面。移除一个容器，然后跑
`up -d --no-recreate --no-deps spinneret`，会用新的规格把空出来的槽位填上，而其他副本不动：

```bash
# 一个副本一个副本地来；--wait 会阻塞到新容器健康为止
for id in $(./spnrctl ps -q spinneret); do
  docker rm -f "$id"
  ./spnrctl up -d --no-recreate --wait --no-deps spinneret
done
./spnrctl up -d --wait        # 最后一遍把负载均衡和其他服务带上
```

注意这里用的是对容器 id 执行裸的 `docker rm -f`，而不是 `docker compose rm`：Compose 寻址的是**服务**，
而一个有副本数的服务的某一个副本并不是一个服务。

40 秒的停止宽限期正是每一步都干净的原因：被移除的那个容器会先排空五秒（期间 `/readyz` 回 `draining`），
把在途请求做完，把后台循环刷干净，Docker 才会放弃它。安装脚本做的就是这件事，一个副本一个副本地做，
而且一旦有任何一步不顺就退回普通重建 —— 慢一点的升级好过一次做了一半的聪明升级。

**回滚。** 把 `SPINNERET_IMAGE_TAG` 改回上一个 tag，再走一遍上面的顺序。这是最快的路，而且
**在表结构没变的前提下**有效。它不能跨着一次迁移往回走：**永远不要用 `spnr migrate down` 回滚** ——
向下迁移会删表，以及表里的数据。恢复备份才是安全的路。

**一次表结构迁移允许什么、不允许什么。** 向前迁移在 PostgreSQL advisory lock 下执行，所以多个实例同时
启动是安全的，而且旧副本和新副本可以在一次滚动重启的时长内并存服务。它们不能做到的是让旧二进制长期理解
新表结构 —— 把这个混合窗口控制在一次重启的量级，而不是一周。有两样东西在升级时绝不能变：
`SPINNERET_REPORT_SHARDS`（租约 ID 编码了分片），以及这个数据库初始化时用的那把密钥加密密钥。

节点不需要同步升级：节点 API 和 SDK 在两个方向上都忽略未知 JSON 字段，而 SDK 客户端会重试
`unavailable` —— 那正是一个在途 POST 撞上正在停止的副本时的样子。

完整清单：[运维手册 → 升级与数据库迁移](./16-operations.md#升级与数据库迁移)。

---

## 卸载

三个级别，按这个顺序。安装脚本菜单第 4 项提供的正是这三个，而且级别 2 和 3 要求你输入 `delete` ——
不是一个 y/n。

```bash
# 1. 停掉编排，一个字节的数据都不删。
./spnrctl down --remove-orphans

# 2. 同时删掉数据卷：pgdata、valkeydata、chdata。
./spnrctl down -v --remove-orphans

# 3. 同时删掉安装目录，包括 .env 和 secrets/kek.key。
cat deploy/compose/secrets/kek.key      # 如果你以后还可能需要那些导出，先把它复制到别处
rm -rf /opt/spinneret
```

每个级别的代价：

| 之后 | 能恢复吗 |
| --- | --- |
| 级别 1 | 全部能。`./spnrctl up -d --wait` 就回来了 |
| 级别 2 | 这套部署里每一个身份、代理、账号、策略、配置项、密钥、用户、令牌和审计记录都没了。密钥加密密钥还在安装目录里，所以**用它导出的备份仍然可以恢复到一套全新的编排里** |
| 级别 3 | 什么都恢复不了，除非 `kek.key` 和 `.env` 已经在这台机器之外。没有那把密钥，这套部署的任何备份都再也打不开了，谁都打不开 |

三个级别都不会动 Docker 镜像：这里没有任何东西会去清理一台共用的机器。`docker image prune` 和
`docker builder prune` 由你自己决定；注意 BuildKit 的缓存是按主机而不是按项目的 —— 清掉它会让这台机器上
下一次任何构建都变成冷启动。

安装脚本的级别 3 还额外拒绝任何不包含 `deploy/compose/docker-compose.yml` 的路径，并且直接拒绝
`/`、`/usr`、`/etc`、`/var`、`/home`、`/root`、`/opt`、`/bin`、`/sbin`、`/lib` 和 `/boot`。

---

## 不用 Docker 部署

"支持"的意思是二进制本身不依赖容器。"不支持"的意思是没有脚本，而且 Compose 编排替你做的那些调参会变成
你的活。下面是诚实的版本。

### 先要装什么

| | 版本 | 为什么 |
| --- | --- | --- |
| PostgreSQL | 17 | 存储。从编排的参数起步：`max_connections` 高于 `实例数 × SPINNERET_DATABASE_MAX_CONNS` 加余量，`shared_buffers` 512 MiB 或更多，`wal_compression=on` |
| Valkey 或 Redis | Valkey 8 / Redis 7+ | 持久化（`appendonly yes`、`appendfsync everysec`）、关掉 RDB 存盘点、`auto-aof-rewrite-percentage 300`、`auto-aof-rewrite-min-size 1gb`、**`maxmemory-policy noeviction`** 且不设 `maxmemory`。这五项不是可选的偏好 —— 见 [valkey](#valkey) |
| ClickHouse | 25.8，可选 | 只在你想要原始请求事件时才需要。缓存上限照 `deploy/compose/config/clickhouse-limits.xml` 设 |
| Go | 1.27.1 或更高 | 用来构建 |
| Node | 22.13 或更高 | 用来构建控制台 |
| pnpm | 10.27.0 | `corepack enable` 会按 `web/package.json` 里钉住的版本安装 |

### 构建

控制台是用 `go:embed` 编进二进制的，所以**前端构建必须发生在 Go 构建之前**。不构建前端的话，嵌进去的
只有 `web/dist/.keep`，服务端不会提供任何 UI 文件 —— 看起来像是部署坏了，而不是像漏了一步。

```bash
git clone https://github.com/TikHub/Spinneret.git
cd Spinneret

make web-install      # cd web && pnpm install --frozen-lockfile
make web              # cd web && pnpm build       -> web/dist
make build            # -> bin/spinneret-server 和 bin/spnr
```

`make build` 是两次 `go build`，带 `CGO_ENABLED=0 -trimpath`，并用 `-ldflags` 烙进版本号；产物是两个静态
二进制，可以拷到一台既没有 Go 也没有 Node 的机器上。容器做的也是同一件事、同一个顺序 ——
`deploy/docker/Dockerfile` 是一个三阶段构建（Node，然后 Go，然后 distroless），它把第一阶段的
`web/dist` 拷进第二阶段。

把 `bin/spinneret-server` 和 `bin/spnr` 拷到 `/usr/local/bin/`。

### 配置

全部是 `SPINNERET_*` 环境变量，没有配置文件。最少需要：

```bash
# /etc/spinneret/spinneret.env  —— 权限 0600，属主 root
SPINNERET_HTTP_ADDR=127.0.0.1:8080
SPINNERET_DATABASE_URL=postgres://spinneret:…@db.internal:5432/spinneret?sslmode=require
SPINNERET_REDIS_URL=redis://cache.internal:6379/0
SPINNERET_KEK_FILE=/etc/spinneret/kek.key
SPINNERET_TRUSTED_PROXIES=10.0.0.0/8
SPINNERET_COOKIE_SECURE=true
SPINNERET_LOG_FORMAT=json
```

用 `spnr kek generate` 生成一次密钥文件，并且在放入任何数据之前先备份它：

```bash
spnr kek generate > /etc/spinneret/kek.key    # 一行：k1:<32 随机字节的 base64>
chmod 0600 /etc/spinneret/kek.key             # 这里 0600 是对的 —— 没有容器用户要迁就
chown spinneret: /etc/spinneret/kek.key
```

启动任何东西之前先校验整套环境。`spnr config check` 会把它回显出来，校验过，所有凭据都已脱敏 ——
一个写错的时长、一个越界的分片数或者一个缺失的必填变量，它一秒钟就能抓出来，而不是让你看崩溃循环：

```bash
set -a; . /etc/spinneret/spinneret.env; set +a
spnr config check
```

### 迁移与引导

```bash
spnr migrate up                 # 内嵌迁移，由 advisory lock 串行化
spnr migrate status             # 已应用版本和内嵌版本
printf '%s\n' "$ADMIN_PASSWORD" | spnr admin init --username admin --password-stdin
```

`spnr admin init` 是幂等的：已经存在平台管理员时，它打印 `already initialized` 并以 0 退出。

### 一个 systemd unit

```ini
# /etc/systemd/system/spinneret.service
[Unit]
Description=Spinneret control plane
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=spinneret
Group=spinneret
EnvironmentFile=/etc/spinneret/spinneret.env
ExecStart=/usr/local/bin/spinneret-server
Restart=always
RestartSec=2
# SIGTERM 开始排空；第二个信号会中止它。这个值要高于
# SPINNERET_SHUTDOWN_TIMEOUT（默认 30s），这样 systemd 不会杀掉正在排空的实例。
KillSignal=SIGTERM
TimeoutStopSec=45
# 这个进程不往本地磁盘写任何东西。
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
NoNewPrivileges=yes
ReadOnlyPaths=/etc/spinneret

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now spinneret
curl -s 127.0.0.1:8080/readyz
```

要拆分角色就往 `ExecStart` 加 `--role api` 或 `--role worker`，把 unit 放到不同机器上或做成多个 unit
实例。至少保留两个 worker。

### 还有哪些是你自己的活

- 前面放一个反向代理，按[反向代理与 TLS](#反向代理与-tls)配置；或者在服务端上用
  `SPINNERET_TLS_CERT_FILE` / `SPINNERET_TLS_KEY_FILE`。
- **每个实例一个互不相同的 `SPINNERET_INSTANCE_ID`。** 默认值是主机名加随机后缀，本来就互不相同 ——
  坑在于从模板里把它设成同一个值。搞错了会让集群的 Redis 并发变成实例数倍；见
  [横向扩容](#acquire-准入预算是怎么分的)。
- PostgreSQL **和**密钥文件的备份，以及一次恢复演练 —— [运维手册](./16-operations.md#备份)。
- 抓取 `/metrics`；如果 API 监听是暴露出去的，就把它挪到 `SPINNERET_METRICS_ADDR`。
- 升级：构建、`spnr migrate up`，然后一个一个地重启 unit。

除此之外的一切 —— 配置、策略、密钥、扩容、数据保留 —— 都和 Compose 部署完全一样，因为它们都和容器无关。

---

## 安装出问题时

| 你看到的 | 意思 |
| --- | --- |
| `Compose is 2.20.x; this stack needs 2.24 or newer` | 升级 Docker。覆盖文件用的合并标签在 2.24 之前不存在 |
| `No terminal to ask questions on` | 脚本被管道执行，而且没有可用的 `/dev/tty`。下载下来直接跑，或者加 `--yes` |
| `required variable PG_PASSWORD is missing a value` | Compose 找不到可读的 `.env`。要么 `deploy/compose/.env` 还没生成 —— 跑 `./scripts/compose-init.sh` —— 要么 `--env-file` / `COMPOSE_ENV_FILES` 指向了别处。Compose 是在编排文件旁边找它，不是在你当前目录找 |
| `Docker is running, but your user cannot reach it` | 你的用户不在 `docker` 组里。在多数机器上那个组等于 root，所以这里不会替你加 |
| `tikhubio/spinneret:latest cannot be fetched from here` | 这个 tag 还没发布，或者这台机器到不了镜像仓库。你的检出没问题，改成从源码构建 |
| `vault: read kek file … permission denied` | 容器里的用户读不到 `kek.key`。它必须是 `-rw-r--r--`；不要"加固"成 `0600` |
| `vault: kek "k1" must be 32 bytes, got N` | 密钥文件被截断或损坏 |
| `load dedupe_pepper system key (is the KEK the one used to initialize this database?)` | 这个数据库用的不是这把密钥 —— 这是"用错密钥恢复"的特征。除了那把正确的密钥没有别的办法 |
| `bind: address already in use` | 端口被别的东西占了。`ss -ltnp 'sport = :8080'` 能点名是谁；改 `.env` 里的 `SPINNERET_PORT` 再起 |
| 启动时 `connect clickhouse: …` | 设了 `SPINNERET_CLICKHOUSE_URL` 而 ClickHouse 连不上 —— 小机器上常常是被 OOM 杀了。加内存，或者把变量留空以不带分析功能运行 |
| ClickHouse 起不来，日志里提到 `nofile` | 容器要 262,144 个软硬描述符限制。查宿主机的 `ulimit -Hn` |
| `/readyz` 一直不 `ok` | 响应体会点名是哪个依赖：`postgres`、`redis`、`catalog` 或 `hotstate`。`hotstate: building` 在首次启动时是正常的；`hotstate: epoch missing (rebuild pending)` 需要 `spnr rebuild` |
| 所有节点都拿到 `429 no_proxy_available` | 通常是 `SPINNERET_PROXY_CHECK_URL`：它默认指向公网上的一个地址，而到不了那里的机器会把每个代理都标成死的。把它指到可达的地方 |
| 控制台能打开但没有任何 UI 文件 | 构建时漏了前端。先 `make web` 再 `make build` |
| 源码构建失败 | 单独构建那一个服务，这样错误不会被埋在进度流里：`./spnrctl build spinneret` |

以上每一条，加上安装之后才会遇到的那些，以及该 grep 哪几行日志：[故障排查](./18-troubleshooting.md)。

---

## 下一步

- [配置参考](./03-configuration.md) —— 每个 `SPINNERET_*` 变量、默认值、取值范围，以及什么时候该改它。
- [运维手册](./16-operations.md) —— 备份与恢复演练、密钥轮换、升级、扩容、重建热状态、数据保留、
  故障处置手册。
- [安全](./19-security.md) —— 在别人能访问到它之前应该逐项过一遍的加固清单。
- [可观测性与告警](./12-observability.md) —— 该抓哪些指标，以及哪些信号值得告警。
- [性能与调优](./17-performance.md) —— 实测数字、拥塞行为，以及按收益排序的调优项。
- [故障排查](./18-troubleshooting.md) —— 按你看到的报错文本查。
- [命令行工具](./15-cli.md) —— 每个 `spnr` 命令和每个 `spinneret-server` 选项。
