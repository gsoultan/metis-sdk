import { useEffect, useState } from 'react';
import { api, rupiah, type InboxItem, type Person } from '../api';
import { nameOf } from '../components/ApprovalLine';

// What is waiting for the person the app is acting as, as metis decides it
// for them: the approvals offered to their group, and for the requester the
// requests sent back to revise.
export function Inbox({ as, people, go }: { as: string; people: Person[]; go: (to: string) => void }) {
  const [items, setItems] = useState<InboxItem[] | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let live = true;
    const load = () =>
      api.inbox(as).then(
        (r) => live && setItems(r),
        (e: Error) => live && setError(e.message),
      );
    load();
    const t = setInterval(load, 5000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [as]);

  return (
    <div className="stack">
      <h1>Waiting for {nameOf(people, as)}</h1>
      {error && <p className="error" role="alert">{error}</p>}
      {items && items.length === 0 && <p className="hint">Nothing is waiting. Switch “Acting as” to see someone else's inbox.</p>}
      <ul className="list">
        {items?.map((i) => (
          <li key={i.taskId}>
            <a
              className="list-item"
              href={`/requests/${i.request.id}`}
              onClick={(e) => {
                e.preventDefault();
                go(`/requests/${i.request.id}`);
              }}
            >
              <span className="list-main">
                <span className="list-title">{i.request.title}</span>
                <span className="list-sub">
                  {i.step === 'revise' ? 'Sent back to you to revise' : i.stepName} · from {nameOf(people, i.request.submittedBy)}
                </span>
              </span>
              <span className="list-side">
                <span className="amount">{rupiah(i.request.amount)}</span>
                <span className="pill pill-now">Your turn</span>
              </span>
            </a>
          </li>
        ))}
      </ul>
    </div>
  );
}
