// The e2e files run in Node, but the desk does not depend on @types/node: only what they use.
declare const process: { env: Record<string, string | undefined> };
// A file to upload is given to Playwright as a Node Buffer.
declare const Buffer: { from(text: string): unknown };
