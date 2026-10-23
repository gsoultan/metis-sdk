package metis

import "testing"

const batchOperationJSON = `{"id":"b-1","project_id":"p-1","kind":"retry","reason":"provider back",
	"filter":{"needs_attention":true},"requested_by":"ops","requested_at":"2026-10-22T09:00:00Z",
	"status":"running","total":12,"succeeded":4,"failed":1,"skipped":0,"pending":7}`

// A request that leaves dry_run out is a dry run on the server. The SDK says
// which it means every time, so the body reads as what it does.
func TestABatchOperationIsADryRunUnlessApplied(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/projects/p-1/batch-operations", 200, `{"kind":"retry","matched":12,"too_many":false,"limit":10000}`)

	req := BatchRequest{Kind: BatchRetry, Reason: "provider back", Filter: BatchFilter{NeedsAttention: true}}
	preview, err := client.StartBatchOperation(t.Context(), "p-1", req)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Matched != 12 || preview.Operation != nil {
		t.Errorf("preview = %+v", preview)
	}
	body := f.last().Body
	if body["dry_run"] != true || body["kind"] != "retry" || body["reason"] != "provider back" {
		t.Errorf("body = %#v", body)
	}
	if filter, _ := body["filter"].(map[string]any); filter["needs_attention"] != true || len(filter) != 1 {
		t.Errorf("filter = %#v", body["filter"])
	}
}

func TestApplyingABatchOperationStartsIt(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/projects/p-1/batch-operations", 202,
		`{"kind":"retry","matched":12,"limit":10000,"operation":`+batchOperationJSON+`}`)

	started, err := client.StartBatchOperation(t.Context(), "p-1", BatchRequest{Kind: BatchRetry, Reason: "provider back", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if started.Operation == nil || started.Operation.ID != "b-1" || started.Operation.IsFinished() {
		t.Errorf("operation = %+v", started.Operation)
	}
	if got := f.last().Body["dry_run"]; got != false {
		t.Errorf("dry_run = %#v", got)
	}
}

func TestABatchOperationNeedsAKindAndAReason(t *testing.T) {
	f, client := newFakeServer(t)
	for _, req := range []BatchRequest{{Reason: "why"}, {Kind: BatchCancel}} {
		if _, err := client.StartBatchOperation(t.Context(), "p-1", req); err == nil {
			t.Errorf("%+v was sent", req)
		}
	}
	if _, err := client.StopBatchOperation(t.Context(), "b-1", ""); err == nil {
		t.Error("an operation was stopped with no reason")
	}
	if f.count() != 0 {
		t.Error("a request the SDK refused reached the server")
	}
}

func TestFollowingABatchOperation(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/projects/p-1/batch-operations", 200, `{"operations":[`+batchOperationJSON+`]}`)
	f.respond("GET /api/v1/batch-operations/b-1", 200, `{"operation":`+batchOperationJSON+`}`)
	f.respond("GET /api/v1/batch-operations/b-1/items", 200,
		`{"items":[{"instance_id":"i-1","status":"failed","detail":"still no answer"}],"total":1,"page":2,"page_size":10}`)
	f.respond("POST /api/v1/batch-operations/b-1/stop", 200,
		`{"operation":{"id":"b-1","status":"stopped","stopped_by":"ops","stop_reason":"wrong filter"}}`)
	f.respond("GET /api/v1/batch-operations/b-2", 200, `{}`)

	if ops, err := client.ListBatchOperations(t.Context(), "p-1"); err != nil || len(ops) != 1 {
		t.Errorf("ListBatchOperations = %+v, %v", ops, err)
	}
	op, err := client.GetBatchOperation(t.Context(), "b-1")
	if err != nil || op.Pending != 7 {
		t.Errorf("GetBatchOperation = %+v, %v", op, err)
	}
	if _, err := client.GetBatchOperation(t.Context(), "b-2"); err == nil {
		t.Error("an answer with no operation was taken for one")
	}

	page, err := client.ListBatchOperationItems(t.Context(), "b-1", ListBatchItemsOptions{Status: BatchItemFailed, Page: 2, PageSize: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Detail != "still no answer" {
		t.Errorf("items = %+v, %v", page, err)
	}
	query := f.last().Query
	if query.Get("status") != "failed" || query.Get("page") != "2" || query.Get("page_size") != "10" {
		t.Errorf("query = %v", query)
	}

	stopped, err := client.StopBatchOperation(t.Context(), "b-1", "wrong filter")
	if err != nil || !stopped.IsFinished() || stopped.StopReason != "wrong filter" {
		t.Errorf("StopBatchOperation = %+v, %v", stopped, err)
	}
	if got := f.last().Body["reason"]; got != "wrong filter" {
		t.Errorf("reason = %#v", got)
	}
}
