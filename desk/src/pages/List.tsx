import { useEffect, useId, useMemo, useRef, useState } from 'react';

import { ApiError, createView, deleteView, getDocTypeMeta, listDocumentsWithTitles, listViews, updateView } from '../api';
import { Badge } from '../components/Badge';
import { ColumnsMenu } from '../components/ColumnsMenu';
import { ExportMenu } from '../components/ExportMenu';
import { Dialog } from '../components/Dialog';
import { EmptyState } from '../components/EmptyState';
import { FilterBar } from '../components/FilterBar';
import { PageHeader } from '../components/PageHeader';
import { SaveViewDialog } from '../components/SaveViewDialog';
import { Skeleton } from '../components/Skeleton';
import { SummaryPanel } from '../components/SummaryPanel';
import { useToast } from '../components/Toast';
import { ViewsMenu } from '../components/ViewsMenu';
import { filterChips, parseFilters, withoutFilter } from '../filters';
import { FunnelIcon, PlusIcon, SearchIcon, XIcon } from '../icons';
import { NO_TITLES } from '../linkTitles';
import type { LinkTitles } from '../linkTitles';
import {
  MODIFIED_KEY,
  NAME_KEY,
  OWNER_KEY,
  PAGE_SIZE,
  filterFields,
  formatCell,
  availableColumns,
  hasStatus,
  isNumericColumn,
  isStatusKey,
  listStateParams,
  nextSort,
  parseListState,
  rowTitle,
  searchField,
  selectColumns,
  showingText,
  sortText,
  sortableKeys,
  toggleColumn,
  withSearch,
} from '../listing';
import { initials } from '../people';
import { stateBadge } from '../status';
import { exactTime, relativeTime } from '../time';
import type { ListState } from '../listing';
import { formHref, importHref, listHref, navigate, newHref } from '../router';
import { canExport, canImport, exportFields, exportHref } from '../transfer';
import { withGroupFilter } from '../summary';
import type { GroupField } from '../summary';
import type { DocTypeMeta, DocumentRow } from '../types';
import {
  canChangeView,
  definitionOfState,
  differsFromView,
  isBareViewLink,
  parseViewState,
  stateOfView,
  viewTitle,
  withViewState,
} from '../views';
import type { SavedView, ViewState } from '../views';

interface Props {
  doctype: string;
  params: URLSearchParams;
  /** The signed-in user, who owns the views they save. */
  username: string;
  /** An Administrator may share a view and change a shared one. */
  isAdmin: boolean;
}

type MetaState = { doctype: string; meta?: DocTypeMeta; error?: string };
/**
 * The last answer. `rows` stay on screen (and keep the keyboard focus on the controls) while the
 * next request loads; `offset` is the offset they belong to; `error` is that of request `key`.
 */
type RowsState = {
  doctype: string;
  key: string;
  offset: number;
  rows?: DocumentRow[];
  /** The titles of the documents the rows link to (only those the user may read). */
  titles?: LinkTitles;
  /** When the answer arrived: the clock of the relative "Modified" times. */
  at?: Date;
  error?: string;
  /** The `code` of the API error, when it was one. */
  code?: string;
};

/** The views of a DocType; `rev` is the reload they answer. */
type ViewsState = { doctype: string; rev: number; views: SavedView[] };

const message = (e: unknown): string =>
  e instanceof ApiError ? e.message : 'Cannot reach the server.';

/** The text of a failed view action: the details of a refused definition, else the message. */
const viewMessage = (e: unknown): string =>
  e instanceof ApiError && e.details.length > 0 ? e.details.join(' ') : message(e);

/** How long after the last key the quick search is applied. */
const SEARCH_DELAY_MS = 300;

