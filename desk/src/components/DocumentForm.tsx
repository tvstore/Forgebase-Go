import { useEffect, useId, useMemo, useRef, useState } from 'react';
import type { FormEvent } from 'react';

import {
  ApiError,
  createDocument,
  deleteDocument,
  getDocument,
  getDocumentWithTitles,
  getHistory,
  getTransitions,
  transitionDocument,
  updateDocument,
  workflowTransition,
} from '../api';
import type { AvailableAction, Transition } from '../api';
import { docStatusLabel, docStatusOf, fieldReadOnly, hasEditableField, hasEditableRequired, headerActions } from '../access';
import type { DocContext, LifecycleAction } from '../access';
import { conflictMessage, isStaleCode, mapDetails } from '../errors';
import { blankState, buildChanges, buildCreate, changedFields, emptyValue, errorKey, stateOf } from '../fields';
import type { Errors, FormState, FormValue } from '../fields';
import type { LinkTitles } from '../linkTitles';
import { recordRecent } from '../recent';
import { formHref, listHref, navigate, setNavigationBlocker } from '../router';
import { rowTitle } from '../listing';
import { initials } from '../people';
import { saveShortcut } from '../shortcut';
import { stateBadge } from '../status';
import { exactTime, relativeTime } from '../time';
import type { DocTypeMeta, DocumentRow, FieldMeta } from '../types';
import { actionLayout, actionsAreCurrent } from '../workflow';
import { ActivityCard } from './ActivityCard';
import type { HistoryState } from './ActivityCard';
import { Badge } from './Badge';
import { Dialog } from './Dialog';
import { FieldControl } from './FieldControl';
import { MenuButton } from './MenuButton';
import { PageHeader } from './PageHeader';
import { ReadOnlyField } from './ReadOnlyValue';
import { TableGrid } from './TableGrid';
import { useToast } from './Toast';

interface Props {
  meta: DocTypeMeta;
  /** The stored document, or null for a new one. */
  initial: DocumentRow | null;
  /** The titles of the documents it links to (only those the user may read). */
  initialTitles: LinkTitles;
  username: string;
}

/** The question the form is asking in the page, if any. */
type Asking =
  | { kind: 'delete' }
  | { kind: 'cancel' }
  | { kind: 'amend' }
  | { kind: 'reload' }
  | { kind: 'workflow'; action: AvailableAction }
  | { kind: 'leave'; next: string }
  | null;

/** An error that is not about a field: shown in the alert at the top. */
interface Failure {
  message: string;
  details: string[];
}

/** The workflow actions this caller may take now; kept while a refresh runs, so the buttons do not flicker. */
type Available =
  | { status: 'loading' }
  | { status: 'ready'; version: number; state: string | null; actions: AvailableAction[] }
  | { status: 'error' };

const TRANSITION_VERB: Record<Transition, string> = {
  submit: 'submitted',
  cancel: 'cancelled',
  amend: 'amended',
};

const TRANSITION_DONE: Record<Transition, string> = {
  submit: 'Submitted',
  cancel: 'Cancelled',
  amend: 'Amended',
};

const ACTION_LABEL: Record<LifecycleAction, string> = {
  submit: 'Submit',
  cancel: 'Cancel',
  amend: 'Amend',
  delete: 'Delete',
};

/**
 * The form of one document: a native control per field of the metadata, Save, the lifecycle
 * buttons the user's rights and the document's status allow, and the messages. The state of the
 * controls is `FormState` (strings and booleans); the conversion to JSON and the change
 * detection are in fields.ts and the rules in access.ts (both tested without a DOM).
 */
