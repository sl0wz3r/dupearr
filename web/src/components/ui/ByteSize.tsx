import { formatBytes, formatNumber } from '@/lib/format';

export interface ByteSizeProps {
  bytes: number | null | undefined;
  decimals?: number;
  className?: string;
  /** Text when bytes is null/undefined (default "-"). */
  fallback?: string;
}

/**
 * Human readable size ("4.2 GiB") with the exact byte count as a tooltip.
 */
export function ByteSize({ bytes, decimals = 1, className, fallback = '-' }: ByteSizeProps) {
  if (bytes === null || bytes === undefined) return <span className={className}>{fallback}</span>;
  return (
    <span className={className} title={`${formatNumber(bytes)} bytes`}>
      {formatBytes(bytes, decimals)}
    </span>
  );
}
