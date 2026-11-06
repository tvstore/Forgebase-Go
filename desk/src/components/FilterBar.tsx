import { useEffect, useMemo, useRef, useState } from 'react';

import { NO_VALUE_OPS, buildFilters, focusAfterRemove, opsFor } from '../filters';
import type { FilterField, FilterRow, FocusTarget, Op } from '../filters';

interface Props {
  fields: FilterField[];
  /** The rows of the address; when `initialKey` changes the bar starts again from them. */
  initial: FilterRow[];
  /** Identifies `initial` (the `filters` text of the address). */
  initialKey: string;
  /** Called with the `filters` JSON text, or null for no filter. */
  onApply: (filters: string | null) => void;
}

/** How long after the last change the filters are applied. */
export const APPLY_DELAY_MS = 400;

/** A yes/no field always shows a choice, so it starts with one. */
const defaultValue = (field: FilterField): string => (field.type === 'check' ? 'true' : '');

function newRow(fields: FilterField[]): FilterRow | null {
  const field = fields[0];
  const op = field ? opsFor(field.type)[0] : undefined;
  return field && op ? { field: field.name, op, value: defaultValue(field) } : null;
}

/** The `filters` text of complete rows ('' for none); null while a row is incomplete or wrong. */
function textOf(rows: FilterRow[], fields: FilterField[]): string | null {
  const built = buildFilters(rows, fields);
  if (!built.ok) return null;
  return built.filters.length === 0 ? '' : JSON.stringify(built.filters);
}

/**
 * A filter bar: add and remove rows of field, operator and value. There is no Apply button: a
 * short moment after the last change, the rows are applied when they are all complete; while one
 * is not (a new row has no value yet) the bar says what is missing and nothing is applied.
 */
export function FilterBar({ fields, initial, initialKey, onApply }: Props) {
  const [rows, setRows] = useState<FilterRow[]>(initial);
  // The `filters` text the address holds, or that this bar applied last.
  const [applied, setApplied] = useState(() => textOf(initial, fields) ?? initialKey);
  // Follow the address (back button, a new link) without remounting the form, which would drop
  // the keyboard focus: adjust the state while rendering when `initialKey` changed, unless the
  // change is the bar's own.
  const [seenKey, setSeenKey] = useState(initialKey);
  if (seenKey !== initialKey) {
    setSeenKey(initialKey);
    const fromAddress = textOf(initial, fields) ?? initialKey;
    if (fromAddress !== applied) {
      setRows(initial);
      setApplied(fromAddress);
    }
  }

  const built = useMemo(() => buildFilters(rows, fields), [rows, fields]);
  const text = textOf(rows, fields);
  const hint = built.ok ? null : `Not applied yet: ${built.error}.`;

  const latest = useRef(onApply);
  useEffect(() => {
    latest.current = onApply;
  });
  useEffect(() => {
    if (text === null || text === applied) return;
    const timer = window.setTimeout(() => {
      setApplied(text);
      latest.current(text === '' ? null : text);
    }, APPLY_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [text, applied]);

  // Keyboard focus after Remove (its button is gone) and Add filter.
  const form = useRef<HTMLFormElement>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  const focusTarget = useRef<FocusTarget | null>(null);
  useEffect(() => {
    const target = focusTarget.current;
    focusTarget.current = null;
    if (target === null) return;
    if (target.kind === 'add') addButton.current?.focus();
    else form.current?.querySelector<HTMLElement>(`[data-row="${target.index}"] select`)?.focus();
  }, [rows]);

  const change = (index: number, patch: Partial<FilterRow>) =>
    setRows((current) => current.map((row, i) => (i === index ? { ...row, ...patch } : row)));

  const changeField = (index: number, name: string) => {
    const field = fields.find((f) => f.name === name);
    const op = field ? opsFor(field.type)[0] : undefined;
    if (field && op) change(index, { field: name, op, value: defaultValue(field) });
  };

  const remove = (index: number) => {
    focusTarget.current = focusAfterRemove(index, rows.length);
    setRows((current) => current.filter((_, i) => i !== index));
  };

  const add = () => {
    const row = newRow(fields);
    if (!row) return;
    focusTarget.current = { kind: 'row', index: rows.length };
    setRows((current) => [...current, row]);
  };

  return (
    <form className="filters" ref={form} onSubmit={(e) => e.preventDefault()}>
      <fieldset>
        <legend>Filters</legend>
        {rows.map((row, index) => {
          const field = fields.find((f) => f.name === row.field);
          const number = index + 1;
          return (
            <div className="filter-row" key={index} data-row={index}>
              <select
                aria-label={`Filter ${number} field`}
                value={row.field}
                onChange={(e) => changeField(index, e.target.value)}
              >
                {fields.map((f) => (
                  <option key={f.name} value={f.name}>
                    {f.label}
                  </option>
                ))}
              </select>
              <select
                aria-label={`Filter ${number} operator`}
                value={row.op}
                onChange={(e) =>
                  change(index, {
                    op: e.target.value as Op,
                    value: row.value || (field ? defaultValue(field) : ''),
                  })
                }
              >
                {(field ? opsFor(field.type) : []).map((op) => (
                  <option key={op} value={op}>
                    {op}
                  </option>
                ))}
              </select>
              {NO_VALUE_OPS.includes(row.op) ? null : (
                <ValueInput
                  field={field}
                  row={row}
                  label={`Filter ${number} value`}
                  onChange={(value) => change(index, { value })}
                />
              )}
              <button
                type="button"
                className="btn btn-ghost btn-small"
                aria-label={`Remove filter ${number}`}
                onClick={() => remove(index)}
              >
                Remove
              </button>
            </div>
          );
        })}
        <div className="filter-actions">
          <button type="button" className="btn btn-small" ref={addButton} onClick={add}>
            <span aria-hidden="true">+ </span>Add filter
          </button>
          {hint ? <span className="hint">{hint}</span> : null}
        </div>
      </fieldset>
    </form>
  );
}

interface ValueProps {
  field: FilterField | undefined;
  row: FilterRow;
  label: string;
  onChange: (value: string) => void;
}

function ValueInput({ field, row, label, onChange }: ValueProps) {
  const single = row.op === '=' || row.op === '!=';
  if (field?.type === 'check') {
    return (
      <select aria-label={label} value={row.value} onChange={(e) => onChange(e.target.value)}>
        <option value="true">Yes</option>
        <option value="false">No</option>
      </select>
    );
  }
  if (field?.type === 'select' && field.options && single) {
    return (
      <select aria-label={label} value={row.value} onChange={(e) => onChange(e.target.value)}>
        <option value="">(choose)</option>
        {field.options.map((option) => (
          <option key={option} value={option}>
            {option}
          </option>
        ))}
      </select>
    );
  }
  const type = field?.type === 'date' && row.op !== 'in' ? 'date' : 'text';
  return (
    <input
      aria-label={label}
      type={type}
      value={row.value}
      placeholder={row.op === 'in' ? 'a, b, c' : row.op === 'like' ? 'pattern, % matches any text' : ''}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}
