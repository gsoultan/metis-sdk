package metis

import "testing"

// Once a client is signed in as a person, the task calls a User makes go to
// the People routes, so an app has one set of calls for both.
func TestTaskCallsWorkAsThePersonSignedIn(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/people/login", 200, `{"person":{"id":"p-1","username":"ana"},"token":"pt","expires_at":"2026-10-29T10:00:00Z"}`)
	f.respond("GET /api/v1/people/tasks/t-1", 200, `{"task":{"id":"t-1","status":"claimed"}}`)
	f.respond("POST /api/v1/people/tasks/t-1/claim", 200, `{}`)
	f.respond("POST /api/v1/people/tasks/t-1/release", 200, `{}`)
	f.respond("POST /api/v1/people/tasks/t-1/complete", 200, `{}`)
	f.respond("GET /api/v1/people/tasks", 200, `{"tasks":[{"id":"t-1","status":"claimed"},{"id":"t-2","status":"completed"}],"has_more":false}`)

	if _, err := client.LoginPerson(t.Context(), "proj-1", "ana", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if task, err := client.GetTask(t.Context(), "t-1"); err != nil || task.ID != "t-1" {
		t.Fatalf("get: %+v, %v", task, err)
	}
	for name, call := range map[string]func() error{
		"/api/v1/people/tasks/t-1/claim":    func() error { return client.ClaimTask(t.Context(), "t-1") },
		"/api/v1/people/tasks/t-1/release":  func() error { return client.UnclaimTask(t.Context(), "t-1") },
		"/api/v1/people/tasks/t-1/complete": func() error { return client.CompleteTask(t.Context(), "t-1", Variables{"ok": true}) },
	} {
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := f.last().Path; got != name {
			t.Errorf("sent %s, want %s", got, name)
		}
	}

	inbox, err := client.Inbox(t.Context())
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(inbox) != 1 || inbox[0].ID != "t-1" {
		t.Errorf("inbox = %+v, want only the open task", inbox)
	}

	before := f.count()
	if err := client.UnclaimTask(t.Context(), "t-1", WithReason("away")); err == nil {
		t.Error("a person's release with a reason was sent")
	}
	if f.count() != before {
		t.Error("a refused release still asked the server")
	}
}

// A person's API key is said to be one, and a User's sign-in afterwards
// puts the client back on the User routes.
func TestPersonTokenAndLoginSwitchRoutes(t *testing.T) {
	f, client := newFakeServer(t)
	WithPersonToken("key")(client)
	f.respond("POST /api/v1/people/tasks/t-1/claim", 200, `{}`)
	f.respond("POST /api/v1/tasks/t-1/claim", 200, `{}`)
	f.respond("POST /api/v1/login", 200, `{"token":"ut","user":{"id":"u-1","username":"dana"}}`)

	if err := client.ClaimTask(t.Context(), "t-1"); err != nil {
		t.Fatalf("claim as person: %v", err)
	}
	if got := f.last().Path; got != "/api/v1/people/tasks/t-1/claim" {
		t.Errorf("with a person's key, claim went to %s", got)
	}
	if got := f.last().Auth; got != "Bearer key" {
		t.Errorf("Authorization = %q", got)
	}

	if err := client.Login(t.Context(), "dana", "pw"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := client.ClaimTask(t.Context(), "t-1"); err != nil {
		t.Fatalf("claim as user: %v", err)
	}
	if got := f.last().Path; got != "/api/v1/tasks/t-1/claim" {
		t.Errorf("after a User's login, claim went to %s", got)
	}
}
