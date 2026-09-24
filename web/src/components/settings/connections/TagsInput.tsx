import { clsx } from 'clsx';
import { X } from 'lucide-react';
import { useState, type KeyboardEvent } from 'react';

/** Max length of one tag (longer input is truncated). */
const MAX_TAG_LENGTH = 64;

/** Splits raw text on commas, trims, drops empties and duplicates (case-insensitive) of `existing`. */
export function parseTags(raw: string, existing: readonly string[] = []): string[] {
  const seen = new Set(existing.map((t) => t.toLowerCase()));
  const out: string[] = [];
  for (const part of raw.split(',')) {
    const tag = part.trim().slice(0, MAX_TAG_LENGTH);
    if (!tag || seen.has(tag.toLowerCase())) continue;
    seen.add(tag.toLowerCase());
    out.push(tag);
  }
  return out;
}

export interface TagsInputProps {
  value: readonly string[];
  onChange: (tags: string[]) => void;
  id?: string;
  placeholder?: string;
  disabled?: boolean;
  'aria-label'?: string;
}

/**
 * Free-form tag list: type and press Enter or comma to add, Backspace on an empty field removes the
 * last tag, pending text is added on blur.
 */
export function TagsInput({ value, onChange, id, placeholder = 'Add a tag…', disabled, ...aria }: TagsInputProps) {
  const [draft, setDraft] = useState('');

  const commit = () => {
    const added = parseTags(draft, value);
    if (added.length > 0) onChange([...value, ...added]);
    setDraft('');
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault();
      commit();
    } else if (e.key === 'Backspace' && draft === '' && value.length > 0) {
      e.preventDefault();
      onChange(value.slice(0, -1));
    }
  };

  return (
    <div
      className={clsx(
        'flex min-h-9 w-full flex-wrap items-center gap-1.5 rounded border border-input-border bg-input px-2 py-1',
        'focus-within:border-accent',
        disabled && 'opacity-55',
      )}
    >
      {value.map((tag) => (
        <span
          key={tag}
          className="inline-flex items-center gap-1 rounded-sm bg-accent/15 px-1.5 py-0.5 text-xs text-accent-soft"
        >
          {tag}
          {!disabled && (
            <button
              type="button"
              aria-label={`Remove tag ${tag}`}
              className="rounded-sm text-muted hover:text-fg-strong"
              onClick={() => onChange(value.filter((t) => t !== tag))}
            >
              <X aria-hidden width={12} height={12} />
            </button>
          )}
        </span>
      ))}
      <input
        id={id}
        type="text"
        value={draft}
        disabled={disabled}
        placeholder={value.length === 0 ? placeholder : ''}
        aria-label={aria['aria-label']}
        onChange={(e) => {
          // Pasting "a, b, c" adds all complete tags at once.
          const next = e.target.value;
          if (next.includes(',')) {
            const parts = next.split(',');
            const tail = parts.pop() ?? '';
            const added = parseTags(parts.join(','), value);
            if (added.length > 0) onChange([...value, ...added]);
            setDraft(tail);
          } else {
            setDraft(next);
          }
        }}
        onKeyDown={onKeyDown}
        onBlur={commit}
        className="min-w-24 flex-1 border-0 bg-transparent py-1 text-sm text-fg outline-none placeholder:text-subtle"
      />
    </div>
  );
}
