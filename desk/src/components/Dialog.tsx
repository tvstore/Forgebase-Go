import { useEffect, useId, useRef } from 'react';
import type { ReactNode, RefObject } from 'react';

interface ModalProps {
  /** The id of the element that names the dialog (`aria-labelledby`), or ... */
  labelledBy?: string;
  /** ... the name itself (`aria-label`). */
  label?: string;
  className: string;
  /** Escape (and, with `closeOnBackdrop`, a click outside) asks to close. */
  onClose: () => void;
  /** The element that takes the focus when the dialog opens; the dialog itself when omitted. */
  initialFocus?: RefObject<HTMLElement | null>;
  closeOnBackdrop?: boolean;
  children: ReactNode;
}

/**
 * The native `<dialog>` opened with `showModal()`: the rest of the page is inert while it is open,
 * Tab stays inside it, and Escape asks to close it (`onClose`; the page decides, the browser does
 * not). Focus goes to `initialFocus` when it opens and returns to where it was when it closes.
 * The fade-and-scale is a CSS animation (styles.css), which `prefers-reduced-motion` switches off.
 */
export function Modal({ labelledBy, label, className, onClose, initialFocus, closeOnBackdrop = false, children }: ModalProps) {
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const before = document.activeElement;
    const dialog = ref.current;
    if (dialog && !dialog.open) dialog.showModal();
    initialFocus?.current?.focus();
    return () => {
      dialog?.close();
      if (before instanceof HTMLElement && before.isConnected) before.focus();
    };
    // Once, when the dialog is mounted: the dialog is mounted only while it is open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <dialog
      ref={ref}
      className={className}
      aria-labelledby={labelledBy}
      aria-label={label}
      onCancel={(event) => {
        // Escape: let the page decide (it unmounts this dialog), not the browser.
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        // A click on the backdrop is delivered to the dialog element itself.
        if (closeOnBackdrop && event.target === event.currentTarget) onClose();
      }}
    >
      {children}
    </dialog>
  );
}

interface Props {
  title: string;
  children?: ReactNode;
  confirmLabel: string;
  cancelLabel: string;
  /** Draws the confirm button as a danger button (a destructive answer). */
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

/**
 * A modal question (never `window.confirm`) on [`Modal`]. Focus goes to the safe answer when it
 * opens.
 */
export function Dialog({ title, children, confirmLabel, cancelLabel, danger = false, onConfirm, onCancel }: Props) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();

  return (
    <Modal className="dialog" labelledBy={titleId} onClose={onCancel} initialFocus={cancelRef}>
      <h2 id={titleId}>{title}</h2>
      {children ? <div className="dialog-body">{children}</div> : null}
      <div className="dialog-actions">
        <button type="button" className="btn" ref={cancelRef} onClick={onCancel}>
          {cancelLabel}
        </button>
        <button type="button" className={danger ? 'btn btn-danger' : 'btn btn-primary'} onClick={onConfirm}>
          {confirmLabel}
        </button>
      </div>
    </Modal>
  );
}
