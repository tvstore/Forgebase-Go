// Icons: inline SVG components, copied from Lucide (https://lucide.dev, lucide-static 1.65.0, ISC
// licence; the notice is in src/icons/LICENSE-lucide.txt). They are copied, not a dependency: a
// curated set of the icons the desk draws, so the bundle holds only these.
//
// Every icon is decorative (`aria-hidden`, not focusable); the text next to it, or the
// `aria-label` of its button, is the name. The icons are drawn with `currentColor`.
//
// DocType icons: the names a DocType file may give as `icon = "..."` are in ONE list, the file
// crates/forgebase-core/src/doctype_icons.txt (Rust validates DocType files against it). The
// `DOCTYPE_ICONS` map below must have a component for exactly those names: icons.test.ts reads
// that file and checks both directions.

import type { ReactNode } from 'react';

export interface IconProps {
  /** Width and height in px (the default is 16). */
  size?: number;
  className?: string;
}

function svg(children: ReactNode, { size = 16, className }: IconProps) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  );
}

export function SearchIcon(props: IconProps) {
  return svg(
    <>
      <path d="m21 21-4.34-4.34" />
      <circle cx="11" cy="11" r="8" />
    </>,
    props,
  );
}

export function PlusIcon(props: IconProps) {
  return svg(
    <>
      <path d="M5 12h14" />
      <path d="M12 5v14" />
    </>,
    props,
  );
}

export function XIcon(props: IconProps) {
  return svg(
    <>
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </>,
    props,
  );
}

export function ChevronDownIcon(props: IconProps) {
  return svg(
    <>
      <path d="m6 9 6 6 6-6" />
    </>,
    props,
  );
}

export function ChevronRightIcon(props: IconProps) {
  return svg(
    <>
      <path d="m9 18 6-6-6-6" />
    </>,
    props,
  );
}

export function EllipsisIcon(props: IconProps) {
  return svg(
    <>
      <circle cx="12" cy="12" r="1" />
      <circle cx="19" cy="12" r="1" />
      <circle cx="5" cy="12" r="1" />
    </>,
    props,
  );
}

export function CheckIcon(props: IconProps) {
  return svg(
    <>
      <path d="M20 6 9 17l-5-5" />
    </>,
    props,
  );
}

export function CircleCheckIcon(props: IconProps) {
  return svg(
    <>
      <circle cx="12" cy="12" r="10" />
      <path d="m16 9-5.5 5.5L8 12" />
    </>,
    props,
  );
}

export function CircleAlertIcon(props: IconProps) {
  return svg(
    <>
      <circle cx="12" cy="12" r="10" />
      <line x1="12" x2="12" y1="8" y2="12" />
      <line x1="12" x2="12.01" y1="16" y2="16" />
    </>,
    props,
  );
}

export function InfoIcon(props: IconProps) {
  return svg(
    <>
      <circle cx="12" cy="12" r="10" />
      <path d="M12 16v-4" />
      <path d="M12 8h.01" />
    </>,
    props,
  );
}

export function FunnelIcon(props: IconProps) {
  return svg(
    <>
      <path d="M10 20a1 1 0 0 0 .553.895l2 1A1 1 0 0 0 14 21v-7a2 2 0 0 1 .517-1.341L21.74 4.67A1 1 0 0 0 21 3H3a1 1 0 0 0-.742 1.67l7.225 7.989A2 2 0 0 1 10 14z" />
    </>,
    props,
  );
}

export function LogOutIcon(props: IconProps) {
  return svg(
    <>
      <path d="m16 17 5-5-5-5" />
      <path d="M21 12H9" />
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
    </>,
    props,
  );
}

export function MenuIcon(props: IconProps) {
  return svg(
    <>
      <path d="M4 5h16" />
      <path d="M4 12h16" />
      <path d="M4 19h16" />
    </>,
    props,
  );
}

export function HouseIcon(props: IconProps) {
  return svg(
    <>
      <path d="M15 21v-8a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v8" />
      <path d="M3 10a2 2 0 0 1 .709-1.528l7-6a2 2 0 0 1 2.582 0l7 6A2 2 0 0 1 21 10v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
    </>,
    props,
  );
}

export function ArrowLeftIcon(props: IconProps) {
  return svg(
    <>
      <path d="m12 19-7-7 7-7" />
      <path d="M19 12H5" />
    </>,
    props,
  );
}

