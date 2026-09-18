# Console end-to-end suite

Playwright specs that drive the real console against a running stack. Nothing is
mocked: the browser talks to the Connect APIs of the deployment under test.

## Running

```bash
make e2e-web                       # from the repository root, against http://localhost:8080
make e2e-web ARGS='-g "sites"'     # one journey
make e2e-web ARGS='--headed'       # watch it
```

`make e2e-web` installs the Chromium build that matches `@playwright/test`, reads
the administrator credentials from `deploy/compose/.env` and runs the suite.
Running it by hand needs the same three variables:

```bash
cd web
SPINNERET_UI_URL=http://localhost:8080 \
SPINNERET_E2E_USER=admin SPINNERET_E2E_PASSWORD=… \
pnpm exec playwright test
```

`SPINNERET_UI_URL` also accepts the Vite dev server (`pnpm dev`, port 5173) while
working on a page; it proxies the API to the stack.

## Layout

| File                     | Journey                                                                                             |
| ------------------------ | --------------------------------------------------------------------------------------------------- |
| `auth.setup.ts`          | signs in once through the UI and stores the session for every spec                                  |
| `shell.spec.ts`          | sign in and out, redirect after login, language, theme, navigation, 404, profile                    |
| `sites.spec.ts`          | create a site with clients, endpoint group, URI rules, URI tester, edit, delete, pause switch       |
| `identity-types.spec.ts` | create a type from the YAML example, delivery preview, delete                                       |
| `identities.spec.ts`     | import JSONL (dry run then import), filters, detail, cooldown, ban, unban, payload reveal           |
| `proxies.spec.ts`        | import URL lines, check now, bulk disable/enable, providers, bulk delete                            |
| `policies.spec.ts`       | create a rotation policy in the form, publish, edit the YAML, compare versions, bind, rule debugger |
| `breakers.spec.ts`       | open and close a breaker, pause and resume a site                                                   |
| `config.spec.ts`         | create a JSON item, publish, second version, diff, rollback                                         |
| `secrets.spec.ts`        | create a secret, audited reveal, new version, versions and access logs                              |
| `notifications.spec.ts`  | webhook channel, test delivery, alert history, disable, delete                                      |
| `access.spec.ts`         | API token (plaintext once, revoke), operator restricted to one site, audit log                      |
| `tenants.spec.ts`        | create and delete a namespace and a tenant                                                          |
| `observability.spec.ts`  | overview, heatmap, request explorer, risk events, accounts                                          |
| `screenshots.spec.ts`    | writes `docs/images/*.png` for the README                                                           |

## Conventions

- Everything is located by role, label or accessible name. A control that cannot
  be reached that way is a defect in the component, not a reason for a CSS
  selector.
- Each spec creates its own uniquely named resources (`e2e-<kind>-<random>`) and
  deletes them in a `finally` block, so the suite can run repeatedly against a
  long-lived deployment.
- `fixtures/api.ts` is a small Connect client used only for setup and cleanup
  (and for the node API, to produce request and risk events). Journeys
  themselves always go through the UI.
- `fixtures/test.ts` fails a test on any uncaught error or `console.error`; the
  few expected browser messages (aborted requests of an unmounted page) are
  listed there.
- The suite runs serially (`workers: 1`) because the specs share one namespace.
