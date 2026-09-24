import { CircleAlert, RefreshCw } from 'lucide-react';
import { Component, type ErrorInfo, type ReactNode } from 'react';
import { isRouteErrorResponse, Link, useRouteError } from 'react-router';
import { Button } from '@/components/ui/Button';

/** Lazy chunk failed to load (usually a new version was deployed): a reload fixes it. */
export function isChunkLoadError(error: unknown): boolean {
  const msg = error instanceof Error ? `${error.name} ${error.message}` : String(error);
  return /Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module|ChunkLoadError/i.test(
    msg,
  );
}

export interface ErrorFallbackProps {
  error: unknown;
  onRetry?: () => void;
}

/** Full-area error message with Retry / Reload. */
export function ErrorFallback({ error, onRetry }: ErrorFallbackProps) {
  const chunk = isChunkLoadError(error);
  const message = error instanceof Error ? error.message : String(error ?? 'Unknown error');
  return (
    <div role="alert" className="flex h-full min-h-60 flex-col items-center justify-center gap-4 px-6 py-16 text-center">
      <div className="flex size-14 items-center justify-center rounded-full bg-danger/15 text-danger">
        <CircleAlert aria-hidden width={28} height={28} />
      </div>
      <div className="text-lg text-fg-strong">
        {chunk ? 'Dupearr was updated' : 'Something went wrong'}
      </div>
      <div className="max-w-xl text-sm break-words text-muted">
        {chunk ? 'Reload the page to get the latest version.' : message}
      </div>
      <div className="flex gap-2">
        {onRetry && !chunk && <Button onClick={onRetry}>Try Again</Button>}
        <Button variant="primary" icon={RefreshCw} onClick={() => window.location.reload()}>
          Reload
        </Button>
      </div>
    </div>
  );
}

interface ErrorBoundaryProps {
  children: ReactNode;
  /** When this value changes the boundary resets (e.g. the route pathname). */
  resetKey?: unknown;
  fallback?: (error: unknown, reset: () => void) => ReactNode;
}

interface ErrorBoundaryState {
  error: unknown;
  resetKey: unknown;
}

/** Catches render errors below it (pages), showing ErrorFallback. */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  override state: ErrorBoundaryState = { error: null, resetKey: this.props.resetKey };

  static getDerivedStateFromError(error: unknown): Partial<ErrorBoundaryState> {
    return { error: error ?? new Error('Unknown error') };
  }

  static getDerivedStateFromProps(props: ErrorBoundaryProps, state: ErrorBoundaryState): Partial<ErrorBoundaryState> | null {
    if (props.resetKey !== state.resetKey) return { error: null, resetKey: props.resetKey };
    return null;
  }

  override componentDidCatch(error: unknown, info: ErrorInfo) {
    console.error('UI error', error, info.componentStack);
  }

  reset = () => this.setState({ error: null });

  override render() {
    if (this.state.error) {
      return this.props.fallback
        ? this.props.fallback(this.state.error, this.reset)
        : <ErrorFallback error={this.state.error} onRetry={this.reset} />;
    }
    return this.props.children;
  }
}

/** Router `errorElement`: loader/render errors and unknown routes outside the layout. */
export function RouteErrorPage() {
  const error = useRouteError();
  if (isRouteErrorResponse(error) && error.status === 404) {
    return (
      <div className="flex h-dvh flex-col items-center justify-center gap-3 bg-page text-center">
        <div className="text-5xl font-light text-fg-strong">404</div>
        <div className="text-muted">Page not found</div>
        <Link to="/" className="text-accent-soft">
          Go to Duplicates
        </Link>
      </div>
    );
  }
  return (
    <div className="h-dvh bg-page">
      <ErrorFallback error={isRouteErrorResponse(error) ? new Error(`${error.status} ${error.statusText}`) : error} />
    </div>
  );
}
