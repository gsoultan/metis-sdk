package metis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
