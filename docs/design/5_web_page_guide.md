# Web Console — Page Implementation Guide

Written by the web foundation review; normative for every console page.

SPINNERET CONSOLE: GUIDE FOR PAGE AGENTS (web/, React 18 + TS strict + Vite 6 + TanStack Router/Query + Connect v2 + Tailwind v4)

0. RULES
- Edit only your own area: src/features/<area>/** and src/i18n/locales/{en,zh-CN}/<area>.json. Do NOT edit src/router.tsx, src/app/layout/nav.ts, src/i18n/locales/*/common.json, src/gen/**, src/components/** or src/lib/** (ask the orchestrator if a shared change is needed).
- Before finishing, run from web/: pnpm format && pnpm typecheck && pnpm lint && pnpm test && pnpm build. All must pass. English code comments only. Use pnpm, never npm.
- Never import 'monaco-editor' or 'echarts' directly; use CodeEditor/DiffView/EChart (lazy chunks).

1. WHERE PAGES GO (replace the placeholder, keep a DEFAULT export; the router lazy-loads these exact files)
/identities -> src/features/identities/pages/IdentitiesPage.tsx (route id '/_app/identities')
/identities/$id -> src/features/identities/pages/IdentityDetailPage.tsx (useParams({ from: '/_app/identities/$id' }))
/identity-types -> src/features/identity-types/pages/IdentityTypesPage.tsx
/accounts -> src/features/accounts/pages/AccountsPage.tsx
/proxies -> src/features/proxies/pages/ProxiesPage.tsx
/sites -> src/features/sites/pages/SitesPage.tsx
/policies -> src/features/policies/pages/PoliciesPage.tsx
/heatmap -> src/features/heatmap/pages/HeatmapPage.tsx
/breakers -> src/features/breakers/pages/BreakersPage.tsx
/config -> src/features/config/pages/ConfigPage.tsx
/secrets -> src/features/secrets/pages/SecretsPage.tsx
/requests -> src/features/requests/pages/RequestsPage.tsx
/risk-events -> src/features/risk-events/pages/RiskEventsPage.tsx
/notifications -> src/features/notifications/pages/NotificationsPage.tsx
/access/tokens, /access/users, /access/audit -> src/features/access/pages/{TokensPage,UsersPage,AuditPage}.tsx
/admin/tenants -> src/features/tenants/pages/TenantsPage.tsx (route renders even when the user has no tenants)
Route ids are '/_app' + path (e.g. '/_app/access/tokens'). Put components in src/features/<area>/components/, hooks and api helpers in src/features/<area>/ (e.g. useProxies.ts), and pure-logic tests next to them (*.test.ts, vitest + jsdom). src/test/setup.ts already stubs ResizeObserver, pointer capture, scrollIntoView, scrollTo and matchMedia for Radix components; don't re-stub them in tests. Breadcrumbs and document title come from the route automatically; override the title with usePageTitle(title) from '@/app/pageTitle' (e.g. an entity name on a detail page).

2. SEARCH PARAMS
List routes accept any search params (type PageSearch = Record<string, string | number | boolean | string[] | undefined>). Read with const search = useSearch({ from: '/_app/proxies' }) and validate or normalize each key yourself. Update with const navigate = useNavigate({ from: '/proxies' }); navigate({ search: (prev) => ({ ...prev, state: 'active' }), replace: true }). Link to a detail page with <Link to="/identities/$id" params={{ id }}>.

3. PAGE SKELETON AND STATES (every page handles loading, empty, error and permission denied)
export default function ProxiesPage() {
  const { t } = useTranslation('proxies');
  return (
    <RequirePermission permission={PERMISSIONS.proxyRead}>
      <PageHeader title={t('title')} description={t('description')} actions={...} />
      ...content...
    </RequirePermission>
  );
}
- Loading: Skeleton (ui/skeleton), DataTable isLoading, StatCard loading, or FullPageLoader.
- Empty: <EmptyState title description? icon? action? compact? />.
- Error: <ErrorState error={query.error} onRetry={() => void query.refetch()} compact? title? description? />. It shows the translated Spinneret-Reason (errors.reasons.<reason>) or code, the raw server message and the reason code; for PermissionDenied it shows a lock icon and no retry.
- Permission denied: RequirePermission renders it. Tables with no data show EmptyState or ErrorState themselves.

4. AUTH AND PERMISSIONS (import from '@/app/auth/AuthContext', '@/app/auth/permissions', '@/app/auth/PermissionGate')
useAuth() returns { status, error, user, isPlatformAdmin, tenants, tenant, tenantId, namespaces, namespace, namespaceName, setTenant, setNamespace, can(permission, site?), canAny(permissions[], site?), canInTenant(permission), login, logout, refresh }.
- namespaceName: pass it as `namespace` in EVERY namespace-scoped request. Disable queries until it exists: enabled: Boolean(namespaceName).
- can(perm, site?): site is a site ID or site name. Without a site, a per-site grant on any site counts (use it for nav/page visibility). With a site, only grants on that site (or namespace-wide grants) count. Namespace-level permissions (namespace:write, proxy:write/operate, config:*, secret:*, token:*, user:*, audit:read, notify:*) never come from per-site grants. proxy:read and namespace:read also come from site-restricted bindings pinned to the namespace. Platform admins pass everything; tenant:manage and kek:manage are platform-admin only.
- canInTenant(perm): tenant-level resources (users, role bindings, tenant-wide notification channels or alerts listed without a namespace). It needs a binding covering all namespaces and sites.
- PERMISSIONS constants: tenantManage, kekManage, namespaceRead/Write, siteRead/Write, identityRead/Write/Operate/Reveal, proxyRead/Write/Operate, policyRead/Write/Publish, breakerRead/Operate, configRead/Write/Publish, secretList/Write/Reveal, tokenRead/Write, userRead/Write, auditRead, notifyRead/Write, dashboardRead. Also exported: checkPermission, checkTenantPermission, bindingGrants, ROLE_PERMISSIONS, sitesWithPermission(scope, perm) (site IDs granted only per site).
- usePermission(perm, site?) is a boolean shorthand.
- <RequirePermission permission={p | p[]} mode?="all"|"any" site? tenantLevel?>children</RequirePermission> is the page guard.
- <PermissionGate permission mode? site? tenantLevel? fallback?>{children | (allowed) => node}</PermissionGate> hides content, or lets a render function decide.
- <PermissionButton permission site? tenantLevel? {...ButtonProps}/> is disabled with a tooltip explaining the missing permission (spec: disable actions, don't hide them). It forwards its ref to the <button>, so it works as an asChild trigger (DropdownMenuTrigger, PopoverTrigger, DialogTrigger).
- Users page: RequirePermission permission={PERMISSIONS.userRead} tenantLevel; write actions use PermissionButton permission="user:write" tenantLevel.

5. DATA: CLIENTS, QUERY KEYS, REFRESH
- Clients (from '@/lib/clients'), typed Connect v2 clients sharing one JSON transport that adds X-Spinneret-CSRF: 1 and X-Spinneret-Tenant automatically. An Unauthenticated error ends the session and redirects to /login?redirect=… by itself:
  authClient, tenantClient (TenantAdminService), accessClient (AccessAdminService), siteClient (SiteAdminService), identityClient (IdentityAdminService), proxyClient (ProxyAdminService), policyClient (PolicyAdminService), breakerClient (BreakerAdminService), configAdminClient, secretAdminClient, notificationClient (NotificationAdminService), dashboardClient (DashboardService); node-only: leaseClient, reportClient, configClient, secretClient.
  Methods are lowerCamelCase RPC names taking a plain init object with camelCase fields: proxyClient.listProxies({ namespace, pageSize, pageToken }, { signal }). Message types come from '@/gen/spinneret/v1/<file>_pb' (types only; build nested messages with create(Schema, {...}) from '@bufbuild/protobuf' when needed).
- Proto conventions: int64 fields are bigint (use toNumber or formatNumber); Timestamps are messages (use toDate, TimeAgo, toTimestamp, lastTimeRange(ms)); admin durations are strings ("30m", "permanent"); enum-like fields are strings.
- Query keys: const key = useScopedQueryKey(); key('<domain>', ...parts) builds ['<domain>', tenantId, namespace, ...parts]. The domain MUST be one of: dashboard identities identity-types accounts proxies sites policies heatmap breakers config secrets requests risk-events notifications access tenants. Real-time events invalidate by domain: breaker.transition -> breakers,dashboard,sites; identity.state -> identities,accounts,heatmap,dashboard; proxy.state -> proxies,dashboard; alert -> notifications,dashboard; config.published -> config; policy.published -> policies,breakers,sites.
- Canonical list query:
  const { namespaceName, tenantId } = useAuth();
  const key = useScopedQueryKey();
  const keepPrevious = useScopedPlaceholder();
  const pager = useCursorPagination({ resetOn: [tenantId, namespaceName, filters] });
  const query = useQuery({
    queryKey: key('proxies', 'list', filters, pager.pageToken, pager.pageSize),
    queryFn: ({ signal }) => proxyClient.listProxies({ namespace: namespaceName ?? '', ...filters, pageSize: pager.pageSize, pageToken: pager.pageToken }, { signal }),
    enabled: Boolean(namespaceName),
    placeholderData: keepPrevious,
    refetchInterval: dialogOpen ? false : LIVE_REFETCH_MS,
  });
  Use useScopedPlaceholder() (never keepPreviousData) so another tenant's or namespace's rows never show after a switch. Always put tenantId and namespaceName in resetOn.
- '@/app/queryClient' exports LIVE_REFETCH_MS = 5000 (identities, proxies, breakers: pause while a dialog is open), OVERVIEW_REFETCH_MS = 10000 and DEFAULT_STALE_TIME_MS = 5000. Default queries retry transient errors twice and never retry unauthenticated, permission_denied, invalid_argument, not_found, already_exists, failed_precondition or unimplemented. Mutations never retry.
- Mutations: useMutation({ mutationFn, onSuccess: () => { toast.success(t('saved')); void queryClient.invalidateQueries({ queryKey: ['proxies'] }); }, onError: (err) => toast.error(errorMessage(err, t)) }). toast comes from 'sonner'. Bulk RPCs return BulkResult { succeeded, failed[{id, reason, message}] }: report the failures.
- Mutations whose variables or results hold credentials or secrets (passwords, token plaintext, secret values, proxy URLs, channel secrets, identity payloads) use useMutation(sensitiveMutation({ ... })) from '@/lib/sensitiveMutation': gcTime 0 and no retry, so the mutation leaves the cache when its form unmounts. Mount such forms only while their dialog is open; a form that stays mounted calls mutation.reset() after success.

6. ERRORS ('@/lib/errors')
describeError(err, t) returns { title (translated reason or code), detail (raw server message), reason, code, retryAfterMs }. errorMessage(err, t) gives a one-line "title: detail" for toasts. Also: toApiError(err) returns { code, codeName, reason, message, retryAfterMs, cause }; errorReason, retryAfterMs, hasCode(err, ...codes), isUnauthenticated, isPermissionDenied, isNotFound, KNOWN_REASONS, REASON_HEADER ('Spinneret-Reason'). Inline form errors: FormField error={...}. The /requests page should detect Code.Unavailable from QueryRequestEvents (ClickHouse off) and render a disabled EmptyState.

7. SHARED COMPONENTS (import paths under '@/components/')
- PageHeader {title, description?, actions?, children?, className?}
- EmptyState {title, description?, icon?: LucideIcon, action?, compact?, className?}
- ErrorState {error?, title?, description?, onRetry?, compact?, className?}
- FullPageLoader {label?}
- ConfirmDialog {open, onOpenChange, title?, description?, children?, confirmLabel?, cancelLabel?, destructive?, affectedCount?, confirmText?, confirmDisabled?, onConfirm: () => void | Promise<unknown>}. With a promise it shows a spinner, closes on success and shows a toast (staying open) on failure. confirmText forces typed confirmation and resets on every open. Use it for ban, delete, archive, revoke, publish, rollback and reveal; pass affectedCount for bulk actions; secret or payload reveal needs an explicit confirm step.
- StateBadge {state, kind?: 'identity'|'proxy'|'breaker'|'account'|'site', label?, className?}. Colors follow spec section 5; labels come from common states.* (pending active expired banned quarantined disabled retired dead closed open half_open paused unknown).
- TimeAgo {value: Timestamp|Date|number|string|undefined, fallback?, past?, className?}: relative time with the absolute time in a tooltip.
- CopyButton {value, label?, ...ButtonProps} (also works over plain HTTP) and IdText {value, truncate?, copy?, className?} (monospace ID + copy), both from '@/components/CopyButton'.
- JsonView {value, collapseDepth? = 2, copyable? = true, className?}
- editor/CodeEditor {value, onChange?, language?: 'yaml'|'json'|'plaintext' (default yaml), readOnly?, height? = 360, markers?: {line, column?, endLine?, endColumn?, message, severity?: 'error'|'warning'|'info'}[], path?, className?, options?, 'aria-label'?}. Lazy Monaco with skeleton; the theme follows the app. Give each simultaneously mounted editor a unique `path` (or none).
- editor/DiffView {original, modified, language?, height? = 420, sideBySide? = true, editable?, onModifiedChange?, className?}
- DurationInput {value, onChange, allowPermanent?, allowEmpty?, presets?, placeholder?, disabled?, id?, className?}. Block submit with validateDurationInput(value, { allowPermanent, allowEmpty }) === 'ok'.
- TagsInput {value: string[], onChange, placeholder?, maxTags?, validate?: (tag) => boolean, disabled?, id?}
- KeyValueEditor {value: {key, value}[], onChange, keyPlaceholder?, valuePlaceholder?, disabled?, secretValues?}. Helpers pairsToRecord, recordToPairs and duplicateKeys are in '@/lib/keyValue'.
- FilterBar {search?: {value, onChange (debounced), placeholder?, debounceMs?}, children (filter controls), activeCount?, onReset?, actions?} and SearchInput (same props as search), both from '@/components/FilterBar'.
- StatCard {label, value, hint?, icon?, tone?: 'default'|'success'|'warning'|'danger'|'muted', loading?, children?}
- charts/EChart {option (MEMOIZE it with useMemo), height? = 240, loading?, setOptionOpts? (default notMerge), onEvents? (memoize), 'aria-label'?, className?}. Registered: line, bar, pie, heatmap, scatter + grid, tooltip, legend, dataset, title, visualMap, dataZoom, markLine, markArea. Colors: CHART_COLORS and SEMANTIC_CHART_COLORS[name][resolvedTheme] from charts/palette (useTheme() from '@/app/theme/ThemeProvider').
- TenantSwitcher, NamespaceSwitcher, LanguageSwitcher, ThemeToggle are already in the topbar; don't re-add them. Scope switches do not navigate, so router blockers never see them: editors with local drafts call useRegisterUnsavedChanges(dirty | () => boolean) from '@/app/unsaved/useUnsavedChanges' while mounted, and the switchers ask before discarding. useUnsavedChangesRegistry() returns { hasUnsavedChanges, confirmDiscard, runAfterDiscardConfirmed } for other actions that discard drafts.
- DataTable ('@/components/data-table'): <DataTable columns data getRowId isLoading={query.isLoading} isFetching={query.isFetching} error={query.error} onRetry emptyTitle emptyDescription emptyAction sorting onSortingChange manualSorting initialSorting enableColumnVisibility(default true) columnVisibility onColumnVisibilityChange initialColumnVisibility enableRowSelection rowSelection onRowSelectionChange bulkActions={(selectedRows, clearSelection) => node} onRowClick rowClassName virtualize="auto"(>100 rows)|true|false estimateRowHeight maxHeight pagination={{ pager, nextPageToken: data?.nextPageToken, total: data?.total, pageSizeOptions? }} toolbar className skeletonRows />.
  Columns are DataTableColumn<T>[] (TanStack ColumnDef) with meta: { label (column menu, REQUIRED for hideable columns), align, className, headerClassName }. Memoize columns with useMemo([t, ...]).
  Pass isLoading, not isPending (isPending stays true for disabled queries). Always pass getRowId (entity ID) when selecting rows.
  Rows with onRowClick are keyboard-focusable (Enter/Space); clicks on buttons, links and inputs inside cells don't trigger it. A failed refetch over existing rows shows a stale-data banner.
  With manualSorting, map the sorting state to the request's order_by and reset the pager.
  Row details: renderExpandedRow={(row, tableRow) => node} adds an expander column and a full-width detail row below each expanded row; getRowCanExpand={(tableRow) => boolean} limits which rows expand (default all); expansion state is keyed by row id (pass getRowId), optionally controlled with expanded/onExpandedChange or seeded with initialExpanded. Works with virtualization (detail rows are measured).
  useCursorPagination({ pageSize?, resetOn }) returns { pageToken, pageSize, pageIndex, canPrevious, next(token), previous, first, setPageSize }; PAGE_SIZE_OPTIONS = 25/50/100/200/500, DEFAULT_PAGE_SIZE = 50.
- UI primitives ('@/components/ui/<name>'): button (Button, variants default|destructive|outline|secondary|ghost|link, sizes default|sm|lg|icon|icon-sm, asChild), input, textarea, select (Select, SelectTrigger size?, SelectValue, SelectContent, SelectItem, SelectGroup, SelectLabel, SelectSeparator), checkbox, switch, label, badge (default|secondary|destructive|outline|muted), card (Card, CardHeader, CardTitle, CardDescription, CardAction, CardContent, CardFooter), dialog (DialogContent hideClose?), alert-dialog, dropdown-menu, popover, tabs, tooltip (SimpleTooltip {content, side?, enabled?}; wrap disabled buttons in a span), table (static tables), skeleton, separator, scroll-area, sheet (SheetContent side?), form (FormField {label?, description?, error?, required?, id?, inline?, children: ONE control} wires id and aria-* into the control, including Select; FormStack). Use semantic Tailwind tokens (bg-background, bg-card, text-muted-foreground, border, text-destructive) and the `tabular` utility for numbers so both themes work.

8. LIB HELPERS
- '@/lib/time': toDate, toTimestamp, formatDateTime(v, withSeconds?), formatClock, formatRelative(v, now?, lang?), remainingMs, lastTimeRange(ms) -> TimeRange, dateLocale, SECOND_MS/MINUTE_MS/HOUR_MS/DAY_MS.
- '@/lib/duration': parseDuration -> {permanent, ms} | undefined, formatDuration(ms | parsed) (canonical like the backend durationx), normalizeDuration, humanizeDuration(ms, maxUnits | { maxUnits?, t? }) (pass { t } for user-visible text: "1天 2小时" in zh-CN; without t the compact English form "1d 2h"), isValidDuration(s, allowPermanent?), validateDurationInput, durationToMs, DURATION_PRESETS, PERMANENT, DURATION_PATTERN.
- '@/lib/format': formatNumber(v, opts?, lang?), formatCompact, formatPercent(ratio 0..1, digits?, lang?), formatRate(perSecond, lang?), formatBytes, formatLatency(ms), toNumber(bigint|number).
- '@/lib/sse': useEventSource(url|null, onMessage, { eventTypes?, withCredentials?, minRetryMs?, maxRetryMs? }) -> 'idle'|'connecting'|'open'|'reconnecting'. The shell already subscribes to the namespace event stream; pages normally don't need it.
- '@/lib/useDebouncedValue' (useDebouncedValue(value, ms = 300)), '@/lib/clipboard' (copyText(text, container?)), '@/lib/states' (stateTone, STATE_TONE_DOT_CLASS), '@/lib/utils' (cn), '@/lib/storage' (readStorage and writeStorage; prefix new keys with 'spinneret.').

9. I18N
- Put keys in src/i18n/locales/en/<area>.json and src/i18n/locales/zh-CN/<area>.json (currently {}). Both files must have identical keys; zh-CN must be real Simplified Chinese. The namespace name is the file name (e.g. 'identity-types', 'risk-events').
- const { t } = useTranslation('<area>'): the namespace loads lazily and Suspense covers it. Missing keys fall back to 'common', so t('actions.save') works; write t('common:actions.save') to be explicit. For translations shared across components of the area, pass the same namespace.
- Useful common keys: actions.{save,cancel,confirm,close,retry,refresh,delete,edit,create,add,remove,copy,copied,search,reset,apply,clear,back,viewAll,submit,more,processing}; states.<state>; table.*; filters.{search,reset,active_one/_other}; confirm.{title,typeToConfirm,affected_one/_other}; permission.{deniedTitle,deniedDescription,missing}; duration.*; time.{never,updated}; validation.{required,minLength,mismatch}; errors.codes.<code>; errors.reasons.<reason>; nav.<page> (page titles).
- Plurals use i18next suffixes _one/_other with {{count}}. Never hardcode user-visible strings, including aria-labels, placeholders and toast text.

10. SPEC REMINDERS
Dense tables with sticky headers, monospace IDs with copy (IdText), relative times with an absolute tooltip (TimeAgo), row click opens the detail page. Destructive or sensitive actions go through ConfirmDialog, bulk actions show the count. Layout must work at 1280px width. Durations in admin APIs are strings; node APIs use *_ms ints.