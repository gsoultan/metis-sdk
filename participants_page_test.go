package metis

import "testing"

// A page is asked for by number, size and search, and the window the server
// served comes back.
func TestListParticipantsPageAsksForOnePage(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/participants", 200, `{"participants":[{"id":"p-3","username":"carol","active":true}],
		"page":{"total":5,"page":2,"page_size":2,"has_more":true}}`)

	people, page, err := client.ListParticipantsPage(t.Context(), ListParticipantsOptions{
		ProjectID: "proj-1", Search: "smith", Page: 2, PageSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	query := f.last().Query
	if query.Get("project_id") != "proj-1" || query.Get("page") != "2" || query.Get("page_size") != "2" || query.Get("q") != "smith" {
		t.Errorf("sent ?%s", query.Encode())
	}
	if len(people) != 1 || people[0].Username != "carol" {
		t.Errorf("people = %+v", people)
	}
	if page == nil || page.Total != 5 || page.Page != 2 || !page.HasMore {
		t.Errorf("page = %+v", page)
	}
}

// The zero options still ask for a page, so the answer is bounded and counted
// rather than the whole directory.
func TestListParticipantsPageAsksForTheFirstPageByDefault(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/participants", 200, `{"participants":[],"page":{"total":0,"page":1,"page_size":50}}`)

	if _, _, err := client.ListParticipantsPage(t.Context(), ListParticipantsOptions{ProjectID: "proj-1"}); err != nil {
		t.Fatal(err)
	}
	query := f.last().Query
	if query.Get("page") != "1" || query.Has("page_size") || query.Has("q") {
		t.Errorf("sent ?%s", query.Encode())
	}
}

// An older server answers everybody and no page: the page is nil.
func TestListParticipantsPageFromAnOlderServer(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/participants", 200, `{"participants":[{"id":"p-1","username":"ada"},{"id":"p-2","username":"bob"}]}`)

	people, page, err := client.ListParticipantsPage(t.Context(), ListParticipantsOptions{ProjectID: "proj-1", PageSize: 1})
	if err != nil || len(people) != 2 || page != nil {
		t.Fatalf("people = %+v, page = %+v, err = %v", people, page, err)
	}
}

func TestListParticipantsPageNeedsAProject(t *testing.T) {
	_, client := newFakeServer(t)
	if _, _, err := client.ListParticipantsPage(t.Context(), ListParticipantsOptions{}); err == nil {
		t.Error("no error without a project")
	}
}
