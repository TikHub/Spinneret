import { type DefaultError, type MutationMeta, type UseMutationOptions } from '@tanstack/react-query';

/** `meta` flag marking mutations whose variables or results hold credentials. */
export const SENSITIVE_MUTATION_META_KEY = 'sensitive';

/**
 * Options for mutations whose variables or results carry credentials or
 * secrets (passwords, token plaintext, secret values, proxy URLs with
 * passwords, channel secrets, identity payloads):
 *
 * - gcTime 0: React Query keeps finished mutations (variables, result and the
 *   mutationFn closure) in the MutationCache for five minutes by default, long
 *   after the form closed. With gcTime 0 a mutation is dropped as soon as no
 *   component observes it, i.e. when the form unmounts or submits again.
 * - retry false: a failed credential request is never replayed automatically.
 * - meta.sensitive: lets cache tooling (devtools, persisters) skip it.
 *
 * Usage: `useMutation(sensitiveMutation({ mutationFn, onSuccess, onError }))`.
 */
export function sensitiveMutation<
  TData = unknown,
  TError = DefaultError,
  TVariables = void,
  TContext = unknown,
>(
  options: UseMutationOptions<TData, TError, TVariables, TContext>,
): UseMutationOptions<TData, TError, TVariables, TContext> {
  return {
    ...options,
    gcTime: 0,
    retry: false,
    meta: { ...options.meta, [SENSITIVE_MUTATION_META_KEY]: true },
  };
}

/** Reports whether mutation meta marks the mutation as sensitive. */
export function isSensitiveMutation(meta: MutationMeta | undefined): boolean {
  return meta?.[SENSITIVE_MUTATION_META_KEY] === true;
}
