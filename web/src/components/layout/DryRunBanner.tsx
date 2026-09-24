import { FlaskConical } from 'lucide-react';
import { Link } from 'react-router';
import { useSettings } from '@/api/hooks/useSettings';

/** Persistent banner while settings.dryRun is on (the safe default). */
export function DryRunBanner() {
  const { data } = useSettings();
  if (!data?.dryRun) return null;
  return (
    <div
      role="status"
      className="flex shrink-0 items-center justify-center gap-2 border-b border-warning/40 bg-warning/15 px-4 py-1.5 text-center text-[13px] text-fg-strong"
    >
      <FlaskConical aria-hidden width={15} height={15} className="shrink-0 text-warning" />
      <span>
        <strong className="font-semibold">Dry run is enabled</strong> — nothing will be deleted.{' '}
        <Link to="/settings/mediamanagement" className="font-semibold whitespace-nowrap text-accent-soft underline-offset-2 hover:underline">
          Media Management
        </Link>
      </span>
    </div>
  );
}
