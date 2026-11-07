// The status badge of a document: the text always says the status, the colour only supports it.

import { docStatusLabel, docStatusOf } from './access';
import type { DocTypeMeta } from './types';

export type Tone = 'neutral' | 'success' | 'danger' | 'warning' | 'info';

export interface BadgeSpec {
  label: string;
  tone: Tone;
}

/** Draft is grey, Submitted green, Cancelled red. */
export function statusBadge(docstatus: unknown): BadgeSpec {
  const status = docStatusOf(docstatus);
  return { label: docStatusLabel(status), tone: status === 1 ? 'success' : status === 2 ? 'danger' : 'neutral' };
}

const TONES: readonly Tone[] = ['neutral', 'success', 'danger', 'warning', 'info'];

/**
 * The badge of a document of `meta`'s DocType. With a workflow it is the workflow state's name in
 * the tone of the state's `style`. It falls back to the document status badge when the DocType has
 * no workflow or the document has no state; a state that the workflow no longer defines (renamed
 * or removed) shows its raw name in the neutral tone. It never throws.
 */
export function stateBadge(meta: Pick<DocTypeMeta, 'workflow'>, doc: Record<string, unknown>): BadgeSpec {
  const workflow = meta.workflow ?? null;
  const state = doc.workflow_state;
  if (workflow === null || typeof state !== 'string' || state === '') return statusBadge(doc.docstatus);
  const known = workflow.states.find((s) => s.name === state);
  if (!known) return { label: state, tone: 'neutral' };
  return { label: known.name, tone: TONES.includes(known.style) ? known.style : 'neutral' };
}
