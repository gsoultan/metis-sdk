package metis

import (
	"net/http"
	"testing"
)

// An import sends the source, its mapping and the rename rule, and reads the
// summary back.
func TestImportPeopleSendsTheSourceAndItsMapping(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/participants/import", 200, `{"created":1,"updated":2,"unchanged":3,"renamed":1,"tasks_moved":2,"groups":0,
		"problems":[{"line":4,"username":"alan.t","reason":"\"alan.t\" already belongs to external ID \"u-1\""}]}`)

	got, err := client.ImportPeople(t.Context(), "proj-1", PeopleSource{
		Kind: "http", URL: "https://app.example.com/users",
		Headers:       map[string]string{"Authorization": "Bearer x"},
		Mapping:       map[string]string{"external_id": "id", "username": "login"},
		MoveOpenTasks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	last := f.last()
	mapping, _ := last.Body["mapping"].(map[string]any)
	if last.Method != http.MethodPost || last.Body["project_id"] != "proj-1" || last.Body["kind"] != "http" ||
		mapping["external_id"] != "id" || last.Body["move_open_tasks"] != true {
		t.Errorf("body = %v", last.Body)
	}
	if got.Created != 1 || got.Renamed != 1 || got.TasksMoved != 2 || len(got.Problems) != 1 || got.Problems[0].Line != 4 {
		t.Errorf("summary = %+v", got)
	}
}

// A check reads back each row's outcome.
func TestCheckPeopleImportReadsEachRow(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/participants/import/check", 200, `{"read":2,"counts":{"new":1,"renamed":1},
		"fields":["id","login"],"rows":[{"line":1,"outcome":"renamed","username":"m.hamilton","external_id":"u-2","detail":"was \"margaret\"","open_tasks":3}]}`)

	got, err := client.CheckPeopleImport(t.Context(), "proj-1", PeopleSource{Kind: "http", URL: "https://app.example.com/users"})
	if err != nil {
		t.Fatal(err)
	}
	if f.last().Path != "/api/v1/participants/import/check" {
		t.Errorf("sent to %s", f.last().Path)
	}
	if got.Read != 2 || got.Counts[ImportRenamed] != 1 || got.Rows[0].OpenTasks != 3 || got.Rows[0].Outcome != ImportRenamed {
		t.Errorf("check = %+v", got)
	}
}

// Finding by external ID asks the server for that id, and answers false for
// nobody — and for a server too old to filter, which answers everybody.
func TestFindParticipantByExternalID(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/participants", 200, `{"participants":[{"id":"p-1","username":"ada","external_id":"u-7","active":true}]}`)

	person, found, err := client.FindParticipantByExternalID(t.Context(), "proj-1", "u-7")
	if err != nil || !found || person.Username != "ada" {
		t.Fatalf("u-7 should be ada: %+v %v %v", person, found, err)
	}
	if q := f.last().Query; q.Get("external_id") != "u-7" || q.Get("project_id") != "proj-1" {
		t.Errorf("query = %v", q.Encode())
	}

	if _, found, err := client.FindParticipantByExternalID(t.Context(), "proj-1", "u-8"); err != nil || found {
		t.Fatalf("u-8 is nobody: %v %v", found, err)
	}
}
