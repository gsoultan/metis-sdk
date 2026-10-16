package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// FetchAndLock claims up to maxTasks external tasks on a topic for workerID.
// The tasks stay invisible to other workers for lockDuration; a worker that
// crashes simply lets its locks expire and the tasks become fetchable again.
func (c *Client) FetchAndLock(ctx context.Context, topic, workerID string, maxTasks int, lockDuration time.Duration) ([]ExternalTask, error) {
	var out struct {
		Tasks []ExternalTask `json:"tasks"`
		// This endpoint reports failures inline rather than by status code.
		Error string `json:"error"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/external-tasks/fetch-and-lock", map[string]any{
		"topic":            topic,
		"worker_id":        workerID,
		"max_tasks":        maxTasks,
		"lock_duration_ms": lockDuration.Milliseconds(),
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("metis: fetch-and-lock: %s", out.Error)
	}
	return out.Tasks, nil
}

// CompleteExternalTask reports a task done, writes variables back into the
// process, and the instance moves on. workerID must match the lock holder.
func (c *Client) CompleteExternalTask(ctx context.Context, taskID, workerID string, variables Variables) error {
	var out struct {
		Error string `json:"error"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/external-tasks/"+url.PathEscape(taskID)+"/complete", map[string]any{
		"worker_id": workerID,
		"variables": variables,
	}, &out)
	if err != nil {
		return err
	}
	if out.Error != "" {
		return fmt.Errorf("metis: complete external task: %s", out.Error)
	}
	return nil
}

// FailExternalTask reports a task failed. retriesRemaining says how many
// attempts are left after this one — zero means give up and leave the task
// for an operator. retryAfter is how long before it becomes fetchable again.
func (c *Client) FailExternalTask(ctx context.Context, taskID, workerID, message, details string, retriesRemaining int, retryAfter time.Duration) error {
	var out struct {
		Error string `json:"error"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/external-tasks/"+url.PathEscape(taskID)+"/failure", map[string]any{
		"worker_id":        workerID,
		"error_message":    message,
		"error_details":    details,
		"retries":          retriesRemaining,
		"retry_timeout_ms": retryAfter.Milliseconds(),
	}, &out)
	if err != nil {
		return err
	}
	if out.Error != "" {
		return fmt.Errorf("metis: fail external task: %s", out.Error)
	}
	return nil
}

// ExtendExternalTaskLock asks for more time on the lock workerID holds on a
// task: the lock is moved to lockDuration from now, and the answer is when it
// now runs out by the server's clock. lockDuration may be at most a day.
//
// Only the worker holding the lock can extend it, and only before it runs out.
// Anything else is refused with a 400 ([IsInvalid]): the task may already be
// with another worker, so stop working on it and let it be fetched again. It
// needs **Metis v0.5.0 or later**; an older server has no such route and
// answers 404 ([IsNotFound]).
//
// [Worker] does this for you when [WorkerOptions.ExtendLock] is set.
func (c *Client) ExtendExternalTaskLock(ctx context.Context, taskID, workerID string, lockDuration time.Duration) (time.Time, error) {
	if lockDuration <= 0 {
		return time.Time{}, errors.New("metis: ExtendExternalTaskLock needs a positive lock duration")
	}
	var out struct {
		LockExpiration time.Time `json:"lock_expiration"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/external-tasks/"+url.PathEscape(taskID)+"/extend-lock", map[string]any{
		"worker_id":        workerID,
		"lock_duration_ms": lockDuration.Milliseconds(),
	}, &out)
	if err != nil {
		return time.Time{}, err
	}
	return out.LockExpiration, nil
}

