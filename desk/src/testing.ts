// Builders shared by the unit tests (not imported by the app).

import type { DocTypeMeta, FieldMeta, FieldType, Rights } from './types';

export const field = (name: string, type: FieldType, extra: Partial<FieldMeta> = {}): FieldMeta => ({
  name,
  label: name.toUpperCase(),
  type,
  required: false,
  unique: false,
  allow_on_submit: false,
  read_only: false,
  ...extra,
});

export const rights = (all: Partial<Rights> = {}): Rights => ({
  read: 'none',
  write: 'none',
  create: 'none',
  delete: 'none',
  submit: 'none',
  cancel: 'none',
  amend: 'none',
  export: 'none',
  import: 'none',
  ...all,
});

export const meta = (fields: FieldMeta[], extra: Partial<DocTypeMeta> = {}): DocTypeMeta => ({
  name: 'order',
  label: 'Order',
  app: null,
  icon: null,
  naming: 'random',
  submittable: false,
  title_field: null,
  list_fields: null,
  fields,
  rights: rights({ read: 'all', write: 'all', create: 'all' }),
  ...extra,
});
