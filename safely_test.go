package metis

import (
	"errors"
	"strings"
	"testing"
)

func signedInAs(f *fakeServer, username string) {
	f.respond("GET /api/v1/users/me", 200, `{"user":{"id":"u-`+username+`","username":"`+username+`"}}`)
}

// sent counts the requests made to path with method.
func sent(f *fakeServer, method, path string) int {
	n := 0
	for _, r := range f.snapshot() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func TestCurrentUserIsReadOnce(t *testing.T) {
	f, client := newFakeServer(t)
	signedInAs(f, "ann")
	for range 2 {
		me, err := client.CurrentUser(t.Context())
		if err != nil || me.Username != "ann" {
			t.Fatalf("me = %v, %v; want ann", me, err)
		}
	}
	if n := f.count(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestCurrentUserComesFromLogin(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/login", 200, `{"token":"t","user":{"id":"u1","username":"bob"}}`)
	signedInAs(f, "someone-else")
	if err := client.Login(t.Context(), "bob", "pw"); err != nil {
		t.Fatal(err)
	}
	me, err := client.CurrentUser(t.Context())
	if err != nil || me.Username != "bob" {
		t.Errorf("me = %v, %v; want bob from the login", me, err)
	}
	if n := sent(f, "GET", "/api/v1/users/me"); n != 0 {
		t.Errorf("users/me read %d times, want none", n)
	}
}

func TestCurrentUserFailures(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/users/me", 200, `{"user":{}}`)
	if _, err := client.CurrentUser(t.Context()); err == nil {
		t.Error("an answer without a username was accepted")
	}
	f2, client2 := newFakeServer(t)
	f2.respond("GET /api/v1/users/me", 404, `{"error":"not found"}`)
	if _, err := client2.CurrentUser(t.Context()); !IsNotFound(err) {
		t.Errorf("err = %v, want 404 from an older server", err)
	}
}

func TestCompleteTaskSafely(t *testing.T) {
	const task = "/api/v1/tasks/t1"
	cases := []struct {
		name      string
		reads     []reply
		complete  reply
		completes int
		want      error
	}{
		{
			name:      "mine and open",
			reads:     []reply{ok(`{"task":{"id":"t1","status":"claimed","assignee":{"username":"ann"}}}`)},
			complete:  ok(`{}`),
			completes: 1,
		},
		{
			name:      "unclaimed, ann a candidate",
			reads:     []reply{ok(`{"task":{"id":"t1","status":"unclaimed"}}`)},
			complete:  ok(`{}`),
			completes: 1,
		},
		{
			name:  "already done by ann",
			reads: []reply{ok(`{"task":{"id":"t1","status":"completed","assignee":{"username":"ann"}}}`)},
		},
		{
			name:  "held by bob",
			reads: []reply{ok(`{"task":{"id":"t1","status":"claimed","assignee":{"username":"bob"}}}`)},
			want:  ErrTaskTakenByOther,
		},
		{
			name:  "withdrawn",
			reads: []reply{ok(`{"task":{"id":"t1","status":"canceled"}}`)},
			want:  ErrTaskClosed,
		},
		{
			name: "answer lost, but it went through",
			reads: []reply{
				ok(`{"task":{"id":"t1","status":"claimed","assignee":{"username":"ann"}}}`),
				ok(`{"task":{"id":"t1","status":"completed","assignee":{"username":"ann"}}}`),
			},
			complete:  reply{502, `bad gateway`},
			completes: 1,
		},
		{
			name: "bob finished it first",
			reads: []reply{
				ok(`{"task":{"id":"t1","status":"unclaimed"}}`),
				ok(`{"task":{"id":"t1","status":"completed","assignee":{"username":"bob"}}}`),
			},
			complete:  reply{400, `{"error":"this task is already completed"}`},
			completes: 1,
			want:      ErrTaskTakenByOther,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, client := newFakeServer(t)
			signedInAs(f, "ann")
			replies(f, "GET "+task, tc.reads...)
			if tc.complete.status != 0 {
				replies(f, "POST "+task+"/complete", tc.complete)
			}
			err := client.CompleteTaskSafely(t.Context(), "t1", Variables{"ok": true})
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if n := sent(f, "POST", task+"/complete"); n != tc.completes {
				t.Errorf("completions sent = %d, want %d", n, tc.completes)
			}
			if conflict, isConflict := errors.AsType[*TaskConflictError](err); isConflict && !strings.Contains(conflict.Error(), "t1") {
				t.Errorf("message %q does not name the task", conflict.Error())
			}
		})
	}
}

func TestCompleteTaskSafelyHandsBackWhatItCannotSettle(t *testing.T) {
	f, client := newFakeServer(t)
	signedInAs(f, "ann")
	f.respond("GET /api/v1/tasks/t1", 200, `{"task":{"id":"t1","status":"claimed","assignee":{"username":"ann"}}}`)
	f.respond("POST /api/v1/tasks/t1/complete", 503, `{"error":"down"}`)
	if err := client.CompleteTaskSafely(t.Context(), "t1", nil); !IsServerError(err) {
		t.Errorf("still open after a 503: err = %v, want the 503 to try again", err)
	}

	f.respond("POST /api/v1/tasks/t2/complete", 400, `{"error":"a form field is missing"}`)
	f.respond("GET /api/v1/tasks/t2", 200, `{"task":{"id":"t2","status":"claimed","assignee":{"username":"ann"}}}`)
	if err := client.CompleteTaskSafely(t.Context(), "t2", nil); !IsInvalid(err) {
		t.Errorf("err = %v, want the 400 as it came", err)
	}

	f.respond("GET /api/v1/tasks/t3", 404, `{"error":"no such task"}`)
	if err := client.CompleteTaskSafely(t.Context(), "t3", nil); !IsNotFound(err) {
		t.Errorf("err = %v, want 404", err)
	}
	if err := client.CompleteTaskSafely(t.Context(), "", nil); err == nil {
		t.Error("an empty ID was accepted")
	}
}

func TestClaimAndComplete(t *testing.T) {
	f, client := newFakeServer(t)
	signedInAs(f, "ann")
	replies(f, "GET /api/v1/tasks/t1",
		ok(`{"task":{"id":"t1","status":"unclaimed"}}`),
		ok(`{"task":{"id":"t1","status":"claimed","assignee":{"username":"ann"}}}`),
	)
	f.respond("POST /api/v1/tasks/t1/claim", 200, `{}`)
	f.respond("POST /api/v1/tasks/t1/complete", 200, `{}`)

	if err := client.ClaimAndComplete(t.Context(), "t1", Variables{"approved": true}); err != nil {
		t.Fatal(err)
	}
	if sent(f, "POST", "/api/v1/tasks/t1/claim") != 1 || sent(f, "POST", "/api/v1/tasks/t1/complete") != 1 {
		t.Errorf("want one claim and one completion, got %v", f.snapshot())
	}
}

func TestClaimAndCompleteSkipsTheClaimOfAHeldTask(t *testing.T) {
	f, client := newFakeServer(t)
	signedInAs(f, "ann")
	f.respond("GET /api/v1/tasks/t1", 200, `{"task":{"id":"t1","status":"claimed","assignee":{"username":"ann"}}}`)
	f.respond("POST /api/v1/tasks/t1/complete", 200, `{}`)
	if err := client.ClaimAndComplete(t.Context(), "t1", nil); err != nil {
		t.Fatal(err)
	}
	if n := sent(f, "POST", "/api/v1/tasks/t1/claim"); n != 0 {
		t.Errorf("claims = %d, want none", n)
	}
}

func TestClaimAndCompleteLosingTheRace(t *testing.T) {
	f, client := newFakeServer(t)
	signedInAs(f, "ann")
	replies(f, "GET /api/v1/tasks/t1",
		ok(`{"task":{"id":"t1","status":"unclaimed"}}`),
		ok(`{"task":{"id":"t1","status":"claimed","assignee":{"username":"bob"}}}`),
	)
	f.respond("POST /api/v1/tasks/t1/claim", 400, `{"error":"somebody else has already claimed this task"}`)

	err := client.ClaimAndComplete(t.Context(), "t1", nil)
	conflict, isConflict := errors.AsType[*TaskConflictError](err)
	if !errors.Is(err, ErrTaskTakenByOther) || !isConflict || conflict.Task.AssigneeUsername() != "bob" {
		t.Errorf("err = %v, want bob named as the holder", err)
	}
	if !strings.Contains(err.Error(), "bob") {
		t.Errorf("message %q does not say who has it", err)
	}
	if n := sent(f, "POST", "/api/v1/tasks/t1/complete"); n != 0 {
		t.Errorf("completions = %d, want none", n)
	}
}

func TestClaimAndCompleteAfterALostClaimAnswer(t *testing.T) {
	f, client := newFakeServer(t)
	signedInAs(f, "ann")
	replies(f, "GET /api/v1/tasks/t1",
		ok(`{"task":{"id":"t1","status":"unclaimed"}}`),
		ok(`{"task":{"id":"t1","status":"claimed","assignee":{"username":"ann"}}}`),
	)
	f.respond("POST /api/v1/tasks/t1/claim", 504, `gateway timeout`)
	f.respond("POST /api/v1/tasks/t1/complete", 200, `{}`)
	if err := client.ClaimAndComplete(t.Context(), "t1", nil); err != nil {
		t.Errorf("err = %v, want the landed claim to be completed", err)
	}

	f2, client2 := newFakeServer(t)
	signedInAs(f2, "ann")
	f2.respond("GET /api/v1/tasks/t1", 200, `{"task":{"id":"t1","status":"unclaimed"}}`)
	f2.respond("POST /api/v1/tasks/t1/claim", 504, `gateway timeout`)
	if err := client2.ClaimAndComplete(t.Context(), "t1", nil); !IsServerError(err) {
		t.Errorf("err = %v, want the claim's 504: it did not land", err)
	}
	if err := client2.ClaimAndComplete(t.Context(), "", nil); err == nil {
		t.Error("an empty ID was accepted")
	}
}

func TestClaimAndCompleteFailsEarly(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/users/me", 401, `{"error":"no token"}`)
	if err := client.ClaimAndComplete(t.Context(), "t1", nil); !IsUnauthorized(err) {
		t.Errorf("err = %v, want 401", err)
	}
	if err := client.CompleteTaskSafely(t.Context(), "t1", nil); !IsUnauthorized(err) {
		t.Errorf("err = %v, want 401", err)
	}
	f2, client2 := newFakeServer(t)
	signedInAs(f2, "ann")
	f2.respond("GET /api/v1/tasks/t1", 404, `{"error":"gone"}`)
	if err := client2.ClaimAndComplete(t.Context(), "t1", nil); !IsNotFound(err) {
		t.Errorf("err = %v, want 404", err)
	}
	f2.respond("GET /api/v1/tasks/t2", 200, `{"task":{"id":"t2","status":"completed","assignee":{"username":"ann"}}}`)
	if err := client2.ClaimAndComplete(t.Context(), "t2", nil); err != nil {
		t.Errorf("err = %v, want nil for a task ann already finished", err)
	}
}

const orderVersions = `{"versions":[{"id":"d-order-2","key":"order","version":2},{"id":"d-order-1","key":"order","version":1}]}`

func TestStartProcessOnceStartsANewCase(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances":[],"page":{"has_more":false}}`)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i9"}`)

	inst, started, err := client.StartProcessOnce(t.Context(), "p1", "order", "ORDER-1", Variables{"total": 5})
	if err != nil || !started || inst.ID != "i9" || inst.BusinessKey != "ORDER-1" {
		t.Fatalf("inst = %+v, started = %v, err = %v; want i9 started", inst, started, err)
	}
	if got := f.last().Body["business_key"]; got != "ORDER-1" {
		t.Errorf("business_key = %v, want it sent with the start", got)
	}
}

func TestStartProcessOnceFindsTheCase(t *testing.T) {
	f, client := newFakeServer(t)
	// Newest first: a shipping instance with the same key, then the order.
	f.respond("GET /api/v1/instances", 200, `{"instances":[
		{"id":"i-ship","status":"active","business_key":"ORDER-1","definition":{"id":"d-ship-1"}},
		{"id":"i-order","status":"completed","business_key":"ORDER-1","definition":{"id":"d-order-1"}}],"page":{"has_more":false}}`)
	f.respond("GET /api/v1/definitions/versions", 200, orderVersions)

	inst, started, err := client.StartProcessOnce(t.Context(), "p1", "order", "ORDER-1", nil)
	if err != nil || started || inst.ID != "i-order" {
		t.Fatalf("inst = %+v, started = %v, err = %v; want the finished order instance", inst, started, err)
	}
	if n := sent(f, "POST", "/api/v1/process/start"); n != 0 {
		t.Errorf("starts = %d, want none", n)
	}
	if q := f.last().Query; q.Get("key") != "order" || q.Get("project_id") != "p1" {
		t.Errorf("versions query = %v", q)
	}
}

func TestStartProcessOnceIgnoresOtherProcesses(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances":[{"id":"i-ship","business_key":"ORDER-1","definition":{"id":"d-ship-1"}}],"page":{"has_more":false}}`)
	f.respond("GET /api/v1/definitions/versions", 200, orderVersions)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i-new"}`)

	inst, started, err := client.StartProcessOnce(t.Context(), "p1", "order", "ORDER-1", nil)
	if err != nil || !started || inst.ID != "i-new" {
		t.Errorf("inst = %+v, started = %v, err = %v; want a new order instance", inst, started, err)
	}
}