export function List({ doctype, params, username, isAdmin }: Props) {
  const [metaState, setMetaState] = useState<MetaState | null>(null);
  const [rowsState, setRowsState] = useState<RowsState | null>(null);

  useEffect(() => {
    let cancelled = false;
    getDocTypeMeta(doctype).then(
      (meta) => {
        if (!cancelled) setMetaState({ doctype, meta });
      },
      (e: unknown) => {
        if (!cancelled) setMetaState({ doctype, error: message(e) });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [doctype]);

  const meta = metaState?.doctype === doctype ? metaState.meta : undefined;
  const vs = useMemo(() => parseViewState(params), [params]);
  const columns = useMemo(() => (meta ? selectColumns(meta, vs.columns) : []), [meta, vs.columns]);
  const menuColumns = useMemo(() => (meta ? availableColumns(meta) : []), [meta]);
  const fields = useMemo(() => (meta ? filterFields(meta) : []), [meta]);
  const sortable = useMemo(() => (meta ? sortableKeys(meta) : []), [meta]);
  const state: ListState = useMemo(() => parseListState(params, sortable), [params, sortable]);
  const filtersText = state.filters;
  const sort = sortText(state.sort);
  const offset = state.offset;
  const search = meta ? searchField(meta) : { name: NAME_KEY, label: 'Name' };
  const requestFilters = withSearch(filtersText, state.q, search.name);
  // One key per request: while it differs from the stored result, the list is loading.
  const key = `${doctype}|${offset}|${sort}|${requestFilters ?? ''}`;

  useEffect(() => {
    if (!meta) return;
    let cancelled = false;
    // One more row than a page, to know whether there is a next page.
    listDocumentsWithTitles(doctype, {
      filters: requestFilters,
      sort,
      limit: PAGE_SIZE + 1,
      offset,
    }).then(
      ({ rows, titles }) => {
        if (!cancelled) setRowsState({ doctype, key, offset, rows, titles, at: new Date() });
      },
      (e: unknown) => {
        if (cancelled) return;
        // No rows: the old ones belong to another request and must not pass for this result.
        // The table (its sort buttons) and the paging controls stay mounted, so keyboard focus
        // stays where it is.
        setRowsState({ doctype, key, offset, error: message(e), ...(e instanceof ApiError ? { code: e.code } : {}) });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [meta, doctype, requestFilters, sort, offset, key]);

  // The views of this DocType: loaded again (`viewsRev`) after one is saved, updated or deleted.
  const [viewsState, setViewsState] = useState<ViewsState | null>(null);
  const [viewsRev, setViewsRev] = useState(0);
  useEffect(() => {
    let cancelled = false;
    listViews(doctype).then(
      (views) => {
        if (!cancelled) setViewsState({ doctype, rev: viewsRev, views });
      },
      () => {
        // No views (the menu then offers none; saving one reports its own error).
        if (!cancelled) setViewsState({ doctype, rev: viewsRev, views: [] });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [doctype, viewsRev]);
  const views = useMemo(() => (viewsState?.doctype === doctype ? viewsState.views : []), [viewsState, doctype]);
  const viewsFresh = viewsState?.doctype === doctype && viewsState.rev === viewsRev;
  const currentView = vs.view === null ? undefined : views.find((v) => v.id === vs.view);
  const mayChange = currentView ? canChangeView(currentView, username, isAdmin) : false;
  const differs = currentView ? differsFromView(state, vs.columns, currentView) : false;

  /** Goes to `next`; the view and the columns of the address stay unless `extra` says otherwise. */
  const go = (next: ListState, extra: ViewState = vs) =>
    navigate(listHref(doctype, withViewState(listStateParams(next), extra)));

  // An address with only `view=<id>` (a copied link) is the view: its filters, sort and columns
  // are put in the address. A view that is gone (deleted, or not shared with this user) is dropped.
  const bare = isBareViewLink(params);
  useEffect(() => {
    if (!viewsFresh || vs.view === null || !meta) return;
    const target = currentView
      ? (() => {
          const own = stateOfView(currentView);
          return bare
            ? listHref(
                doctype,
                withViewState(listStateParams({ offset: 0, sort: own.sort, filters: own.filters, q: '' }), {
                  columns: own.columns,
                  view: currentView.id,
                }),
              )
            : null;
        })()
      : listHref(doctype, withViewState(params, { ...vs, view: null }));
    if (target !== null && target !== window.location.hash) navigate(target, true);
  }, [viewsFresh, vs, meta, currentView, bare, doctype, params]);

  const applyView = (view: SavedView) => {
    const own = stateOfView(view);
    go({ offset: 0, sort: own.sort, filters: own.filters, q: '' }, { columns: own.columns, view: view.id });
  };

  const toast = useToast();
  const [dialog, setDialog] = useState<'save' | 'delete' | null>(null);
  const [busy, setBusy] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [viewError, setViewError] = useState<string | null>(null);
  const [summaryOpen, setSummaryOpen] = useState(false);
  const summaryButton = useRef<HTMLButtonElement>(null);
  const summaryId = useId();

  const saveView = async (label: string, shared: boolean) => {
    setBusy(true);
    setDialogError(null);
    try {
      const created = await createView(doctype, label, definitionOfState(state, vs.columns), shared);
      setViewError(null);
      setViewsRev((r) => r + 1);
      setDialog(null);
      toast('View saved');
      go(state, { columns: vs.columns, view: created.id });
    } catch (e) {
      setDialogError(viewMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const updateCurrent = async () => {
    if (!currentView || busy) return;
    setBusy(true);
    setViewError(null);
    try {
      await updateView(doctype, currentView.id, currentView.version, definitionOfState(state, vs.columns));
      toast('View updated');
    } catch (e) {
      setViewError(viewMessage(e));
    } finally {
      setViewsRev((r) => r + 1);
      setBusy(false);
    }
  };

  const deleteCurrent = async () => {
    if (!currentView) return;
    setBusy(true);
    setViewError(null);
    try {
      await deleteView(doctype, currentView.id, currentView.version);
      toast('View deleted');
      go(state, { columns: vs.columns, view: null });
    } catch (e) {
      setViewError(viewMessage(e));
    } finally {
      setDialog(null);
      setViewsRev((r) => r + 1);
      setBusy(false);
    }
  };

  // The search box: the text is local while typing and goes to the address a moment after the
  // last key (so it survives a reload and the back button); a change of the address (back button,
  // "Clear") is followed without losing the focus.
  const [typed, setTyped] = useState(state.q);
  const [seenQ, setSeenQ] = useState(state.q);
  if (seenQ !== state.q) {
    setSeenQ(state.q);
    setTyped(state.q);
  }
  useEffect(() => {
    if (typed === state.q) return;
    const timer = window.setTimeout(
      () => navigate(listHref(doctype, withViewState(listStateParams({ ...state, q: typed, offset: 0 }), vs))),
      SEARCH_DELAY_MS,
    );
    return () => window.clearTimeout(timer);
  }, [typed, state, doctype, vs]);

  const [filtersOpen, setFiltersOpen] = useState(filtersText !== null);
  const chips = useMemo(() => filterChips(filtersText, fields), [filtersText, fields]);
  const filtersId = useId();

  if (metaState?.doctype === doctype && metaState.error) {
    return (
      <div className="page">
        <PageHeader title={<h1>Cannot open this list</h1>} />
        <p role="alert" className="alert">
          {metaState.error}
        </p>
      </div>
    );
  }
  if (!meta) {
    return (
      <div className="page" aria-busy="true">
        <PageHeader title={<h1>Loading...</h1>} />
        <Skeleton lines={5} />
      </div>
    );
  }

  // The last answer for this DocType stays on screen while a new request loads.
  const answer = rowsState?.doctype === doctype ? rowsState : null;
  const loading = answer?.key !== key;
  const shown = answer?.rows?.slice(0, PAGE_SIZE) ?? [];
  const shownOffset = answer?.offset ?? offset;
  const hasNext = (answer?.rows?.length ?? 0) > PAGE_SIZE;
  const filtered = state.q.trim() !== '' || filtersText !== null;
  const mayCreate = meta.rights.create !== 'none';
  const mayImport = canImport(meta.rights);
  const mayExport = canExport(meta.rights);
  const empty = !!answer?.rows && shown.length === 0 && shownOffset === 0 && !answer.error;

  const titles = answer?.titles ?? NO_TITLES;
  const now = answer?.at ?? new Date(0);

  return (
    <div className="page">
      <PageHeader
        title={<h1>{meta.label}</h1>}
        meta={currentView ? <span className="view-label">{viewTitle(currentView)}</span> : undefined}
        actions={
          mayCreate || mayImport ? (
            <>
              {mayImport ? (
                <a className="btn" href={importHref(doctype)}>
                  Import
                </a>
              ) : null}
              {mayCreate ? (
                <a className="btn btn-primary" href={newHref(doctype)}>
                  <PlusIcon size={16} />
                  New
                </a>
              ) : null}
            </>
          ) : null
        }
      />

      <div className="list-card">
        <div className="toolbar">
          <div className="search-wrap">
            <SearchIcon size={16} className="icon" />
            <input
              type="search"
              className="search"
              aria-label={`Search ${meta.label} by ${search.label}`}
              placeholder={`Search by ${search.label.toLowerCase()}`}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
            />
          </div>
          {chips.length > 0 ? (
            <ul className="chips" aria-label="Active filters">
              {chips.map((chip) => (
                <li key={`${chip.index}-${chip.label}`} className="chip">
                  <span>{chip.label}</span>
                  <button
                    type="button"
                    aria-label={`Remove filter: ${chip.label}`}
                    onClick={() => go({ ...state, filters: withoutFilter(filtersText, chip.index, fields), offset: 0 })}
                  >
                    <XIcon size={14} />
                  </button>
                </li>
              ))}
            </ul>
          ) : null}
          <button
            type="button"
            className="pill"
            aria-expanded={filtersOpen}
            aria-controls={filtersId}
            onClick={() => setFiltersOpen((o) => !o)}
          >
            {chips.length > 0 ? <FunnelIcon size={14} /> : <PlusIcon size={14} />}
            Filter
          </button>
          <span className="list-count" role="status" aria-live="polite">
            {answer?.rows ? showingText(shownOffset, shown.length) : ''}
          </span>
          <div className="toolbar-actions">
            <ViewsMenu
              views={views}
              current={currentView}
              canChange={mayChange}
              differs={differs}
              onApply={applyView}
              onDefault={() => navigate(listHref(doctype))}
              onSave={() => {
                setDialogError(null);
                setDialog('save');
              }}
              onUpdate={() => void updateCurrent()}
              onDelete={() => setDialog('delete')}
            />
            <ColumnsMenu
              columns={menuColumns}
              shown={columns.filter((c) => c.key !== NAME_KEY).map((c) => c.key)}
              changed={vs.columns !== null}
              onToggle={(key) =>
                go(state, {
                  ...vs,
                  columns: toggleColumn(
                    meta,
                    columns.filter((c) => c.key !== NAME_KEY).map((c) => c.key),
                    key,
                  ),
                })
              }
              onReset={() => go(state, { ...vs, columns: null })}
            />
            {mayExport ? (
              <ExportMenu
                heading="Export this list"
                href={(format) =>
                  exportHref(doctype, format, {
                    filters: requestFilters,
                    sort,
                    fields: exportFields(
                      columns.map((c) => c.key),
                      meta.title_field,
                    ),
                  })
                }
                note="The current filters and sort, and the shown columns."
              />
            ) : null}
            <button
              type="button"
              ref={summaryButton}
              className="btn btn-small"
              aria-expanded={summaryOpen}
              aria-controls={summaryOpen ? summaryId : undefined}
              onClick={() => setSummaryOpen((open) => !open)}
            >
              Summary
            </button>
          </div>
        </div>
        {summaryOpen ? (
          <div id={summaryId}>
            <SummaryPanel
              doctype={doctype}
              meta={meta}
              filters={requestFilters}
              onPick={(field: GroupField, key: unknown) => {
                go({ ...state, filters: withGroupFilter(filtersText, field, key), offset: 0 });
                setSummaryOpen(false);
                summaryButton.current?.focus();
              }}
              onClose={() => {
                setSummaryOpen(false);
                summaryButton.current?.focus();
              }}
            />
          </div>
        ) : null}
        {filtersOpen ? (
          <div id={filtersId}>
            <FilterBar
              initialKey={filtersText ?? ''}
              fields={fields}
              initial={parseFilters(filtersText)}
              onApply={(next) => go({ ...state, filters: next, offset: 0 })}
            />
          </div>
        ) : null}

        {viewError ? (
          <p role="alert" className="alert">
            {viewError}
          </p>
        ) : null}

        {!loading && answer?.error && answer.code === 'invalid_query' && currentView ? (
          <div role="alert" className="alert stale-view">
            <p>This view refers to fields that no longer exist.</p>
            <p className="hint">
              Remove the filters that no longer apply, then update the view &mdash; or delete it.
            </p>
            <div className="stale-actions">
              <button
                type="button"
                className="btn btn-small"
                onClick={() => go({ ...state, filters: null, offset: 0 })}
              >
                Clear filters
              </button>
              {mayChange ? (
                <>
                  <button
                    type="button"
                    className="btn btn-small"
                    aria-disabled={!differs || busy || undefined}
                    title={differs ? undefined : 'Change the filters first'}
                    onClick={() => {
                      if (differs) void updateCurrent();
                    }}
                  >
                    Update view
                  </button>
                  <button type="button" className="btn btn-small btn-danger" onClick={() => setDialog('delete')}>
                    Delete view&hellip;
                  </button>
                </>
              ) : null}
            </div>
          </div>
        ) : !loading && answer?.error ? (
          <p role="alert" className="alert">
            {answer.error}
          </p>
        ) : null}

        {!answer ? (
          <div className="skeleton-pad" aria-busy="true">
            <Skeleton lines={6} />
          </div>
        ) : empty ? (
          filtered ? (
            <EmptyState
              title="No documents match"
              action={
                <button type="button" className="btn" onClick={() => go({ ...state, q: '', filters: null, offset: 0 })}>
                  Clear search and filters
                </button>
              }
            >
              Try a different search, or remove a filter.
            </EmptyState>
          ) : (
            <EmptyState
              title={`No ${meta.label} yet`}
              action={
                mayCreate ? (
                  <a className="btn btn-primary" href={newHref(doctype)}>
                    <PlusIcon size={16} />
                    New {meta.label}
                  </a>
                ) : undefined
              }
            >
              {mayCreate ? 'Create the first one to get started.' : 'There is nothing here to show you.'}
            </EmptyState>
          )
        ) : (
          <div className={loading ? 'table-wrap is-loading' : 'table-wrap'} aria-busy={loading}>
            <table className="list-table">
              <caption className="visually-hidden">{meta.label} documents</caption>
              <thead>
                <tr>
                  {columns.map((column) => {
                    const sorted = state.sort.field === column.key;
                    return (
                      <th
                        key={column.key}
                        scope="col"
                        className={
                          isStatusKey(column.key) ? 'col-status' : isNumericColumn(column) ? 'num' : undefined
                        }
                        aria-sort={sorted ? (state.sort.desc ? 'descending' : 'ascending') : undefined}
                      >
                        <button
                          type="button"
                          className="sort"
                          onClick={() => go({ ...state, sort: nextSort(state.sort, column.key), offset: 0 })}
                        >
                          {column.label}
                          {sorted ? <span aria-hidden="true">{state.sort.desc ? ' \u25BC' : ' \u25B2'}</span> : null}
                        </button>
                      </th>
                    );
                  })}
                </tr>
              </thead>
              <tbody>
                {shown.map((row, index) => {
                  const { title, name } = rowTitle(row, meta, titles);
                  const href = formHref(doctype, name);
                  return (
                    <tr
                      key={name || index}
                      className="row-link"
                      onClick={(event) => {
                        // A convenience for the mouse: the link in the first cell is the real control.
                        if (!(event.target instanceof Element && event.target.closest('a, button'))) navigate(href);
                      }}
                    >
                      {columns.map((column) =>
                        column.key === NAME_KEY ? (
                          <th key={NAME_KEY} scope="row">
                            <a href={href}>{title}</a>
                            {title !== name ? <span className="muted-name">{name}</span> : null}
                            {hasStatus(meta) ? (
                              <Badge tone={stateBadge(meta, row).tone} className="status-inline">
                                {stateBadge(meta, row).label}
                              </Badge>
                            ) : null}
                          </th>
                        ) : isStatusKey(column.key) ? (
                          <td key={column.key} className="col-status">
                            <Badge tone={stateBadge(meta, row).tone}>{stateBadge(meta, row).label}</Badge>
                          </td>
                        ) : column.key === OWNER_KEY ? (
                          <td key={OWNER_KEY}>
                            {typeof row[OWNER_KEY] === 'string' && row[OWNER_KEY] !== '' ? (
                              <span className="person">
                                <span className="avatar-sm" aria-hidden="true">
                                  {initials(row[OWNER_KEY])}
                                </span>
                                <span>{row[OWNER_KEY]}</span>
                              </span>
                            ) : null}
                          </td>
                        ) : column.key === MODIFIED_KEY ? (
                          <td key={MODIFIED_KEY} className="muted">
                            {typeof row[MODIFIED_KEY] === 'string' ? (
                              <time dateTime={row[MODIFIED_KEY]} title={exactTime(row[MODIFIED_KEY])}>
                                {relativeTime(row[MODIFIED_KEY], now)}
                              </time>
                            ) : null}
                          </td>
                        ) : (
                          <td key={column.key} className={isNumericColumn(column) ? 'num' : undefined}>
                            {formatCell(row[column.key], column.type, column.target, titles)}
                          </td>
                        ),
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <p className="visually-hidden" role="status" aria-live="polite">
          {loading ? 'Loading...' : ''}
        </p>

        <nav className="paging" aria-label="Pages">
          {/* aria-disabled, not disabled: the button that was just used may become unavailable
              (the first or last page) and must keep the keyboard focus. */}
          {/* Not while a request is running: `hasNext` and the rows still belong to the previous
              request, so a click then could go past the last page. */}
          <button
            type="button"
            className="btn btn-small"
            aria-disabled={offset === 0 || loading}
            onClick={() => {
              if (offset > 0 && !loading) go({ ...state, offset: Math.max(0, offset - PAGE_SIZE) });
            }}
          >
            Previous
          </button>
          <button
            type="button"
            className="btn btn-small"
            aria-disabled={!hasNext || loading}
            onClick={() => {
              if (hasNext && !loading) go({ ...state, offset: offset + PAGE_SIZE });
            }}
          >
            Next
          </button>
        </nav>
      </div>

      {dialog === 'save' ? (
        <SaveViewDialog
          doctype={meta.label}
          canShare={isAdmin}
          busy={busy}
          error={dialogError}
          onSave={(label, shared) => void saveView(label, shared)}
          onCancel={() => setDialog(null)}
        />
      ) : null}
      {dialog === 'delete' && currentView ? (
        <Dialog
          title={`Delete the view \u2018${currentView.label}\u2019?`}
          confirmLabel="Delete view"
          cancelLabel="Cancel"
          danger
          onConfirm={() => void deleteCurrent()}
          onCancel={() => setDialog(null)}
        >
          {currentView.shared
            ? 'The view disappears for everyone who can see it. The documents are not changed.'
            : 'The documents are not changed.'}
        </Dialog>
      ) : null}
    </div>
  );
}
