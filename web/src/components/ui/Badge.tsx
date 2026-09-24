import { clsx } from 'clsx';
import type { LucideIcon } from 'lucide-react';
import type { ReactNode } from 'react';
import type { ActionStatus, CommandStatus, Decision, GroupFlag, GroupStatus, HealthType } from '@/api/types';
import {
  ACTION_STATUS_KIND,
  ACTION_STATUS_LABELS,
  COMMAND_STATUS_KIND,
  COMMAND_STATUS_LABELS,
  DECISION_KIND,
  DECISION_LABELS,
  GROUP_FLAG_DESCRIPTIONS,
  GROUP_FLAG_KIND,
  GROUP_FLAG_LABELS,
  GROUP_STATUS_DESCRIPTIONS,
  GROUP_STATUS_KIND,
  GROUP_STATUS_LABELS,
  HEALTH_TYPE_KIND,
  HEALTH_TYPE_LABELS,
  labelOf,
  type StatusKind,
} from '@/lib/constants';

export type BadgeKind = StatusKind;

const SOLID: Record<BadgeKind, string> = {
  default: 'bg-neutral text-white border-neutral',
  primary: 'bg-primary text-white border-primary',
  accent: 'bg-accent text-white border-accent',
  success: 'bg-success text-white border-success',
  warning: 'bg-warning text-black border-warning',
  danger: 'bg-danger text-white border-danger',
  info: 'bg-info text-white border-info',
  pink: 'bg-pink text-white border-pink',
  inverse: 'bg-fg-strong text-page border-fg-strong',
};

const OUTLINE: Record<BadgeKind, string> = {
  default: 'text-muted border-border-strong',
  primary: 'text-primary border-primary/60 bg-primary/10',
  accent: 'text-accent-soft border-accent/60 bg-accent/10',
  success: 'text-success border-success/60 bg-success/10',
  warning: 'text-warning border-warning/60 bg-warning/10',
  danger: 'text-danger border-danger/60 bg-danger/10',
  info: 'text-info border-info/60 bg-info/10',
  pink: 'text-pink border-pink/60 bg-pink/10',
  inverse: 'text-fg-strong border-fg-strong',
};

export interface BadgeProps {
  kind?: BadgeKind;
  /** Outline (tinted) instead of solid. */
  outline?: boolean;
  size?: 'sm' | 'md';
  icon?: LucideIcon;
  /** Tooltip. */
  title?: string;
  className?: string;
  children: ReactNode;
}

/**
 * *arr-style label/badge.
 * @example <Badge kind="success">Keep</Badge>
 */
export function Badge({ kind = 'default', outline = false, size = 'sm', icon: Icon, title, className, children }: BadgeProps) {
  return (
    <span
      title={title}
      className={clsx(
        'inline-flex max-w-full items-center gap-1 rounded-sm border font-semibold whitespace-nowrap',
        size === 'sm' ? 'px-1.5 py-px text-[11px] leading-4' : 'px-2 py-0.5 text-xs leading-5',
        outline ? OUTLINE[kind] : SOLID[kind],
        className,
      )}
    >
      {Icon && <Icon aria-hidden width={12} height={12} className="shrink-0" />}
      <span className="truncate">{children}</span>
    </span>
  );
}

/** Alias matching *arr naming. */
export const Label = Badge;

// --- Domain badges -----------------------------------------------------------

type DomainBadgeProps = Omit<BadgeProps, 'kind' | 'children'>;

/** Duplicate group status (pending/review/…) with its colour + description tooltip. */
export function GroupStatusBadge({ status, ...rest }: DomainBadgeProps & { status: GroupStatus }) {
  return (
    <Badge kind={GROUP_STATUS_KIND[status] ?? 'default'} title={GROUP_STATUS_DESCRIPTIONS[status]} {...rest}>
      {labelOf(GROUP_STATUS_LABELS, status)}
    </Badge>
  );
}

/** Group flag (cross_library, duration_mismatch, …). */
export function GroupFlagBadge({ flag, ...rest }: DomainBadgeProps & { flag: GroupFlag }) {
  return (
    <Badge kind={GROUP_FLAG_KIND[flag] ?? 'default'} outline title={GROUP_FLAG_DESCRIPTIONS[flag]} {...rest}>
      {labelOf(GROUP_FLAG_LABELS, flag)}
    </Badge>
  );
}

export function DecisionBadge({ decision, ...rest }: DomainBadgeProps & { decision: Decision }) {
  return (
    <Badge kind={DECISION_KIND[decision] ?? 'default'} {...rest}>
      {labelOf(DECISION_LABELS, decision)}
    </Badge>
  );
}

export function ActionStatusBadge({ status, ...rest }: DomainBadgeProps & { status: ActionStatus }) {
  return (
    <Badge kind={ACTION_STATUS_KIND[status] ?? 'default'} {...rest}>
      {labelOf(ACTION_STATUS_LABELS, status)}
    </Badge>
  );
}

export function CommandStatusBadge({ status, ...rest }: DomainBadgeProps & { status: CommandStatus }) {
  return (
    <Badge kind={COMMAND_STATUS_KIND[status] ?? 'default'} {...rest}>
      {labelOf(COMMAND_STATUS_LABELS, status)}
    </Badge>
  );
}

export function HealthBadge({ type, ...rest }: DomainBadgeProps & { type: HealthType }) {
  return (
    <Badge kind={HEALTH_TYPE_KIND[type] ?? 'default'} {...rest}>
      {labelOf(HEALTH_TYPE_LABELS, type)}
    </Badge>
  );
}
