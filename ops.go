package metis

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"time"
)

// --- Watching incidents -----------------------------------------------------

// WatchOptions says how often a watch looks.
type WatchOptions struct {
	// Poll is how long to wait between looks. Zero means 30 seconds. Each
	// look is one listing plus one request per instance that needs attention,
	// so keep it to what someone will act on.
	Poll time.Duration
}

// WatchIncidents yields each open incident of a project once, as it appears,
// until ctx ends or the loop stops:
//
//	for incident, err := range client.WatchIncidents(ctx, projectID, metis.WatchOptions{}) {
//		if err != nil {
//			log.Print(err) // the watch goes on
//			continue
//		}
//		page(incident.Error, incident.Instance.ID)
//	}
//
// The incidents open when the watch starts are yielded first. Each look lists
// the instances that need attention, as [ListInstancesOptions.NeedsAttention]
// does, and reads the incidents of each. The server has no project-wide
// incident listing, so a project with many such instances costs as many
// requests per look.
//
// A look that fails in a way that may pass — no answer, a 5xx, a 429 — is
// yielded as an error, and the watch goes on at the next look unless the loop
// stops. Any other error ends the watch. The end of ctx ends it without one.
// An incident resolved and raised again is a new incident, with a new ID, and
// is yielded again.
func (c *Client) WatchIncidents(ctx context.Context, projectID string, opts WatchOptions) iter.Seq2[Incident, error] {
	poll := opts.Poll
	if poll <= 0 {
		poll = 30 * time.Second
	}
	return func(yield func(Incident, error) bool) {
		if projectID == "" {
			yield(Incident{}, errors.New("metis: watch incidents: no project ID"))
			return
		}
		seen := make(map[string]struct{})
		for {
			open, err := c.openIncidents(ctx, projectID)
			switch {
			case ctx.Err() != nil:
				return
			case err != nil && !retryable(ctx, err):
				yield(Incident{}, err)
				return
			case err != nil:
				if !yield(Incident{}, err) {
					return
				}
			}
			// Forget what is no longer open, so the set stays the size of
			// the open incidents rather than of every one ever seen.
			current := make(map[string]struct{}, len(open))
			for _, incident := range open {
				current[incident.ID] = struct{}{}
				if _, old := seen[incident.ID]; old {
					continue
				}
				if !yield(incident, nil) {
					return
				}
			}
			if err == nil {
				seen = current
			} else {
				// A partial look says nothing about what closed.
				for id := range current {
					seen[id] = struct{}{}
				}
			}
			if sleep(ctx, poll) != nil {
				return
			}
		}
	}
}

// openIncidents reads every open incident of a project's instances that
// need attention.
func (c *Client) openIncidents(ctx context.Context, projectID string) ([]Incident, error) {
	var open []Incident
	for inst, err := range c.AllInstances(ctx, ListInstancesOptions{ProjectID: projectID, NeedsAttention: true}) {
		if err != nil {
			return open, err
		}
		incidents, err := c.ListIncidents(ctx, inst.ID)
		if IsNotFound(err) {
			continue // the instance went between the listing and the read
		}
		if err != nil {
			return open, err
		}
		for _, incident := range incidents {
			if incident.IsOpen() {
				open = append(open, incident)
			}
		}
	}
	return open, nil
}

// --- Retrying failed instances ----------------------------------------------

// ErrTooMany means an operation would act on more instances than the caller
// allowed. Nothing was done, unless the error says the operation was stopped.
var ErrTooMany = errors.New("metis: more instances match than allowed")

// RetryFailedOptions bounds [Client.RetryFailed].
type RetryFailedOptions struct {
	// Reason is kept beside whoever asked, and required.
	Reason string
	// Filter narrows which instances are retried. Its NeedsAttention is always
	// set: only instances with an open incident have anything to retry.
	Filter BatchFilter
	// MaxInstances is the most instances the retry may touch. Zero means 100.
	// It is the guard against retrying a whole project's backlog against a
	// dependency that is still down.
	MaxInstances int64
	// DryRun answers how many match and starts nothing.
	DryRun bool
}

