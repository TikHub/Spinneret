import { CircleHelpIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';

import { toKeySegment } from '../../forms';
import { EXTRA_PERMISSION_OPTIONS, ROLES } from '../../userForm';

/** Help popover explaining what each role and extra permission grants (spec section 3.2). */
export function RoleHelpPopover() {
  const { t } = useTranslation('access');
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="size-6 text-muted-foreground"
          aria-label={t('roles.help.open')}
        >
          <CircleHelpIcon className="size-4" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-96 text-sm">
        <div className="grid gap-3">
          <p className="font-medium">{t('roles.help.title')}</p>
          <dl className="grid gap-2">
            {ROLES.map((role) => (
              <div key={role} className="grid gap-0.5">
                <dt className="font-medium">{t(`roles.names.${role}`)}</dt>
                <dd className="text-xs text-muted-foreground">{t(`roles.descriptions.${role}`)}</dd>
              </div>
            ))}
          </dl>
          <div className="grid gap-1 border-t pt-2">
            <p className="font-medium">{t('roles.help.extraTitle')}</p>
            <p className="text-xs text-muted-foreground">{t('roles.help.extraDescription')}</p>
            <ul className="grid gap-0.5 text-xs">
              {EXTRA_PERMISSION_OPTIONS.map((permission) => (
                <li key={permission}>
                  <code className="font-mono">{permission}</code>
                  <span className="text-muted-foreground">
                    {' '}
                    · {t(`roles.extra.${toKeySegment(permission)}`)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
          <p className="border-t pt-2 text-xs text-muted-foreground">{t('roles.help.scope')}</p>
        </div>
      </PopoverContent>
    </Popover>
  );
}
