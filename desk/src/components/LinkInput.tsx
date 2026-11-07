import { useEffect, useId, useState } from 'react';

import { listDocuments } from '../api';
import { suggestionParams } from '../links';
import { linkTitle } from '../linkTitles';
import type { LinkTitles } from '../linkTitles';

interface Props {
  id: string;
  /** The target DocType of the Link field. */
  target: string;
  value: string;
  onChange: (value: string) => void;
  /** The titles of linked documents: the title of the typed name is shown under the input. */
  titles?: LinkTitles;
  required: boolean;
  invalid: boolean;
  describedBy?: string;
  ariaLabel?: string;
}

/** Wait this long after the last keystroke before asking the server for suggestions. */
const DEBOUNCE_MS = 250;

/**
 * A Link field: a text input with the native suggestion list (`<input list>` and `<datalist>`),
 * and the target's title under it when the API gave one for the typed name.
 * The suggestions are the target's names that start with what was typed (`suggestionParams`);
 * the user may type any name and the server decides. A target the user cannot read (403) or any
 * other failure simply gives no suggestions.
 */
export function LinkInput({ id, target, value, onChange, titles, required, invalid, describedBy, ariaLabel }: Props) {
  const listId = useId();
  const [names, setNames] = useState<string[]>([]);
  const [focused, setFocused] = useState(false);

  useEffect(() => {
    if (!focused) return;
    let cancelled = false;
    const timer = window.setTimeout(() => {
      listDocuments(target, suggestionParams(value)).then(
        (rows) => {
          if (!cancelled) setNames(rows.map((row) => String(row.name ?? '')).filter((n) => n !== ''));
        },
        () => {
          if (!cancelled) setNames([]);
        },
      );
    }, DEBOUNCE_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [target, value, focused]);

  const title = titles ? linkTitle(titles, target, value) : undefined;

  return (
    <>
      <input
        id={id}
        type="text"
        list={listId}
        value={value}
        aria-required={required || undefined}
        aria-invalid={invalid || undefined}
        aria-describedby={describedBy}
        aria-label={ariaLabel}
        autoComplete="off"
        onChange={(e) => onChange(e.target.value)}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
      />
      <datalist id={listId}>
        {names.map((name) => (
          <option key={name} value={name} />
        ))}
      </datalist>
      {title === undefined ? null : <span className="link-title">{title}</span>}
    </>
  );
}
