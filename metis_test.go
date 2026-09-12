package metis

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// fakeServer records what the SDK sends and answers with canned JSON, so these
// tests pin the wire contract: paths, methods, headers, and body shapes. The
// live end-to-end proof against a real server lives in examples/quickstart.
type fakeServer struct {
	t   *testing.T
	mux *http.ServeMux

	// A Worker polls from its own goroutine while the test reads what it sent,
	// so the record is shared across goroutines and needs the lock. Without it
	// the concurrent tests below are a data race that -race would fail on.
	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Auth   string
	Org    string
	Body   map[string]any
}

func newFakeServer(t *testing.T) (*fakeServer, *Client) {
	t.Helper()
	f := &fakeServer{t: t, mux: http.NewServeMux()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Auth:   r.Header.Get("Authorization"),
			Org:    r.Header.Get("X-Organization-ID"),
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&record.Body)
		}
		f.mu.Lock()
		f.requests = append(f.requests, record)
		f.mu.Unlock()

		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return f, NewClient(server.URL, WithToken("test-token"))
}

func (f *fakeServer) respond(pattern string, status int, body string) {
	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func (f *fakeServer) last() recordedRequest {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		f.t.Fatal("the SDK sent no request")
	}
	return f.requests[len(f.requests)-1]
}

// snapshot copies what has been recorded so far, for tests that read while a
// worker is still polling.
func (f *fakeServer) snapshot() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

// count reports how many requests have been recorded.
func (f *fakeServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func TestLoginStoresTheToken(t *testing.T) {
	f, client := newFakeServer(t)
	client.token = "" // start unauthenticated
	f.respond("POST /api/v1/login", 200, `{"token":"issued-token","user":{"id":"u1"}}`)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[]}`)

	if err := client.Login(t.Context(), "admin", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, _, err := client.ListTasks(t.Context(), ListTasksOptions{}); err != nil {
		t.Fatalf("list: %v", err)
	}

	if got := f.last().Auth; got != "Bearer issued-token" {
		t.Errorf("Authorization = %q, want the token Login stored", got)
	}
}

func TestOrganizationHeaderIsSent(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[]}`)

	client.SetOrganization("org-123")
	if _, _, err := client.ListTasks(t.Context(), ListTasksOptions{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := f.last().Org; got != "org-123" {
		t.Errorf("X-Organization-ID = %q, want org-123", got)
	}
}

func TestImportDefinitionSendsProjectAndBase64XML(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/definitions/import", 200, `{"id":"def-1"}`)

	xml := []byte(`<definitions/>`)
	id, err := client.ImportDefinition(t.Context(), "proj-1", xml)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if id != "def-1" {
		t.Errorf("id = %q, want def-1", id)
	}

	body := f.last().Body
	if body["project_id"] != "proj-1" {
		t.Errorf("project_id = %v, want proj-1", body["project_id"])
	}
	// []byte marshals to base64; the server decodes exactly that.
	if body["xml"] != base64.StdEncoding.EncodeToString(xml) {
		t.Errorf("xml was not base64-encoded: %v", body["xml"])
	}
}

func TestImportDefinitionRefusesNoProject(t *testing.T) {
	_, client := newFakeServer(t)
	if _, err := client.ImportDefinition(t.Context(), "", []byte("<x/>")); err == nil {
		t.Fatal("an import with no project must be refused client-side, not deployed invisibly")
	}
}

func TestStartProcessAndCompleteTaskContract(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"inst-1"}`)
	f.respond("POST /api/v1/tasks/task-1/complete", 200, `{}`)

	id, err := client.StartProcess(t.Context(), "proj-1", "invoice", Variables{"amount": 900})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if id != "inst-1" {
		t.Errorf("instance = %q, want inst-1", id)
	}
	if body := f.last().Body; body["definition_key"] != "invoice" {
		t.Errorf("definition_key = %v", body["definition_key"])
	}

	if err := client.CompleteTask(t.Context(), "task-1", Variables{"approved": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	last := f.last()
	if last.Path != "/api/v1/tasks/task-1/complete" {
		t.Errorf("path = %q", last.Path)
	}
	vars, ok := last.Body["variables"].(map[string]any)
	if !ok {
		t.Fatalf("variables missing from body: %#v", last.Body)
	}
	if vars["approved"] != true {
		t.Errorf("approved = %v", vars["approved"])
	}
	// The acting user is the token, never the body. Sending a user_id here
	// would be a lie the server ignores: it reads the principal from the
	// Authorization header and overrides whatever the body claims.
	if _, sent := last.Body["user_id"]; sent {
		t.Errorf("body carries user_id, which the server ignores: %#v", last.Body)
	}
}

func TestServerErrorsBecomeAPIErrors(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 401, `{"error":"token expired"}`)

	_, err := client.StartProcess(t.Context(), "p", "k", nil)
	if err == nil {
		t.Fatal("a 401 must surface as an error")
	}
	if !IsUnauthorized(err) {
		t.Errorf("IsUnauthorized(%v) = false, want true", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "token expired" {
		t.Errorf("err = %v, want the server's message carried through", err)
	}
}

func TestFetchAndLockContract(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/external-tasks/fetch-and-lock", 200,
		`{"tasks":[{"id":"et-1","topic":"charge-card","retries":3,"variables":{"amount":42}}]}`)

	tasks, err := client.FetchAndLock(t.Context(), "charge-card", "worker-1", 5, 60_000_000_000)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "et-1" || tasks[0].Variables["amount"] != float64(42) {
		t.Fatalf("tasks = %+v", tasks)
	}

	body := f.last().Body
	if body["topic"] != "charge-card" || body["worker_id"] != "worker-1" {
		t.Errorf("body = %v", body)
	}
	// The unit must be milliseconds on the wire — this codebase has already
	// paid once for an ambiguous lock_duration.
	if body["lock_duration_ms"] != float64(60_000) {
		t.Errorf("lock_duration_ms = %v, want 60000", body["lock_duration_ms"])
	}
}

// TestEmbeddedErrorsAreNotSilent pins the one endpoint family that reports
// failures inside a 200 body instead of a status code.
func TestEmbeddedErrorsAreNotSilent(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/external-tasks/et-1/complete", 200, `{"error":"lock for task et-1 has expired"}`)

	err := client.CompleteExternalTask(t.Context(), "et-1", "worker-1", nil)
	if err == nil {
		t.Fatal("an embedded error in a 200 body was swallowed — the worker would believe the task completed")
	}
}
