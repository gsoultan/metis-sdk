package metis

import (
	"net/http"
	"testing"
)

// Deactivating sends what to do with the person's tasks and reads back what
// moved and what stayed.
func TestSetParticipantActiveDeactivatesAndSettlesTheirWork(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("PUT /api/v1/participants/p-1/active", 200, `{"result":{"moved":1,"kept":[
		{"task_id":"t-2","name":"Finance approval","why":"nobody-here is not an account here"}]}}`)

	res, err := client.SetParticipantActive(t.Context(), "proj-1", "p-1", false, &HeldWork{
		Do: HeldWorkHandOver, To: "sari", Reason: "Budi has left",
	})
	if err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	last := f.last()
	if last.Method != http.MethodPut || last.Query.Get("project_id") != "proj-1" {
		t.Errorf("sent %s %s?%s", last.Method, last.Path, last.Query.Encode())
	}
	work, _ := last.Body["work"].(map[string]any)
	if last.Body["active"] != false || work["do"] != "hand_over" || work["to"] != "sari" || work["reason"] != "Budi has left" {
		t.Errorf("body = %v", last.Body)
	}
	if res.Moved != 1 || len(res.Kept) != 1 || res.Kept[0].TaskID != "t-2" || res.Kept[0].Why == "" {
		t.Errorf("result = %+v", res)
	}
}

// Reactivating moves nothing, so no work is sent even when one is given.
func TestSetParticipantActiveReactivatesWithoutWork(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("PUT /api/v1/participants/p-1/active", 200, `{"result":{"moved":0}}`)
	if _, err := client.SetParticipantActive(t.Context(), "proj-1", "p-1", true, &HeldWork{Do: HeldWorkRelease}); err != nil {
		t.Fatal(err)
	}
	if body := f.last().Body; body["active"] != true || body["work"] != nil {
		t.Errorf("body = %v", body)
	}
}

// The held tasks and the People are read for one project.
func TestListHeldTasksAndParticipants(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/participants/p-1/held-tasks", 200, `{"tasks":[{"task_id":"t-1","name":"Supervisor approval","since":"2026-11-07T13:32:24Z"}]}`)
	f.respond("GET /api/v1/participants", 200, `{"participants":[{"id":"p-1","username":"budi","active":false}]}`)

	held, err := client.ListHeldTasks(t.Context(), "proj-1", "p-1")
	if err != nil || len(held) != 1 || held[0].TaskID != "t-1" || held[0].Since.IsZero() {
		t.Fatalf("held = %+v, %v", held, err)
	}
	if f.last().Query.Get("project_id") != "proj-1" {
		t.Errorf("asked for project %q", f.last().Query.Get("project_id"))
	}
	people, err := client.ListParticipants(t.Context(), "proj-1")
	if err != nil || len(people) != 1 || people[0].Username != "budi" || people[0].Active {
		t.Fatalf("people = %+v, %v", people, err)
	}
}

// Without a project and a person there is nothing to ask for.
func TestPeopleCallsNeedAProjectAndAPerson(t *testing.T) {
	_, client := newFakeServer(t)
	if _, err := client.SetParticipantActive(t.Context(), "proj-1", "", false, nil); err == nil {
		t.Error("SetParticipantActive: no error without a person")
	}
	if _, err := client.ListHeldTasks(t.Context(), "", "p-1"); err == nil {
		t.Error("ListHeldTasks: no error without a project")
	}
	if _, err := client.ListParticipants(t.Context(), ""); err == nil {
		t.Error("ListParticipants: no error without a project")
	}
}
