package metis

import (
	"testing"
	"time"
)

func TestEvaluateDecision(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/decisions/evaluate", 200, `{"result": {
		"values": {"limit": 5000, "tier": "gold"},
		"matched_rules": [2], "matched_rule_ids": ["r-gold"],
		"decision_key": "credit-limit", "decision_name": "Credit limit", "decision_version": 4}}`)

	result, err := client.EvaluateDecision(t.Context(), "p-1", "credit-limit", Variables{"score": 712}, WithDecisionVersion(4))
	if err != nil {
		t.Fatal(err)
	}
	if limit, _ := result.Values.Int("limit"); limit != 5000 {
		t.Errorf("limit = %d", limit)
	}
	if !result.Matched() || result.MatchedRuleIDs[0] != "r-gold" || result.DecisionVersion != 4 {
		t.Errorf("result = %+v", result)
	}
	body := f.last().Body
	if body["project_id"] != "p-1" || body["key"] != "credit-limit" || body["version"] != float64(4) {
		t.Errorf("body = %v", body)
	}
	if vars, _ := body["variables"].(map[string]any); vars["score"] != float64(712) {
		t.Errorf("variables = %v", body["variables"])
	}
}

func TestEvaluateDecisionSendsNoVersionUnlessAsked(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/decisions/evaluate", 200, `{"result": {"values": {}}}`)

	result, err := client.EvaluateDecision(t.Context(), "p-1", "credit-limit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched() {
		t.Error("a result with no matched line says it matched")
	}
	if _, sent := f.last().Body["version"]; sent {
		t.Error("a version was sent without being asked for")
	}
}

func TestEvaluateDecisionRefusesNoProjectWithoutAsking(t *testing.T) {
	f, client := newFakeServer(t)
	if _, err := client.EvaluateDecision(t.Context(), "", "credit-limit", nil); err == nil {
		t.Fatal("no error for a missing project")
	}
	if f.count() != 0 {
		t.Error("a request was sent")
	}
}

func TestListInstancesPageCarriesTheCounts(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{
		"instances": [{"id": "i-1", "status": "active"}, {"id": "i-2", "status": "active"}],
		"page": {"total": 40, "page": 1, "page_size": 2, "has_more": true},
		"status_counts": [{"status": "active", "total": 30}, {"status": "failed", "total": 10}],
		"needs_attention_total": 3,
		"needs_attention_ids": ["i-2"]}`)

	page, err := client.ListInstancesPage(t.Context(), ListInstancesOptions{ProjectID: "p-1", PageSize: 2, Status: ProcessActive})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Instances) != 2 || page.Page == nil || !page.Page.HasMore {
		t.Fatalf("page = %+v", page)
	}
	if page.StatusCounts[ProcessFailed] != 10 || page.StatusCounts[ProcessActive] != 30 {
		t.Errorf("counts = %v", page.StatusCounts)
	}
	if page.NeedsAttention != 3 || len(page.NeedsAttentionIDs) != 1 {
		t.Errorf("needs attention = %d %v", page.NeedsAttention, page.NeedsAttentionIDs)
	}
	if q := f.last().Query; q.Get("status") != "active" || q.Get("page_size") != "2" {
		t.Errorf("query = %v", q)
	}
}

func TestListInstancesPageWithoutCountsIsEmptyNotNil(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances": []}`)

	page, err := client.ListInstancesPage(t.Context(), ListInstancesOptions{ProjectID: "p-1"})
	if err != nil {
		t.Fatal(err)
	}
	if page.StatusCounts == nil || page.StatusCounts[ProcessFailed] != 0 {
		t.Errorf("counts = %v", page.StatusCounts)
	}
}

func TestListInstancesPageKeepsTheBusinessKeyGuard(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances": [{"id": "i-1", "status": "active"}]}`)

	_, err := client.ListInstancesPage(t.Context(), ListInstancesOptions{ProjectID: "p-1", BusinessKey: "ORDER-1"})
	if err == nil {
		t.Fatal("an unfiltered answer was accepted")
	}
}

func TestListSubProcesses(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/{id}/subprocesses", 200, `{"instances": [{"id": "child-1", "status": "failed"}]}`)

	children, err := client.ListSubProcesses(t.Context(), "parent 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].Status != ProcessFailed {
		t.Errorf("children = %+v", children)
	}
	if path := f.last().Path; path != "/api/v1/instances/parent 1/subprocesses" {
		t.Errorf("path = %q", path)
	}
}

