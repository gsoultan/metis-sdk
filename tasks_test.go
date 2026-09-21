package metis

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The acting user is the token on every one of these. A body that carries a
// user_id is a claim the server discards, so the SDK must not send one — these
// tests are what keeps it from creeping back in.
func TestClaimAndUnclaimActAsTheToken(t *testing.T) {
	t.Run("claim posts an empty object, not a user", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("POST /api/v1/tasks/task-1/claim", 200, `{}`)

		if err := client.ClaimTask(t.Context(), "task-1"); err != nil {
			t.Fatalf("claim: %v", err)
		}
		last := f.last()
		if last.Method != "POST" || last.Path != "/api/v1/tasks/task-1/claim" {
			t.Errorf("sent %s %s", last.Method, last.Path)
		}
		if _, sent := last.Body["user_id"]; sent {
			t.Errorf("claim sent user_id: %#v", last.Body)
		}
		// The server decodes a JSON body here; sending none is a decode error
		// on its side, so the empty object is load-bearing.
		if last.Body == nil {
			t.Error("claim sent no JSON body; the server cannot decode that")
		}
	})

	t.Run("unclaim needs only the path", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("POST /api/v1/tasks/task-1/unclaim", 200, `{}`)

		if err := client.UnclaimTask(t.Context(), "task-1"); err != nil {
			t.Fatalf("unclaim: %v", err)
		}
		if got := f.last().Path; got != "/api/v1/tasks/task-1/unclaim" {
			t.Errorf("path = %q", got)
		}
	})
}

