import { useEffect, useRef } from 'react';

import { FieldControl } from './FieldControl';
import { ReadOnlyCell } from './ReadOnlyValue';
import { PlusIcon } from '../icons';
import type { LinkTitles } from '../linkTitles';
import { errorKey, blankRow } from '../fields';
import type { Errors, FormRow, FormValue } from '../fields';
import { focusAfterRemove } from '../filters';
import { isNumeric } from '../listing';
import type { FieldMeta } from '../types';

interface Props {
  /** The Table field; `field.fields` are the columns. */
  field: FieldMeta;
  rows: FormRow[];
  onChange: (rows: FormRow[]) => void;
  readOnly: boolean;
  errors: Errors;
  /** The titles of the documents the rows link to. */
  titles: LinkTitles;
  /** The prefix of the element ids (unique per form). */
  idBase: string;
}

/** Where focus goes after the rows changed (set by the buttons, applied after the render). */
type Focus = { kind: 'cell'; row: number } | { kind: 'remove'; row: number } | { kind: 'add' };

/**
 * The rows of a Table field as a native `<table>` inside its card: one row per child row (the
 * order is the display order), the editable child fields as the same native controls, a read-only
 * or locked value as plain text (numbers on the right), "Add row" and a "Remove row N" button per
 * row. Every control is named by its column and row; a problem of a cell is linked with
 * `aria-describedby`. Child tables cannot nest, so no cell is a table.
 */
export function TableGrid({ field, rows, onChange, readOnly, errors, titles, idBase }: Props) {
  const columns = field.fields ?? [];
  const wrap = useRef<HTMLDivElement>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  const pending = useRef<Focus | null>(null);

  // Runs after every render; acts only when a button asked for a focus move.
  useEffect(() => {
    const focus = pending.current;
    pending.current = null;
    if (!focus) return;
    if (focus.kind === 'add') addButton.current?.focus();
    else {
      const tr = wrap.current?.querySelectorAll('tbody tr')[focus.row];
      const target =
        focus.kind === 'cell'
          ? tr?.querySelector<HTMLElement>('input:not([disabled]), select:not([disabled]), textarea')
          : tr?.querySelector<HTMLElement>('button.remove');
      target?.focus();
    }
  });

  const setCell = (index: number, name: string, value: FormValue) =>
    onChange(rows.map((row, i) => (i === index ? { ...row, values: { ...row.values, [name]: value } } : row)));

  const tableError = errors[errorKey(field.name)];
  const errorId = `${idBase}-error`;

  return (
    <>
      {rows.length > 0 ? (
        <div className="table-wrap" ref={wrap}>
          <table className="grid-table">
            <caption className="visually-hidden">{field.label}</caption>
            <thead>
              <tr>
                <th scope="col">Row</th>
                {columns.map((c) => (
                  <th key={c.name} scope="col" className={isNumeric(c.type) ? 'num' : undefined}>
                    {c.label}
                    {c.required && !readOnly && !c.read_only ? <span aria-hidden="true"> *</span> : null}
                  </th>
                ))}
                {readOnly ? null : (
                  <th scope="col" className="remove-cell">
                    <span className="visually-hidden">Remove</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, index) => (
                <tr key={row.key}>
                  <th scope="row">{index + 1}</th>
                  {columns.map((c) => {
                    const key = errorKey(field.name, index + 1, c.name);
                    const message = errors[key];
                    const cellId = `${idBase}-${row.key}-${c.name}`;
                    if (readOnly || c.read_only) {
                      return (
                        <ReadOnlyCell
                          key={c.name}
                          id={cellId}
                          field={c}
                          value={row.values[c.name] ?? ''}
                          titles={titles}
                          message={message}
                        />
                      );
                    }
                    return (
                      <td key={c.name} className={isNumeric(c.type) ? 'num' : undefined}>
                        <FieldControl
                          id={cellId}
                          field={c}
                          value={row.values[c.name] ?? ''}
                          onChange={(value) => setCell(index, c.name, value)}
                          titles={titles}
                          invalid={message !== undefined}
                          describedBy={message ? `${cellId}-error` : undefined}
                          ariaLabel={`${c.label}, row ${index + 1}`}
                        />
                        {message ? (
                          <p id={`${cellId}-error`} className="field-error">
                            {message}
                          </p>
                        ) : null}
                      </td>
                    );
                  })}
                  {readOnly ? null : (
                    <td className="remove-cell">
                      <button
                        type="button"
                        className="btn btn-ghost btn-small remove"
                        aria-label={`Remove row ${index + 1}`}
                        onClick={() => {
                          onChange(rows.filter((_, i) => i !== index));
                          const next = focusAfterRemove(index, rows.length);
                          pending.current = next.kind === 'add' ? { kind: 'add' } : { kind: 'remove', row: next.index };
                        }}
                      >
                        Remove
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="hint table-empty">{readOnly ? 'No rows.' : 'No rows yet.'}</p>
      )}
      {tableError ? (
        <p id={errorId} className="field-error table-empty">
          {tableError}
        </p>
      ) : null}
      {readOnly ? null : (
        <div className="table-foot">
          <button
            type="button"
            ref={addButton}
            className="btn btn-small"
            aria-describedby={tableError ? errorId : undefined}
            onClick={() => {
              onChange([...rows, blankRow(columns)]);
              pending.current = { kind: 'cell', row: rows.length };
            }}
          >
            <PlusIcon size={14} />
            Add row
          </button>
        </div>
      )}
    </>
  );
}
