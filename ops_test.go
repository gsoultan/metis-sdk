package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchIncidentsYieldsEachOnce(t *testing.T) {
	f, client := newFakeServer(t)
	replies(f, "GET /api/v1/instances",
		ok(`{"instances":[{"id":"i1"}],"page":{"has_more":false}}`),
		ok(`{"instances":[{"id":"i1"},{"id":"i2"}],"page":{"has_more":false}}`),
	)
	f.respond("GET /api/v1/incidents/i1", 200, `{"incidents":[{"id":"x1","status":"open"},{"id":"x0","status":"resolved"}]}`)
	f.respond("GET /api/v1/incidents/i2", 200, `{"incidents":[{"id":"x2","status":"open"}]}`)

	var got []string
	for incident, err := range client.WatchIncidents(t.Context(), "p1", WatchOptions{Poll: quickPoll}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, incident.ID)
		if len(got) == 2 {
			break
		}
	}
	if strings.Join(got, " ") != "x1 x2" {
		t.Errorf("incidents = %v, want x1 then x2, each once", got)
	}
	if q := f.snapshot()[0].Query; q.Get("needs_attention") != "true" || q.Get("project_id") != "p1" {
		t.Errorf("query = %v, want the project's instances needing attention", q)
	}
}

func TestWatchIncidentsYieldsARaisedAgainIncident(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances":[{"id":"i1"}],"page":{"has_more":false}}`)
	// x1 is open, then resolved, then open again under the same ID: the set
	// forgets what closed, so it comes back.
	replies(f, "GET /api/v1/incidents/i1",
		ok(`{"incidents":[{"id":"x1","status":"open"}]}`),
		ok(`{"incidents":[]}`),
		ok(`{"incidents":[{"id":"x1","status":"open"}]}`),
	)
	n := 0
	for _, err := range client.WatchIncidents(t.Context(), "p1", WatchOptions{Poll: quickPoll}) {
		if err != nil {
			t.Fatal(err)
		}
		if n++; n == 2 {
			break
		}
	}
}

func TestWatchIncidentsGoesOnAfterAPassingFailure(t *testing.T) {
	f, client := newFakeServer(t)
	replies(f, "GET /api/v1/instances",
		reply{503, `{"error":"busy"}`},
		ok(`{"instances":[{"id":"i1"}],"page":{"has_more":false}}`),
	)
	f.respond("GET /api/v1/incidents/i1", 200, `{"incidents":[{"id":"x1","status":"open"}]}`)

	var errs int
	for incident, err := range client.WatchIncidents(t.Context(), "p1", WatchOptions{Poll: quickPoll}) {
		if err != nil {
			errs++
			continue
		}
		if incident.ID == "x1" {
			break
		}
	}
	if errs != 1 {
		t.Errorf("errors = %d, want the 503 once", errs)
	}
}

func TestWatchIncidentsPartialLookDoesNotRepeat(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances":[{"id":"i1"},{"id":"i2"}],"page":{"has_more":false}}`)
	f.respond("GET /api/v1/incidents/i1", 200, `{"incidents":[{"id":"x1","status":"open"}]}`)
	replies(f, "GET /api/v1/incidents/i2",
		reply{502, `bad gateway`},
		ok(`{"incidents":[{"id":"x2","status":"open"}]}`),
	)
	var got []string
	for incident, err := range client.WatchIncidents(t.Context(), "p1", WatchOptions{Poll: quickPoll}) {
		if err != nil {
			continue
		}
		got = append(got, incident.ID)
		if incident.ID == "x2" {
			break
		}
	}
	if strings.Join(got, " ") != "x1 x2" {
		t.Errorf("incidents = %v, want x1 once and then x2", got)
	}
}

func TestWatchIncidentsEnds(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 403, `{"error":"not yours"}`)
	var errs []error
	for _, err := range client.WatchIncidents(t.Context(), "p1", WatchOptions{Poll: quickPoll}) {
		errs = append(errs, err)
	}
	if len(errs) != 1 || !IsUnauthorized(errs[0]) {
		t.Errorf("errors = %v, want the 403 once, ending the watch", errs)
	}

	for _, err := range client.WatchIncidents(t.Context(), "", WatchOptions{}) {
		if err == nil {
			t.Error("an empty project was watched")
		}
	}

	f2, client2 := newFakeServer(t)
	f2.respond("GET /api/v1/instances", 200, `{"instances":[{"id":"gone"}],"page":{"has_more":false}}`)
	f2.respond("GET /api/v1/incidents/gone", 404, `{"error":"no such instance"}`)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	for _, err := range client2.WatchIncidents(ctx, "p1", WatchOptions{Poll: quickPoll}) {
		t.Errorf("yielded %v; want the watch to skip the vanished instance and end quietly with ctx", err)
	}

	f3, client3 := newFakeServer(t)
	f3.respond("GET /api/v1/instances", 200, `{"instances":[{"id":"i1"}],"page":{"has_more":false}}`)
	f3.respond("GET /api/v1/incidents/i1", 400, `{"error":"bad id"}`)
	for _, err := range client3.WatchIncidents(t.Context(), "p1", WatchOptions{Poll: quickPoll}) {
		if !IsInvalid(err) {
			t.Errorf("err = %v, want the 400", err)
		}
	}
}

