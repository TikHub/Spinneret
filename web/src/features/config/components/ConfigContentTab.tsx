import { CircleCheckIcon, CircleXIcon } from 'lucide-react';
import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { CodeEditor, type EditorMarker } from '@/components/editor/CodeEditor';
import { type ConfigItemInfo } from '@/gen/spinneret/v1/config_admin_pb';
import { formatBytes } from '@/lib/format';

import { formatLanguage, MAX_CONFIG_CONTENT_BYTES } from '../configModel';
import { secretRefMarkers } from '../secretRefs';
import { type ConfigValidation } from '../useConfigEditor';
import { SecretRefHelp } from './SecretRefHelp';

export interface ConfigContentTabProps {
  item: ConfigItemInfo;
  content: string;
  onChange: (value: string) => void;
  readOnly: boolean;
  validation: ConfigValidation;
}

/** Content editor with client-side JSON and secret reference checks. */
export function ConfigContentTab({ item, content, onChange, readOnly, validation }: ConfigContentTabProps) {
  const { t } = useTranslation('config');
  const { json, refs, contentBytes, tooLarge } = validation;

  const markers = useMemo<EditorMarker[]>(() => {
    const list = secretRefMarkers(refs.problems, (issue) => t(`secretRefs.issues.${issue}`));
    if (json && !json.ok && !json.empty) list.push(json.marker);
    return list;
  }, [json, refs.problems, t]);

  const ext = item.format === 'text' ? 'txt' : item.format;
  return (
    <div className="grid gap-3">
      <CodeEditor
        value={content}
        onChange={readOnly ? undefined : onChange}
        readOnly={readOnly}
        language={formatLanguage(item.format)}
        markers={markers}
        height="min(60vh, 560px)"
        path={`config/${item.namespace}/${item.group}/${item.key}.content.${ext}`}
        aria-label={t('fields.content')}
      />
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
        {json &&
          (json.ok ? (
            <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
              <CircleCheckIcon className="size-3.5" aria-hidden />
              {t('editor.jsonValid')}
            </span>
          ) : (
            <span className="inline-flex items-center gap-1 text-destructive" role="alert">
              <CircleXIcon className="size-3.5" aria-hidden />
              {json.empty
                ? t('editor.jsonEmpty')
                : t('editor.jsonError', {
                    line: json.marker.line,
                    column: json.marker.column,
                    message: json.marker.message,
                  })}
            </span>
          ))}
        {refs.problems.length > 0 && (
          <span className="inline-flex items-center gap-1 text-destructive" role="alert">
            <CircleXIcon className="size-3.5" aria-hidden />
            {t('secretRefs.problems', { count: refs.problems.length })}
          </span>
        )}
        <span className={tooLarge ? 'text-destructive' : 'text-muted-foreground'}>
          {t('editor.size', { size: formatBytes(contentBytes), max: formatBytes(MAX_CONFIG_CONTENT_BYTES) })}
        </span>
        {item.format === 'yaml' && (
          <span className="text-muted-foreground">{t('editor.yamlServerCheck')}</span>
        )}
      </div>
      <SecretRefHelp refs={refs.refs} />
    </div>
  );
}
