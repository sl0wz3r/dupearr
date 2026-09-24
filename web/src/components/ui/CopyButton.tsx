import { Check, Copy } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { IconButton, type IconButtonSize } from './IconButton';

/** Copies text to the clipboard (with a textarea fallback for insecure contexts / plain http). */
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // fall through to the legacy path
  }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

export interface CopyButtonProps {
  /** Text to copy. */
  value: string;
  /** Accessible label (default "Copy to clipboard"). */
  label?: string;
  size?: IconButtonSize;
  className?: string;
}

/**
 * Icon button that copies `value` and shows a check mark for 2s.
 * @example <TextInput readOnly value={apiKey} suffix={<CopyButton value={apiKey} size="sm" />} />
 */
export function CopyButton({ value, label = 'Copy to clipboard', size = 'md', className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  return (
    <IconButton
      icon={copied ? Check : Copy}
      label={copied ? 'Copied' : label}
      size={size}
      className={copied ? `text-success! ${className ?? ''}` : className}
      onClick={async () => {
        if (await copyToClipboard(value)) {
          setCopied(true);
          clearTimeout(timer.current);
          timer.current = setTimeout(() => setCopied(false), 2000);
        }
      }}
    />
  );
}
