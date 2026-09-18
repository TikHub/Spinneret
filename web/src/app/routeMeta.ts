/** Route static data used for breadcrumbs and document titles. */
export interface RouteMeta {
  /** common namespace key of the page title (interpolated with route params). */
  titleKey?: string;
  /** common namespace key of the navigation group (breadcrumb root). */
  groupKey?: string;
  /** Parent page for detail routes. */
  parent?: { to: string; titleKey: string };
  /** The page works without any tenant access (profile, tenant administration). */
  tenantOptional?: boolean;
}

declare module '@tanstack/react-router' {
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  interface StaticDataRouteOption extends RouteMeta {}
}
