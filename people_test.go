package metis

import (
	"testing"
	"time"
)

// Signing a person in stores their token, and every later call carries it,
// as Login does for a User.
func TestLoginPersonStoresTheToken(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/people/login", 200, `{
		"person":{"id":"p-1","project_id":"proj-1","username":"ana","display_name":"Ana"},
		"token":"person-token","expires_at":"2026-10-29T10:00:00Z"
	}`)
	f.respond("GET /api/v1/people/me", 200, `{"person":{"id":"p-1","project_id":"proj-1","username":"ana"}}`)

	session, err := client.LoginPerson(t.Context(), "proj-1", "ana", "secret")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if session.Person.Username != "ana" || session.Person.ProjectID != "proj-1" {
		t.Errorf("person = %+v", session.Person)
	}
	if !session.ExpiresAt.Equal(time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("expires = %v", session.ExpiresAt)
	}
	body := f.last().Body
	if body["project_id"] != "proj-1" || body["username"] != "ana" || body["password"] != "secret" {
		t.Errorf("sign-in sent %+v", body)
	}

	me, err := client.CurrentPerson(t.Context())
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	if me.ID != "p-1" {
		t.Errorf("me = %+v", me)
	}
	if got := f.last().Auth; got != "Bearer person-token" {
		t.Errorf("Authorization = %q, want the person's token", got)
	}
}

func TestLoginPersonRefusals(t *testing.T) {
	t.Run("no project is refused without asking", func(t *testing.T) {
		f, client := newFakeServer(t)
		if _, err := client.LoginPerson(t.Context(), "", "ana", "secret"); err == nil {
			t.Fatal("a sign-in with no project was accepted")
		}
		if f.count() != 0 {
			t.Errorf("sent %d requests", f.count())
		}
	})
	t.Run("an answer without a token keeps the old one", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("POST /api/v1/people/login", 200, `{"person":{"id":"p-1"}}`)
		if _, err := client.LoginPerson(t.Context(), "proj-1", "ana", "secret"); err == nil {
			t.Fatal("a sign-in without a token was taken for success")
		}
		f.respond("GET /api/v1/people/me", 200, `{"person":{}}`)
		_, _ = client.CurrentPerson(t.Context())
		if got := f.last().Auth; got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want the token the client had", got)
		}
	})
	t.Run("a refused password is unauthorized", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("POST /api/v1/people/login", 401, `{"error":"the username or password is wrong"}`)
		_, err := client.LoginPerson(t.Context(), "proj-1", "ana", "wrong")
		if !IsUnauthorized(err) {
			t.Fatalf("err = %v", err)
		}
	})
}

// The person's listing writes its page beside the tasks, not under "page".
func TestListPersonTasks(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/people/tasks", 200, `{
		"tasks":[{"id":"t-1","name":"Sign the contract","status":"unclaimed","type":"userTask"}],
		"total":21,"page":2,"page_size":10,"has_more":true
	}`)

	tasks, page, err := client.ListPersonTasks(t.Context(), PageOptions{Page: 2, PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "t-1" || tasks[0].Status != TaskUnclaimed {
		t.Fatalf("tasks = %+v", tasks)
	}
	if page.Total != 21 || page.Page != 2 || page.PageSize != 10 || !page.HasMore {
		t.Errorf("page = %+v", page)
	}
	if q := f.last().Query; q.Get("page") != "2" || q.Get("page_size") != "10" {
		t.Errorf("query = %v", q)
	}
}

func TestPersonTaskCalls(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/people/tasks/t-1", 200, `{"task":{"id":"t-1","name":"Sign","status":"claimed"}}`)
	f.respond("POST /api/v1/people/tasks/t-1/claim", 200, `{}`)
	f.respond("POST /api/v1/people/tasks/t-1/release", 200, `{}`)
	f.respond("POST /api/v1/people/tasks/t-1/complete", 200, `{}`)

	task, err := client.GetPersonTask(t.Context(), "t-1")
	if err != nil || task.ID != "t-1" {
		t.Fatalf("get: %+v, %v", task, err)
	}

	if err := client.ClaimPersonTask(t.Context(), "t-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if last := f.last(); last.Method != "POST" || last.Path != "/api/v1/people/tasks/t-1/claim" {
		t.Errorf("claim sent %s %s", last.Method, last.Path)
	}
	if err := client.ReleasePersonTask(t.Context(), "t-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := f.last().Path; got != "/api/v1/people/tasks/t-1/release" {
		t.Errorf("release path = %q", got)
	}

	if err := client.CompletePersonTask(t.Context(), "t-1", Variables{"signed": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	vars, ok := f.last().Body["variables"].(map[string]any)
	if !ok || vars["signed"] != true {
		t.Errorf("complete sent %+v", f.last().Body)
	}
	// Nobody is named in the body: who completes is the token.
	for _, field := range []string{"user_id", "username", "person_id"} {
		if _, sent := f.last().Body[field]; sent {
			t.Errorf("complete named somebody: %+v", f.last().Body)
		}
	}

	before := f.count()
	for name, call := range map[string]func() error{
		"get":      func() error { _, err := client.GetPersonTask(t.Context(), ""); return err },
		"claim":    func() error { return client.ClaimPersonTask(t.Context(), "") },
		"release":  func() error { return client.ReleasePersonTask(t.Context(), "") },
		"complete": func() error { return client.CompletePersonTask(t.Context(), "", nil) },
	} {
		if call() == nil {
			t.Errorf("%s with no task ID was accepted", name)
		}
	}
	if f.count() != before {
		t.Errorf("sent %d requests for calls without a task", f.count()-before)
	}
}

func TestPersonAPIKeys(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/people/keys", 200, `{
		"key":{"id":"k-1","name":"payroll","prefix":"mpk_ab12","created_at":"2026-10-28T09:00:00Z"},
		"token":"mpk_ab12.secret"
	}`)
	f.respond("GET /api/v1/people/keys", 200, `{"keys":[
		{"id":"k-1","name":"payroll","prefix":"mpk_ab12","created_at":"2026-10-28T09:00:00Z"},
		{"id":"k-0","name":"old","prefix":"mpk_zz99","created_at":"2026-10-01T09:00:00Z","revoked_at":"2026-10-20T09:00:00Z"}
	]}`)
	f.respond("DELETE /api/v1/people/keys/k-1", 200, `{}`)

	key, token, err := client.CreatePersonAPIKey(t.Context(), "payroll")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if key.ID != "k-1" || token != "mpk_ab12.secret" {
		t.Errorf("key = %+v, token = %q", key, token)
	}
	if f.last().Body["name"] != "payroll" {
		t.Errorf("create sent %+v", f.last().Body)
	}

	keys, err := client.ListPersonAPIKeys(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 2 || keys[0].Revoked() || !keys[1].Revoked() {
		t.Errorf("keys = %+v", keys)
	}

	if err := client.RevokePersonAPIKey(t.Context(), "k-1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if last := f.last(); last.Method != "DELETE" || last.Path != "/api/v1/people/keys/k-1" {
		t.Errorf("revoke sent %s %s", last.Method, last.Path)
	}

	// A key works the person's tasks; it is only a token to the client.
	keyed := NewClient(client.baseURL, WithToken(token))
	f.respond("GET /api/v1/people/tasks", 200, `{"tasks":[]}`)
	if _, _, err := keyed.ListPersonTasks(t.Context(), PageOptions{}); err != nil {
		t.Fatalf("list with a key: %v", err)
	}
	if got := f.last().Auth; got != "Bearer "+token {
		t.Errorf("Authorization = %q, want the key", got)
	}

	before := f.count()
	if _, _, err := client.CreatePersonAPIKey(t.Context(), "  "); err == nil {
		t.Error("a key with no name was accepted")
	}
	if err := client.RevokePersonAPIKey(t.Context(), ""); err == nil {
		t.Error("revoking no key was accepted")
	}
	if f.count() != before {
		t.Errorf("sent %d requests for refused calls", f.count()-before)
	}
}

// The link's token travels in the body, never the address, which access logs
// keep.
func TestTaskLinks(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/people/links/read", 200, `{
		"task":{"id":"t-1","name":"Approve","status":"unclaimed"},
		"person":{"id":"p-1","username":"ana"},
		"expires_at":"2026-11-04T09:00:00Z"
	}`)
	f.respond("POST /api/v1/people/links/complete", 200, `{}`)

	link, err := client.ReadTaskLink(t.Context(), "link-token")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if link.Task.ID != "t-1" || link.Person.Username != "ana" || link.ExpiresAt.IsZero() {
		t.Errorf("link = %+v", link)
	}
	last := f.last()
	if last.Body["token"] != "link-token" || len(last.Query) != 0 {
		t.Errorf("read sent body %+v, query %v", last.Body, last.Query)
	}

	if err := client.CompleteByTaskLink(t.Context(), "link-token", Variables{"approved": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	body := f.last().Body
	vars, _ := body["variables"].(map[string]any)
	if body["token"] != "link-token" || vars["approved"] != true {
		t.Errorf("complete sent %+v", body)
	}

	before := f.count()
	if _, err := client.ReadTaskLink(t.Context(), ""); err == nil {
		t.Error("reading no link was accepted")
	}
	if err := client.CompleteByTaskLink(t.Context(), "", nil); err == nil {
		t.Error("completing no link was accepted")
	}
	if f.count() != before {
		t.Errorf("sent %d requests for refused calls", f.count()-before)
	}
}
