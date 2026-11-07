/**
 * The Forgebase logo mark (docs/assets/logo-mark.svg), drawn inline. Decorative: the name next to
 * it, where there is one, is the text.
 */
export function Logo({ size = 28 }: { size?: number }) {
  return (
    <svg
      className="logo"
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 128 128"
      width={size}
      height={size}
      aria-hidden="true"
      focusable="false"
    >
      <rect x="24" y="12" width="28" height="104" fill="#FBBF24" />
      <rect x="24" y="12" width="80" height="28" fill="#FBBF24" />
      <rect x="24" y="52" width="40" height="26" fill="#FBBF24" />
      <rect x="64" y="52" width="40" height="26" fill="#F59E0B" />
    </svg>
  );
}
