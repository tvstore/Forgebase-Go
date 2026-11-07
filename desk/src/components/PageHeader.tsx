import { createContext, useContext } from 'react';
import type { ReactNode } from 'react';

import type { Crumb } from '../nav';
import { Breadcrumbs } from './Breadcrumbs';

/** The breadcrumbs of the current route, provided by the layout (which knows the DocTypes). */
export const CrumbsContext = createContext<Crumb[]>([]);

interface Props {
  /** The `<h1>` (a form gives it a ref to move the focus to it). */
  title: ReactNode;
  /** Badges and the muted name next to the title. */
  meta?: ReactNode;
  /** Buttons and menus on the right. */
  actions?: ReactNode;
}

/**
 * The header of a page: breadcrumb, title (with its badges) and the actions. It is sticky under
 * the top bar on a wide screen and scrolls away on a phone (styles.css), so it never covers the
 * content; `scroll-padding-top` keeps a focused control from sliding under it.
 */
export function PageHeader({ title, meta, actions }: Props) {
  const crumbs = useContext(CrumbsContext);
  return (
    <header className="page-header">
      <Breadcrumbs crumbs={crumbs} />
      <div className="page-header-row">
        <div className="page-title">
          {title}
          {meta}
        </div>
        {actions ? <div className="page-actions">{actions}</div> : null}
      </div>
    </header>
  );
}
