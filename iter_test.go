package metis

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"testing"
)

// pagedTasks answers the task listing from pages, keyed by the page asked
// for; a page past the last answers empty.
func pagedTasks(f *fakeServer, pages map[string]string) {
	f.mux.HandleFunc("GET /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Query().Get("page")]
		if !ok {
			body = `{"tasks":[],"page":{"has_more":false}}`
		}
		_, _ = w.Write([]byte(body))
	})
}

func collect[T any](t *testing.T, seq iter.Seq2[T, error]) ([]T, error) {
	t.Helper()
	var rows []T
	for row, err := range seq {
		if err != nil {
			return rows, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func TestAllTasksWalksEveryPageOnce(t *testing.T) {
	f, client := newFakeServer(t)
	// t3 was pushed from page 1 onto page 2 by a task created mid-walk.
	pagedTasks(f, map[string]string{
		"1": `{"tasks":[{"id":"t1"},{"id":"t2"},{"id":"t3"}],"page":{"page":1,"has_more":true}}`,
		"2": `{"tasks":[{"id":"t3"},{"id":"t4"},{"id":"t5"}],"page":{"page":2,"has_more":true}}`,
		"3": `{"tasks":[{"id":"t6"}],"page":{"page":3,"has_more":false}}`,
	})

	tasks, err := collect(t, client.AllTasks(t.Context(), ListTasksOptions{ProjectID: "p1", PageSize: 3}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, task := range tasks {
		got = append(got, task.ID)
	}
	if want := "t1 t2 t3 t4 t5 t6"; strings.Join(got, " ") != want {
		t.Errorf("tasks = %v, want %s", got, want)
	}
	if n := f.count(); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
	last := f.last().Query
	if last.Get("project_id") != "p1" || last.Get("page_size") != "3" {
		t.Errorf("query = %v, want the filters on every page", last)
	}
}

func TestAllTasksStopsWhenTheLoopBreaks(t *testing.T) {
	f, client := newFakeServer(t)
	pagedTasks(f, map[string]string{
		"1": `{"tasks":[{"id":"t1"},{"id":"t2"}],"page":{"has_more":true}}`,
		"2": `{"tasks":[{"id":"t3"}],"page":{"has_more":false}}`,
	})
	for task, err := range client.AllTasks(t.Context(), ListTasksOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if task.ID == "t1" {
			break
		}
	}
	if n := f.count(); n != 1 {
		t.Errorf("requests = %d, want 1: no page is read after the loop stops", n)
	}
}

func TestAllTasksStartsAtTheAskedPage(t *testing.T) {
	f, client := newFakeServer(t)
	pagedTasks(f, map[string]string{
		"3": `{"tasks":[{"id":"t9"}],"page":{"has_more":false}}`,
	})
	tasks, err := collect(t, client.AllTasks(t.Context(), ListTasksOptions{Page: 3}))
	if err != nil || len(tasks) != 1 || tasks[0].ID != "t9" {
		t.Fatalf("tasks = %v, %v; want t9 from page 3", tasks, err)
	}
}

func TestAllTasksYieldsTheErrorOnceAndStops(t *testing.T) {
	f, client := newFakeServer(t)
	f.mux.HandleFunc("GET /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`{"tasks":[{"id":"t1"}],"page":{"has_more":true}}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"not yours"}`))
	})

	var rows, errs int
	for _, err := range client.AllTasks(t.Context(), ListTasksOptions{}) {
		if err != nil {
			errs++
			if !IsUnauthorized(err) {
				t.Errorf("err = %v, want the server's 403", err)
			}
			continue
		}
		rows++
	}
	if rows != 1 || errs != 1 {
		t.Errorf("rows = %d, errors = %d; want 1 and 1", rows, errs)
	}
}

func TestAllTasksGivesUpOnAServerThatIgnoresThePage(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[{"id":"t1"}],"page":{"has_more":true}}`)

	rows, err := collect(t, client.AllTasks(t.Context(), ListTasksOptions{}))
	if !errors.Is(err, ErrPagingStalled) {
		t.Fatalf("err = %v, want ErrPagingStalled", err)
	}
	if len(rows) != 1 {
		t.Errorf("rows = %d, want the one row before it stalled", len(rows))
	}
	if n := f.count(); n != 1+stalledPages {
		t.Errorf("requests = %d, want %d", n, 1+stalledPages)
	}
}

func TestAllTasksKeepsGoingPastOneRepeatedPage(t *testing.T) {
	f, client := newFakeServer(t)
	// Page 2 is all rows already seen, as after a burst of inserts.
	pagedTasks(f, map[string]string{
		"1": `{"tasks":[{"id":"t1"},{"id":"t2"}],"page":{"has_more":true}}`,
		"2": `{"tasks":[{"id":"t1"},{"id":"t2"}],"page":{"has_more":true}}`,
		"3": `{"tasks":[{"id":"t3"}],"page":{"has_more":false}}`,
	})
	rows, err := collect(t, client.AllTasks(t.Context(), ListTasksOptions{}))
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %v, %v; want t1 t2 t3", rows, err)
	}
}

func TestAllTasksHonoursACancelledContext(t *testing.T) {
	f, client := newFakeServer(t)
	pagedTasks(f, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := collect(t, client.AllTasks(ctx, ListTasksOptions{}))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if n := f.count(); n != 0 {
		t.Errorf("requests = %d, want none", n)
	}
}

func TestAllTasksReadsAnUnpagedListingOnce(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[{"id":"t1"},{"id":"t2"}]}`)
	rows, err := collect(t, client.AllTasks(t.Context(), ListTasksOptions{}))
	if err != nil || len(rows) != 2 || f.count() != 1 {
		t.Errorf("rows = %d, err = %v, requests = %d; want 2, nil, 1", len(rows), err, f.count())
	}
}

func TestAllInstancesDefinitionsAndClaimable(t *testing.T) {
	f, client := newFakeServer(t)
	f.mux.HandleFunc("GET /api/v1/instances", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		fmt.Fprintf(w, `{"instances":[{"id":"i%s"}],"page":{"has_more":%t}}`, page, page == "1")
	})
	f.mux.HandleFunc("GET /api/v1/definitions", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		fmt.Fprintf(w, `{"definitions":[{"id":"d%s"}],"page":{"has_more":%t}}`, page, page == "1")
	})
	f.mux.HandleFunc("POST /api/v1/tasks/candidates", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tasks":[{"id":"c1"}],"page":{"has_more":false}}`))
	})

	instances, err := collect(t, client.AllInstances(t.Context(), ListInstancesOptions{ProjectID: "p1"}))
	if err != nil || len(instances) != 2 || instances[1].ID != "i2" {
		t.Errorf("instances = %v, %v; want i1 i2", instances, err)
	}
	definitions, err := collect(t, client.AllDefinitions(t.Context(), ListDefinitionsOptions{}))
	if err != nil || len(definitions) != 2 || definitions[1].ID != "d2" {
		t.Errorf("definitions = %v, %v; want d1 d2", definitions, err)
	}
	claimable, err := collect(t, client.AllClaimableTasks(t.Context(), PageOptions{PageSize: 50}))
	if err != nil || len(claimable) != 1 {
		t.Errorf("claimable = %v, %v; want c1", claimable, err)
	}
	if got := f.last().Body["page_size"]; got != float64(50) {
		t.Errorf("page_size = %v, want 50", got)
	}
}

