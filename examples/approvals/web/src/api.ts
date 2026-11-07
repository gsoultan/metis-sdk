// The Go backend's API. Every call here ends up as metis-sdk calls on the
// server; the browser never talks to metis itself.

export type Person = { username: string; name: string; role: string; group?: string };

export type StepState = 'done' | 'now' | 'later' | 'rejected' | 'returned' | 'skipped';
export type Action = 'submit' | 'resubmit' | 'approve' | 'reject' | 'revise';

export type LineStep = {
  id: string;
  name: string;
  state: StepState;
  who?: string;
  comment?: string;
  actions?: Action[];
  actor?: string;
  taskId?: string;
};

export type RequestSummary = {
  id: string;
  title: string;
  amount: number;
  submittedBy: string;
  status: string;
  outcome: string;
  createdAt: string;
};

export type RequestDetail = RequestSummary & {
  variables: Record<string, unknown>;
  line: LineStep[];
  history: { at: string; text: string; step?: string }[];
};

export type InboxItem = {
  taskId: string;
  step: string;
  stepName: string;
  request: RequestSummary;
  since: string;
};

export type Submission = {
  as: string;
  title: string;
  amount: number;
  category: string;
  costCentre: string;
  reason: string;
};

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...init });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error ?? `${res.status} ${res.statusText}`);
  }
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const api = {
  people: () => call<Person[]>('/api/people'),
  requests: (as?: string) => call<RequestSummary[]>(`/api/requests${as ? `?as=${encodeURIComponent(as)}` : ''}`),
  request: (id: string) => call<RequestDetail>(`/api/requests/${encodeURIComponent(id)}`),
  submit: (s: Submission) => call<{ id: string }>('/api/requests', { method: 'POST', body: JSON.stringify(s) }),
  inbox: (as: string) => call<InboxItem[]>(`/api/inbox?as=${encodeURIComponent(as)}`),
  decide: (taskId: string, as: string, decision: 'approve' | 'reject' | 'revise', comment: string) =>
    call<void>(`/api/tasks/${encodeURIComponent(taskId)}/decision`, {
      method: 'POST',
      body: JSON.stringify({ as, decision, comment }),
    }),
  resubmit: (taskId: string, s: Submission) =>
    call<void>(`/api/tasks/${encodeURIComponent(taskId)}/resubmit`, { method: 'POST', body: JSON.stringify(s) }),
};

export const rupiah = (n: number) => 'Rp ' + Math.round(n).toLocaleString('id-ID');