// Handler does the actual work of one external task. Return the variables to
// write back and the task completes; return an error and it is retried after
// [WorkerOptions.RetryDelay] until its retries run out.
//
// Read the task's input with the [Variables] accessors rather than a type
// assertion — every number arrives as a float64, so task.Variables["amount"].(int)
// panics where task.Variables.Int("amount") does not:
//
//	func(ctx context.Context, task *metis.ExternalTask) (metis.Variables, error) {
//		amount, ok := task.Variables.Float64("amount")
//		if !ok {
//			return nil, fmt.Errorf("task %s carries no amount", task.ID)
//		}
//		...
//		return metis.Variables{"reversed": true}, nil
//	}
//
// **Handlers must be idempotent.** The engine re-dispatches a task whose lock
// expired, and a handler whose work succeeded but whose report-back failed sees
// the same task again — see [Worker] for both windows. ctx is cancelled when
// the lock would expire; respect it, because past that point another worker may
// already hold this task. With [WorkerOptions.ExtendLock] the lock is kept
// alive while the handler runs, and ctx is cancelled only once it could not
// be.
type Handler func(ctx context.Context, task *ExternalTask) (Variables, error)

// WorkerOptions tune a Worker. The zero value is usable.
type WorkerOptions struct {
	// MaxTasks per fetch. Default 5.
	MaxTasks int
	// LockDuration is how long fetched tasks stay ours. It is also the budget
	// a Handler gets: the handler context is cancelled when the lock would
	// expire, because finishing work on a lock another worker may now hold
	// means two workers charging the same card. Default 1 minute.
	//
	// The budget is per batch, not per task: the tasks of one fetch are locked
	// together and run one after another, so the last of them has whatever the
	// earlier ones left. A task whose lock runs out before its turn is not
	// started; it is fetched again later. Size LockDuration for MaxTasks
	// handlers in a row, or lower MaxTasks.
	LockDuration time.Duration
	// PollInterval is the pause between fetches that returned nothing.
	// Default 2 seconds.
	PollInterval time.Duration
	// RetryDelay is how long a failed task waits before another attempt.
	// Default 10 seconds.
	RetryDelay time.Duration
	// ExtendLock keeps a running handler's lock alive. When half of what is
	// left of it has passed, the worker asks for another LockDuration, and
	// keeps doing so until the handler returns, so work may outlast
	// LockDuration without another worker being handed the task. The handler's
	// context is then cancelled only when the lock could not be extended: at
	// once if the server says the lock is no longer this worker's, or when it
	// runs out if the server could not be reached in time.
	//
	// A handler that never returns now holds its task for as long as the
	// process lives, where without this the lock would hand it on; put your
	// own timeout on work that can hang.
	//
	// Only the task running is extended. The rest of its batch still run out
	// LockDuration after the fetch, so with long work set MaxTasks to 1.
	//
	// It needs **Metis v0.5.0 or later**. Against an older server the first
	// attempt is answered 404; that is reported once through OnError, and the
	// worker carries on as if this were unset.
	ExtendLock bool
}

func (o WorkerOptions) withDefaults() WorkerOptions {
	if o.MaxTasks <= 0 {
		o.MaxTasks = 5
	}
	if o.LockDuration <= 0 {
		o.LockDuration = time.Minute
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 2 * time.Second
	}
	if o.RetryDelay <= 0 {
		o.RetryDelay = 10 * time.Second
	}
	return o
}

// Worker polls one topic and runs a Handler for each task. Create with
// NewWorker, start with Run.
type Worker struct {
	client   *Client
	topic    string
	workerID string
	handler  Handler
	opts     WorkerOptions

	// cannotExtend is set once the server has said it has no extend-lock
	// route, so the worker stops asking.
	cannotExtend atomic.Bool

	// OnError, if set, observes errors the loop absorbs — transport failures,
	// report-back failures. The loop keeps going either way: a worker's job is
	// to outlive blips, but silently absorbing errors makes a dead integration
	// look idle, so they are surfaced to whoever wants them.
	OnError func(error)
}

// NewWorker builds a worker for one topic. workerID names this worker in
// locks — use something stable and unique per process, such as hostname+pid.
func NewWorker(client *Client, topic, workerID string, opts WorkerOptions, handler Handler) *Worker {
	return &Worker{
		client:   client,
		topic:    topic,
		workerID: workerID,
		handler:  handler,
		opts:     opts.withDefaults(),
	}
}

