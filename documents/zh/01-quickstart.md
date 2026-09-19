# 快速开始

**从一台空机器，到一个节点真正租借了身份、发出了请求、上报了结果 —— 大约半小时，而且你会明白
自己做了什么。只需要 Docker，其余全部跑在容器里。**

[English](../en/01-quickstart.md)

---

## 目录

- [你即将搭出什么](#你即将搭出什么)
- [开始之前](#开始之前)
- [步骤 1 —— 把整套服务跑起来](#步骤-1--把整套服务跑起来)
  - [方式 A —— 引导式安装脚本](#方式-a--引导式安装脚本)
  - [方式 B —— 克隆仓库、手动 Compose](#方式-b--克隆仓库手动-compose)
- [步骤 2 —— 确认它是健康的](#步骤-2--确认它是健康的)
- [步骤 3 —— 登录控制台，并把密钥备份出去](#步骤-3--登录控制台并把密钥备份出去)
- [步骤 4 —— 概览页导览](#步骤-4--概览页导览)
- [步骤 5 —— 创建站点、客户端和两个端点组](#步骤-5--创建站点客户端和两个端点组)
- [步骤 6 —— 创建身份类型并导入两个身份](#步骤-6--创建身份类型并导入两个身份)
- [步骤 7 —— 为节点创建 API 令牌](#步骤-7--为节点创建-api-令牌)
- [步骤 8 —— 租借一个身份](#步骤-8--租借一个身份)
- [步骤 9 —— 先上报一次成功，再上报一次失败](#步骤-9--先上报一次成功再上报一次失败)
- [步骤 10 —— 回头看它发生了什么](#步骤-10--回头看它发生了什么)
- [步骤 11 —— 完整的示例节点](#步骤-11--完整的示例节点)
- [停止与清理](#停止与清理)
- [如果出错了](#如果出错了)
- [下一步](#下一步)

---

## 你即将搭出什么

Spinneret 是一个控制平面。爬虫节点从你这里只拿两样东西 —— 服务端地址和一个 API 令牌 ——
其余一切都在请求时从服务端获得：该用哪个身份、走哪个代理、这个端点当前是否正在熔断、它的配置是
什么。作为交换，节点把发生了什么上报回来，服务端把这些上报变成冷却、封禁、健康分和熔断。

```text
Acquire(site, client, uri)          ->  身份 + 凭据 + 代理 + 租约
  ... 节点把请求发给目标站点 ...
Report(lease_id, status, markers)   ->  服务端判定、冷却、封禁、触发熔断
```

读完这一页，你会用 `curl` 亲手跑完这个闭环，并看到服务端因此把一个身份冷却下去。为了让这件事发生，
你不需要部署任何东西 —— 这正是整个设计的要点。

读的时候记住三件事：整套服务是**一个二进制**（`spinneret-server`）加一个管理 CLI（`spnr`），
打在同一个镜像里，前面是 PostgreSQL、Valkey 和（可选的）ClickHouse；控制台是**编译进**二进制的，
没有独立的 web 服务；你在控制台里做的每一个动作，都是对节点所调用的同一套 API 的调用。

---

## 开始之前

| | |
| --- | --- |
| Docker Engine | 24 或更高，带 Compose 插件 —— `docker compose version` 必须是 **2.24 或更高** |
| 架构 | 已发布镜像支持 `x86_64` 和 `arm64`；其他架构可以从源码构建 |
| 内存 | 默认编排 4 GiB；关掉 ClickHouse 的话 2 GiB 也能跑 |
| 磁盘 | 20 GiB |
| 端口 | 一个空闲的宿主机端口，默认 `8080` |
| 宿主机工具（方式 A） | `bash`、`git`、`curl`、`awk`、`base64`、常用 coreutils，以及 `/dev/urandom` 或 `openssl` 之一 |

宿主机上不会装别的东西：PostgreSQL、Valkey、ClickHouse、服务端副本和负载均衡都是容器，数据放在
Docker 命名卷里。

默认编排的 4 GiB 是硬下限，不是建议值：ClickHouse 不管缓存怎么调，空载也在 1.2 GiB 上下，
PostgreSQL 启动时就会分配 512 MiB 的 shared buffers。

完整的环境要求、每一个开关、一台机器上跑多套、以及不用 Docker 的路径，见
[安装与部署](./02-installation.md)。

---

## 步骤 1 —— 把整套服务跑起来

两条路径，结果是同一套服务。方式 A 是三条命令加几个提问；方式 B 是同样的事手动做一遍，适合想看清
每一步的人。挑一条，然后继续[步骤 2](#步骤-2--确认它是健康的)。

### 方式 A —— 引导式安装脚本

`install/install.zh.sh` 会识别这台机器、在缺少 Docker 时给出正确的安装方式、克隆仓库、
**在本机**生成口令和密钥加密密钥、按这台机器的规格写好 Compose 覆盖文件、拉取或构建镜像、
把数据库迁移作为独立一步执行、启动全部服务、轮询 `/readyz` 直到每个依赖都报 `ok`、
创建第一个管理员，最后打印控制台地址和登录凭据。

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.zh.sh -o install.zh.sh
less install.zh.sh       # 先读一遍，你即将运行它
bash install.zh.sh
```

这个顺序不是走过场。任何你管道进 shell 的东西都以你的身份运行，而在装 Docker 那一步，脚本还会问你
要不要以 root 跑一个脚本。它是写来给人读的：每一步之前都会先说自己要做什么，每一次改动之前都会先问。

`install/install.sh` 是同一个脚本的英文提示版 —— 同样的选项、同样的环境变量、同样的管理菜单、
同样的边界。

管道运行同样能提问，因为答案是从 `/dev/tty` 读的，而不是从标准输入读 —— `curl | bash` 的时候，
标准输入就是脚本本身：

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.zh.sh | bash
```

如果根本没有终端，脚本会说明并停下，而不是对「我该监听哪个地址」这种问题悄悄取默认值。
真正要无人值守，就显式说出来：

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.zh.sh | bash -s -- --yes
```

#### 七个问题

每个都有可以直接回车接受的默认值。

| | 问题 | 默认值 |
| --- | --- | --- |
| 1 | 装到哪里 | root 是 `/opt/spinneret`，否则是 `~/spinneret` |
| 2 | 控制台是否只监听 `127.0.0.1`？ | 是 |
| 3 | 用哪个端口 | `8080` |
| 4 | 管理员用户名 | `admin`，并按服务端自己的规则（`^[a-z0-9][a-z0-9._-]{2,63}$`）校验，所以名字不合法只是多敲一次键，而不是启动失败 |
| 5 | 几个服务端副本 | 每两核一个，限制在 1–4，内存低于 4 GiB 时强制为 1 |
| 6 | 可观测性 profile | 关 |
| 7 | 用已发布镜像还是从源码构建 | 已发布镜像，而且这个问题是在确认 `tikhubio/spinneret:latest` 在这台机器上**确实能拉到**之后才问的 |

第 2 个问题回答「否」会发布到 `0.0.0.0`，脚本会警告你：控制台是管理面，而且 `/metrics` 在同一个
监听器上、没有任何认证，所以这种情况前面要有带 TLS 的反向代理，`.env` 里还要设
`SPINNERET_COOKIE_SECURE=true`。第 5 个问题关系到升级 —— 只有一个副本时，升级会有一小段时间没人
提供服务；两个副本就没有，代价是大约多 150–500 MiB 内存。拉镜像大约一分钟；首次构建要 5–15 分钟，
以及大约 2 GB 的构建缓存。

每个答案都能用环境变量预设，这才是 `--yes` 成为一次完整无人值守安装（而不只是「全取默认值」）的原因：
`SPINNERET_PROJECT`、`SPINNERET_INSTALL_DIR`、`SPINNERET_BIND_HOST`、`SPINNERET_PORT`、
`SPINNERET_ADMIN_USERNAME`、`SPINNERET_REPLICAS`、`SPINNERET_ENABLE_OBSERVABILITY`、
`SPINNERET_USE_PUBLISHED`、`SPINNERET_IMAGE`、`SPINNERET_IMAGE_TAG`、`NO_COLOR`。

```bash
SPINNERET_INSTALL_DIR=/srv/spinneret SPINNERET_PORT=9000 \
SPINNERET_REPLICAS=2 SPINNERET_IMAGE_TAG=v0.1.0 \
  bash install.zh.sh --yes
```

管理员口令和两个数据库口令**始终**在本机生成，绝不从环境变量读取。

#### 四个选项

| 选项 | |
| --- | --- |
| `--yes`、`-y` | 每个问题都取默认答案。对已经存在的部署，它**什么都不改**，并如实说明 |
| `--check` | 只识别这台机器并打印会发生什么，然后停下。不改、不写、不问 |
| `--manage` | 直接进入已有部署的管理菜单 |
| `--help`、`-h` | 选项列表 |

在不熟悉的机器上，`--check` 是第一个该跑的：它会报告发行版、架构、核数和内存，Docker 是否装了、
在跑、版本够不够，`git` 和 `curl` 在不在（缺哪个就给出对应的包管理器命令），
以及这台机器的网络能不能真的拉到已发布镜像。

#### 跑完之后它打印什么

```text
==> 完成

    控制台      http://127.0.0.1:8080
    用户名      admin
    口令        <24 位随机字母数字，在这里显示>
    第一次登录后请立刻改掉。在改掉之前，它还明文躺在 .env 里。

    安装目录    /opt/spinneret
    控制脚本    /opt/spinneret/spnrctl ps | logs -f spinneret | restart lb | down
    管理菜单    bash install.zh.sh --manage
    环境文件    /opt/spinneret/deploy/compose/.env  （0600 —— 数据库口令在里面）
    保险箱密钥  /opt/spinneret/deploy/compose/secrets/kek.key  （务必备份；没有它什么都解不开）

    接下来，在这台机器上：
      # 给一个采集节点签发令牌
      /opt/spinneret/spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate \
          token create --name node-1 --scope lease:acquire --scope report:write --scope config:read

      # 然后把节点指向服务端
      export SPINNERET_URL=http://127.0.0.1:8080
      export SPINNERET_TOKEN=<上面打印出来的令牌>

    快速开始    https://github.com/TikHub/Spinneret/blob/main/documents/zh/01-quickstart.md
    Quickstart  https://github.com/TikHub/Spinneret/blob/main/documents/en/01-quickstart.md
```

从现在起有两个文件很重要：`deploy/compose/.env`（权限 `0600`，数据库口令和管理员口令）和
`deploy/compose/secrets/kek.key`（密钥加密密钥）。见[步骤 3](#步骤-3--登录控制台并把密钥备份出去)。

`spnrctl` 是这套部署唯一的入口。它就是 `docker compose`，只是已经带好了每次都必须正确、
而一旦缺失只会带来困惑而不是报错的四样东西：项目名、顺序正确的 compose 文件、
能让 `./config`、`./secrets` 和 `../..` 正确解析的工作目录，以及 `COMPOSE_ENV_FILES`。
名字后面的内容会原样传给 Compose。

```bash
cd /opt/spinneret
./spnrctl ps                        # 有什么在跑
./spnrctl logs -f spinneret         # 跟随服务端日志
./spnrctl restart lb                # 重启某一个服务
```

之后再跑一次 `bash install.zh.sh`，它会自己判断该做哪件事：已经存在的部署（通过询问 Docker
「`spinneret` 这个 Compose 项目是从哪个目录启动的」找到）会打开管理菜单 —— 状态、升级、账号、
令牌、重建热状态、查看配置、健康检查、日志、重启、备份、恢复、磁盘、卸载。
`install/README.md` 用中英两种语言写清了每一个问题、每一个选项、以及它写下的每一个文件。

现在去[步骤 2](#步骤-2--确认它是健康的)。

### 方式 B —— 克隆仓库、手动 Compose

同一套服务，三条命令，没有任何你没法先读一遍的东西。

```bash
git clone https://github.com/TikHub/Spinneret.git
cd Spinneret
```

**1. 写环境文件和密钥。** `compose-init.sh` 只在文件不存在时创建：从仓库自带的 `.env.example`
生成 `deploy/compose/.env`，其中 `PG_PASSWORD`、`CLICKHOUSE_PASSWORD` 和
`SPINNERET_ADMIN_PASSWORD` 是随机值；以及 `deploy/compose/secrets/kek.key`，内容是一个新的
32 字节密钥，形如 `k1:<base64>`。它可以重复执行 —— 已有文件会保留 —— 并且需要 `PATH` 上有
`openssl`。两个文件都在 git 忽略列表里，也被排除在 Docker 构建上下文之外。

```bash
./scripts/compose-init.sh
chmod 0700 deploy/compose/secrets      # 脚本不做这一步，安装脚本会做
```

```text
created /path/to/Spinneret/deploy/compose/.env
created /path/to/Spinneret/deploy/compose/secrets/kek.key (back it up: data encrypted with it is unrecoverable without it)
```

这个 `chmod` 是 `compose-init.sh` 唯一留给你的一步：它故意把 `kek.key` 写成 `0644`
（见[步骤 3](#步骤-3--登录控制台并把密钥备份出去)），保护应该加在目录上，而它创建出来的目录
是所有人可读的。

**2. 构建并启动。** PostgreSQL、Valkey、ClickHouse、一次性的迁移任务、两个服务端副本，
以及 Caddy 负载均衡。

```bash
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
```

首次 `--build` 要 5–15 分钟，而且需要能访问 Go 和 Node 的包仓库；想直接拉已发布镜像，见
[安装与部署 → 已发布镜像还是从源码构建](./02-installation.md#已发布镜像还是从源码构建)。
`--wait` 会等到每个容器都报告健康才返回。数据库迁移由 PostgreSQL 的 advisory lock 串行化，
所以多个实例同时启动是安全的。

**3. 创建第一个管理员。** 一次性的 `init-admin` 服务会创建平台管理员
（`SPINNERET_ADMIN_USERNAME`，默认 `admin`）、租户 `default`、命名空间 `default`
及其四条内置默认策略。它是幂等的：

```bash
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

第二次运行会打印

```text
already initialized
```

并以 0 退出。

**一个能省掉一小时的提醒。** Compose 是从 **compose 文件所在目录**读 `.env` 的，不是从你当前目录读；
而且编排里的相对路径（`./config`、`./secrets`、构建上下文 `../..`）也都相对同一个目录解析。
要么 `cd deploy/compose` 后用裸 `docker compose`，要么用安装脚本写出来的 `spnrctl`。
看到 `required variable PG_PASSWORD is missing a value`，说明 `.env` 根本还不存在 ——
跑一次 `./scripts/compose-init.sh`。这跟你在哪个目录无关：`-f deploy/compose/docker-compose.yml`
在任何目录下都能用，因为 Compose 的项目目录取自 compose 文件本身。真正受当前目录影响的是
写成裸 `-f docker-compose.yml` 的情况 —— 那只能在 `deploy/compose` 里解析得到。

---

## 步骤 2 —— 确认它是健康的

```bash
cd deploy/compose
docker compose ps -a --format 'table {{.Service}}\t{{.Status}}'
```

```text
SERVICE           STATUS
clickhouse        Up 10 hours (healthy)
lb                Up 20 hours (healthy)
migrate           Exited (0) 20 seconds ago
postgres          Up 28 hours (healthy)
spinneret         Up 6 minutes (healthy)
spinneret         Up 6 minutes (healthy)
valkey            Up 11 hours (healthy)
```

`migrate` 以 `0` 退出是对的 —— 它是一次性任务，服务端依赖它成功完成。两行 `spinneret`
就是默认的副本数。

然后问服务端自己。`/healthz` 只说明进程活着；`/readyz` 会逐个点名每个依赖：

```bash
curl -s 127.0.0.1:8080/healthz
curl -s 127.0.0.1:8080/readyz
```

```json
{"status":"ok"}
```

```json
{"checks":{"catalog":"ok","hotstate":"ok","postgres":"ok","redis":"ok"},"status":"ok"}
```

这四个名字就是整个就绪契约，而某一项不是 `ok` 时，它的值就是诊断结论：

| 检查项 | `ok` 表示 | 不是 `ok` 时 |
| --- | --- | --- |
| `postgres` | 连接池 ping 成功 | `unreachable` —— 口令和数据卷不匹配，或者连接池被打满 |
| `redis` | Valkey/Redis ping 成功 | `unreachable` —— Valkey 挂了，小内存机器上多半是被 OOM killer 杀了 |
| `catalog` | 命名空间快照已加载 | `not loaded` —— 稍等；如果一直如此，去查 PostgreSQL |
| `hotstate` | epoch key 存在，且在运行 worker 的实例上热状态已构建完成 | 首次启动时是 `building`；Valkey 被清空或数据卷被重建后是 `epoch missing (rebuild pending)` —— 执行 `spnr rebuild` |

任何一项失败时，顶层 `status` 是 `unavailable`，HTTP 状态码是 `503`。

**负载均衡该探的是 `/readyz`，不是 `/healthz`。** 正在关闭的副本会在 `SPINNERET_SHUTDOWN_TIMEOUT`
的前四分之一（最多 5 秒）里回 `503 {"status":"draining"}`，**然后**才关掉监听器 ——
这正是滚动重启对节点无感的原因。

还有两个值得跑一次的检查，都在服务端容器里执行：

```bash
docker compose exec spinneret spnr migrate status
```

```text
database schema version 6 (up to date)
```

（数字是你这个版本的 schema 版本号，`up to date` 才是重点。）

```bash
docker compose exec spinneret spnr config check
```

```json
{
  "acquire_fleet_inflight": "64",
  "acquire_max_inflight": "0",
  "clickhouse_url": "clickhouse://spinneret:xxxxx@clickhouse:9000/spinneret",
  "database_url": "postgres://spinneret:xxxxx@postgres:5432/spinneret?sslmode=disable",
  "http_addr": ":8080",
  "instance_id": "899a20dbb6ef-ac8841",
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

这就是服务端实际解析出来的整个 `SPINNERET_*` 环境，所有凭据都被替换成了 `xxxxx`。
它是手动改过 `.env` 之后的起飞前检查：如果你设的某个变量没有以你期望的值出现在这里，
那服务端就没在用它。每个字段都在[配置参考](./03-configuration.md)里有说明。

---

## 步骤 3 —— 登录控制台，并把密钥备份出去

### 管理员口令从哪里来

| 方式 | 在哪里 |
| --- | --- |
| A —— 安装脚本 | 结束时在**口令**一行打印一次。同时也写进了 `.env` 的 `SPINNERET_ADMIN_PASSWORD` |
| B —— 手动 | `grep SPINNERET_ADMIN_PASSWORD deploy/compose/.env` |

```bash
grep SPINNERET_ADMIN_PASSWORD deploy/compose/.env
```

它是 24 位随机字母数字（`compose-init.sh` 生成的是 20 位），在本机生成。只有一次性的
`init-admin` 服务会读它，运行中的服务端从不读。

打开 <http://127.0.0.1:8080>，用 `admin` 和这个口令登录。

**首次登录后就把它改掉。** 在你改之前，它还以明文躺在 `.env` 里。右上角的账号菜单进入
**个人资料**页，在那里改口令会登出你**其他**所有会话，保留当前这个。安装脚本的管理菜单也能改，
而且会顺手问你要不要同步更新 `.env` 里的 `SPINNERET_ADMIN_PASSWORD`，免得它变成过期信息。

关于会话，有几件事早知道能少走弯路：它是服务端会话，存在 Redis/Valkey 里，
浏览器侧只有 `spinneret_session` cookie；有效期是 `SPINNERET_SESSION_TTL`（默认 `12h`），
而且是滑动的 —— 每次使用都会延长。登录失败有两重节流：同一用户名 5 次、同一客户端 IP 20 次，
窗口都是 15 分钟；触发任一条就返回 `login_throttled`，登录表单会告诉你还要等多久。
控制台跟随浏览器语言，切换入口在页头，紧挨着主题菜单。

### 放数据之前，先把密钥备份出去

`deploy/compose/secrets/kek.key` 是密钥加密密钥。这套部署里每一份身份载荷、每一个代理 URL、
每一条保险箱密钥、每一份通知渠道凭据，都是用一个逐记录的数据密钥加密的，而每一个数据密钥
都用这一把密钥包裹。数据库里存的只有被包裹的密钥和密文 —— 这把密钥本身从不进入数据库。

**没有它就没有任何恢复途径。** 用错的密钥恢复数据库转储会失败并报
`load dedupe_pepper system key (is the KEK the one used to initialize this database?)`，
除了换成正确的那一把，没有别的解法。

```bash
cp deploy/compose/secrets/kek.key ~/somewhere-safe/spinneret-kek-$(date -u +%Y%m%d).key
cp deploy/compose/.env            ~/somewhere-safe/spinneret-env-$(date -u +%Y%m%d)
```

现在就把两个文件都复制到这台机器之外，趁着还没有东西可丢。`.env` 要和它一起备份，
因为里面的口令是数据卷当初建立时用的那一套；换一份新文件是打不开旧数据卷的。

**注意。** `kek.key` 是 `0644` 权限，这是**故意的**，不是需要修的问题 —— 保护应该加在目录上，
安装脚本会把目录设成 `0700`，方式 B 里则是你自己设的（`chmod 0700 deploy/compose/secrets`）。
Compose 会把这个文件原样 bind-mount 进容器，而容器以 distroless 的 `nonroot` 用户（uid 65532）
运行，与任何宿主机用户都不对应；`0600` 的文件在那里读不了，服务端会以
`vault: read kek file "/run/secrets/kek": … permission denied` 退出。

什么才算一份完整备份，以及怎么通过把它恢复进一个空栈来证明它可用，见
[运维手册 → 备份](./16-operations.md#备份)。

---

## 步骤 4 —— 概览页导览

登录后落在**概览**页。它是某一个命名空间的实时健康状况，每 10 秒自动刷新一次，
页头的统计窗口选择器（`1m`、`5m`、`15m`、`1h`）决定你看到的是一次尖刺还是一个趋势。

![控制台概览页](../images/overview-zh.png)

从上到下：

| 区块 | 它告诉你什么 |
| --- | --- |
| 六个指标卡 | **可用身份**、**获取 QPS**（下面附获取失败率）、**上报 QPS**（附待处理上报数）、**成功率**（附未知率）、**风险率**、**打开的熔断器**（附半开数量） |
| 两张图 | 最近一小时的获取速率，以及最近一小时的成功率与风险率 |
| 站点 | 每个站点一张卡片：身份按状态分布、可用身份、代理按状态分布、获取与上报速率、成功/风险/未知/客户端错误各项比例、打开与半开的熔断器，以及低于低水位的端点组 |
| 打开的熔断器 | 命名空间内所有打开或半开的熔断器，附端点组和还会打开多久 |
| 低水位告警 | 可用身份掉到你设定阈值以下的端点组 |
| 节点（最近 1 小时） | 按节点：获取次数、上报次数、未上报租约、被拒绝次数、未上报比例 |

其中三个指标卡是你最后真正会盯的。**成功率**低于 80 % 会变琥珀色（完全没有上报流量时保持中性色），
**风险率**达到 10 % 及以上会变红，**打开的熔断器**只要有一个打开就变红 —— 颜色从不是唯一的信息载体，
数字就摆在那里。站点卡片回答的是「到底是**哪个**站点不舒服」，所以一次真实故障的排查路径通常是：
概览 → 那张不舒服的站点卡片 → 身份 或 熔断器。

现在这一页还没有任何数字，因为还没有站点。接下来四步就是来解决这件事的。
控制台的其余部分 —— 侧边栏的六个分组、租户与命名空间切换器、页面说明卡片、只读用户看到什么 ——
在[控制台总览](./05-console-overview.md)。

---

## 步骤 5 —— 创建站点、客户端和两个端点组

这是其他一切挂靠的结构，所以值得理解一遍，而不是照抄。

| 术语 | 是什么 |
| --- | --- |
| **站点** | 一个目标，属于一个命名空间。冷却、封禁、熔断和策略绑定都以它为范围 |
| **客户端** | 访问该站点的一类调用方 —— `web`、`mobile`、`partner`。身份属于某一个客户端 |
| **端点组** | 风控特征相近的一组 URI。**轮换、冷却与熔断的基本单位** |
| **URI 规则** | 把请求路径映射到端点组的规则：`exact`、`template`、`prefix` 或 `regex` |

我们要建的是站点 `example-site`，一个客户端 `web`，两个端点组：`search` 覆盖 `/search` 下的一切，
`detail` 覆盖 `/detail/{id}`。

### 在控制台里

1. **站点** → **新建站点**。名称 `example-site`（`^[a-z0-9_][a-z0-9_.-]{0,63}$` ——
   小写字母、数字、`_`、`.` 和 `-`，最多 64 个字符，首字符只能是字母、数字或 `_`，
   创建后不可修改），显示名称 *Example site*，客户端 `web` —— 输入后按回车。创建。
2. 站点出现时已经带了**一个**端点组：`_default`。每个客户端都有一个，它接收该客户端下所有未匹配
   任何规则的请求路径，且不可删除。
3. **新建端点组** → 客户端 `web`，名称 `search`。低水位留 `0`（它是可用身份数的告警阈值，
   `0` 表示关闭告警）。创建。
4. 在新建出来的组上点 **编辑 URI 规则** → **添加规则** → 类型 `prefix`，模式 `/search`。保存。
5. 对 `detail` 重复一遍，规则类型 `template`，模式 `/detail/{id}`。

规则的匹配优先级是固定的 —— 精确 → 模板（字面段更多者优先）→ 前缀（最长者优先）→ 正则
（按列表顺序）→ `_default` —— 所以列表顺序只影响正则规则和模板的平局判定。

### 走 API

控制台做的每一件事都是对同一套 API 的调用。这些调用需要一个带 `admin` 权限范围的令牌
（见[步骤 7](#步骤-7--为节点创建-api-令牌)）或者一个控制台会话；下面的例子用令牌。

```bash
export SPINNERET_URL=http://127.0.0.1:8080
export SPINNERET_ADMIN_TOKEN=spn_…          # 一个带 admin 权限范围的令牌

curl -sS -X POST "$SPINNERET_URL/spinneret.v1.SiteAdminService/CreateSite" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","name":"example-site","display_name":"Example site","clients":["web"]}'
```

```json
{
  "site": {
    "id": "sit_01a0b5bb20427b289c8fe36bfdd6afae",
    "namespace": "default",
    "name": "example-site",
    "display_name": "Example site",
    "description": "",
    "clients": ["web"],
    "paused": false,
    "paused_reason": "",
    "paused_at": null,
    "endpoint_group_count": 1,
    "identity_count": 0,
    "created_at": "2026-09-18T18:15:34.722764Z",
    "updated_at": "2026-09-18T18:15:34.722764Z"
  }
}
```

`endpoint_group_count: 1` 就是站点创建时自带的 `_default` 组。

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.SiteAdminService/CreateEndpointGroup" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","site":"example-site","client":"web","name":"search",
       "rules":[{"kind":"prefix","pattern":"/search"}]}'

curl -sS -X POST "$SPINNERET_URL/spinneret.v1.SiteAdminService/CreateEndpointGroup" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","site":"example-site","client":"web","name":"detail",
       "rules":[{"kind":"template","pattern":"/detail/{id}"}]}'
```

```json
{
  "endpoint_group": {
    "id": "eg_01a0b5bb205d7df6b44e3b610d32f878",
    "site": "example-site",
    "site_id": "sit_01a0b5bb20427b289c8fe36bfdd6afae",
    "client": "web",
    "name": "search",
    "description": "",
    "low_watermark": 0,
    "rules": [
      {"id": "uri_01a0b5bb205e7ef19655a7f90724c795", "kind": "prefix", "pattern": "/search", "position": 0}
    ],
    "available_identities": 0,
    "breaker_state": "closed",
    "created_at": "2026-09-18T18:15:34.749940Z",
    "updated_at": "2026-09-18T18:15:34.749940Z"
  }
}
```

**你不需要创建策略。** 创建命名空间时已经装好了四条内置默认策略 —— `default-rotation`、
`default-signal`、`default-action` 和 `default-breaker` —— 作为第 1 版发布，并绑定在命名空间层级。
接下来六步里发生的每一件事，都是它们决定的。这里用得上的关键数值就在默认值里：

| 默认项 | 取值 | 它在哪里体现 |
| --- | --- | --- |
| 轮换 `lease_ttl` | `2m` | 不续约时租约能活多久 |
| 轮换 `max_concurrent_leases` | `1` | 同一时刻一个身份只有一个租约 —— 独占 |
| 轮换 `reuse_interval` | `0s` | 同一个身份两次被使用之间没有最小间隔 —— 刚归还的身份立刻又是候选 |
| 轮换 `proxy.mode` | `none` | 不分配代理，所以读完这一页你完全不需要代理池 |
| 信号规则 `rate-limited` | `http_status: [429]` → 判定结果 `rate_limited` | 步骤 9 |
| 动作规则 `rate-limited-cooldown` | 冷却，范围 `identity_endpoint`，基准 `60s`，倍数 `2`，上限 `30m` | 步骤 9 |
| 健康分 | 基线 `70`、`alpha 0.1`、success 观测值 `100`、`rate_limited` 观测值 `30` | 健康分会变动 |
| 熔断 | 窗口 `60s`、`min_requests: 50`、`risk_ratio ≥ 0.4` 或 `success_ratio ≤ 0.2` 时打开、`open_duration: 2m` | 步骤 11 |

怎么读、怎么改：[策略](./08-policies.md)。

---

## 步骤 6 —— 创建身份类型并导入两个身份

**身份**是调度器租借出去的一份凭据。**身份类型**是一族身份的结构定义：载荷有哪些字段、
哪些字段是敏感的、哪些字段用来标识这个身份（于是同一份凭据导入两次还是同一个身份）、
新身份如何激活，以及载荷如何渲染成节点收到的**凭据**。

### 在控制台里

**身份类型** → **新建类型**。选站点 `example-site`，写下面这段 YAML（**插入示例**菜单里有
一个 cookie 类型和一个设备类型可以作为起点），切到**交付预览**标签看一眼节点到手会是什么样，
然后**创建**。

```yaml
name: web_cookie
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
unique_by: [cookies.sessionid]
activation: probe
deliver:
  cookies: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
```

其中四行值得停一下：

- `cookie_map` 接受字符串对象、`Cookie` 请求头字符串，**或者** `{name, value}` 对象数组，
  这就是浏览器导出的 cookie 可以直接用的原因。
- `sensitive: true` 让这个值在任何展示的地方都被打码，只保留最后四个字符（`••••b2c3`）。
  它只影响展示 —— 无论如何整个载荷都是加密存储的。
- `unique_by: [cookies.sessionid]` 是去重键。Spinneret 存的是规范化取值列表的 HMAC-SHA256，
  密钥是一个服务端侧的 pepper；去重键的明文从不落库。sessionid 相同的两行就是同一个身份，
  第二次导入会更新它，而不是插入一份重复。
- `activation: probe`（默认值）意味着新身份从 `pending` 状态开始，只有当某个节点在它的租约上
  上报了一次**成功**，它才会变成 `active`。pending 身份依然会被租借，只是权重更低、
  同时最多两个，并且它们的租约会带上 `probe: true` 标记。如果凭据已经在别处验证过了，
  可以用 `immediate`。

模板只支持一种写法：`{{ path }}` —— 没有管道、没有函数调用、没有条件判断。
这既排除了模板注入，也让渲染足够便宜，能放进 `Acquire` 的热路径。

走 API 就是一次调用：

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.IdentityAdminService/CreateIdentityType" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","site":"example-site",
       "spec_yaml":"name: web_cookie\nclient: web\nfields:\n  cookies: { type: cookie_map, required: true, sensitive: true }\n  user_agent: { type: string }\nunique_by: [cookies.sessionid]\nactivation: probe\ndeliver:\n  cookies: \"{{ cookies }}\"\n  headers:\n    User-Agent: \"{{ user_agent }}\"\n"}'
```

响应会回显存下来的定义（`site: example-site` 已写进 YAML）、自动生成的 JSON Schema
（draft 2020-12，`additionalProperties: false`，外部导入程序可以拿它先校验数据行），
以及 `version: 1`。

### 导入两个身份

导入接受 JSON Lines 或 CSV，一次针对一个站点、一个身份类型。两个身份就足够看到轮换发生。

**身份** → **导入**，或者粘贴这两行：

```json
{"cookies": "sessionid=a1b2c3; csrf_token=xyz", "user_agent": "example-crawler/1.0"}
{"cookies": "sessionid=d4e5f6; csrf_token=uvw", "user_agent": "example-crawler/1.0"}
```

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.IdentityAdminService/ImportIdentities" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","site":"example-site","type":"web_cookie","format":"jsonl",
       "data":"{\"cookies\": \"sessionid=a1b2c3; csrf_token=xyz\", \"user_agent\": \"example-crawler/1.0\"}\n{\"cookies\": \"sessionid=d4e5f6; csrf_token=uvw\", \"user_agent\": \"example-crawler/1.0\"}\n"}'
```

```json
{"created": 2, "updated": 0, "unchanged": 0, "failed": []}
```

把同一份数据再导一次，返回的是 `{"created": 0, "updated": 0, "unchanged": 2, "failed": []}` ——
这就是去重键在起作用，也正因为如此，一个凭据刷新服务可以简单地把整个文件重新提交一遍。

**身份**页上这两行现在都是 **pending** 状态，类型 `web_cookie`，全局评分 70 —— 健康分基线。
cookie 的值是打码的；查看明文需要 `identity:reveal` 权限，而且会被审计。

![身份列表](../images/identities.png)

完整内容 —— 每种字段类型、交付段、状态机、CSV、大批量导入、账号、冷却热力图 ——
在[身份与账号](./06-identities.md)。

---

## 步骤 7 —— 为节点创建 API 令牌

节点只需要两样东西：服务端地址和一个令牌。令牌本身携带租户和命名空间，所以节点从不发送这两者。

### 在控制台里

**令牌** → **创建令牌**。用节点名命名（`node-1`），在**作用域**（即权限范围）构建器里选**爬虫节点**预设 ——
`lease:acquire`、`report:write`、`config:read` —— 然后把前两个收窄到这个站点。
IP 白名单和限速先留空。**有效期**填 `30d`。

明文只显示**一次**，在一个需要你确认「已保存」的对话框里。服务端只保存它的 SHA-256 摘要，
没有任何接口能再取回明文。丢了就吊销这个令牌，重新创建一个。

### 在命令行里

`spnr` 在镜像里的路径是 `/usr/local/bin/spnr`，它只需要 PostgreSQL，所以请通过一次性的
`migrate` 服务运行它，而不是通过一个可能还不健康的副本：

```bash
cd deploy/compose
docker compose run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  token create --tenant default --namespace default --name node-1 \
    --scope lease:acquire:example-site \
    --scope report:write:example-site \
    --scope config:read \
    --expires 720h
```

```text
spn_EN36ISldyttiSq4ksD1xCs1ShzBOpkGevq8mpNk6AyN
```

标准输出上只有令牌本身，所以可以安全地直接赋给变量。如果是用安装脚本装的，同样的命令是
`./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate token create …`，
或者安装脚本菜单里的**管理** → 第 4 项。
`--expires` 接受 `720h`、`30d`，或者 `0`／`never` 表示永不过期；默认是 `720h`。

### 为什么是这三个权限范围

| 权限范围 | 它开启了什么 | 参数 |
| --- | --- | --- |
| `lease:acquire:example-site` | 对这一个站点的 `Acquire`、`AcquireBatch`、`Renew`、`Release` | 站点名，**精确**匹配；省略参数则覆盖该命名空间的所有站点 |
| `report:write:example-site` | 对该站点租约的 `Report` | 站点名，精确匹配。只要令牌在该命名空间内任何地方有 `report:write`，这一批就会被受理，然后逐条按租约所属站点校验，不通过的那条单独以 `scope_missing` 被拒 |
| `config:read` | `GetConfig`、`BatchGetConfig`、`WatchConfig` | 配置分组的可选 **glob**（`config:read:crawler*`）；省略表示所有分组 |

重点其实是节点**拿不到**什么。它不能创建身份（`identity:write`）、不能发布配置
（`config:publish`）、不能读保险箱（`secret:read`，这是唯一**必须**带参数的权限范围），
也碰不到任何管理操作（`admin`）。给每个节点各自一个令牌，只给它的工作真正需要的权限范围；
这样一个令牌泄露的影响，就被限制在那个节点原本被允许做的事情之内。角色、权限、IP 白名单、
限速和轮换，见[租户、用户与令牌](./11-access-control.md)。

```bash
export SPINNERET_TOKEN=spn_EN36ISldyttiSq4ksD1xCs1ShzBOpkGevq8mpNk6AyN
```

---

## 步骤 8 —— 租借一个身份

节点调用的一切，都是对 `<base-url>/spinneret.v1.<Service>/<Method>` 的一次一元 `POST`，
请求体是 JSON。没有 `/api` 前缀，路径里也没有版本号 —— 版本在包名里 —— 而且不需要任何客户端库。

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Acquire" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: node-1' \
  -d '{"site":"example-site","client":"web","uri":"/search?q=shoes","wait_ms":500}'
```

```json
{
  "lease": {
    "lease_id": "lse_01a0b5b5707d7defa2af06b7ed755f0e_6v_0c",
    "identity_id": "idt_01a0b5b4f9797382ae09590b2b49bd9c",
    "identity_type": "web_cookie",
    "endpoint_group": "search",
    "expires_at": "2026-09-18T18:11:22.045Z",
    "sticky": false,
    "probe": true
  },
  "credential": {
    "cookies": {"csrf_token": "uvw", "sessionid": "d4e5f6"},
    "cookie_header": "",
    "headers": {"User-Agent": "example-crawler/1.0"},
    "query": {},
    "json": null,
    "values": {}
  },
  "proxy": null,
  "hints": {"renew_before_ms": 30000}
}
```

逐个字段读一遍，因为这一个响应几乎就是整个产品：

- `endpoint_group: "search"` —— 服务端拿 `/search?q=shoes` 去匹配 `example-site` + `web` 的
  URI 规则。你没有告诉它用哪个端点组；你告诉它的是 URI。
- `probe: true` —— 这个身份还是 `pending`，所以这是一个探测租约。它的上报会决定一次状态跃迁，
  所以节点必须如实上报，绝不能丢掉。
- `expires_at` 是两分钟后（默认 `lease_ttl`），`renew_before_ms` 是 30000，正好是它的四分之一：
  剩余时间少于这个数就续约，或者活干完了就让它自然过期。
- `credential` 是载荷的交付渲染结果。`cookies` 和 `headers` 有值，因为类型的 `deliver`
  要了这两段；`cookie_header`、`query`、`json` 和 `values` 是空的，因为它没要。
  节点把这些合并进自己的 HTTP 客户端，从不自己解析身份。
- `proxy: null` —— 默认轮换策略不分配代理。如果有代理池，且 `proxy.mode` 是 `pool` 或
  `bind_identity`，这里会是一个**含凭据**的完整 URL，所以它绝不能被写进日志。

现在值得知道的三个可选请求字段：`endpoint_group` 直接指定端点组，优先级高于 `uri`；
`session_key` 在轮换策略开启会话粘性时，让同一个 key 复用同一个身份；`wait_ms`（0–5000）
是服务端在失败之前最多等多久，等一个身份空出来。

### 失败长什么样

默认轮换策略里 `max_concurrent_leases` 是 `1`，所以只有两个身份时，第三次并发 Acquire
就无可给出。把上面那条命令再跑两次，中间什么都不上报 —— 这次加上 `-i`，因为关键信息在响应头里：

```bash
curl -i -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Acquire" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: node-1' \
  -d '{"site":"example-site","client":"web","uri":"/search?q=shoes","wait_ms":500}'
```

```text
HTTP/1.1 429 Too Many Requests
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 20894
```

```json
{"code":"resource_exhausted","message":"no identity available for example-site/web/search"}
```

两个响应头承载了机器可读的部分：`Spinneret-Reason` 是你用来分支的稳定原因字符串，
`Spinneret-Retry-After-Ms` 是该等多久（会被夹在 50 ms – 60 s 之间）。这里是大约 21 秒，
也就是第一个身份的租约到期的时刻。节点必须处理的那些原因 —— `no_identity_available`、
`no_proxy_available`、`circuit_open`、`site_paused`、`overloaded`、`scope_missing`、
`rate_limited` 以及各种令牌错误 —— 每一个在
[节点 API 参考 → 错误模型](./13-node-api.md#错误模型)里都有一行，包括重试是否安全。

**Acquire 不是幂等的**：重试一次 Acquire 就会再发一个租约。只有在失败可以证明发生在请求发出之前、
或者服务端自己回了 `unavailable` 时才重试它。

---

## 步骤 9 —— 先上报一次成功，再上报一次失败

上报是热路径的另一半，规则只有一句话：**节点上报事实，不上报结论。**
节点说它观测到了什么 —— 状态码、延迟、大小、传输层错误、页面标记。服务端的信号策略把这些判定成
一个判定结果，动作策略再决定拿它怎么办。正是这个分工，让你可以改变「429 该被怎么对待」，
而不用重新部署任何一个节点。

### 一次成功

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ReportService/Report" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: node-1' \
  -d '{"reports":[{
        "report_id": "3f1a8c02-5b6d-4e11-9a77-0c2d4e6f8a10",
        "lease_id": "lse_01a0b5b5707d7defa2af06b7ed755f0e_6v_0c",
        "uri": "/search",
        "method": "GET",
        "http_status": 200,
        "latency_ms": 412,
        "response_bytes": 48213,
        "started_at": "2026-09-18T18:09:22.100Z",
        "finished_at": "2026-09-18T18:09:22.512Z",
        "release": true
      }]}'
```

```json
{"accepted": 1, "duplicated": 0, "rejected": []}
```

`report_id` 是你的幂等键 —— 用 UUID 是最自然的选择。重复提交同一批会返回
`{"accepted": 0, "duplicated": 1, "rejected": []}`，而不是重复计数，这就是整批重试安全的原因。
`started_at` 和 `finished_at` 是**必填**的。`release: true` 在同一次调用里归还租约，
一个往返就够了，不用两个。一次调用可以带 1 到 500 条上报，逐条校验：
不合法的那条会带原因出现在 `rejected` 里，这一批其余的照常受理。

现在去控制台看这个身份 —— **身份**页，点进那一行。或者问 API：

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.IdentityAdminService/GetIdentity" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"id":"idt_01a0b5b4f9797382ae09590b2b49bd9c"}'
```

响应里还有 `hot_state` 和 `recent_events`；下面只摘了发生变化的那两个字段：

```json
{
  "identity": {
    "id": "idt_01a0b5b4f9797382ae09590b2b49bd9c",
    "site": "example-site",
    "client": "web",
    "type": "web_cookie",
    "state": "active",
    "state_reason": "lifecycle.activate",
    "state_changed_at": "2026-09-18T18:09:29.917769Z",
    "activated_at": "2026-09-18T18:09:29.917769Z",
    "global_score": 72.99756793080434,
    "global_samples": 1,
    "active_leases": 0
  },
  "payload": {
    "cookies": {"csrf_token": "••••", "sessionid": "••••e5f6"},
    "user_agent": "example-crawler/1.0"
  },
  "revealed": false
}
```

有两处变了，而且都不是你决定的：状态从 `pending` 变成 `active`，原因是 `lifecycle.activate`
（探测成功了，于是这份凭据以全权重加入池子）；健康分从基线 70 按 `alpha` = 0.1 向 success
的观测值 100 靠近了一步 —— 约 73。载荷回来是打码的，因为你没有要求查看明文。

### 一次失败

再 Acquire 一次。默认策略是 `weighted_random`，而 `reuse_interval` 是 `0s`，所以拿到的**可能是
任意一个**身份 —— 包括你刚刚激活的那个，它现在评分更高，权重也就更大。从响应里读出
`identity_id` 和 `lease_id`，然后在那个租约上上报一个 `429`：

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ReportService/Report" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: node-1' \
  -d '{"reports":[{
        "report_id": "7c4e91b6-2d38-4a55-8f0b-1e9c3d5a7b20",
        "lease_id": "lse_01a0b5b5fbdf7e56a5940987aad4c882_6v_0b",
        "uri": "/search",
        "method": "GET",
        "http_status": 429,
        "latency_ms": 120,
        "response_bytes": 512,
        "started_at": "2026-09-18T18:10:00.000Z",
        "finished_at": "2026-09-18T18:10:00.120Z",
        "release": true
      }]}'
```

```json
{"accepted": 1, "duplicated": 0, "rejected": []}
```

响应一模一样 —— 受理一条上报，并不说明它被判定成了什么。判定结果在一秒后体现在身份上。
在控制台里，这个身份那一行现在有了一个倒计时；详情页会显示到底发生了什么，`GetIdentity`
也一样 —— 这次只摘 `hot_state` 和 `recent_events` 里最新的那一条，对象是那个仍然
`pending` 的身份：

```json
{
  "hot_state": {
    "state": "pending",
    "global_score": 66.00138309415952,
    "global_samples": 1,
    "groups": [
      {"endpoint_group": "search", "score": 66.00138309415952, "samples": 1,
       "consecutive_failures": 1, "cooldown_until": "2026-09-18T18:11:01.472Z",
       "available_at": "2026-09-18T18:11:01.472Z", "in_ready_queue": true},
      {"endpoint_group": "detail", "score": 70, "samples": 0,
       "consecutive_failures": 0, "cooldown_until": null, "available_at": null},
      {"endpoint_group": "_default", "score": 70, "samples": 0,
       "consecutive_failures": 0, "cooldown_until": null, "available_at": null}
    ]
  },
  "recent_events": [
    {
      "created_at": "2026-09-18T18:09:57.784856Z",
      "site": "example-site",
      "subject_kind": "identity",
      "endpoint_group": "search",
      "from_state": "pending",
      "to_state": "pending",
      "action": "cooldown",
      "scope": "identity_endpoint",
      "until": "2026-09-18T18:11:01.472Z",
      "permanent": false,
      "outcome": "rate_limited",
      "policy_id": "pol_01a0afa70af67cdca274e9e5f564ea3c",
      "policy_version": 1,
      "rule": "rate-limited-cooldown",
      "report_id": "7c4e91b6-2d38-4a55-8f0b-1e9c3d5a7b20",
      "lease_id": "lse_01a0b5b5fbdf7e56a5940987aad4c882_6v_0b",
      "actor": "system",
      "reason": "rate-limited-cooldown",
      "shadow": false
    }
  ]
}
```

这一条事件就是整条链路，被完整地写了下来：

| 字段 | 含义 |
| --- | --- |
| `outcome: rate_limited` | 信号策略里的规则 `rate-limited` 匹配了 `http_status: [429]` |
| `rule: rate-limited-cooldown`、`policy_version: 1` | 触发的动作策略规则，以及它的哪一个版本 |
| `action: cooldown`、`scope: identity_endpoint` | 冷却这个身份，**只在这一个端点组上** —— `detail` 不受影响，另一个身份也不受影响 |
| `until` | 60 秒后：规则的 `base`。连续第二次会是 120 秒（`multiplier: 2`），上限 `30m` |
| `from_state: pending`、`to_state: pending` | 探测失败**不会**激活身份；它仍然是 pending |
| `actor: system` | 没有人手动做过这件事 |
| `shadow: false` | 策略处于 `enforce` 模式。在影子模式下，事件仍会记录，但冷却不会真的生效 |

健康分也往另一个方向动了：`rate_limited` 的健康观测值是 30，所以 70 漂到了约 66。
轮换策略给候选身份加权用的正是这个按端点组的评分 —— 一份在某个端点组上反复失败的凭据，
就这样悄悄地不再被选中去跑那个端点组。

如果这次 Acquire 拿回来的是你刚刚激活的那个身份，冷却、规则和动作完全一样，只有两个状态字段
和算术不同：`from_state` 和 `to_state` 都是 `active`，健康分是从约 73 按 `alpha` = 0.1
向 30 靠近，也就是约 68.7，而不是 66。

在控制台里看着倒计时，一分钟后这个身份自己就回来了。这套逻辑你一行都没写；
你只是导入了两行数据、上报了两个事实。

---

## 步骤 10 —— 回头看它发生了什么

有两个地方记下了这件事，它们回答的是不同的问题。

### 请求明细

侧边栏的**请求明细**是每一条被处理过的上报，存在 ClickHouse 里。筛到
`site=example-site`，你会看到刚才发的那两条：

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.DashboardService/QueryRequestEvents" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","site":"example-site","page_size":5}'
```

一条请求事件有 30 个字段；下面第一条是完整的，第二条只摘了不一样的那些：

```json
{
  "events": [
    {
      "event_time": "2026-09-18T18:10:00.120Z",
      "received_at": "2026-09-18T18:09:57.784Z",
      "site": "example-site",
      "client": "web",
      "endpoint_group": "search",
      "identity_id": "idt_01a0b5b4f979735cbaf25a48ee1fe9f7",
      "identity_type": "web_cookie",
      "proxy_id": "",
      "lease_id": "lse_01a0b5b5fbdf7e56a5940987aad4c882_6v_0b",
      "report_id": "7c4e91b6-2d38-4a55-8f0b-1e9c3d5a7b20",
      "node": "node-1",
      "token_id": "tok_01a0b5b4f99e70558cbb2823bd228a4d",
      "uri": "/search",
      "method": "GET",
      "http_status": 429,
      "error_kind": "",
      "markers": [],
      "outcome": "rate_limited",
      "blame": "both",
      "rule": "rate-limited",
      "latency_ms": 120,
      "response_bytes": "512",
      "suppressed": false,
      "late": false,
      "probe": false
    },
    {
      "event_time": "2026-09-18T18:09:22.512Z",
      "site": "example-site",
      "endpoint_group": "search",
      "identity_id": "idt_01a0b5b4f9797382ae09590b2b49bd9c",
      "uri": "/search",
      "http_status": 200,
      "outcome": "success",
      "blame": "none",
      "rule": "success",
      "latency_ms": 412,
      "response_bytes": "48213"
    }
  ],
  "next_page_token": "",
  "summary": null
}
```

每一行既带着节点上报的内容，**也**带着服务端的判定：`outcome`、产生它的 `rule`，
以及 `blame`（`none`、`identity`、`proxy` 或 `both`）—— 正是后者让一个坏代理不会把
一个好身份连带封掉。`node` 和 `token_id` 是你找出那一个行为异常的节点的办法。

这里的 `probe: false` 并不跟[步骤 8](#步骤-8--租借一个身份) 里租约上的 `probe: true` 矛盾：
请求事件的 `probe` **只**标记半开熔断器的探测，而租约的 `probe` 对任何 `pending` 身份的租约
都是 true。

控制台里同一份数据的筛选条件都在 URL 里，所以任何一个视图都是一条可以贴进故障群的链接：
`/requests?site=example-site&outcomes=captcha,banned&range=6h`。

如果 `SPINNERET_CLICKHOUSE_URL` 留空了，这个页面不可用。**风险事件**是由 PostgreSQL
承载的兜底：只有非成功的上报，但永远在。

### 身份详情页

在任何地方点开一个身份，它的完整经历都在一页上：状态时间线（就是你刚读的那些事件）、
载荷版本、按端点组的调度状态（评分、样本数、连续失败次数、剩余冷却、是否在就绪队列里），
以及最近的风险事件。旁边是全部人工操作 —— 冷却、封禁、解封、隔离、解除隔离、置为过期、停用、
启用、归档、恢复、激活、重置统计 —— 每一个都会被审计。

![身份详情页](../images/identity-detail.png)

这两个页面的详细说明：[可观测性与告警](./12-observability.md) 和
[身份与账号](./06-identities.md)。

---

## 步骤 11 —— 完整的示例节点

到这里你已经手动驱动过一遍闭环了。仓库里还带了一个完整的节点 —— 用 Python SDK 写的 FastAPI
服务 —— 以及一个行为可编程的 mock 目标站点（含一个需要认证的转发代理），
所以你可以在不碰任何真实目标的情况下，看到身份被冷却、熔断器被打开。

```bash
./scripts/example-quickstart.sh        # 或者：make example
```

它是幂等的，在已经跑起来的栈上大约十五秒，并且会告诉你它做了什么：

```text
[example-quickstart] stack ready (5.3s)
[example-quickstart] seeded: {"namespace": "default", "site": "example", "site_created": false, "groups_created": 0, "identities": {"created": 0, "updated": 0, "unchanged": 20}, "proxies": {"created": 0, "updated": 0, "unchanged": 2}, "policies": {"rotation": "unchanged", "signal": "unchanged"}, "config": "unchanged", "token_reused": true, "seconds": 0.205} (0.4s)
[example-quickstart] example-crawler running on http://localhost:18000 (8.5s)
[example-quickstart] GET /healthz -> {"ok":true,"config_watcher_running":true}
[example-quickstart] GET /crawl/search?q=quickstart -> {"ok":true,"status":200,"identity_id":"idt_01a0b0a4dc4774759bba124ee0e6be8f","proxy_id":"pxy_01a0b0a4dc4b7c5984d858b85e92f447","endpoint_group":"search","markers":[],"business_code":"","data":{"ok":true,"path":"/site/search","identity":"example-17","proxy":"expx1","items":[{"id":"/site/search#1","title":"item 1"},{"id":"/site/search#2","title":"item 2"},{"id":"/site/search#3","title":"item 3"}]}}
[example-quickstart] GET /crawl/item/42 -> {"ok":true,"status":200,"identity_id":"idt_01a0b0a4dc4774268ccf781b336894c3","proxy_id":"pxy_01a0b0a4dc4b7c5984d858b85e92f447","endpoint_group":"detail","markers":[],"business_code":"","data":{"ok":true,"path":"/site/item/42","identity":"example-06","proxy":"expx1","items":[{"id":"/site/item/42#1","title":"item 1"},{"id":"/site/item/42#2","title":"item 2"},{"id":"/site/item/42#3","title":"item 3"}]}}
[example-quickstart] GET /config -> {"ok":true,"group":"crawler","key":"example.json","version":3,"format":"json","from_snapshot":false,"content":{"search_page_size":10,"item_fields":["id","title"],"greeting":"hello from Spinneret"}}
[example-quickstart] chain verified (0.2s)
[example-quickstart] done in 14.5s: console http://localhost:8080 (site example), crawler http://localhost:18000
```

它在命名空间 `default` 里创建了什么（与你手工搭的那套并存）：

| | |
| --- | --- |
| 站点 `example` | 客户端 `web`，端点组 `search`（前缀 `/site/search`）和 `detail`（模板 `/site/item/{id}`） |
| 身份类型 `example_web_cookie` | 以及 20 个身份，`activation: immediate` |
| 两个代理 | 指向 mock 目标自带的、需要认证的转发代理 |
| 两条已发布策略 | `example-rotation`（`proxy.mode: bind_identity`、`reuse_interval: 1s`）和 `example-signal` |
| 配置项 `crawler/example.json` | 节点用长轮询订阅它 |
| 一个节点令牌 | 权限范围 `lease:acquire:example`、`report:write:example`、`config:read:crawler`，写入 `deploy/compose/.env` 的 `EXAMPLE_TOKEN`；已有的有效令牌会被复用 |
| 服务 `example-crawler` | 在 <http://127.0.0.1:18000>，另有 `mocktarget` 的 `19090`（站点）和 `19091`（代理） |

你也可以自己调它：

```bash
curl 'http://127.0.0.1:18000/crawl/search?q=shoes'
curl  http://127.0.0.1:18000/crawl/item/42
curl  http://127.0.0.1:18000/config
```

每一次抓取都做你手动做过的那三件事：为即将抓取的 URI `lease` 一个身份和一个代理，
用 `httpx.AsyncClient(**lease.httpx_kwargs())` 请求目标（凭据和代理已经合并进客户端参数），
然后用 `report_response(...)` 上报它观测到的事实。用 Python 写，整个闭环就是：

```python
import httpx, spinneret

async with spinneret.AsyncClient() as client:          # 读 SPINNERET_URL / SPINNERET_TOKEN
    async with client.lease(site="example-site", client="web", uri="/search?q=shoes") as lease:
        async with httpx.AsyncClient(**lease.httpx_kwargs()) as http:
            response = await http.get("https://target.invalid/search", params={"q": "shoes"})
        lease.report_response(response)                # 只上报事实，结论由服务端下
```

### 让目标「变坏」

mock 目标的行为可以按路径前缀编程。把 `/site/search` 变成一个限流器，打一些流量进去，
然后看控制台：

```bash
curl -X PUT 127.0.0.1:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'
```

```json
[{"prefix":"/site/search","mode":"rate_limit","status":429,"probability":1,"item_count":3}]
```

```bash
for i in $(seq 1 60); do curl -s -o /dev/null 'http://127.0.0.1:18000/crawl/search?q=x'; done
```

最初几次请求依然回 `200`，观测到的失败在响应体里 —— 节点如实上报它看到的，然后继续干活：

```json
{"ok":false,"status":429,"identity_id":"idt_01a0b0a4dc4774078234a278cde1ef49","proxy_id":"pxy_…","endpoint_group":"search","markers":[],"business_code":"","data":{"ok":false,"error":"rate_limited"}}
```

身份在 `search` 上陆续带上 60 秒冷却，评分下降。接着，一旦熔断器的窗口里凑够 50 次请求，
它就会打开，此后针对这个端点组的每一次 `Acquire` 都会失败，直到它关闭。带 `-i` 再调一次节点就能看到：

```bash
curl -i 'http://127.0.0.1:18000/crawl/search?q=x'
```

```text
HTTP/1.1 503 Service Unavailable
retry-after: 117
```

```json
{"ok":false,"error":"unavailable","reason":"circuit_open"}
```

控制台的**熔断器**页会用熔断器自己的话说明原因 —— 这里只摘了做决定的那部分
（完整状态里还有 `namespace`、`site_id`、`endpoint_group_id`、`last_opened_at`、
`last_closed_at`、`site_paused` 和 `policy_id`）：

```json
{
  "site": "example",
  "client": "web",
  "endpoint_group": "search",
  "state": "open",
  "open_until": "2026-09-18T18:14:05.109Z",
  "consecutive_opens": 1,
  "manual": false,
  "reason": "risk_ratio 0.98 >= 0.40; success_ratio 0.02 <= 0.20",
  "window": {"total": 58, "success": 1, "risk": 57, "captcha_identities": 0,
             "risk_ratio": 0.9828, "success_ratio": 0.0172},
  "probe": {"samples": 0, "successes": 0, "issued": 0},
  "policy_name": "default-breaker"
}
```

两分钟后（`open_duration`）它转为半开，发出少量探测租约，如果其中足够多成功就重新关闭 ——
默认策略里是 5 个样本、成功率 80 %。把目标恢复原状，它会自己好起来：

```bash
curl -X DELETE 127.0.0.1:19090/_admin/rules
```

这里面没有任何一步是靠部署、重启或改文件做到的。`./scripts/example-quickstart.sh --reset`
会先删掉示例站点及其令牌、代理、策略和配置项，方便你从头再来一遍。

这个节点的逐接口说明、环境变量和上生产要注意的事，在
[examples/fastapi-crawler/README.zh-CN.md](../../examples/fastapi-crawler/README.zh-CN.md)；
两个 SDK 在[SDK 与示例](./14-sdks.md)。

---

## 停止与清理

```bash
cd deploy/compose
docker compose stop                 # 停容器，什么都不删
docker compose up -d --wait         # 再启动
docker compose down                 # 删容器，保留数据卷
docker compose down -v              # 连 pgdata、valkeydata、chdata 一起删。不可逆。
```

`down -v` 会删掉这套部署里每一个身份、代理、策略、配置项、密钥、用户和令牌。
密钥加密密钥仍然留在 `deploy/compose/secrets/kek.key`，所以用它做过的转储还能恢复进一个新栈 ——
这正是你在[步骤 3](#步骤-3--登录控制台并把密钥备份出去)里备份它的原因。

用安装脚本装出来的部署，上面这些都用 `./spnrctl` 做 —— 它已经带好了每次都必须正确的项目名、
覆盖文件、工作目录和环境文件：

```bash
./spnrctl stop
./spnrctl logs -f spinneret
./spnrctl down -v
```

或者 `bash install.zh.sh --manage` → 菜单第 4 项，它提供三个级别：停下来、一个字节都不删；
停下来并删除数据卷；再加上删除整个安装目录，包括 `.env` 和 `kek.key` —— 而且它会先问你要不要
把密钥打印出来，因为没有它，这套部署的任何备份都再也打不开了。任何会删数据的操作都要求你输入
`delete`，而不是 y/n。两种情况下 Docker 镜像都不会被动：这里没有任何东西会去清理一台共用的机器。

---

## 如果出错了

| 你看到的 | 去哪里看 |
| --- | --- |
| `当前 Compose 是 2.20.x，这套编排需要 2.24 或更高`（英文版脚本是 `Compose is 2.20.x; this stack needs 2.24 or newer`） | 升级 Docker。覆盖文件用了 2.24 之前不存在的 merge 标签 |
| `required variable PG_PASSWORD is missing a value` | `.env` 还不存在 —— 跑一次 `./scripts/compose-init.sh`。Compose 是从 **compose 文件所在**目录读它的，不是从你当前目录，所以写全的 `-f deploy/compose/docker-compose.yml` 在任何目录下都有效 |
| `bind: address already in use` | 8080 端口被别的东西占了。`lsof -iTCP:8080 -sTCP:LISTEN` 能指出是谁；改 `.env` 里的 `SPINNERET_PORT` |
| `vault: read kek file … permission denied` | `kek.key` 必须是 `-rw-r--r--`。不要把它「加固」成 `0600` —— 见[步骤 3](#步骤-3--登录控制台并把密钥备份出去) |
| `vault: kek "k1" must be 32 bytes, got N` | 密钥文件被截断或损坏 |
| `load dedupe_pepper system key (is the KEK the one used to initialize this database?)` | 密钥和这个数据库不匹配 —— 这是「用错的密钥做恢复」的典型特征 |
| `/readyz` 一直不变成 `ok` | 响应体会点名是哪个依赖。见[步骤 2](#步骤-2--确认它是健康的)里那张表 |
| `503 {"status":"draining"}` | 重启期间每个副本最多五秒，属正常。更久说明前面那一层没有遵循 `/readyz` |
| ClickHouse 反复重启 | 小内存机器上通常是 OOM killer。加内存，或者把 `SPINNERET_CLICKHOUSE_URL` 留空，不带分析功能运行 |
| 真实部署上出现 `429 no_identity_available` | 可用身份不够，或者轮换策略太窄。从[故障排查](./18-troubleshooting.md)开始 |
| `429 no_proxy_available` | 最常见的原因是 `SPINNERET_PROXY_CHECK_URL`：它默认是一个公网 URL，访问不到它的机器会把所有代理标记为死亡 |
| `503 circuit_open` | 该端点组的熔断器打开了。目标恢复后它会关闭；**熔断器**页会说明它为什么打开 |
| `403 scope_missing` | 令牌缺少这次调用或这个站点所需的权限范围。见[步骤 7](#步骤-7--为节点创建-api-令牌) |

[故障排查](./18-troubleshooting.md)按现象组织，并带有完整的错误原因对照表，
以及怎么读日志、怎么采集诊断包。

---

## 下一步

三页，按这个顺序：

- [核心概念](./04-concepts.md) —— 你刚做的每一件事背后的心智模型：租户、命名空间、站点、端点组、
  身份、租约、上报、信号、动作、策略、熔断。读一遍，你就能在发出请求之前预判它会发生什么。
- [控制台总览](./05-console-overview.md) —— 完整的导航地图、每个页面是干什么的、哪份文档讲它，
  以及只读用户或按站点授权的用户看到什么。
- [节点 API 参考](./13-node-api.md) —— 节点会调用的每一个 RPC，含请求与响应字段、
  错误原因对照表、重试规则和推荐的客户端循环。

等你准备真正把它跑起来：

- [安装与部署](./02-installation.md) —— 反向代理与 TLS、横向扩容、一台机器上跑多套、升级、卸载、
  不用 Docker 怎么部署。
- [安全](./19-security.md) —— 在这套服务被别人能访问到之前，该走一遍的加固清单。
- [运维手册](./16-operations.md) —— 备份与恢复演练、密钥轮换、重建热状态、数据保留、故障处置手册。
