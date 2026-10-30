package metis

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrInstanceFailed means an instance being waited for failed: an incident
// nothing handled stopped it. [Client.ListIncidents] says why.
var ErrInstanceFailed = errors.New("metis: the instance failed")

// ErrNoReceiver means a message was not sent because nothing was waiting for
// it: the instance finished, or never reached the step, before the wait ended.
var ErrNoReceiver = errors.New("metis: nothing is waiting for the message")

// WaitError is a wait that ended before what it waited for: the context ran
// out or was cancelled. Instance is the instance as last read, nil if it was
// never read, so the caller can see how far it got.
type WaitError struct {
	Instance *Instance
	Err      error
}

func (e *WaitError) Error() string {
	if e.Instance == nil {
		return fmt.Sprintf("metis: stopped waiting: %v", e.Err)
	}
	return fmt.Sprintf("metis: stopped waiting for instance %s, last %s: %v", e.Instance.ID, e.Instance.Status, e.Err)
}

func (e *WaitError) Unwrap() error { return e.Err }

// WaitOptions says what to wait for and how often to look.
type WaitOptions struct {
	// Until is the state waited for. Nil waits for the instance to finish:
	// completed, or failed.
	Until func(*Instance) bool
	// Poll is how long to wait between reads. Zero means half a second.
	// Every read is a request, so keep it to what the caller can act on.
	Poll time.Duration
}

func (o WaitOptions) poll() time.Duration {
	if o.Poll > 0 {
		return o.Poll
	}
	return 500 * time.Millisecond
}

// WaitForInstance reads the instance until opts.Until holds, and returns it as
// it was then. Bound the wait with ctx: the instance may be waiting for a
// person, and nothing else ends the wait.
//
//	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
//	defer cancel()
//	done, err := client.WaitForInstance(ctx, id, metis.WaitOptions{})
//
// A failed instance ends the wait with [ErrInstanceFailed] unless Until
// accepts it, and the instance is returned with the error. A context that
// ends first gives a [*WaitError] carrying the instance as last read. A read
// that fails in a way that may pass — no answer, a 5xx, a 429 — is tried again
// at the next poll; any other error ends the wait.
func (c *Client) WaitForInstance(ctx context.Context, instanceID string, opts WaitOptions) (*Instance, error) {
	if instanceID == "" {
		return nil, errors.New("metis: wait for instance: no instance ID")
	}
	until := opts.Until
	if until == nil {
		until = func(i *Instance) bool { return i.IsFinished() }
	}
	var last *Instance
	for {
		inst, err := c.GetInstance(ctx, instanceID)
		switch {
		case err == nil:
			last = inst
			if until(inst) {
				return inst, nil
			}
			if inst.Status == ProcessFailed {
				return inst, fmt.Errorf("%w: instance %s", ErrInstanceFailed, inst.ID)
			}
		case ctx.Err() != nil:
			// Reported below with what was last seen.
		case !retryable(ctx, err):
			return last, err
		}
		if err := sleep(ctx, opts.poll()); err != nil {
			return last, &WaitError{Instance: last, Err: err}
		}
	}
}

// StartAndWait starts a process and waits for the instance as
// [Client.WaitForInstance] does — for a short, unattended process whose result
// the caller needs now:
//
//	done, err := client.StartAndWait(ctx, project, "credit-check", vars, metis.WaitOptions{})
//	if err != nil {
//		return err
//	}
//	approved, _ := done.Variables["approved"].(bool)
//
// When the start succeeds and the wait does not, the instance ID is still
// returned, inside the result when there is one and through
// [WaitError.Instance] when the context ran out, so the caller can find the
// instance again. A process that waits for a person will usually run past any
// sensible deadline; start it and come back to it instead.
func (c *Client) StartAndWait(ctx context.Context, projectID, definitionKey string, variables Variables, wait WaitOptions, opts ...StartOption) (*Instance, error) {
	id, err := c.StartProcess(ctx, projectID, definitionKey, variables, opts...)
	if err != nil {
		return nil, err
	}
	inst, err := c.WaitForInstance(ctx, id, wait)
	if inst == nil && err != nil {
		inst = &Instance{ID: id}
		if waitErr, ok := errors.AsType[*WaitError](err); ok && waitErr.Instance == nil {
			waitErr.Instance = inst
		}
	}
	return inst, err
}

// Receiver names who a message is for and where it waits for it.
type Receiver struct {
	// Instance is the instance the message is for.
	Instance string
	// WaitingAt is the step that catches the message: the receive task or
	// intermediate catch event, or for a boundary message event the activity
	// it is attached to.
	WaitingAt string
	// Poll is how long to wait between reads of the instance. Zero means half
	// a second.
	Poll time.Duration
}

// SendMessageTo sends a message once the instance it is for is waiting for
// it, and not before.
//
// [Client.SendMessage] delivers to whatever is waiting at that moment, and
// answers success when nothing is: the server drops a message nobody waits
// for. A reply that arrives while the instance is still on the step before
// is lost that way, with nothing to say so. SendMessageTo reads the instance
// until one of its paths stands at r.WaitingAt, then sends.
//
//	err := client.SendMessageTo(ctx, project, "payment-received", orderID, vars,
//		metis.Receiver{Instance: id, WaitingAt: "WaitForPayment"})
//
// The wait is bounded by ctx. The message is not sent, and the error wraps
// [ErrNoReceiver], when the instance finishes first or ctx ends first; in the
// second case it wraps the context's error too. Sending can still fail like
// any write and is not retried.
func (c *Client) SendMessageTo(ctx context.Context, projectID, messageName, correlationKey string, variables Variables, r Receiver) error {
	if r.Instance == "" || r.WaitingAt == "" {
		return errors.New("metis: send message: the receiver needs an instance and the step it waits at")
	}
	inst, err := c.WaitForInstance(ctx, r.Instance, WaitOptions{
		Poll: r.Poll,
		Until: func(i *Instance) bool {
			return i.WaitingAt(r.WaitingAt) || i.IsFinished()
		},
	})
	if waitErr, ok := errors.AsType[*WaitError](err); ok {
		return fmt.Errorf("%w: instance %s did not reach %s: %w", ErrNoReceiver, r.Instance, r.WaitingAt, waitErr.Err)
	}
	if err != nil {
		return err
	}
	if !inst.WaitingAt(r.WaitingAt) {
		return fmt.Errorf("%w: instance %s is %s", ErrNoReceiver, inst.ID, inst.Status)
	}
	return c.SendMessage(ctx, projectID, messageName, correlationKey, variables)
}
