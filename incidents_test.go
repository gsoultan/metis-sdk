package metis

import (
	"testing"
	"time"
)

// ProcessFailed was a status the SDK could report and not explain.
func TestListIncidents(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/incidents/inst-1", 200, `{"incidents":[
		{"id":"inc-2","error":"connect: connection refused","status":"open",
		 "node":{"id":"charge"},"instance":{"id":"inst-1","status":"failed"},
		 "created_at":"2026-09-07T12:00:00Z"},
		{"id":"inc-1","error":"timeout","status":"resolved",
		 "node":{"id":"notify"},"created_at":"2026-09-06T12:00:00Z",
		 "resolved_at":"2026-09-06T13:00:00Z"}
	]}`)

	incidents, err := client.ListIncidents(t.Context(), "inst-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(incidents) != 2 {
		t.Fatalf("got %d incidents", len(incidents))
	}

	open := incidents[0]
	if !open.IsOpen() || open.Status != IncidentOpen {
		t.Errorf("status = %q", open.Status)
	}
	if open.NodeID() != "charge" {
		t.Errorf("node = %q", open.NodeID())
	}
	if open.Error != "connect: connection refused" {
		t.Errorf("error = %q", open.Error)
	}
	if open.ResolvedAt != nil {
		t.Error("an open incident has a resolution time")
	}
	if open.Instance == nil || open.Instance.Status != ProcessFailed {
		t.Errorf("instance = %+v", open.Instance)
	}

	// Resolved ones come back too, so an instance that recovered keeps its
	// history; IsOpen is what filters to what still blocks.
	done := incidents[1]
	if done.IsOpen() {
		t.Error("a resolved incident reported itself open")
	}
	if done.ResolvedAt == nil || !done.ResolvedAt.After(done.CreatedAt) {
		t.Errorf("resolved_at = %v", done.ResolvedAt)
	}

	if got := f.last().Path; got != "/api/v1/incidents/inst-1" {
		t.Errorf("path = %q", got)
	}
}

func TestIncidentAccessorsOnSparseResponses(t *testing.T) {
	var incident Incident
	if got := incident.NodeID(); got != "" {
		t.Errorf("NodeID = %q", got)
	}
	if incident.IsOpen() {
		t.Error("a zero incident reported itself open")
	}
}

func TestResolveIncident(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/incidents/inc-1/resolve", 200, `{}`)

	if err := client.ResolveIncident(t.Context(), "inc-1"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	last := f.last()
	if last.Method != "POST" || last.Path != "/api/v1/incidents/inc-1/resolve" {
		t.Errorf("sent %s %s", last.Method, last.Path)
	}
}

// The timestamps are what an operator sorts by, so they have to decode as real
// times rather than strings.
func TestIncidentTimesDecode(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/incidents/inst-1", 200, `{"incidents":[
		{"id":"inc-1","error":"boom","status":"resolved",
		 "created_at":"2026-09-07T12:00:00Z","resolved_at":"2026-09-07T12:30:00Z"}
	]}`)

	incidents, err := client.ListIncidents(t.Context(), "inst-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	if !incidents[0].CreatedAt.Equal(want) {
		t.Errorf("created = %v", incidents[0].CreatedAt)
	}
	if got := incidents[0].ResolvedAt.Sub(incidents[0].CreatedAt); got != 30*time.Minute {
		t.Errorf("resolution took %v", got)
	}
}
