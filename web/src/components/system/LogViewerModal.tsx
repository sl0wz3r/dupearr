import { clsx } from 'clsx';
import { Download, RefreshCw, Search } from 'lucide-react';
import { useDeferredValue, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { errorMessage } from '@/api/client';
import { useLogFile } from '@/api/hooks';
import { Alert, IconButton, LoadingIndicator, Modal, TextInput, buttonClasses } from '@/components/ui';
import { formatNumber } from '@/lib/format';
import { LINE_LEVEL_CLASS, parseLogLines, type LogLine } from './logLevels';
import { logFileDownloadUrl } from './systemFormat';

/** Lines rendered at most (the newest are kept); the full file is always downloadable. */
export const MAX_RENDERED_LINES = 20_000;

/** Lines containing `query` (case-insensitive); all lines when the query is blank. */
export function filterLogLines(lines: readonly LogLine[], query: string): LogLine[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...lines];
  return lines.filter((l) => l.text.toLowerCase().includes(q));
}

/** Wraps case-insensitive matches of `query` in <mark>. */
function highlight(text: string, query: string): ReactNode {
  const q = query.trim();
  if (!q) return text;
  const lower = text.toLowerCase();
  const needle = q.toLowerCase();
  const out: ReactNode[] = [];
  let from = 0;
  let idx = lower.indexOf(needle, from);
  while (idx !== -1) {
    if (idx > from) out.push(text.slice(from, idx));
    out.push(
      <mark key={idx} className="rounded-sm bg-warning/40 text-inherit">
        {text.slice(idx, idx + needle.length)}
      </mark>,
    );
    from = idx + needle.length;
    idx = lower.indexOf(needle, from);
  }
  if (from < text.length) out.push(text.slice(from));
  return out;
}

export interface LogViewerModalProps {
  /** File to show; null closes the modal. */
  filename: string | null;
  onClose: () => void;
}

/**
 * Log file viewer: monospace, level-coloured lines, search (filters + highlights), refresh and
 * download. Scrolls to the newest entries (end of file) when loaded.
 */
export function LogViewerModal({ filename, onClose }: LogViewerModalProps) {
  const file = useLogFile(filename);
  const [query, setQuery] = useState('');
  const deferredQuery = useDeferredValue(query);
  const scrollRef = useRef<HTMLDivElement>(null);

  // Reset the search when another file is opened.
  useEffect(() => {
    setQuery('');
  }, [filename]);

  const lines = useMemo(() => parseLogLines(file.data ?? ''), [file.data]);
  const matches = useMemo(() => filterLogLines(lines, deferredQuery), [lines, deferredQuery]);
  const truncated = matches.length > MAX_RENDERED_LINES;
  const visible = truncated ? matches.slice(matches.length - MAX_RENDERED_LINES) : matches;

  // Jump to the end (newest entries) whenever new content arrives.
  useEffect(() => {
    const el = scrollRef.current;
    if (el && file.data !== undefined) el.scrollTop = el.scrollHeight;
  }, [file.data, filename]);

  const searching = deferredQuery.trim() !== '';

  return (
    <Modal
      open={filename !== null}
      onClose={onClose}
      title={filename ?? ''}
      size="full"
      bodyClassName="flex flex-col gap-3"
    >
      <div className="flex flex-wrap items-center gap-2">
        <div className="min-w-48 flex-1">
          <TextInput
            type="search"
            aria-label="Search log"
            placeholder="Search…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            prefix={<Search aria-hidden width={15} height={15} />}
          />
        </div>
        <span className="text-xs text-muted" aria-live="polite">
          {file.data === undefined
            ? ''
            : searching
              ? `${formatNumber(matches.length)} of ${formatNumber(lines.length)} lines`
              : `${formatNumber(lines.length)} lines`}
        </span>
        <IconButton
          icon={RefreshCw}
          label="Refresh"
          variant="default"
          spinning={file.isFetching}
          onClick={() => void file.refetch()}
        />
        {filename && (
          <a
            href={logFileDownloadUrl(filename)}
            download={filename}
            className={buttonClasses({ variant: 'default', size: 'md' })}
          >
            <Download aria-hidden width={16} height={16} />
            Download
          </a>
        )}
      </div>

      {file.isLoading ? (
        <LoadingIndicator message="Loading log file" />
      ) : file.isError ? (
        <Alert kind="error" title="Unable to load log file">
          {errorMessage(file.error)}
        </Alert>
      ) : (
        <>
          {truncated && (
            <Alert kind="info">
              Showing the last {formatNumber(MAX_RENDERED_LINES)} of {formatNumber(matches.length)} lines. Download the
              file to see everything.
            </Alert>
          )}
          <div
            ref={scrollRef}
            role="log"
            aria-label={`Contents of ${filename ?? 'log file'}`}
            tabIndex={0}
            className="max-h-[62dvh] min-h-40 overflow-auto rounded border border-border bg-page p-2 font-mono text-xs leading-5"
          >
            {visible.length === 0 ? (
              <div className="p-4 text-center text-muted">{searching ? 'No matching lines.' : 'The log file is empty.'}</div>
            ) : (
              visible.map((l) => (
                <div
                  key={l.number}
                  data-level={l.level ?? undefined}
                  className={clsx('flex gap-3 whitespace-pre-wrap break-all', l.level ? LINE_LEVEL_CLASS[l.level] : 'text-fg')}
                >
                  <span aria-hidden className="w-12 shrink-0 text-right text-subtle select-none">
                    {l.number}
                  </span>
                  <span className="min-w-0 flex-1">{highlight(l.text, deferredQuery) || ' '}</span>
                </div>
              ))
            )}
          </div>
        </>
      )}
    </Modal>
  );
}
