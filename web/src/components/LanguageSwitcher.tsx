import { LanguagesIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

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
import { LANGUAGE_NAMES, SUPPORTED_LANGUAGES, toSupportedLanguage } from '@/i18n';

/** Topbar language menu (English / 简体中文), persisted in localStorage. */
export function LanguageSwitcher() {
  const { t, i18n } = useTranslation();
  const current = toSupportedLanguage(i18n.language) ?? 'en';

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={t('shell.language')}
          data-testid="language-switcher"
        >
          <LanguagesIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>{t('shell.language')}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuRadioGroup value={current} onValueChange={(lng) => void i18n.changeLanguage(lng)}>
          {SUPPORTED_LANGUAGES.map((lng) => (
            <DropdownMenuRadioItem key={lng} value={lng} data-testid={`language-${lng}`}>
              {LANGUAGE_NAMES[lng]}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
