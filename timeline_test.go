package metis

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// The server writes an entry's step as a nested node and its time as
// "timestamp". Read under the SDK's own names alone, both came back empty.
func TestAuditEntryReadsTheServersShape(t *testing.T) {
	var entry AuditEntry
	err := json.Unmarshal([]byte(`{
		"id":"e-1","type":"node_reached","message":"node_reached",
		"node":{"id":"approve","name":"Approve refund","type":"userTask"},
		"timestamp":"2026-10-14T09:30:00Z"
	}`), &entry)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if entry.ID != "e-1" || entry.Step.ID != "approve" || entry.Step.Name != "Approve refund" {
		t.Errorf("entry = %+v", entry)
	}
	if !entry.CreatedAt.Equal(time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("created = %v", entry.CreatedAt)
	}

	// The SDK's own spelling still reads, for anything that wrote it.
	var flat AuditEntry
	if err := json.Unmarshal([]byte(`{"type":"x","node_id":"n","node_name":"N","created_at":"2026-10-14T09:30:00Z"}`), &flat); err != nil {
		t.Fatalf("decode flat: %v", err)
	}
	if flat.Step.ID != "n" || flat.Step.Name != "N" || flat.CreatedAt.IsZero() {
		t.Errorf("flat = %+v", flat)
	}
}

// fakeTrail serves an audit trail as Metis v0.5.0 does: the newest pageMax
// entries after skipping offset of the newest, oldest first, with the total.
// grow entries are added to the trail after each read, as a running instance
// would while somebody pages through it.
type fakeTrail struct {
	entries []string
	pageMax int
	grow    int
	reads   int
}

func (f *fakeTrail) serve(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > f.pageMax {
		limit = f.pageMax
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	end := max(len(f.entries)-offset, 0)
	start := max(end-limit, 0)
	page := make([]map[string]string, 0, end-start)
	for _, id := range f.entries[start:end] {
		page = append(page, map[string]string{"id": id, "type": "step", "message": id})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": page, "total": len(f.entries)})

	f.reads++
	for range f.grow {
		f.entries = append(f.entries, fmt.Sprintf("e%d", len(f.entries)+1))
	}
}

func newFakeTrail(n, pageMax, grow int) *fakeTrail {
	f := &fakeTrail{pageMax: pageMax, grow: grow}
	for i := 1; i <= n; i++ {
		f.entries = append(f.entries, fmt.Sprintf("e%d", i))
	}
	return f
}

func TestGetTimelineReadsEveryPage(t *testing.T) {
	cases := []struct {
		name               string
		n, pageMax, grow   int
		wantReads, wantLen int
	}{
		{"one page", 3, 1000, 0, 1, 3},
		{"exactly one page", 4, 4, 0, 1, 4},
		{"several pages", 10, 4, 0, 3, 10},
		// Entries written between reads push older ones back, so pages
		// overlap. Every entry of the trail as it was first read must come
		// back once, in order.
		{"a trail that grows while it is read", 10, 4, 2, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, client := newFakeServer(t)
			trail := newFakeTrail(tc.n, tc.pageMax, tc.grow)
			f.mux.HandleFunc("GET /api/v1/instances/inst-1/audit", trail.serve)

			entries, err := client.GetTimeline(t.Context(), "inst-1")
			if err != nil {
				t.Fatalf("timeline: %v", err)
			}
			if tc.wantReads > 0 && trail.reads != tc.wantReads {
				t.Errorf("reads = %d, want %d", trail.reads, tc.wantReads)
			}
			if tc.wantLen > 0 && len(entries) != tc.wantLen {
				t.Errorf("got %d entries, want %d", len(entries), tc.wantLen)
			}

			seen := map[string]bool{}
			last := 0
			for _, entry := range entries {
				if seen[entry.ID] {
					t.Fatalf("entry %s came back twice", entry.ID)
				}
				seen[entry.ID] = true
				n, _ := strconv.Atoi(entry.ID[1:])
				if n <= last {
					t.Fatalf("entries out of order: %s after e%d", entry.ID, last)
				}
				last = n
			}
			for i := 1; i <= tc.n; i++ {
				if !seen[fmt.Sprintf("e%d", i)] {
					t.Errorf("entry e%d is missing", i)
				}
			}
		})
	}
}

// A server older than v0.5.0 sends the whole trail and no total. That is one
// read, as it always was.
func TestGetTimelineAgainstAServerThatDoesNotPage(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/inst-1/audit", 200,
		`{"entries":[{"id":"e1","type":"a","message":"a"},{"id":"e2","type":"b","message":"b"}]}`)

	entries, err := client.GetTimeline(t.Context(), "inst-1")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(entries) != 2 || f.count() != 1 {
		t.Errorf("got %d entries in %d reads", len(entries), f.count())
	}
}

// A server that claims more entries than it will ever send must not keep the
// reader asking forever.
func TestGetTimelineStopsWhenThePagesRunDry(t *testing.T) {
	f, client := newFakeServer(t)
	reads := 0
	f.mux.HandleFunc("GET /api/v1/instances/inst-1/audit", func(w http.ResponseWriter, r *http.Request) {
		reads++
		if r.URL.Query().Get("offset") == "" {
			_, _ = w.Write([]byte(`{"entries":[{"id":"e9","type":"a","message":"a"}],"total":50}`))
			return
		}
		_, _ = w.Write([]byte(`{"entries":[],"total":50}`))
	})

	entries, err := client.GetTimeline(t.Context(), "inst-1")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(entries) != 1 || reads != 2 {
		t.Errorf("got %d entries in %d reads", len(entries), reads)
	}
}

func TestGetTimelinePage(t *testing.T) {
	f, client := newFakeServer(t)
	trail := newFakeTrail(30, 1000, 0)
	f.mux.HandleFunc("GET /api/v1/instances/inst-1/audit", trail.serve)

	page, err := client.GetTimelinePage(t.Context(), "inst-1", TimelinePageOptions{Limit: 5, Offset: 10})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 30 || len(page.Entries) != 5 {
		t.Fatalf("page = %d entries of %d", len(page.Entries), page.Total)
	}
	// Ten newest skipped, then the five before them, oldest first.
	if page.Entries[0].ID != "e16" || page.Entries[4].ID != "e20" {
		t.Errorf("entries run %s..%s", page.Entries[0].ID, page.Entries[4].ID)
	}
	if q := f.last().Query; q.Get("limit") != "5" || q.Get("offset") != "10" {
		t.Errorf("query = %v", q)
	}
}
