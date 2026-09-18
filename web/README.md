# Spinneret Console

The Spinneret web console: a React single-page application built with Vite and embedded into the Go
server binary (`web/embed.go`, `//go:embed all:dist`). It talks to the server only through the Connect
APIs (JSON) and one server-sent events stream. The authoritative specification is
[`docs/design/4_web_console.md`](../docs/design/4_web_console.md).

## Requirements

- Node.js 22.13+ and pnpm 10 (`corepack enable` picks the version from `package.json`)
- For `pnpm gen`: the [buf](https://buf.build) CLI (`go install github.com/bufbuild/buf/cmd/buf@latest`; the script adds `$HOME/go/bin` to `PATH`)
- A running Spinneret server for `pnpm dev` (default `http://localhost:8080`, override with `SPINNERET_API_URL`)

## Scripts

| Script           | What it does                                                                                     |
| ---------------- | ------------------------------------------------------------------------------------------------ |
| `pnpm dev`       | Vite dev server on :5173; proxies `/spinneret.v1.*`, `/api`, `/healthz`, `/readyz` to the server |
| `pnpm build`     | `tsc -b`, `vite build` into `dist/`, then recreates `dist/.keep` (required by `go:embed`)        |
| `pnpm preview`   | Serves `dist/` with the same proxy                                                               |
| `pnpm gen`       | Regenerates protobuf-es code from `../proto` into `src/gen` (`buf.gen.yaml`)                     |
| `pnpm typecheck` | Type-checks app, config and e2e projects                                                         |
| `pnpm lint`      | ESLint (typescript-eslint, react-hooks, react-refresh)                                           |
| `pnpm format`    | Prettier (`format:check` for CI)                                                                 |
| `pnpm test`      | Vitest unit tests (jsdom)                                                                        |
| `pnpm e2e`       | Playwright end-to-end suite against a running stack (see below)                                  |

After `pnpm build`, `go build ./cmd/spinneret-server` embeds the console. Without a build only
`dist/.keep` is embedded.

## Structure

```
web/
  embed.go                package web: Assets() fs.FS rooted at dist/
  buf.gen.yaml            protoc-gen-es (target=ts, include_imports) -> src/gen
  e2e/                    Playwright smoke specs
  scripts/                gen.mjs (buf with lock), postbuild.mjs (dist/.keep)
  src/
    main.tsx              bootstrap: i18n, QueryClient, auth bridge, providers, router
    router.tsx            code-based TanStack Router tree (all routes, lazy page components)
    index.css             Tailwind v4 + design tokens (light/dark CSS variables)
    gen/                  generated protobuf-es code (do not edit, committed)
    lib/                  framework-free helpers
      transport.ts        Connect transport + CSRF/tenant headers + unauthenticated handling
      clients.ts          one typed client per service (authClient, identityClient, ...)
      errors.ts           ConnectError -> reason / retry-after / translated message
      time.ts duration.ts format.ts sse.ts clock.ts queryKeys.ts storage.ts states.ts utils.ts
    app/                  providers, auth (AuthContext, permissions, RequireAuth, PermissionGate),
                          layout (AppShell, Sidebar, Topbar, Breadcrumbs), events (SSE), errors, 404
    components/ui/        shadcn-style primitives on Radix (button, dialog, select, table, form, ...)
    components/           shared building blocks (DataTable, PageHeader, ConfirmDialog, CodeEditor, EChart, ...)
    features/<area>/      feature code: pages/<Name>Page.tsx, components/, hooks
    i18n/                 i18next init + locales/{en,zh-CN}/{common,<area>}.json
```

## Conventions

### API calls

- Import clients from `@/lib/clients` and call them inside React Query. Pass the active namespace
  **name** (`useAuth().namespaceName`) in every namespace-scoped request; the transport adds
  `X-Spinneret-CSRF: 1` and `X-Spinneret-Tenant` automatically.
- Build query keys with `useScopedQueryKey()`: `key('identities', filters, pager.pageToken)` becomes
  `['identities', tenantId, namespace, ...]`. The first element must be a domain from
  `src/lib/queryKeys.ts`; real-time events invalidate by that prefix.
- Live pages refetch with `refetchInterval: LIVE_REFETCH_MS` (5 s, `@/app/queryClient`) and pause it
  while a dialog is open (`refetchInterval: dialogOpen ? false : LIVE_REFETCH_MS`).
- Paged lists keep the previous page visible while the next one loads with
  `placeholderData: useScopedPlaceholder()` (from `@/app/auth/AuthContext`), not `keepPreviousData`:
  it never shows another tenant's or namespace's rows after a switch. Reset the cursor stack on scope
  changes too: `useCursorPagination({ resetOn: [tenantId, namespaceName, filters] })`.
- Mutations: `useMutation` + `toast.success(...)` / `toast.error(errorMessage(err, t))`, then
  `queryClient.invalidateQueries({ queryKey: ['<domain>'] })`.
- Errors: render `<ErrorState error={query.error} onRetry={query.refetch} />`; it shows the translated
  `Spinneret-Reason`, the raw server message and a lock icon for permission errors.
- int64 proto fields are `bigint`; format them with `formatNumber` or convert with `toNumber`.
  Timestamps are `Timestamp` messages; use `toDate`, `<TimeAgo value={ts} />`, `lastTimeRange(ms)`.

### Permissions

- `useAuth().can(permission, site?)` (site ID or name). Without `site`, a per-site grant on any site
  counts; namespace-level permissions (config, secrets, tokens, users, audit, notify, proxy write)
  require a namespace-wide grant (`proxy:read` / `namespace:read` also come from site-restricted
  bindings pinned to the namespace). Platform admins pass everything.
- Tenant-level resources (users, role bindings, tenant-wide notification channels) need a binding that
  covers the whole tenant: use `useAuth().canInTenant(permission)` or the `tenantLevel` prop of
  `RequirePermission` / `PermissionGate` / `PermissionButton`.
- Page guard: wrap the page body in `<RequirePermission permission={PERMISSIONS.identityRead}>`.
- Actions: `<PermissionButton permission="identity:operate" site={siteId}>` (disabled with tooltip)
  or `<PermissionGate permission="...">` (hide).
- Destructive actions use `<ConfirmDialog destructive affectedCount={n} confirmText="archive" />`.

### i18n

- Every visible string is translated. Shell, navigation, states, errors and shared components use the
  `common` namespace. Each feature area owns `src/i18n/locales/{en,zh-CN}/<area>.json`, lazily loaded
  on `useTranslation('<area>')` (the Suspense boundary around pages covers the load).
- Keep both languages in sync; zh-CN must be real Chinese. Plurals use i18next `_one` / `_other`.
- State labels: `t('states.<state>')`; error reasons: `errors.reasons.<reason>`.

### UI

- Pages: `PageHeader` + content; handle loading (skeleton), empty (`EmptyState`), error
  (`ErrorState`) and permission denied (`RequirePermission`).
- Tables: `DataTable` with `useCursorPagination` for server pagination; monospace IDs with
  `<IdText value={id} />`; relative times with `<TimeAgo />`; row click opens details (rows with
  `onRowClick` are focusable and open with Enter; clicks on buttons, links and inputs inside a row do
  not trigger it). Pass `isLoading={query.isLoading}` (not `isPending`, which stays true for disabled
  queries) and `error={query.error}`; a failed refetch over existing rows shows a stale-data banner.
- Forms: wrap every control in `FormField` (label, description, error and `aria-*` wiring; it also
  labels a `Select` trigger).
- `CodeEditor` models are keyed by `path`: give each simultaneously mounted editor a unique `path`
  (or none).
- States: `<StateBadge kind="identity|proxy|breaker|account|site" state={s} />`.
- Monaco (`CodeEditor`, `DiffView`) and ECharts (`EChart`) are lazy-loaded; never import
  `monaco-editor` or `echarts` directly from a page.
- Use Tailwind semantic tokens (`bg-background`, `text-muted-foreground`, `border`) so both themes work.

## Adding a page

1. Create `src/features/<area>/pages/<Name>Page.tsx` with a **default export** (the router lazy-loads it).
   Route files already exist for every route of the spec; replace the placeholder content.
2. Put feature components and hooks next to it (`src/features/<area>/components`, `.../use<Thing>.ts`).
3. Add strings to `src/i18n/locales/en/<area>.json` and `src/i18n/locales/zh-CN/<area>.json` and use
   `const { t } = useTranslation('<area>')`. Keys missing in the area namespace fall back to `common`
   (`fallbackNS`), so `t('actions.save')` works too; use `t('common:actions.save')` to be explicit.
4. Search params: list routes accept any search params (`PageSearch`); read them with
   `useSearch({ from: '/_app/<path>' })` and update with
   `useNavigate({ from: '/<path>' })({ search: (prev) => ({ ...prev, site }) })`. Path params:
   `useParams({ from: '/_app/identities/$id' })`.
5. A new route (rare) goes into `src/router.tsx` (`createRoute` under `appRoute` with
   `staticData: { titleKey, groupKey }`) and, if it needs a sidebar entry, `src/app/layout/nav.ts`.
6. Add unit tests next to pure logic (`*.test.ts`) and run `pnpm typecheck && pnpm lint && pnpm test`.

## Protobuf code generation

`pnpm gen` runs `buf generate --template buf.gen.yaml` from `web/`. The template's input is the
repository buf workspace (`..`, limited to `proto/spinneret/v1`) so the protovalidate dependency in
`../buf.lock` resolves; `include_imports` also emits `src/gen/buf/validate/validate_pb.ts`
(well-known types come from `@bufbuild/protobuf/wkt`). Commit the regenerated files.

## End-to-end tests

The console has a Playwright suite (`e2e/`) that drives the real UI against a
running stack: every page and its main flows, with no mocked API. From the
repository root:

```bash
make e2e-web                       # against http://localhost:8080 (make up)
make e2e-web ARGS='-g "sites"'     # one journey
make e2e-web ARGS='--headed'       # watch it
```

It installs the matching Chromium build and takes the credentials from
`deploy/compose/.env`. `e2e/README.md` lists the journeys and the conventions
(accessible locators only, unique resources per spec, zero console errors).
`e2e/screenshots.spec.ts` refreshes the console screenshots in `docs/images/`.

While working on a page, point the suite at the dev server instead:

```bash
pnpm dev   # :5173, proxies the API to the stack
SPINNERET_UI_URL=http://localhost:5173 SPINNERET_E2E_USER=admin \
  SPINNERET_E2E_PASSWORD=… pnpm e2e
```
