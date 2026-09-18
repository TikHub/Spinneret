import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { CodeEditor, type EditorMarker } from '@/components/editor/CodeEditor';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { type ConfigItemInfo } from '@/gen/spinneret/v1/config_admin_pb';

import { checkJsonSyntax, formatSupportsSchema, MAX_CONFIG_DESCRIPTION_LENGTH } from '../configModel';
import { type ConfigDraftValues } from '../draftState';
import { type ConfigValidation } from '../useConfigEditor';
import { EditorField } from './EditorField';

export interface ConfigSchemaTabProps {
  item: ConfigItemInfo;
  values: ConfigDraftValues;
  onChange: (field: 'schema' | 'description', value: string) => void;
  readOnly: boolean;
  validation: ConfigValidation;
}

/** JSON Schema (json and yaml items) and description, saved with the draft. */
export function ConfigSchemaTab({ item, values, onChange, readOnly, validation }: ConfigSchemaTabProps) {
  const { t } = useTranslation('config');
  const supported = formatSupportsSchema(item.format);
  const markers = useMemo<EditorMarker[]>(() => {
    if (values.schema.trim() === '') return [];
    const parsed = checkJsonSyntax(values.schema);
    return parsed.ok ? [] : [parsed.marker];
  }, [values.schema]);

  return (
    <div className="grid gap-4">
      <FormField label={t('fields.description')}>
        <Input
          value={values.description}
          readOnly={readOnly}
          maxLength={MAX_CONFIG_DESCRIPTION_LENGTH}
          onChange={(e) => onChange('description', e.target.value)}
        />
      </FormField>
      {supported ? (
        <EditorField
          label={t('fields.schema')}
          error={validation.schemaError ? t(`validation.schema.${validation.schemaError}`) : undefined}
          description={t('schema.hint')}
        >
          <CodeEditor
            value={values.schema}
            onChange={readOnly ? undefined : (v) => onChange('schema', v)}
            readOnly={readOnly}
            language="json"
            markers={markers}
            height="min(50vh, 460px)"
            path={`config/${item.namespace}/${item.group}/${item.key}.schema.json`}
            aria-label={t('fields.schema')}
          />
        </EditorField>
      ) : (
        <p className="rounded-md border border-dashed p-4 text-sm text-muted-foreground">
          {t('schema.notSupported')}
        </p>
      )}
    </div>
  );
}
