package metis

import (
	"testing"
	"time"
)

func TestListExternalTaskWorkers(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/external-task-workers", 200, `{
		"live_window_seconds": 120,
		"topics": [
			{"topic": "charge", "waiting": 3, "locked": 1, "unserved": false,
			 "workers": [{"worker_id": "w-1", "version": "metis-sdk/v0.3.0",
			              "last_seen_at": "2026-10-22T09:00:00Z", "live": true, "locked": 1}]},
			{"topic": "refund", "waiting": 2, "locked": 0, "unserved": true, "workers": []}
		]}`)

	topics, err := client.ListExternalTaskWorkers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if topics.LiveWindow != 2*time.Minute {
		t.Errorf("LiveWindow = %v", topics.LiveWindow)
	}
	if len(topics.Topics) != 2 || !topics.Topics[1].Unserved {
		t.Fatalf("topics = %+v", topics.Topics)
	}
	worker := topics.Topics[0].Workers[0]
	if worker.WorkerID != "w-1" || worker.Version != "metis-sdk/v0.3.0" || !worker.Live || worker.LastSeenAt == nil {
		t.Errorf("worker = %+v", worker)
	}
}

func TestListOperationsSendsTheFilter(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/audit/operations", 200, `{
		"operations": [{"id": "a-1", "at": "2026-10-20T10:00:00Z", "actor": "boss",
		                "type": "instance_cancelled", "instance_id": "i-1", "narrative": "boss cancelled it"}],
		"next": "cursor-2"}`)

	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	page, err := client.ListOperations(t.Context(), ListOperationsOptions{
		Actor: "boss", Type: "instance_cancelled", ProjectID: "p-1",
		From: from, To: from.AddDate(0, 1, 0), Before: "cursor-1", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Next != "cursor-2" || len(page.Operations) != 1 || page.Operations[0].Narrative != "boss cancelled it" {
		t.Errorf("page = %+v", page)
	}
	query := f.last().Query
	for name, want := range map[string]string{
		"actor": "boss", "type": "instance_cancelled", "project": "p-1",
		"from": "2026-10-01T00:00:00Z", "to": "2026-11-01T00:00:00Z", "before": "cursor-1", "limit": "20",
	} {
		if got := query.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestListOperationsWithNoFilterSendsNoQuery(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/audit/operations", 200, `{"operations": []}`)

	if _, err := client.ListOperations(t.Context(), ListOperationsOptions{}); err != nil {
		t.Fatal(err)
	}
	if query := f.last().Query; len(query) != 0 {
		t.Errorf("query = %v", query)
	}
}

func TestARetentionHold(t *testing.T) {
	f, client := newFakeServer(t)
	hold := `{"id":"h-1","project_id":"p-1","instance_id":"i-1","reason":"litigation","placed_by":"boss","placed_at":"2026-10-20T10:00:00Z"}`
	f.respond("GET /api/v1/instances/i-1/retention-hold", 200, `{"held":true,"hold":`+hold+`}`)
	f.respond("GET /api/v1/instances/i-2/retention-hold", 200, `{"held":false}`)
	f.respond("POST /api/v1/instances/i-1/retention-hold", 200, `{"hold":`+hold+`}`)
	f.respond("POST /api/v1/instances/i-1/retention-hold/release", 200, `{"hold":`+hold+`}`)
	f.respond("GET /api/v1/projects/p-1/retention-holds", 200, `{"holds":[`+hold+`]}`)

	got, err := client.GetRetentionHold(t.Context(), "i-1")
	if err != nil || got == nil || got.Reason != "litigation" {
		t.Fatalf("GetRetentionHold = %+v, %v", got, err)
	}
	if got, err := client.GetRetentionHold(t.Context(), "i-2"); err != nil || got != nil {
		t.Errorf("an instance with no hold = %+v, %v", got, err)
	}

	if _, err := client.PlaceRetentionHold(t.Context(), "i-1", "litigation"); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Body["reason"]; got != "litigation" {
		t.Errorf("reason = %#v", got)
	}
	if _, err := client.ReleaseRetentionHold(t.Context(), "i-1", "settled"); err != nil {
		t.Fatal(err)
	}
	if last := f.last(); last.Path != "/api/v1/instances/i-1/retention-hold/release" || last.Body["reason"] != "settled" {
		t.Errorf("sent %s %#v", last.Path, last.Body)
	}
	holds, err := client.ListRetentionHolds(t.Context(), "p-1")
	if err != nil || len(holds) != 1 {
		t.Errorf("ListRetentionHolds = %+v, %v", holds, err)
	}

	before := f.count()
	if _, err := client.PlaceRetentionHold(t.Context(), "i-1", ""); err == nil {
		t.Error("a hold was placed with no reason")
	}
	if f.count() != before {
		t.Error("a hold with no reason reached the server")
	}
}

func TestARetentionPeriod(t *testing.T) {
	f, client := newFakeServer(t)
	period := `{"id":"r-1","project_id":"p-1","process_key":"purchase","days":365,"reason":"audit","set_by":"boss","set_at":"2026-10-20T10:00:00Z"}`
	f.respond("PUT /api/v1/projects/p-1/retention-periods/purchase", 200, `{"period":`+period+`}`)
	f.respond("GET /api/v1/projects/p-1/retention-periods", 200, `{"periods":[`+period+`]}`)
	f.respond("GET /api/v1/projects/p-1/retention-periods/purchase/history", 200, `{"periods":[`+period+`]}`)

	days := 365
	got, err := client.SetRetentionPeriod(t.Context(), "p-1", "purchase", &days, "audit")
	if err != nil || got.Days == nil || *got.Days != 365 {
		t.Fatalf("SetRetentionPeriod = %+v, %v", got, err)
	}
	if body := f.last().Body; body["days"] != float64(365) || body["reason"] != "audit" {
		t.Errorf("body = %#v", body)
	}

	// Clearing a period is days: null, said out loud. Leaving it out is
	// refused by the server, so that nothing clears a period by accident.
	if _, err := client.SetRetentionPeriod(t.Context(), "p-1", "purchase", nil, "back to the default"); err != nil {
		t.Fatal(err)
	}
	body := f.last().Body
	if days, sent := body["days"]; !sent || days != nil {
		t.Errorf("days = %#v (sent %v), want null", days, sent)
	}

	if _, err := client.SetRetentionPeriod(t.Context(), "p-1", "purchase", &days, ""); err == nil {
		t.Error("a period was set with no reason")
	}
	if periods, err := client.ListRetentionPeriods(t.Context(), "p-1"); err != nil || len(periods) != 1 {
		t.Errorf("ListRetentionPeriods = %+v, %v", periods, err)
	}
	if periods, err := client.RetentionPeriodHistory(t.Context(), "p-1", "purchase"); err != nil || len(periods) != 1 {
		t.Errorf("RetentionPeriodHistory = %+v, %v", periods, err)
	}
}
