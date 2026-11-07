package metis

import "testing"

// The report is read for one project, and each task says who it waits for.
func TestListStrandedTasksReadsTheProjectsReport(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/participants/stranded-tasks", 200, `{"tasks":[
		{"task_id":"t-1","name":"Supervisor approval","instance_id":"i-1","since":"2026-11-07T13:32:24Z",
		 "kind":"assignee_inactive","assignee":"budi","missing":[{"name":"budi","kind":"person","why":"inactive"}]},
		{"task_id":"t-2","name":"Manager approves","kind":"no_candidate",
		 "missing":[{"name":"line-managers","kind":"group","why":"gone"}]}
	]}`)

	got, err := client.ListStrandedTasks(t.Context(), "proj-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if f.last().Query.Get("project_id") != "proj-1" {
		t.Errorf("asked for project %q", f.last().Query.Get("project_id"))
	}
	if len(got) != 2 || got[0].Kind != StrandedAssigneeInactive || got[0].Assignee != "budi" || got[0].Since.IsZero() {
		t.Fatalf("first = %+v", got)
	}
	if got[1].Kind != StrandedNoCandidate || got[1].Missing[0].Why != "gone" || got[1].Missing[0].Kind != "group" {
		t.Errorf("second = %+v", got[1])
	}
}

// Without a project there is nothing to ask for.
func TestListStrandedTasksNeedsAProject(t *testing.T) {
	_, client := newFakeServer(t)
	if _, err := client.ListStrandedTasks(t.Context(), ""); err == nil {
		t.Fatal("no error without a project")
	}
}
