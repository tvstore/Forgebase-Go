// Saves a file the page already holds (a report export), through an object URL.

/** Offers `blob` to the user as a download named `filename`. */
export function saveDownload(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.hidden = true;
  document.body.append(link);
  link.click();
  link.remove();
  // The browser has taken the file by then; a later revoke frees the memory.
  window.setTimeout(() => URL.revokeObjectURL(url), 10_000);
}
