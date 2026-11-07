import { useId, useMemo, useRef, useState, useSyncExternalStore } from 'react';

import { findCommands, flatten, moveActive } from '../command';
import { SearchIcon } from '../icons';
import { getRecent, subscribeRecent } from '../recent';
import type { ReportMeta } from '../reports';
import { navigate } from '../router';
import type { DocTypeSummary } from '../types';
import { Modal } from './Dialog';

interface Props {
  doctypes: DocTypeSummary[];
  /** The reports the user may run ("Run <report>"). */
  reports: ReportMeta[];
  /** Closes the bar (Escape, a click outside, or after a result was chosen). */
  onClose: () => void;
}

/**
 * The command bar: a modal with a text input and a list of results (the ARIA combobox and
 * listbox pattern). The input keeps the focus; ArrowUp and ArrowDown move the active option
 * (`aria-activedescendant`), Enter opens it, Escape closes the bar and gives the focus back to
 * where it was. Navigation only: it opens a list, a new document, a recent document or a report.
 */
export function CommandBar({ doctypes, reports, onClose }: Props) {
  const [input, setInput] = useState('');
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const baseId = useId();
  const listId = `${baseId}-list`;
  // Follows the list: a form that finishes loading just after the bar opened is listed too.
  const recent = useSyncExternalStore(subscribeRecent, getRecent);
  const groups = useMemo(() => findCommands(input, doctypes, recent, reports), [input, doctypes, recent, reports]);
  const commands = useMemo(() => flatten(groups), [groups]);
  const optionId = (index: number) => `${baseId}-option-${index}`;
  const current = commands[active];

  const open = (href: string) => {
    onClose();
    navigate(href);
  };

  let index = 0;
  return (
    <Modal className="command" label="Search or jump to" onClose={onClose} initialFocus={inputRef} closeOnBackdrop>
      <div className="command-input">
        <SearchIcon size={18} className="icon" />
        <input
          ref={inputRef}
          type="text"
          role="combobox"
          aria-label="Search or jump to"
          aria-expanded="true"
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={current ? optionId(active) : undefined}
          autoComplete="off"
          spellCheck={false}
          placeholder="Search or jump to..."
          value={input}
          onChange={(e) => {
            setInput(e.target.value);
            setActive(0);
          }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
              e.preventDefault();
              setActive((a) => moveActive(a, e.key === 'ArrowDown' ? 1 : -1, commands.length));
            } else if (e.key === 'Enter' && current) {
              e.preventDefault();
              open(current.href);
            }
          }}
        />
      </div>
      <div id={listId} role="listbox" aria-label="Results" className="command-list">
        {groups.map((group) => {
          const titleId = `${baseId}-group-${group.title}`;
          return (
            <div key={group.title} role="group" aria-labelledby={titleId} className="command-group">
              <div id={titleId} role="presentation" className="command-group-title">
                {group.title}
              </div>
              {group.commands.map((command) => {
                const i = index++;
                return (
                  <div
                    key={command.id}
                    id={optionId(i)}
                    role="option"
                    aria-selected={i === active}
                    className="command-option"
                    // Not a tab stop: the input keeps the focus and points at the active option.
                    onMouseMove={() => setActive(i)}
                    // Keep the focus in the input when the mouse goes down on an option.
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => open(command.href)}
                  >
                    <span>{command.label}</span>
                    {command.hint ? <span className="command-hint">{command.hint}</span> : null}
                  </div>
                );
              })}
            </div>
          );
        })}
      </div>
      {commands.length === 0 ? (
        <p className="command-empty" role="status">
          No matches.
        </p>
      ) : null}
    </Modal>
  );
}