func TestListWaiting(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/projects/p-1/waiting", 200, `{"processes": [
		{"key": "order", "name": "Order", "instances": 7,
		 "steps": [{"node_id": "approve", "waiting": 5}, {"node_id": "ship", "waiting": 3}]}]}`)

	waiting, err := client.ListWaiting(t.Context(), "p-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 || waiting[0].Instances != 7 || waiting[0].Steps[0].Waiting != 5 {
		t.Errorf("waiting = %+v", waiting)
	}
}

func TestListDeadlines(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/projects/p-1/deadlines", 200, `{"deadlines": {
		"tasks": [
			{"id": "t-1", "name": "Approve", "node_id": "approve", "status": "claimed", "priority": 2,
			 "assignee": "alice", "due_date": "2026-10-20T09:00:00Z", "process_key": "order", "process_name": "Order"},
			{"id": "t-2", "name": "Ship", "node_id": "ship", "status": "unclaimed",
			 "due_date": "2026-10-30T09:00:00Z", "process_key": "order", "process_name": "Order"}],
		"with_deadline": 2, "without_deadline": 9}}`)

	deadlines, err := client.ListDeadlines(t.Context(), "p-1")
	if err != nil {
		t.Fatal(err)
	}
	if deadlines.WithDeadline != 2 || deadlines.WithoutDeadline != 9 || deadlines.Tasks[0].Status != TaskClaimed {
		t.Errorf("deadlines = %+v", deadlines)
	}
	late := deadlines.Overdue(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	if len(late) != 1 || late[0].ID != "t-1" {
		t.Errorf("overdue = %+v", late)
	}
}

func TestProjectViewsRefuseNoProjectWithoutAsking(t *testing.T) {
	f, client := newFakeServer(t)
	if _, err := client.ListWaiting(t.Context(), ""); err == nil {
		t.Error("ListWaiting took no project")
	}
	if _, err := client.ListDeadlines(t.Context(), ""); err == nil {
		t.Error("ListDeadlines took no project")
	}
	if _, err := client.ListSubProcesses(t.Context(), ""); err == nil {
		t.Error("ListSubProcesses took no instance")
	}
	if f.count() != 0 {
		t.Error("a request was sent")
	}
}

func TestVersions(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/definitions/live-versions", 200, `{"live": {"order": 3}}`)
	f.respond("GET /api/v1/definitions/versions", 200, `{"versions": [
		{"id": "d-4", "key": "order", "version": 4, "live": false, "running_instances": 0,
		 "scheduled_for": "2026-11-01T00:00:00Z", "scheduled_release_id": "rel-1"},
		{"id": "d-3", "key": "order", "version": 3, "live": true, "running_instances": 12},
		{"id": "d-2", "key": "order", "version": 2, "live": false, "running_instances": 4}]}`)
	f.respond("POST /api/v1/definitions/versions/promote", 200, `{}`)
	f.respond("POST /api/v1/definitions/versions/schedule", 200, `{}`)
	f.respond("POST /api/v1/definitions/versions/schedule/cancel", 200, `{}`)
	ctx := t.Context()

	live, err := client.ListLiveVersions(ctx, "p-1")
	if err != nil || live["order"] != 3 {
		t.Fatalf("live = %v, %v", live, err)
	}
	if q := f.last().Query; q.Get("project_id") != "p-1" {
		t.Errorf("query = %v", q)
	}

	versions, err := client.ListVersions(ctx, "p-1", "order")
	if err != nil {
		t.Fatal(err)
	}
	if versions[0].ScheduledReleaseID != "rel-1" || versions[0].ScheduledFor.IsZero() {
		t.Errorf("scheduled = %+v", versions[0])
	}
	if versions[1].Draining() || !versions[2].Draining() {
		t.Error("Draining is wrong")
	}
	if q := f.last().Query; q.Get("key") != "order" {
		t.Errorf("query = %v", q)
	}

	if err := client.PromoteVersion(ctx, "p-1", "order", 4); err != nil {
		t.Fatal(err)
	}
	if body := f.last().Body; body["key"] != "order" || body["version"] != float64(4) || body["project_id"] != "p-1" {
		t.Errorf("promote body = %v", body)
	}

	at := time.Date(2026, 11, 1, 1, 0, 0, 0, time.FixedZone("CET", 3600))
	if err := client.ScheduleVersion(ctx, "p-1", "order", 4, at); err != nil {
		t.Fatal(err)
	}
	if body := f.last().Body; body["activate_at"] != "2026-11-01T00:00:00Z" {
		t.Errorf("schedule body = %v", body)
	}

	if err := client.CancelScheduledVersion(ctx, "p-1", "rel-1"); err != nil {
		t.Fatal(err)
	}
	if body := f.last().Body; body["release_id"] != "rel-1" {
		t.Errorf("cancel body = %v", body)
	}
}

func TestVersionCallsRefuseWhatTheServerWould(t *testing.T) {
	f, client := newFakeServer(t)
	ctx := t.Context()
	if err := client.PromoteVersion(ctx, "p-1", "order", 0); err == nil {
		t.Error("version 0 was promoted")
	}
	if err := client.ScheduleVersion(ctx, "p-1", "order", 2, time.Time{}); err == nil {
		t.Error("a cutover with no time was scheduled")
	}
	if err := client.CancelScheduledVersion(ctx, "p-1", ""); err == nil {
		t.Error("a cutover with no ID was cancelled")
	}
	if f.count() != 0 {
		t.Error("a request was sent")
	}
}

func TestUpdateTaskSendsOnlyWhatChanges(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("PUT /api/v1/tasks/t-1", 200, `{}`)
	ctx := t.Context()

	priority := 5
	if err := client.UpdateTask(ctx, "t-1", TaskChanges{Priority: &priority, Reason: "escalated"}); err != nil {
		t.Fatal(err)
	}
	body := f.last().Body
	if body["priority"] != float64(5) || body["reason"] != "escalated" {
		t.Errorf("body = %v", body)
	}
	for _, untouched := range []string{"name", "due_date"} {
		if _, sent := body[untouched]; sent {
			t.Errorf("%s was sent without being changed", untouched)
		}
	}

	due := time.Date(2026, 10, 26, 18, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	if err := client.UpdateTask(ctx, "t-1", TaskChanges{DueDate: &due}); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Body["due_date"]; got != "2026-10-26T11:00:00Z" {
		t.Errorf("due_date = %v", got)
	}

	if err := client.UpdateTask(ctx, "t-1", TaskChanges{DueDate: &due, ClearDueDate: true}); err != nil {
		t.Fatal(err)
	}
	if got, sent := f.last().Body["due_date"]; !sent || got != nil {
		t.Errorf("clearing sent due_date = %v (sent %v), want null", got, sent)
	}
}

func TestUpdateTaskWithNoChangesSendsNothing(t *testing.T) {
	f, client := newFakeServer(t)
	if err := client.UpdateTask(t.Context(), "t-1", TaskChanges{Reason: "why not"}); err == nil {
		t.Fatal("no error for an empty change")
	}
	if f.count() != 0 {
		t.Error("a request was sent")
	}
}

func TestActivateAdHocTask(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/processes/adhoc/activate", 200, `{}`)

	if err := client.ActivateAdHocTask(t.Context(), "i-1", "case", "second-valuation", "the first was disputed"); err != nil {
		t.Fatal(err)
	}
	body := f.last().Body
	if body["instance_id"] != "i-1" || body["sub_process_node_id"] != "case" ||
		body["task_node_id"] != "second-valuation" || body["reason"] != "the first was disputed" {
		t.Errorf("body = %v", body)
	}

	if err := client.ActivateAdHocTask(t.Context(), "i-1", "case", "", ""); err == nil {
		t.Error("a step with no node ID was started")
	}
}

func TestListClaimableTasks(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/tasks/candidates", 200, `{
		"tasks": [{"id": "t-1", "name": "Check", "status": "unclaimed"}],
		"page": {"total": 7, "page": 2, "page_size": 1, "has_more": true}}`)

	tasks, page, err := client.ListClaimableTasks(t.Context(), PageOptions{Page: 2, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != TaskUnclaimed || page == nil || page.Total != 7 {
		t.Errorf("tasks = %+v, page = %+v", tasks, page)
	}
	body := f.last().Body
	if body["page"] != float64(2) || body["page_size"] != float64(1) {
		t.Errorf("body = %v", body)
	}
	if _, named := body["user_id"]; named {
		t.Error("a user was named; the server takes it from the token")
	}
}
