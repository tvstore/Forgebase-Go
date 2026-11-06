import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';

import { CircleCheckIcon, XIcon } from '../icons';

/** How long a toast stays when nobody closes it. */
export const TOAST_MS = 5000;

interface ToastItem {
  id: number;
  message: string;
}

const ToastContext = createContext<(message: string) => void>(() => {});

/** `const toast = useToast(); toast('Saved')`: a short success message that goes away by itself. */
export function useToast(): (message: string) => void {
  return useContext(ToastContext);
}

/**
 * Keeps the toasts and draws them in one polite live region (`role="status"`), which stays in
 * the page so that a screen reader announces each new message. Errors are not toasts: they stay
 * on the page where the user can act on them.
 */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const nextId = useRef(1);
  const timers = useRef(new Map<number, number>());

  const dismiss = useCallback((id: number) => {
    window.clearTimeout(timers.current.get(id));
    timers.current.delete(id);
    setItems((list) => list.filter((t) => t.id !== id));
  }, []);

  const show = useCallback(
    (message: string) => {
      const id = nextId.current++;
      setItems((list) => [...list.slice(-2), { id, message }]);
      timers.current.set(id, window.setTimeout(() => dismiss(id), TOAST_MS));
    },
    [dismiss],
  );

  useEffect(() => {
    const live = timers.current;
    return () => live.forEach((timer) => window.clearTimeout(timer));
  }, []);

  const value = useMemo(() => show, [show]);
  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {items.map((t) => (
          <div className="toast" key={t.id}>
            <CircleCheckIcon size={18} className="icon" />
            <span>{t.message}</span>
            <button type="button" className="toast-close" aria-label="Dismiss" onClick={() => dismiss(t.id)}>
              <XIcon size={16} />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
