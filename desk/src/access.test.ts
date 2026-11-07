import { describe, expect, it } from 'vitest';

import {
  docStatusOf,
  fieldReadOnly,
  hasEditableField,
  hasEditableRequired,
  headerActions,
  isAddressable,
  lifecycleActions,
  mayChange,
  permits,
} from './access';
import type { DocContext, DocStatus } from './access';
import { field, meta, rights } from './testing';

const submittableMeta = meta(
  [
    field('title', 'data'),
    field('remarks', 'text', { allow_on_submit: true }),
    field('lines', 'table', { target: 'line', fields: [field('item', 'data')] }),
  ],
  { submittable: true },
);
const namedMeta = meta([field('email', 'data'), field('name_note', 'data')], { naming: 'field:email' });

const ctx = (over: Partial<DocContext> = {}): DocContext => ({
  isNew: false,
  docStatus: 0,
  rights: rights({ read: 'all', write: 'all', create: 'all', delete: 'all', submit: 'all', cancel: 'all', amend: 'all' }),
  owner: 'ann',
  username: 'ann',
  submittable: true,
  ...over,
});

describe('permits', () => {
  it('all, own and none', () => {
    expect(permits('all', 'bob', 'ann')).toBe(true);
    expect(permits('own', 'ann', 'ann')).toBe(true);
    expect(permits('own', 'bob', 'ann')).toBe(false);
    expect(permits('own', null, 'ann')).toBe(false);
    expect(permits('none', 'ann', 'ann')).toBe(false);
  });
});

describe('read-only rules (docstatus x allow_on_submit x field kind x rights)', () => {
  const [title, remarks, lines] = submittableMeta.fields as [
    (typeof submittableMeta.fields)[0],
    (typeof submittableMeta.fields)[0],
    (typeof submittableMeta.fields)[0],
  ];
  const readOnly = (docStatus: DocStatus, f: typeof title, over: Partial<DocContext> = {}) =>
    fieldReadOnly(ctx({ docStatus, ...over }), submittableMeta, f);

  it('a draft is editable, tables included', () => {
    expect(readOnly(0, title)).toBe(false);
    expect(readOnly(0, remarks)).toBe(false);
    expect(readOnly(0, lines)).toBe(false);
  });

  it('a submitted document edits only its allow_on_submit fields, never its tables', () => {
    expect(readOnly(1, title)).toBe(true);
    expect(readOnly(1, remarks)).toBe(false);
    expect(readOnly(1, lines)).toBe(true);
  });

  it('a read_only field of the metadata is read-only for everyone, also on a new document', () => {
    const total = field('total', 'decimal', { read_only: true });
    const m = meta([field('title', 'data'), total]);
    expect(fieldReadOnly(ctx({ isNew: true }), m, total)).toBe(true);
    expect(fieldReadOnly(ctx({ submittable: false }), m, total)).toBe(true);
    expect(fieldReadOnly(ctx({ isNew: true }), m, m.fields[0] as typeof total)).toBe(false);
  });

  it('a cancelled document is read-only', () => {
    for (const f of submittableMeta.fields) expect(readOnly(2, f)).toBe(true);
    expect(hasEditableField(ctx({ docStatus: 2 }), submittableMeta)).toBe(false);
  });

  it('without the write right everything is read-only', () => {
    const noWrite = rights({ read: 'all' });
    for (const status of [0, 1] as const) {
      expect(readOnly(status, title, { rights: noWrite })).toBe(true);
      expect(readOnly(status, remarks, { rights: noWrite })).toBe(true);
    }
    expect(hasEditableField(ctx({ rights: noWrite }), submittableMeta)).toBe(false);
  });

  it('own write: editable only on the user\'s own document', () => {
    const own = rights({ read: 'all', write: 'own' });
    expect(mayChange(ctx({ rights: own }))).toBe(true);
    expect(mayChange(ctx({ rights: own, owner: 'bob' }))).toBe(false);
    expect(readOnly(0, title, { rights: own, owner: 'bob' })).toBe(true);
  });

  it('a new document needs the create right, not write', () => {
    const createOnly = rights({ read: 'all', create: 'all' });
    expect(readOnly(0, title, { isNew: true, owner: null, rights: createOnly })).toBe(false);
    const writeOnly = rights({ read: 'all', write: 'all' });
    expect(readOnly(0, title, { isNew: true, owner: null, rights: writeOnly })).toBe(true);
  });

  it('the naming field is editable on create only', () => {
    const [email, other] = namedMeta.fields as [(typeof title), (typeof title)];
    const base = { submittable: false };
    expect(fieldReadOnly(ctx({ ...base, isNew: true, owner: null }), namedMeta, email)).toBe(false);
    expect(fieldReadOnly(ctx(base), namedMeta, email)).toBe(true);
    expect(fieldReadOnly(ctx(base), namedMeta, other)).toBe(false);
  });
});