func TestStartProcessOnceAfterALostStartAnswer(t *testing.T) {
	f, client := newFakeServer(t)
	replies(f, "GET /api/v1/instances",
		ok(`{"instances":[],"page":{"has_more":false}}`),
		ok(`{"instances":[{"id":"i-landed","business_key":"ORDER-1","definition":{"id":"d-order-2"}}],"page":{"has_more":false}}`),
	)
	f.respond("GET /api/v1/definitions/versions", 200, orderVersions)
	f.respond("POST /api/v1/process/start", 502, `bad gateway`)

	inst, started, err := client.StartProcessOnce(t.Context(), "p1", "order", "ORDER-1", nil)
	if err != nil || !started || inst.ID != "i-landed" {
		t.Errorf("inst = %+v, started = %v, err = %v; want the instance the lost start made", inst, started, err)
	}
}

func TestStartProcessOnceFailures(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances", 200, `{"instances":[],"page":{"has_more":false}}`)
	f.respond("POST /api/v1/process/start", 503, `{"error":"down"}`)
	if _, _, err := client.StartProcessOnce(t.Context(), "p1", "order", "ORDER-1", nil); !IsServerError(err) {
		t.Errorf("err = %v, want the 503: nothing landed", err)
	}

	f2, client2 := newFakeServer(t)
	f2.respond("GET /api/v1/instances", 200, `{"instances":[],"page":{"has_more":false}}`)
	f2.respond("POST /api/v1/process/start", 400, `{"error":"no such process"}`)
	if _, _, err := client2.StartProcessOnce(t.Context(), "p1", "order", "ORDER-1", nil); !IsInvalid(err) {
		t.Errorf("err = %v, want 400", err)
	}

	f3, client3 := newFakeServer(t)
	f3.respond("GET /api/v1/instances", 403, `{"error":"not yours"}`)
	if _, _, err := client3.StartProcessOnce(t.Context(), "p1", "order", "ORDER-1", nil); !IsUnauthorized(err) {
		t.Errorf("err = %v, want 403", err)
	}
	f3.respond("GET /api/v1/definitions/versions", 404, `{"error":"no such process"}`)

	f4, client4 := newFakeServer(t)
	f4.respond("GET /api/v1/instances", 200, `{"instances":[{"id":"i1","business_key":"K","definition":{"id":"d"}}],"page":{"has_more":false}}`)
	f4.respond("GET /api/v1/definitions/versions", 404, `{"error":"no such process"}`)
	if _, _, err := client4.StartProcessOnce(t.Context(), "p1", "order", "K", nil); !IsNotFound(err) {
		t.Errorf("err = %v, want 404 from the versions", err)
	}

	for _, args := range [][3]string{{"", "order", "K"}, {"p1", "", "K"}, {"p1", "order", ""}} {
		if _, _, err := client.StartProcessOnce(t.Context(), args[0], args[1], args[2], nil); err == nil {
			t.Errorf("%q was accepted", args)
		}
	}
}
