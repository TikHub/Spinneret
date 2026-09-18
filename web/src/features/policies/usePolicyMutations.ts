import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';

import { useAuth } from '@/app/auth/AuthContext';
import { policyClient } from '@/lib/clients';
import { errorMessage } from '@/lib/errors';

/** Invalidates everything that depends on policies (lists, bindings, resolved policies, breakers, sites). */
export function useInvalidatePolicies() {
  const queryClient = useQueryClient();
  return () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ['policies'] }),
      queryClient.invalidateQueries({ queryKey: ['breakers'] }),
      queryClient.invalidateQueries({ queryKey: ['sites'] }),
    ]);
}

export interface CreatePolicyInput {
  kind: string;
  yaml: string;
  publish: boolean;
  comment: string;
}

export function useCreatePolicy() {
  const { t } = useTranslation('policies');
  const { namespaceName } = useAuth();
  const invalidate = useInvalidatePolicies();
  return useMutation({
    mutationFn: (input: CreatePolicyInput) =>
      policyClient.createPolicy({ namespace: namespaceName ?? '', ...input }),
    onSuccess: (res, input) => {
      toast.success(input.publish ? t('toasts.createdPublished') : t('toasts.created'), {
        description: res.policy?.name,
      });
      void invalidate();
    },
  });
}

export function useSaveDraft() {
  const { t } = useTranslation('policies');
  const invalidate = useInvalidatePolicies();
  return useMutation({
    mutationFn: (input: { id: string; yaml: string }) => policyClient.saveDraft(input),
    onSuccess: () => {
      toast.success(t('toasts.draftSaved'));
      void invalidate();
    },
  });
}

export function usePublishPolicy() {
  const { t } = useTranslation('policies');
  const invalidate = useInvalidatePolicies();
  return useMutation({
    mutationFn: (input: { id: string; comment: string; expectedVersion: number }) =>
      policyClient.publishPolicy(input),
    onSuccess: (res) => {
      toast.success(t('toasts.published', { version: res.policy?.currentVersion ?? 0 }));
      void invalidate();
    },
  });
}

export function useRollbackPolicy() {
  const { t } = useTranslation('policies');
  const invalidate = useInvalidatePolicies();
  return useMutation({
    mutationFn: (input: { id: string; version: number; comment: string }) =>
      policyClient.rollbackPolicy(input),
    onSuccess: (res, input) => {
      toast.success(
        t('toasts.rolledBack', { from: input.version, version: res.policy?.currentVersion ?? 0 }),
      );
      void invalidate();
    },
  });
}

export function useDeletePolicy() {
  const { t } = useTranslation('policies');
  const invalidate = useInvalidatePolicies();
  return useMutation({
    mutationFn: (id: string) => policyClient.deletePolicy({ id }),
    onSuccess: () => {
      toast.success(t('toasts.deleted'));
      void invalidate();
    },
  });
}

export interface SetBindingInput {
  policyId: string;
  site: string;
  client: string;
  endpointGroup: string;
}

export function useSetBinding() {
  const { t } = useTranslation('policies');
  const invalidate = useInvalidatePolicies();
  return useMutation({
    mutationFn: (input: SetBindingInput) => policyClient.setBinding(input),
    onSuccess: () => {
      toast.success(t('toasts.bound'));
      void invalidate();
    },
  });
}

export function useDeleteBinding() {
  const { t } = useTranslation('policies');
  const invalidate = useInvalidatePolicies();
  return useMutation({
    mutationFn: (id: string) => policyClient.deleteBinding({ id }),
    onSuccess: () => {
      toast.success(t('toasts.unbound'));
      void invalidate();
    },
  });
}

export function useValidatePolicy() {
  const { t } = useTranslation('policies');
  return useMutation({
    mutationFn: (input: { kind: string; yaml: string }) => policyClient.validatePolicy(input),
    onError: (err) => toast.error(t('toasts.validateFailed'), { description: errorMessage(err, t) }),
  });
}