describe('the required marker', () => {
  const required = meta(
    [
      field('customer', 'link', { required: true }),
      field('remarks', 'text', { allow_on_submit: true }),
      field('lines', 'table', { target: 'line', fields: [field('item', 'data', { required: true })] }),
    ],
    { submittable: true },
  );

  it('shows for a draft and a new document that have an editable required field', () => {
    expect(hasEditableRequired(ctx({ isNew: true }), required)).toBe(true);
    expect(hasEditableRequired(ctx({ docStatus: 0 }), required)).toBe(true);
  });

  it('is not needed once every required field is shown as a value', () => {
    expect(hasEditableRequired(ctx({ docStatus: 1 }), required)).toBe(false); // submitted: only remarks is editable
    expect(hasEditableRequired(ctx({ docStatus: 2 }), required)).toBe(false);
    expect(hasEditableRequired(ctx({ rights: rights({ read: 'all' }) }), required)).toBe(false); // no write right
  });

  it('counts a required column of an editable table, and none when nothing is required', () => {
    const tableOnly = meta([field('lines', 'table', { target: 'line', fields: [field('item', 'data', { required: true })] })]);
    expect(hasEditableRequired(ctx({ submittable: false }), tableOnly)).toBe(true);
    expect(hasEditableRequired(ctx(), meta([field('a', 'data')]))).toBe(false);
  });
});

describe('lifecycle buttons (docstatus x rights x submittable)', () => {
  const full = rights({ read: 'all', write: 'all', delete: 'all', submit: 'all', cancel: 'all', amend: 'all' });

  it('shows the action that fits the docstatus', () => {
    expect(lifecycleActions(ctx({ docStatus: 0, rights: full }))).toEqual(['submit', 'delete']);
    expect(lifecycleActions(ctx({ docStatus: 1, rights: full }))).toEqual(['cancel']);
    expect(lifecycleActions(ctx({ docStatus: 2, rights: full }))).toEqual(['amend', 'delete']);
  });

  it('hides what the rights do not allow (never disabled)', () => {
    expect(lifecycleActions(ctx({ docStatus: 0, rights: { ...full, submit: 'none' } }))).toEqual(['delete']);
    expect(lifecycleActions(ctx({ docStatus: 0, rights: { ...full, delete: 'none' } }))).toEqual(['submit']);
    expect(lifecycleActions(ctx({ docStatus: 1, rights: { ...full, cancel: 'none' } }))).toEqual([]);
    expect(lifecycleActions(ctx({ docStatus: 2, rights: { ...full, amend: 'none' } }))).toEqual(['delete']);
    expect(lifecycleActions(ctx({ docStatus: 2, rights: rights({ read: 'all' }) }))).toEqual([]);
  });

  it('a submitted document cannot be deleted', () => {
    expect(lifecycleActions(ctx({ docStatus: 1, rights: full }))).not.toContain('delete');
  });

  it('own rights apply to the user\'s own documents only', () => {
    const own = { ...full, submit: 'own' as const, delete: 'own' as const };
    expect(lifecycleActions(ctx({ rights: own }))).toEqual(['submit', 'delete']);
    expect(lifecycleActions(ctx({ rights: own, owner: 'bob' }))).toEqual([]);
  });

  it('a DocType that is not submittable has delete only (its lifecycle rights are none)', () => {
    const plain = rights({ read: 'all', write: 'all', delete: 'all' });
    expect(lifecycleActions(ctx({ submittable: false, rights: plain }))).toEqual(['delete']);
    // Even with rights the API would refuse, nothing but delete is offered.
    expect(lifecycleActions(ctx({ submittable: false, rights: full }))).toEqual(['delete']);
  });

  it('a new document has no lifecycle buttons', () => {
    expect(lifecycleActions(ctx({ isNew: true, owner: null, rights: full }))).toEqual([]);
  });

  it('reads the docstatus of a document', () => {
    expect(docStatusOf(0)).toBe(0);
    expect(docStatusOf(1)).toBe(1);
    expect(docStatusOf(2)).toBe(2);
    expect(docStatusOf(undefined)).toBe(0); // a DocType that is not submittable has none
    expect(docStatusOf(7)).toBe(0);
  });
});

