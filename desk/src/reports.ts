// Query reports (M8.4, ADR 0016): the shapes of the API, the parameter form (a report parameter
// is shown with the control of the field of the same kind), the address that holds the
// parameters (so that a reload runs the same report again) and the request body. Pure functions,
// tested without a DOM.

import { mapNamedDetails } from './errors';
import type { MappedErrors } from './errors';
import { emptyValue, toFormValue, toJsonValue } from './fields';
import type { Errors, FormValue, FormValues } from './fields';
import type { LinkTitles } from './linkTitles';
import type { FieldMeta, FieldType } from './types';

export type ParamKind = 'data' | 'int' | 'decimal' | 'date' | 'check' | 'link' | 'select';

export type ColumnKind = 'data' | 'int' | 'decimal' | 'date' | 'datetime' | 'check' | 'link';

export interface ReportParam {
  name: string;
  label: string;
  kind: ParamKind;
  required: boolean;
  /** The value the form starts with (the server uses it too when the parameter is absent). */
  default: unknown;
  /** Link parameters: the DocType the name belongs to. */
  doctype?: string;
  /** Select parameters. */
  options?: string[];
}

export interface ReportColumn {
  name: string;
  label: string;
  kind: ColumnKind;
  /** Link columns: the DocType the name belongs to. */
  doctype?: string;
}

/** A report the user may run, from `GET /api/v1/report`. The SQL is never sent. */
export interface ReportMeta {
  /** `<app>.<name>`. */
  name: string;
  app: string;
  label: string;
  params: ReportParam[];
  columns: ReportColumn[];
}

/** The answer of a run. */
export interface ReportResult {
  columns: ReportColumn[];
  rows: unknown[][];
  /** There were more rows than the report's limit; the rest was not returned. */
  truncated: boolean;
  titles: LinkTitles;
}

/** The form control that fits a parameter: the field of the same kind. */
export function paramField(param: ReportParam): FieldMeta {
  const type: FieldType = param.kind;
  return {
    name: param.name,
    label: param.label,
    type,
    required: param.required,
    unique: false,
    allow_on_submit: false,
    read_only: false,
    max_length: param.kind === 'data' ? 140 : undefined,
    options: param.kind === 'select' ? (param.options ?? []) : undefined,
    target: param.kind === 'link' ? param.doctype : undefined,
  };
}

/** What the address holds for a parameter that is not mentioned: nothing, the default of the report. */
function fromAddress(param: ReportParam, query: URLSearchParams): FormValue {
  const field = paramField(param);
  const text = query.get(param.name);
  if (text === null) return toFormValue(field, param.default);
  return param.kind === 'check' ? text === 'true' : text;
}

/** The values the form starts with: those in the address, else the report's defaults. */
export function initialValues(report: ReportMeta, query: URLSearchParams): FormValues {
  return Object.fromEntries(report.params.map((p) => [p.name, fromAddress(p, query)]));
}

/** True when the address holds anything to run: parameters, or the `run` marker of a report without any. */
export function hasRunQuery(query: URLSearchParams): boolean {
  return [...query.keys()].length > 0;
}

/**
 * The query part of the address for `values`: the parameters that have a value (a Check always,
 * `true` or `false`) and, for a report without parameters, `run=1` so that a reload runs it too.
 */
export function valuesQuery(report: ReportMeta, values: FormValues): URLSearchParams {
  const query = new URLSearchParams();
  for (const param of report.params) {
    const value = values[param.name] ?? emptyValue(paramField(param));
    if (param.kind === 'check') query.set(param.name, value === true ? 'true' : 'false');
    else if (typeof value === 'string' && value !== '') query.set(param.name, value);
  }
  if (report.params.length === 0) query.set('run', '1');
  return query;
}

export type Request = { ok: true; params: Record<string, unknown> } | { ok: false; errors: Errors };

/**
 * The `params` of the request: every parameter, an empty control as `null` (the user cleared it,
 * so the report's default does not come back), numbers and checks in their JSON kind. A value
 * that cannot be sent (text in a whole-number field) is an error on its field; everything else is
 * decided by the server.
 */
export function buildRequest(report: ReportMeta, values: FormValues): Request {
  const params: Record<string, unknown> = {};
  const errors: Errors = {};
  for (const param of report.params) {
    const field = paramField(param);
    const converted = toJsonValue(field, values[param.name] ?? emptyValue(field));
    if (converted.ok) params[param.name] = converted.value;
    else errors[param.name] = converted.error;
  }
  return Object.keys(errors).length > 0 ? { ok: false, errors } : { ok: true, params };
}

/** The `422` details of a run as messages on the parameters, and the details that name none. */
export function mapParamDetails(report: ReportMeta, details: string[]): MappedErrors {
  return mapNamedDetails(details, new Set(report.params.map((p) => p.name)));
}

/** The first parameter, in form order, that has an error. */
export function firstInvalid(report: ReportMeta, errors: Errors): string | null {
  return report.params.find((p) => p.name in errors)?.name ?? null;
}

/** What the page says when a run takes too long (`504 report_timeout`). */
export const TIMEOUT_MESSAGE = 'The report took too long. Narrow the parameters.';