// RetryFailed retries the failed step of every instance the filter matches
// that has an open incident — after an outage, say — as one batch operation,
// and never more of them than opts.MaxInstances.
//
//	preview, err := client.RetryFailed(ctx, projectID, metis.RetryFailedOptions{
//		Reason:       "the payment provider is back",
//		Filter:       metis.BatchFilter{DefinitionID: payments.ID},
//		MaxInstances: 500,
//	})
//	done, err := client.WaitForBatch(ctx, preview.Operation.ID, 0)
//
// It asks for a dry run first and starts nothing when more match than
// MaxInstances, answering [ErrTooMany] with the preview. Instances can fail
// between the dry run and the start; if the operation started takes more than
// MaxInstances after all, it is stopped at once and ErrTooMany returned with
// it — the instances it reached first have been retried. An operator's, and
// it needs what [Client.StartBatchOperation] needs.
func (c *Client) RetryFailed(ctx context.Context, projectID string, opts RetryFailedOptions) (*BatchPreview, error) {
	limit := opts.MaxInstances
	if limit <= 0 {
		limit = 100
	}
	req := BatchRequest{Kind: BatchRetry, Reason: opts.Reason, Filter: opts.Filter}
	req.Filter.NeedsAttention = true

	preview, err := c.StartBatchOperation(ctx, projectID, req)
	if err != nil {
		return nil, err
	}
	if preview.Matched > limit {
		return preview, fmt.Errorf("%w: %d match, at most %d allowed; nothing was retried", ErrTooMany, preview.Matched, limit)
	}
	if opts.DryRun || preview.Matched == 0 {
		return preview, nil
	}

	req.Apply = true
	started, err := c.StartBatchOperation(ctx, projectID, req)
	if err != nil {
		return nil, err
	}
	if op := started.Operation; op != nil && op.Total > limit {
		if _, stopErr := c.StopBatchOperation(ctx, op.ID, fmt.Sprintf("took %d instances, more than the %d allowed", op.Total, limit)); stopErr != nil {
			return started, fmt.Errorf("%w: operation %s took %d instances, and stopping it failed: %w", ErrTooMany, op.ID, op.Total, stopErr)
		}
		return started, fmt.Errorf("%w: operation %s took %d instances, at most %d allowed; it was stopped", ErrTooMany, op.ID, op.Total, limit)
	}
	return started, nil
}

// WaitForBatch reads a batch operation every poll — half a second when zero —
// until it has finished, completed or stopped, and returns it then. A read
// that fails in a way that may pass is tried again at the next poll. A context
// that ends first gives its error, with the operation as last read.
func (c *Client) WaitForBatch(ctx context.Context, operationID string, poll time.Duration) (*BatchOperation, error) {
	if operationID == "" {
		return nil, errors.New("metis: wait for batch: no operation ID")
	}
	var last *BatchOperation
	for {
		op, err := c.GetBatchOperation(ctx, operationID)
		switch {
		case err == nil:
			last = op
			if op.Status != BatchRunning {
				return op, nil
			}
		case ctx.Err() != nil:
		case !retryable(ctx, err):
			return last, err
		}
		if err := sleep(ctx, WaitOptions{Poll: poll}.poll()); err != nil {
			return last, fmt.Errorf("metis: stopped waiting for batch operation %s: %w", operationID, err)
		}
	}
}

// --- Starting many ----------------------------------------------------------

// StartRequest is one case for [Client.StartMany].
type StartRequest struct {
	// BusinessKey names the case. Set, the start is made with
	// [Client.StartProcessOnce], so a case already started is not started
	// again and the whole batch can be sent again after a failure.
	BusinessKey string
	Variables   Variables
}

// StartResult is what came of one [StartRequest], at the same position.
type StartResult struct {
	Instance *Instance
	// Started is false for a case that already had an instance.
	Started bool
	Err     error
}

// BulkOptions bounds [Client.StartMany].
type BulkOptions struct {
	// Concurrency is how many starts are in flight at once. Zero means 4,
	// and it is capped at 32 so one import cannot crowd out a server.
	Concurrency int
}

// StartMany starts one instance of a process per request, a few at a time,
// and returns a result per request in the same order — an import of open
// orders, say:
//
//	results := client.StartMany(ctx, projectID, "order", requests, metis.BulkOptions{})
//	for i, r := range results {
//		if r.Err != nil {
//			log.Printf("%s: %v", requests[i].BusinessKey, r.Err)
//		}
//	}
//
// A request with a business key starts through [Client.StartProcessOnce], so
// running the same import twice starts each case once. One without is a plain
// start and would be started again. A failure belongs to its own request and
// does not stop the others; the end of ctx fails the requests not yet started
// with the context's error.
func (c *Client) StartMany(ctx context.Context, projectID, definitionKey string, requests []StartRequest, opts BulkOptions) []StartResult {
	workers := opts.Concurrency
	if workers <= 0 {
		workers = 4
	}
	workers = min(workers, 32, max(len(requests), 1))

	results := make([]StartResult, len(requests))
	next := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range next {
				results[i] = c.startOne(ctx, projectID, definitionKey, requests[i])
			}
		})
	}
	for i := range requests {
		if ctx.Err() != nil {
			results[i] = StartResult{Err: ctx.Err()}
			continue
		}
		select {
		case next <- i:
		case <-ctx.Done():
			results[i] = StartResult{Err: ctx.Err()}
		}
	}
	close(next)
	wg.Wait()
	return results
}

func (c *Client) startOne(ctx context.Context, projectID, key string, req StartRequest) StartResult {
	if req.BusinessKey != "" {
		inst, started, err := c.StartProcessOnce(ctx, projectID, key, req.BusinessKey, req.Variables)
		return StartResult{Instance: inst, Started: started, Err: err}
	}
	id, err := c.StartProcess(ctx, projectID, key, req.Variables)
	if err != nil {
		return StartResult{Err: err}
	}
	return StartResult{Instance: &Instance{ID: id}, Started: true}
}
