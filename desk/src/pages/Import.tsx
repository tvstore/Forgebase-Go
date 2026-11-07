import { useEffect, useId, useState } from 'react';
import type { ChangeEvent } from 'react';

import { ApiError, getDocTypeMeta, importFile } from '../api';
import { PageHeader } from '../components/PageHeader';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toast';
import { listHref } from '../router';
import {
  FORMAT_LABELS,
  canImport,
  canUpsert,
  detectFormat,
  errorHeadline,
  mayImport,
  moreErrorsText,
  rowErrorText,
  rowLabel,
  summaryText,
  validationKey,
} from '../transfer';
import type { FileFormat, ImportMode, ImportSummary, Validation } from '../transfer';
import type { DocTypeMeta } from '../types';

interface Props {
  doctype: string;
}

type MetaState = { meta?: DocTypeMeta; error?: string };

const message = (e: unknown): string => (e instanceof ApiError ? e.message : 'Cannot reach the server.');

/** Loads the DocType, then shows the page when the user may import into it. */
export function Import({ doctype }: Props) {
  const [state, setState] = useState<MetaState | null>(null);
  useEffect(() => {
    let cancelled = false;
    getDocTypeMeta(doctype).then(
      (meta) => {
        if (!cancelled) setState({ meta });
      },
      (e: unknown) => {
        if (!cancelled) setState({ error: message(e) });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [doctype]);

  if (state === null) {
    return (
      <div className="page" aria-busy="true">
        <PageHeader title={<h1>Import</h1>} />
        <Skeleton lines={4} />
      </div>
    );
  }
  if (state.meta === undefined) {
    return (
      <div className="page">
        <PageHeader title={<h1>Cannot open this page</h1>} />
        <p role="alert" className="alert">
          {state.error}
        </p>
      </div>
    );
  }
  if (!canImport(state.meta.rights)) {
    return (
      <div className="page">
        <PageHeader title={<h1>Import {state.meta.label}</h1>} />
        <p role="alert" className="alert">
          You may not import {state.meta.label} documents.
        </p>
        <p>
          <a href={listHref(doctype)}>Back to {state.meta.label}</a>
        </p>
      </div>
    );
  }
  return <ImportView doctype={doctype} meta={state.meta} />;
}

/** A file-level refusal: the file cannot be imported at all, or the server could not be reached. */
interface Refusal {
  message: string;
  details: string[];
}

function ImportView({ doctype, meta }: { doctype: string; meta: DocTypeMeta }) {
  const toast = useToast();
  const idBase = useId();
  const [file, setFile] = useState<File | null>(null);
  const [format, setFormat] = useState<FileFormat>('csv');
  const [mode, setMode] = useState<ImportMode>('insert');
  const [busy, setBusy] = useState<'validate' | 'import' | null>(null);
  const [validation, setValidation] = useState<Validation | null>(null);
  const [refusal, setRefusal] = useState<Refusal | null>(null);
  const [done, setDone] = useState<ImportSummary | null>(null);
  const [inputKey, setInputKey] = useState(0);

  const key = file === null ? '' : validationKey(file, format, mode);
  const upsertAllowed = canUpsert(meta.rights);
  const ready = file !== null && busy === null;
  const canRun = ready && mayImport(validation, key);
  // The result on screen is that of the file and options as they are now.
  const shown = validation !== null && validation.key === key ? validation.summary : null;

  const reset = () => {
    setValidation(null);
    setRefusal(null);
    setDone(null);
  };

  const pick = (event: ChangeEvent<HTMLInputElement>) => {
    const next = event.target.files?.[0] ?? null;
    setFile(next);
    if (next !== null) setFormat(detectFormat(next.name) ?? format);
    reset();
  };

  const send = async (dryRun: boolean) => {
    if (file === null || busy !== null) return;
    setBusy(dryRun ? 'validate' : 'import');
    setRefusal(null);
    try {
      const answer = await importFile(doctype, file, { format, mode, dryRun });
      if (dryRun || !answer.ok) {
        // A failed real import changed nothing: the errors are shown like those of a validation.
        setValidation({ key, summary: answer.summary });
        if (!dryRun) setRefusal({ message: 'Nothing was imported. Fix the rows below and validate again.', details: [] });
      } else {
        setValidation(null);
        setDone(answer.summary);
        toast(`Imported: ${summaryText(answer.summary)}`);
      }
    } catch (e) {
      setValidation(null);
      setRefusal({ message: message(e), details: e instanceof ApiError ? e.details : [] });
    } finally {
      setBusy(null);
    }
  };

  const another = () => {
    setFile(null);
    setInputKey((n) => n + 1);
    reset();
  };

  const fileId = `${idBase}-file`;
  const formatId = `${idBase}-format`;
  const modeId = `${idBase}-mode`;
  const live = busy === 'validate' ? 'Validating the file...' : busy === 'import' ? 'Importing...' : '';
  const more = shown === null ? null : moreErrorsText(shown);

  return (
    <div className="page">
      <PageHeader
        title={<h1>Import {meta.label}</h1>}
        actions={
          <a className="btn" href={listHref(doctype)}>
            Back to list
          </a>
        }
      />
      <p className="visually-hidden" role="status" aria-live="polite">
        {live}
      </p>

      {done === null ? (
        <section className="card details-card" aria-labelledby={`${idBase}-file-heading`}>
          <h2 id={`${idBase}-file-heading`}>Upload a file</h2>
          <p className="hint import-hint">
            CSV: the first row names the fields (a <code>name</code> column is allowed); an empty cell is left out, or
            cleared when updating. JSON: an array of documents, with child rows. Nothing is imported until every row is
            fine.
          </p>
          <div className="fields">
            <div className="field field-wide">
              <label htmlFor={fileId}>File</label>
              <input key={inputKey} id={fileId} type="file" accept=".csv,.json,text/csv,application/json" onChange={pick} />
            </div>
            <div className="field">
              <label htmlFor={formatId}>Format</label>
              <select
                id={formatId}
                value={format}
                onChange={(e) => {
                  setFormat(e.target.value === 'json' ? 'json' : 'csv');
                  reset();
                }}
              >
                {(['csv', 'json'] as const).map((f) => (
                  <option key={f} value={f}>
                    {FORMAT_LABELS[f]}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor={modeId}>Mode</label>
              <select
                id={modeId}
                value={mode}
                onChange={(e) => {
                  setMode(e.target.value === 'upsert' ? 'upsert' : 'insert');
                  reset();
                }}
              >
                <option value="insert">Insert new documents</option>
                <option value="upsert" disabled={!upsertAllowed}>
                  Update by name, insert the rest{upsertAllowed ? '' : ' (needs the write right)'}
                </option>
              </select>
            </div>
          </div>
          <div className="report-actions">
            <button type="button" className="btn" aria-disabled={!ready || undefined} onClick={() => void send(true)}>
              Validate
            </button>
            <button
              type="button"
              className="btn btn-primary"
              aria-disabled={!canRun || undefined}
              title={canRun ? undefined : 'Validate the file first: it must have no errors'}
              onClick={() => {
                if (canRun) void send(false);
              }}
            >
              Import
            </button>
          </div>
        </section>
      ) : (
        <section className="card details-card import-done" aria-labelledby={`${idBase}-done-heading`}>
          <h2 id={`${idBase}-done-heading`}>Import finished</h2>
          <p role="status">{summaryText(done)}</p>
          <div className="report-actions">
            <a className="btn btn-primary" href={listHref(doctype)}>
              Back to {meta.label}
            </a>
            <button type="button" className="btn" onClick={another}>
              Import another file
            </button>
          </div>
        </section>
      )}

      {refusal !== null ? (
        <div role="alert" className="alert">
          <p>{refusal.message}</p>
          {refusal.details.length > 0 ? (
            <ul>
              {refusal.details.map((detail, index) => (
                <li key={`${index}-${detail}`}>{detail}</li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}

      {shown !== null && shown.errorCount === 0 ? (
        <section className="card details-card import-result" aria-labelledby={`${idBase}-result-heading`}>
          <h2 id={`${idBase}-result-heading`}>Validation</h2>
          <p role="status" className="import-ok">
            No errors. {summaryText(shown)} Press Import to apply it.
          </p>
        </section>
      ) : null}

      {shown !== null && shown.errorCount > 0 ? (
        <section className="card report-result import-result" aria-labelledby={`${idBase}-result-heading`}>
          <div className="report-head">
            <h2 id={`${idBase}-result-heading`}>Errors</h2>
          </div>
          <p role="alert" className="import-headline">
            {errorHeadline(shown)}
          </p>
          <div className="table-wrap report-wrap" role="region" aria-label="Rows with errors" tabIndex={0}>
            <table className="list-table report-table">
              <caption className="visually-hidden">Rows with errors</caption>
              <thead>
                <tr>
                  <th scope="col" className="num">
                    Row
                  </th>
                  <th scope="col">Name</th>
                  <th scope="col">Error</th>
                </tr>
              </thead>
              <tbody>
                {shown.errors.map((error) => (
                  <tr key={error.row}>
                    <td className="num">{rowLabel(error)}</td>
                    <td>{error.name ?? ''}</td>
                    <td className="import-message">{rowErrorText(error)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {more !== null ? <p className="report-note">{more}</p> : null}
        </section>
      ) : null}
    </div>
  );
}
