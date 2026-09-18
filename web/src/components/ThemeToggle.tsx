import { MonitorIcon, MoonIcon, SunIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { useTheme, type ThemePreference } from '@/app/theme/ThemeProvider';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';

const OPTIONS: Array<{ value: ThemePreference; icon: typeof SunIcon }> = [
  { value: 'light', icon: SunIcon },
  { value: 'dark', icon: MoonIcon },
  { value: 'system', icon: MonitorIcon },
];

/** Topbar theme menu (light / dark / system). */
export function ThemeToggle() {
  const { t } = useTranslation();
  const { theme, resolvedTheme, setTheme } = useTheme();
  const Icon = resolvedTheme === 'dark' ? MoonIcon : SunIcon;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={t('shell.theme')} data-testid="theme-toggle">
          <Icon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>{t('shell.theme')}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuRadioGroup value={theme} onValueChange={(v) => setTheme(v as ThemePreference)}>
          {OPTIONS.map(({ value, icon: OptionIcon }) => (
            <DropdownMenuRadioItem key={value} value={value}>
              <OptionIcon />
              {t(`theme.${value}`)}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
