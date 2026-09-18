import { useRouterState } from '@tanstack/react-router';
import { Component, type ErrorInfo, type ReactNode } from 'react';

import { AppErrorFallback } from '@/app/errors/AppErrorFallback';

interface BoundaryProps {
  children: ReactNode;
  resetKey: string;
}

interface BoundaryState {
  error: unknown;
  resetKey: string;
}

class KeyedErrorBoundary extends Component<BoundaryProps, BoundaryState> {
  override state: BoundaryState = { error: undefined, resetKey: this.props.resetKey };

  static getDerivedStateFromError(error: unknown): Partial<BoundaryState> {
    return { error: error ?? new Error('Unknown render error') };
  }

  static getDerivedStateFromProps(props: BoundaryProps, state: BoundaryState): Partial<BoundaryState> | null {
    // Navigating to another page clears the error.
    return props.resetKey !== state.resetKey ? { error: undefined, resetKey: props.resetKey } : null;
  }

  override componentDidCatch(error: unknown, info: ErrorInfo): void {
    console.error('Page render error', error, info.componentStack);
  }

  override render(): ReactNode {
    if (this.state.error !== undefined) {
      return (
        <AppErrorFallback error={this.state.error} onReset={() => this.setState({ error: undefined })} />
      );
    }
    return this.props.children;
  }
}

/** Error boundary around the routed page that resets on navigation. */
export function RouteErrorBoundary({ children }: { children: ReactNode }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  return <KeyedErrorBoundary resetKey={pathname}>{children}</KeyedErrorBoundary>;
}
