import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Suspense, useState } from 'react';
import { RouterProvider } from 'react-router';
import { isApiError } from '@/api/client';
import { PreferencesProvider } from '@/app/preferences';
import { LoadingIndicator } from '@/components/ui/Spinner';
import { ToastProvider } from '@/components/ui/Toast';
import { createAppRouter } from './router';

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // Live updates come over SSE (src/api/events.ts); don't hammer the server on focus.
        staleTime: 30_000,
        refetchOnWindowFocus: false,
        retry: (failureCount, error) => {
          // 4xx won't fix themselves (401 is handled globally by the AuthGate).
          if (isApiError(error) && error.status >= 400 && error.status < 500) return false;
          return failureCount < 2;
        },
      },
      mutations: {
        retry: false,
      },
    },
  });
}

export function App() {
  const [queryClient] = useState(createQueryClient);
  const [router] = useState(createAppRouter);
  return (
    <QueryClientProvider client={queryClient}>
      <PreferencesProvider>
        <ToastProvider>
          <Suspense fallback={<LoadingIndicator fill className="h-dvh bg-page" />}>
            <RouterProvider router={router} />
          </Suspense>
        </ToastProvider>
      </PreferencesProvider>
    </QueryClientProvider>
  );
}
