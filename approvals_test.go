package metis

import (
	"net/http"
	"testing"
)

const requestJSON = `{"request":{"instance_id":"r-1","key":"expense","version":1,"status":"in_progress","submitted_by":"rina","round":1,
	"steps":[{"id":"supervisor","name":"Supervisor","status":"waiting","waiting":{"task_id":"t-1","groups":["supervisors"]}},
	         {"id":"finance","name":"Finance","status":"pending"}],
	"trail":[]}}`

// Define posts the line to the project, and Submit, Get and the decisions
// read back where the request is.
func TestApprovalsDefineSubmitAndDecide(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/projects/p-1/approval-lines", 200, `{"line":{"key":"expense","name":"Expense","version":2,"steps":[{"id":"supervisor","name":"Supervisor","groups":["supervisors"]}]}}`)
	f.respond("POST /api/v1/projects/p-1/approval-lines/expense/requests", 200, requestJSON)
	f.respond("POST /api/v1/approval-tasks/t-1/decision", 200, requestJSON)
	f.respond("GET /api/v1/approval-requests/r-1", 200, requestJSON)
	approvals := client.Approvals("p-1")

	line, err := approvals.Define(t.Context(), ApprovalLine{Key: "expense", Name: "Expense", AllowRevise: true,
		Steps: []ApprovalStep{{ID: "supervisor", Name: "Supervisor", Groups: []string{"supervisors"}}}})
	if err != nil || line.Version != 2 {
		t.Fatalf("define: %+v, %v", line, err)
	}
	if body := f.last().Body; body["key"] != "expense" || body["allow_revise"] != true || len(body["steps"].([]any)) != 1 {
		t.Errorf("sent %v", body)
	}

	req, err := approvals.Submit(t.Context(), "expense", Variables{"amount": 1200})
	if err != nil {
		t.Fatal(err)
	}
	if vars, _ := f.last().Body["variables"].(map[string]any); vars["amount"] != float64(1200) {
		t.Errorf("submitted %v", f.last().Body)
	}
	waiting := req.Waiting()
	if waiting == nil || waiting.ID != "supervisor" || waiting.Waiting.TaskID != "t-1" || req.Done() {
		t.Fatalf("request %+v", req)
	}

	for _, c := range []struct {
		decide   func() (*ApprovalRequest, error)
		decision string
		comment  string
	}{
		{func() (*ApprovalRequest, error) { return approvals.Approve(t.Context(), "t-1", "") }, DecisionApprove, ""},
		{func() (*ApprovalRequest, error) { return approvals.Reject(t.Context(), "t-1", "No") }, DecisionReject, "No"},
		{func() (*ApprovalRequest, error) { return approvals.Revise(t.Context(), "t-1", "Receipt?") }, DecisionRevise, "Receipt?"},
		{func() (*ApprovalRequest, error) { return approvals.Withdraw(t.Context(), "t-1", "Not needed") }, DecisionWithdraw, "Not needed"},
	} {
		if _, err := c.decide(); err != nil {
			t.Fatalf("%s: %v", c.decision, err)
		}
		body := f.last().Body
		if body["decision"] != c.decision || (c.comment != "" && body["comment"] != c.comment) || body["variables"] != nil {
			t.Errorf("%s sent %v", c.decision, body)
		}
	}
	if _, err := approvals.Resubmit(t.Context(), "t-1", Variables{"amount": 900}); err != nil {
		t.Fatal(err)
	}
	if body := f.last().Body; body["decision"] != DecisionResubmit || body["variables"].(map[string]any)["amount"] != float64(900) {
		t.Errorf("resubmit sent %v", body)
	}
	f.respond("GET /api/v1/projects/p-1/approval-lines/expense/requests", 200, `{"requests":[{"instance_id":"r-1","status":"approved"}]}`)
	if list, err := approvals.List(t.Context(), "expense", "rina"); err != nil || len(list) != 1 || !list[0].Done() || f.last().Query.Get("submitted_by") != "rina" {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if got, err := approvals.Get(t.Context(), "r-1"); err != nil || got.SubmittedBy != "rina" || f.last().Method != http.MethodGet {
		t.Fatalf("get: %+v, %v", got, err)
	}
}

// A call missing what it names is refused before anything is sent.
func TestApprovalsNeedWhatTheyName(t *testing.T) {
	_, client := newFakeServer(t)
	if _, err := client.Approvals("").Define(t.Context(), ApprovalLine{}); err == nil {
		t.Error("Define with no project")
	}
	if _, err := client.Approvals("p").Submit(t.Context(), "", nil); err == nil {
		t.Error("Submit with no key")
	}
	if _, err := client.Approvals("p").Approve(t.Context(), "", ""); err == nil {
		t.Error("Approve with no task")
	}
}

// The inbox is the user's own tasks and the ones offered to them, each once,
// oldest first, open ones only.
func TestInboxPutsMineAndOfferedTogether(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/users/me", 200, `{"user":{"username":"budi"}}`)
	f.respond("GET /api/v1/tasks/assignee/budi", 200, `{"tasks":[
		{"id":"t-2","status":"claimed","created_at":"2026-11-07T10:00:00Z"},
		{"id":"t-9","status":"completed","created_at":"2026-11-07T08:00:00Z"}],"page_info":{"has_more":false}}`)
	f.respond("POST /api/v1/tasks/candidates", 200, `{"tasks":[
		{"id":"t-1","status":"unclaimed","created_at":"2026-11-07T09:00:00Z"},
		{"id":"t-2","status":"claimed","created_at":"2026-11-07T10:00:00Z"}],"page_info":{"has_more":false}}`)

	got, err := client.Inbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "t-1" || got[1].ID != "t-2" {
		t.Fatalf("inbox %+v", got)
	}
}
