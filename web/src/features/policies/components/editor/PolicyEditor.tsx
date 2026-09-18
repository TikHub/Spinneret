import {
  CheckCheckIcon,
  FileCode2Icon,
  HistoryIcon,
  ListTreeIcon,
  LoaderCircleIcon,
  RocketIcon,
  SaveIcon,
  Trash2Icon,
  Undo2Icon,
} from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { PERMISSIONS } from '@/app/auth/permissions';
import { PermissionButton } from '@/app/auth/PermissionGate';
import { CodeEditor } from '@/components/editor/CodeEditor';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { type Policy, type ValidatePolicyResponse } from '@/gen/spinneret/v1/policy_admin_pb';
import { errorMessage } from '@/lib/errors';

import { isPolicyKind, MAX_YAML_BYTES, type PolicyKind } from '../../constants';
import { loadPolicyModel } from '../../model/index';
import { baseYaml } from '../../selectors';
import { policyTemplate } from '../../templates';
import { useSaveDraft, useValidatePolicy } from '../../usePolicyMutations';
import { DeletePolicyDialog } from './DeletePolicyDialog';
import { PolicyBuilder } from './PolicyBuilder';
import { PublishDialog } from './PublishDialog';
import { RollbackDialog } from './RollbackDialog';
import { ValidationPanel } from './ValidationPanel';

/** Unsaved editor text per policy ID, kept while the page is open. */
export type UnsavedDrafts = Map<string, string>;

export interface PolicyEditorProps {
  policy: Policy;
  unsaved: UnsavedDrafts;
  onDeleted: () => void;
}

type EditorMode = 'builder' | 'yaml';
type DialogName = 'publish' | 'rollback' | 'delete';