// batchServer answers the batch-operation start with preview for a dry run
// and applied otherwise, and records each request's dry_run.
func batchServer(f *fakeServer, preview, applied string) *[]bool {
	var mu sync.Mutex
	var dryRuns []bool
	f.mux.HandleFunc("POST /api/v1/projects/p1/batch-operations", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DryRun bool           `json:"dry_run"`
			Filter map[string]any `json:"filter"`
		}
		_ = decodeJSON(r, &body)
		mu.Lock()
		dryRuns = append(dryRuns, body.DryRun)
		mu.Unlock()
		if body.Filter["needs_attention"] != true {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"the test wants needs_attention set"}`))
			return
		}
		if body.DryRun {
			_, _ = w.Write([]byte(preview))
			return
		}
		_, _ = w.Write([]byte(applied))
	})
	return &dryRuns
}

func TestRetryFailedPreviewsThenApplies(t *testing.T) {
	f, client := newFakeServer(t)
	dryRuns := batchServer(f, `{"kind":"retry","matched":12}`, `{"kind":"retry","matched":12,"operation":{"id":"op1","total":12,"status":"running"}}`)

	got, err := client.RetryFailed(t.Context(), "p1", RetryFailedOptions{Reason: "provider back", Filter: BatchFilter{DefinitionID: "d1"}})
	if err != nil || got.Operation == nil || got.Operation.ID != "op1" {
		t.Fatalf("got = %+v, %v; want op1 started", got, err)
	}
	if fmt.Sprint(*dryRuns) != "[true false]" {
		t.Errorf("dry runs = %v, want a preview then the start", *dryRuns)
	}
	if body := f.last().Body; body["reason"] != "provider back" || body["kind"] != "retry" {
		t.Errorf("body = %v", body)
	}
}

func TestRetryFailedRefusesTooMany(t *testing.T) {
	f, client := newFakeServer(t)
	dryRuns := batchServer(f, `{"kind":"retry","matched":101}`, `{}`)
	got, err := client.RetryFailed(t.Context(), "p1", RetryFailedOptions{Reason: "r"})
	if !errors.Is(err, ErrTooMany) || got == nil || got.Matched != 101 {
		t.Errorf("got = %+v, err = %v; want the preview and ErrTooMany", got, err)
	}
	if len(*dryRuns) != 1 {
		t.Errorf("requests = %v, want only the preview", *dryRuns)
	}
}

func TestRetryFailedDryRunAndNothingToDo(t *testing.T) {
	f, client := newFakeServer(t)
	dryRuns := batchServer(f, `{"kind":"retry","matched":3}`, `{}`)
	if _, err := client.RetryFailed(t.Context(), "p1", RetryFailedOptions{Reason: "r", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	f2, client2 := newFakeServer(t)
	dryRuns2 := batchServer(f2, `{"kind":"retry","matched":0}`, `{}`)
	if _, err := client2.RetryFailed(t.Context(), "p1", RetryFailedOptions{Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	if len(*dryRuns) != 1 || len(*dryRuns2) != 1 {
		t.Errorf("requests = %v and %v, want only previews", *dryRuns, *dryRuns2)
	}
}

func TestRetryFailedStopsAnOperationThatGrew(t *testing.T) {
	f, client := newFakeServer(t)
	batchServer(f, `{"kind":"retry","matched":5}`, `{"kind":"retry","matched":9,"operation":{"id":"op1","total":9,"status":"running"}}`)
	f.respond("POST /api/v1/batch-operations/op1/stop", 200, `{"operation":{"id":"op1","status":"stopped"}}`)

	got, err := client.RetryFailed(t.Context(), "p1", RetryFailedOptions{Reason: "r", MaxInstances: 5})
	if !errors.Is(err, ErrTooMany) || got.Operation.ID != "op1" || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("got = %+v, err = %v; want op1 stopped and ErrTooMany", got, err)
	}
	if f.last().Path != "/api/v1/batch-operations/op1/stop" {
		t.Errorf("last request = %s, want the stop", f.last().Path)
	}

	f2, client2 := newFakeServer(t)
	batchServer(f2, `{"kind":"retry","matched":5}`, `{"kind":"retry","operation":{"id":"op1","total":9}}`)
	f2.respond("POST /api/v1/batch-operations/op1/stop", 500, `{"error":"down"}`)
	if _, err := client2.RetryFailed(t.Context(), "p1", RetryFailedOptions{Reason: "r", MaxInstances: 5}); !errors.Is(err, ErrTooMany) || !IsServerError(err) {
		t.Errorf("err = %v, want ErrTooMany and the stop's 500", err)
	}
}

func TestRetryFailedErrors(t *testing.T) {
	_, client := newFakeServer(t)
	if _, err := client.RetryFailed(t.Context(), "p1", RetryFailedOptions{}); err == nil {
		t.Error("a retry without a reason was accepted")
	}
	f, client := newFakeServer(t)
	replies(f, "POST /api/v1/projects/p1/batch-operations",
		ok(`{"kind":"retry","matched":2}`),
		reply{403, `{"error":"operators only"}`},
	)
	if _, err := client.RetryFailed(t.Context(), "p1", RetryFailedOptions{Reason: "r"}); !IsUnauthorized(err) {
		t.Errorf("err = %v, want 403", err)
	}
}

func TestWaitForBatch(t *testing.T) {
	f, client := newFakeServer(t)
	replies(f, "GET /api/v1/batch-operations/op1",
		ok(`{"operation":{"id":"op1","status":"running"}}`),
		reply{503, `busy`},
		ok(`{"operation":{"id":"op1","status":"completed","succeeded":3}}`),
	)
	op, err := client.WaitForBatch(t.Context(), "op1", quickPoll)
	if err != nil || op.Status != BatchCompleted || op.Succeeded != 3 {
		t.Fatalf("op = %+v, %v", op, err)
	}

	f.respond("GET /api/v1/batch-operations/gone", 404, `{"error":"gone"}`)
	if _, err := client.WaitForBatch(t.Context(), "gone", quickPoll); !IsNotFound(err) {
		t.Errorf("err = %v, want 404", err)
	}
	if _, err := client.WaitForBatch(t.Context(), "", 0); err == nil {
		t.Error("an empty ID was accepted")
	}

	f.respond("GET /api/v1/batch-operations/slow", 200, `{"operation":{"id":"slow","status":"running"}}`)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	op, err = client.WaitForBatch(ctx, "slow", quickPoll)
	if !errors.Is(err, context.DeadlineExceeded) || op == nil || op.Status != BatchRunning {
		t.Errorf("op = %v, err = %v; want the running operation and the deadline", op, err)
	}
}

func TestStartManyKeepsOrderAndBoundsConcurrency(t *testing.T) {
	f, client := newFakeServer(t)
	var inFlight, peak atomic.Int32
	f.mux.HandleFunc("POST /api/v1/process/start", func(w http.ResponseWriter, r *http.Request) {
		now := inFlight.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		_ = decodeJSON(r, &body)
		if body.Variables["n"] == float64(3) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"bad order"}`))
			return
		}
		fmt.Fprintf(w, `{"instance_id":"i%v"}`, body.Variables["n"])
	})

	var requests []StartRequest
	for n := range 8 {
		requests = append(requests, StartRequest{Variables: Variables{"n": n}})
	}
	results := client.StartMany(t.Context(), "p1", "order", requests, BulkOptions{Concurrency: 2})
	for n, r := range results {
		switch {
		case n == 3:
			if !IsInvalid(r.Err) {
				t.Errorf("result 3 = %+v, want its own 400", r)
			}
		case r.Err != nil || !r.Started || r.Instance.ID != fmt.Sprintf("i%d", n):
			t.Errorf("result %d = %+v, want i%d", n, r, n)
		}
	}
	if p := peak.Load(); p > 2 {
		t.Errorf("peak in flight = %d, want at most 2", p)
	}
}

