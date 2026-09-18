import { Component, type ErrorInfo, type ReactNode } from 'react';

interface State {
  error: unknown;
}

/**
 * Last-resort boundary around the whole application. It renders without i18n
 * or theme context (those providers may be what failed).
 */
export class GlobalErrorBoundary extends Component<{ children: ReactNode }, State> {
  override state: State = { error: undefined };

  static getDerivedStateFromError(error: unknown): State {
    return { error: error ?? new Error('Unknown error') };
  }

  override componentDidCatch(error: unknown, info: ErrorInfo): void {
    console.error('Unhandled application error', error, info.componentStack);
  }

  override render(): ReactNode {
    if (this.state.error === undefined) return this.props.children;
    const message = this.state.error instanceof Error ? this.state.error.message : String(this.state.error);
    return (
      <div
        role="alert"
        style={{ fontFamily: 'system-ui, sans-serif', padding: '4rem 1.5rem', textAlign: 'center' }}
      >
        <h1 style={{ fontSize: '1.25rem', fontWeight: 600 }}>Something went wrong / 出错了</h1>
        <pre style={{ whiteSpace: 'pre-wrap', opacity: 0.7, fontSize: '0.8rem' }}>{message}</pre>
        <button type="button" onClick={() => window.location.reload()} style={{ marginTop: '1rem' }}>
          Reload / 重新加载
        </button>
      </div>
    );
  }
}