/** Policy editor with form/rule-list and YAML modes, validation, draft, publish, rollback and delete. */
export function PolicyEditor({ policy, unsaved, onDeleted }: PolicyEditorProps) {
  const { t } = useTranslation('policies');
  const kind: PolicyKind = isPolicyKind(policy.kind) ? policy.kind : 'rotation';
  const base = baseYaml(policy);
  const [text, setText] = useState(() => unsaved.get(policy.id) ?? base);
  const [lastBase, setLastBase] = useState(base);
  const [mode, setMode] = useState<EditorMode>(() =>
    loadPolicyModel(kind, unsaved.get(policy.id) ?? base).ok ? 'builder' : 'yaml',
  );
  const [validation, setValidation] = useState<{ text: string; result: ValidatePolicyResponse } | null>(null);
  const [dialog, setDialog] = useState<DialogName | null>(null);
  const saveDraft = useSaveDraft();
  const validate = useValidatePolicy();

  // Follow server-side changes (save, publish, another operator) while there are no local edits.
  if (base !== lastBase) {
    setLastBase(base);
    if (text === lastBase) setText(base);
  }

  const dirty = text !== base;
  const tooLarge = new Blob([text]).size > MAX_YAML_BYTES;
  const updateText = (next: string) => {
    setText(next);
    if (next === base) unsaved.delete(policy.id);
    else unsaved.set(policy.id, next);
  };

  const save = () =>
    saveDraft.mutate(
      { id: policy.id, yaml: text },
      {
        // Keep edits made while the request was running.
        onSuccess: (_res, input) => {
          if (unsaved.get(policy.id) === input.yaml) unsaved.delete(policy.id);
        },
        onError: (err) => toast.error(t('toasts.draftFailed'), { description: errorMessage(err, t) }),
      },
    );
  const runValidation = () =>
    validate.mutate({ kind, yaml: text }, { onSuccess: (result) => setValidation({ text, result }) });
  const discard = () => updateText(base);
  const builderLabel = kind === 'signal' || kind === 'action' ? t('editor.rulesMode') : t('editor.formMode');

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <div role="group" aria-label={t('editor.mode')} className="inline-flex rounded-md border p-0.5">
          <Button
            variant={mode === 'builder' ? 'secondary' : 'ghost'}
            size="sm"
            aria-pressed={mode === 'builder'}
            onClick={() => setMode('builder')}
          >
            <ListTreeIcon />
            {builderLabel}
          </Button>
          <Button
            variant={mode === 'yaml' ? 'secondary' : 'ghost'}
            size="sm"
            aria-pressed={mode === 'yaml'}
            onClick={() => setMode('yaml')}
          >
            <FileCode2Icon />
            {t('editor.yamlMode')}
          </Button>
        </div>
        <EditorStatus policy={policy} dirty={dirty} />
        <div className="ml-auto flex flex-wrap items-center gap-2">
          {dirty && (
            <Button variant="ghost" size="sm" onClick={discard}>
              <Undo2Icon />
              {t('editor.discard')}
            </Button>
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={runValidation}
            disabled={validate.isPending || text.trim() === ''}
          >
            {validate.isPending ? <LoaderCircleIcon className="animate-spin" /> : <CheckCheckIcon />}
            {t('editor.validate')}
          </Button>
          <PermissionButton
            permission={PERMISSIONS.policyWrite}
            variant="outline"
            size="sm"
            onClick={save}
            disabled={!dirty || saveDraft.isPending || tooLarge || text.trim() === ''}
          >
            {saveDraft.isPending ? <LoaderCircleIcon className="animate-spin" /> : <SaveIcon />}
            {t('editor.saveDraft')}
          </PermissionButton>
          <PermissionButton
            permission={PERMISSIONS.policyPublish}
            size="sm"
            onClick={() => setDialog('publish')}
            disabled={(!dirty && !policy.hasDraft) || tooLarge || text.trim() === ''}
          >
            <RocketIcon />
            {t('editor.publish')}
          </PermissionButton>
          <PermissionButton
            permission={PERMISSIONS.policyPublish}
            variant="outline"
            size="sm"
            onClick={() => setDialog('rollback')}
            disabled={policy.currentVersion < 2}
          >
            <HistoryIcon />
            {t('editor.rollback')}
          </PermissionButton>
          <PermissionButton
            permission={PERMISSIONS.policyWrite}
            variant="ghost"
            size="sm"
            className="text-destructive hover:text-destructive"
            onClick={() => setDialog('delete')}
          >
            <Trash2Icon />
            {t('editor.delete')}
          </PermissionButton>
        </div>
      </div>

      {tooLarge && <p className="text-xs text-destructive">{t('editor.tooLarge')}</p>}
      {validation && (
        <ValidationPanel
          policyId={policy.id}
          result={validation.result}
          outdated={validation.text !== text}
          onClose={() => setValidation(null)}
        />
      )}

      {mode === 'builder' && (
        <PolicyBuilder
          kind={kind}
          text={text}
          onTextChange={updateText}
          onOpenYaml={() => setMode('yaml')}
          onResetTemplate={() => updateText(policyTemplate(kind, policy.name))}
        />
      )}
      {mode === 'yaml' && (
        <CodeEditor
          value={text}
          onChange={updateText}
          language="yaml"
          height="calc(100vh - 22rem)"
          path={`policy-${policy.id}.yaml`}
          aria-label={t('editor.yamlLabel', { name: policy.name })}
          className="min-h-[420px]"
        />
      )}

      <PublishDialog
        policy={policy}
        open={dialog === 'publish'}
        onOpenChange={(open) => setDialog(open ? 'publish' : null)}
        unsavedYaml={dirty ? text : undefined}
        onPublished={() => unsaved.delete(policy.id)}
      />
      <RollbackDialog
        policy={policy}
        open={dialog === 'rollback'}
        onOpenChange={(open) => setDialog(open ? 'rollback' : null)}
      />
      <DeletePolicyDialog
        policy={policy}
        open={dialog === 'delete'}
        onOpenChange={(open) => setDialog(open ? 'delete' : null)}
        onDeleted={() => {
          unsaved.delete(policy.id);
          onDeleted();
        }}
      />
    </div>
  );
}

function EditorStatus({ policy, dirty }: { policy: Policy; dirty: boolean }) {
  const { t } = useTranslation('policies');
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-xs">
      {dirty && (
        <Badge variant="outline" className="border-amber-500/40 text-amber-700 dark:text-amber-400">
          {t('editor.unsaved')}
        </Badge>
      )}
      {!dirty && policy.hasDraft && <Badge variant="secondary">{t('editor.editingDraft')}</Badge>}
      {!dirty && !policy.hasDraft && policy.currentVersion > 0 && (
        <Badge variant="muted">{t('editor.matchesPublished', { version: policy.currentVersion })}</Badge>
      )}
    </div>
  );
}
