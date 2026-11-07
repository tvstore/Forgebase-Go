// The fetch wrapper of the desk: JSON in and out, the error envelope as a typed error and the
// CSRF header. The CSRF token is kept in this module's memory only: it is never written to any
// browser storage (the eslint config forbids it). After a page reload it is recovered with
// `GET /api/v1/session`, which issues a new one; a request refused with `csrf_failed` (the token
// was rotated by another tab) refreshes it once and is retried once.

import { isAddressable } from './access';
import { parseLinkTitles } from './linkTitles';
import type { LinkTitles } from './linkTitles';
import type { ReportMeta, ReportResult } from './reports';
import { filenameFromDisposition, importContentType, parseSummary } from './transfer';
import type { FileFormat, ImportMode, ImportSummary } from './transfer';
import type { SummaryResult } from './summary';
import type { DocTypeMeta, DocTypeSummary, DocumentRow, SessionInfo } from './types';
import type { SavedView, ViewDefinition } from './views';

const CSRF_HEADER = 'X-Forgebase-CSRF';

/** An error answered by the API (`{"error": {"code", "message", "details"?}}`). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details: string[];
  /** The `details` as the server sent them (an object for `import_failed` and `export_too_large`). */
  readonly rawDetails: unknown;

  constructor(status: number, code: string, message: string, details: string[] = [], rawDetails: unknown = undefined) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
    this.rawDetails = rawDetails;
  }
}

let csrfToken: string | null = null;
let onUnauthenticated: (() => void) | null = null;
let refreshing: Promise<void> | null = null;

export function setCsrfToken(token: string | null): void {
  csrfToken = token;
}

/** Called when a request that needed a session gets 401 (the session ended). */
export function setUnauthenticatedHandler(handler: (() => void) | null): void {
  onUnauthenticated = handler;
}

interface Options {
  body?: unknown;
  /** The request is allowed to be 401 without meaning "the session ended" (login, session probe). */
  expectUnauthenticated?: boolean;
  /** `GET /api/v1/session` only: the header that marks a deliberate refresh of the CSRF token. */
  refresh?: boolean;
  /** Internal: this request is the retry after a CSRF refresh. */
  retried?: boolean;
  /** Returns the whole answer (`data` and the keys beside it) instead of `data` only. */
  envelope?: boolean;
  /** A body that is not JSON: sent as it is with this content type (an import file). */
  file?: { body: Blob; contentType: string };
  /** The answer is a file: returned as a {@link Download}. */
  download?: boolean;
}

/** A file the server answered with (an export). */
export interface Download {
  blob: Blob;
  filename: string;
  /** The result was cut at the report's row limit (`X-Forgebase-Truncated`). */
  truncated: boolean;
}

async function parse(response: Response): Promise<unknown> {
  if (response.status === 204) return undefined;
  try {
    return await response.json();
  } catch {
    return undefined;
  }
}

function toError(status: number, payload: unknown): ApiError {
  const error = (payload as { error?: Record<string, unknown> } | undefined)?.error;
  if (error && typeof error.code === 'string' && typeof error.message === 'string') {
    const details = Array.isArray(error.details)
      ? error.details.filter((d): d is string => typeof d === 'string')
      : [];
    return new ApiError(status, error.code, error.message, details, error.details);
  }
  return new ApiError(status, 'unexpected', `unexpected response (HTTP ${status})`);
}

async function refreshCsrf(): Promise<void> {
  // One refresh at a time: two parallel refreshes would invalidate each other's token.
  refreshing ??= getSession()
    .then(() => undefined)
    .catch((e: unknown) => {
      // No session any more: the same as any other 401, so the user lands on the login page.
      if (e instanceof ApiError && e.status === 401) {
        csrfToken = null;
        onUnauthenticated?.();
      }
      throw e;
    })
    .finally(() => {
      refreshing = null;
    });
  await refreshing;
}

