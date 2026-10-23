package metis

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStartProcessSendsABusinessKeyAndAVersion(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i-1"}`)

	id, err := client.StartProcess(t.Context(), "p-1", "refund", Variables{"amount": 10},
		WithBusinessKey("ORDER-4411"), WithVersion(3), nil)
	if err != nil || id != "i-1" {
		t.Fatalf("StartProcess = %q, %v", id, err)
	}
	body := f.last().Body
	if body["business_key"] != "ORDER-4411" {
		t.Errorf("business_key = %#v", body["business_key"])
	}
	if body["version"] != float64(3) {
		t.Errorf("version = %#v", body["version"])
	}
}

// An older server ignores fields it does not know, but sending an empty key or
// a zero version would still say something nobody asked.
func TestStartProcessSendsNothingItWasNotGiven(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i-1"}`)

	if _, err := client.StartProcess(t.Context(), "p-1", "refund", nil, WithBusinessKey(""), WithVersion(0)); err != nil {
		t.Fatal(err)
	}
	body := f.last().Body
	for _, field := range []string{"business_key", "version"} {
		if _, sent := body[field]; sent {
			t.Errorf("%s was sent: %#v", field, body)
		}
	}
}

func TestListInstancesSendsEveryFilter(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200,
		`{"instances":[{"id":"i-1","business_key":"PO-1","status":"failed"}],"page":{"total":1}}`)

	instances, _, err := client.ListInstances(t.Context(), ListInstancesOptions{
		ProjectID: "p-1", BusinessKey: "PO-1", Status: ProcessFailed,
		DefinitionID: "d-1", NeedsAttention: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].BusinessKey != "PO-1" {
		t.Errorf("instances = %+v", instances)
	}
	query := f.last().Query
	for name, want := range map[string]string{
		"business_key": "PO-1", "status": "failed", "definition_id": "d-1", "needs_attention": "true",
	} {
		if got := query.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// A server that predates business_key lists every instance, which looks like
// an answer. The instance without the key is the proof it was not filtered.
func TestListInstancesRefusesAnUnfilteredAnswer(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances":[{"id":"i-9","status":"active"}]}`)

	_, _, err := client.ListInstances(t.Context(), ListInstancesOptions{ProjectID: "p-1", BusinessKey: "PO-1"})
	if !errors.Is(err, ErrFilterUnsupported) {
		t.Fatalf("err = %v, want ErrFilterUnsupported", err)
	}
	if !strings.Contains(err.Error(), "i-9") {
		t.Errorf("the error does not name the instance that gave it away: %v", err)
	}
}

func TestListInstancesWithoutABusinessKeyChecksNothing(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances":[{"id":"i-1"},{"id":"i-2","business_key":"PO-2"}]}`)

	instances, _, err := client.ListInstances(t.Context(), ListInstancesOptions{ProjectID: "p-1"})
	if err != nil || len(instances) != 2 {
		t.Fatalf("ListInstances = %d, %v", len(instances), err)
	}
	if _, sent := f.last().Query["needs_attention"]; sent {
		t.Error("needs_attention=false was sent")
	}
}

func TestFetchAndLockSendsAWorkerVersion(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/external-tasks/fetch-and-lock", 200, `{"tasks":[]}`)

	if _, err := client.FetchAndLock(t.Context(), "charge", "w-1", 1, time.Minute, WithWorkerVersion("billing 2.4.1")); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Body["worker_version"]; got != "billing 2.4.1" {
		t.Errorf("worker_version = %#v", got)
	}

	if _, err := client.FetchAndLock(t.Context(), "charge", "w-1", 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, sent := f.last().Body["worker_version"]; sent {
		t.Error("a version was sent that nobody gave")
	}
}

// The server refuses the whole fetch over a version it will not record, so a
// worker that sent one would never get work.
func TestAWorkerVersionIsCutToWhatTheServerKeeps(t *testing.T) {
	cases := map[string]string{
		"build 7\n":                "build 7",
		"a\tb":                     "ab",
		strings.Repeat("v", 70):    strings.Repeat("v", 64),
		strings.Repeat("é", 70):    strings.Repeat("é", 64),
		"  ":                       "",
		"metis-sdk/v0.3.0":         "metis-sdk/v0.3.0",
		"\x00\x01":                 "",
		"line one\r\nline two\x7f": "line oneline two",
	}
	for in, want := range cases {
		if got := cleanWorkerVersion(in); got != want {
			t.Errorf("cleanWorkerVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAWorkerSaysWhatItIs(t *testing.T) {
	if got := (WorkerOptions{}).withDefaults().Version; !strings.HasPrefix(got, "metis-sdk") {
		t.Errorf("default version = %q", got)
	}
	if got := (WorkerOptions{Version: "billing 2.4.1"}).withDefaults().Version; got != "billing 2.4.1" {
		t.Errorf("version = %q, want the one given", got)
	}
}
