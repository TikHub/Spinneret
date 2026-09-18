import { LanguageSwitcher } from '@/components/LanguageSwitcher';
import { NamespaceSwitcher } from '@/components/NamespaceSwitcher';
import { TenantSwitcher } from '@/components/TenantSwitcher';
import { ThemeToggle } from '@/components/ThemeToggle';
import { Separator } from '@/components/ui/separator';
import { type SseStatus } from '@/lib/sse';

import { Breadcrumbs } from './Breadcrumbs';
import { LiveIndicator } from './LiveIndicator';
import { UserMenu } from './UserMenu';

export interface TopbarProps {
  liveStatus: SseStatus;
}

/** Top bar: breadcrumbs, tenant/namespace switchers, live status, language, theme and account menu. */
export function Topbar({ liveStatus }: TopbarProps) {
  return (
    <header className="flex h-12 shrink-0 items-center gap-3 border-b bg-background/95 px-4 backdrop-blur supports-[backdrop-filter]:bg-background/80">
      <div className="min-w-0 flex-1">
        <Breadcrumbs />
      </div>
      <div className="flex items-center gap-1">
        <TenantSwitcher />
        <span className="text-muted-foreground">/</span>
        <NamespaceSwitcher />
        <Separator orientation="vertical" className="mx-1 !h-5" />
        <LiveIndicator status={liveStatus} />
        <LanguageSwitcher />
        <ThemeToggle />
        <UserMenu />
      </div>
    </header>
  );
}
