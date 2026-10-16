package metis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// workerHarness is a fake engine for the worker loop: it serves one batch of
// tasks, then empties, and records every report-back.
type workerHarness struct {
	mu        sync.Mutex
	toServe   []ExternalTask
	completed []Variables
	failed    []map[string]any
	done      chan struct{}
	doneOnce  sync.Once

	// extendStatus is how extend-lock is answered: 0 is a server without the
	// route (404), 200 an extension, anything else a refusal.
	extendStatus int
	extends      []map[string]any
}

func newWorkerHarness(t *testing.T, tasks ...ExternalTask) (*workerHarness, *Client) {
	t.Helper()
	h := &workerHarness{toServe: tasks, done: make(chan struct{})}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/external-tasks/fetch-and-lock", func(w http.ResponseWriter, _ *http.Request) {
		h.mu.Lock()
		batch := h.toServe
		h.toServe = nil
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": batch})
	})
	mux.HandleFunc("POST /api/v1/external-tasks/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables Variables `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.completed = append(h.completed, body.Variables)
		h.mu.Unlock()
		h.doneOnce.Do(func() { close(h.done) })
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /api/v1/external-tasks/{id}/failure", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.failed = append(h.failed, body)
		h.mu.Unlock()
		h.doneOnce.Do(func() { close(h.done) })
		_, _ = w.Write([]byte(`{}`))
	})

	mux.HandleFunc("POST /api/v1/external-tasks/{id}/extend-lock", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		status := h.extendStatus
		if status != 0 {
			h.extends = append(h.extends, body)
		}
		h.mu.Unlock()
		switch status {
		case 0:
			http.NotFound(w, r)
		case http.StatusOK:
			_ = json.NewEncoder(w).Encode(map[string]any{"lock_expiration": time.Now().Add(time.Minute)})
		default:
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"worker \"w-1\" does not hold the lock on this task"}`))
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return h, NewClient(server.URL, WithToken("t"))
}

// runUntilReported runs the worker until the harness sees a report-back, then
// shuts it down and asserts Run returned the shutdown, not a failure.
func runUntilReported(t *testing.T, w *Worker, h *workerHarness) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() { finished <- w.Run(ctx) }()

	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker never reported back")
	}
	cancel()

	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled on shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop on cancel")
	}
}

func TestWorkerCompletesATask(t *testing.T) {
	h, client := newWorkerHarness(t, ExternalTask{ID: "et-1", Topic: "charge", Retries: 3, Variables: Variables{"amount": 42.0}})

	worker := NewWorker(client, "charge", "w-1", WorkerOptions{PollInterval: 20 * time.Millisecond},
		func(_ context.Context, task *ExternalTask) (Variables, error) {
			return Variables{"charged": task.Variables["amount"]}, nil
		})
	runUntilReported(t, worker, h)

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.completed) != 1 || h.completed[0]["charged"] != 42.0 {
		t.Fatalf("completed = %+v, want the handler's variables written back", h.completed)
	}
}

func TestWorkerFailsATaskAndDecrementsRetries(t *testing.T) {
	h, client := newWorkerHarness(t, ExternalTask{ID: "et-1", Topic: "charge", Retries: 3})

	worker := NewWorker(client, "charge", "w-1", WorkerOptions{PollInterval: 20 * time.Millisecond},
		func(context.Context, *ExternalTask) (Variables, error) {
			return nil, errors.New("card declined")
		})
	runUntilReported(t, worker, h)

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.failed) != 1 {
		t.Fatalf("failed = %+v, want one failure report", h.failed)
	}
	if h.failed[0]["error_message"] != "card declined" {
		t.Errorf("error_message = %v", h.failed[0]["error_message"])
	}
	if h.failed[0]["retries"] != float64(2) {
		t.Errorf("retries = %v, want 2 — one attempt spent", h.failed[0]["retries"])
	}
}

// TestWorkerSurvivesAPanickingHandler is the poison-task case: one bad task
// must not kill the worker for every other task on the topic.
func TestWorkerSurvivesAPanickingHandler(t *testing.T) {
	h, client := newWorkerHarness(t,
		ExternalTask{ID: "poison", Topic: "charge", Retries: 1},
		ExternalTask{ID: "fine", Topic: "charge", Retries: 3},
	)

	worker := NewWorker(client, "charge", "w-1", WorkerOptions{PollInterval: 20 * time.Millisecond},
		func(_ context.Context, task *ExternalTask) (Variables, error) {
			if task.ID == "poison" {
				panic("boom")
			}
			return Variables{"ok": true}, nil
		})

	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() { finished <- worker.Run(ctx) }()

	// Wait until both tasks are accounted for: one failure, one completion.
	deadline := time.After(10 * time.Second)
	for {
		h.mu.Lock()
		settled := len(h.failed) == 1 && len(h.completed) == 1
		h.mu.Unlock()
		if settled {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the panic took the healthy task down with it")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-finished
}

func TestWorkerKeepsPollingThroughTransportErrors(t *testing.T) {
	// A server that always refuses.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"down for maintenance"}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	var reported int
	var mu sync.Mutex
	worker := NewWorker(NewClient(server.URL), "t", "w", WorkerOptions{PollInterval: 5 * time.Millisecond},
		func(context.Context, *ExternalTask) (Variables, error) { return nil, nil })
	worker.OnError = func(error) {
		mu.Lock()
		reported++
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := worker.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want to keep polling until the context ends", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if reported < 2 {
		t.Fatalf("OnError saw %d errors; the loop should keep trying and keep telling us", reported)
	}
}

// TestWorkerGivesLaterTasksOnlyTheLockThatIsLeft is the batch case: the tasks
// of one fetch share one lock, and run in turn. A later task's handler must not
// be handed a fresh LockDuration, and a task whose lock has already run out
// must not be started at all — another worker may be running it.
func TestWorkerGivesLaterTasksOnlyTheLockThatIsLeft(t *testing.T) {
	h, client := newWorkerHarness(t,
		ExternalTask{ID: "slow", Topic: "charge", Retries: 3},
		ExternalTask{ID: "late", Topic: "charge", Retries: 3},
	)

	const lock = 200 * time.Millisecond
	var mu sync.Mutex
	var started []string
	var reported []error
	worker := NewWorker(client, "charge", "w-1", WorkerOptions{LockDuration: lock, PollInterval: 20 * time.Millisecond},
		func(ctx context.Context, task *ExternalTask) (Variables, error) {
			mu.Lock()
			started = append(started, task.ID)
			mu.Unlock()
			// Overruns the lock; the deadline is what stops it.
			<-ctx.Done()
			return nil, ctx.Err()
		})
	worker.OnError = func(err error) {
		mu.Lock()
		reported = append(reported, err)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() { finished <- worker.Run(ctx) }()

	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the overrunning handler was never stopped")
	}
	// Long enough for the second task to have been started, had it been.
	time.Sleep(2 * lock)
	cancel()
	<-finished

	mu.Lock()
	defer mu.Unlock()
	if len(started) != 1 || started[0] != "slow" {
		t.Fatalf("handlers started for %v; the second task's lock had run out, so only %q should have run", started, "slow")
	}
	if len(reported) == 0 {
		t.Fatal("the skipped task was not reported through OnError")
	}
}

// TestWorkerReportsAfterShutdown is the deploy case: a handler that finishes as
// the worker is told to stop has done its work, and the engine must hear so.
// Reporting on the cancelled context failed every time, and the side effect ran
// again wherever the task went next.
func TestWorkerReportsAfterShutdown(t *testing.T) {
	h, client := newWorkerHarness(t, ExternalTask{ID: "et-1", Topic: "charge", Retries: 3})

	ctx, cancel := context.WithCancel(t.Context())
	worker := NewWorker(client, "charge", "w-1", WorkerOptions{PollInterval: 20 * time.Millisecond},
		func(context.Context, *ExternalTask) (Variables, error) {
			cancel() // the stop arrives while the work is finishing
			return Variables{"charged": true}, nil
		})

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.completed) != 1 {
		t.Fatalf("completed = %+v; finished work was lost because shutdown cancelled its report", h.completed)
	}
}

// TestWorkerDoesNotSpendARetryOnShutdown: a handler that returns because the
// worker is stopping has not failed. Reporting it would cost a retry, and on
// the last one raise an incident, for a deploy.
func TestWorkerDoesNotSpendARetryOnShutdown(t *testing.T) {
	h, client := newWorkerHarness(t,
		ExternalTask{ID: "et-1", Topic: "charge", Retries: 1},
		ExternalTask{ID: "et-2", Topic: "charge", Retries: 1},
	)

	ctx, cancel := context.WithCancel(t.Context())
	var calls int
	worker := NewWorker(client, "charge", "w-1", WorkerOptions{PollInterval: 20 * time.Millisecond},
		func(handlerCtx context.Context, _ *ExternalTask) (Variables, error) {
			calls++
			cancel()
			<-handlerCtx.Done()
			return nil, handlerCtx.Err()
		})

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.failed) != 0 {
		t.Fatalf("failed = %+v; a shutdown was reported as the task failing", h.failed)
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times; the task after the stop should not have been started", calls)
	}
}

// With ExtendLock a handler may run well past LockDuration, because the lock
// is extended while it works, and its work is reported as done.
func TestWorkerExtendsTheLockWhileTheHandlerRuns(t *testing.T) {
	h, client := newWorkerHarness(t, ExternalTask{ID: "et-1", Topic: "charge", Retries: 3})
	h.extendStatus = http.StatusOK

	const lock = 200 * time.Millisecond
	worker := NewWorker(client, "charge", "w-1",
		WorkerOptions{PollInterval: 20 * time.Millisecond, LockDuration: lock, MaxTasks: 1, ExtendLock: true},
		func(ctx context.Context, _ *ExternalTask) (Variables, error) {
			select {
			case <-time.After(3 * lock):
				return Variables{"charged": true}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	runUntilReported(t, worker, h)

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.completed) != 1 {
		t.Fatalf("completed = %+v, failed = %+v: the handler was cut off at the first lock", h.completed, h.failed)
	}
	if len(h.extends) < 2 {
		t.Fatalf("extended %d times over three lock lengths", len(h.extends))
	}
	if h.extends[0]["worker_id"] != "w-1" || h.extends[0]["lock_duration_ms"] != float64(lock.Milliseconds()) {
		t.Errorf("extension sent %+v", h.extends[0])
	}
}

// A lock the server says is no longer ours stops the handler at once: another
// worker may be running the task already.
func TestWorkerStopsAHandlerWhoseLockWasLost(t *testing.T) {
	h, client := newWorkerHarness(t, ExternalTask{ID: "et-1", Topic: "charge", Retries: 3})
	h.extendStatus = http.StatusBadRequest

	const lock = 400 * time.Millisecond
	stopped := make(chan time.Duration, 1)
	var errs []error
	var errsMu sync.Mutex
	worker := NewWorker(client, "charge", "w-1",
		WorkerOptions{PollInterval: 20 * time.Millisecond, LockDuration: lock, MaxTasks: 1, ExtendLock: true},
		func(ctx context.Context, _ *ExternalTask) (Variables, error) {
			start := time.Now()
			<-ctx.Done()
			stopped <- time.Since(start)
			return nil, ctx.Err()
		})
	worker.OnError = func(err error) {
		errsMu.Lock()
		errs = append(errs, err)
		errsMu.Unlock()
	}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() { finished <- worker.Run(ctx) }()

	select {
	case took := <-stopped:
		// The first extension is asked for halfway through the lock; the
		// handler stops on its refusal rather than at the lock's end.
		if took >= lock*9/10 {
			t.Errorf("handler stopped after %v, want soon after the refusal at about %v", took, lock/2)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the handler was never stopped")
	}
	// Long enough for a failure report to have gone out, had there been one.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-finished
	errsMu.Lock()
	defer errsMu.Unlock()
	if len(errs) == 0 {
		t.Error("the lost lock was not reported through OnError")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.failed) != 0 {
		t.Errorf("a task another worker holds was failed: %+v", h.failed)
	}
}

// Against a server older than v0.5.0 the extension is a 404. The worker says
// so once and keeps to the lock it fetched, as it would without ExtendLock.
func TestWorkerWithoutExtendLockOnTheServerKeepsToItsLock(t *testing.T) {
	_, client := newWorkerHarness(t, ExternalTask{ID: "et-1", Topic: "charge", Retries: 3})

	const lock = 400 * time.Millisecond
	var reports []error
	var mu sync.Mutex
	var cut []string
	worker := NewWorker(client, "charge", "w-1",
		WorkerOptions{PollInterval: 20 * time.Millisecond, LockDuration: lock, MaxTasks: 1, ExtendLock: true},
		func(ctx context.Context, task *ExternalTask) (Variables, error) {
			select {
			case <-time.After(2 * lock):
				return Variables{"ok": true}, nil
			case <-ctx.Done():
				mu.Lock()
				cut = append(cut, task.ID)
				mu.Unlock()
				return nil, ctx.Err()
			}
		})
	worker.OnError = func(err error) {
		mu.Lock()
		reports = append(reports, err)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() { finished <- worker.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		n := len(cut)
		mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the handler ran on past its lock")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-finished

	mu.Lock()
	defer mu.Unlock()
	unsupported := 0
	for _, err := range reports {
		if strings.Contains(err.Error(), "cannot extend locks") {
			unsupported++
		}
	}
	if unsupported != 1 {
		t.Errorf("the missing route was reported %d times, want once: %v", unsupported, reports)
	}
	// And later tasks are not asked for at all.
	if !worker.cannotExtend.Load() {
		t.Error("the worker will ask again for every later task")
	}
}

func TestExtendExternalTaskLock(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/external-tasks/et-1/extend-lock", 200,
		`{"lock_expiration":"2026-10-16T10:00:00Z"}`)

	until, err := client.ExtendExternalTaskLock(t.Context(), "et-1", "w-1", 90*time.Second)
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if !until.Equal(time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("until = %v", until)
	}
	body := f.last().Body
	if body["worker_id"] != "w-1" || body["lock_duration_ms"] != float64(90000) {
		t.Errorf("body = %+v", body)
	}

	if _, err := client.ExtendExternalTaskLock(t.Context(), "et-1", "w-1", 0); err == nil {
		t.Error("a zero duration was accepted")
	}
}
