import { ChevronDownIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { STATE_TONE_DOT_CLASS, stateTone } from '@/lib/states';
import { cn } from '@/lib/utils';

import { IDENTITY_STATES, type IdentityState } from '../identitySearch';

export interface StatesFilterProps {
  value: readonly IdentityState[];
  onChange: (states: IdentityState[]) => void;
}

/** Multi-select of identity lifecycle states. */
export function StatesFilter({ value, onChange }: StatesFilterProps) {
  const { t } = useTranslation('identities');
  const label =
    value.length === 0
      ? t('filters.allStates')
      : value.length <= 2
        ? value.map((s) => t(`common:states.${s}`)).join(', ')
        : t('filters.statesCount', { count: value.length });

  const toggle = (state: IdentityState, checked: boolean) => {
    const next = checked ? [...value, state] : value.filter((s) => s !== state);
    onChange(IDENTITY_STATES.filter((s) => next.includes(s)));
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          className={cn('max-w-56 justify-between', value.length > 0 && 'border-primary/40')}
          aria-label={t('fields.states')}
        >
          <span className="truncate">{label}</span>
          <ChevronDownIcon className="opacity-50" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-48">
        <DropdownMenuLabel>{t('fields.states')}</DropdownMenuLabel>
        {IDENTITY_STATES.map((state) => (
          <DropdownMenuCheckboxItem
            key={state}
            checked={value.includes(state)}
            onCheckedChange={(checked) => toggle(state, checked)}
            onSelect={(event) => event.preventDefault()}
          >
            <span
              className={cn('size-2 rounded-full', STATE_TONE_DOT_CLASS[stateTone('identity', state)])}
              aria-hidden
            />
            {t(`common:states.${state}`)}
          </DropdownMenuCheckboxItem>
        ))}
        {value.length > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => onChange([])}>{t('filters.clearStates')}</DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