// Run polls until ctx is cancelled. It returns ctx.Err() on shutdown and
// never gives up on transient errors — it reports them via OnError and keeps
// polling, because a worker that exits on the first network blip takes the
// whole integration down with it.
//
// On shutdown a handler already running is cancelled, and Run waits for it to
// return. If it finished its work, that is still reported to the engine, for up
// to ten seconds after ctx is done, so a deploy does not throw finished work
// away. If it returned an error because it was cancelled, nothing is reported:
// the task is left to its lock and comes back with its retries intact. Tasks of
// the batch not yet started are left the same way.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Taken before the request, so it is never later than the moment the
		// server started the locks: every lock in the batch runs out at or after
		// lockDeadline. The local clock is used rather than the server's
		// LockExpiration, because the two clocks need not agree.
		fetchedAt := time.Now()
		tasks, err := w.client.FetchAndLock(ctx, w.topic, w.workerID, w.opts.MaxTasks, w.opts.LockDuration)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.report(fmt.Errorf("fetch %q: %w", w.topic, err))
			if err := sleep(ctx, w.opts.PollInterval); err != nil {
				return err
			}
			continue
		}

		lockDeadline := fetchedAt.Add(w.opts.LockDuration)
		for i := range tasks {
			if ctx.Err() != nil {
				// Shutting down: the tasks not started are left to their locks,
				// which expire and hand them to another worker. Starting one now
				// would only run its handler on a cancelled context and fail it.
				return ctx.Err()
			}
			w.runOne(ctx, &tasks[i], lockDeadline)
		}

		if len(tasks) == 0 {
			if err := sleep(ctx, w.opts.PollInterval); err != nil {
				return err
			}
		}
	}
}

// reportTimeout bounds one report-back made after Run's context was cancelled.
const reportTimeout = 10 * time.Second

// runOne executes the handler for one task and reports the outcome.
//
// lockDeadline is when the batch's locks run out. The tasks of a batch run one
// after another, so a later task has less of its lock left than the first; the
// handler gets what remains, not a fresh LockDuration, and a task whose lock has
// already run out is not started at all.
func (w *Worker) runOne(ctx context.Context, task *ExternalTask, lockDeadline time.Time) {
	if !time.Now().Before(lockDeadline) {
		// Another worker may already hold it. Running it here is the double
		// execution the lock exists to prevent, and failing it would spend one
		// of its retries on work nobody attempted.
		w.report(fmt.Errorf("task %s was not started: its lock ran out while earlier tasks of the batch ran; it will be fetched again", task.ID))
		return
	}

	// The handler's budget is the lock: past it, another worker may hold this
	// task, and two workers completing the same work is the failure external
	// tasks exist to prevent.
	var (
		variables Variables
		err       error
		lost      bool
	)
	if w.opts.ExtendLock && !w.cannotExtend.Load() {
		// The deadline moves as the lock is extended, so it is kept by
		// keepLock rather than fixed on the context.
		handlerCtx, cancel := context.WithCancel(ctx)
		kept := make(chan struct{})
		go func() {
			defer close(kept)
			lost = w.keepLock(handlerCtx, cancel, task.ID, lockDeadline)
		}()
		variables, err = safelyHandle(handlerCtx, w.handler, task)
		cancel()
		<-kept
	} else {
		handlerCtx, cancel := context.WithDeadline(ctx, lockDeadline)
		variables, err = safelyHandle(handlerCtx, w.handler, task)
		cancel()
	}

	// The report-back must outlive shutdown. A handler that finished as Run's
	// context was cancelled has done its work; reporting it on that context
	// would fail at once, and the side effect would happen again on whichever
	// worker the task is re-dispatched to.
	reportCtx, cancelReport := context.WithTimeout(context.WithoutCancel(ctx), reportTimeout)
	defer cancelReport()

	if err != nil {
		if ctx.Err() != nil {
			// The handler stopped because the worker is shutting down, not
			// because the work failed. Reporting it would spend a retry — the
			// last one raises an incident — on a deploy. The lock expires and
			// the task comes back untouched.
			w.report(fmt.Errorf("task %s was interrupted by shutdown and left to its lock: %w", task.ID, err))
			return
		}
		if lost {
			// The task is another worker's now. Failing it would be refused,
			// or, worse, spend a retry of the attempt that holds it.
			return
		}
		remaining := task.Retries - 1
		if remaining < 0 {
			remaining = 0
		}
		if failErr := w.client.FailExternalTask(reportCtx, task.ID, w.workerID, err.Error(), "", remaining, w.opts.RetryDelay); failErr != nil {
			w.report(fmt.Errorf("task %s failed and the failure could not be reported: %w", task.ID, failErr))
		}
		return
	}

	if err := w.client.CompleteExternalTask(reportCtx, task.ID, w.workerID, variables); err != nil {
		// The work succeeded but the engine does not know. Do not retry the
		// handler — the side effect happened. The lock expires, the engine
		// re-dispatches, and the handler must be idempotent; the doc for
		// Handler says so because of exactly this window.
		w.report(fmt.Errorf("task %s was done but completing it failed: %w", task.ID, err))
	}
}

