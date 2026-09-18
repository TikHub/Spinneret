import { Link } from '@tanstack/react-router';
import { LogOutIcon, ShieldCheckIcon, UserIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const letters = parts.length > 1 ? `${parts[0]?.[0] ?? ''}${parts[1]?.[0] ?? ''}` : name.slice(0, 2);
  return letters.toUpperCase() || '?';
}

/** Topbar account menu: profile and sign out. */
export function UserMenu() {
  const { t } = useTranslation();
  const { user, isPlatformAdmin, logout } = useAuth();
  const name = user?.displayName || user?.username || '';

  const signOut = async () => {
    try {
      await logout();
      toast.success(t('shell.loggedOut'));
    } catch {
      // The local session is cleared even when the request fails.
    }
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className="rounded-full"
          aria-label={t('shell.userMenu')}
          data-testid="user-menu"
        >
          <span className="flex size-7 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">
            {initials(name)}
          </span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <DropdownMenuLabel className="flex flex-col gap-0.5">
          <span className="truncate text-sm font-medium text-foreground">{name}</span>
          {user?.email && <span className="truncate text-xs font-normal">{user.email}</span>}
          {isPlatformAdmin && (
            <span className="mt-1 inline-flex items-center gap-1 text-xs font-normal text-primary">
              <ShieldCheckIcon className="size-3" />
              {t('shell.platformAdmin')}
            </span>
          )}
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <Link to="/settings/profile">
            <UserIcon />
            {t('shell.profile')}
          </Link>
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => void signOut()} data-testid="logout">
          <LogOutIcon />
          {t('shell.logout')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