// AssignTask is the operation that genuinely names somebody, and the one place
// a user identifier belongs in a body.
func TestAssignTaskNamesAPerson(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/tasks/task-1/assign", 200, `{}`)

	if err := client.AssignTask(t.Context(), "task-1", "alice"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	last := f.last()
	if last.Path != "/api/v1/tasks/task-1/assign" {
		t.Errorf("path = %q", last.Path)
	}
	if last.Body["user_id"] != "alice" {
		t.Errorf("user_id = %v, want alice", last.Body["user_id"])
	}
}

func TestAssignTaskRefusesAnEmptyUsernameWithoutAsking(t *testing.T) {
	f, client := newFakeServer(t)

	err := client.AssignTask(t.Context(), "task-1", "")
	if err == nil {
		t.Fatal("assigning to nobody was accepted")
	}
	// Caught here rather than spent on a round trip the server would reject.
	if f.count() != 0 {
		t.Errorf("sent %d requests for a call that cannot succeed", f.count())
	}
}

func TestCompleteTaskWithoutVariables(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/tasks/task-1/complete", 200, `{}`)

	// Completing without writing anything back is ordinary; nil must not be an
	// error or a panic.
	if err := client.CompleteTask(t.Context(), "task-1", nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got := f.last().Path; got != "/api/v1/tasks/task-1/complete" {
		t.Errorf("path = %q", got)
	}
}

// A listing has to arrive as usable Go: typed status, real times, and variables
// the accessors can read.
func TestListTasksDecodesIntoTheDomainTypes(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200, `{
		"page": {"total": 1, "page": 1, "page_size": 50, "has_more": false},
		"tasks": [{
			"id": "task-1",
			"name": "Approve the refund",
			"status": "claimed",
			"priority": 10,
			"assignee": {"id": "u-1", "username": "alice", "display_name": "Alice"},
			"node": {"id": "approve", "name": "Approve", "type": "userTask"},
			"instance": {"id": "inst-1", "status": "active"},
			"variables": {"amount": 42.5, "urgent": true}
		}]
	}`)

	tasks, page, err := client.ListTasks(t.Context(), ListTasksOptions{PageSize: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks", len(tasks))
	}
	task := tasks[0]

	if task.Status != TaskClaimed {
		t.Errorf("status = %q, want the TaskClaimed constant", task.Status)
	}
	if !task.IsOpen() {
		t.Error("a claimed task should still be open")
	}
	if got := task.AssigneeUsername(); got != "alice" {
		t.Errorf("assignee = %q", got)
	}
	if got := task.InstanceID(); got != "inst-1" {
		t.Errorf("instance = %q", got)
	}
	if task.Instance.Status != ProcessActive {
		t.Errorf("instance status = %q", task.Instance.Status)
	}
	if got, ok := task.Variables.Float64("amount"); !ok || got != 42.5 {
		t.Errorf("amount = %v, %v", got, ok)
	}
	if !task.Variables.BoolOr("urgent", false) {
		t.Error("urgent did not survive decoding")
	}
	if page == nil || page.Total != 1 {
		t.Errorf("page = %+v", page)
	}
}

// The listing returns every status; an inbox is what is left after filtering,
// and IsOpen is the predicate that does it.
func TestTaskStatusIsOpen(t *testing.T) {
	open := []TaskStatus{TaskUnclaimed, TaskClaimed, TaskDelegated, TaskEscalated}
	closed := []TaskStatus{TaskCompleted, TaskCanceled}

	for _, status := range open {
		if !status.IsOpen() {
			t.Errorf("%q should be open", status)
		}
	}
	for _, status := range closed {
		if status.IsOpen() {
			t.Errorf("%q should not be open", status)
		}
	}
}

// Suspended is the interesting case: stopped, but not finished — it is waiting
// for an operator, and an integration that treats it as done abandons work.
func TestProcessStatusIsFinished(t *testing.T) {
	for _, status := range []ProcessStatus{ProcessCompleted, ProcessFailed} {
		if !status.IsFinished() {
			t.Errorf("%q should be finished", status)
		}
	}
	for _, status := range []ProcessStatus{ProcessActive, ProcessSuspended} {
		if status.IsFinished() {
			t.Errorf("%q should not be finished", status)
		}
	}
	if (Instance{Status: ProcessCompleted}).IsFinished() != true {
		t.Error("Instance.IsFinished disagrees with its status")
	}
}

func TestAuditEntryTextPrefersTheNarrative(t *testing.T) {
	withNarrative := AuditEntry{Message: "task.completed", Narrative: "Alice approved the refund"}
	if got := withNarrative.Text(); got != "Alice approved the refund" {
		t.Errorf("Text = %q", got)
	}

	// Not every entry has one; the message is the fallback rather than an empty
	// line in somebody's timeline.
	bare := AuditEntry{Message: "task.completed"}
	if got := bare.Text(); got != "task.completed" {
		t.Errorf("Text = %q", got)
	}
}

// Nil relations are normal in responses that do not expand them, so the
// convenience accessors must not be a new way to panic.
func TestTaskAccessorsOnSparseResponses(t *testing.T) {
	var task UserTask
	if got := task.AssigneeUsername(); got != "" {
		t.Errorf("AssigneeUsername = %q", got)
	}
	if got := task.InstanceID(); got != "" {
		t.Errorf("InstanceID = %q", got)
	}

	if got := task.NodeID(); got != "" {
		t.Errorf("NodeID = %q", got)
	}

	var external ExternalTask
	if got := external.InstanceID(); got != "" {
		t.Errorf("ExternalTask.InstanceID = %q", got)
	}
	if got := external.NodeID(); got != "" {
		t.Errorf("ExternalTask.NodeID = %q", got)
	}

	// And populated, for the path the worker actually takes.
	withNode := ExternalTask{
		Node:            &Node{ID: "charge"},
		ProcessInstance: &Instance{ID: "inst-1"},
	}
	if got := withNode.NodeID(); got != "charge" {
		t.Errorf("ExternalTask.NodeID = %q", got)
	}
	if got := withNode.InstanceID(); got != "inst-1" {
		t.Errorf("ExternalTask.InstanceID = %q", got)
	}
}

func TestGetTaskDecodesDueDate(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks/task-1", 200, `{"task":{
		"id":"task-1","status":"unclaimed","due_date":"2026-09-07T12:00:00Z"
	}}`)

	task, err := client.GetTask(t.Context(), "task-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.DueDate == nil {
		t.Fatal("due date did not decode")
	}
	want := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	if !task.DueDate.Equal(want) {
		t.Errorf("due date = %v, want %v", task.DueDate, want)
	}
	// A task past its due date is still open — the deadline is information,
	// not a cancellation.
	if !task.IsOpen() {
		t.Error("an unclaimed task should be open")
	}
}

// ListTasks does send paging, which is worth pinning precisely because the
// instance listing cannot: the difference is a server-side one, and a reader
// comparing the two methods should find it asserted rather than implied.
func TestListTasksSendsPagingAsQueryParameters(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[]}`)

	_, _, err := client.ListTasks(t.Context(), ListTasksOptions{
		ProjectID: "p-1", Page: 2, PageSize: 25,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	query := f.last().Query
	for key, want := range map[string]string{
		"project_id": "p-1",
		"page":       "2",
		"page_size":  "25",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// The zero value means "no preference", and must not become page=0, which the
// server would read as a request for a page that does not exist.
func TestListTasksOmitsUnsetPaging(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[]}`)

	if _, _, err := client.ListTasks(t.Context(), ListTasksOptions{}); err != nil {
		t.Fatalf("list: %v", err)
	}

	query := f.last().Query
	for _, key := range []string{"project_id", "page", "page_size"} {
		if _, sent := query[key]; sent {
			t.Errorf("sent %s=%q for an unset option", key, query.Get(key))
		}
	}
}

// Node.Name is never sent, and Node.Type only by servers new enough. Reading
// the kind off the task itself is therefore the form that works against every
// server, which is what this pins — the response below is what an older one
// answers.
func TestTaskCarriesNodeIDAndTypeSeparately(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks/task-1", 200, `{"task":{
		"id":"task-1",
		"name":"Approve the refund",
		"type":"userTask",
		"status":"unclaimed",
		"node":{"id":"approve"},
		"created_at":"2026-09-07T12:00:00Z"
	}}`)

	task, err := client.GetTask(t.Context(), "task-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if task.Type != NodeUserTask {
		t.Errorf("Type = %q, want the NodeUserTask constant", task.Type)
	}
	if got := task.NodeID(); got != "approve" {
		t.Errorf("NodeID = %q", got)
	}
	// An older server sends the node as a bare identifier. Task.Type still
	// answered above, which is the point: it does not depend on the server's
	// age, and Node.Type does.
	if task.Node.Type != "" || task.Node.Name != "" {
		t.Errorf("the fixture is meant to be an older server's answer: %+v", task.Node)
	}
	// Name on the task itself is the readable label.
	if task.Name != "Approve the refund" {
		t.Errorf("Name = %q", task.Name)
	}
	if task.CreatedAt.IsZero() {
		t.Error("CreatedAt did not decode")
	}
}

func TestLatestTask(t *testing.T) {
	t.Run("returns the newest, whatever its status", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/tasks", 200, `{"tasks":[
			{"id":"task-3","status":"completed","type":"userTask","node":{"id":"approve"},
			 "created_at":"2026-09-07T12:00:00Z"},
			{"id":"task-2","status":"unclaimed","type":"userTask","node":{"id":"review"},
			 "created_at":"2026-09-06T12:00:00Z"}
		]}`)

		task, err := client.LatestTask(t.Context(), ListTasksOptions{ProjectID: "p-1"})
		if err != nil {
			t.Fatalf("latest: %v", err)
		}
		if task.ID != "task-3" {
			t.Errorf("id = %q, want the newest", task.ID)
		}
		// Completed is still the latest — the doc promises exactly this, and
		// points at IsOpen for the other question.
		if task.IsOpen() {
			t.Error("expected the newest task to be the completed one")
		}
		if got := task.NodeID(); got != "approve" {
			t.Errorf("node = %q", got)
		}
		if got := f.last().Query.Get("project_id"); got != "p-1" {
			t.Errorf("project_id = %q", got)
		}
	})

	t.Run("an empty project is ErrNoTasks", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/tasks", 200, `{"tasks":[]}`)

		task, err := client.LatestTask(t.Context(), ListTasksOptions{ProjectID: "p-1"})
		if !errors.Is(err, ErrNoTasks) {
			t.Fatalf("err = %v, want ErrNoTasks", err)
		}
		if task != nil {
			t.Error("returned a task alongside the error")
		}
		if IsNotFound(err) {
			t.Error("IsNotFound claimed an empty result")
		}
	})

	t.Run("server failures pass through", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/tasks", 403, `{"error":"not a member"}`)

		if _, err := client.LatestTask(t.Context(), ListTasksOptions{ProjectID: "p-1"}); !IsUnauthorized(err) {
			t.Errorf("err = %v", err)
		}
	})
}

// Filtering a listing to one instance is client-side work, because the server
// takes no instance parameter. This is the shape the ListTasks doc describes.
func TestFilteringTasksToOneInstanceIsClientSide(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[
		{"id":"task-3","status":"unclaimed","instance":{"id":"inst-2"},"node":{"id":"approve"}},
		{"id":"task-2","status":"unclaimed","instance":{"id":"inst-1"},"node":{"id":"review"}},
		{"id":"task-1","status":"completed","instance":{"id":"inst-1"},"node":{"id":"start"}}
	]}`)

	tasks, _, err := client.ListTasks(t.Context(), ListTasksOptions{ProjectID: "p-1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	// Newest-first ordering survives the filter, so the first match is the
	// latest task of that instance.
	var latest *UserTask
	for i := range tasks {
		if tasks[i].InstanceID() == "inst-1" {
			latest = &tasks[i]
			break
		}
	}
	if latest == nil {
		t.Fatal("no task matched the instance")
	}
	if latest.ID != "task-2" || latest.NodeID() != "review" {
		t.Errorf("latest for inst-1 = %s at %s", latest.ID, latest.NodeID())
	}

	// And the query carried no instance filter, because there is none to send.
	if _, sent := f.last().Query["instance_id"]; sent {
		t.Error("sent an instance_id the server does not read")
	}
}

// The instance filter is a server-side one now, so it has to travel as a query
// parameter — the client-side match it replaces is what this asserts against.
func TestListTasksFiltersByInstanceOnTheServer(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200, `{
		"page": {"total": 2, "page": 1, "page_size": 50, "has_more": false},
		"tasks": [
			{"id":"task-2","status":"unclaimed","type":"userTask",
			 "instance":{"id":"inst-1"},"node":{"id":"approve","type":"userTask"}},
			{"id":"task-1","status":"completed","type":"userTask",
			 "instance":{"id":"inst-1"},"node":{"id":"review","type":"userTask"}}
		]
	}`)

	tasks, page, err := client.ListTasks(t.Context(), ListTasksOptions{InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if got := f.last().Query.Get("instance_id"); got != "inst-1" {
		t.Errorf("instance_id = %q, want it on the query string", got)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks", len(tasks))
	}
	// The total describes the instance's tasks, so a caller can page them
	// rather than page the project and discard.
	if page == nil || page.Total != 2 {
		t.Errorf("page = %+v", page)
	}
	// Newer servers fill in the node's type; older ones leave it empty and
	// UserTask.Type still answers.
	if tasks[0].Node.Type != NodeUserTask {
		t.Errorf("node type = %q", tasks[0].Node.Type)
	}
	if tasks[0].Type != NodeUserTask {
		t.Errorf("task type = %q", tasks[0].Type)
	}
}

// An instance narrows harder than a project and already implies one, so both
// may be sent; the server decides. What matters here is that the SDK does not
// drop either.
func TestListTasksSendsBothScopesWhenGivenBoth(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[]}`)

	_, _, err := client.ListTasks(t.Context(), ListTasksOptions{
		ProjectID: "p-1", InstanceID: "inst-1",
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	query := f.last().Query
	if query.Get("project_id") != "p-1" || query.Get("instance_id") != "inst-1" {
		t.Errorf("query = %v", query)
	}
}

// LatestTask asks for one row rather than a page it would discard, and works
// against whichever scope it is given.
func TestLatestTaskAsksForASingleRow(t *testing.T) {
	for name, opts := range map[string]ListTasksOptions{
		"by instance": {InstanceID: "inst-1"},
		"by project":  {ProjectID: "p-1"},
		// A caller's own paging is not the question being asked.
		"ignoring the caller's paging": {InstanceID: "inst-1", Page: 4, PageSize: 100},
	} {
		t.Run(name, func(t *testing.T) {
			f, client := newFakeServer(t)
			f.respond("GET /api/v1/tasks", 200,
				`{"tasks":[{"id":"task-9","status":"unclaimed","node":{"id":"approve"}}]}`)

			task, err := client.LatestTask(t.Context(), opts)
			if err != nil {
				t.Fatalf("latest: %v", err)
			}
			if task.ID != "task-9" {
				t.Errorf("id = %q", task.ID)
			}

			query := f.last().Query
			if got := query.Get("page_size"); got != "1" {
				t.Errorf("page_size = %q, want 1 — a whole page would be fetched and thrown away", got)
			}
			if _, sent := query["page"]; sent {
				t.Errorf("page = %q, want it unset", query.Get("page"))
			}
		})
	}
}

func TestListTasksByAssignee(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks/assignee/alice", 200, `{
		"page": {"total": 2, "page": 1, "page_size": 50, "has_more": false},
		"tasks": [
			{"id":"task-2","status":"claimed","type":"userTask",
			 "assignee":{"id":"u-1","username":"alice"},"node":{"id":"approve"}},
			{"id":"task-1","status":"completed","type":"userTask",
			 "assignee":{"id":"u-1","username":"alice"},"node":{"id":"review"}}
		]
	}`)

	tasks, page, err := client.ListTasksByAssignee(t.Context(), "alice", PageOptions{PageSize: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks", len(tasks))
	}
	if got := tasks[0].AssigneeUsername(); got != "alice" {
		t.Errorf("assignee = %q", got)
	}
	// Completed ones are included; IsOpen is the inbox filter.
	if !tasks[0].IsOpen() || tasks[1].IsOpen() {
		t.Errorf("statuses = %q, %q", tasks[0].Status, tasks[1].Status)
	}
	if page == nil || page.Total != 2 {
		t.Errorf("page = %+v", page)
	}
	if got := f.last().Query.Get("page_size"); got != "50" {
		t.Errorf("page_size = %q", got)
	}
}

// A username goes in the path, so one containing a backslash or a space must
// not escape the route it was meant for.
func TestListTasksByAssigneeEscapesTheUsername(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks/assignee/{assignee}", 200, `{"tasks":[]}`)

	if _, _, err := client.ListTasksByAssignee(t.Context(), "domain\\alice smith", PageOptions{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := f.last().Path; got != "/api/v1/tasks/assignee/domain\\alice smith" {
		t.Errorf("path = %q", got)
	}
}

func TestListTasksByAssigneeRefusesAnEmptyUsername(t *testing.T) {
	f, client := newFakeServer(t)

	if _, _, err := client.ListTasksByAssignee(t.Context(), "", PageOptions{}); err == nil {
		t.Fatal("an empty username was accepted")
	}
	// Refused here rather than spent on a request to /tasks/assignee/, which is
	// a different route entirely.
	if f.count() != 0 {
		t.Errorf("sent %d requests", f.count())
	}
}

func TestDelegateTask(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/tasks/task-1/delegate", 200, `{}`)

	if err := client.DelegateTask(t.Context(), "task-1", "bob"); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	last := f.last()
	if last.Path != "/api/v1/tasks/task-1/delegate" {
		t.Errorf("path = %q", last.Path)
	}
	if last.Body["user_id"] != "bob" {
		t.Errorf("user_id = %v", last.Body["user_id"])
	}
}

func TestDelegateTaskRefusesAnEmptyUsername(t *testing.T) {
	f, client := newFakeServer(t)

	if err := client.DelegateTask(t.Context(), "task-1", ""); err == nil {
		t.Fatal("delegating to nobody was accepted")
	}
	if f.count() != 0 {
		t.Errorf("sent %d requests for a call the server would reject", f.count())
	}
}

// Both hand-over calls are guarded the same way server-side, and a caller who
// is neither the holder nor an administrator sees the same answer from each.
func TestHandOverCallsSurfaceForbidden(t *testing.T) {
	for name, call := range map[string]func(*Client) error{
		"assign":   func(c *Client) error { return c.AssignTask(t.Context(), "task-1", "bob") },
		"delegate": func(c *Client) error { return c.DelegateTask(t.Context(), "task-1", "bob") },
	} {
		t.Run(name, func(t *testing.T) {
			f, client := newFakeServer(t)
			f.respond("POST /api/v1/tasks/task-1/"+name, 403,
				`{"error":"only the person holding this task, or an administrator, can hand it to someone else"}`)

			if err := call(client); !IsUnauthorized(err) {
				t.Errorf("err = %v, want the 403", err)
			}
		})
	}
}

// A server that does not read instance_id falls through to an unfiltered
// listing and answers with every task the caller can see — the same shape as a
// filtered one. Returning that as though it were filtered is how a caller
// completes work belonging to another run, so it is refused.
func TestListTasksRefusesAnUnfilteredAnswer(t *testing.T) {
	f, client := newFakeServer(t)
	// What Metis v0.2.0 and earlier answer: the whole organization.
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[
		{"id":"task-2","status":"unclaimed","instance":{"id":"inst-1"}},
		{"id":"task-9","status":"unclaimed","instance":{"id":"inst-OTHER"}}
	]}`)

	tasks, page, err := client.ListTasks(t.Context(), ListTasksOptions{InstanceID: "inst-1"})
	if !errors.Is(err, ErrFilterUnsupported) {
		t.Fatalf("err = %v, want ErrFilterUnsupported", err)
	}
	// No half-answer: a caller that ignores the error must not find a list to
	// range over.
	if tasks != nil || page != nil {
		t.Error("tasks were returned alongside the error")
	}
	// The message has to name what was asked, what came back, and why — the
	// reader is debugging a server they may not have known was too old.
	for _, want := range []string{"inst-1", "inst-OTHER", "task-9", "v0.3.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// The guard must not fire on a correctly filtered answer, or it breaks the
// feature it exists to protect.
func TestListTasksAcceptsACorrectlyFilteredAnswer(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[
		{"id":"task-2","status":"unclaimed","instance":{"id":"inst-1"}},
		{"id":"task-1","status":"completed","instance":{"id":"inst-1"}}
	]}`)

	tasks, _, err := client.ListTasks(t.Context(), ListTasksOptions{InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("a correctly filtered listing was refused: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("got %d tasks", len(tasks))
	}
}

func TestInstanceFilterGuardEdgeCases(t *testing.T) {
	t.Run("no filter asked for, nothing to verify", func(t *testing.T) {
		// A project-wide listing legitimately spans instances.
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/tasks", 200, `{"tasks":[
			{"id":"task-1","instance":{"id":"inst-1"}},
			{"id":"task-2","instance":{"id":"inst-2"}}
		]}`)

		if _, _, err := client.ListTasks(t.Context(), ListTasksOptions{ProjectID: "p-1"}); err != nil {
			t.Fatalf("a project listing was refused: %v", err)
		}
	})

	t.Run("an empty result is not proof of anything", func(t *testing.T) {
		// An instance with no tasks is ordinary; it is also what an old server
		// returns for an empty organization. Neither is an error.
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/tasks", 200, `{"tasks":[]}`)

		if _, _, err := client.ListTasks(t.Context(), ListTasksOptions{InstanceID: "inst-1"}); err != nil {
			t.Fatalf("an empty listing was refused: %v", err)
		}
	})

	t.Run("tasks with no instance expanded are skipped, not suspected", func(t *testing.T) {
		// Nothing here proves the filter was ignored, so the answer stands.
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/tasks", 200, `{"tasks":[{"id":"task-1","status":"unclaimed"}]}`)

		tasks, _, err := client.ListTasks(t.Context(), ListTasksOptions{InstanceID: "inst-1"})
		if err != nil {
			t.Fatalf("an unverifiable listing was refused: %v", err)
		}
		if len(tasks) != 1 {
			t.Errorf("got %d tasks", len(tasks))
		}
	})
}

// LatestTask reads through ListTasks, so it inherits the guard rather than
// needing its own — and must not hand back a task from another run.
func TestLatestTaskInheritsTheInstanceGuard(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200,
		`{"tasks":[{"id":"task-9","status":"unclaimed","instance":{"id":"inst-OTHER"}}]}`)

	task, err := client.LatestTask(t.Context(), ListTasksOptions{InstanceID: "inst-1"})
	if !errors.Is(err, ErrFilterUnsupported) {
		t.Fatalf("err = %v, want ErrFilterUnsupported", err)
	}
	if task != nil {
		t.Error("a task from another instance was returned")
	}
}
