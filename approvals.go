package metis

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Approval lines are the simple way to run approvals: a list of approvers,
// written as data. metis turns the line into a process, keeps who decided
// what, and says where each request is. No BPMN, no gateway conditions, no
// variable naming conventions.
//
//	approvals := client.Approvals(projectID)
//	_, err := approvals.Define(ctx, metis.ApprovalLine{
//		Key: "expense", Name: "Expense approval", AllowRevise: true,
//		Steps: []metis.ApprovalStep{
//			{ID: "supervisor", Name: "Supervisor", Groups: []string{"supervisors"}},
//			{ID: "finance", Name: "Finance", Groups: []string{"finance"}},
//		},
//	})
//	req, err := approvals.Submit(ctx, "expense", metis.Variables{"amount": 1200})
//	req, err = approvals.Approve(ctx, req.Steps[0].Waiting.TaskID, "")
//
// Changing a line deploys a new version. Requests already running keep the
// version they were submitted on.

// What can be decided on an approval line's task.
const (
	DecisionApprove  = "approve"
	DecisionReject   = "reject"
	DecisionRevise   = "revise"
	DecisionResubmit = "resubmit"
	DecisionWithdraw = "withdraw"
)

// Where an [ApprovalRequest] is.
const (
	RequestInProgress = "in_progress"
	RequestSentBack   = "sent_back"
	RequestApproved   = "approved"
	RequestRejected   = "rejected"
	RequestWithdrawn  = "withdrawn"
	// RequestStopped is a request cancelled or failed outside the line.
	RequestStopped = "stopped"
)

// Where one step of a request is.
const (
	StepPending  = "pending"
	StepWaiting  = "waiting"
	StepApproved = "approved"
	StepRejected = "rejected"
	StepSentBack = "sent_back"
)

// ApprovalLine is the approvers of a kind of request, in order.
type ApprovalLine struct {
	// Key is what requests are submitted under: lower-case letters, digits
	// and dashes.
	Key   string         `json:"key"`
	Name  string         `json:"name"`
	Steps []ApprovalStep `json:"steps"`
	// AllowRevise lets an approver send the request back to its submitter,
	// who resubmits it (it starts again at the first step) or withdraws it.
	AllowRevise bool `json:"allow_revise,omitempty"`
	// Fields are the variables a submitter may correct when the request is
	// sent back. Needs AllowRevise.
	Fields []ApprovalField `json:"fields,omitempty"`
	// Version and DefinitionID are set by metis.
	Version      int    `json:"version,omitempty"`
	DefinitionID string `json:"definition_id,omitempty"`
}

// ApprovalStep is one approver: any one of the users and members of the
// groups decides.
type ApprovalStep struct {
	// ID names the step: lower-case letters, digits and dashes.
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Users  []string `json:"users,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

// ApprovalField is a variable of a request a form shows.
type ApprovalField struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	// Type is "text" (the default), "number", "boolean" or "date".
	Type string `json:"type,omitempty"`
}

// ApprovalRequest is a request and where it is.
type ApprovalRequest struct {
	ID          string    `json:"instance_id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Version     int       `json:"version"`
	Status      string    `json:"status"`
	SubmittedBy string    `json:"submitted_by,omitempty"`
	SubmittedAt time.Time `json:"submitted_at,omitzero"`
	// Round counts submissions: 2 after the first resubmit.
	Round int `json:"round"`
	// Steps are the line's steps and where each is, this round.
	Steps []StepState `json:"steps"`
	// Revision is the task waiting on the submitter while the request is
	// sent back.
	Revision *Waiting `json:"revision,omitempty"`
	// Trail is every decision, every round, oldest first.
	Trail     []Decision `json:"trail"`
	Variables Variables  `json:"variables,omitempty"`
}

// Waiting returns the step the request waits on, or nil when it waits on no
// approver: finished, or sent back.
func (r *ApprovalRequest) Waiting() *StepState {
	for i := range r.Steps {
		if r.Steps[i].Status == StepWaiting {
			return &r.Steps[i]
		}
	}
	return nil
}

// Done reports whether the request has finished, whichever way.
func (r *ApprovalRequest) Done() bool {
	switch r.Status {
	case RequestApproved, RequestRejected, RequestWithdrawn, RequestStopped:
		return true
	}
	return false
}

