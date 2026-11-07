import { MAX_COLUMNS } from '../listing';
import type { Column } from '../listing';
import { MenuButton } from './MenuButton';

interface Props {
  /** Every column the menu offers (not `name`: the link column is always shown). */
  columns: Column[];
  /** The keys of the shown columns. */
  shown: string[];
  onToggle: (key: string) => void;
  onReset: () => void;
  /** The shown columns are not the default ones. */
  changed: boolean;
}

/**
 * "Columns": a checkbox for each column. The menu stays open while boxes are ticked, so several
 * can be changed at once. At most `MAX_COLUMNS` are shown and the last one cannot be unticked;
 * a box that cannot change is `aria-disabled` and says why.
 */
export function ColumnsMenu({ columns, shown, onToggle, onReset, changed }: Props) {
  const full = shown.length >= MAX_COLUMNS;
  return (
    <MenuButton label="Columns" ariaLabel="Columns" align="start" keepOpen className="btn btn-small">
      <p className="menu-heading">Show columns</p>
      {columns.map((column) => {
        const on = shown.includes(column.key);
        const locked = on ? shown.length <= 1 : full;
        return (
          <label
            key={column.key}
            className="menu-item menu-check"
            title={locked ? (on ? 'At least one column stays' : `At most ${MAX_COLUMNS} columns`) : undefined}
          >
            <input
              type="checkbox"
              checked={on}
              aria-disabled={locked || undefined}
              onChange={() => {
                if (!locked) onToggle(column.key);
              }}
            />
            <span>{column.label}</span>
          </label>
        );
      })}
      <div className="menu-sep" role="separator" />
      <button
        type="button"
        className="menu-item"
        aria-disabled={!changed || undefined}
        onClick={() => {
          if (changed) onReset();
        }}
      >
        Reset to default
      </button>
    </MenuButton>
  );
}
