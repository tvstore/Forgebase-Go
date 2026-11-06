import { ClipboardListIcon, DocTypeIcon, HouseIcon } from '../icons';
import { REPORTS_LABEL, groupByApp } from '../nav';
import type { ReportMeta } from '../reports';
import { homeHref, listHref, reportHref } from '../router';
import type { Route } from '../router';
import type { DocTypeSummary } from '../types';

interface Props {
  id: string;
  open: boolean;
  doctypes: DocTypeSummary[];
  /** The reports the user may run; the section is left out when there are none. */
  reports: ReportMeta[];
  route: Route;
  /** A link was followed (the drawer closes, also when the address does not change). */
  onNavigate: () => void;
}

/**
 * The DocTypes the user can read, grouped by app, each with its icon, and then the reports the
 * user may run. The current one is marked.
 */
export function Sidebar({ id, open, doctypes, reports, route, onNavigate }: Props) {
  const current =
    route.name === 'list' || route.name === 'new' || route.name === 'form' || route.name === 'import' ? route.doctype : null;
  return (
    <nav
      id={id}
      className={open ? 'sidebar open' : 'sidebar'}
      aria-label="Document types"
      onClick={(event) => {
        if (event.target instanceof Element && event.target.closest('a')) onNavigate();
      }}
    >
      <ul className="side-list">
        <li>
          <a className="side-link" href={homeHref()} aria-current={route.name === 'home' ? 'page' : undefined}>
            <HouseIcon className="icon" />
            <span>Home</span>
          </a>
        </li>
      </ul>
      {groupByApp(doctypes).map((group) => (
        <section key={group.app} aria-label={group.label}>
          <h2 className="side-heading">{group.label}</h2>
          <ul className="side-list">
            {group.doctypes.map((d) => (
              <li key={d.name}>
                <a
                  className="side-link"
                  href={listHref(d.name)}
                  aria-current={d.name === current ? (route.name === 'list' ? 'page' : 'true') : undefined}
                >
                  <DocTypeIcon name={d.icon} className="icon" />
                  <span>{d.label}</span>
                </a>
              </li>
            ))}
          </ul>
        </section>
      ))}
      {reports.length > 0 ? (
        <section aria-label={REPORTS_LABEL}>
          <h2 className="side-heading">{REPORTS_LABEL}</h2>
          <ul className="side-list">
            {reports.map((r) => (
              <li key={r.name}>
                <a
                  className="side-link"
                  href={reportHref(r.name)}
                  aria-current={route.name === 'report' && route.report === r.name ? 'page' : undefined}
                >
                  <ClipboardListIcon className="icon" />
                  <span>{r.label}</span>
                </a>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </nav>
  );
}
