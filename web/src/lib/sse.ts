import { useEffect, useLayoutEffect, useRef, useState } from 'react';

/** Connection status reported by useEventSource. */
export type SseStatus = 'idle' | 'connecting' | 'open' | 'reconnecting';

/** One received server-sent event. */
export interface SseMessage {
  /** Event name ("message" for unnamed events). */
  type: string;
  /** Parsed JSON payload, or the raw string when it is not JSON. */
  data: unknown;
  lastEventId: string;
}

export interface EventSourceOptions {
  /** Named events to listen to in addition to unnamed "message" events. */
  eventTypes?: readonly string[];
  withCredentials?: boolean;
  /** First reconnect delay in milliseconds. */
  minRetryMs?: number;
  /** Maximum reconnect delay in milliseconds. */
  maxRetryMs?: number;
}

export const DEFAULT_MIN_RETRY_MS = 1_000;
export const DEFAULT_MAX_RETRY_MS = 30_000;

/** Exponential backoff with "equal jitter": a delay in [base/2, base], base = min * 2^attempt capped at max. */
export function computeBackoff(
  attempt: number,
  minMs: number = DEFAULT_MIN_RETRY_MS,
  maxMs: number = DEFAULT_MAX_RETRY_MS,
  random: () => number = Math.random,
): number {
  const base = Math.min(maxMs, minMs * 2 ** Math.max(0, attempt));
  return Math.round(base / 2 + (random() * base) / 2);
}

/** Parses an SSE data field as JSON, falling back to the raw string. */
export function parseSseData(raw: string): unknown {
  try {
    return JSON.parse(raw) as unknown;
  } catch {
    return raw;
  }
}

interface ConnectionCallbacks {
  onMessage: (message: SseMessage) => void;
  onStatus: (status: SseStatus) => void;
}

/** EventSource wrapper that reconnects with backoff after errors. */
export class ReconnectingEventSource {
  private source: EventSource | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private attempt = 0;
  private closed = false;
  private readonly url: string;
  private readonly options: EventSourceOptions;
  private readonly callbacks: ConnectionCallbacks;

  constructor(url: string, options: EventSourceOptions, callbacks: ConnectionCallbacks) {
    this.url = url;
    this.options = options;
    this.callbacks = callbacks;
  }

  start(): void {
    this.closed = false;
    this.connect();
  }

  close(): void {
    this.closed = true;
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = undefined;
    this.source?.close();
    this.source = undefined;
  }

  private connect(): void {
    if (this.closed) return;
    this.callbacks.onStatus(this.attempt === 0 ? 'connecting' : 'reconnecting');
    const source = new EventSource(this.url, { withCredentials: this.options.withCredentials ?? false });
    this.source = source;
    const handle = (event: MessageEvent<string>) => {
      this.callbacks.onMessage({
        type: event.type,
        data: parseSseData(event.data),
        lastEventId: event.lastEventId,
      });
    };
    source.onopen = () => {
      this.attempt = 0;
      this.callbacks.onStatus('open');
    };
    source.onmessage = handle;
    for (const type of this.options.eventTypes ?? []) {
      source.addEventListener(type, handle as EventListener);
    }
    source.onerror = () => {
      // Always take over reconnection so HTTP errors (which close the source) back off too.
      source.close();
      if (this.closed) return;
      const delay = computeBackoff(
        this.attempt,
        this.options.minRetryMs ?? DEFAULT_MIN_RETRY_MS,
        this.options.maxRetryMs ?? DEFAULT_MAX_RETRY_MS,
      );
      this.attempt += 1;
      this.callbacks.onStatus('reconnecting');
      this.timer = setTimeout(() => this.connect(), delay);
    };
  }
}

/**
 * Subscribes to a server-sent event stream while `url` is set. The handler may
 * change between renders without reconnecting; changing the URL or event types
 * reconnects.
 */
export function useEventSource(
  url: string | null | undefined,
  onMessage: (message: SseMessage) => void,
  options: EventSourceOptions = {},
): SseStatus {
  const [status, setStatus] = useState<SseStatus>('idle');
  const handlerRef = useRef(onMessage);
  useLayoutEffect(() => {
    handlerRef.current = onMessage;
  });

  const typesKey = (options.eventTypes ?? []).join(',');
  const { withCredentials, minRetryMs, maxRetryMs } = options;

  useEffect(() => {
    if (!url || typeof EventSource === 'undefined') return undefined;
    const connection = new ReconnectingEventSource(
      url,
      { eventTypes: typesKey ? typesKey.split(',') : [], withCredentials, minRetryMs, maxRetryMs },
      { onMessage: (m) => handlerRef.current(m), onStatus: setStatus },
    );
    connection.start();
    return () => connection.close();
  }, [url, typesKey, withCredentials, minRetryMs, maxRetryMs]);

  return url ? status : 'idle';
}
