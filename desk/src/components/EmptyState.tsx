import type { ReactNode } from 'react';

interface Props {
  title: string;
  children?: ReactNode;
  /** A button or link. */
  action?: ReactNode;
}

/** "Nothing here yet", with what to do next. */
export function EmptyState({ title, children, action }: Props) {
  return (
    <div className="empty">
      <h2>{title}</h2>
      {children ? <p>{children}</p> : null}
      {action}
    </div>
  );
}
