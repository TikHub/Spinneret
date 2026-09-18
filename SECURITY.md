# Security policy

[中文](#安全策略)

## Supported versions

Spinneret is pre-1.0. Security fixes are released against the latest tagged version and the
`main` branch; older tags are not patched. Upgrade to the latest release before reporting an
issue you found on an older one.

| Version | Supported |
| --- | --- |
| latest release and `main` | yes |
| any earlier tag | no |

## Reporting a vulnerability

**Do not open a public issue for a security problem.**

Report it privately through GitHub's private vulnerability reporting on this repository:
<https://github.com/TikHub/Spinneret/security/advisories/new>. That creates a private advisory
only the maintainers can see, and it is the fastest route to a fix.

Please include:

- the version or commit you tested,
- how the deployment was configured (Compose, replicas, what sits in front of the server),
- what an attacker gains, and what access they need to start,
- the minimal steps to reproduce it,
- any log lines, requests or crash output that help.

You will get an acknowledgement within 72 hours and an assessment within 7 days. We will tell
you what we found, what we plan to do and when, and we will credit you in the advisory and the
changelog unless you prefer otherwise. Please give us 90 days before disclosing publicly, or
less if we ship a fix sooner and agree with you on a date.

## Scope

In scope: the server, the CLI, the web console, the SDKs, the container image, the Compose
deployment and the installer in this repository.

Out of scope: vulnerabilities that require an attacker to already hold a platform-administrator
account or the key-encryption key; missing hardening in a deployment that the documentation tells
you to configure yourself (TLS termination, network exposure, KEK custody); vulnerabilities in
third-party dependencies that already have a published advisory and a version bump, which we
handle as ordinary dependency updates; and anything that only affects the target sites your nodes
talk to — Spinneret does not send those requests.

## What the project protects, and what you are responsible for

Read [documents/en/19-security.md](documents/en/19-security.md) for the full threat model. In
short: the software encrypts identity payloads and secrets at rest with a key-encryption key that
**you** hold, authenticates every call, scopes every token, isolates tenants, and audits every
operation that hands out a credential. You are responsible for where the KEK lives and its
backups, for terminating TLS, for who you give which permission to, and for what your nodes do
with the identities they lease.

---

# 安全策略

## 支持的版本

Spinneret 尚未发布 1.0。安全修复只针对最新的 tag 和 `main` 分支；更早的 tag 不再打补丁。在
报告旧版本上发现的问题之前，请先升级到最新版本确认。

| 版本 | 是否支持 |
| --- | --- |
| 最新 release 与 `main` | 是 |
| 更早的 tag | 否 |

## 如何报告漏洞

**请不要用公开 issue 报告安全问题。**

请通过本仓库的 GitHub 私密漏洞报告提交：
<https://github.com/TikHub/Spinneret/security/advisories/new>。它会创建一份只有维护者可见的
私密安全公告，这是最快拿到修复的途径。

请在报告中包含：

- 你测试的版本或 commit；
- 部署方式（Compose、副本数、服务器前面挂了什么）；
- 攻击者能拿到什么，以及发起攻击需要什么前置权限；
- 最小复现步骤；
- 有帮助的日志、请求或崩溃输出。

我们会在 72 小时内确认收到，7 天内给出评估结论：我们发现了什么、打算怎么修、什么时候修。
除非你不希望署名，我们会在安全公告和 changelog 中致谢。请在公开披露前给我们 90 天；如果修复
提前发布，我们会和你商定一个更早的日期。

## 范围

范围内：本仓库中的服务端、命令行工具、Web 控制台、SDK、容器镜像、Compose 部署和安装脚本。

范围外：需要攻击者已经持有平台管理员账号或 KEK 才能利用的问题；文档明确要求你自行配置的加固项
缺失（TLS 终止、网络暴露面、KEK 保管）；已有公开公告和升级版本的第三方依赖漏洞（按普通依赖升级
处理）；以及只影响你的节点所访问的目标站点的问题——那些请求不是 Spinneret 发出的。

## 项目保护什么，你负责什么

完整威胁模型见 [documents/zh/19-security.md](documents/zh/19-security.md)。简单说：软件用**你**
持有的密钥加密密钥（KEK）对身份载荷和密钥做静态加密，对每一次调用做鉴权，对每个令牌做权限范围
限制，隔离租户，并审计每一次发放凭据的操作。你负责 KEK 存放在哪里、怎么备份，负责 TLS 终止，
负责把哪些权限给谁，也负责你的节点拿这些身份去做了什么。