func TestStartManyStartsACaseOnce(t *testing.T) {
	f, client := newFakeServer(t)
	f.mux.HandleFunc("GET /api/v1/instances", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("business_key") == "OLD" {
			_, _ = w.Write([]byte(`{"instances":[{"id":"i-old","business_key":"OLD","definition":{"id":"d-order-1"}}],"page":{"has_more":false}}`))
			return
		}
		_, _ = w.Write([]byte(`{"instances":[],"page":{"has_more":false}}`))
	})
	f.respond("GET /api/v1/definitions/versions", 200, orderVersions)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i-new"}`)

	results := client.StartMany(t.Context(), "p1", "order", []StartRequest{{BusinessKey: "OLD"}, {BusinessKey: "NEW"}}, BulkOptions{})
	if r := results[0]; r.Err != nil || r.Started || r.Instance.ID != "i-old" {
		t.Errorf("OLD = %+v, want the existing instance", r)
	}
	if r := results[1]; r.Err != nil || !r.Started || r.Instance.ID != "i-new" {
		t.Errorf("NEW = %+v, want a new instance", r)
	}
}

func TestStartManyAfterTheContextEnds(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i"}`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	results := client.StartMany(ctx, "p1", "order", make([]StartRequest, 3), BulkOptions{Concurrency: 100})
	for i, r := range results {
		if !errors.Is(r.Err, context.Canceled) {
			t.Errorf("result %d = %+v, want context.Canceled", i, r)
		}
	}
	if n := f.count(); n != 0 {
		t.Errorf("requests = %d, want none", n)
	}
	if got := client.StartMany(t.Context(), "p1", "order", nil, BulkOptions{}); len(got) != 0 {
		t.Errorf("results = %v, want none", got)
	}
}
