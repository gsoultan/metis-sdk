import { useCallback, useEffect, useState } from 'react';
import { api, type Person } from './api';
import { Inbox } from './pages/Inbox';
import { MyRequests } from './pages/MyRequests';
import { NewRequest } from './pages/NewRequest';
import { RequestPage } from './pages/RequestPage';

// A router of four paths does not need a library.
function usePath(): [string, (to: string) => void] {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    const onPop = () => setPath(window.location.pathname);
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);
  const go = useCallback((to: string) => {
    window.history.pushState(null, '', to);
    setPath(to);
    window.scrollTo(0, 0);
  }, []);
  return [path, go];
}

function rememberedPerson(): string {
  try {
    return localStorage.getItem('approvals.as') ?? 'rina';
  } catch {
    return 'rina';
  }
}

export function App() {
  const [path, go] = usePath();
  const [people, setPeople] = useState<Person[]>([]);
  const [as, setAsState] = useState(rememberedPerson);
  const [error, setError] = useState('');

  useEffect(() => {
    api.people().then(setPeople, (e: Error) => setError(e.message));
  }, []);

  const setAs = (username: string) => {
    setAsState(username);
    try {
      localStorage.setItem('approvals.as', username);
    } catch {
      /* a private window keeps no settings; the picker still works */
    }
  };

  const requestID = path.match(/^\/requests\/([^/]+)$/)?.[1];
  const page = requestID ? 'request' : path === '/new' ? 'new' : path === '/inbox' ? 'inbox' : 'mine';
  const link = (to: string, label: string, current: boolean) => (
    <a
      href={to}
      className={current ? 'nav-link current' : 'nav-link'}
      aria-current={current ? 'page' : undefined}
      onClick={(e) => {
        e.preventDefault();
        go(to);
      }}
    >
      {label}
    </a>
  );

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand-row">
          <div className="brand-mark" aria-hidden="true">E</div>
          <div className="brand">Expense approvals</div>
          <nav className="nav" aria-label="Main">
            {link('/new', 'New request', page === 'new')}
            {link('/', 'Requests', page === 'mine' || page === 'request')}
            {link('/inbox', 'Inbox', page === 'inbox')}
          </nav>
        </div>
        <div className="persona">
          <label htmlFor="as">Acting as</label>
          <select id="as" value={as} onChange={(e) => setAs(e.target.value)}>
            {people.map((p) => (
              <option key={p.username} value={p.username}>
                {p.name} ({p.role})
              </option>
            ))}
          </select>
        </div>
      </header>
      <main className="main">
        {error && <p className="error" role="alert">Could not reach the sample's backend: {error}</p>}
        {page === 'new' && <NewRequest as={as} people={people} go={go} />}
        {page === 'mine' && <MyRequests as={as} people={people} go={go} />}
        {page === 'inbox' && <Inbox as={as} people={people} go={go} />}
        {page === 'request' && requestID && <RequestPage id={requestID} as={as} people={people} setAs={setAs} />}
      </main>
    </div>
  );
}
