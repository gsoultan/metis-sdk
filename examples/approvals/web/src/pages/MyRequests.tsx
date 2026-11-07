import { useEffect, useState } from 'react';
import { api, rupiah, type Person, type RequestSummary } from '../api';
import { nameOf } from '../components/ApprovalLine';

export function MyRequests({ as, people, go }: { as: string; people: Person[]; go: (to: string) => void }) {
  const [rows, setRows] = useState<RequestSummary[] | null>(null);
  const [error, setError] = useState('');
  const me = people.find((p) => p.username === as);
  const onlyMine = me?.role === 'requester';

  useEffect(() => {
    let live = true;
    const load = () =>
      api.requests(onlyMine ? as : undefined).then(
        (r) => live && setRows(r),
        (e: Error) => live && setError(e.message),
      );
    load();
    const t = setInterval(load, 5000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [as, onlyMine]);

  return (
    <div className="stack">
      <div className="title-row">
        <h1>{onlyMine ? 'My requests' : 'All requests'}</h1>
        <a
          href="/new"
          className="btn btn-primary"
          onClick={(e) => {
            e.preventDefault();
            go('/new');
          }}
        >
          New request
        </a>
      </div>
      {error && <p className="error" role="alert">{error}</p>}
      {rows && rows.length === 0 && <p className="hint">No requests yet. Make one with New request.</p>}
      <ul className="list">
        {rows?.map((r) => (
          <li key={r.id}>
            <a
              className="list-item"
              href={`/requests/${r.id}`}
              onClick={(e) => {
                e.preventDefault();
                go(`/requests/${r.id}`);
              }}
            >
              <span className="list-main">
                <span className="list-title">{r.title}</span>
                <span className="list-sub">
                  {nameOf(people, r.submittedBy)} · {new Date(r.createdAt).toLocaleString()}
                </span>
              </span>
              <span className="list-side">
                <span className="amount">{rupiah(r.amount)}</span>
                <span className={`pill pill-${r.outcome.replace(' ', '-')}`}>{r.outcome}</span>
              </span>
            </a>
          </li>
        ))}
      </ul>
    </div>
  );
}
