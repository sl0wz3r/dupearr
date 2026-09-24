import { Copy, SearchX } from 'lucide-react';
import { useLocation } from 'react-router';
import { PageBody, PageContent } from '@/components/page';
import { EmptyState, LinkButton } from '@/components/ui';

/** Unknown route inside the app shell. */
export default function NotFoundPage() {
  const { pathname } = useLocation();
  return (
    <PageContent title="Not Found">
      <PageBody>
        <EmptyState
          icon={SearchX}
          title="Page not found"
          description={
            <>
              Nothing lives at <code className="rounded bg-card-hover px-1 py-0.5 text-fg">{pathname}</code>.
            </>
          }
          action={
            <LinkButton to="/" variant="primary" icon={Copy}>
              Go to Duplicates
            </LinkButton>
          }
        />
      </PageBody>
    </PageContent>
  );
}
