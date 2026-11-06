import type { ReactNode } from 'react';

import { cx } from '../cx';
import type { Tone } from '../status';

interface Props {
  tone?: Tone;
  className?: string;
  children: ReactNode;
}

// Full literal class names: Tailwind finds a class by scanning the source, never by building a name.
const TONE_CLASS: Record<Tone, string> = {
  neutral: 'badge badge-neutral',
  success: 'badge badge-success',
  warning: 'badge badge-warning',
  danger: 'badge badge-danger',
  info: 'badge badge-info',
};

/** A small label. The text carries the meaning; the colour only supports it. */
export function Badge({ tone = 'neutral', className, children }: Props) {
  return <span className={cx(TONE_CLASS[tone], className)}>{children}</span>;
}
