import type { ReactNode } from 'react';
import type { LineStep, Person } from '../api';

// The approval line: one column per step, a dot for its state, a bar to the
// next, and under each step what can be done there. A step's actions are
// rendered by the page, because what a button does depends on the page.
export function ApprovalLine({
  line,
  renderActions,
}: {
  line: LineStep[];
  renderActions?: (step: LineStep) => ReactNode;
}) {
  return (
    <ol className="line">
      {line.map((step, i) => (
        <li key={step.id} className={`step step-${step.state}`}>
          <div className="step-track">
            <span className="dot" aria-hidden="true">
              {step.state === 'done' && <Check />}
              {step.state === 'rejected' && <Cross />}
              {step.state === 'returned' && <Back />}
            </span>
            {i < line.length - 1 && <span className={step.state === 'done' ? 'bar bar-done' : 'bar'} aria-hidden="true" />}
          </div>
          <div className="step-name">{step.name}</div>
          <div className="step-state">{stateLabel[step.state]}</div>
          {step.who && <div className="step-who">{step.who}</div>}
          {step.comment && <div className="step-comment">“{step.comment}”</div>}
          {renderActions?.(step)}
        </li>
      ))}
    </ol>
  );
}

const stateLabel: Record<LineStep['state'], string> = {
  done: 'Done',
  now: 'Now',
  later: 'Not yet',
  rejected: 'Rejected',
  returned: 'Sent back',
  skipped: 'Not needed',
};

export function nameOf(people: Person[], username?: string) {
  return people.find((p) => p.username === username)?.name ?? username ?? 'someone';
}

const Check = () => (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round">
    <path d="M5 12l5 5L20 7" />
  </svg>
);
const Cross = () => (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round">
    <path d="M6 6l12 12M18 6L6 18" />
  </svg>
);
const Back = () => (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round">
    <path d="M9 14L4 9l5-5" />
    <path d="M4 9h10a6 6 0 010 12h-2" />
  </svg>
);