// minExtendWait is the shortest pause between two extensions of one lock, or a
// tenth of LockDuration when that is shorter. It keeps a server that answers
// slowly, or a lock nearly gone, from turning the extension into a busy loop:
// with less than twice this left, the lock is left to run out.
const minExtendWait = time.Second

// keepLock extends the lock on taskID while ctx lives, and calls cancel when
// the lock is lost: at once if the server says it is no longer ours, or when
// lockDeadline passes if it could not be extended in time. It reports whether
// the server said the lock was lost.
//
// lockDeadline is kept on the local clock, as Run keeps it: an extension asked
// for at t runs out no earlier than t plus LockDuration, whatever the server's
// clock says.
func (w *Worker) keepLock(ctx context.Context, cancel context.CancelFunc, taskID string, lockDeadline time.Time) (lost bool) {
	defer cancel()
	floor := min(minExtendWait, w.opts.LockDuration/10)
	extend := true
	for {
		remaining := time.Until(lockDeadline)
		if remaining <= 0 {
			return false
		}
		wait := remaining / 2
		if !extend || wait < floor {
			wait = remaining
		}
		if err := sleep(ctx, wait); err != nil {
			return false
		}
		if !time.Now().Before(lockDeadline) {
			return false
		}

		asked := time.Now()
		reqCtx, cancelReq := context.WithDeadline(ctx, lockDeadline)
		_, err := w.client.ExtendExternalTaskLock(reqCtx, taskID, w.workerID, w.opts.LockDuration)
		cancelReq()
		switch {
		case err == nil:
			lockDeadline = asked.Add(w.opts.LockDuration)
		case ctx.Err() != nil:
			return false
		case IsNotFound(err):
			// No such route: a server older than v0.5.0. Keep to the lock we
			// have, and stop asking, for this task and every later one.
			extend = false
			if w.cannotExtend.CompareAndSwap(false, true) {
				w.report(fmt.Errorf("the server cannot extend locks (it needs Metis v0.5.0 or later), so ExtendLock is off for this worker: %w", err))
			}
		case IsInvalid(err):
			// The lock is not ours any more. Another worker may be running
			// this task now; stop at once rather than finish it twice.
			w.report(fmt.Errorf("task %s lost its lock and its handler was stopped: %w", taskID, err))
			return true
		default:
			// Unreachable or failing for now. Try again while the lock lasts.
			w.report(fmt.Errorf("task %s: extending its lock failed, will retry while it lasts: %w", taskID, err))
		}
	}
}

// safelyHandle keeps a panicking handler from killing the whole worker: one
// poison task must not stop every other task on the topic.
func safelyHandle(ctx context.Context, handler Handler, task *ExternalTask) (vars Variables, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panicked: %v", r)
		}
	}()
	return handler(ctx, task)
}

func (w *Worker) report(err error) {
	if w.OnError != nil {
		w.OnError(err)
	}
}

// sleep waits without ignoring shutdown.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ErrStopped is a convenience sentinel a caller can use to distinguish a
// deliberate shutdown from a failure when wrapping Run.
var ErrStopped = errors.New("metis: worker stopped")
