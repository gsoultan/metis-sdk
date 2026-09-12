package metis

import (
	"encoding/base64"
	"errors"
	"net/http"
	"testing"
	"time"
)

// These pin the paths and body shapes of the calls that are thin wrappers over
// do(). They are one line of behaviour each, but the path is the contract: a
// typo here is a 404 in somebody's integration, not a compile error.

func TestListProjects(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/projects", 200,
		`{"projects":[{"id":"p-1","name":"Default Project","description":"the one"}]}`)

	projects, err := client.ListProjects(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 1 || projects[0].Name != "Default Project" {
		t.Fatalf("projects = %+v", projects)
	}
	if got := f.last().Path; got != "/api/v1/projects" {
		t.Errorf("path = %q", got)
	}
}

func TestImportAndExportDefinition(t *testing.T) {
	xml := []byte(`<definitions/>`)

	t.Run("import sends the XML base64-encoded", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("POST /api/v1/definitions/import", 200, `{"id":"def-1"}`)

		id, err := client.ImportDefinition(t.Context(), "p-1", xml)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if id != "def-1" {
			t.Errorf("id = %q", id)
		}
		// []byte marshals to base64, which is what the server decodes.
		encoded, ok := f.last().Body["xml"].(string)
		if !ok {
			t.Fatalf("xml sent as %T", f.last().Body["xml"])
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || string(decoded) != string(xml) {
			t.Errorf("xml did not survive encoding: %q (%v)", encoded, err)
		}
	})

	t.Run("import without a project is refused locally", func(t *testing.T) {
		f, client := newFakeServer(t)

		if _, err := client.ImportDefinition(t.Context(), "", xml); err == nil {
			t.Fatal("a definition with no project was accepted")
		}
		// Refused before the round trip, because such a definition would deploy
		// and then be invisible to its own organization.
		if f.count() != 0 {
			t.Errorf("sent %d requests", f.count())
		}
	})

	t.Run("export returns the XML", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/definitions/def-1/export", 200,
			`{"xml":"`+base64.StdEncoding.EncodeToString(xml)+`"}`)

		got, err := client.ExportDefinition(t.Context(), "def-1")
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		if string(got) != string(xml) {
			t.Errorf("xml = %q", got)
		}
		if path := f.last().Path; path != "/api/v1/definitions/def-1/export" {
			t.Errorf("path = %q", path)
		}
	})
}

func TestGetInstance(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/inst-1", 200, `{"instance":{
		"id":"inst-1","status":"completed",
		"definition":{"id":"def-1","key":"refund","name":"Refund","version":2},
		"variables":{"amount":42.5},
		"created_at":"2026-09-07T12:00:00Z"
	}}`)

	instance, err := client.GetInstance(t.Context(), "inst-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if instance.Status != ProcessCompleted || !instance.IsFinished() {
		t.Errorf("status = %q", instance.Status)
	}
	if instance.Definition == nil || instance.Definition.Version != 2 {
		t.Errorf("definition = %+v", instance.Definition)
	}
	if got, ok := instance.Variables.Float64("amount"); !ok || got != 42.5 {
		t.Errorf("amount = %v", got)
	}
	if !instance.CreatedAt.Equal(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("created = %v", instance.CreatedAt)
	}
}

func TestGetTimeline(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/inst-1/audit", 200, `{"entries":[
		{"type":"task.completed","message":"task.completed","narrative":"Alice approved"},
		{"type":"process.started","message":"process.started"}
	]}`)

	entries, err := client.GetTimeline(t.Context(), "inst-1")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries", len(entries))
	}
	if got := entries[0].Text(); got != "Alice approved" {
		t.Errorf("first line = %q", got)
	}
	if got := entries[1].Text(); got != "process.started" {
		t.Errorf("second line = %q", got)
	}
	if path := f.last().Path; path != "/api/v1/instances/inst-1/audit" {
		t.Errorf("path = %q", path)
	}
}

// A message goes to one waiting instance; a signal goes to all of them. They
// are different endpoints for that reason, and the correlation key is what
// makes the first one specific.
func TestMessagesAndSignals(t *testing.T) {
	t.Run("message carries the correlation key", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("POST /api/v1/processes/message", 200, `{}`)

		err := client.SendMessage(t.Context(), "p-1", "payment-received", "order-9",
			Variables{"paid": true})
		if err != nil {
			t.Fatalf("message: %v", err)
		}
		last := f.last()
		if last.Path != "/api/v1/processes/message" {
			t.Errorf("path = %q", last.Path)
		}
		if last.Body["message_name"] != "payment-received" {
			t.Errorf("message_name = %v", last.Body["message_name"])
		}
		if last.Body["correlation_key"] != "order-9" {
			t.Errorf("correlation_key = %v", last.Body["correlation_key"])
		}
	})

	t.Run("signal has no addressee", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("POST /api/v1/processes/signal", 200, `{}`)

		if err := client.BroadcastSignal(t.Context(), "p-1", "market-closed", nil); err != nil {
			t.Fatalf("signal: %v", err)
		}
		last := f.last()
		if last.Path != "/api/v1/processes/signal" {
			t.Errorf("path = %q", last.Path)
		}
		if _, has := last.Body["correlation_key"]; has {
			t.Error("a signal should not carry a correlation key")
		}
	})
}

func TestStartProcessSendsTheKeyNotTheID(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"inst-1"}`)

	id, err := client.StartProcess(t.Context(), "p-1", "refund", Variables{"amount": 900})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if id != "inst-1" {
		t.Errorf("instance = %q", id)
	}
	// definition_key, not definition_id: starting follows the latest version of
	// a key, which is what makes a redeploy take effect without a code change.
	if got := f.last().Body["definition_key"]; got != "refund" {
		t.Errorf("definition_key = %v", got)
	}
}

