import { useEffect, useId, useRef, useState } from 'react';
import type { ReactNode } from 'react';

import { ChevronDownIcon } from '../icons';

interface Props {
  /** The visible text of the button. */
  label: ReactNode;
  /** Names the button when `label` is not enough. */
  ariaLabel?: string;
  /** Which edge of the button the menu lines up with. */
  align?: 'start' | 'end';
  className?: string;
  /** Keeps the menu open when something in it is clicked (a menu of checkboxes). */
  keepOpen?: boolean;
  /** Buttons (or links); a click on any of them closes the menu (unless `keepOpen`). */
  children: ReactNode;
}

/**
 * A disclosure: a button with `aria-expanded` that shows a short list of actions. Escape and a
 * click or Tab outside close it (Escape gives the focus back to the button), and so does a click
 * on one of the actions. Every action is a normal button, reachable with Tab.
 */
export function MenuButton({ label, ariaLabel, align = 'end', className, keepOpen = false, children }: Props) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menuId = useId();

  useEffect(() => {
    if (!open) return;
    const onPointer = (event: PointerEvent) => {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setOpen(false);
    };
    // Tab (or any focus change) to something outside closes it as well.
    const onFocus = (event: FocusEvent) => onPointer(event as unknown as PointerEvent);
    document.addEventListener('pointerdown', onPointer);
    document.addEventListener('focusin', onFocus);
    return () => {
      document.removeEventListener('pointerdown', onPointer);
      document.removeEventListener('focusin', onFocus);
    };
  }, [open]);

  return (
    <div
      className="menu"
      ref={root}
      onKeyDown={(e) => {
        if (open && e.key === 'Escape') {
          e.stopPropagation();
          setOpen(false);
          trigger.current?.focus();
        }
      }}
    >
      <button
        type="button"
        ref={trigger}
        className={className ?? 'btn'}
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={ariaLabel}
        onClick={() => setOpen((o) => !o)}
      >
        {label}
        <ChevronDownIcon size={14} className="icon" />
      </button>
      {open ? (
        <div
          id={menuId}
          className={`menu-list menu-${align}`}
          onClick={() => {
            if (keepOpen) return;
            // The focus goes back to the button first, so that a dialog opened by the action
            // returns it there (the action's own button is gone with the menu).
            trigger.current?.focus();
            setOpen(false);
          }}
        >
          {children}
        </div>
      ) : null}
    </div>
  );
}
