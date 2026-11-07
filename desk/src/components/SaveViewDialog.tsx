import { useId, useRef, useState } from 'react';
import type { FormEvent } from 'react';

import { MAX_LABEL_LENGTH, labelError } from '../views';
import { Modal } from './Dialog';

interface Props {
  /** The DocType's label, for the title. */
  doctype: string;
  /** An Administrator may share the view with everyone. */
  canShare: boolean;
  busy: boolean;
  /** The server's answer when the last try failed (a name that is taken, a limit). */
  error: string | null;
  onSave: (label: string, shared: boolean) => void;
  onCancel: () => void;
}

/** "Save view": a name, and for an Administrator the choice to share it with everyone. */
export function SaveViewDialog({ doctype, canShare, busy, error, onSave, onCancel }: Props) {
  const [label, setLabel] = useState('');
  const [shared, setShared] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const inputId = useId();
  const errorId = useId();
  const shownError = problem ?? error;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    const wrong = labelError(label);
    setProblem(wrong);
    if (wrong === null) onSave(label.trim(), canShare && shared);
    else input.current?.focus();
  };

  return (
    <Modal className="dialog" labelledBy={titleId} onClose={onCancel} initialFocus={input}>
      <form onSubmit={submit} noValidate>
        <h2 id={titleId}>Save view</h2>
        <div className="dialog-body">
          <p className="hint">
            Saves the filters, sort order and columns of this {doctype} list, so you can open them again.
          </p>
          <div className="field">
            <label htmlFor={inputId}>Name</label>
            <input
              id={inputId}
              ref={input}
              type="text"
              value={label}
              maxLength={MAX_LABEL_LENGTH}
              autoComplete="off"
              aria-invalid={shownError !== null ? true : undefined}
              aria-describedby={shownError !== null ? errorId : undefined}
              onChange={(e) => {
                setLabel(e.target.value);
                setProblem(null);
              }}
            />
            {shownError !== null ? (
              <p id={errorId} role="alert" className="field-error">
                {shownError}
              </p>
            ) : null}
          </div>
          {canShare ? (
            <div className="field field-check">
              <input id={`${inputId}-shared`} type="checkbox" checked={shared} onChange={(e) => setShared(e.target.checked)} />
              <label htmlFor={`${inputId}-shared`}>Shared with everyone</label>
            </div>
          ) : null}
        </div>
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onCancel}>
            Cancel
          </button>
          <button type="submit" className="btn btn-primary" aria-disabled={busy || undefined}>
            Save
          </button>
        </div>
      </form>
    </Modal>
  );
}
