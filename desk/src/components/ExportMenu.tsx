import type { ReactNode } from 'react';

import { FORMAT_LABELS } from '../transfer';
import type { FileFormat } from '../transfer';
import { MenuButton } from './MenuButton';

interface Props {
  /** What the export holds, as the menu's heading says it. */
  heading: string;
  /**
   * The address of the file for a format: a link, so the browser downloads it (the list), or
   * `null` when the page fetches it itself (a report, which is a POST) and `onPick` runs.
   */
  href?: (format: FileFormat) => string;
  onPick?: (format: FileFormat) => void;
  /** Extra text under the formats (what is exported, what the limit is). */
  note?: ReactNode;
}

const FORMATS: FileFormat[] = ['csv', 'json'];

/** "Export": a menu of the file formats. Each is a link (or a button) reachable with Tab. */
export function ExportMenu({ heading, href, onPick, note }: Props) {
  return (
    <MenuButton label="Export" ariaLabel="Export" align="end" className="btn btn-small">
      <p className="menu-heading">{heading}</p>
      {FORMATS.map((format) =>
        href ? (
          <a key={format} className="menu-item" href={href(format)} download>
            {FORMAT_LABELS[format]}
          </a>
        ) : (
          <button key={format} type="button" className="menu-item" onClick={() => onPick?.(format)}>
            {FORMAT_LABELS[format]}
          </button>
        ),
      )}
      {note ? <p className="menu-note">{note}</p> : null}
    </MenuButton>
  );
}
