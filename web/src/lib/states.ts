/** Color tones used for lifecycle states (spec section 5). */
export type StateTone = 'emerald' | 'sky' | 'amber' | 'rose' | 'orange' | 'zinc' | 'stone' | 'red' | 'indigo';
export type StateKind = 'identity' | 'proxy' | 'breaker' | 'account' | 'site';

const STATE_TONES: Record<StateKind, Record<string, StateTone>> = {
  identity: {
    pending: 'sky',
    active: 'emerald',
    expired: 'amber',
    banned: 'rose',
    quarantined: 'orange',
    disabled: 'zinc',
    retired: 'stone',
  },
  proxy: {
    active: 'emerald',
    disabled: 'zinc',
    dead: 'red',
    banned: 'rose',
    quarantined: 'orange',
    retired: 'stone',
  },
  breaker: {
    closed: 'emerald',
    half_open: 'amber',
    open: 'rose',
  },
  account: {
    active: 'emerald',
    banned: 'rose',
    disabled: 'zinc',
  },
  site: {
    active: 'emerald',
    paused: 'amber',
  },
};

/** Tone of a state; unknown states are neutral. */
export function stateTone(kind: StateKind, state: string): StateTone {
  return STATE_TONES[kind][state] ?? 'zinc';
}

/** Solid background class per tone (dots, stacked bars). */
export const STATE_TONE_DOT_CLASS: Record<StateTone, string> = {
  emerald: 'bg-emerald-500',
  sky: 'bg-sky-500',
  amber: 'bg-amber-500',
  rose: 'bg-rose-500',
  orange: 'bg-orange-500',
  zinc: 'bg-zinc-400',
  stone: 'bg-stone-400',
  red: 'bg-red-600',
  indigo: 'bg-indigo-500',
};
