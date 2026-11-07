/** Joins class names, skipping the empty ones: `cx('btn', active && 'btn-primary')`. */
export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter((part): part is string => typeof part === 'string' && part !== '').join(' ');
}
