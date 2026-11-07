import { useState, type FormEvent } from 'react';
import type { Submission } from '../api';

export const costCentres = ['CC-210 Platform', 'CC-120 Sales', 'CC-330 Operations'];
export const categories = ['Events', 'Travel', 'Equipment', 'Software'];

export type FormValues = Omit<Submission, 'as'>;

export const emptyForm: FormValues = { title: '', amount: 0, category: 'Events', costCentre: costCentres[0], reason: '' };

// The fields a request is made of, for submitting it and for resubmitting it
// after it was sent back.
export function RequestForm({
  formId,
  initial,
  submitLabel,
  busy,
  onSubmit,
  onCancel,
}: {
  formId?: string;
  initial: FormValues;
  submitLabel: string;
  busy: boolean;
  onSubmit: (values: FormValues) => void;
  onCancel?: () => void;
}) {
  const [v, setV] = useState(initial);
  const [amountText, setAmountText] = useState(initial.amount ? String(initial.amount) : '');
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSubmit({ ...v, amount: Number(amountText.replace(/[^\d]/g, '')) });
  };
  return (
    <form id={formId} className="form" onSubmit={submit}>
      <div className="field">
        <label htmlFor="title">What is it for</label>
        <input id="title" required value={v.title} onChange={(e) => setV({ ...v, title: e.target.value })} />
      </div>
      <div className="field-row">
        <div className="field">
          <label htmlFor="amount">Amount (Rp)</label>
          <input id="amount" required inputMode="numeric" value={amountText} onChange={(e) => setAmountText(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="category">Category</label>
          <select id="category" value={v.category} onChange={(e) => setV({ ...v, category: e.target.value })}>
            {categories.map((c) => (
              <option key={c}>{c}</option>
            ))}
          </select>
        </div>
      </div>
      <div className="field">
        <label htmlFor="cc">Cost centre</label>
        <select id="cc" value={v.costCentre} onChange={(e) => setV({ ...v, costCentre: e.target.value })}>
          {costCentres.map((c) => (
            <option key={c}>{c}</option>
          ))}
        </select>
      </div>
      <div className="field">
        <label htmlFor="reason">Why it is needed</label>
        <textarea id="reason" rows={3} value={v.reason} onChange={(e) => setV({ ...v, reason: e.target.value })} />
      </div>
      <div className="buttons">
        {onCancel && (
          <button type="button" className="btn btn-quiet" onClick={onCancel}>
            Cancel
          </button>
        )}
        <button type="submit" className="btn btn-primary" disabled={busy}>
          {busy ? 'Sending…' : submitLabel}
        </button>
      </div>
    </form>
  );
}
