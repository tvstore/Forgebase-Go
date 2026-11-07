import { useEffect, useId, useMemo, useRef, useState } from 'react';
import type { FormEvent } from 'react';

import { isAddressable } from '../access';
import { ApiError, exportReport, runReport } from '../api';
import { ExportMenu } from '../components/ExportMenu';
import { useToast } from '../components/Toast';
import { saveDownload } from '../download';
import type { FileFormat } from '../transfer';
import { EmptyState } from '../components/EmptyState';
import { FieldControl } from '../components/FieldControl';
import { PageHeader } from '../components/PageHeader';
import { Skeleton } from '../components/Skeleton';
import type { Errors, FormValue, FormValues } from '../fields';
import { appLabel } from '../nav';
import { ariaSort, cellText, isNumericKind, nextSort, rowCountText, sortRows, truncatedText } from '../reportRows';
import type { SortState } from '../reportRows';
import {
  TIMEOUT_MESSAGE,
  buildRequest,
  firstInvalid,
  hasRunQuery,
  initialValues,
  mapParamDetails,
  paramField,
  valuesQuery,
} from '../reports';
import type { ReportMeta, ReportResult } from '../reports';
import { formHref, homeHref, navigate, reportHref } from '../router';
import type { Reports } from '../App';

interface Props {
  /** `<app>.<name>` from the address. */
  name: string;
  /** The parameters in the address: a reload with any of them runs the report again. */
  params: URLSearchParams;
  reports: Reports;
}

/** Looks the report up in the list of the ones the user may run, then shows it. */
export function Report({ name, params, reports }: Props) {
  if (reports.status === 'loading') {
    return (
      <div className="page">
        <PageHeader title={<h1>Report</h1>} />
        <div className="card" aria-busy="true">
          <Skeleton lines={3} />
        </div>
      </div>
    );
  }
  if (reports.status === 'error') {
    return (
      <div className="page">
        <PageHeader title={<h1>Report</h1>} />
        <p role="alert" className="alert">
          {reports.message}
        </p>
      </div>
    );
  }
  const report = reports.list.find((r) => r.name === name);
  if (report === undefined) {
    // A report that does not exist and one the user may not run look the same, as in the API.
    return (
      <div className="page">
        <PageHeader title={<h1>Report not found</h1>} />
        <p>
          This report does not exist, or you may not run it. <a href={homeHref()}>Go to the home page</a>
        </p>
      </div>
    );
  }
  return <ReportView key={report.name} report={report} params={params} />;
}

/** What a run came to. `want` says which request it answers (a run of the same address again has a new one). */
type Answer =
  | { want: string; status: 'ok'; result: ReportResult }
  | { want: string; status: 'invalid' }
  | { want: string; status: 'failed'; message: string; details: string[] };

const message = (e: unknown): string => (e instanceof ApiError ? e.message : 'Cannot reach the server.');

/**
 * The page of one report: the parameters in a card, "Run report", and the result in a card. The
 * address holds the parameters, so a reload (or Back) shows and runs the same report. A run starts
 * on a click on "Run report", or when the address already has parameters (a reload, a link): it
 * never starts by itself on a bare address.
 */