export function DocumentForm({ meta, initial, initialTitles, username }: Props) {
  const [doc, setDoc] = useState<DocumentRow | null>(initial);
  const [titles, setTitles] = useState<LinkTitles>(initialTitles);
  // The clock of the relative times in the info panel; it moves on when the document is replaced.
  const [now, setNow] = useState(() => new Date());
  const [saved, setSaved] = useState<FormState>(() => (initial ? stateOf(meta, initial) : blankState(meta)));
  const [state, setState] = useState<FormState>(saved);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [asking, setAsking] = useState<Asking>(null);
  // The version in the form is known to be out of date (a conflict, or a status that moved on):
  // Reload stays offered until the current version is loaded.
  const [stale, setStale] = useState(false);
  // Counts the attempts that failed, so the focus moves each time, also to the same field.
  const [attempt, setAttempt] = useState(0);

  // The workflow (M8.2): what the caller may do now, and the log. Both are fetched again whenever
  // the document's version changes (open, save, action, reload) and after a refused action.
  const [available, setAvailable] = useState<Available>({ status: 'loading' });
  const [history, setHistory] = useState<HistoryState>({ status: 'loading' });
  const [refresh, setRefresh] = useState(0);

  const idBase = useId();
  const alertRef = useRef<HTMLDivElement>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const formRef = useRef<HTMLFormElement>(null);

  const isNew = doc === null;
  const hasWorkflow = (meta.workflow ?? null) !== null;
  const ctx: DocContext = {
    isNew,
    docStatus: docStatusOf(doc?.docstatus),
    rights: meta.rights,
    owner: typeof doc?.owner === 'string' ? doc.owner : null,
    username,
    submittable: meta.submittable,
    workflow: hasWorkflow,
  };
  const readOnlyOf = (field: FieldMeta): boolean => fieldReadOnly(ctx, meta, field);
  const changed = useMemo(() => changedFields(meta, saved, state), [meta, saved, state]);
  const dirty = changed.length > 0;
  const canSave = hasEditableField(ctx, meta);
  const header = headerActions(ctx, dirty, canSave);
  const name = typeof doc?.name === 'string' ? doc.name : null;
  const version = typeof doc?.version === 'number' ? doc.version : 0;

  useEffect(() => {
    if (!hasWorkflow || name === null) return;
    let cancelled = false;
    getTransitions(meta.name, name).then(
      (answer) => {
        if (!cancelled) setAvailable({ status: 'ready', version, state: answer.state, actions: answer.transitions });
      },
      (e: unknown) => {
        if (cancelled) return;
        if (e instanceof ApiError && e.status === 404) {
          // The document is gone (deleted or no longer readable): no actions, the same message as a failed save.
          setAvailable({ status: 'ready', version, state: null, actions: [] });
          setFailure({ message: e.message, details: [] });
        } else {
          setAvailable({ status: 'error' });
        }
      },
    );
    getHistory(meta.name, name).then(
      (entries) => {
        if (!cancelled) setHistory({ status: 'ready', entries });
      },
      () => {
        if (!cancelled) setHistory({ status: 'error' });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [hasWorkflow, meta.name, name, version, refresh]);

  // The form heading takes the focus when the page opens, so a screen reader announces where the
  // user arrived (the page is replaced without a page load).
  useEffect(() => {
    headingRef.current?.focus();
  }, []);

  // After a failed attempt: the first invalid field, else the alert.
  useEffect(() => {
    if (attempt === 0) return;
    const invalid = formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]');
    (invalid ?? alertRef.current)?.focus();
  }, [attempt]);

  // Unsaved changes: leaving by a change of the address asks first (in the page); closing or
  // reloading the tab uses the browser's own prompt.
  const dirtyRef = useRef(false);
  const leavingRef = useRef(false);
  useEffect(() => {
    dirtyRef.current = dirty;
  });
  useEffect(() => {
    setNavigationBlocker((next) => {
      if (leavingRef.current || !dirtyRef.current) return false;
      setAsking({ kind: 'leave', next });
      return true;
    });
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (dirtyRef.current && !leavingRef.current) event.preventDefault();
    };
    window.addEventListener('beforeunload', beforeUnload);
    return () => {
      setNavigationBlocker(null);
      window.removeEventListener('beforeunload', beforeUnload);
    };
  }, []);

  const leaveTo = (href: string) => {
    leavingRef.current = true;
    navigate(href);
  };

  const adopt = (next: DocumentRow) => {
    const fresh = stateOf(meta, next);
    setDoc(next);
    setSaved(fresh);
    setState(fresh);
    setErrors({});
    setFailure(null);
    setStale(false);
    setNow(new Date());
    // The titles of what the new version links to (a link may have changed). Best effort: without
    // them the form shows the names, as it did before.
    if (typeof next.name === 'string') {
      getDocumentWithTitles(meta.name, next.name).then(
        (fresh) => setTitles(fresh.titles),
        () => {},
      );
    }
  };

  /** `verb` completes "The document was not ..." (saved, submitted, ...). */
  const fail = (e: unknown, verb: string) => {
    setAsking(null);
    if (e instanceof ApiError) {
      if (e.status === 422 && e.code === 'validation_failed') {
        const mapped = mapDetails(e.details, meta);
        setErrors(mapped.errors);
        setFailure({ message: `The document was not ${verb}.`, details: mapped.unmapped });
      } else if (e.code === 'conflict') {
        setErrors({});
        setStale(true);
        setFailure({ message: conflictMessage(verb, dirty), details: [] });
      } else {
        setErrors({});
        if (isStaleCode(e.code)) setStale(true);
        setFailure({ message: e.message, details: e.details });
      }
    } else {
      setErrors({});
      setFailure({ message: 'Cannot reach the server. Try again.', details: [] });
    }
    setAttempt((n) => n + 1);
  };

  const run = async (verb: string, work: () => Promise<void>) => {
    setBusy(true);
    setFailure(null);
    setErrors({});
    try {
      await work();
    } catch (e) {
      fail(e, verb);
    } finally {
      setBusy(false);
    }
  };

  const save = (event?: FormEvent) => {
    event?.preventDefault();
    if (busy || !canSave) return;
    const built = isNew
      ? buildCreate(meta, state)
      : buildChanges(meta, saved, state, (f) => !readOnlyOf(f));
    if (!built.ok) {
      setErrors(built.errors);
      setFailure({ message: 'Fix the fields marked below. The document was not saved.', details: [] });
      setAttempt((n) => n + 1);
      return;
    }
    if (!isNew && Object.keys(built.data).length === 0) {
      toast('There are no changes to save.');
      return;
    }
    void run('saved', async () => {
      if (name === null) {
        const created = await createDocument(meta.name, built.data);
        leavingRef.current = true;
        toast('Saved');
        navigate(formHref(meta.name, String(created.name)));
      } else {
        const updated = await updateDocument(meta.name, name, version, built.data);
        adopt(updated);
        toast('Saved');
      }
    });
  };

  const transition = (action: Transition) => {
    if (busy || name === null) return;
    setAsking(null);
    void run(TRANSITION_VERB[action], async () => {
      const result = await transitionDocument(meta.name, name, action, version);
      toast(TRANSITION_DONE[action]);
      if (action === 'amend') {
        leavingRef.current = true;
        navigate(formHref(meta.name, String(result.name)));
        return;
      }
      adopt(result);
      // The button that was used is gone: put the focus on the heading, which names the document.
      headingRef.current?.focus();
    });
  };

  /** True (and says so) when there are unsaved changes: an action that saves nothing must not run over them. */
  const needSave = (): boolean => {
    if (!dirty) return false;
    setErrors({});
    setFailure({ message: 'You have unsaved changes. Save them first.', details: [] });
    setAttempt((n) => n + 1);
    return true;
  };

  /** Submit goes ahead; Cancel, Amend and Delete ask first. With unsaved changes all ask to save first. */
  const lifecycle = (action: LifecycleAction) => {
    if (busy || name === null || needSave()) return;
    if (action === 'submit') transition('submit');
    else setAsking({ kind: action });
  };

  /** A workflow action always asks first; with unsaved changes it asks to save first (its button is disabled then). */
  const askAction = (action: AvailableAction) => {
    if (busy || name === null || !actionsCurrent || needSave()) return;
    setAsking({ kind: 'workflow', action });
  };

  const takeAction = (action: AvailableAction) => {
    if (busy || name === null || !actionsCurrent) return;
    setAsking(null);
    void run('moved', async () => {
      try {
        const result = await workflowTransition(meta.name, name, action.action, version);
        toast(`${action.action}: ${String(result.workflow_state ?? action.to)}`);
        adopt(result);
        // The button that was used is gone: put the focus on the heading, which names the document.
        headingRef.current?.focus();
      } catch (e) {
        // The actions may have changed (another user moved the document, a role was removed).
        setRefresh((n) => n + 1);
        throw e;
      }
    });
  };

  const confirmDelete = () => {
    if (name === null) return;
    setAsking(null);
    void run('deleted', async () => {
      await deleteDocument(meta.name, name, version);
      leavingRef.current = true;
      navigate(listHref(meta.name));
    });
  };

  const reload = () => {
    if (name === null) return;
    setAsking(null);
    void run('reloaded', async () => {
      adopt(await getDocument(meta.name, name));
      toast('Loaded the current version');
      // The Reload button is gone: put the focus on the heading, as after a lifecycle action.
      headingRef.current?.focus();
    });
  };

  // Ctrl/Cmd+S saves (the browser's own "save page" dialog is not wanted here).
  const saveCallback = useRef<() => void>(() => {});
  useEffect(() => {
    saveCallback.current = () => save();
  });
  const shortcutState = { canSave, dirty, isNew, dialogOpen: asking !== null };
  const shortcutRef = useRef(shortcutState);
  useEffect(() => {
    shortcutRef.current = shortcutState;
  });
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const action = saveShortcut(event, shortcutRef.current);
      if (action === 'ignore') return;
      event.preventDefault();
      if (action === 'save') saveCallback.current();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const setValue = (field: string, value: FormValue) => {
    setState((s) => ({ ...s, values: { ...s.values, [field]: value } }));
    setErrors((e) => (field in e ? Object.fromEntries(Object.entries(e).filter(([k]) => k !== field)) : e));
  };

  // The heading is the title field's value (as in the list), the name muted under it.
  const shown = doc === null ? null : rowTitle(doc, meta, titles);
  const title = shown === null ? `New ${meta.label}` : shown.title === '' ? meta.label : shown.title;
  const docName = name ?? 'this document';

  // Problems that no single control carries: shown in the alert next to the unmapped details.
  const tableLevel = meta.fields
    .filter((f) => f.type === 'table' && errors[errorKey(f.name)] !== undefined)
    .map((f) => `${f.label}: ${errors[errorKey(f.name)]}`);

  // The command bar lists the documents opened in this tab.
  const recentName = shown?.name;
  const recentTitle = shown?.title;
  useEffect(() => {
    if (recentName) recordRecent({ doctype: meta.name, name: recentName, title: recentTitle || recentName });
  }, [meta.name, recentName, recentTitle]);

  const badge = stateBadge(meta, doc ?? {});
  const layout = actionLayout(available.status === 'ready' ? available.actions : [], dirty);
  // Actions fetched for an older version are shown (the layout stays) but disabled until the new ones arrive.
  const fetchedFor = available.status === 'ready' ? available.version : null;
  const actionsCurrent = actionsAreCurrent(fetchedFor, version);
  const actionsOff = layout.blocked || !actionsCurrent;
  const saveFirstId = `${idBase}-save-first`;
  const fromState =
    (available.status === 'ready' && actionsCurrent ? available.state : null) ?? String(doc?.workflow_state ?? '');
  const movingName = shown !== null && shown.title !== '' ? shown.title : docName;

  /** A workflow action as a header button; while there are unsaved changes it is disabled and secondary. */
  const actionButton = (action: AvailableAction, primary: boolean) => (
    <button
      key={action.action}
      type="button"
      className={primary && !layout.blocked ? 'btn btn-primary' : 'btn'}
      aria-disabled={busy || actionsOff || undefined}
      aria-describedby={layout.blocked ? saveFirstId : undefined}
      title={layout.blocked ? 'Save first' : undefined}
      onClick={() => askAction(action)}
    >
      {action.action}
    </button>
  );
  const moreCount = layout.more.length + header.more.length;
  const detailsId = `${idBase}-details`;
  const columnFields = meta.fields.filter((f) => f.type !== 'table');
  const tableFields = meta.fields.filter((f) => f.type === 'table');
  const requiredShown = canSave && hasEditableRequired(ctx, meta);

  return (
    <div className="page">
      <p className="visually-hidden" role="status" aria-live="polite">
        {busy ? 'Working...' : ''}
      </p>
      <form ref={formRef} className="doc-form" noValidate onSubmit={save}>
        <PageHeader
          title={
            <h1 ref={headingRef} tabIndex={-1}>
              {title}
            </h1>
          }
          meta={
            <>
              {(meta.submittable || hasWorkflow) && !isNew ? (
                <Badge tone={badge.tone} className="status-badge">
                  {badge.label}
                </Badge>
              ) : null}
              {isNew || dirty ? <Badge tone="warning">{isNew ? 'Not saved' : 'Unsaved changes'}</Badge> : null}
              {shown !== null && shown.title !== shown.name ? <p className="doc-title-name">{shown.name}</p> : null}
            </>
          }
          actions={
            <>
              {header.save ? (
                <button
                  type="submit"
                  className={header.save === 'primary' ? 'btn btn-primary' : 'btn'}
                  aria-disabled={busy || undefined}
                >
                  Save
                </button>
              ) : null}
              {header.submit ? (
                <button
                  type="button"
                  className={header.submit === 'primary' ? 'btn btn-primary' : 'btn'}
                  aria-disabled={busy || undefined}
                  onClick={() => lifecycle('submit')}
                >
                  Submit
                </button>
              ) : null}
              {layout.primary ? actionButton(layout.primary, true) : null}
              {layout.secondary ? actionButton(layout.secondary, false) : null}
              {layout.blocked && layout.primary ? (
                <span id={saveFirstId} className="visually-hidden">
                  Save your changes first.
                </span>
              ) : null}
              {moreCount > 0 ? (
                <MenuButton label="More" ariaLabel="More actions">
                  {layout.more.map((action) => (
                    <button
                      key={action.action}
                      type="button"
                      className="menu-item"
                      aria-disabled={busy || actionsOff || undefined}
                      aria-describedby={layout.blocked ? saveFirstId : undefined}
                      title={layout.blocked ? 'Save first' : undefined}
                      onClick={() => askAction(action)}
                    >
                      {action.action}
                    </button>
                  ))}
                  {header.more.map((action) => (
                    <button
                      key={action}
                      type="button"
                      className={action === 'delete' ? 'menu-item menu-danger' : 'menu-item'}
                      aria-disabled={busy || undefined}
                      onClick={() => lifecycle(action)}
                    >
                      {ACTION_LABEL[action]}
                    </button>
                  ))}
                </MenuButton>
              ) : null}
            </>
          }
        />

        {hasWorkflow && !isNew && available.status === 'error' ? (
          <p className="hint workflow-error">
            Cannot load the available actions.{' '}
            <button type="button" className="btn btn-small" onClick={() => setRefresh((n) => n + 1)}>
              Retry
            </button>
          </p>
        ) : null}

        {failure ? (
          <div role="alert" tabIndex={-1} ref={alertRef} className="alert">
            <p>{failure.message}</p>
            {failure.details.length + tableLevel.length > 0 ? (
              <ul>
                {[...failure.details, ...tableLevel].map((d) => (
                  <li key={d}>{d}</li>
                ))}
              </ul>
            ) : null}
            {stale ? (
              <button
                type="button"
                className="btn btn-small"
                onClick={() => (dirty ? setAsking({ kind: 'reload' }) : reload())}
              >
                Reload
              </button>
            ) : null}
          </div>
        ) : null}

        {asking?.kind === 'delete' ? (
          <Dialog
            title={`Delete ${docName}?`}
            confirmLabel="Delete"
            cancelLabel="Keep"
            danger
            onConfirm={confirmDelete}
            onCancel={() => setAsking(null)}
          >
            This cannot be undone.
          </Dialog>
        ) : null}
        {asking?.kind === 'cancel' ? (
          <Dialog
            title={`Cancel ${docName}?`}
            confirmLabel="Cancel document"
            cancelLabel="Go back"
            danger
            onConfirm={() => transition('cancel')}
            onCancel={() => setAsking(null)}
          >
            The document is marked Cancelled and can no longer be changed. You can amend it afterwards.
          </Dialog>
        ) : null}
        {asking?.kind === 'amend' ? (
          <Dialog
            title={`Amend ${docName}?`}
            confirmLabel="Amend"
            cancelLabel="Go back"
            onConfirm={() => transition('amend')}
            onCancel={() => setAsking(null)}
          >
            A new draft is created from this cancelled document.
          </Dialog>
        ) : null}
        {asking?.kind === 'workflow' ? (
          <Dialog
            title={asking.action.action}
            confirmLabel="Confirm"
            cancelLabel="Back"
            onConfirm={() => takeAction(asking.action)}
            onCancel={() => setAsking(null)}
          >
            Move {movingName} from {fromState} to {asking.action.to}?
          </Dialog>
        ) : null}
        {asking?.kind === 'reload' ? (
          <Dialog
            title="Reload the current version?"
            confirmLabel="Discard edits and reload"
            cancelLabel="Keep editing"
            danger
            onConfirm={reload}
            onCancel={() => setAsking(null)}
          >
            Reloading discards your unsaved edits.
          </Dialog>
        ) : null}
        {asking?.kind === 'leave' ? (
          <Dialog
            title="You have unsaved changes."
            confirmLabel="Leave and discard"
            cancelLabel="Stay"
            danger
            onConfirm={() => leaveTo(asking.next)}
            onCancel={() => setAsking(null)}
          >
            Leave this page and lose them?
          </Dialog>
        ) : null}

        <div className="doc-grid">
          <div className="doc-main">
            {isNew && !canSave ? (
              <p role="alert" className="alert">
                You cannot create {meta.label} documents.
              </p>
            ) : (
              <>
                {columnFields.length > 0 ? (
                  <section className="card details-card" aria-labelledby={detailsId}>
                    <h2 id={detailsId}>Details</h2>
                    {requiredShown ? <p className="hint required-hint">* required</p> : null}
                    <div className="fields">
                      {columnFields.map((field) => {
                        const id = `${idBase}-${field.name}`;
                        const message = errors[errorKey(field.name)];
                        if (readOnlyOf(field)) {
                          return (
                            <ReadOnlyField
                              key={field.name}
                              id={id}
                              field={field}
                              value={state.values[field.name] ?? emptyValue(field)}
                              titles={titles}
                              message={message}
                              className={field.type === 'text' ? 'field-wide' : undefined}
                            />
                          );
                        }
                        const control = (
                          <FieldControl
                            id={id}
                            field={field}
                            value={state.values[field.name] ?? ''}
                            onChange={(value) => setValue(field.name, value)}
                            titles={titles}
                            invalid={message !== undefined}
                            describedBy={message ? `${id}-error` : undefined}
                          />
                        );
                        const label = (
                          <label htmlFor={id}>
                            {field.label}
                            {field.required ? <span aria-hidden="true"> *</span> : null}
                          </label>
                        );
                        const classes = [
                          'field',
                          field.type === 'check' ? 'field-check' : '',
                          field.type === 'text' ? 'field-wide' : '',
                        ]
                          .filter(Boolean)
                          .join(' ');
                        return (
                          <div key={field.name} className={classes}>
                            {field.type === 'check' ? (
                              <>
                                {control}
                                {label}
                              </>
                            ) : (
                              <>
                                {label}
                                {control}
                              </>
                            )}
                            {message ? (
                              <p id={`${id}-error`} className="field-error">
                                {message}
                              </p>
                            ) : null}
                          </div>
                        );
                      })}
                    </div>
                  </section>
                ) : null}
                {tableFields.map((field) => {
                  const id = `${idBase}-${field.name}`;
                  const readOnly = readOnlyOf(field);
                  return (
                    <section key={field.name} className="card table-card" aria-labelledby={`${id}-title`}>
                      <h2 id={`${id}-title`}>
                        {field.label}
                        {field.required && !readOnly ? <span aria-hidden="true"> *</span> : null}
                      </h2>
                      <TableGrid
                        field={field}
                        rows={state.tables[field.name] ?? []}
                        onChange={(rows) => {
                          setState((s) => ({ ...s, tables: { ...s.tables, [field.name]: rows } }));
                          setErrors((e) =>
                            Object.fromEntries(
                              Object.entries(e).filter(([k]) => k !== field.name && !k.startsWith(`${field.name}/`)),
                            ),
                          );
                        }}
                        readOnly={readOnly}
                        errors={errors}
                        titles={titles}
                        idBase={id}
                      />
                    </section>
                  );
                })}
              </>
            )}
          </div>
          {doc ? (
            <aside className="doc-side" aria-label="Document information">
              <DocumentInfo meta={meta} doc={doc} now={now} />
              {hasWorkflow ? (
                <ActivityCard history={history} now={now} onRetry={() => setRefresh((n) => n + 1)} />
              ) : null}
            </aside>
          ) : null}
        </div>
      </form>
    </div>
  );
}

/**
 * The info panel: who made the document and when, who changed it last and when, the version and,
 * for a submittable DocType, its status and what it amends. Times are relative, with the exact
 * local time on hover. The workflow history is the Activity card under it (ActivityCard.tsx).
 */
function DocumentInfo({ meta, doc, now }: { meta: DocTypeMeta; doc: DocumentRow; now: Date }) {
  const text = (value: unknown): string => (typeof value === 'string' ? value : '');
  const owner = text(doc.owner);
  const created = text(doc.created_at);
  const modified = text(doc.modified_at);
  const when = (iso: string) =>
    iso === '' ? null : (
      <time dateTime={iso} title={exactTime(iso)}>
        {relativeTime(iso, now)}
      </time>
    );
  return (
    <>
      <section className="card info" aria-labelledby="info-heading">
        <h2 id="info-heading">Document</h2>
        <dl>
          <div>
            <dt>Created by</dt>
            <dd>
              <span className="person">
                <span className="avatar-sm" aria-hidden="true">
                  {initials(owner)}
                </span>
                <span>{owner}</span>
              </span>
              {when(created)}
            </dd>
          </div>
          <div>
            <dt>Last modified</dt>
            <dd>
              {when(modified)}
              <span className="info-by">by {text(doc.modified_by)}</span>
            </dd>
          </div>
          <div>
            <dt>Version</dt>
            <dd>{String(doc.version ?? '')}</dd>
          </div>
          {meta.submittable ? (
            <div>
              <dt>Document status</dt>
              <dd>{docStatusLabel(docStatusOf(doc.docstatus))}</dd>
            </div>
          ) : null}
          {meta.submittable && typeof doc.amended_from === 'string' ? (
            <div>
              <dt>Amended from</dt>
              <dd>
                <a href={formHref(meta.name, doc.amended_from)}>{doc.amended_from}</a>
              </dd>
            </div>
          ) : null}
        </dl>
      </section>
    </>
  );
}
