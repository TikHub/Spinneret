# 文档

部署、使用、运维和扩展 **Spinneret** 所需的全部内容 —— 为共用同一批稀缺、限速凭据与出口的节点集群提供控制平面。

**English documentation: [README.md](./README.md)** —— 这里的每一页都有中英两个版本，内容一致。

<div align="center">
    <img src="./images/overview-zh.png" width="860" alt="Spinneret 控制台"/>
</div>

---

## 从这里开始

如果你从未跑过这套系统，按顺序读这三页。大约一小时，你会得到一个能跑、而且你真正理解的部署。

| | 页面 | 你会得到 |
|---|---|---|
| 1 | [快速开始](./zh/01-quickstart.md) | 单机跑起整套服务、一个管理员账号，以及一个节点完成「租借身份 → 请求 → 上报结果」的闭环（控制台和 `curl` 两种方式） |
| 2 | [核心概念](./zh/04-concepts.md) | 心智模型：租户、站点、端点组、身份、租约、上报、策略。读完就能在发请求之前预判它会发生什么 |
| 3 | [控制台总览](./zh/05-console-overview.md) | 怎么在控制台里找东西，以及判断部署是否健康的那四个页面 |

最快的路径是引导式安装脚本：问几个问题，剩下的它来做。

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.zh.sh -o install.zh.sh
less install.zh.sh       # 先读一遍，你即将运行它
bash install.zh.sh
```

英文版是 `install/install.sh`。详见 [install/README.md](../install/README.md)。

---

## 按你要做的事来找

**部署与配置**

- [安装与部署](./zh/02-installation.md) —— 安装脚本、Compose 各个服务、profile、端口与数据卷、
  反向代理与 TLS、横向扩容、升级、卸载，以及不用 Docker 怎么跑
- [配置参考](./zh/03-configuration.md) —— 每一个 `SPINNERET_*` 变量的默认值、取值范围和该在什么
  情况下改它
- [安全](./zh/19-security.md) —— 威胁模型、软件保护了什么、你要负责什么，以及哪些操作会发放凭据

**使用控制台**

- [控制台总览](./zh/05-console-overview.md) —— 界面框架、作用域切换器，以及每个页面对应哪篇文档
- [身份与账号](./zh/06-identities.md) —— 身份类型、载荷、导入、状态机、健康分、手工操作和冷却热力图
- [代理池](./zh/07-proxies.md) —— 代理池、分配模式、健康检查、与身份的绑定
- [策略](./zh/08-policies.md) —— 轮换、信号、动作、熔断四类策略的 YAML、发布流程、绑定优先级和
  规则调试器
- [配置中心](./zh/09-config-center.md) —— 给节点下发的带版本配置、发布与回滚、订阅协议、
  `${secret:...}` 引用
- [密钥保管库](./zh/10-secrets.md) —— 信封加密、KEK、节点如何读取密钥、在控制台查看明文
- [租户、用户与令牌](./zh/11-access-control.md) —— 隔离模型、角色与权限、API 令牌与权限范围、
  会话、审计日志

**基于它开发**

- [节点 API 参考](./zh/13-node-api.md) —— 节点会调用的每一个 RPC，含请求/响应 JSON、错误原因表和
  重试规则
- [SDK 与示例](./zh/14-sdks.md) —— Python SDK、Go SDK、示例节点，以及没有 SDK 时怎么写客户端
- [命令行工具](./zh/15-cli.md) —— 每一个 `spnr` 命令和 `spinneret-server` 的每个参数

**把它跑住**

- [可观测性与告警](./zh/12-observability.md) —— 仪表盘、请求明细、Prometheus 指标和一套可直接用的
  告警规则
- [运维手册](./zh/16-operations.md) —— 容量规划、备份与恢复演练、升级、扩容、KEK 轮换、数据保留、
  故障处置流程
- [性能与调优](./zh/17-performance.md) —— 实测数据、时间花在哪里、过了拐点会怎样，以及调优手段的
  优先顺序
- [故障排查](./zh/18-troubleshooting.md) —— 现象、原因、处理方法，以及完整的错误原因表

**修改代码**

- [参与贡献](./zh/20-contributing.md) —— 开发环境、仓库结构、各层测试、改动必须通过的质量门禁

---

## 完整目录

| | 页面 | |
|---|---|---|
| 01 | [快速开始](./zh/01-quickstart.md) | 从零到第一次租借身份并上报结果 |
| 02 | [安装与部署](./zh/02-installation.md) | 安装脚本、Compose、profile、TLS、扩容、升级 |
| 03 | [配置参考](./zh/03-configuration.md) | 全部 `SPINNERET_*` 变量 |
| 04 | [核心概念](./zh/04-concepts.md) | 端到端的心智模型 |
| 05 | [控制台总览](./zh/05-console-overview.md) | 界面框架与导航地图 |
| 06 | [身份与账号](./zh/06-identities.md) | 类型、载荷、导入、状态、操作 |
| 07 | [代理池](./zh/07-proxies.md) | 代理池、分配、健康、绑定 |
| 08 | [策略](./zh/08-policies.md) | 轮换、信号、动作、熔断 |
| 09 | [配置中心](./zh/09-config-center.md) | 给节点的带版本配置 |
| 10 | [密钥保管库](./zh/10-secrets.md) | 信封加密与 KEK |
| 11 | [租户、用户与令牌](./zh/11-access-control.md) | 隔离、角色、权限、令牌 |
| 12 | [可观测性与告警](./zh/12-observability.md) | 仪表盘、指标、通知 |
| 13 | [节点 API 参考](./zh/13-node-api.md) | 节点会调用的每个 RPC |
| 14 | [SDK 与示例](./zh/14-sdks.md) | Python、Go、示例节点 |
| 15 | [命令行工具](./zh/15-cli.md) | `spnr` 与 `spinneret-server` |
| 16 | [运维手册](./zh/16-operations.md) | 日常运维：备份、升级、扩容、故障处置 |
| 17 | [性能与调优](./zh/17-performance.md) | 实测数据与调优手段 |
| 18 | [故障排查](./zh/18-troubleshooting.md) | 现象 → 原因 → 处理，错误原因表 |
| 19 | [安全](./zh/19-security.md) | 威胁模型与加固 |
| 20 | [参与贡献](./zh/20-contributing.md) | 开发流程与质量门禁 |
| 21 | [常见问题与术语表](./zh/21-faq.md) | 大家最常问的问题，以及全部术语释义 |

---

## 仓库里的其他文档

| | |
|---|---|
| [README.zh-CN.md](../README.zh-CN.md) · [EN](../README.md) | 五分钟了解 Spinneret 是什么 |
| [install/README.md](../install/README.md) | 一键部署脚本 |
| [proto/README.md](../proto/README.md) | 传输约定与服务列表 |
| [sdk/python/README.zh-CN.md](../sdk/python/README.zh-CN.md) · [EN](../sdk/python/README.md) | Python SDK |
| [sdk/go/README.zh-CN.md](../sdk/go/README.zh-CN.md) · [EN](../sdk/go/README.md) | Go SDK |
| [examples/fastapi-crawler/README.zh-CN.md](../examples/fastapi-crawler/README.zh-CN.md) · [EN](../examples/fastapi-crawler/README.md) | 示例节点逐接口说明 |
| [web/README.md](../web/README.md) | 控制台开发 |
| [test/load/README.md](../test/load/README.md) | k6 压测场景与性能目标 |
| [CONTRIBUTING.md](../CONTRIBUTING.md) | 如何参与贡献 |
| [SECURITY.md](../SECURITY.md) | 如何报告安全漏洞 |
| [CHANGELOG.md](../CHANGELOG.md) | 版本变更记录 |

---

Spinneret 由 [TikHub](https://github.com/TikHub) 维护并开源，采用
[Apache License 2.0](../LICENSE) 许可证。
