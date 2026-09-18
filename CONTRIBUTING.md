# Contributing to Spinneret

[中文](#参与贡献)

Thanks for considering it. This file is the short version; the full development guide —
environment, repository layout, code generation, every test layer and the quality gates — is
[documents/en/20-contributing.md](documents/en/20-contributing.md).

## Before you write code

- **A bug?** Open an issue with the version, what you expected, what happened, and the smallest
  way to reproduce it. A failing test is the best bug report.
- **A feature?** Open an issue describing the problem before the solution. Spinneret is
  deliberately site-agnostic: it manages the state around requests and contains no signing
  algorithms, login flows or captcha handling, and it will stay that way.
- **A security problem?** Do not open an issue. See [SECURITY.md](SECURITY.md).

## The loop

```bash
make infra-up          # PostgreSQL, Valkey and ClickHouse for the test suite
make generate          # protobuf and the root sqlc config, if you changed .proto or .sql
make test              # Go tests
make test-race         # Go tests with the race detector, which is what CI runs
make lint              # go vet + golangci-lint
make web-test          # console unit tests, typecheck, lint, format
make e2e               # the Compose end-to-end suite
```

`make help` lists everything. Run at least `make test` and `make lint` before you push; CI runs
all of it and will not merge a change that does not pass.

## House rules

1. **Comments and identifiers are English**, everywhere, without exception.
2. **No brand or platform names** in code, tests, examples, fixtures or documentation. Use neutral
   placeholders. This project is site-agnostic and stays that way.
3. **Documentation is bilingual.** A change to `documents/en/<page>` is not complete until
   `documents/zh/<page>` says the same thing. Same sections, same tables, same examples.
4. **Tests come with the change.** New behaviour needs a test that fails without it. A bug fix
   needs a regression test that reproduces the bug.
5. **Errors carry context.** Wrap with `fmt.Errorf("...: %w", err)`; never swallow an error.
6. **Keep files focused.** Extract rather than grow: most files here are under 400 lines.
7. **Do not commit generated code by hand.** Run `make generate`; the protobuf descriptors contain
   length-prefixed strings and a hand edit corrupts them.

## Commits and pull requests

Commit messages follow `<type>: <description>` with types `feat`, `fix`, `refactor`, `docs`,
`test`, `chore`, `perf`, `ci`. Write the body for someone reading `git log` in a year: what
changed and why, not what the diff already shows.

A pull request should describe the problem, the approach, and how you verified it. If it changes
behaviour a user can see, it also updates `documents/` in both languages and `CHANGELOG.md`.

## Licence

By contributing you agree that your contribution is licensed under the
[Apache License 2.0](LICENSE), the same licence as the project.

---

# 参与贡献

感谢你愿意参与。本文是简版；完整开发指南（环境、仓库结构、代码生成、各层测试和质量门禁）见
[documents/zh/20-contributing.md](documents/zh/20-contributing.md)。

## 动手写代码之前

- **发现 bug？** 提 issue，写清版本、你期望的行为、实际发生了什么，以及最小复现方式。一个能复现
  的失败测试是最好的 bug 报告。
- **想加功能？** 先提 issue 描述问题，而不是直接描述方案。Spinneret 刻意保持站点无关：它管理
  请求周围的状态，不包含任何签名算法、登录流程或验证码处理，今后也不会包含。
- **安全问题？** 不要提公开 issue，见 [SECURITY.md](SECURITY.md)。

## 开发循环

```bash
make infra-up          # 拉起测试用的 PostgreSQL、Valkey 和 ClickHouse
make generate          # 改过 .proto 或 .sql 之后重新生成 protobuf 与根目录的 sqlc 配置
make test              # Go 测试
make test-race         # 带竞态检测的 Go 测试，CI 跑的是这个
make lint              # go vet + golangci-lint
make web-test          # 控制台单测、类型检查、lint、格式化
make e2e               # Compose 端到端测试
```

`make help` 会列出全部目标。推送前至少跑 `make test` 和 `make lint`；CI 会跑全部，不通过不合并。

## 项目约定

1. **注释和标识符一律用英文**，没有例外。
2. **代码、测试、示例、fixture 和文档里不出现任何具体品牌或平台名称**，用中性占位符。本项目
   与站点无关，并将一直如此。
3. **文档是双语的。** 改了 `documents/en/<page>` 就必须让 `documents/zh/<page>` 说同样的话：
   相同的章节、相同的表格、相同的示例。
4. **改动要带测试。** 新行为需要一个没有它就会失败的测试；修 bug 需要一个能复现该 bug 的回归
   测试。
5. **错误要带上下文。** 用 `fmt.Errorf("...: %w", err)` 包装，绝不吞掉错误。
6. **文件保持聚焦。** 宁可拆分也不要膨胀：本仓库多数文件在 400 行以内。
7. **不要手改生成代码。** 跑 `make generate`；protobuf 描述符里是带长度前缀的字符串，手改会
   直接损坏它。

## 提交与 PR

提交信息格式为 `<type>: <description>`，type 取 `feat`、`fix`、`refactor`、`docs`、`test`、
`chore`、`perf`、`ci`。正文写给一年后翻 `git log` 的人看：改了什么、为什么改，而不是复述 diff。

PR 应说明问题、方案和你如何验证。如果改动了用户可见的行为，还要同步更新双语 `documents/` 和
`CHANGELOG.md`。

## 许可证

提交贡献即表示你同意你的贡献以 [Apache License 2.0](LICENSE) 授权，与本项目一致。
