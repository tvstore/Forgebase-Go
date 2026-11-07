import { useEffect, useId, useRef, useState } from 'react';
import type { ReactNode } from 'react';

import { breadcrumbs } from '../nav';
import { initials } from '../people';
import type { ReportMeta } from '../reports';
import { homeHref } from '../router';
import type { Route } from '../router';
import { commandKeyHint, commandKeyName, isCommandBarKey } from '../shortcut';
import type { DocTypeSummary } from '../types';
import { CommandBar } from './CommandBar';
import { MenuIcon, SearchIcon } from '../icons';
import { Logo } from './Logo';
import { MenuButton } from './MenuButton';
import { CrumbsContext } from './PageHeader';
import { Sidebar } from './Sidebar';

interface Props {
  username: string;
  route: Route;
  /** The DocTypes the user can read (empty until they are loaded). */
  doctypes: DocTypeSummary[];
  /** The reports the user may run (empty until they are loaded, or when there are none). */
  reports: ReportMeta[];
  onLogout: () => void;
  children: ReactNode;
}

/** `navigator.platform`, or the user-agent data's platform where the browser has it. */
function platform(): string {
  const data = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData;
  return data?.platform ?? navigator.platform ?? '';
}

/**
 * The frame of every page after login: a skip link, the top bar (logo, search field that opens the
 * command bar, user menu), the sidebar and the `main` landmark. Below 900 px the sidebar is behind
 * the menu button (`aria-expanded`) and closes when the page changes. Ctrl/Cmd+K opens the command
 * bar from anywhere.
 */
export function Layout({ username, route, doctypes, reports, onLogout, children }: Props) {
  const [open, setOpen] = useState(false);
  const [commandOpen, setCommandOpen] = useState(false);
  const sidebarId = useId();
  const main = useRef<HTMLElement>(null);
  const toggle = useRef<HTMLButtonElement>(null);
  const hint = commandKeyHint(platform());
  const searchLabel = `Search or jump to... (${commandKeyName(platform())})`;

  // A new page closes the drawer (state is adjusted while rendering, not in an effect).
  const pageKey =
    route.name === 'list' || route.name === 'new' || route.name === 'form' || route.name === 'import'
      ? `${route.name}/${route.doctype}`
      : route.name === 'report'
        ? `report/${route.report}`
        : route.name;
  const [seenPage, setSeenPage] = useState(pageKey);
  if (seenPage !== pageKey) {
    setSeenPage(pageKey);
    setOpen(false);
  }

  // An open drawer takes the focus (its first link) and gives it back to the Menu button on Escape.
  useEffect(() => {
    if (!open) return;
    document.getElementById(sidebarId)?.querySelector<HTMLElement>('a')?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      setOpen(false);
      toggle.current?.focus();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, sidebarId]);

  // Ctrl/Cmd+K opens the command bar (the browser's own use of the key is not wanted here). Not
  // while a question dialog is open: that is answered first.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (!isCommandBarKey(event)) return;
      event.preventDefault();
      if (document.querySelector('dialog[open]:not(.command)')) return;
      setCommandOpen(true);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  return (
    <div className="shell">
      <a
        className="skip-link"
        href="#content"
        onClick={(event) => {
          // The address is the router's: move the focus instead of following the link.
          event.preventDefault();
          main.current?.focus();
        }}
      >
        Skip to content
      </a>
      <header className="topbar">
        <button
          type="button"
          ref={toggle}
          className="btn btn-ghost nav-toggle"
          aria-expanded={open}
          aria-controls={sidebarId}
          onClick={() => setOpen((o) => !o)}
        >
          <MenuIcon size={18} className="icon" />
          <span className="nav-label">Menu</span>
        </button>
        <a className="brand" href={homeHref()}>
          <Logo />
          <span>Forgebase</span>
        </a>
        <button type="button" className="topsearch" aria-label={searchLabel} onClick={() => setCommandOpen(true)}>
          <SearchIcon size={16} className="icon" />
          <span className="topsearch-text" aria-hidden="true">
            Search or jump to...
          </span>
          <kbd aria-hidden="true">{hint}</kbd>
        </button>
        <button type="button" className="topsearch-icon" aria-label={searchLabel} onClick={() => setCommandOpen(true)}>
          <SearchIcon size={18} />
        </button>
        <MenuButton
          label={
            <>
              <span className="avatar" aria-hidden="true">
                {initials(username)}
              </span>
              <span className="user-name">{username}</span>
            </>
          }
          ariaLabel={`Account: ${username}`}
          className="btn btn-ghost user-button"
        >
          <button type="button" className="menu-item" onClick={onLogout}>
            Log out
          </button>
        </MenuButton>
      </header>
      <div className="body">
        {open ? <div className="backdrop" aria-hidden="true" onClick={() => setOpen(false)} /> : null}
        <Sidebar id={sidebarId} open={open} doctypes={doctypes} reports={reports} route={route} onNavigate={() => setOpen(false)} />
        <main id="content" ref={main} tabIndex={-1} className="content">
          <CrumbsContext.Provider value={breadcrumbs(route, doctypes, reports)}>{children}</CrumbsContext.Provider>
        </main>
      </div>
      {commandOpen ? <CommandBar doctypes={doctypes} reports={reports} onClose={() => setCommandOpen(false)} /> : null}
    </div>
  );
}
