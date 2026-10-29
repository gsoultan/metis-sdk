package metis

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// failFirst answers status to the first n requests on pattern, then ok.
func failFirst(f *fakeServer, pattern string, n int32, status int, header, ok string) {
	var seen atomic.Int32
	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		if seen.Add(1) <= n {
			if header != "" {
				w.Header().Set("Retry-After", header)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"busy"}`))
			return
		}
		_, _ = w.Write([]byte(ok))
	})
}

var quickRetry = RetryPolicy{Attempts: 3, Base: time.Millisecond, Max: 5 * time.Millisecond}

func TestRetryReadsAgainAfterAPassingFailure(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504} {
		f, client := newFakeServer(t)
		WithRetry(quickRetry)(client)
		failFirst(f, "GET /api/v1/tasks/t1", 2, status, "", `{"task":{"id":"t1"}}`)

		task, err := client.GetTask(t.Context(), "t1")
		if err != nil || task.ID != "t1" {
			t.Errorf("%d: task = %v, %v; want t1 on the third try", status, task, err)
		}
		if n := f.count(); n != 3 {
			t.Errorf("%d: requests = %d, want 3", status, n)
		}
	}
}

func TestRetryGivesUpAfterTheAttempts(t *testing.T) {
	f, client := newFakeServer(t)
	WithRetry(quickRetry)(client)
	failFirst(f, "GET /api/v1/tasks/t1", 10, 503, "", `{}`)

	_, err := client.GetTask(t.Context(), "t1")
	if !IsServerError(err) {
		t.Errorf("err = %v, want the last 503", err)
	}
	if n := f.count(); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
}

func TestRetryLeavesWritesAndMistakesAlone(t *testing.T) {
	f, client := newFakeServer(t)
	WithRetry(quickRetry)(client)
	failFirst(f, "POST /api/v1/process/start", 1, 503, "", `{"id":"i1"}`)
	failFirst(f, "GET /api/v1/tasks/t1", 1, 400, "", `{}`)

	if _, err := client.StartProcess(t.Context(), "p1", "order", nil); !IsServerError(err) {
		t.Errorf("start: err = %v, want the 503 as it came", err)
	}
	if _, err := client.GetTask(t.Context(), "t1"); !IsInvalid(err) {
		t.Errorf("get: err = %v, want the 400", err)
	}
	if n := f.count(); n != 2 {
		t.Errorf("requests = %d, want 2: neither is sent again", n)
	}
}

func TestNoRetryByDefault(t *testing.T) {
	f, client := newFakeServer(t)
	failFirst(f, "GET /api/v1/tasks/t1", 1, 503, "", `{"task":{"id":"t1"}}`)
	if _, err := client.GetTask(t.Context(), "t1"); !IsServerError(err) {
		t.Errorf("err = %v, want the 503", err)
	}
	if n := f.count(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestRetryHonoursRetryAfterUpToMax(t *testing.T) {
	f, client := newFakeServer(t)
	WithRetry(quickRetry)(client)
	failFirst(f, "GET /api/v1/tasks/t1", 1, 429, "120", `{"task":{"id":"t1"}}`)

	start := time.Now()
	if _, err := client.GetTask(t.Context(), "t1"); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("waited %v, want Max (5ms) to cap the server's 120s", waited)
	}
}

func TestRetryReportsRetryAfterOnTheError(t *testing.T) {
	f, client := newFakeServer(t)
	failFirst(f, "GET /api/v1/tasks/t1", 1, 503, "7", `{}`)
	_, err := client.GetTask(t.Context(), "t1")
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.RetryAfter != 7*time.Second {
		t.Errorf("err = %#v, want RetryAfter 7s", err)
	}
}

func TestRetryAfterANetworkFailure(t *testing.T) {
	var seen atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if seen.Add(1) == 1 {
			// Drop the connection without an answer.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`{"task":{"id":"t1"}}`))
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.URL, WithRetry(quickRetry))

	task, err := client.GetTask(t.Context(), "t1")
	if err != nil || task.ID != "t1" {
		t.Errorf("task = %v, %v; want t1 after the dropped connection", task, err)
	}
	if n := seen.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestRetryStopsWhenTheContextEnds(t *testing.T) {
	f, client := newFakeServer(t)
	WithRetry(RetryPolicy{Attempts: 5, Base: time.Hour})(client)
	failFirst(f, "GET /api/v1/tasks/t1", 10, 503, "", `{}`)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := client.GetTask(ctx, "t1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline, not an hour's wait", err)
	}
	if n := f.count(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestRetryDelay(t *testing.T) {
	p := RetryPolicy{Base: 100 * time.Millisecond, Max: time.Second}
	for attempt, want := range map[int]time.Duration{1: 100 * time.Millisecond, 2: 200 * time.Millisecond, 4: 800 * time.Millisecond, 9: time.Second, 200: time.Second} {
		got := p.delay(attempt, nil)
		if got > want || got < want/2 {
			t.Errorf("delay(%d) = %v, want within [%v, %v]", attempt, got, want/2, want)
		}
	}
	if got := (RetryPolicy{}).delay(1, nil); got > 200*time.Millisecond || got < 100*time.Millisecond {
		t.Errorf("default delay = %v, want about 200ms", got)
	}
	asked := &APIError{StatusCode: 429, RetryAfter: 3 * time.Second}
	if got := (RetryPolicy{}).delay(1, asked); got != 3*time.Second {
		t.Errorf("delay with Retry-After = %v, want 3s", got)
	}
	if got := (RetryPolicy{}).delay(1, &APIError{RetryAfter: time.Hour}); got != 30*time.Second {
		t.Errorf("delay = %v, want the default 30s cap", got)
	}
}

func TestRetryAfterHeader(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for header, want := range map[string]time.Duration{
		"":                              0,
		"5":                             5 * time.Second,
		"0":                             0,
		"-3":                            0,
		"99999999999":                   24 * time.Hour,
		"soon":                          0,
		"Wed, 28 Oct 2026 12:00:30 GMT": 30 * time.Second,
		"Wed, 28 Oct 2026 11:00:00 GMT": 0,
	} {
		if got := retryAfter(header, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", header, got, want)
		}
	}
}
