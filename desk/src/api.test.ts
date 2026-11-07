import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  ApiError,
  createDocument,
  deleteDocument,
  exportReport,
  getDocTypeMeta,
  getDocument,
  getDocumentWithTitles,
  getSession,
  importFile,
  listDocuments,
  listDocumentsWithTitles,
  login,
  logout,
  setCsrfToken,
  setUnauthenticatedHandler,
  transitionDocument,
  updateDocument,
} from './api';

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const session = (csrf: string) => json(200, { data: { username: 'ann', roles: ['A'], csrf_token: csrf } });
const csrfFailed = () =>
  json(403, { error: { code: 'csrf_failed', message: 'missing or invalid CSRF token' } });

let calls: { url: string; init: RequestInit }[];

function mockFetch(handler: Handler) {
  calls = [];
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    return handler(url, init);
  });
  vi.stubGlobal('fetch', fetchMock);
}

const header = (call: { init: RequestInit } | undefined, name: string) =>
  (call?.init.headers as Record<string, string> | undefined)?.[name];

beforeEach(() => {
  setCsrfToken(null);
  setUnauthenticatedHandler(null);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('envelope parsing', () => {
  it('unwraps data and sends cookies same-origin', async () => {
    mockFetch(() => json(200, { data: [{ name: 'a', label: 'A', submittable: false, rights: {} }] }));
    const meta = await getDocTypeMeta('a b');
    expect(meta).toEqual([{ name: 'a', label: 'A', submittable: false, rights: {} }]);
    expect(calls[0]?.url).toBe('/api/v1/meta/a%20b');
    expect(calls[0]?.init.credentials).toBe('same-origin');
    expect(calls[0]?.init.method).toBe('GET');
  });

  it('turns an error envelope into an ApiError', async () => {
    mockFetch(() =>
      json(422, { error: { code: 'validation_failed', message: 'validation failed', details: ['a', 'b'] } }),
    );
    const error = await getDocTypeMeta('x').catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 422, code: 'validation_failed', message: 'validation failed', details: ['a', 'b'] });
  });

  it('survives a response that is not the envelope', async () => {
    mockFetch(() => new Response('<html>bad gateway</html>', { status: 502 }));
    const error = await getDocTypeMeta('x').catch((e: unknown) => e);
    expect(error).toMatchObject({ status: 502, code: 'unexpected' });
    mockFetch(() => json(500, { error: { code: 5 } }));
    expect(await getDocTypeMeta('x').catch((e: unknown) => e)).toMatchObject({ code: 'unexpected' });
  });

  it('builds the list query', async () => {
    mockFetch(() => json(200, { data: [] }));
    await listDocuments('sales order', {
      filters: '[{"field":"a","op":"=","value":"x y"}]',
      sort: 'name:desc',
      limit: 21,
      offset: 40,
    });
    const url = new URL(calls[0]?.url ?? '', 'http://x');
    expect(url.pathname).toBe('/api/v1/resource/sales%20order');
    expect(url.searchParams.get('filters')).toBe('[{"field":"a","op":"=","value":"x y"}]');
    expect(url.searchParams.get('sort')).toBe('name:desc');
    expect(url.searchParams.get('limit')).toBe('21');
    expect(url.searchParams.get('offset')).toBe('40');
    await listDocuments('x', { limit: 20, offset: 0 });
    expect(new URL(calls[1]?.url ?? '', 'http://x').searchParams.has('filters')).toBe(false);
  });
});

