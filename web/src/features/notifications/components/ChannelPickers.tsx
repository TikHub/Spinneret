import { useTranslation } from 'react-i18next';

import { TagsInput } from '@/components/TagsInput';

import { ALERT_KINDS } from '../channelConfig';
import { useSiteNames } from '../useNotificationsApi';
import { CheckboxListPicker } from './CheckboxListPicker';

interface PickerProps {
  value: readonly string[];
  onChange: (value: string[]) => void;
  id?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/** Alert kinds with descriptions. */
export function EventTypesPicker({ value, onChange, ...field }: PickerProps) {
  const { t } = useTranslation('notifications');
  const options = ALERT_KINDS.map((kind) => ({
    value: kind,
    label: t(`alertKinds.${kind}.label`),
    description: t(`alertKinds.${kind}.description`),
  }));
  const summary =
    value.length === 0
      ? t('picker.eventTypesNone')
      : value.length === ALERT_KINDS.length
        ? t('picker.eventTypesAll')
        : value.map((kind) => t(`alertKinds.${kind}.label`, { defaultValue: kind })).join(', ');
  return (
    <CheckboxListPicker {...field} options={options} value={value} onChange={onChange} summary={summary} />
  );
}

/** Sites of the channel's namespace; free-text entry when the site list cannot be loaded. */
export function SitesPicker({ namespace, value, onChange, ...field }: PickerProps & { namespace: string }) {
  const { t } = useTranslation('notifications');
  const sites = useSiteNames(namespace, true);
  if (!sites.data) {
    return (
      <TagsInput
        {...field}
        value={value}
        onChange={onChange}
        placeholder={sites.isLoading ? t('common:table.loading') : t('picker.sitesManual')}
        maxTags={500}
      />
    );
  }
  // Keep selected sites that are not in the list (e.g. deleted since) visible and removable.
  const names = [...new Set([...sites.data, ...value])];
  const summary = value.length === 0 ? t('picker.sitesAll') : value.join(', ');
  return (
    <CheckboxListPicker
      {...field}
      options={names.map((name) => ({
        value: name,
        label: <span className="font-mono text-xs">{name}</span>,
      }))}
      value={value}
      onChange={onChange}
      summary={summary}
    />
  );
}