export function ClockIcon(props: IconProps) {
  return svg(
    <>
      <circle cx="12" cy="12" r="10" />
      <path d="M12 6v6l4 2" />
    </>,
    props,
  );
}

export function UserIcon(props: IconProps) {
  return svg(
    <>
      <path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2" />
      <circle cx="12" cy="7" r="4" />
    </>,
    props,
  );
}

export function HistoryIcon(props: IconProps) {
  return svg(
    <>
      <path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8" />
      <path d="M3 3v5h5" />
      <path d="M12 7v5l4 2" />
    </>,
    props,
  );
}

export function ExternalLinkIcon(props: IconProps) {
  return svg(
    <>
      <path d="M15 3h6v6" />
      <path d="M10 14 21 3" />
      <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
    </>,
    props,
  );
}

export function FileTextIcon(props: IconProps) {
  return svg(
    <>
      <path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z" />
      <path d="M14 2v5a1 1 0 0 0 1 1h5" />
      <path d="M10 9H8" />
      <path d="M16 13H8" />
      <path d="M16 17H8" />
    </>,
    props,
  );
}

export function UsersIcon(props: IconProps) {
  return svg(
    <>
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <path d="M16 3.128a4 4 0 0 1 0 7.744" />
      <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
      <circle cx="9" cy="7" r="4" />
    </>,
    props,
  );
}

export function ShoppingCartIcon(props: IconProps) {
  return svg(
    <>
      <path d="m2.05 2.05 1.099-.028a1 1 0 0 1 1.008.815l2.69 14.347A1 1 0 0 0 7.83 18H18" />
      <path d="M4.563 5h16.435a1 1 0 0 1 .981 1.204l-1.026 6.226A2 2 0 0 1 18.962 14H6.25" />
      <circle cx="18" cy="20" r="2" />
      <circle cx="8" cy="20" r="2" />
    </>,
    props,
  );
}

export function PackageIcon(props: IconProps) {
  return svg(
    <>
      <path d="M11 21.73a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73z" />
      <path d="M12 22V12" />
      <polyline points="3.29 7 12 12 20.71 7" />
      <path d="m7.5 4.27 9 5.15" />
    </>,
    props,
  );
}

export function Building2Icon(props: IconProps) {
  return svg(
    <>
      <path d="M10 12h4" />
      <path d="M10 8h4" />
      <path d="M14 21v-3a2 2 0 0 0-4 0v3" />
      <path d="M6 10H4a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-2" />
      <path d="M6 21V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v16" />
    </>,
    props,
  );
}

export function ReceiptIcon(props: IconProps) {
  return svg(
    <>
      <path d="M12 17V7" />
      <path d="M16 8h-6a2 2 0 0 0 0 4h4a2 2 0 0 1 0 4H8" />
      <path d="M4 3a1 1 0 0 1 1-1 1.3 1.3 0 0 1 .7.2l.933.6a1.3 1.3 0 0 0 1.4 0l.934-.6a1.3 1.3 0 0 1 1.4 0l.933.6a1.3 1.3 0 0 0 1.4 0l.933-.6a1.3 1.3 0 0 1 1.4 0l.934.6a1.3 1.3 0 0 0 1.4 0l.933-.6A1.3 1.3 0 0 1 19 2a1 1 0 0 1 1 1v18a1 1 0 0 1-1 1 1.3 1.3 0 0 1-.7-.2l-.933-.6a1.3 1.3 0 0 0-1.4 0l-.934.6a1.3 1.3 0 0 1-1.4 0l-.933-.6a1.3 1.3 0 0 0-1.4 0l-.933.6a1.3 1.3 0 0 1-1.4 0l-.934-.6a1.3 1.3 0 0 0-1.4 0l-.933.6a1.3 1.3 0 0 1-.7.2 1 1 0 0 1-1-1z" />
    </>,
    props,
  );
}

export function BriefcaseIcon(props: IconProps) {
  return svg(
    <>
      <path d="M16 20V4a2 2 0 0 0-2-2h-4a2 2 0 0 0-2 2v16" />
      <rect width="20" height="14" x="2" y="6" rx="2" />
    </>,
    props,
  );
}

export function CalendarIcon(props: IconProps) {
  return svg(
    <>
      <path d="M8 2v3" />
      <path d="M16 2v3" />
      <rect x="3" y="3" width="18" height="18" rx="2" />
      <path d="M3 9h18" />
    </>,
    props,
  );
}