async function request<T>(method: string, path: string, options: Options = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: options.download ? '*/*' : 'application/json' };
  if (options.body !== undefined) headers['Content-Type'] = 'application/json';
  if (options.file) headers['Content-Type'] = options.file.contentType;
  // Only changing requests carry the token; a GET never needs it.
  if (method !== 'GET' && csrfToken !== null) headers[CSRF_HEADER] = csrfToken;
  // The server rotates the token only for this header, which a navigation or an <img> of
  // another site cannot send.
  if (options.refresh) headers[CSRF_HEADER] = 'refresh';

  const response = await fetch(path, {
    method,
    headers,
    credentials: 'same-origin',
    body: options.file ? options.file.body : options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  if (response.ok && options.download) {
    const download: Download = {
      blob: await response.blob(),
      filename: filenameFromDisposition(response.headers.get('Content-Disposition'), 'export'),
      truncated: response.headers.get('X-Forgebase-Truncated') === 'true',
    };
    return download as T;
  }
  const payload = await parse(response);

  if (response.ok) return options.envelope ? (payload as T) : ((payload as { data: T } | undefined)?.data as T);

  const error = toError(response.status, payload);
  if (error.status === 403 && error.code === 'csrf_failed' && method !== 'GET' && !options.retried) {
    await refreshCsrf();
    return request<T>(method, path, { ...options, retried: true });
  }
  if (error.status === 401 && !options.expectUnauthenticated) {
    csrfToken = null;
    onUnauthenticated?.();
  }
  throw error;
}

/** Logs in; the session cookie is set by the server, the CSRF token is kept in memory. */
export async function login(username: string, password: string): Promise<SessionInfo> {
  const session = await request<SessionInfo>('POST', '/api/v1/login', {
    body: { username, password },
    expectUnauthenticated: true,
  });
  csrfToken = session.csrf_token;
  return session;
}

/**
 * The user of the cookie session, with a **new** CSRF token (kept in memory). Rejects with a 401
 * `ApiError` when there is no session. Used on start and to recover from `csrf_failed`.
 */
export async function getSession(): Promise<SessionInfo> {
  const session = await request<SessionInfo>('GET', '/api/v1/session', {
    expectUnauthenticated: true,
    refresh: true,
  });
  csrfToken = session.csrf_token;
  return session;
}

export async function logout(): Promise<void> {
  try {
    await request<void>('POST', '/api/v1/logout', { expectUnauthenticated: true });
  } finally {
    csrfToken = null;
  }
}

export function getMeta(): Promise<DocTypeSummary[]> {
  return request<DocTypeSummary[]>('GET', '/api/v1/meta');
}

export function getDocTypeMeta(doctype: string): Promise<DocTypeMeta> {
  return request<DocTypeMeta>('GET', `/api/v1/meta/${encodeURIComponent(doctype)}`);
}

export interface ListParams {
  /** The `filters` JSON text (an array of `{field, op, value}`), see filters.ts. */
  filters?: string;
  /** `field` or `field:desc`. */
  sort?: string;
  limit: number;
  offset: number;
}

export function listDocuments(doctype: string, params: ListParams): Promise<DocumentRow[]> {
  const query = new URLSearchParams();
  if (params.filters) query.set('filters', params.filters);
  if (params.sort) query.set('sort', params.sort);
  query.set('limit', String(params.limit));
  query.set('offset', String(params.offset));
  return request<DocumentRow[]>(
    'GET',
    `/api/v1/resource/${encodeURIComponent(doctype)}?${query.toString()}`,
  );
}

/** A page of documents with the titles of the documents they link to (`link_titles=1`, UX.4). */
export interface TitledList {
  rows: DocumentRow[];
  titles: LinkTitles;
}

/** [`listDocuments`] that also asks for the titles of the linked documents the user may read. */
export async function listDocumentsWithTitles(doctype: string, params: ListParams): Promise<TitledList> {
  const query = new URLSearchParams();
  if (params.filters) query.set('filters', params.filters);
  if (params.sort) query.set('sort', params.sort);
  query.set('limit', String(params.limit));
  query.set('offset', String(params.offset));
  query.set('link_titles', '1');
  const answer = await request<{ data?: DocumentRow[]; link_titles?: unknown }>(
    'GET',
    `/api/v1/resource/${encodeURIComponent(doctype)}?${query.toString()}`,
    { envelope: true },
  );
  return { rows: answer?.data ?? [], titles: parseLinkTitles(answer?.link_titles) };
}

/**
 * The path of a document. A name that cannot be put in a path segment (`.`, `..`, empty: see
 * `isAddressable`) is refused here, so the desk never requests a wrong URL; the form view shows
 * a message for such a document before it gets this far.
 */
function documentPath(doctype: string, name: string): string {
  if (!isAddressable(name)) throw new Error(`the name ${JSON.stringify(name)} has no REST path`);
  return `/api/v1/resource/${encodeURIComponent(doctype)}/${encodeURIComponent(name)}`;
}

export function getDocument(doctype: string, name: string): Promise<DocumentRow> {
  return request<DocumentRow>('GET', documentPath(doctype, name));
}

/** A document with the titles of the documents it links to (`link_titles=1`, UX.4). */
export interface TitledDocument {
  doc: DocumentRow;
  titles: LinkTitles;
}

/** [`getDocument`] that also asks for the titles of the linked documents the user may read. */
export async function getDocumentWithTitles(doctype: string, name: string): Promise<TitledDocument> {
  const answer = await request<{ data: DocumentRow; link_titles?: unknown }>(
    'GET',
    `${documentPath(doctype, name)}?link_titles=1`,
    { envelope: true },
  );
  return { doc: answer.data, titles: parseLinkTitles(answer.link_titles) };
}

/** `POST`: creates a document (201); the answer is the stored document with its generated name. */
export function createDocument(doctype: string, data: Record<string, unknown>): Promise<DocumentRow> {
  return request<DocumentRow>('POST', `/api/v1/resource/${encodeURIComponent(doctype)}`, { body: data });
}

/** `PATCH`: only the changed fields in `data`; `version` is the one the user saw. */
export function updateDocument(
  doctype: string,
  name: string,
  version: number,
  data: Record<string, unknown>,
): Promise<DocumentRow> {
  return request<DocumentRow>('PATCH', documentPath(doctype, name), { body: { version, data } });
}

export function deleteDocument(doctype: string, name: string, version: number): Promise<void> {
  return request<void>('DELETE', `${documentPath(doctype, name)}?version=${version}`);
}

export type Transition = 'submit' | 'cancel' | 'amend';

/** Submit, cancel or amend (the answer of `amend` is the new draft). */
export function transitionDocument(
  doctype: string,
  name: string,
  transition: Transition,
  version: number,
): Promise<DocumentRow> {
  return request<DocumentRow>('POST', `${documentPath(doctype, name)}/${transition}`, {
    body: { version },
  });
}

/** An action of the workflow that the caller may take on a document now (`GET .../transitions`). */
export interface AvailableAction {
  action: string;
  to: string;
}

export interface AvailableActions {
  /** The document's workflow state, or null when it has none. */
  state: string | null;
  transitions: AvailableAction[];
}

/** The workflow actions this caller may take now (role and right applied; a hook may still veto). */
export function getTransitions(doctype: string, name: string): Promise<AvailableActions> {
  return request<AvailableActions>('GET', `${documentPath(doctype, name)}/transitions`);
}

/** Takes a workflow action; the answer is the stored document in its new state. */
export function workflowTransition(
  doctype: string,
  name: string,
  action: string,
  version: number,
): Promise<DocumentRow> {
  return request<DocumentRow>('POST', `${documentPath(doctype, name)}/transition`, {
    body: { action, version },
  });
}

/** One committed transition of the log. */
export interface HistoryEntry {
  action: string;
  from: string;
  to: string;
  actor: string;
  at: string;
}

/** The transitions of a document, oldest first, at most the newest 500. */
export async function getHistory(doctype: string, name: string): Promise<HistoryEntry[]> {
  const answer = await request<{ history?: HistoryEntry[] }>('GET', `${documentPath(doctype, name)}/history`);
  return answer?.history ?? [];
}

/** The views the user sees for a DocType: the shared ones first, then their own (M8.3). */
export function listViews(doctype: string): Promise<SavedView[]> {
  return request<SavedView[]>('GET', `/api/v1/views/${encodeURIComponent(doctype)}`);
}

/** Saves a view. `shared` needs the Administrator role (the server refuses it otherwise). */
export function createView(
  doctype: string,
  label: string,
  definition: ViewDefinition,
  shared: boolean,
): Promise<SavedView> {
  return request<SavedView>('POST', `/api/v1/views/${encodeURIComponent(doctype)}`, {
    body: { label, definition, shared },
  });
}

/** Changes the definition of a view; `version` is the one the user saw. */
export function updateView(
  doctype: string,
  id: string,
  version: number,
  definition: ViewDefinition,
): Promise<SavedView> {
  return request<SavedView>('PATCH', `/api/v1/views/${encodeURIComponent(doctype)}/${encodeURIComponent(id)}`, {
    body: { version, definition },
  });
}

export function deleteView(doctype: string, id: string, version: number): Promise<void> {
  return request<void>(
    'DELETE',
    `/api/v1/views/${encodeURIComponent(doctype)}/${encodeURIComponent(id)}?version=${version}`,
  );
}

export interface SummaryParams {
  groupBy: string;
  /** `sum:amount,avg:qty`, see summary.ts. */
  aggregates: string;
  /** The `filters` JSON text, as for the list. */
  filters?: string;
}

/** The documents the user may read, grouped by a field (M8.3). */
export function getSummary(doctype: string, params: SummaryParams): Promise<SummaryResult> {
  const query = new URLSearchParams({ group_by: params.groupBy });
  if (params.aggregates) query.set('aggregates', params.aggregates);
  if (params.filters) query.set('filters', params.filters);
  return request<SummaryResult>('GET', `/api/v1/summary/${encodeURIComponent(doctype)}?${query.toString()}`);
}

/** The query reports the user may run (M8.4), by label. The SQL is never in the answer. */
export function listReports(): Promise<ReportMeta[]> {
  return request<ReportMeta[]>('GET', '/api/v1/report');
}

/**
 * Runs a report with `params` (every parameter, an empty one as `null`) and asks for the titles of
 * its Link columns. A POST, so it carries the CSRF token like every changing request, although it
 * changes nothing: the server runs it read-only.
 */
export async function runReport(name: string, params: Record<string, unknown>): Promise<ReportResult> {
  const answer = await request<{
    data?: { columns?: ReportResult['columns']; rows?: unknown[][]; truncated?: boolean };
    link_titles?: unknown;
  }>('POST', `/api/v1/report/${encodeURIComponent(name)}?link_titles=1`, { body: { params }, envelope: true });
  return {
    columns: answer?.data?.columns ?? [],
    rows: answer?.data?.rows ?? [],
    truncated: answer?.data?.truncated === true,
    titles: parseLinkTitles(answer?.link_titles),
  };
}

/**
 * Runs a report and returns the result as a file (`POST /api/v1/report/<name>/export`, M9.3):
 * the same parameters as {@link runReport}, with the CSRF token like every POST. The caller saves
 * the blob (see `saveDownload`).
 */
export function exportReport(name: string, params: Record<string, unknown>, format: FileFormat): Promise<Download> {
  return request<Download>('POST', `/api/v1/report/${encodeURIComponent(name)}/export?format=${format}`, {
    body: { params },
    download: true,
  });
}

/** What an import came to: the summary of a 200, or of a 422 `import_failed`. */
export interface ImportAnswer {
  summary: ImportSummary;
  /** False for the 422 `import_failed`: at least one row failed and nothing was imported. */
  ok: boolean;
}

/**
 * Sends a file to `POST /api/v1/import/<doctype>`. With `dryRun` nothing is ever kept. A 422
 * `import_failed` is not thrown: its details are the summary with the row errors, returned with
 * `ok: false`. Any other error (the file cannot be used at all, a right is missing) is thrown.
 */
export async function importFile(
  doctype: string,
  file: Blob,
  options: { format: FileFormat; mode: ImportMode; dryRun: boolean },
): Promise<ImportAnswer> {
  const query = new URLSearchParams({ format: options.format, mode: options.mode });
  if (options.dryRun) query.set('dry_run', '1');
  try {
    const data = await request<unknown>('POST', `/api/v1/import/${encodeURIComponent(doctype)}?${query.toString()}`, {
      file: { body: file, contentType: importContentType(options.format) },
    });
    const summary = parseSummary(data);
    if (summary === null) throw new ApiError(200, 'unexpected', 'unexpected answer to the import');
    return { summary, ok: true };
  } catch (e) {
    if (e instanceof ApiError && e.code === 'import_failed') {
      const summary = parseSummary(e.rawDetails);
      if (summary !== null) return { summary, ok: false };
    }
    throw e;
  }
}