// StepState is one step of a request.
type StepState struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// Decision is this round's, once the step decided.
	Decision *Decision `json:"decision,omitempty"`
	// Waiting is the open task while the step waits: decide it by its
	// TaskID.
	Waiting *Waiting `json:"waiting,omitempty"`
	Users   []string `json:"users,omitempty"`
	Groups  []string `json:"groups,omitempty"`
}

// Waiting is the open task a request waits on, and for whom.
type Waiting struct {
	TaskID   string    `json:"task_id"`
	Assignee string    `json:"assignee,omitempty"`
	Users    []string  `json:"users,omitempty"`
	Groups   []string  `json:"groups,omitempty"`
	Since    time.Time `json:"since,omitzero"`
}

// Decision is one decision on a request.
type Decision struct {
	Step     string    `json:"step"`
	Decision string    `json:"decision"`
	Comment  string    `json:"comment,omitempty"`
	By       string    `json:"by"`
	At       time.Time `json:"at"`
	Round    int       `json:"round"`
}

// Approvals runs a project's approval lines. Get one from
// [Client.Approvals]. Who submits and who decides is whoever the client is
// signed in as.
type Approvals struct {
	c       *Client
	project string
}

// Approvals is the project's approval lines.
func (c *Client) Approvals(projectID string) Approvals {
	return Approvals{c: c, project: projectID}
}

// Define deploys the line as the next version of its key. It needs the
// designer role. Requests already running keep their version.
func (a Approvals) Define(ctx context.Context, line ApprovalLine) (*ApprovalLine, error) {
	if a.project == "" {
		return nil, errors.New("metis: approvals need a project ID")
	}
	var out struct {
		Line *ApprovalLine `json:"line"`
	}
	err := a.c.do(ctx, http.MethodPost, "/api/v1/projects/"+url.PathEscape(a.project)+"/approval-lines", line, &out)
	return out.Line, err
}

// Line reads the line new requests are submitted on.
func (a Approvals) Line(ctx context.Context, key string) (*ApprovalLine, error) {
	if a.project == "" || key == "" {
		return nil, errors.New("metis: Line needs a project ID and a key")
	}
	var out struct {
		Line *ApprovalLine `json:"line"`
	}
	err := a.c.do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(a.project)+"/approval-lines/"+url.PathEscape(key), nil, &out)
	return out.Line, err
}

// Submit starts a request on the line. The submitter is the signed-in user,
// who gets it back if an approver sends it back.
func (a Approvals) Submit(ctx context.Context, key string, vars Variables) (*ApprovalRequest, error) {
	if a.project == "" || key == "" {
		return nil, errors.New("metis: Submit needs a project ID and a key")
	}
	body := struct {
		Variables Variables `json:"variables"`
	}{vars}
	var out struct {
		Request *ApprovalRequest `json:"request"`
	}
	err := a.c.do(ctx, http.MethodPost, "/api/v1/projects/"+url.PathEscape(a.project)+"/approval-lines/"+url.PathEscape(key)+"/requests", body, &out)
	return out.Request, err
}

// List lists the line's requests, newest first, across every version of the
// line; only submittedBy's when it is not empty. Each says how its steps
// decided but not who a step waits on: [Approvals.Get] says that.
func (a Approvals) List(ctx context.Context, key, submittedBy string) ([]ApprovalRequest, error) {
	if a.project == "" || key == "" {
		return nil, errors.New("metis: List needs a project ID and a key")
	}
	path := "/api/v1/projects/" + url.PathEscape(a.project) + "/approval-lines/" + url.PathEscape(key) + "/requests"
	if submittedBy != "" {
		path += "?" + url.Values{"submitted_by": {submittedBy}}.Encode()
	}
	var out struct {
		Requests []ApprovalRequest `json:"requests"`
	}
	err := a.c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Requests, err
}

// LineChange is what is still running on a line's older versions once a
// newer one is live.
type LineChange struct {
	Key         string          `json:"key"`
	LiveVersion int             `json:"live_version"`
	Behind      []OlderRequests `json:"behind"`
}

// OlderRequests is one older version's open requests, and what moving them
// onto the live version would do.
type OlderRequests struct {
	Version      int               `json:"version"`
	DefinitionID string            `json:"definition_id"`
	Requests     []ApprovalRequest `json:"requests"`
	Plan         MigrationPlan     `json:"plan"`
}

// MoveResult is what a move did.
type MoveResult struct {
	Moved int        `json:"moved"`
	Left  []LeftOver `json:"left,omitempty"`
}

// LeftOver is a request a move left on its version, and why.
type LeftOver struct {
	RequestID string `json:"instance_id"`
	Why       string `json:"why"`
}

