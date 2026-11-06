import { useId } from 'react';

import { CheckIcon } from '../icons';
import { splitViews } from '../views';
import type { SavedView } from '../views';
import { MenuButton } from './MenuButton';

interface Props {
  views: SavedView[];
  /** The view the list started from, if it is still there. */
  current: SavedView | undefined;
  /** The user may update or delete the current view. */
  canChange: boolean;
  /** The list state differs from the current view. */
  differs: boolean;
  onApply: (view: SavedView) => void;
  onDefault: () => void;
  onSave: () => void;
  onUpdate: () => void;
  onDelete: () => void;
}

/**
 * "Views": the saved views in two groups (Shared, My views), and the actions to save the list as
 * a view, to update or delete the current one, and to go back to the default list. Every item is
 * a button, reachable with Tab; Escape closes the menu.
 */
export function ViewsMenu({ views, current, canChange, differs, onApply, onDefault, onSave, onUpdate, onDelete }: Props) {
  const { shared, mine } = splitViews(views);
  const sharedId = useId();
  const mineId = useId();

  const group = (id: string, heading: string, list: SavedView[]) =>
    list.length === 0 ? null : (
      <div role="group" aria-labelledby={id} className="menu-group">
        <p id={id} className="menu-heading">
          {heading}
        </p>
        {list.map((view) => (
          <button
            key={view.id}
            type="button"
            className="menu-item menu-choice"
            aria-current={current?.id === view.id ? 'true' : undefined}
            onClick={() => onApply(view)}
          >
            <span className="menu-choice-label">{view.label}</span>
            {current?.id === view.id ? <CheckIcon size={16} className="icon" /> : null}
          </button>
        ))}
      </div>
    );

  return (
    <MenuButton label="Views" ariaLabel="Views" align="start" className="btn btn-small">
      {group(sharedId, 'Shared', shared)}
      {group(mineId, 'My views', mine)}
      {views.length === 0 ? <p className="menu-note">No saved views yet.</p> : null}
      <div className="menu-sep" role="separator" />
      <button type="button" className="menu-item" onClick={onSave}>
        Save view&hellip;
      </button>
      {current && canChange ? (
        <>
          <button
            type="button"
            className="menu-item"
            aria-disabled={!differs || undefined}
            title={differs ? undefined : 'Nothing has changed since this view was saved'}
            onClick={() => {
              if (differs) onUpdate();
            }}
          >
            Update &lsquo;{current.label}&rsquo;
          </button>
          <button type="button" className="menu-item menu-danger" onClick={onDelete}>
            Delete &lsquo;{current.label}&rsquo;&hellip;
          </button>
        </>
      ) : null}
      {current ? (
        <button type="button" className="menu-item" onClick={onDefault}>
          Default view
        </button>
      ) : null}
    </MenuButton>
  );
}
