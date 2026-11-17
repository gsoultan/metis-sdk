package metis

import (
	"testing"
	"time"
)

func TestStartSchedule(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/definitions/start-schedule", 200, `{"schedule": {"process_key": "nightly-report",
		"scheduled": true, "paused": false, "next_run_at": "2026-11-18T02:00:00Z", "failures": 2, "last_error": "no such group"}}`)
	f.respond("POST /api/v1/definitions/start-schedule/pause", 200, `{"schedule": {"process_key": "nightly-report",
		"scheduled": true, "paused": true, "paused_at": "2026-11-17T09:30:00Z", "failures": 0}}`)
	f.respond("POST /api/v1/definitions/start-schedule/resume", 200, `{"schedule": {"process_key": "nightly-report",
		"scheduled": true, "paused": false, "next_run_at": "2026-11-18T02:00:00Z", "failures": 0}}`)
	ctx := t.Context()
	next := time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC)

	status, err := client.GetStartSchedule(ctx, "p-1", "nightly-report")
	if err != nil {
		t.Fatalf("GetStartSchedule: %v", err)
	}
	if !status.Scheduled || status.Paused || status.NextRunAt == nil || !status.NextRunAt.Equal(next) {
		t.Errorf("status = %+v", status)
	}
	if status.Failures != 2 || status.LastError != "no such group" {
		t.Errorf("failure = %d, %q", status.Failures, status.LastError)
	}
	if q := f.last().Query; q.Get("project_id") != "p-1" || q.Get("key") != "nightly-report" {
		t.Errorf("query = %v", q)
	}

	paused, err := client.Project("p-1").PauseStartSchedule(ctx, "nightly-report")
	if err != nil {
		t.Fatalf("PauseStartSchedule: %v", err)
	}
	if !paused.Paused || paused.PausedAt == nil || paused.NextRunAt != nil {
		t.Errorf("paused = %+v", paused)
	}
	if req := f.last(); req.Path != "/api/v1/definitions/start-schedule/pause" ||
		req.Body["project_id"] != "p-1" || req.Body["key"] != "nightly-report" {
		t.Errorf("pause sent %s %v", req.Path, req.Body)
	}

	resumed, err := client.Project("p-1").ResumeStartSchedule(ctx, "nightly-report")
	if err != nil {
		t.Fatalf("ResumeStartSchedule: %v", err)
	}
	if resumed.Paused || resumed.NextRunAt == nil || !resumed.NextRunAt.Equal(next) {
		t.Errorf("resumed = %+v", resumed)
	}
	if req := f.last(); req.Path != "/api/v1/definitions/start-schedule/resume" || req.Body["key"] != "nightly-report" {
		t.Errorf("resume sent %s %v", req.Path, req.Body)
	}

	if got, err := client.Project("p-1").StartSchedule(ctx, "nightly-report"); err != nil || !got.Scheduled {
		t.Errorf("Project.StartSchedule = %+v, %v", got, err)
	}
}

func TestStartScheduleNeedsAProcess(t *testing.T) {
	f, client := newFakeServer(t)
	ctx := t.Context()
	if _, err := client.GetStartSchedule(ctx, "", "nightly-report"); err == nil {
		t.Error("GetStartSchedule took no project")
	}
	if _, err := client.PauseStartSchedule(ctx, "p-1", ""); err == nil {
		t.Error("PauseStartSchedule took no process key")
	}
	if _, err := client.ResumeStartSchedule(ctx, "", ""); err == nil {
		t.Error("ResumeStartSchedule took no process")
	}
	if f.count() != 0 {
		t.Error("a request was sent")
	}
}