describe('the CSRF token', () => {
  it('is added to changing requests only, from memory', async () => {
    mockFetch((url) => (url === '/api/v1/login' ? session('tok1') : json(200, { data: [] })));
    await login('ann', 'secret password');
    expect(header(calls[0], 'X-Forgebase-CSRF')).toBeUndefined(); // no token yet
    expect(calls[0]?.init.body).toBe('{"username":"ann","password":"secret password"}');
    expect(header(calls[0], 'Content-Type')).toBe('application/json');

    await listDocuments('x', { limit: 1, offset: 0 });
    expect(header(calls[1], 'X-Forgebase-CSRF')).toBeUndefined(); // GET

    mockFetch(() => new Response(null, { status: 204 }));
    await logout();
    expect(calls[0]?.init.method).toBe('POST');
    expect(header(calls[0], 'X-Forgebase-CSRF')).toBe('tok1');
    expect(calls[0]?.init.body).toBeUndefined();
  });

  it('is forgotten after logout', async () => {
    mockFetch(() => session('tok1'));
    await getSession();
    mockFetch(() => new Response(null, { status: 204 }));
    await logout();
    await logout();
    expect(header(calls[1], 'X-Forgebase-CSRF')).toBeUndefined();
  });

  it('is refreshed and the request retried once on csrf_failed', async () => {
    setCsrfToken('stale');
    let posts = 0;
    mockFetch((url, init) => {
      if (url === '/api/v1/session') return session('fresh');
      posts += 1;
      const sent = (init.headers as Record<string, string>)['X-Forgebase-CSRF'];
      return sent === 'fresh' ? new Response(null, { status: 204 }) : csrfFailed();
    });
    await logout();
    expect(calls.map((c) => `${c.init.method} ${c.url}`)).toEqual([
      'POST /api/v1/logout',
      'GET /api/v1/session',
      'POST /api/v1/logout',
    ]);
    expect(header(calls[2], 'X-Forgebase-CSRF')).toBe('fresh');
    expect(posts).toBe(2);
  });

  it('is requested with the refresh marker header, which the server requires', async () => {
    setCsrfToken('old');
    mockFetch(() => session('new'));
    await getSession();
    expect(calls[0]?.url).toBe('/api/v1/session');
    expect(header(calls[0], 'X-Forgebase-CSRF')).toBe('refresh');
  });

  it('sends the user to the login page when the refresh itself is a 401', async () => {
    setCsrfToken('stale');
    const handler = vi.fn();
    setUnauthenticatedHandler(handler);
    mockFetch((url) =>
      url === '/api/v1/session'
        ? json(401, { error: { code: 'unauthenticated', message: 'authentication required' } })
        : csrfFailed(),
    );
    const error = await logout().catch((e: unknown) => e);
    expect(error).toMatchObject({ status: 401 });
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it('is retried only once, then the error is reported', async () => {
    setCsrfToken('stale');
    mockFetch((url) => (url === '/api/v1/session' ? session('still-wrong') : csrfFailed()));
    const error = await logout().catch((e: unknown) => e);
    expect(error).toMatchObject({ status: 403, code: 'csrf_failed' });
    expect(calls.filter((c) => c.url === '/api/v1/logout')).toHaveLength(2);
    expect(calls.filter((c) => c.url === '/api/v1/session')).toHaveLength(1);
  });

  it('is refreshed once for parallel failing requests', async () => {
    setCsrfToken('stale');
    mockFetch(async (url, init) => {
      if (url === '/api/v1/session') {
        await Promise.resolve();
        return session('fresh');
      }
      return (init.headers as Record<string, string>)['X-Forgebase-CSRF'] === 'fresh'
        ? new Response(null, { status: 204 })
        : csrfFailed();
    });
    await Promise.all([logout(), logout()]);
    expect(calls.filter((c) => c.url === '/api/v1/session')).toHaveLength(1);
  });

  it('is never refreshed for a GET', async () => {
    mockFetch(() => csrfFailed());
    await getDocTypeMeta('x').catch(() => undefined);
    expect(calls).toHaveLength(1);
  });
});

describe('401 handling', () => {
  it('calls the handler and forgets the token when the session ended', async () => {
    setCsrfToken('tok');
    const handler = vi.fn();
    setUnauthenticatedHandler(handler);
    mockFetch(() => json(401, { error: { code: 'unauthenticated', message: 'authentication required' } }));
    const error = await getDocTypeMeta('x').catch((e: unknown) => e);
    expect(error).toMatchObject({ status: 401, code: 'unauthenticated' });
    expect(handler).toHaveBeenCalledTimes(1);
    mockFetch(() => new Response(null, { status: 204 }));
    await logout();
    expect(header(calls[0], 'X-Forgebase-CSRF')).toBeUndefined();
  });

  it('does not treat a failed login or the start-up probe as an ended session', async () => {
    const handler = vi.fn();
    setUnauthenticatedHandler(handler);
    mockFetch(() => json(401, { error: { code: 'unauthenticated', message: 'invalid username or password' } }));
    const error = await login('a', 'b').catch((e: unknown) => e);
    expect(error).toMatchObject({ status: 401, message: 'invalid username or password' });
    await getSession().catch(() => undefined);
    expect(handler).not.toHaveBeenCalled();
  });
});

describe('no browser storage', () => {
  it('is never touched by the client', async () => {
    const touched: string[] = [];
    const trap = (name: string) =>
      new Proxy({}, { get: (_t, prop) => { touched.push(`${name}.${String(prop)}`); return undefined; } });
    vi.stubGlobal('localStorage', trap('localStorage'));
    vi.stubGlobal('sessionStorage', trap('sessionStorage'));
    vi.stubGlobal('indexedDB', trap('indexedDB'));
    mockFetch((url) => (url === '/api/v1/login' || url === '/api/v1/session' ? session('t') : new Response(null, { status: 204 })));
    await login('a', 'b');
    await getSession();
    await logout();
    expect(touched).toEqual([]);
  });
});

describe('document requests', () => {
  const doc = { name: 'SO-1', version: 1 };

  it('reads, creates, updates, deletes and transitions with the CSRF header and encoded names', async () => {
    setCsrfToken('tok');
    mockFetch((_url, init) =>
      init.method === 'DELETE' ? new Response(null, { status: 204 }) : json(init.method === 'POST' ? 201 : 200, { data: doc }),
    );
    expect(await getDocument('sales order', 'a/b c')).toEqual(doc);
    expect(calls[0]?.url).toBe('/api/v1/resource/sales%20order/a%2Fb%20c');
    expect(header(calls[0], 'X-Forgebase-CSRF')).toBeUndefined(); // a GET needs none

    await createDocument('note', { title: 't' });
    expect(calls[1]?.url).toBe('/api/v1/resource/note');
    expect(calls[1]?.init.method).toBe('POST');
    expect(calls[1]?.init.body).toBe('{"title":"t"}');
    expect(header(calls[1], 'X-Forgebase-CSRF')).toBe('tok');

    await updateDocument('note', 'n 1', 3, { title: 'u' });
    expect(calls[2]?.url).toBe('/api/v1/resource/note/n%201');
    expect(calls[2]?.init.method).toBe('PATCH');
    expect(JSON.parse(String(calls[2]?.init.body))).toEqual({ version: 3, data: { title: 'u' } });

    expect(await deleteDocument('note', 'n', 4)).toBeUndefined();
    expect(calls[3]?.url).toBe('/api/v1/resource/note/n?version=4');
    expect(calls[3]?.init.method).toBe('DELETE');

    for (const action of ['submit', 'cancel', 'amend'] as const) {
      await transitionDocument('note', 'n', action, 5);
      const call = calls[calls.length - 1];
      expect(call?.url).toBe(`/api/v1/resource/note/n/${action}`);
      expect(call?.init.method).toBe('POST');
      expect(JSON.parse(String(call?.init.body))).toEqual({ version: 5 });
    }
  });

  it('never requests the path of a name that cannot be addressed', async () => {
    mockFetch(() => json(200, { data: doc }));
    expect(() => getDocument('note', '..')).toThrow(/no REST path/);
    expect(() => getDocument('note', '.')).toThrow(/no REST path/);
    expect(() => deleteDocument('note', '..', 1)).toThrow(/no REST path/);
    expect(() => transitionDocument('note', '.', 'submit', 1)).toThrow(/no REST path/);
    expect(calls).toEqual([]);
  });

  it('keeps the details of a validation error and the code of a conflict', async () => {
    mockFetch(() =>
      json(422, { error: { code: 'validation_failed', message: 'validation failed', details: ['field `a` is required'] } }),
    );
    await expect(updateDocument('note', 'n', 1, {})).rejects.toMatchObject({
      status: 422,
      code: 'validation_failed',
      details: ['field `a` is required'],
    });
    mockFetch(() => json(409, { error: { code: 'conflict', message: 'changed' } }));
    await expect(deleteDocument('note', 'n', 1)).rejects.toMatchObject({ status: 409, code: 'conflict' });
  });
});

describe('link titles', () => {
  it('asks for them on a list and returns the rows with the titles', async () => {
    mockFetch(() =>
      json(200, { data: [{ name: 'SO-1', customer: 'C-1' }], link_titles: { customer: { 'C-1': 'Ada' } } }),
    );
    const answer = await listDocumentsWithTitles('sales order', { sort: 'name', limit: 21, offset: 0 });
    expect(answer).toEqual({ rows: [{ name: 'SO-1', customer: 'C-1' }], titles: { customer: { 'C-1': 'Ada' } } });
    const url = new URL(calls[0]?.url ?? '', 'http://x');
    expect(url.pathname).toBe('/api/v1/resource/sales%20order');
    expect(url.searchParams.get('link_titles')).toBe('1');
    expect(url.searchParams.get('limit')).toBe('21');
  });

  it('asks for them on one document', async () => {
    mockFetch(() => json(200, { data: { name: 'SO-1' }, link_titles: { customer: { 'C-1': 'Ada' } } }));
    const answer = await getDocumentWithTitles('order', 'SO/1');
    expect(answer).toEqual({ doc: { name: 'SO-1' }, titles: { customer: { 'C-1': 'Ada' } } });
    expect(calls[0]?.url).toBe('/api/v1/resource/order/SO%2F1?link_titles=1');
  });

  it('works against a server that sends no titles', async () => {
    mockFetch(() => json(200, { data: [{ name: 'a' }] }));
    expect(await listDocumentsWithTitles('order', { limit: 1, offset: 0 })).toEqual({ rows: [{ name: 'a' }], titles: {} });
    mockFetch(() => json(200, { data: { name: 'a' } }));
    expect(await getDocumentWithTitles('order', 'a')).toEqual({ doc: { name: 'a' }, titles: {} });
  });

  it('still reports an error as an ApiError', async () => {
    mockFetch(() => json(403, { error: { code: 'permission_denied', message: 'no' } }));
    await expect(getDocumentWithTitles('order', 'a')).rejects.toBeInstanceOf(ApiError);
  });

  it('never touches the plain requests: no link_titles parameter', async () => {
    mockFetch(() => json(200, { data: [] }));
    await listDocuments('order', { limit: 5, offset: 0 });
    expect(calls[0]?.url).not.toContain('link_titles');
  });
});

describe('import and export (M9.3)', () => {
  it('sends an import file as it is, with its content type, the options in the address and the CSRF header', async () => {
    setCsrfToken('tok');
    const summary = { dry_run: true, mode: 'upsert', rows: 2, inserted: 1, updated: 1, errors: [], error_count: 0 };
    mockFetch(() => json(200, { data: summary }));
    const file = new Blob(['a,b\r\n1,2\r\n'], { type: 'text/plain' });
    const answer = await importFile('sales order', file, { format: 'csv', mode: 'upsert', dryRun: true });
    expect(answer.ok).toBe(true);
    expect(answer.summary).toMatchObject({ dryRun: true, mode: 'upsert', rows: 2, updated: 1 });
    expect(calls[0]?.url).toBe('/api/v1/import/sales%20order?format=csv&mode=upsert&dry_run=1');
    expect(calls[0]?.init.method).toBe('POST');
    expect(calls[0]?.init.body).toBe(file);
    expect(header(calls[0], 'Content-Type')).toBe('text/csv');
    expect(header(calls[0], 'X-Forgebase-CSRF')).toBe('tok');
    mockFetch(() => json(200, { data: { ...summary, dry_run: false } }));
    await importFile('a', file, { format: 'json', mode: 'insert', dryRun: false });
    expect(calls[0]?.url).toBe('/api/v1/import/a?format=json&mode=insert');
    expect(header(calls[0], 'Content-Type')).toBe('application/json');
  });

  it('returns the summary of a failed import instead of throwing, and throws any other error', async () => {
    const summary = {
      dry_run: false, mode: 'insert', rows: 3, inserted: 2, updated: 0, error_count: 1,
      errors: [{ row: 2, name: null, code: 'hook_rejected', message: 'no' }],
    };
    mockFetch(() => json(422, { error: { code: 'import_failed', message: '1 row(s) have errors', details: summary } }));
    const answer = await importFile('a', new Blob(['x']), { format: 'csv', mode: 'insert', dryRun: false });
    expect(answer.ok).toBe(false);
    expect(answer.summary.errors[0]?.row).toBe(2);

    mockFetch(() => json(422, { error: { code: 'invalid_import', message: 'the file cannot be imported', details: ['unknown column `x`'] } }));
    const error = await importFile('a', new Blob(['x']), { format: 'csv', mode: 'insert', dryRun: true }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).code).toBe('invalid_import');
    expect((error as ApiError).details).toEqual(['unknown column `x`']);
    mockFetch(() => json(403, { error: { code: 'permission_denied', message: 'permission denied' } }));
    await expect(importFile('a', new Blob(['x']), { format: 'csv', mode: 'insert', dryRun: true })).rejects.toBeInstanceOf(ApiError);
  });

  it('keeps the object details of an error', async () => {
    mockFetch(() => json(422, { error: { code: 'export_too_large', message: 'too many', details: { count: 5, limit: 3 } } }));
    const error = await getDocument('a', 'b').catch((e: unknown) => e);
    expect((error as ApiError).details).toEqual([]);
    expect((error as ApiError).rawDetails).toEqual({ count: 5, limit: 3 });
  });

  it('downloads a report export with the CSRF token and reads the file name and the truncation', async () => {
    setCsrfToken('tok');
    mockFetch(
      () =>
        new Response('a,b\r\n', {
          status: 200,
          headers: {
            'Content-Type': 'text/csv; charset=utf-8',
            'Content-Disposition': 'attachment; filename="sales.by_customer-20261031T101500Z.csv"',
            'X-Forgebase-Truncated': 'true',
          },
        }),
    );
    const download = await exportReport('sales.by_customer', { from: '2026-01-01' }, 'csv');
    expect(calls[0]?.url).toBe('/api/v1/report/sales.by_customer/export?format=csv');
    expect(calls[0]?.init.method).toBe('POST');
    expect(header(calls[0], 'X-Forgebase-CSRF')).toBe('tok');
    expect(JSON.parse(String(calls[0]?.init.body))).toEqual({ params: { from: '2026-01-01' } });
    expect(download.filename).toBe('sales.by_customer-20261031T101500Z.csv');
    expect(download.truncated).toBe(true);
    expect(await download.blob.text()).toBe('a,b\r\n');
    // An error is still the envelope.
    mockFetch(() => json(404, { error: { code: 'not_found', message: 'report not found' } }));
    await expect(exportReport('x.y', {}, 'json')).rejects.toBeInstanceOf(ApiError);
  });
});
