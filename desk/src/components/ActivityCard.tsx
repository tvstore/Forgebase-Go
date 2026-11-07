import { useState } from 'react';

import type { HistoryEntry } from '../api';
import { exactTime, relativeTime } from '../time';
import { historyView } from '../workflow';

/** What the card knows about the history of the document. */
export type HistoryState =
  | { status: 'loading' }
  | { status: 'ready'; entries: HistoryEntry[] }
  | { status: 'error' };

interface Props {
  history: HistoryState;
  /** The clock of the relative times. */
  now: Date;
  onRetry: () => void;
}

/**
 * The workflow history in the info panel: a vertical timeline, newest at the top, each entry with
 * who took which action, the move between two states and when (relative, with the exact local time
 * on hover). The first 20 entries are listed; "Show all" expands to everything the API returned.
 * The dots and connectors are decoration (`aria-hidden`); the list is the content.
 */
export function ActivityCard({ history, now, onRetry }: Props) {
  const [expanded, setExpanded] = useState(false);
  const view = history.status === 'ready' ? historyView(history.entries, expanded) : null;
  return (
    <section className="card activity" aria-labelledby="activity-heading">
      <h2 id="activity-heading">Activity</h2>
      {history.status === 'error' ? (
        <p className="hint">
          Cannot load the history.{' '}
          <button type="button" className="btn btn-small" onClick={onRetry}>
            Retry
          </button>
        </p>
      ) : view === null ? (
        <p className="hint">Loading...</p>
      ) : view.items.length === 0 ? (
        <p className="hint">No transitions yet.</p>
      ) : (
        <>
          <ol className="timeline" aria-label="Workflow history">
            {view.items.map((entry, index) => (
              <li key={`${entry.at}-${view.items.length - index}`}>
                <span className="timeline-dot" aria-hidden="true" />
                <p className="timeline-text">
                  <span className="timeline-actor">{entry.actor}</span> {entry.action}
                </p>
                <p className="timeline-move">
                  {entry.from} → {entry.to}
                </p>
                <time dateTime={entry.at} title={exactTime(entry.at)}>
                  {relativeTime(entry.at, now)}
                </time>
              </li>
            ))}
          </ol>
          {view.hidden > 0 ? (
            <button type="button" className="btn btn-small" onClick={() => setExpanded(true)}>
              Show all ({view.hidden + view.items.length})
            </button>
          ) : null}
          {view.capped ? <p className="hint">Only the latest 500 transitions are kept here.</p> : null}
        </>
      )}
    </section>
  );
}
