import { useMutation } from '@tanstack/react-query';
import { LoaderCircleIcon } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { PERMISSIONS } from '@/app/auth/permissions';
import { CodeEditor } from '@/components/editor/CodeEditor';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { FormField } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import { configAdminClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';

import {
  CONFIG_FORMATS,
  checkJsonSyntax,
  formatLanguage,
  formatSupportsSchema,
  isConfigFormat,
  MAX_CONFIG_COMMENT_LENGTH,
  MAX_CONFIG_CONTENT_BYTES,
  MAX_CONFIG_DESCRIPTION_LENGTH,
  validateConfigGroup,
  validateConfigKey,
  utf8ByteLength,
  validateSchemaDocument,
  type ConfigFormat,
} from '../configModel';
import { scanSecretRefs } from '../secretRefs';
import { useConfigCacheUpdate, type ConfigSelection } from '../useConfigApi';
import { EditorField } from './EditorField';
import { SecretRefHelp } from './SecretRefHelp';

interface FormState {
  group: string;
  key: string;
  format: ConfigFormat;
  description: string;
  schema: string;
  content: string;
  publish: boolean;
  comment: string;
}

const INITIAL: FormState = {
  group: '',
  key: '',
  format: 'json',
  description: '',
  schema: '',
  content: '{\n  \n}\n',
  publish: false,
  comment: '',
};

export interface CreateConfigDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Prefilled group (e.g. the selected item's group). */
  defaultGroup?: string;
  onCreated: (selection: ConfigSelection) => void;
}

type ContentError =
  { kind: 'jsonEmpty' | 'jsonInvalid' | 'tooLarge' } | { kind: 'secretRefs'; count: number };

/** Client-side content checks, the same the item editor applies before saving. */
function contentError(form: FormState): ContentError | undefined {
  if (utf8ByteLength(form.content) > MAX_CONFIG_CONTENT_BYTES) return { kind: 'tooLarge' };
  if (form.format === 'json') {
    const json = checkJsonSyntax(form.content);
    if (!json.ok) return { kind: json.empty ? 'jsonEmpty' : 'jsonInvalid' };
  }
  const { problems } = scanSecretRefs(form.content);
  return problems.length > 0 ? { kind: 'secretRefs', count: problems.length } : undefined;
}

function hasFormErrors(form: FormState): boolean {
  return (
    validateConfigGroup(form.group) !== undefined ||
    validateConfigKey(form.key) !== undefined ||
    (formatSupportsSchema(form.format) && validateSchemaDocument(form.schema) !== undefined) ||
    contentError(form) !== undefined
  );
}

function useFormErrors(form: FormState, touched: boolean) {
  const { t } = useTranslation('config');
  if (!touched) return {};
  const group = validateConfigGroup(form.group);
  const key = validateConfigKey(form.key);
  const schema = formatSupportsSchema(form.format) ? validateSchemaDocument(form.schema) : undefined;
  const content = contentError(form);
  let contentMessage: string | undefined;
  if (content?.kind === 'secretRefs') contentMessage = t('secretRefs.problems', { count: content.count });
  else if (content) contentMessage = t(`editor.${content.kind}`);
  return {
    group: group ? t(`validation.group.${group}`) : undefined,
    key: key ? t(`validation.key.${key}`) : undefined,
    schema: schema ? t(`validation.schema.${schema}`) : undefined,
    content: contentMessage,
  };
}

/** Dialog creating a config item with draft or published content. */
export function CreateConfigDialog({ open, onOpenChange, defaultGroup, onCreated }: CreateConfigDialogProps) {
  const { t } = useTranslation('config');
  const { namespaceName, can } = useAuth();
  const canPublish = can(PERMISSIONS.configPublish);
  const updateCache = useConfigCacheUpdate();
  const [form, setForm] = useState<FormState>(INITIAL);
  const [touched, setTouched] = useState(false);
  const [wasOpen, setWasOpen] = useState(open);
  // Reset the form on every opening.
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setForm({ ...INITIAL, group: defaultGroup && !defaultGroup.startsWith('_') ? defaultGroup : '' });
      setTouched(false);
    }
  }
  const errors = useFormErrors(form, touched);
  const set = <K extends keyof FormState>(field: K, value: FormState[K]) =>
    setForm((prev) => ({ ...prev, [field]: value }));

  const mutation = useMutation({
    mutationFn: (values: FormState) =>
      configAdminClient.createConfigItem({
        namespace: namespaceName ?? '',
        group: values.group,
        key: values.key,
        format: values.format,
        description: values.description.trim(),
        schemaJson: formatSupportsSchema(values.format) ? values.schema.trim() : '',
        content: values.content,
        publish: values.publish && canPublish,
        comment: values.publish ? values.comment.trim() : '',
      }),
    onSuccess: (res, values) => {
      toast.success(values.publish ? t('create.createdPublished') : t('create.createdDraft'));
      updateCache(res.item);
      onOpenChange(false);
      onCreated({ group: values.group, key: values.key });
    },
    onError: (err) => toast.error(errorMessage(err, t)),
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (!hasFormErrors(form)) mutation.mutate(form);
  };

  const changeFormat = (value: string) => {
    if (!isConfigFormat(value)) return;
    setForm((prev) => {
      // Replace the untouched JSON template when switching formats.
      const untouched = prev.content === INITIAL.content || prev.content === '';
      return {
        ...prev,
        format: value,
        content: untouched ? (value === 'json' ? INITIAL.content : '') : prev.content,
      };
    });
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !mutation.isPending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-3xl">
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <DialogHeader>
            <DialogTitle>{t('create.title')}</DialogTitle>
            <DialogDescription>{t('create.description', { namespace: namespaceName })}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 sm:grid-cols-[1fr_1fr_10rem]">
            <FormField
              label={t('fields.group')}
              required
              error={errors.group}
              description={t('create.groupHint')}
            >
              <Input
                value={form.group}
                onChange={(e) => set('group', e.target.value)}
                className="font-mono"
                autoComplete="off"
                maxLength={64}
              />
            </FormField>
            <FormField label={t('fields.key')} required error={errors.key} description={t('create.keyHint')}>
              <Input
                value={form.key}
                onChange={(e) => set('key', e.target.value)}
                className="font-mono"
                autoComplete="off"
                maxLength={128}
              />
            </FormField>
            <FormField label={t('fields.format')} required>
              <Select value={form.format} onValueChange={changeFormat}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {CONFIG_FORMATS.map((format) => (
                    <SelectItem key={format} value={format}>
                      {t(`formats.${format}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </FormField>
          </div>
          <FormField label={t('fields.description')}>
            <Input
              value={form.description}
              maxLength={MAX_CONFIG_DESCRIPTION_LENGTH}
              onChange={(e) => set('description', e.target.value)}
            />
          </FormField>
          <EditorField label={t('fields.content')} error={errors.content}>
            <CodeEditor
              value={form.content}
              onChange={(v) => set('content', v)}
              language={formatLanguage(form.format)}
              height={220}
              path={`create-config-content.${form.format === 'text' ? 'txt' : form.format}`}
              aria-label={t('fields.content')}
            />
          </EditorField>
          <SecretRefHelp compact />
          {formatSupportsSchema(form.format) && (
            <EditorField label={t('fields.schema')} error={errors.schema} description={t('schema.hint')}>
              <CodeEditor
                value={form.schema}
                onChange={(v) => set('schema', v)}
                language="json"
                height={140}
                path="create-config-schema.json"
                aria-label={t('fields.schema')}
              />
            </EditorField>
          )}
          <div className="grid gap-3 rounded-md border p-3">
            <FormField
              inline
              label={t('create.publishNow')}
              description={
                canPublish
                  ? t('create.publishHint')
                  : t('common:permission.missing', { permission: PERMISSIONS.configPublish })
              }
            >
              <Switch
                checked={form.publish}
                disabled={!canPublish}
                onCheckedChange={(v) => set('publish', v)}
              />
            </FormField>
            {form.publish && (
              <FormField label={t('fields.comment')}>
                <Textarea
                  value={form.comment}
                  maxLength={MAX_CONFIG_COMMENT_LENGTH}
                  rows={2}
                  onChange={(e) => set('comment', e.target.value)}
                />
              </FormField>
            )}
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={mutation.isPending}
              onClick={() => onOpenChange(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={mutation.isPending || !namespaceName}>
              {mutation.isPending && <LoaderCircleIcon className="animate-spin" />}
              {form.publish ? t('create.submitPublish') : t('create.submitDraft')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
