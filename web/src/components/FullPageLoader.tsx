import { LoaderCircleIcon } from 'lucide-react';

export interface FullPageLoaderProps {
  label?: string;
}

/** Centered spinner filling the viewport (session boot, lazy route fallback). */
export function FullPageLoader({ label }: FullPageLoaderProps) {
  return (
    <div
      className="flex h-full min-h-[50vh] w-full items-center justify-center"
      role="status"
      aria-live="polite"
    >
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <LoaderCircleIcon className="size-4 animate-spin" aria-hidden />
        {label && <span>{label}</span>}
      </div>
    </div>
  );
}
