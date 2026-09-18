import { ChevronDownIcon, ChevronRightIcon } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { CopyButton } from '@/components/CopyButton';
import { cn } from '@/lib/utils';

export interface JsonViewProps {
  /** Any JSON-compatible value (objects from protobuf JsonValue, parsed payloads, ...). */
  value: unknown;
  /** Nodes deeper than this start collapsed. */
  collapseDepth?: number;
  /** Show a copy button with the pretty-printed JSON. */
  copyable?: boolean;
  /**
   * CSS max-height of the scrollable body (for example `24rem`). Without it the
   * block grows with its content; the copy button stays pinned either way.
   */
  maxHeight?: string;
  className?: string;
}

function stringify(value: unknown): string {
  try {
    return (
      JSON.stringify(value, (_key, v: unknown) => (typeof v === 'bigint' ? v.toString() : v), 2) ??
      'undefined'
    );
  } catch {
    return String(value);
  }
}

function Primitive({ value }: { value: unknown }) {
  if (value === null || value === undefined) return <span className="text-muted-foreground">null</span>;
  if (typeof value === 'string')
    return <span className="text-emerald-700 dark:text-emerald-400">"{value}"</span>;
  if (typeof value === 'number' || typeof value === 'bigint')
    return <span className="text-sky-700 dark:text-sky-400">{String(value)}</span>;
  if (typeof value === 'boolean')
    return <span className="text-amber-700 dark:text-amber-400">{String(value)}</span>;
  return <span>{String(value)}</span>;
}

interface NodeProps {
  name?: string;
  value: unknown;
  depth: number;
  collapseDepth: number;
  last: boolean;
}

function JsonNode({ name, value, depth, collapseDepth, last }: NodeProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(depth < collapseDepth);
  const isArray = Array.isArray(value);
  const isObject = value !== null && typeof value === 'object' && !isArray;
  const comma = last ? '' : ',';
  const label = name !== undefined && <span className="text-indigo-700 dark:text-indigo-300">"{name}"</span>;

  if (!isArray && !isObject) {
    return (
      <div className="whitespace-pre-wrap break-all" style={{ paddingLeft: depth * 16 }}>
        {label}
        {label && ': '}
        <Primitive value={value} />
        {comma}
      </div>
    );
  }

  const entries: Array<[string | undefined, unknown]> = isArray
    ? (value as unknown[]).map((v) => [undefined, v])
    : Object.entries(value as Record<string, unknown>);
  const [openBrace, closeBrace] = isArray ? ['[', ']'] : ['{', '}'];
  const summary = isArray
    ? t('json.items', { count: entries.length })
    : t('json.keys', { count: entries.length });

  if (entries.length === 0) {
    return (
      <div style={{ paddingLeft: depth * 16 }}>
        {label}
        {label && ': '}
        {openBrace}
        {closeBrace}
        {comma}
      </div>
    );
  }

  return (
    <div>
      <div className="flex items-start" style={{ paddingLeft: Math.max(0, depth * 16 - 16) }}>
        <button
          type="button"
          className="mr-0.5 flex size-4 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-muted"
          onClick={() => setOpen((o) => !o)}
          aria-label={open ? t('json.collapse') : t('json.expand')}
          aria-expanded={open}
        >
          {open ? <ChevronDownIcon className="size-3" /> : <ChevronRightIcon className="size-3" />}
        </button>
        <span>
          {label}
          {label && ': '}
          {openBrace}
          {!open && (
            <>
              <span className="px-1 text-muted-foreground">{summary}</span>
              {closeBrace}
              {comma}
            </>
          )}
        </span>
      </div>
      {open && (
        <>
          {entries.map(([key, child], index) => (
            <JsonNode
              key={key ?? index}
              name={key}
              value={child}
              depth={depth + 1}
              collapseDepth={collapseDepth}
              last={index === entries.length - 1}
            />
          ))}
          <div style={{ paddingLeft: depth * 16 }}>
            {closeBrace}
            {comma}
          </div>
        </>
      )}
    </div>
  );
}

/** Collapsible, syntax-colored JSON viewer. */
export function JsonView({ value, collapseDepth = 2, copyable = true, maxHeight, className }: JsonViewProps) {
  return (
    <div className={cn('relative rounded-md border bg-muted/30 font-mono text-xs leading-5', className)}>
      {copyable && (
        // Outside the scroller, so it stays reachable while the JSON scrolls.
        <CopyButton value={stringify(value)} className="absolute top-1.5 right-1.5 z-10 bg-muted/80" />
      )}
      <div className="overflow-auto p-3 pr-10" style={maxHeight ? { maxHeight } : undefined}>
        <JsonNode value={value} depth={0} collapseDepth={collapseDepth} last />
      </div>
    </div>
  );
}