function ReportView({ report, params }: { report: ReportMeta; params: URLSearchParams }) {
  const query = params.toString();
  // The address says what to run; `rev` makes a second click with the same parameters run again.
  const [rev, setRev] = useState(0);
  const want = hasRunQuery(params) ? `${rev}:${query}` : null;
  // What the address asks for. One that cannot be sent (text in a whole-number parameter, typed
  // into the address) is not run: the form shows why.
  const request = useMemo(
    () => buildRequest(report, initialValues(report, new URLSearchParams(query))),
    [report, query],
  );
  const [values, setValues] = useState<FormValues>(() => initialValues(report, params));
  const [errors, setErrors] = useState<Errors>(() => (request.ok ? {} : request.errors));
  const [answer, setAnswer] = useState<Answer | null>(null);
  const [sort, setSort] = useState<SortState>(null);
  const [attempt, setAttempt] = useState(0);
  const [exportError, setExportError] = useState<string | null>(null);
  const toast = useToast();
  const idBase = useId();
  const alertRef = useRef<HTMLDivElement>(null);

  // Back, Forward or a link changed the address: the form shows what the address holds.
  const [seen, setSeen] = useState(query);
  if (seen !== query) {
    setSeen(query);
    setValues(initialValues(report, params));
    setErrors(request.ok ? {} : request.errors);
  }

  // Runs what the address holds. The request is built from the address, not from the form, so a
  // reload and a click do the same thing.
  useEffect(() => {
    if (want === null || !request.ok) return;
    let cancelled = false;
    runReport(report.name, request.params).then(
      (result) => {
        if (cancelled) return;
        setSort(null);
        setErrors({});
        setAnswer({ want, status: 'ok', result });
      },
      (e: unknown) => {
        if (cancelled) return;
        if (e instanceof ApiError && e.status === 422) {
          const mapped = mapParamDetails(report, e.details);
          setErrors(mapped.errors);
          setAnswer(
            mapped.unmapped.length > 0
              ? { want, status: 'failed', message: e.message, details: mapped.unmapped }
              : { want, status: 'invalid' },
          );
        } else if (e instanceof ApiError && e.code === 'report_timeout') {
          setAnswer({ want, status: 'failed', message: TIMEOUT_MESSAGE, details: [] });
        } else {
          setAnswer({ want, status: 'failed', message: message(e), details: [] });
        }
        setAttempt((n) => n + 1);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [report, want, request]);

  // After a refused run: the first parameter with a message, else the alert.
  useEffect(() => {
    if (attempt === 0) return;
    const name = firstInvalid(report, errors);
    const control = name === null ? null : document.getElementById(`${idBase}-${name}`);
    (control ?? alertRef.current)?.focus();
    // Only when an attempt fails; the messages are read as they are at that moment.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attempt]);

  const current = answer !== null && answer.want === want ? answer : null;
  const blocked = want !== null && !request.ok;
  const loading = want !== null && !blocked && current === null;
  const result = current?.status === 'ok' ? current.result : null;
  const rows = useMemo(
    () => (result === null ? [] : sortRows(result.rows, result.columns, sort, result.titles)),
    [result, sort],
  );

  /** Runs the report again as a file with the parameters of the result on screen, then saves it. */
  const exportAs = async (format: FileFormat) => {
    if (!request.ok) return;
    setExportError(null);
    try {
      const file = await exportReport(report.name, request.params, format);
      saveDownload(file.blob, file.filename);
      toast(file.truncated ? 'Exported. The report stopped at its row limit.' : 'Exported');
    } catch (e) {
      setExportError(message(e));
    }
  };

  const setValue = (field: string, value: FormValue) => {
    setValues((v) => ({ ...v, [field]: value }));
    setErrors((e) => (field in e ? Object.fromEntries(Object.entries(e).filter(([k]) => k !== field)) : e));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const request = buildRequest(report, values);
    if (!request.ok) {
      setErrors(request.errors);
      setAttempt((n) => n + 1);
      return;
    }
    setErrors({});
    setRev((n) => n + 1);
    navigate(reportHref(report.name, valuesQuery(report, values)));
  };

  const requiredShown = report.params.some((p) => p.required);
  const paramsId = `${idBase}-params`;
  const resultId = `${idBase}-result`;
  const failure = current?.status === 'failed' ? current : null;
  const live = loading ? 'Running the report...' : result !== null ? rowCountText(rows.length) : '';

  return (
    <div className="page">
      <PageHeader
        title={<h1>{report.label}</h1>}
        meta={<p className="subtitle">{appLabel(report.app)}</p>}
        actions={
          result !== null && request.ok ? (
            <ExportMenu heading="Export this result" onPick={(format) => void exportAs(format)} note="Runs the report again with these parameters." />
          ) : null
        }
      />
      <p className="visually-hidden" role="status" aria-live="polite">
        {live}
      </p>

      <form className="report-form" noValidate onSubmit={submit}>
        <section className="card details-card report-params" aria-labelledby={paramsId}>
          <h2 id={paramsId}>Parameters</h2>
          {requiredShown ? <p className="hint required-hint">* required</p> : null}
          {report.params.length === 0 ? <p className="hint">This report has no parameters.</p> : null}
          {report.params.length > 0 ? (
            <div className="fields">
              {report.params.map((param) => {
                const field = paramField(param);
                const id = `${idBase}-${param.name}`;
                const error = errors[param.name];
                const control = (
                  <FieldControl
                    id={id}
                    field={field}
                    value={values[param.name] ?? ''}
                    onChange={(value) => setValue(param.name, value)}
                    invalid={error !== undefined}
                    describedBy={error ? `${id}-error` : undefined}
                  />
                );
                const label = (
                  <label htmlFor={id}>
                    {param.label}
                    {param.required ? <span aria-hidden="true"> *</span> : null}
                  </label>
                );
                return (
                  <div key={param.name} className={param.kind === 'check' ? 'field field-check' : 'field'}>
                    {param.kind === 'check' ? (
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
                    {error ? (
                      <p id={`${id}-error`} className="field-error">
                        {error}
                      </p>
                    ) : null}
                  </div>
                );
              })}
            </div>
          ) : null}
          <div className="report-actions">
            <button type="submit" className="btn btn-primary" aria-disabled={loading || undefined}>
              Run report
            </button>
          </div>
        </section>
      </form>

      {exportError !== null ? (
        <p role="alert" className="alert">
          {exportError}
        </p>
      ) : null}

      {failure ? (
        <div role="alert" tabIndex={-1} ref={alertRef} className="alert">
          <p>{failure.message}</p>
          {failure.details.length > 0 ? (
            <ul>
              {failure.details.map((detail) => (
                <li key={detail}>{detail}</li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}

      {blocked || current?.status === 'invalid' || failure ? null : (
        <section className="card report-result" aria-labelledby={resultId} aria-busy={loading}>
          <div className="report-head">
            <h2 id={resultId}>Result</h2>
            {result !== null ? <p className="report-count">{rowCountText(rows.length)}</p> : null}
          </div>
          {want === null ? (
            <p className="hint report-idle">
              {report.params.length === 0 ? 'Run the report to see its rows.' : 'Set the parameters, then run the report.'}
            </p>
          ) : null}
          {loading ? (
            <div className="report-loading">
              <Skeleton lines={5} />
            </div>
          ) : null}
          {result !== null && result.rows.length === 0 ? (
            <EmptyState title="No rows">Nothing matches these parameters. Change them and run the report again.</EmptyState>
          ) : null}
          {result !== null && result.rows.length > 0 ? (
            <>
              <div className="table-wrap report-wrap">
                <table className="list-table report-table">
                  <caption className="visually-hidden">{report.label} result</caption>
                  <thead>
                    <tr>
                      {result.columns.map((column, index) => {
                        const sorted = ariaSort(sort, index);
                        return (
                          <th
                            key={column.name}
                            scope="col"
                            className={isNumericKind(column.kind) ? 'num' : undefined}
                            aria-sort={sorted}
                          >
                            <button type="button" className="sort" onClick={() => setSort((s) => nextSort(s, index))}>
                              {column.label}
                              {sorted ? <span aria-hidden="true">{sorted === 'descending' ? ' ▼' : ' ▲'}</span> : null}
                            </button>
                          </th>
                        );
                      })}
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((row, rowIndex) => (
                      <tr key={rowIndex}>
                        {result.columns.map((column, index) => {
                          const value = row[index];
                          const text = cellText(value, column, result.titles);
                          const link =
                            column.kind === 'link' &&
                            column.doctype !== undefined &&
                            typeof value === 'string' &&
                            isAddressable(value);
                          return (
                            <td key={column.name} className={isNumericKind(column.kind) ? 'num' : undefined}>
                              {link && column.doctype !== undefined && typeof value === 'string' ? (
                                <a href={formHref(column.doctype, value)}>{text}</a>
                              ) : (
                                text
                              )}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {result.truncated ? (
                <p className="report-note" role="status">
                  {truncatedText(result.rows.length)}
                </p>
              ) : null}
            </>
          ) : null}
        </section>
      )}
    </div>
  );
}
