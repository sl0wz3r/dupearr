import { clsx } from 'clsx';
import { Eye, EyeOff } from 'lucide-react';
import { useState, type FocusEvent } from 'react';
import { IconButton, PasswordInput, TextArea, type PasswordInputProps, type TextAreaProps } from '@/components/ui';
import { isMaskedSecret } from './connectionUtils';

/**
 * New value of a secret field after an edit. While the field still holds the "********" mask,
 * only the text the user actually typed or pasted is kept — appending to, inserting into or
 * deleting from the mask never produces a value that contains (part of) the mask. Edits of a real
 * (unmasked) value pass through unchanged.
 */
export function unmaskEdit(previous: string, next: string): string {
  if (!isMaskedSecret(previous) || next === previous) return next;
  let prefix = 0;
  while (prefix < previous.length && prefix < next.length && previous[prefix] === next[prefix]) prefix += 1;
  let suffix = 0;
  while (
    suffix < previous.length - prefix &&
    suffix < next.length - prefix &&
    previous[previous.length - 1 - suffix] === next[next.length - 1 - suffix]
  ) {
    suffix += 1;
  }
  return next.slice(prefix, next.length - suffix);
}

export interface SecretInputProps extends Omit<PasswordInputProps, 'value' | 'onChange'> {
  value: string;
  /** Receives the new value (see `unmaskEdit`). */
  onChange: (value: string) => void;
}

/**
 * Password input for stored secrets (tokens, API keys, passwords, notification secrets). The API
 * returns stored secrets as "********" and keeps the stored value when the mask is sent back
 * unchanged; typing into a masked field replaces the whole mask with what was typed.
 */
export function SecretInput({ value, onChange, onFocus, title, ...rest }: SecretInputProps) {
  const masked = isMaskedSecret(value);
  return (
    <PasswordInput
      {...rest}
      value={value}
      title={masked ? 'Stored value hidden — type to replace it' : title}
      onChange={(e) => onChange(unmaskEdit(value, e.target.value))}
      onFocus={(e: FocusEvent<HTMLInputElement>) => {
        // Keyboard focus: select the mask so it is visibly replaced by typing.
        if (isMaskedSecret(e.target.value)) e.target.select();
        onFocus?.(e);
      }}
    />
  );
}

export interface SecretTextAreaProps extends Omit<TextAreaProps, 'value' | 'onChange'> {
  value: string;
  /** Receives the new value (see `unmaskEdit`). */
  onChange: (value: string) => void;
}

/**
 * Multi-line secret (e.g. Apprise URLs, one per line). A single-line password input would strip
 * the line breaks of a pasted list, so this is a textarea whose characters are hidden (where the
 * browser supports it) until "Show" is pressed. Same "********" mask semantics as SecretInput.
 */
export function SecretTextArea({ value, onChange, onFocus, title, className, ...rest }: SecretTextAreaProps) {
  const [visible, setVisible] = useState(false);
  const masked = isMaskedSecret(value);
  return (
    <div className="relative w-full">
      <TextArea
        {...rest}
        value={value}
        autoComplete="off"
        spellCheck={false}
        title={masked ? 'Stored value hidden — type to replace it' : title}
        className={clsx('pr-10', !visible && '[-webkit-text-security:disc]', className)}
        onChange={(e) => onChange(unmaskEdit(value, e.target.value))}
        onFocus={(e: FocusEvent<HTMLTextAreaElement>) => {
          if (isMaskedSecret(e.target.value)) e.target.select();
          onFocus?.(e);
        }}
      />
      <span className="absolute top-1 right-1 flex items-center text-muted">
        <IconButton
          icon={visible ? EyeOff : Eye}
          label={visible ? 'Hide' : 'Show'}
          size="sm"
          onClick={() => setVisible((v) => !v)}
          tabIndex={-1}
        />
      </span>
    </div>
  );
}
