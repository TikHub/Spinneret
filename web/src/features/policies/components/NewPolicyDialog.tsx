import { LoaderCircleIcon } from 'lucide-react';
import { useState } from 'react';
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
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import { SimpleTooltip } from '@/components/ui/tooltip';
import { errorMessage } from '@/lib/errors';
import { cn } from '@/lib/utils';

import { NAME_PATTERN, POLICY_KINDS, type PolicyKind } from '../constants';
import { policyTemplate } from '../templates';
import { useCreatePolicy } from '../usePolicyMutations';
import { KIND_ICONS } from '../selectors';

export interface NewPolicyDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (policyId: string) => void;
}

/** Creates a policy from the template of its kind, as a draft or published as version 1. */
export function NewPolicyDialog({ open, onOpenChange, onCreated }: NewPolicyDialogProps) {
  const { t } = useTranslation('policies');
  const { can } = useAuth();
  const canPublish = can(PERMISSIONS.policyPublish);
  const [kind, setKind] = useState<PolicyKind>('rotation');
  const [name, setName] = useState('');
  const [publish, setPublish] = useState(false);
  const [comment, setComment] = useState('');
  const [touched, setTouched] = useState(false);
  const [wasOpen, setWasOpen] = useState(open);
  const create = useCreatePolicy();
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setName('');
      setPublish(false);
      setComment('');
      setTouched(false);
    }
  }

  const nameValid = NAME_PATTERN.test(name);
  const submit = () => {
    setTouched(true);
    if (!nameValid) return;
    create.mutate(
      { kind, yaml: policyTemplate(kind, name), publish: publish && canPublish, comment: comment.trim() },
      {
        onSuccess: (res) => {
          onOpenChange(false);
          if (res.policy) onCreated(res.policy.id);
        },
        onError: (err) => toast.error(t('toasts.createFailed'), { description: errorMessage(err, t) }),
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !create.isPending && onOpenChange(next)}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('create.title')}</DialogTitle>
          <DialogDescription>{t('create.description')}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <fieldset className="grid gap-2">
            <legend className="mb-1.5 text-sm font-medium">{t('fields.kind')}</legend>
            <div role="radiogroup" className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {POLICY_KINDS.map((k) => {
                const Icon = KIND_ICONS[k];
                const checked = kind === k;
                return (
                  <button
                    key={k}
                    type="button"
                    role="radio"
                    aria-checked={checked}
                    onClick={() => setKind(k)}
                    className={cn(
                      'grid gap-1 rounded-lg border p-3 text-left outline-none hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50',
                      checked && 'border-primary bg-primary/5',
                    )}
                  >
                    <span className="flex items-center gap-1.5 text-sm font-medium">
                      <Icon className="size-4" aria-hidden />
                      {t(`kinds.${k}`)}
                    </span>
                    <span className="text-xs text-muted-foreground">{t(`kindDescriptions.${k}`)}</span>
                  </button>
                );
              })}
            </div>
          </fieldset>
          <FormField
            label={t('fields.name')}
            required
            description={t('create.nameHint')}
            error={touched && !nameValid ? t('create.nameInvalid') : undefined}
          >
            <Input
              value={name}
              onChange={(e) => setName(e.target.value.trim())}
              onBlur={() => setTouched(true)}
              placeholder={`web-search-${kind}`}
              autoComplete="off"
              spellCheck={false}
              maxLength={64}
              className="font-mono"
              autoFocus
            />
          </FormField>
          <div className="grid gap-1.5">
            <span className="text-sm font-medium">{t('create.template')}</span>
            <CodeEditor
              value={policyTemplate(kind, name || 'NAME')}
              readOnly
              height={200}
              path="new-policy-template.yaml"
              aria-label={t('create.template')}
            />
          </div>
          <SimpleTooltip
            content={t('common:permission.missing', { permission: PERMISSIONS.policyPublish })}
            enabled={!canPublish}
          >
            <div className="w-fit">
              <FormField label={t('create.publish')} description={t('create.publishHint')} inline>
                <Switch checked={publish && canPublish} onCheckedChange={setPublish} disabled={!canPublish} />
              </FormField>
            </div>
          </SimpleTooltip>
          {publish && canPublish && (
            <FormField label={t('fields.comment')}>
              <Textarea
                value={comment}
                onChange={(e) => setComment(e.target.value)}
                rows={2}
                maxLength={1024}
              />
            </FormField>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={create.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending || (touched && !nameValid)}>
              {create.isPending && <LoaderCircleIcon className="animate-spin" />}
              {publish && canPublish ? t('create.submitPublish') : t('create.submitDraft')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