describe('names that cannot be put in a REST path', () => {
  it('refuses ".", ".." and the empty name', () => {
    expect(isAddressable('.')).toBe(false);
    expect(isAddressable('..')).toBe(false);
    expect(isAddressable('')).toBe(false);
  });

  it('accepts every other name (it is encoded in paths and routes)', () => {
    for (const name of ['a', '...', '.a', 'a.', 'SO-2026-00001', 'a/b', '%2e%2e', 'a b', '日本', '..\\x']) {
      expect(isAddressable(name), name).toBe(true);
    }
  });
});

describe('header actions (the main action and the More menu)', () => {
  const full = ctx();

  it('makes Save the main action for a new document and while there are unsaved changes', () => {
    expect(headerActions(ctx({ isNew: true }), false, true)).toEqual({ save: 'primary', submit: null, more: [] });
    expect(headerActions(full, true, true)).toEqual({ save: 'primary', submit: 'secondary', more: ['delete'] });
  });

  it('makes Submit the main action for a saved draft', () => {
    expect(headerActions(full, false, true)).toEqual({ save: 'secondary', submit: 'primary', more: ['delete'] });
  });

  it('puts Cancel and Amend in the menu, and offers no Submit for a submitted document', () => {
    expect(headerActions(ctx({ docStatus: 1 }), false, true)).toEqual({ save: 'secondary', submit: null, more: ['cancel'] });
    expect(headerActions(ctx({ docStatus: 2 }), false, false)).toEqual({ save: null, submit: null, more: ['amend', 'delete'] });
  });

  it('offers only what the rights allow', () => {
    const clerk = ctx({ rights: rights({ read: 'all', write: 'all', create: 'all' }) });
    expect(headerActions(clerk, false, true)).toEqual({ save: 'secondary', submit: null, more: [] });
    expect(headerActions(clerk, false, false)).toEqual({ save: null, submit: null, more: [] });
  });
});

describe('a DocType with a workflow', () => {
  const full = rights({ read: 'all', write: 'all', create: 'all', delete: 'all', submit: 'all', cancel: 'all', amend: 'all' });

  it('never lists the plain Submit and Cancel, and keeps Amend and Delete', () => {
    expect(lifecycleActions(ctx({ docStatus: 0, rights: full, workflow: true }))).toEqual(['delete']);
    expect(lifecycleActions(ctx({ docStatus: 1, rights: full, workflow: true }))).toEqual([]);
    expect(lifecycleActions(ctx({ docStatus: 2, rights: full, workflow: true }))).toEqual(['amend', 'delete']);
  });

  it('has no Submit button in the header; Save follows the same rule as before', () => {
    expect(headerActions(ctx({ rights: full, workflow: true }), false, true)).toEqual({
      save: 'secondary',
      submit: null,
      more: ['delete'],
    });
    expect(headerActions(ctx({ rights: full, workflow: true }), true, true).save).toBe('primary');
  });

  it('changes nothing for a DocType without one', () => {
    expect(lifecycleActions(ctx({ docStatus: 0, rights: full, workflow: false }))).toEqual(['submit', 'delete']);
  });
});
