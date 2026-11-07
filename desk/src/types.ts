// Shapes of the JSON the API returns (see docs/blueprint/architecture.md, section 7).

export type Access = 'all' | 'own' | 'none';

export type Right = 'read' | 'write' | 'create' | 'delete' | 'submit' | 'cancel' | 'amend' | 'export' | 'import';

export type Rights = Record<Right, Access>;

export interface SessionInfo {
  username: string;
  roles: string[];
  csrf_token: string;
}

export interface DocTypeSummary {
  name: string;
  label: string;
  /** The app that owns the DocType, or null when none is recorded. */
  app: string | null;
  /** The icon the DocType names (one of the list in doctype_icons.txt), or null for the default. */
  icon: string | null;
  submittable: boolean;
  rights: Rights;
}

export type FieldType =
  | 'data'
  | 'text'
  | 'int'
  | 'decimal'
  | 'check'
  | 'date'
  | 'datetime'
  | 'select'
  | 'link'
  | 'table';

export interface FieldMeta {
  name: string;
  label: string;
  type: FieldType;
  required: boolean;
  unique: boolean;
  allow_on_submit: boolean;
  /** Written only by the server (hooks): shown read-only and never sent. */
  read_only: boolean;
  max_length?: number;
  precision?: number;
  scale?: number;
  options?: string[];
  target?: string;
  /** Table fields only: the fields of the child DocType. */
  fields?: FieldMeta[];
}

/** The badge tone a workflow state names (the same words as the badge tones). */
export type StateStyle = 'neutral' | 'success' | 'warning' | 'danger' | 'info';

export interface WorkflowState {
  name: string;
  docstatus: number;
  style: StateStyle;
}

/** The workflow of a DocType (M8.1). The roles of the transitions are never sent: see `/transitions`. */
export interface WorkflowMeta {
  states: WorkflowState[];
  transitions: { action: string; from: string; to: string }[];
}

export interface DocTypeMeta {
  name: string;
  label: string;
  app: string | null;
  icon: string | null;
  naming: string;
  submittable: boolean;
  /** The field that names a document for a person, or null. */
  title_field: string | null;
  /** The columns of the list after `name`, or null for the default. */
  list_fields: string[] | null;
  fields: FieldMeta[];
  rights: Rights;
  /** The workflow, or null (absent in older answers and test data) for a DocType without one. */
  workflow?: WorkflowMeta | null;
}

/** A listed document: the standard columns plus one key per field. */
export type DocumentRow = Record<string, unknown>;
