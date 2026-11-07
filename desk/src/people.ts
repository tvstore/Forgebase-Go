// How a user is shown: initials in a small circle next to the name.

/**
 * Up to two capital letters: the first letters of the first two words of the name. A user name
 * such as `e2e_manager` or `ada.lovelace` is split at `_`, `.`, `-` and spaces; an e-mail address
 * counts only the part before `@`. No letters or digits at all gives `?`.
 */
export function initials(name: string): string {
  const local = name.split('@')[0] ?? '';
  const words = local.split(/[^\p{L}\p{N}]+/u).filter((word) => word !== '');
  const letters = words.slice(0, 2).map((word) => [...word][0] ?? '');
  const text = letters.join('').toUpperCase();
  return text === '' ? '?' : text;
}