// Behind lists the open requests still on a line's older versions. A new
// version never moves a running request by itself: each keeps the line it
// was submitted on until [Approvals.Move] moves it. An administrator's.
func (a Approvals) Behind(ctx context.Context, key string) (*LineChange, error) {
	if a.project == "" || key == "" {
		return nil, errors.New("metis: Behind needs a project ID and a key")
	}
	var out struct {
		Takeover *LineChange `json:"takeover"`
	}
	err := a.c.do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(a.project)+"/approval-lines/"+url.PathEscape(key)+"/behind", nil, &out)
	return out.Takeover, err
}

// Move moves open requests onto the line's live version. Each moved
// request's open step goes to the new line's approvers. A request the move
// cannot take (its step is gone, or the move would skip an approval) stays
// where it is and is named in Left. An administrator's.
func (a Approvals) Move(ctx context.Context, key string, requestIDs ...string) (*MoveResult, error) {
	if a.project == "" || key == "" || len(requestIDs) == 0 {
		return nil, errors.New("metis: Move needs a project ID, a key and the requests to move")
	}
	var out struct {
		Result *MoveResult `json:"result"`
	}
	err := a.c.do(ctx, http.MethodPost, "/api/v1/projects/"+url.PathEscape(a.project)+"/approval-lines/"+url.PathEscape(key)+"/move",
		map[string]any{"requests": requestIDs}, &out)
	return out.Result, err
}

// Get says where a request is.
func (a Approvals) Get(ctx context.Context, requestID string) (*ApprovalRequest, error) {
	if requestID == "" {
		return nil, errors.New("metis: Get needs a request ID")
	}
	var out struct {
		Request *ApprovalRequest `json:"request"`
	}
	err := a.c.do(ctx, http.MethodGet, "/api/v1/approval-requests/"+url.PathEscape(requestID), nil, &out)
	return out.Request, err
}

// Approve approves a step. The comment may be empty.
func (a Approvals) Approve(ctx context.Context, taskID, comment string) (*ApprovalRequest, error) {
	return a.Decide(ctx, taskID, DecisionApprove, comment, nil)
}

// Reject ends the request. The comment says why and is required.
func (a Approvals) Reject(ctx context.Context, taskID, comment string) (*ApprovalRequest, error) {
	return a.Decide(ctx, taskID, DecisionReject, comment, nil)
}

// Revise sends the request back to its submitter. The comment says what to
// change and is required.
func (a Approvals) Revise(ctx context.Context, taskID, comment string) (*ApprovalRequest, error) {
	return a.Decide(ctx, taskID, DecisionRevise, comment, nil)
}

// Resubmit sends a revised request through the line again, from the first
// step. changes may set only the line's Fields.
func (a Approvals) Resubmit(ctx context.Context, taskID string, changes Variables) (*ApprovalRequest, error) {
	return a.Decide(ctx, taskID, DecisionResubmit, "", changes)
}

// Withdraw ends a sent-back request instead of resubmitting it. The comment
// is required.
func (a Approvals) Withdraw(ctx context.Context, taskID, comment string) (*ApprovalRequest, error) {
	return a.Decide(ctx, taskID, DecisionWithdraw, comment, nil)
}

// Decide is Approve, Reject, Revise, Resubmit and Withdraw by name: for a
// UI that posts the decision it shows.
func (a Approvals) Decide(ctx context.Context, taskID, decision, comment string, changes Variables) (*ApprovalRequest, error) {
	if taskID == "" {
		return nil, errors.New("metis: a decision needs a task ID")
	}
	body := struct {
		Decision  string    `json:"decision"`
		Comment   string    `json:"comment,omitempty"`
		Variables Variables `json:"variables,omitempty"`
	}{decision, comment, changes}
	var out struct {
		Request *ApprovalRequest `json:"request"`
	}
	err := a.c.do(ctx, http.MethodPost, "/api/v1/approval-tasks/"+url.PathEscape(taskID)+"/decision", body, &out)
	return out.Request, err
}

// ApprovalStep is the approval line step a task is, by the step's ID, or
// "revise" for a request sent back to its submitter; "" for a task that is
// not on an approval line. Decide it with [Approvals.Decide].
func (t UserTask) ApprovalStep() string {
	if t.Node == nil {
		return ""
	}
	if step, ok := strings.CutPrefix(t.Node.ID, "step-"); ok {
		return step
	}
	if t.Node.ID == "revise" {
		return "revise"
	}
	return ""
}