export function ClipboardListIcon(props: IconProps) {
  return svg(
    <>
      <rect width="8" height="4" x="8" y="2" rx="1" ry="1" />
      <path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2" />
      <path d="M12 11h4" />
      <path d="M12 16h4" />
      <path d="M8 11h.01" />
      <path d="M8 16h.01" />
    </>,
    props,
  );
}

export function FolderIcon(props: IconProps) {
  return svg(
    <>
      <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
    </>,
    props,
  );
}

export function TagIcon(props: IconProps) {
  return svg(
    <>
      <path d="M12.586 2.586A2 2 0 0 0 11.172 2H4a2 2 0 0 0-2 2v7.172a2 2 0 0 0 .586 1.414l8.704 8.704a2.426 2.426 0 0 0 3.42 0l6.58-6.58a2.426 2.426 0 0 0 0-3.42z" />
      <circle cx="7.5" cy="7.5" r=".5" fill="currentColor" />
    </>,
    props,
  );
}

export function TruckIcon(props: IconProps) {
  return svg(
    <>
      <path d="M14 18V6a2 2 0 0 0-2-2H4a2 2 0 0 0-2 2v11a1 1 0 0 0 1 1h2" />
      <path d="M15 18H9" />
      <path d="M19 18h2a1 1 0 0 0 1-1v-3.65a1 1 0 0 0-.22-.624l-3.48-4.35A1 1 0 0 0 17.52 8H14" />
      <circle cx="17" cy="18" r="2" />
      <circle cx="7" cy="18" r="2" />
    </>,
    props,
  );
}

export function WalletIcon(props: IconProps) {
  return svg(
    <>
      <path d="M19 7V4a1 1 0 0 0-1-1H5a2 2 0 0 0 0 4h15a1 1 0 0 1 1 1v4h-3a2 2 0 0 0 0 4h3a1 1 0 0 0 1-1v-2a1 1 0 0 0-1-1" />
      <path d="M3 5v14a2 2 0 0 0 2 2h15a1 1 0 0 0 1-1v-4" />
    </>,
    props,
  );
}

export function SettingsIcon(props: IconProps) {
  return svg(
    <>
      <path d="M9.671 4.136a2.34 2.34 0 0 1 4.659 0 2.34 2.34 0 0 0 3.319 1.915 2.34 2.34 0 0 1 2.33 4.033 2.34 2.34 0 0 0 0 3.831 2.34 2.34 0 0 1-2.33 4.033 2.34 2.34 0 0 0-3.319 1.915 2.34 2.34 0 0 1-4.659 0 2.34 2.34 0 0 0-3.32-1.915 2.34 2.34 0 0 1-2.33-4.033 2.34 2.34 0 0 0 0-3.831A2.34 2.34 0 0 1 6.35 6.051a2.34 2.34 0 0 0 3.319-1.915" />
      <circle cx="12" cy="12" r="3" />
    </>,
    props,
  );
}

export function BoxIcon(props: IconProps) {
  return svg(
    <>
      <path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z" />
      <path d="m3.3 7 8.7 5 8.7-5" />
      <path d="M12 22V12" />
    </>,
    props,
  );
}

/** The icons a DocType may name (`icon` in its file), by name. Keep in step with doctype_icons.txt. */
export const DOCTYPE_ICONS: Record<string, (props: IconProps) => ReactNode> = {
  'file-text': FileTextIcon,
  'users': UsersIcon,
  'user': UserIcon,
  'shopping-cart': ShoppingCartIcon,
  'package': PackageIcon,
  'building-2': Building2Icon,
  'receipt': ReceiptIcon,
  'briefcase': BriefcaseIcon,
  'calendar': CalendarIcon,
  'clipboard-list': ClipboardListIcon,
  'folder': FolderIcon,
  'tag': TagIcon,
  'truck': TruckIcon,
  'wallet': WalletIcon,
  'settings': SettingsIcon,
  'box': BoxIcon,
};

/** The icon used for a DocType that names none. */
export const DEFAULT_DOCTYPE_ICON = 'file-text';

/** The icon of a DocType: the one it names, or the default. */
export function DocTypeIcon({ name, ...props }: IconProps & { name: string | null | undefined }) {
  const Icon = (name ? DOCTYPE_ICONS[name] : undefined) ?? DOCTYPE_ICONS[DEFAULT_DOCTYPE_ICON];
  return Icon ? <Icon {...props} /> : null;
}
