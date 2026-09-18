import { Link } from '@tanstack/react-router';
import { PanelLeftCloseIcon, PanelLeftOpenIcon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { Button } from '@/components/ui/button';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';

import { visibleNavGroups } from './nav';

export interface SidebarProps {
  collapsed: boolean;
  onToggle: () => void;
}

function Logo({ collapsed }: { collapsed: boolean }) {
  const { t } = useTranslation();
  return (
    <Link
      to="/"
      className="flex items-center gap-2 overflow-hidden rounded-md px-1 outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <img src="/favicon.svg" alt="" className="size-7 shrink-0" />
      {!collapsed && <span className="truncate text-sm font-semibold tracking-tight">{t('app.name')}</span>}
    </Link>
  );
}

/** Collapsible navigation sidebar; entries are hidden when the user lacks their read permission. */
export function Sidebar({ collapsed, onToggle }: SidebarProps) {
  const { t } = useTranslation();
  const { canAny, canInTenant } = useAuth();
  const groups = useMemo(() => visibleNavGroups({ canAny, canInTenant }), [canAny, canInTenant]);

  return (
    <aside
      className={cn(
        'flex h-full shrink-0 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground transition-[width] duration-200',
        collapsed ? 'w-14' : 'w-56',
      )}
      data-collapsed={collapsed}
    >
      <div
        className={cn(
          'flex h-12 items-center border-b border-sidebar-border px-3',
          collapsed && 'justify-center px-0',
        )}
      >
        <Logo collapsed={collapsed} />
      </div>
      <nav className="flex-1 overflow-y-auto px-2 py-3" aria-label={t('shell.mainNavigation')}>
        {groups.map((group) => (
          <div key={group.id} className="mb-3">
            {!collapsed && group.id !== 'overview' && (
              <div className="px-2 pb-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
                {t(group.labelKey)}
              </div>
            )}
            {collapsed && group.id !== 'overview' && <div className="mx-2 mb-2 h-px bg-sidebar-border" />}
            <ul className="grid gap-0.5">
              {group.items.map((item) => {
                const label = t(item.labelKey);
                const Icon = item.icon;
                const link = (
                  <Link
                    to={item.to}
                    activeOptions={{ exact: item.to === '/' }}
                    className={cn(
                      'flex h-8 items-center gap-2.5 rounded-md px-2 text-sm transition-colors outline-none hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-ring data-[status=active]:bg-sidebar-accent data-[status=active]:font-medium data-[status=active]:text-sidebar-accent-foreground',
                      collapsed && 'justify-center px-0',
                    )}
                    aria-label={collapsed ? label : undefined}
                  >
                    <Icon className="size-4 shrink-0" aria-hidden />
                    {!collapsed && <span className="truncate">{label}</span>}
                  </Link>
                );
                return (
                  <li key={item.to}>
                    {collapsed ? (
                      <SimpleTooltip content={label} side="right">
                        {link}
                      </SimpleTooltip>
                    ) : (
                      link
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </nav>
      <div className={cn('border-t border-sidebar-border p-2', collapsed && 'flex justify-center')}>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onToggle}
          aria-label={collapsed ? t('shell.expandSidebar') : t('shell.collapseSidebar')}
          aria-expanded={!collapsed}
        >
          {collapsed ? <PanelLeftOpenIcon /> : <PanelLeftCloseIcon />}
        </Button>
      </div>
    </aside>
  );
}
