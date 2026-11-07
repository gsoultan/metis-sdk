package metis

import "testing"

// A Project view sends its own ID on every call, so a caller gives it once.
func TestProjectCarriesItsID(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i-1"}`)
	f.respond("GET /api/v1/definitions/versions", 200, `{"versions":[]}`)
	f.respond("PUT /api/v1/participants/p-1/active", 200, `{"result":{"moved":0}}`)

	p := client.Project("proj-1")
	if p.ID() != "proj-1" {
		t.Errorf("ID = %q", p.ID())
	}
	if _, err := p.Start(t.Context(), "expense", Variables{"amount": 1}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if body := f.last().Body; body["project_id"] != "proj-1" {
		t.Errorf("start sent %v", body)
	}
	if _, err := p.Versions(t.Context(), "expense"); err != nil {
		t.Fatalf("versions: %v", err)
	}
	if q := f.last().Query; q.Get("project_id") != "proj-1" || q.Get("key") != "expense" {
		t.Errorf("versions sent %v", q)
	}

	if _, err := p.People().Deactivate(t.Context(), "p-1", &HeldWork{Do: HeldWorkRelease, Reason: "left"}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if last := f.last(); last.Query.Get("project_id") != "proj-1" || last.Body["active"] != false {
		t.Errorf("deactivate sent %v %v", last.Query, last.Body)
	}
	if err := p.People().Activate(t.Context(), "p-1"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if body := f.last().Body; body["active"] != true || body["work"] != nil {
		t.Errorf("activate sent %v", body)
	}
	if got := p.Approvals(); got.project != "proj-1" {
		t.Errorf("approvals project = %q", got.project)
	}
}
