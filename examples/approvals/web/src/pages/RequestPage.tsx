import { useCallback, useEffect, useState } from 'react';
import { api, rupiah, type LineStep, type Person, type RequestDetail } from '../api';
import { ApprovalLine, nameOf } from '../components/ApprovalLine';
import { RequestForm, type FormValues } from '../components/RequestForm';

// A request, with its approval line and the actions of the step it is at.
// It reloads every few seconds, so a step done in another tab, or by metis
// itself, moves on here too.
export function RequestPage({
  id,
  as,
  people,
  setAs,
}: {
  id: string;
  as: string;
  people: Person[];
  setAs: (username: string) => void;
}) {
  const [req, setReq] = useState<RequestDetail | null>(null);
  const [error, setError] = useState('');

  const load = useCallback(
    () =>
      api.request(id).then(
        (r) => {
          setReq(r);
          setError('');
        },
        (e: Error) => setError(e.message),
      ),
    [id],
  );
  useEffect(() => {
    load();
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [load]);

  if (!req) return <p className="hint">{error || 'Loading…'}</p>;

  const v = req.variables;
  const now = req.line.find((s) => s.state === 'now');
  const stopped = req.line.find((s) => s.state === 'rejected');
  const status = stopped
    ? stopped.who ?? 'Rejected'
    : req.line[req.line.length - 1].state === 'done'
      ? 'Completed'
      : v.budgetOk === false
        ? 'Not enough budget'
        : now
          ? now.who ?? now.name
          : 'Being checked';

  return (
    <div className="stack">
      <div className="title-row">
        <div>
          <div className="eyebrow">
            Submitted by {nameOf(people, req.submittedBy)} · {new Date(req.createdAt).toLocaleString()}
          </div>
          <h1>{req.title}</h1>
        </div>
        <div className="title-side">
          <span className="amount big">{rupiah(req.amount)}</span>
          <span className={`pill ${stopped ? 'pill-rejected' : now ? 'pill-now' : 'pill-completed'}`}>{status}</span>
        </div>
      </div>

      <section className="card" aria-labelledby="line-h">
        <div className="card-head">
          <h2 id="line-h">Approval line</h2>
          <Legend />
        </div>
        <ApprovalLine
          line={req.line}
          renderActions={(step) => <StepActions step={step} req={req} as={as} people={people} setAs={setAs} reload={load} />}
        />
      </section>

      <div className="two-col">
        <section className="card grow" aria-labelledby="hist-h">
          <h2 id="hist-h">What happened</h2>
          <ul className="history">
            {req.history.map((h, i) => (
              <li key={i}>
                <time>{new Date(h.at).toLocaleTimeString()}</time>
                <span>{h.text}</span>
              </li>
            ))}
          </ul>
        </section>
        <aside className="card side" aria-labelledby="facts-h">
          <h2 id="facts-h">Request</h2>
          <dl className="facts">
            <dt>Category</dt>
            <dd>{String(v.category ?? '')}</dd>
            <dt>Cost centre</dt>
            <dd>{String(v.costCentre ?? '')}</dd>
            <dt>Budget left</dt>
            <dd>{typeof v.budgetLeft === 'number' ? rupiah(v.budgetLeft) : '…'}</dd>
            <dt>Risk</dt>
            <dd>{String(v.riskLevel ?? '…')}</dd>
            <dt>Paid by</dt>
            <dd>{String(v.paymentMethod ?? '…')}</dd>
            {typeof v.paymentReference === 'string' && (
              <>
                <dt>Payment</dt>
                <dd>{v.paymentReference}</dd>
              </>
            )}
            <dt>Reason</dt>
            <dd>{String(v.reason ?? '')}</dd>
          </dl>
          <p className="hint">Budget left comes from the budget API, risk and payment method from the decision table.</p>
        </aside>
      </div>
    </div>
  );
}

// The buttons a step offers. Only the person the step waits for can press
// them; everyone else sees who that is, and can switch to them.
function StepActions({
  step,
  req,
  as,
  people,
  setAs,
  reload,
}: {
  step: LineStep;
  req: RequestDetail;
  as: string;
  people: Person[];
  setAs: (u: string) => void;
  reload: () => Promise<void>;
}) {
  const [comment, setComment] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState(false);

  if (!step.actions?.length || !step.taskId) return null;
  const mine = step.actor === as;

  if (!mine) {
    return (
      <div className="step-actions">
        <div className="buttons-small">
          {step.actions.map((a) => (
            <button key={a} type="button" className={`btn btn-small ${buttonClass[a]}`} disabled>
              {buttonLabel[a]}
            </button>
          ))}
        </div>
        <p className="hint small">
          Only {nameOf(people, step.actor)} can do this.{' '}
          <button type="button" className="link" onClick={() => setAs(step.actor!)}>
            Act as {nameOf(people, step.actor)}
          </button>
        </p>
      </div>
    );
  }

  const run = async (f: () => Promise<void>) => {
    setBusy(true);
    setError('');
    try {
      await f();
      setComment('');
      setEditing(false);
      await reload();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  if (step.actions.includes('resubmit')) {
    const v = req.variables;
    const initial: FormValues = {
      title: req.title,
      amount: req.amount,
      category: String(v.category ?? 'Events'),
      costCentre: String(v.costCentre ?? ''),
      reason: String(v.reason ?? ''),
    };
    const sentBack = req.line.find((s) => s.state === 'returned');
    return (
      <div className="step-actions">
        {sentBack && (
          <p className="hint small">
            {sentBack.who}
            {sentBack.comment ? `: “${sentBack.comment}”` : ''}
          </p>
        )}
        {error && <p className="error small" role="alert">{error}</p>}
        {editing ? (
          <div className="popover">
            <RequestForm
              initial={initial}
              submitLabel="Resubmit"
              busy={busy}
              onCancel={() => setEditing(false)}
              onSubmit={(values) => run(() => api.resubmit(step.taskId!, { as, ...values }))}
            />
          </div>
        ) : (
          <button type="button" className="btn btn-primary btn-small" onClick={() => setEditing(true)}>
            Revise and resubmit
          </button>
        )}
      </div>
    );
  }

  return (
    <div className="step-actions">
      <label className="sr-only" htmlFor={`c-${step.id}`}>
        Comment for {nameOf(people, req.submittedBy)}
      </label>
      <textarea
        id={`c-${step.id}`}
        rows={2}
        placeholder="Comment (needed to reject or send back)"
        value={comment}
        onChange={(e) => setComment(e.target.value)}
      />
      {error && <p className="error small" role="alert">{error}</p>}
      <div className="buttons-small">
        {(['approve', 'revise', 'reject'] as const)
          .filter((a) => step.actions!.includes(a))
          .map((a) => (
            <button
              key={a}
              type="button"
              className={`btn btn-small ${buttonClass[a]}`}
              disabled={busy}
              onClick={() => run(() => api.decide(step.taskId!, as, a, comment))}
            >
              {buttonLabel[a]}
            </button>
          ))}
      </div>
    </div>
  );
}

const buttonLabel = { submit: 'Submit', resubmit: 'Resubmit', approve: 'Approve', reject: 'Reject', revise: 'Revise' };
const buttonClass = {
  submit: 'btn-primary',
  resubmit: 'btn-primary',
  approve: 'btn-primary',
  reject: 'btn-danger',
  revise: 'btn-quiet',
};

function Legend() {
  return (
    <div className="legend" aria-label="Legend">
      <span>
        <span className="dot-mini done" aria-hidden="true" />
        Done
      </span>
      <span>
        <span className="dot-mini now" aria-hidden="true" />
        Now
      </span>
      <span>
        <span className="dot-mini later" aria-hidden="true" />
        Not yet
      </span>
      <span>
        <span className="dot-mini returned" aria-hidden="true" />
        Sent back
      </span>
      <span>
        <span className="dot-mini rejected" aria-hidden="true" />
        Rejected
      </span>
    </div>
  );
}
