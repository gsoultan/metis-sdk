package metis

import "testing"

// Without this, a definition ID could only come from ImportDefinition — so
// ListUserTaskNodes and ExportDefinition were reachable only for a process you
// had just deployed yourself, in the same program.
func TestListDefinitions(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/definitions", 200, `{
		"page": {"total": 3, "page": 1, "page_size": 50, "has_more": false},
		"definitions": [
			{"id":"def-3","key":"refund","name":"Refund a customer","version":3,
			 "created_at":"2026-09-07T12:00:00Z"},
			{"id":"def-2","key":"refund","name":"Refund a customer","version":2,
			 "created_at":"2026-09-06T12:00:00Z"},
			{"id":"def-1","key":"invoice","name":"Invoice approval","version":1,
			 "created_at":"2026-09-05T12:00:00Z"}
		]
	}`)

	definitions, page, err := client.ListDefinitions(t.Context(), ListDefinitionsOptions{
		ProjectID: "p-1", PageSize: 50,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(definitions) != 3 {
		t.Fatalf("got %d definitions", len(definitions))
	}

	// Every version of every process is an entry: two of these share a key and
	// differ only by version, which is the shape callers have to expect.
	if definitions[0].Key != definitions[1].Key {
		t.Errorf("expected two versions of one key, got %q and %q", definitions[0].Key, definitions[1].Key)
	}
	if definitions[0].Version != 3 || definitions[1].Version != 2 {
		t.Errorf("versions = %d, %d", definitions[0].Version, definitions[1].Version)
	}
	if definitions[0].ID == definitions[1].ID {
		t.Error("two versions shared an ID; the ID names a version, not a process")
	}
	if definitions[0].CreatedAt.IsZero() {
		t.Error("created_at did not decode")
	}
	if page == nil || page.Total != 3 {
		t.Errorf("page = %+v", page)
	}

	query := f.last().Query
	if query.Get("project_id") != "p-1" || query.Get("page_size") != "50" {
		t.Errorf("query = %v", query)
	}
}

// The project filter is optional here, unlike the instance listing's, so an
// empty one must not be refused locally — it means "the whole organization".
func TestListDefinitionsWithoutAProjectListsEverything(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/definitions", 200, `{"definitions":[]}`)

	if _, _, err := client.ListDefinitions(t.Context(), ListDefinitionsOptions{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, sent := f.last().Query["project_id"]; sent {
		t.Error("sent an empty project_id rather than omitting it")
	}
}

// A listing never carries the graph, whatever the diagram holds — the server
// selects scalar columns. Callers reaching for Nodes here get nothing, and the
// doc points them at ListDefinitionNodes.
func TestListDefinitionsCarriesNoNodes(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/definitions", 200,
		`{"definitions":[{"id":"def-1","key":"refund","name":"Refund","version":1}]}`)

	definitions, _, err := client.ListDefinitions(t.Context(), ListDefinitionsOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(definitions[0].Nodes) != 0 {
		t.Errorf("a listing carried %d nodes", len(definitions[0].Nodes))
	}
}
