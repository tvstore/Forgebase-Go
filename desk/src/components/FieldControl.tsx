import type { FormValue } from '../fields';
import type { LinkTitles } from '../linkTitles';
import type { FieldMeta } from '../types';
import { LinkInput } from './LinkInput';

interface Props {
  id: string;
  field: FieldMeta;
  value: FormValue;
  onChange: (value: FormValue) => void;
  invalid: boolean;
  /** The titles of the documents the form links to (shown under a Link field). */
  titles?: LinkTitles;
  /** The id of the element with the error message, when there is one. */
  describedBy?: string;
  /** Names the control when no `<label for>` does (a cell of a table: the column and the row). */
  ariaLabel?: string;
}

/**
 * The native control of an editable field (never a Table field, see TableGrid). Required is
 * marked with `aria-required`, not the `required` attribute: the form has `noValidate`, the
 * server decides and its message is shown on the field. A read-only or locked field is not a
 * control at all: it is shown as plain text (ReadOnlyField, ReadOnlyCell).
 */
export function FieldControl({ id, field, value, onChange, invalid, titles, describedBy, ariaLabel }: Props) {
  const common = {
    id,
    'aria-required': field.required || undefined,
    'aria-invalid': invalid || undefined,
    'aria-describedby': describedBy,
    'aria-label': ariaLabel,
  };
  const text = typeof value === 'string' ? value : '';

  switch (field.type) {
    case 'text':
      return (
        <textarea {...common} rows={4} value={text} onChange={(e) => onChange(e.target.value)} />
      );
    case 'check':
      return (
        <input
          {...common}
          type="checkbox"
          checked={value === true}
          onChange={(e) => onChange(e.target.checked)}
        />
      );
    case 'select':
      return (
        <select {...common} value={text} onChange={(e) => onChange(e.target.value)}>
          {field.required && text !== '' ? null : <option value="" />}
          {(field.options ?? []).map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </select>
      );
    case 'link':
      return (
        <LinkInput
          id={id}
          target={field.target ?? ''}
          value={text}
          onChange={onChange}
          titles={titles}
          required={field.required}
          invalid={invalid}
          describedBy={describedBy}
          ariaLabel={ariaLabel}
        />
      );
    case 'int':
      return (
        <input
          {...common}
          type="number"
          step="1"
          value={text}
          // A number control reports "" for text it cannot read (for example "e"): keep a marker
          // so the save refuses it on the field instead of treating it as empty.
          onChange={(e) => onChange(e.target.validity.badInput ? 'not a number' : e.target.value)}
        />
      );
    case 'decimal':
      // Text, not a number control: the value is sent as a string (no float rounding).
      return (
        <input {...common} type="text" inputMode="decimal" value={text} onChange={(e) => onChange(e.target.value)} />
      );
    case 'date':
      return <input {...common} type="date" value={text} onChange={(e) => onChange(e.target.value)} />;
    case 'datetime':
      // Local time of the browser; converted to and from UTC in fields.ts. step=1: whole seconds.
      return (
        <input {...common} type="datetime-local" step="1" value={text} onChange={(e) => onChange(e.target.value)} />
      );
    case 'table':
      return null;
    case 'data':
      return (
        <input
          {...common}
          type="text"
          maxLength={field.max_length}
          value={text}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}
