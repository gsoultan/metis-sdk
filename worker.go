package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
// already hold this task.
type Handler func(ctx context.Context, task *ExternalTask) (Variables, error)

// WorkerOptions tune a Worker. The zero value is usable.
type WorkerOptions struct {
	// MaxTasks per fetch. Default 5.
	MaxTasks int
	// LockDuration is how long fetched tasks stay ours. It is also the budget
	// a Handler gets: the handler context is cancelled when the lock would
	// expire, because finishing work on a lock another worker may now hold
	// means two workers charging the same card. Default 1 minute.
	LockDuration time.Duration
	// PollInterval is the pause between fetches that returned nothing.
	// Default 2 seconds.
	PollInterval time.Duration
	// RetryDelay is how long a failed task waits before another attempt.
	// Default 10 seconds.
	RetryDelay time.Duration
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
func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

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

		for i := range tasks {
			w.runOne(ctx, &tasks[i])
		}

		if len(tasks) == 0 {
			if err := sleep(ctx, w.opts.PollInterval); err != nil {
				return err
			}
		}
	}
}

// runOne executes the handler for one task and reports the outcome.
func (w *Worker) runOne(ctx context.Context, task *ExternalTask) {
	// The handler's budget is the lock: past it, another worker may hold this
	// task, and two workers completing the same work is the failure external
	// tasks exist to prevent.
	handlerCtx, cancel := context.WithTimeout(ctx, w.opts.LockDuration)
	defer cancel()

	variables, err := safelyHandle(handlerCtx, w.handler, task)
	if err != nil {
		remaining := task.Retries - 1
		if remaining < 0 {
			remaining = 0
		}
		if failErr := w.client.FailExternalTask(ctx, task.ID, w.workerID, err.Error(), "", remaining, w.opts.RetryDelay); failErr != nil {
			w.report(fmt.Errorf("task %s failed and the failure could not be reported: %w", task.ID, failErr))
		}
		return
	}

	if err := w.client.CompleteExternalTask(ctx, task.ID, w.workerID, variables); err != nil {
		// The work succeeded but the engine does not know. Do not retry the
		// handler — the side effect happened. The lock expires, the engine
		// re-dispatches, and the handler must be idempotent; the doc for
		// Handler says so because of exactly this window.
		w.report(fmt.Errorf("task %s was done but completing it failed: %w", task.ID, err))
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
