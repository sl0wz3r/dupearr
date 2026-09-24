import { BookOpen, Bug, ExternalLink, FolderGit2, type LucideIcon } from 'lucide-react';
import { Card } from '@/components/ui';
import { DOCS_URL, ISSUES_URL, REPOSITORY_URL } from './systemFormat';

const LINKS: { label: string; description: string; href: string; icon: LucideIcon }[] = [
  { label: 'Source', description: 'Repository, releases and changelog', href: REPOSITORY_URL, icon: FolderGit2 },
  { label: 'Documentation', description: 'Setup, settings and troubleshooting', href: DOCS_URL, icon: BookOpen },
  { label: 'Feedback', description: 'Report a bug or request a feature', href: ISSUES_URL, icon: Bug },
];

/** System → Status → More Info: project links. */
export function MoreInfoCard() {
  return (
    <Card title="More Info">
      <ul className="m-0 grid list-none grid-cols-1 gap-2 p-0 sm:grid-cols-3">
        {LINKS.map(({ label, description, href, icon: Icon }) => (
          <li key={label}>
            <a
              href={href}
              target="_blank"
              rel="noopener noreferrer"
              className="group flex h-full items-start gap-3 rounded border border-border p-3 text-fg no-underline transition-colors hover:border-border-strong hover:bg-card-hover"
            >
              <Icon aria-hidden width={18} height={18} className="mt-0.5 shrink-0 text-accent-soft" />
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-1.5 font-semibold text-fg-strong">
                  {label}
                  <ExternalLink aria-hidden width={12} height={12} className="text-muted" />
                </span>
                <span className="block text-xs text-muted">{description}</span>
              </span>
            </a>
          </li>
        ))}
      </ul>
    </Card>
  );
}
