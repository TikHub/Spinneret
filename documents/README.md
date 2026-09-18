# Documentation

Everything you need to deploy, use, operate and extend **Spinneret** — a control plane for fleets
that share scarce, rate-limited credentials and egress.

**中文文档：[README.zh-CN.md](./README.zh-CN.md)** — every page here exists in both languages and
they say the same thing.

<div align="center">
    <img src="./images/overview.png" width="860" alt="The Spinneret console"/>
</div>

---

## Start here

If you have never run this before, read these three in order. They take about an hour and leave
you with a working deployment you actually understand.

| | Page | What you get |
|---|---|---|
| 1 | [Quick start](./en/01-quickstart.md) | The stack running on one host, an administrator account, and one node leasing an identity and reporting the result — from the console and from `curl` |
| 2 | [Concepts](./en/04-concepts.md) | The mental model: tenants, sites, endpoint groups, identities, leases, reports, policies. Enough to predict what a request will do before you send it |
| 3 | [Console overview](./en/05-console-overview.md) | Finding your way around, and the four pages that tell you whether the deployment is healthy |

The fastest path of all is the guided installer, which asks a handful of questions and does the
rest:

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh -o install.sh
less install.sh          # read it first; you are about to run it
bash install.sh
```

Chinese version: `install/install.zh.sh`. See [install/README.md](../install/README.md).

---

## By what you are trying to do

**Deploy and configure it**

- [Installation and deployment](./en/02-installation.md) — the installer, the Compose stack service
  by service, profiles, ports and volumes, reverse proxies and TLS, scaling out, upgrading,
  uninstalling, and running without Docker
- [Configuration reference](./en/03-configuration.md) — every `SPINNERET_*` variable with its
  default, its valid range and when you would change it
- [Security](./en/19-security.md) — the threat model, what the software protects, what you are
  responsible for, and which operations hand out a credential

**Use the console**

- [Console overview](./en/05-console-overview.md) — the shell, the scope switcher, and which
  document covers which page
- [Identities and accounts](./en/06-identities.md) — identity types, payloads, imports, the state
  machine, health scores, manual operations and the cooldown heatmap
- [Proxies](./en/07-proxies.md) — the pool, assignment modes, health checks, identity binding
- [Policies](./en/08-policies.md) — rotation, signal, action and breaker policies, their YAML,
  publishing, the binding hierarchy and the rule debugger
- [Configuration center](./en/09-config-center.md) — versioned config for nodes, publish and
  rollback, the watch protocol, `${secret:...}` references
- [Secret vault](./en/10-secrets.md) — envelope encryption, the KEK, reading a secret from a node,
  revealing one in the console
- [Tenants, users and tokens](./en/11-access-control.md) — the isolation model, roles and
  permissions, API tokens and scopes, sessions, the audit log

**Build against it**

- [Node API reference](./en/13-node-api.md) — every RPC a node calls, with request and response
  JSON, the error-reason table and the retry rules
- [SDKs and examples](./en/14-sdks.md) — the Python SDK, the Go SDK, the example node, and writing
  a client with no SDK
- [CLI reference](./en/15-cli.md) — every `spnr` command and every `spinneret-server` flag

**Keep it running**

- [Observability and alerting](./en/12-observability.md) — the dashboards, the request explorer,
  the Prometheus metrics and a starter set of alert rules
- [Operations runbook](./en/16-operations.md) — capacity planning, backups and restore drills,
  upgrades, scaling, KEK rotation, retention, incident playbooks
- [Performance and tuning](./en/17-performance.md) — the measured numbers, where the time goes,
  what happens past the knee, and the tuning levers in the order they pay
- [Troubleshooting](./en/18-troubleshooting.md) — symptom, cause, fix, plus the complete
  error-reason table

**Change the code**

- [Contributing](./en/20-contributing.md) — development environment, repository layout, the test
  layers, the quality gates a change has to pass

---

## The full set

| | Page | |
|---|---|---|
| 01 | [Quick start](./en/01-quickstart.md) | From nothing to a first leased identity and a reported result |
| 02 | [Installation and deployment](./en/02-installation.md) | The installer, Compose, profiles, TLS, scaling, upgrades |
| 03 | [Configuration reference](./en/03-configuration.md) | Every `SPINNERET_*` variable |
| 04 | [Concepts](./en/04-concepts.md) | The mental model, end to end |
| 05 | [Console overview](./en/05-console-overview.md) | The shell and the navigation map |
| 06 | [Identities and accounts](./en/06-identities.md) | Types, payloads, imports, states, operations |
| 07 | [Proxies](./en/07-proxies.md) | The pool, assignment, health, binding |
| 08 | [Policies](./en/08-policies.md) | Rotation, signal, action, breaker |
| 09 | [Configuration center](./en/09-config-center.md) | Versioned config for nodes |
| 10 | [Secret vault](./en/10-secrets.md) | Envelope encryption and the KEK |
| 11 | [Tenants, users and tokens](./en/11-access-control.md) | Isolation, roles, permissions, tokens |
| 12 | [Observability and alerting](./en/12-observability.md) | Dashboards, metrics, notifications |
| 13 | [Node API reference](./en/13-node-api.md) | Every RPC a node calls |
| 14 | [SDKs and examples](./en/14-sdks.md) | Python, Go, the example node |
| 15 | [CLI reference](./en/15-cli.md) | `spnr` and `spinneret-server` |
| 16 | [Operations runbook](./en/16-operations.md) | Day 2: backups, upgrades, scaling, incidents |
| 17 | [Performance and tuning](./en/17-performance.md) | Measured numbers and the tuning levers |
| 18 | [Troubleshooting](./en/18-troubleshooting.md) | Symptom → cause → fix, error reasons |
| 19 | [Security](./en/19-security.md) | Threat model and hardening |
| 20 | [Contributing](./en/20-contributing.md) | Development and the quality gates |
| 21 | [FAQ and glossary](./en/21-faq.md) | The questions people ask, and every term defined |

---

## Elsewhere in the repository

| | |
|---|---|
| [README.md](../README.md) · [中文](../README.zh-CN.md) | What Spinneret is, in five minutes |
| [install/README.md](../install/README.md) | The one-command installer |
| [proto/README.md](../proto/README.md) | Wire conventions and the service list |
| [sdk/python/README.md](../sdk/python/README.md) · [中文](../sdk/python/README.zh-CN.md) | Python SDK |
| [sdk/go/README.md](../sdk/go/README.md) · [中文](../sdk/go/README.zh-CN.md) | Go SDK |
| [examples/fastapi-crawler/README.md](../examples/fastapi-crawler/README.md) · [中文](../examples/fastapi-crawler/README.zh-CN.md) | The example node, endpoint by endpoint |
| [web/README.md](../web/README.md) | Console development |
| [test/load/README.md](../test/load/README.md) | The k6 load scenarios and their targets |
| [CONTRIBUTING.md](../CONTRIBUTING.md) | How to contribute |
| [SECURITY.md](../SECURITY.md) | Reporting a vulnerability |
| [CHANGELOG.md](../CHANGELOG.md) | Release notes |

---

Spinneret is maintained and open-sourced by [TikHub](https://github.com/TikHub) under the
[Apache License 2.0](../LICENSE).
