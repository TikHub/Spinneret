/** Chart series colors (hex: the canvas renderer does not understand oklch). */
export const CHART_COLORS = {
  light: ['#4f46e5', '#059669', '#e11d48', '#d97706', '#0284c7', '#7c3aed', '#64748b'],
  dark: ['#818cf8', '#34d399', '#fb7185', '#fbbf24', '#38bdf8', '#a78bfa', '#94a3b8'],
} as const;

/** Semantic colors for well-known series. */
export const SEMANTIC_CHART_COLORS = {
  success: { light: '#059669', dark: '#34d399' },
  risk: { light: '#e11d48', dark: '#fb7185' },
  warning: { light: '#d97706', dark: '#fbbf24' },
  primary: { light: '#4f46e5', dark: '#818cf8' },
  neutral: { light: '#64748b', dark: '#94a3b8' },
} as const;

export const CHART_TEXT = {
  light: {
    text: '#475569',
    axis: '#cbd5e1',
    split: '#e2e8f0',
    tooltipBg: '#ffffff',
    tooltipBorder: '#e2e8f0',
  },
  dark: {
    text: '#94a3b8',
    axis: '#334155',
    split: '#1e293b',
    tooltipBg: '#0f172a',
    tooltipBorder: '#334155',
  },
} as const;
