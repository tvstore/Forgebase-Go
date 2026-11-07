import { useEffect, useId, useMemo, useState } from 'react';

import { ApiError, getSummary } from '../api';
import {
  AGGREGATE_FNS,
  MAX_VALUES,
  aggregatesParam,
  defaultGroupBy,
  defaultValues,
  distinctValues,
  groupFields,
  summaryCaption,
  summaryRows,
  truncatedNote,
  valueFields,
  valueHeading,
} from '../summary';
import type { AggregateFn, GroupField, SummaryResult, ValueChoice } from '../summary';
import type { DocTypeMeta } from '../types';
import { PlusIcon, XIcon } from '../icons';
import { Badge } from './Badge';
import { Skeleton } from './Skeleton';

interface Props {
  doctype: string;
  meta: DocTypeMeta;
  /** The `filters` of the list, search included: the summary counts what the list shows. */
  filters: string | undefined;
  /** A click on a group: the list is filtered to it and the panel closes. */
  onPick: (field: GroupField, key: unknown) => void;
  onClose: () => void;
}

type Answer = { key: string; result?: SummaryResult; error?: string };

const FN_LABEL: Record<AggregateFn, string> = { sum: 'Sum', avg: 'Average', min: 'Minimum', max: 'Maximum' };

/**
 * The group-by summary above the list: a field to group by, up to three values (the sum of a number
 * field by default; average, minimum and maximum can be chosen) and a compact table with one row per
 * group. It counts what the list shows (the same filters), through the same permission rules.
 */
export function SummaryPanel({ doctype, meta, filters, onPick, onClose }: Props) {
  const groups = useMemo(() => groupFields(meta), [meta]);
  const numbers = useMemo(() => valueFields(meta), [meta]);
  const [groupName, setGroupName] = useState(() => defaultGroupBy(meta)?.name ?? '');
  const [choices, setChoices] = useState<ValueChoice[]>(() => defaultValues(meta));
  const [answer, setAnswer] = useState<Answer | null>(null);
  const titleId = useId();
  const groupId = useId();

  const group = groups.find((g) => g.name === groupName) ?? groups[0];
  const values = useMemo(() => distinctValues(choices), [choices]);
  const aggregates = aggregatesParam(values);
  const key = `${doctype}|${group?.name ?? ''}|${aggregates}|${filters ?? ''}`;

  useEffect(() => {
    if (!group) return;
    let cancelled = false;
    getSummary(doctype, { groupBy: group.name, aggregates, ...(filters ? { filters } : {}) }).then(
      (result) => {
        if (!cancelled) setAnswer({ key, result });
      },
      (e: unknown) => {
        if (!cancelled) setAnswer({ key, error: e instanceof ApiError ? e.message : 'Cannot reach the server.' });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [doctype, group, aggregates, filters, key]);

  const loading = answer?.key !== key;
  const rows = answer?.result && group ? summaryRows(answer.result, group, values, numbers, meta) : [];
  const note = answer?.result ? truncatedNote(answer.result) : null;

  const change = (index: number, next: Partial<ValueChoice>) =>
    setChoices((list) => list.map((c, i) => (i === index ? { ...c, ...next } : c)));
  const firstFree = numbers.find((n) => !choices.some((c) => c.field === n.name && c.fn === 'sum')) ?? numbers[0];

  return (
    <section className="summary" aria-labelledby={titleId}>
      <div className="summary-head">
        <h2 id={titleId}>Summary</h2>
        <button type="button" className="btn btn-ghost btn-small" aria-label="Close summary" onClick={onClose}>
          <XIcon size={16} />
        </button>
      </div>

      <div className="summary-controls">
        <div className="field">
          <label htmlFor={groupId}>Group by</label>
          <select id={groupId} value={group?.name ?? ''} onChange={(e) => setGroupName(e.target.value)}>
            {groups.map((g) => (
              <option key={g.name} value={g.name}>
                {g.label}
              </option>
            ))}
          </select>
        </div>
        {numbers.length > 0 ? (
          <fieldset className="summary-values">
            <legend>Values</legend>
            {choices.map((choice, index) => (
              <div className="summary-value" key={index}>
                <select
                  aria-label={`Value ${index + 1}: field`}
                  value={choice.field}
                  onChange={(e) => change(index, { field: e.target.value })}
                >
                  {numbers.map((n) => (
                    <option key={n.name} value={n.name}>
                      {n.label}
                    </option>
                  ))}
                </select>
                <select
                  aria-label={`Value ${index + 1}: calculation`}
                  value={choice.fn}
                  onChange={(e) => change(index, { fn: e.target.value as AggregateFn })}
                >
                  {AGGREGATE_FNS.map((fn) => (
                    <option key={fn} value={fn}>
                      {FN_LABEL[fn]}
                    </option>
                  ))}
                </select>
                <button
                  type="button"
                  className="btn btn-ghost btn-small"
                  aria-label={`Remove value ${index + 1}`}
                  onClick={() => setChoices((list) => list.filter((_, i) => i !== index))}
                >
                  <XIcon size={14} />
                </button>
              </div>
            ))}
            {choices.length < MAX_VALUES && firstFree ? (
              <button
                type="button"
                className="pill"
                onClick={() => setChoices((list) => [...list, { field: firstFree.name, fn: 'sum' }])}
              >
                <PlusIcon size={14} />
                Add value
              </button>
            ) : null}
          </fieldset>
        ) : null}
      </div>

      {answer?.error && !loading ? (
        <p role="alert" className="alert">
          {answer.error}
        </p>
      ) : null}
      {!answer ? (
        <div className="skeleton-pad" aria-busy="true">
          <Skeleton lines={3} />
        </div>
      ) : answer.result && group ? (
        rows.length === 0 ? (
          <p className="menu-note">No documents to group.</p>
        ) : (
          <div className={loading ? 'table-wrap is-loading' : 'table-wrap'} aria-busy={loading}>
            <table className="summary-table">
              <caption className="visually-hidden">{summaryCaption(meta, group)}</caption>
              <thead>
                <tr>
                  <th scope="col">{group.label}</th>
                  <th scope="col" className="num">
                    Count
                  </th>
                  {values.map((choice) => (
                    <th scope="col" className="num" key={`${choice.fn}_${choice.field}`}>
                      {valueHeading(choice, numbers)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr
                    key={`${typeof row.key}:${String(row.key)}`}
                    className="row-link"
                    onClick={(event) => {
                      // A convenience for the mouse: the button in the first cell is the real control.
                      if (!(event.target instanceof Element && event.target.closest('button'))) onPick(group, row.key);
                    }}
                  >
                    <th scope="row">
                      <button type="button" className="summary-group" title="Show these documents" onClick={() => onPick(group, row.key)}>
                        {row.badge ? <Badge tone={row.badge.tone}>{row.badge.label}</Badge> : row.label}
                      </button>
                    </th>
                    <td className="num">{row.count}</td>
                    {row.values.map((text, i) => (
                      <td className="num" key={i}>
                        {text}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
      ) : null}
      {note ? <p className="hint summary-note">{note}</p> : null}
    </section>
  );
}
