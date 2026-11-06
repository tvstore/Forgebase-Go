import { describe, expect, it } from 'vitest';

import { stateBadge, statusBadge } from './status';
import type { WorkflowMeta } from './types';

describe('statusBadge', () => {
  it('maps the document status to a text and a tone', () => {
    expect(statusBadge(0)).toEqual({ label: 'Draft', tone: 'neutral' });
    expect(statusBadge(1)).toEqual({ label: 'Submitted', tone: 'success' });
    expect(statusBadge(2)).toEqual({ label: 'Cancelled', tone: 'danger' });
  });

  it('treats a missing or unknown status as a draft', () => {
    expect(statusBadge(undefined).label).toBe('Draft');
    expect(statusBadge(7).label).toBe('Draft');
  });
});

const workflow: WorkflowMeta = {
  states: [
    { name: 'Draft', docstatus: 0, style: 'neutral' },
    { name: 'Pending Approval', docstatus: 0, style: 'warning' },
    { name: 'Approved', docstatus: 1, style: 'success' },
    { name: 'Rejected', docstatus: 0, style: 'danger' },
    { name: 'Review', docstatus: 0, style: 'info' },
  ],
  transitions: [],
};

describe('stateBadge', () => {
  it('shows the workflow state in the tone of its style', () => {
    const meta = { workflow };
    expect(stateBadge(meta, { workflow_state: 'Draft' })).toEqual({ label: 'Draft', tone: 'neutral' });
    expect(stateBadge(meta, { workflow_state: 'Pending Approval' })).toEqual({
      label: 'Pending Approval',
      tone: 'warning',
    });
    expect(stateBadge(meta, { workflow_state: 'Approved', docstatus: 1 })).toEqual({
      label: 'Approved',
      tone: 'success',
    });
    expect(stateBadge(meta, { workflow_state: 'Rejected' }).tone).toBe('danger');
    expect(stateBadge(meta, { workflow_state: 'Review' }).tone).toBe('info');
  });

  it('falls back to the document status without a workflow', () => {
    expect(stateBadge({ workflow: null }, { docstatus: 1, workflow_state: 'Approved' })).toEqual({
      label: 'Submitted',
      tone: 'success',
    });
    expect(stateBadge({}, { docstatus: 2 })).toEqual({ label: 'Cancelled', tone: 'danger' });
  });

  it('falls back to the document status when the document has no state', () => {
    expect(stateBadge({ workflow }, { docstatus: 0 })).toEqual({ label: 'Draft', tone: 'neutral' });
    expect(stateBadge({ workflow }, { docstatus: 1, workflow_state: null })).toEqual({
      label: 'Submitted',
      tone: 'success',
    });
    expect(stateBadge({ workflow }, { docstatus: 0, workflow_state: '' }).label).toBe('Draft');
  });

  it('shows the raw name, neutral, for a state the workflow does not define', () => {
    expect(stateBadge({ workflow }, { workflow_state: 'Old State', docstatus: 1 })).toEqual({
      label: 'Old State',
      tone: 'neutral',
    });
  });

  it('treats an unknown style as neutral', () => {
    const odd = { workflow: { states: [{ name: 'X', docstatus: 0, style: 'purple' as never }], transitions: [] } };
    expect(stateBadge(odd, { workflow_state: 'X' })).toEqual({ label: 'X', tone: 'neutral' });
  });
});
