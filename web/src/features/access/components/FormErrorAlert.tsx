import { useTranslation } from 'react-i18next';

import { describeError } from '@/lib/errors';

/** Inline server error of a form: translated reason, raw message and reason code. */
export function FormErrorAlert({ error }: { error: unknown }) {
  const { t } = useTranslation();
  if (error === null || error === undefined) return null;
  const described = describeError(error, t);
  return (
    <div
      role="alert"
      className="grid gap-0.5 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive"
    >
      <span className="font-medium">{described.title}</span>
      {described.detail && described.detail !== described.title && (
        <span className="break-words">{described.detail}</span>
      )}
      {described.reason && <code className="text-xs opacity-80">{described.reason}</code>}
    </div>
  );
}
