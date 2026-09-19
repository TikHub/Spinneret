# Console overview

**A guided tour of the Spinneret web console: how you sign in, what the shell around every page does, where each page lives, and which document explains it in depth.**

[中文](../zh/05-console-overview.md)

---

## Contents

- [What the console is](#what-the-console-is)
- [Signing in](#signing-in)
- [The shell](#the-shell)
- [The scope switcher](#the-scope-switcher)
- [Live updates and toasts](#live-updates-and-toasts)
- [Language, theme and the account menu](#language-theme-and-the-account-menu)
- [Page introduction cards](#page-introduction-cards)
- [The navigation map](#the-navigation-map)
- [What different users see](#what-different-users-see)
- [URLs, filters and links you can share](#urls-filters-and-links-you-can-share)
- [Keyboard and accessibility](#keyboard-and-accessibility)
- [First ten minutes in the console](#first-ten-minutes-in-the-console)

---

## What the console is

The console is a single-page React application compiled into the `spinneret-server` binary. The server
serves it from the root path of its own HTTP listener — there is no separate web service, no separate
port and nothing to deploy alongside it.

| Fact | Value |
| --- | --- |
| Address | the root path of `SPINNERET_HTTP_ADDR` (default `:8080`) |
| Enabled by | `SPINNERET_UI_ENABLED` (default `true`) |
| API it calls | the same Connect API a node calls, on the same origin |
| Event stream | `GET /api/v1/events/stream?tenant=<id>&namespace=<name>` |

Set `SPINNERET_UI_ENABLED=false` on instances that should expose the API only. Unknown paths that do
not look like an asset request fall back to the application shell, so client-side routes such as
`/identities/abc123` survive a page reload.

The console has no privileged back door: every request it makes goes through the same services, the
same permission checks and the same audit log as any other client. Whatever you cannot do through the
API, you cannot do here either.

![The overview page of the console](../images/overview.png)

---

## Signing in

Open the server address in a browser. An unauthenticated visitor is redirected to `/login`, with the
page they asked for preserved as a `redirect` search parameter (`/login?redirect=/identities`); only
same-origin paths are accepted, so the parameter cannot be used to bounce you off the host.

Sign in with a username and password. There is no self-registration: the first platform administrator
is created by `spnr admin init`, and later users by someone holding `user:write` on the
[Users](#the-navigation-map) page. See [Tenants, users and tokens](./11-access-control.md).

### What a session is

| Property | Behaviour |
| --- | --- |
| Cookie | `spinneret_session`, set by the server on a successful sign-in |
| Lifetime | `SPINNERET_SESSION_TTL`, default `12h`, **sliding** — it is extended on use |
| Storage | server side, in Redis/Valkey; revoking it takes effect immediately |
| CSRF | every console request carries `X-Spinneret-CSRF: 1`; cookie-authenticated unsafe requests without it are refused |
| Active tenant | sent as `X-Spinneret-Tenant`, or as the `tenant` query parameter on the event stream |
| `Secure` flag | `SPINNERET_COOKIE_SECURE` — `auto` (default), `true` or `false` |

`auto` marks the cookie `Secure` when the request arrived over TLS, or through a reverse proxy listed
in `SPINNERET_TRUSTED_PROXIES` that sent `X-Forwarded-Proto: https`. Behind a proxy that is not on
that list, set the variable explicitly — see [Installation and deployment](./02-installation.md).

When the session expires or is revoked, the next request fails with `session_invalid`, the console
clears its cached data and sends you back to `/login` with the current page as the redirect target.

### Failed sign-ins

Sign-in attempts are throttled in two dimensions:

| Dimension | Limit | Window |
| --- | --- | --- |
| Per username | 5 failures | 15 minutes |
| Per client IP address | 20 failures | 15 minutes |

Exceeding either returns `login_throttled`, and the login form shows how long to wait. A successful
sign-in clears that username's counter and returns its slot in the per-IP counter, so other failures
recorded against the same address stay counted.

### Signing out

The account menu in the top-right corner has **Sign out**. It ends the session on the server and drops
everything the console had cached. Changing your password on the **Profile** page signs out all your
*other* sessions and keeps the current one.

---

## The shell

Every authenticated page is rendered inside the same shell: a sidebar on the left, a top bar above,
and the page itself in the middle.

### Sidebar

The sidebar holds the navigation, in six groups:

| Group | Pages |
| --- | --- |
| Overview | Overview (the group heading itself is not drawn) |
| Scheduling | Identities, Identity Types, Accounts, Proxies, Sites, Policies, Heatmap, Breakers |
| Configuration | Config Center, Secrets |
| Observability | Requests, Risk Events, Notifications |
| Access | Tokens, Users, Audit |
| Platform | Tenants |

A seventh group, **Settings**, exists only as a breadcrumb: its two pages, Profile and System, are
reached from the account menu.

Notes on its behaviour:

- **Entries you cannot read are not shown.** Each entry has a read permission; if the active scope
  does not grant it, the entry disappears, and a group with no remaining entries disappears with it.
- **Collapsing.** The button at the bottom collapses the sidebar to icons only. The choice is stored
  in this browser under the `spinneret.sidebar.collapsed` key and survives a reload. While collapsed,
  hovering or focusing an icon shows the page name as a tooltip.
- **About.** The button at the foot of the sidebar opens a dialog with the copyright, the licence, the
  maintainer and a link to the source. Nothing operational lives there — the build number and the
  update check are on **Settings → System**, which is a page you can link a colleague at.
- **The logo** at the top links to the overview page.

### Top bar

From left to right: breadcrumbs, the tenant switcher, `/`, the namespace switcher, the live-update
indicator, the language menu, the theme menu and the account menu.

Breadcrumbs are built from the active route: the navigation group, then the parent page for detail
routes, then the current page. On an identity detail page they read
`Scheduling › Identities › Identity <id>`, and the middle crumb is a link back to the list.

The browser tab title follows the same source and reads `<page> · Spinneret`. Detail views replace the
page part with the identifier or the name of the thing you are looking at — the identity ID on an
identity detail page, the policy name when a policy is open.

---

## The scope switcher

The pair of dropdowns in the top bar — **Tenant** and **Namespace** — is the most important control in
the console. Nearly everything you see is scoped to them.

| Switching | Changes |
| --- | --- |
| Tenant | the `X-Spinneret-Tenant` header on every request, the namespace list, your effective permissions, and the whole visible data set |
| Namespace | the namespace sent with every namespace-scoped request: identities, proxies, sites, policies, breakers, config, secrets, tokens, the dashboards and the event stream |

Both are remembered in this browser (`spinneret.tenant`, `spinneret.namespace`). On sign-in the
console restores your last choice; if that tenant is no longer accessible it falls back to the first
tenant you can reach, and for the namespace to `default`, or else to the first namespace of that
tenant.

Three consequences worth knowing:

- **Cached data is scoped too.** Every query is keyed by tenant and namespace, so a switch never shows
  you the previous scope's rows while the new ones load.
- **The live stream resubscribes.** The event stream is opened per tenant and namespace, so a switch
  reconnects it.
- **If an editor has unsaved changes**, the switch is not silent: a dialog asks whether to discard
  them, with **Keep editing** and **Discard changes**.

Two pages work without any tenant access: **Tenants** and **Profile**. A user with no role bindings at
all sees *No tenant access* instead of a page, with the advice to ask a tenant owner or a platform
administrator for access; a platform administrator sees an invitation to create the first tenant.

**Note.** The scope is *not* part of the URL. A link you copy carries the page and its filters, but
the person you send it to opens it in their own tenant and namespace. Say which namespace you meant.

---

## Live updates and toasts

The small coloured dot in the top bar is the state of the server-sent event stream for the active
scope.

| Dot | Meaning |
| --- | --- |
| Green | Live updates connected |
| Blue, pulsing | Connecting live updates |
| Amber, pulsing | Live updates reconnecting |
| Grey | Live updates off — no tenant or namespace is selected |

The stream carries six event types. Each one refreshes the affected pages within about half a second
(bursts are coalesced into one refresh per area), and two of them also raise a toast:

| Event | Refreshes | Toast |
| --- | --- | --- |
| `breaker.transition` | breakers, dashboards, sites | yes — *Breaker open / half-open / closed*, or *Site paused / resumed* for a site switch |
| `identity.state` | identities, accounts, heatmap, dashboards | no |
| `proxy.state` | proxies, dashboards | no |
| `alert` | notifications, dashboards | yes, except breaker and identity-expiry alerts, which would duplicate the events above |
| `config.published` | config | no |
| `policy.published` | policies, breakers, sites | no |

Toasts appear in the bottom-right corner, are colour-coded by severity, carry a close button and
disappear after six seconds. They are informational: the same information is on the page they point
at. The event stream itself is described in [Observability and alerting](./12-observability.md).

---

## Language, theme and the account menu

**Language.** English and 简体中文. The initial language comes from your stored choice, then from the
browser language, then English. The choice is saved in this browser under `spinneret.lang` and also
sets the `lang` attribute of the document, so screen readers pronounce the page correctly. It is a
per-browser preference, not a server-side profile setting.

**Theme.** Light, Dark or System. *System* follows the operating system setting and changes with it
live. The choice is saved under `spinneret.theme`. The language and theme menus are also available on
the sign-in page, before you have an account context.

**Account menu.** The round button at the far right shows your initials. It opens to your display
name, your email address, a *Platform admin* badge when you are one, links to **Profile** and
**System**, and **Sign out**.

The **Profile** page (`/settings/profile`) shows the account as the server stores it — username,
display name, email, last sign-in, creation time — the tenants your role bindings reach and the roles
they give you there, the same language and theme controls, and the password change form.

The **System** page (`/settings/system`) is about the deployment rather than about you: the version of
the server you are talking to, an on-demand check against the published releases, and links to the
manual, the issue tracker, the security policy and the source. Any signed-in user can open it; it needs
no role binding, because knowing which build you are on is not privileged information. The check itself
makes no outbound request until you press the button, caches its answer for an hour, and can be turned
off with `SPINNERET_UPDATE_CHECK_URL=""` — see
[Configuration → The console](./03-configuration.md#the-console).

---

## Page introduction cards

Every page carries a collapsible card directly under its title, headed **What this page is for**. It
is not decoration: it is the shortest accurate description of the page that exists, written against
the same source as this documentation.

Each card has three parts:

1. A one-paragraph **summary** of what the page controls.
2. Three bullets on **how it is normally used**.
3. **When you need this:** a concrete situation that sends you to the page.

Cards that have obvious neighbours also list **Related** links to those pages.

Click the header to collapse a card. The collapsed state is remembered per page in this browser (the
`spinneret.intro.<page>` key), so you can dismiss the cards you have read one by one and keep the ones
you have not. Clearing the browser's site data brings them all back.

---

## The navigation map

Every route the console has. *Read permission* is what makes the sidebar entry visible and the page
open; without it the page shows *Permission denied* instead.

| Route | Page | Read permission | What it is for | Covered in |
| --- | --- | --- | --- | --- |
| `/login` | Sign in | — | Username and password sign-in | this page |
| `/` | Overview | `dashboard:read` | Live health of the namespace: identity supply, acquire and report rates, success and risk ratios, per-site cards, open breakers, low-watermark warnings, node activity | [12-observability.md](./12-observability.md) |
| `/identities` | Identities | `identity:read` | Every credential the scheduler leases, with state, health score, filters, bulk operations, import and rollback | [06-identities.md](./06-identities.md) |
| `/identities/$id` | Identity `<id>` | `identity:read` | One identity: state timeline, payload versions, per-endpoint-group scheduler state, recent risk events | [06-identities.md](./06-identities.md) |
| `/identity-types` | Identity Types | `identity:read` | The payload schema of a family of credentials and the delivery mapping nodes receive | [06-identities.md](./06-identities.md) |
| `/accounts` | Accounts | `identity:read` | Accounts grouping the identities of one site under one external reference | [06-identities.md](./06-identities.md) |
| `/proxies` | Proxies | `proxy:read` | The outbound proxy pool: health, per-site state, provider comparison, import and operations | [07-proxies.md](./07-proxies.md) |
| `/sites` | Sites | `site:read` | Sites, client types, endpoint groups and URI rules — the structure everything else is scoped to | [04-concepts.md](./04-concepts.md) |
| `/policies` | Policies | `policy:read` | Rotation, signal, action and breaker policies: drafts, versions, bindings, resolve and debug | [08-policies.md](./08-policies.md) |
| `/heatmap` | Heatmap | `dashboard:read` | Identities × endpoint groups of one site client, coloured by remaining cooldown or health score | [12-observability.md](./12-observability.md) |
| `/breakers` | Breakers | `breaker:read` | Circuit breakers per endpoint group, site pause switches, transition history | [08-policies.md](./08-policies.md) |
| `/config` | Config Center | `config:read` | Versioned configuration distributed to nodes: groups, keys, drafts, versions, secret references | [09-config-center.md](./09-config-center.md) |
| `/secrets` | Secrets | `secret:list` | The encrypted secret store: write-only values, audited reveals | [10-secrets.md](./10-secrets.md) |
| `/requests` | Requests | `dashboard:read` | Every processed report stored in ClickHouse: outcome, blame, matching rule, latency, identity, proxy, node | [12-observability.md](./12-observability.md) |
| `/risk-events` | Risk Events | `dashboard:read` | Non-success reports kept in PostgreSQL — available without ClickHouse | [12-observability.md](./12-observability.md) |
| `/notifications` | Notifications | `notify:read` | Alert channels of the tenant and the delivery history of every alert | [12-observability.md](./12-observability.md) |
| `/access/tokens` | Tokens | `token:read` | API tokens for nodes and automation: scopes, IP allowlists, rate limits, expiry | [11-access-control.md](./11-access-control.md) |
| `/access/users` | Users | `user:read` (tenant-wide) | Members of the tenant and their role bindings | [11-access-control.md](./11-access-control.md) |
| `/access/audit` | Audit | `audit:read` | Who did what in this tenant, whether it was allowed, and from which client | [11-access-control.md](./11-access-control.md) |
| `/admin/tenants` | Tenants | `tenant:manage` or `namespace:write` | Tenants and the namespaces inside the active one | [11-access-control.md](./11-access-control.md) |
| `/settings/profile` | Profile | — | Your account, your tenant access, and this browser's preferences | this page |
| `/settings/system` | System | — | The running build, the update check, and where to get help | this page |

Any other path shows the *Page not found* page with a button back to the overview.

Two entries deserve a footnote:

- **Users** is checked tenant-wide, not per namespace. A binding limited to one namespace or to
  specific sites never grants it, however high the role.
- **Tenants** appears in the sidebar for `tenant:manage` (platform administrators) or
  `namespace:write` (tenant owners), but the page itself also opens read-only for anyone with
  `namespace:read`. Reaching it by URL as a viewer therefore shows the lists without the create and
  delete actions.

---

## What different users see

The console does not have a "read-only mode" switch. What you see is derived entirely from your role
bindings in the active tenant and namespace, and the server enforces the same rules again on every
request.

### A read-only user (role `viewer`)

A namespace-wide `viewer` binding grants `namespace:read`, `site:read`, `identity:read`,
`proxy:read`, `policy:read`, `breaker:read`, `config:read`, `secret:list`, `dashboard:read`,
`audit:read` and `notify:read`. In practice that means:

- Every sidebar entry is visible **except Tokens, Users and Tenants**.
- Every page opens, including Secrets — but secret values are never shown: revealing a plaintext needs
  `secret:reveal`.
- Buttons that would change something are present but disabled, with a tooltip reading
  *Requires the `<permission>` permission*. You can see what the page can do, which is the point.

### A site-scoped user

A binding can be restricted to specific sites. Such a user sees the scheduling pages, but:

- Lists are filtered by the server to the sites the binding covers. There is no way to widen them from
  the browser.
- Namespace-level permissions are **never** satisfied by a per-site grant. Config Center, Secrets,
  Tokens, Users, Audit and Notifications stay out of reach, and so do proxy write and operate actions,
  no matter which role the site binding carries.
- `proxy:read` and `namespace:read` are the exception: a site-restricted binding pinned to the
  namespace does grant them, so the proxy pool is readable.

### A platform administrator

Passes every permission check in every tenant, and is the only kind of user that can hold
`tenant:manage` and `kek:manage`. The account menu shows a *Platform admin* badge. A platform
administrator with no role bindings still reaches **Tenants** and **Profile**.

### Nobody at all

A signed-in user with no role bindings sees *No tenant access* on every page except Profile, and is
told to ask a tenant owner or a platform administrator for access.

The full permission list, the four roles and the extra permissions are in
[Tenants, users and tokens](./11-access-control.md).

---

## URLs, filters and links you can share

Page state lives in the URL. Filters, the selected tab, the time range and the open row are search
parameters, updated in place so the browser's back button steps through your navigation rather than
through every filter keystroke:

```text
/identities?site=example-site&states=banned&order_by=score
/breakers?site=example-site&tab=history
/requests?site=example-site&outcomes=captcha,banned&range=6h
```

Time ranges on the request and risk-event explorers are `range=15m|1h|6h|24h|7d`, or
`range=custom&from=…&to=…` with ISO 8601 or epoch-millisecond bounds. `outcomes` is a comma-separated
list of the twelve classified outcomes — `success`, `empty`, `rate_limited`, `captcha`,
`auth_invalid`, `forbidden`, `banned`, `proxy_error`, `network_error`, `target_error`,
`client_error`, `unknown` — and any other token is silently dropped.

That makes any view you are looking at a link you can paste into an incident channel. Two caveats:

- The **tenant and namespace are not in the URL** (see [The scope switcher](#the-scope-switcher)).
- The recipient needs the same read permission, or they get *Permission denied*.

Hovering a sidebar entry starts loading that page in the background, so the click itself is instant,
and the scroll position of a list is restored when you come back to it.

---

## Keyboard and accessibility

The console is built on accessible primitives and is usable without a mouse.

| Interaction | Keys |
| --- | --- |
| Move between controls | `Tab` / `Shift+Tab` |
| Open a dropdown (tenant, namespace, language, theme, account, filters) | `Enter` or `Space` |
| Move inside an open menu | `↑` / `↓`, `Home` / `End`, or type to jump |
| Choose a menu item | `Enter` |
| Close a menu or dialog | `Esc` |
| Open the row a table row points at | `Enter` or `Space` on the focused row |
| Collapse or expand a page introduction card | `Enter` or `Space` on its header |
| Add or remove a tag in a tag input | `Enter` or `,` adds; `Backspace` on an empty field removes the last one |

Further details:

- Every focusable element draws a visible focus ring; focus is trapped inside dialogs and returns to
  the trigger when they close.
- Icon-only buttons all carry an accessible name — *Collapse sidebar*, *Language*, *Theme*, *Account
  menu*, *Refresh*, *Previous page* and so on — and the collapsed sidebar exposes each page name both
  as a tooltip and as the link's label.
- The live-update dot is a status element: its colour is never the only carrier of meaning, the state
  is also its accessible label, and it is reachable with `Tab`.
- The sidebar is labelled *Main navigation* and the breadcrumb trail *Breadcrumbs*; the current page's
  crumb is marked as the current page.
- Table headers expose their sort direction to screen readers (`aria-sort`), the number of selected
  rows is shown in the bulk-action bar, and a disabled action explains itself through a tooltip that
  focus alone reveals — you do not need to hover.
- The sign-in error is announced as an alert the moment it appears.
- Both themes are designed for readable contrast, and the page declares its colour scheme so that
  browser-rendered controls (scrollbars, form widgets) match.

There are no global single-key shortcuts, so typing in a filter field never triggers an action
somewhere else on the page.

---

## First ten minutes in the console

A path through the console that leaves you able to read what your fleet is doing.

1. **Sign in** and check the top-right corner: the tenant and namespace you are about to look at.
   Switch if this is not the one.
2. **Overview.** Read the six numbers at the top: available identities, acquire QPS, report QPS,
   success ratio, risk ratio, open breakers. Change the window (`1m`, `5m`, `15m`, `1h`) to see
   whether a number is a spike or a trend; the page refreshes itself every ten seconds.
3. **Scroll down the overview** to the per-site cards. Each one breaks its identities down by state,
   and flags the endpoint groups that are below their low watermark or have an open breaker. This is
   where you learn *which* site is unhappy.
4. **Sites.** Look at how your sites are cut into client types and endpoint groups. Every cooldown,
   every breaker and every policy binding is scoped to that structure, so it is worth five minutes.
   See [Concepts](./04-concepts.md).
5. **Identities.** Filter by one site and sort by health score. Open one identity and read its state
   timeline: you now know what "the scheduler cooled this down" looks like in practice.
6. **Heatmap.** Pick that same site and client. The grid is identities × endpoint groups, coloured by
   remaining cooldown. A vertical stripe is a sick endpoint group; scattered dots are ordinary
   rotation.
7. **Breakers.** See which endpoint groups are open, and note that you can open one yourself, or pause
   a whole site, when you need to stop the bleeding before automation reacts.
8. **Requests** or **Risk Events.** Filter to the last hour of non-success reports for that site. This
   is the evidence behind everything the other pages showed you.
9. **Policies.** Open the policy bound to that site and read the rotation and signal rules that
   produced those cooldowns. Do not change anything yet — see [Policies](./08-policies.md) first.
10. **Audit.** Confirm that your own session shows up. Everything you do here is recorded, including
    the reveal attempts that were refused.

When you are ready to connect a node, go to **Tokens**, mint one with only the scopes that node needs,
and follow the [Node API reference](./13-node-api.md).

---

## Next

- [Concepts](./04-concepts.md) — the model behind every page you just walked through.
- [Tenants, users and tokens](./11-access-control.md) — roles, permissions and why a page is hidden.
- [Observability and alerting](./12-observability.md) — the dashboards, the request explorer and the event stream in depth.
- [Quick start](./01-quickstart.md) — if you do not have a console to sign in to yet.
