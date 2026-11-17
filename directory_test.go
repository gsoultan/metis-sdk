package metis

import (
	"net/http"
	"testing"
)

// EnsureAccount makes what is missing and nothing else.
func TestEnsureAccountMakesWhatIsMissing(t *testing.T) {
	f, client := newFakeServer(t)
	users := `{"users":[]}`
	groups := `{"groups":[{"id":"g-1","name":"finance"}]}`
	f.mux.HandleFunc("GET /api/v1/organizations/o-1/users", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(users)) })
	f.mux.HandleFunc("GET /api/v1/organizations/o-1/groups", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(groups)) })
	f.mux.HandleFunc("POST /api/v1/users", func(w http.ResponseWriter, _ *http.Request) {
		users = `{"users":[{"id":"u-1","username":"dewi"}]}`
		_, _ = w.Write([]byte(`{}`))
	})
	f.mux.HandleFunc("POST /api/v1/organizations/o-1/groups", func(w http.ResponseWriter, _ *http.Request) {
		groups = `{"groups":[{"id":"g-1","name":"finance"},{"id":"g-2","name":"auditors"}]}`
		_, _ = w.Write([]byte(`{}`))
	})
	added := map[string]bool{}
	f.mux.HandleFunc("POST /api/v1/groups/{g}/members/{u}", func(w http.ResponseWriter, r *http.Request) {
		added[r.PathValue("g")+"/"+r.PathValue("u")] = true
		_, _ = w.Write([]byte(`{}`))
	})

	got, err := client.EnsureAccount(t.Context(), "o-1", NewAccount{Username: "dewi", Name: "Dewi", Password: "long-enough", Groups: []string{"finance", "auditors"}})
	if err != nil || got.ID != "u-1" {
		t.Fatalf("ensure: %+v, %v", got, err)
	}
	if !added["g-1/u-1"] || !added["g-2/u-1"] {
		t.Errorf("memberships %v", added)
	}
	created := 0
	for _, r := range f.snapshot() {
		if r.Method == http.MethodPost && r.Path == "/api/v1/users" {
			created++
			if u, _ := r.Body["user"].(map[string]any); u["username"] != "dewi" || r.Body["password"] != "long-enough" {
				t.Errorf("created %v", r.Body)
			}
		}
	}
	if _, err := client.EnsureAccount(t.Context(), "o-1", NewAccount{Username: "dewi", Password: "x"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.snapshot() {
		if r.Method == http.MethodPost && r.Path == "/api/v1/users" {
			created--
		}
	}
	if created != 0 {
		t.Error("a second EnsureAccount made the account again")
	}
}
