package metis

import (
	"testing"
)

// From Metis v0.5.0 a hand-over by anybody but the task's holder needs a
// reason, and the server reads it from the body. These pin that it is sent
// when given, and that nothing is invented when it is not.
func TestHandOversCarryAReason(t *testing.T) {
	calls := []struct {
		name string
		path string
		call func(c *Client, opts ...HandOverOption) error
	}{
		{"assign", "/api/v1/tasks/task-1/assign", func(c *Client, opts ...HandOverOption) error {
			return c.AssignTask(t.Context(), "task-1", "bob", opts...)
		}},
		{"delegate", "/api/v1/tasks/task-1/delegate", func(c *Client, opts ...HandOverOption) error {
			return c.DelegateTask(t.Context(), "task-1", "bob", opts...)
		}},
		{"unclaim", "/api/v1/tasks/task-1/unclaim", func(c *Client, opts ...HandOverOption) error {
			return c.UnclaimTask(t.Context(), "task-1", opts...)
		}},
		{"resolve", "/api/v1/tasks/task-1/resolve", func(c *Client, opts ...HandOverOption) error {
			return c.ResolveTask(t.Context(), "task-1", opts...)
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name+" sends the reason", func(t *testing.T) {
			f, client := newFakeServer(t)
			f.respond("POST "+tc.path, 200, `{}`)

			if err := tc.call(client, WithReason("Alice is on leave")); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			last := f.last()
			if last.Method != "POST" || last.Path != tc.path {
				t.Errorf("sent %s %s", last.Method, last.Path)
			}
			if got := last.Body["reason"]; got != "Alice is on leave" {
				t.Errorf("reason = %#v", got)
			}
		})
		t.Run(tc.name+" sends no reason it was not given", func(t *testing.T) {
			f, client := newFakeServer(t)
			f.respond("POST "+tc.path, 200, `{}`)

			if err := tc.call(client, WithReason(""), nil); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			last := f.last()
			if _, sent := last.Body["reason"]; sent {
				t.Errorf("an empty reason was sent: %#v", last.Body)
			}
			// A JSON object either way: the server decodes the body on every
			// one of these routes.
			if last.Body == nil {
				t.Error("no JSON body was sent")
			}
		})
	}
}

// The server's refusal of a hand-over without a reason is a 400, and is
// answered as the caller's mistake rather than retried.
func TestHandOverWithoutAReasonIsInvalid(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/tasks/task-1/assign", 400,
		`{"error":"say why the task is being handed over"}`)

	err := client.AssignTask(t.Context(), "task-1", "bob")
	if !IsInvalid(err) {
		t.Fatalf("err = %v, want a 400", err)
	}
}

func TestListDelegatedTasks(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks/delegated", 200, `{
		"page":{"page":2,"page_size":10,"total":11},
		"tasks":[{"id":"task-1","name":"Approve refund","status":"delegated"}]
	}`)

	tasks, page, err := client.ListDelegatedTasks(t.Context(), PageOptions{Page: 2, PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "task-1" || tasks[0].Status != TaskDelegated {
		t.Fatalf("tasks = %+v", tasks)
	}
	if page == nil {
		t.Fatal("no page info")
	}
	last := f.last()
	if last.Path != "/api/v1/tasks/delegated" {
		t.Errorf("path = %q", last.Path)
	}
	if last.Query.Get("page") != "2" || last.Query.Get("page_size") != "10" {
		t.Errorf("query = %v", last.Query)
	}
}