func TestAllBatchOperationItemsReadsUpToTheTotal(t *testing.T) {
	f, client := newFakeServer(t)
	f.mux.HandleFunc("GET /api/v1/batch-operations/op1/items", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{"items":[{"instance_id":"i1"},{"instance_id":"i2"}],"total":3,"page":1,"page_size":2}`))
		default:
			_, _ = w.Write([]byte(`{"items":[{"instance_id":"i3"}],"total":3,"page":2,"page_size":2}`))
		}
	})
	items, err := collect(t, client.AllBatchOperationItems(t.Context(), "op1", ListBatchItemsOptions{Status: BatchItemStatus("failed")}))
	if err != nil || len(items) != 3 || items[2].Instance.ID != "i3" {
		t.Fatalf("items = %v, %v; want i1 i2 i3", items, err)
	}
	if n := f.count(); n != 2 {
		t.Errorf("requests = %d, want 2: the total says page 2 is the last", n)
	}
	if got := f.last().Query.Get("status"); got != "failed" {
		t.Errorf("status = %q, want the filter on every page", got)
	}
}

func TestAllBatchOperationItemsStopsOnError(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/batch-operations/op1/items", 404, `{"error":"no such operation"}`)
	_, err := collect(t, client.AllBatchOperationItems(t.Context(), "op1", ListBatchItemsOptions{}))
	if !IsNotFound(err) {
		t.Errorf("err = %v, want 404", err)
	}
}

func TestAllOperationsFollowsTheCursor(t *testing.T) {
	f, client := newFakeServer(t)
	f.mux.HandleFunc("GET /api/v1/audit/operations", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("before") {
		case "":
			_, _ = w.Write([]byte(`{"operations":[{"id":"o1"},{"id":"o2"}],"next":"c2"}`))
		case "c2":
			_, _ = w.Write([]byte(`{"operations":[{"id":"o3"}]}`))
		}
	})
	ops, err := collect(t, client.AllOperations(t.Context(), ListOperationsOptions{Actor: "ann"}))
	if err != nil || len(ops) != 3 || ops[2].ID != "o3" {
		t.Fatalf("operations = %v, %v; want o1 o2 o3", ops, err)
	}
	if got := f.last().Query; got.Get("before") != "c2" || got.Get("actor") != "ann" {
		t.Errorf("query = %v, want the cursor and the filter", got)
	}
}

func TestAllOperationsStopsOnARepeatedCursor(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/audit/operations", 200, `{"operations":[{"id":"o1"}],"next":"same"}`)
	_, err := collect(t, client.AllOperations(t.Context(), ListOperationsOptions{Before: "same"}))
	if !errors.Is(err, ErrPagingStalled) {
		t.Errorf("err = %v, want ErrPagingStalled", err)
	}
}

func TestAllOperationsStopsOnError(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/audit/operations", 403, `{"error":"administrators only"}`)
	_, err := collect(t, client.AllOperations(t.Context(), ListOperationsOptions{}))
	if !IsUnauthorized(err) {
		t.Errorf("err = %v, want 403", err)
	}
}

func TestAllOutgoingWebhookDeliveriesFollowsTheCursor(t *testing.T) {
	f, client := newFakeServer(t)
	f.mux.HandleFunc("GET /api/v1/outgoing-webhooks/w1/deliveries", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("before") == "" {
			_, _ = w.Write([]byte(`{"deliveries":[{"id":"d1"}],"next_before":"d1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"deliveries":[{"id":"d0"}]}`))
	})
	deliveries, err := collect(t, client.AllOutgoingWebhookDeliveries(t.Context(), "w1", ListDeliveriesOptions{}))
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("deliveries = %v, %v; want d1 d0", deliveries, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := collect(t, client.AllOutgoingWebhookDeliveries(ctx, "w1", ListDeliveriesOptions{})); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	f.respond("GET /api/v1/outgoing-webhooks/w2/deliveries", 404, `{"error":"gone"}`)
	if _, err := collect(t, client.AllOutgoingWebhookDeliveries(t.Context(), "w2", ListDeliveriesOptions{})); !IsNotFound(err) {
		t.Errorf("err = %v, want 404", err)
	}
}