// WithHTTPClient is how a caller supplies their own transport — for TLS,
// proxies, or instrumentation — so it has to actually be used.
func TestWithHTTPClientIsUsed(t *testing.T) {
	used := false
	custom := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			used = true
			return http.DefaultTransport.RoundTrip(r)
		}),
	}

	f, client := newFakeServer(t)
	WithHTTPClient(custom)(client)
	f.respond("GET /api/v1/projects", 200, `{"projects":[]}`)

	if _, err := client.ListProjects(t.Context()); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !used {
		t.Error("the supplied http.Client was not used")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The whole value of ListInstances is the ordering: the server sorts by
// created_at descending, and LatestInstance is only correct because of it. If
// that ever changes server-side, this is the test that should fail.
func TestListInstancesIsNewestFirst(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{
		"page": {"total": 3, "page": 1, "page_size": 20, "has_more": false},
		"instances": [
			{"id":"inst-3","status":"active","created_at":"2026-09-07T12:00:00Z"},
			{"id":"inst-2","status":"completed","created_at":"2026-09-06T12:00:00Z"},
			{"id":"inst-1","status":"failed","created_at":"2026-09-05T12:00:00Z"}
		]
	}`)

	instances, page, err := client.ListInstances(t.Context(), ListInstancesOptions{ProjectID: "p-1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(instances) != 3 {
		t.Fatalf("got %d instances", len(instances))
	}
	if instances[0].ID != "inst-3" {
		t.Errorf("first = %q, want the newest", instances[0].ID)
	}
	if instances[0].Status != ProcessActive {
		t.Errorf("status = %q", instances[0].Status)
	}
	if page == nil || page.Total != 3 {
		t.Errorf("page = %+v", page)
	}

	// project_id is a query parameter, and the only one this endpoint reads.
	last := f.last()
	if got := last.Query.Get("project_id"); got != "p-1" {
		t.Errorf("project_id = %q", got)
	}
}

func TestLatestInstance(t *testing.T) {
	t.Run("returns the newest", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/instances", 200, `{"instances":[
			{"id":"inst-3","status":"active"},
			{"id":"inst-2","status":"completed"}
		]}`)

		instance, err := client.LatestInstance(t.Context(), "p-1")
		if err != nil {
			t.Fatalf("latest: %v", err)
		}
		if instance.ID != "inst-3" {
			t.Errorf("id = %q", instance.ID)
		}
	})

	t.Run("an empty project is ErrNoInstances, not a nil instance", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/instances", 200, `{"instances":[]}`)

		instance, err := client.LatestInstance(t.Context(), "p-1")
		if !errors.Is(err, ErrNoInstances) {
			t.Fatalf("err = %v, want ErrNoInstances", err)
		}
		if instance != nil {
			t.Error("returned an instance alongside the error")
		}
		// Nothing was wrong with the request, so the 404 predicate must not
		// claim this one.
		if IsNotFound(err) {
			t.Error("IsNotFound claimed an empty result")
		}
	})

	t.Run("server failures pass through", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/instances", 403, `{"error":"not a member"}`)

		if _, err := client.LatestInstance(t.Context(), "p-1"); !IsUnauthorized(err) {
			t.Errorf("err = %v, want the underlying 403", err)
		}
	})
}

// The server answers 400 for an empty project id rather than listing the whole
// organization. Spending a round trip to be told that is waste the SDK can
// save, and the local message can say why.
func TestListInstancesRefusesAnEmptyProject(t *testing.T) {
	f, client := newFakeServer(t)

	if _, _, err := client.ListInstances(t.Context(), ListInstancesOptions{}); err == nil {
		t.Fatal("an empty project ID was accepted")
	}
	if f.count() != 0 {
		t.Errorf("sent %d requests", f.count())
	}

	if _, err := client.LatestInstance(t.Context(), ""); err == nil {
		t.Fatal("LatestInstance accepted an empty project ID")
	}
}

// Paging the instance listing works now; it did not before, and the SDK left
// the options out rather than send parameters the server discarded.
func TestListInstancesSendsPaging(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances":[]}`)

	_, _, err := client.ListInstances(t.Context(), ListInstancesOptions{
		ProjectID: "p-1", Page: 3, PageSize: 25,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	query := f.last().Query
	for key, want := range map[string]string{
		"project_id": "p-1", "page": "3", "page_size": "25",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestListInstancesOmitsUnsetPaging(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances":[]}`)

	if _, _, err := client.ListInstances(t.Context(), ListInstancesOptions{ProjectID: "p-1"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	query := f.last().Query
	for _, key := range []string{"page", "page_size"} {
		if _, sent := query[key]; sent {
			t.Errorf("sent %s=%q for an unset option", key, query.Get(key))
		}
	}
}

func TestLatestInstanceAsksForASingleRow(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200,
		`{"instances":[{"id":"inst-9","status":"active"}]}`)

	instance, err := client.LatestInstance(t.Context(), "p-1")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if instance.ID != "inst-9" {
		t.Errorf("id = %q", instance.ID)
	}
	if got := f.last().Query.Get("page_size"); got != "1" {
		t.Errorf("page_size = %q, want 1", got)
	}
}

// The execution path is an instance read, like GetTimeline above. The
// frequencies are what make a loop visible: a node entered four times is one
// entry in Nodes and a four in Frequencies.
func TestGetExecutionPath(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/inst-1/path", 200, `{
		"nodes":[{"id":"start"},{"id":"review"},{"id":"decide"}],
		"frequencies":{"start":1,"review":4,"decide":4}
	}`)

	path, err := client.GetExecutionPath(t.Context(), "inst-1")
	if err != nil {
		t.Fatalf("path: %v", err)
	}

	if len(path.Nodes) != 3 || path.Nodes[0].ID != "start" {
		t.Fatalf("nodes = %+v", path.Nodes)
	}
	if got := path.Visits("review"); got != 4 {
		t.Errorf("Visits(review) = %d, want 4", got)
	}
	if got := path.Visits("never"); got != 0 {
		t.Errorf("Visits of an unreached node = %d", got)
	}
	if !path.Reached("decide") || path.Reached("escalate") {
		t.Error("Reached disagrees with the frequencies")
	}
	if !path.Looped() {
		t.Error("a node entered four times is a loop")
	}

	// Built from the audit trail, so ids without labels.
	if path.Nodes[0].Name != "" || path.Nodes[0].Type != "" {
		t.Errorf("the path carried labels it cannot know: %+v", path.Nodes[0])
	}
}

func TestExecutionPathWithoutLoops(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/inst-1/path", 200,
		`{"nodes":[{"id":"start"},{"id":"done"}],"frequencies":{"start":1,"done":1}}`)

	path, err := client.GetExecutionPath(t.Context(), "inst-1")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if path.Looped() {
		t.Error("a straight run reported a loop")
	}
}

// This endpoint reports failures inline as well as by status code, the same as
// fetch-and-lock — a 200 with an error field is still a failure.
func TestExecutionPathSurfacesInlineErrors(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/inst-1/path", 200, `{"error":"instance not found"}`)

	path, err := client.GetExecutionPath(t.Context(), "inst-1")
	if err == nil {
		t.Fatal("an inline error was reported as success")
	}
	if path != nil {
		t.Error("a path was returned alongside the error")
	}
}
