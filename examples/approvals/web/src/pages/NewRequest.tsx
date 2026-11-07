import { useState } from 'react';
import { api, type LineStep, type Person } from '../api';
import { ApprovalLine } from '../components/ApprovalLine';
import { RequestForm, emptyForm, type FormValues } from '../components/RequestForm';

// The line a new request will take, before it exists: the start is where the
// requester stands, with Submit as its action.
function previewLine(people: Person[]): LineStep[] {
  const approver = (group: string) => people.find((p) => p.group === group);
  const step = (id: string, name: string, group: string): LineStep => {
    const p = approver(group);
    return { id, name, state: 'later', who: p ? `${p.name} (${p.role})` : undefined };
  };
  return [
    { id: 'submitted', name: 'Submitted', state: 'now', who: 'You', actions: ['submit'] },
    step('supervisor-approval', 'Supervisor approval', 'supervisors'),
    step('unit-head-approval', 'Unit head approval', 'unit-heads'),
    step('finance-approval', 'Finance approval', 'finance'),
    { id: 'completed', name: 'Completed', state: 'later' },
  ];
}

export function NewRequest({ as, people, go }: { as: string; people: Person[]; go: (to: string) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const me = people.find((p) => p.username === as);

  const submit = async (v: FormValues) => {
    setBusy(true);
    setError('');
    try {
      const { id } = await api.submit({ as, ...v });
      go(`/requests/${id}`);
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <h1>New expense request</h1>
      <section className="card" aria-labelledby="line-h">
        <h2 id="line-h">Approval line</h2>
        <ApprovalLine
          line={previewLine(people)}
          renderActions={(s) =>
            s.actions?.includes('submit') ? (
              <div className="step-actions">
                <button type="submit" form="new-request" className="btn btn-primary btn-small" disabled={busy}>
                  Submit
                </button>
              </div>
            ) : null
          }
        />
      </section>
      <section className="card" aria-labelledby="form-h">
        <h2 id="form-h">The request</h2>
        {me && me.role !== 'requester' && (
          <p className="hint">You are acting as {me.name}, the {me.role}. Anyone can submit, but the sample's requester is Rina.</p>
        )}
        {error && <p className="error" role="alert">{error}</p>}
        {/* The line's Submit button submits this form too. */}
        <RequestForm formId="new-request" initial={emptyForm} submitLabel="Submit for approval" busy={busy} onSubmit={submit} />
        <p className="hint">
          Before anyone is asked, metis checks the form (a script task), asks the budget API whether the cost centre can
          cover it (a service task calling an API), and classifies it with a decision table (a business rule task).
        </p>
      </section>
    </div>
  );
}
