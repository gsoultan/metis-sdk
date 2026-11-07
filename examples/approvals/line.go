package main

import (
	"fmt"

	metis "github.com/gsoultan/metis-sdk"
)

// expenseLine is the whole approval process: who approves, in order. metis
// turns it into the process, keeps every decision and says where each request
// is, so there is no BPMN in this sample.
var expenseLine = metis.ApprovalLine{
	Key:         lineKey,
	Name:        "Expense approval",
	AllowRevise: true,
	Steps: []metis.ApprovalStep{
		{ID: "supervisor", Name: "Supervisor approval", Groups: []string{"supervisors"}},
		{ID: "unit-head", Name: "Unit head approval", Groups: []string{"unit-heads"}},
		{ID: "finance", Name: "Finance approval", Groups: []string{"finance"}},
	},
	// What the requester may correct when a request is sent back.
	Fields: []metis.ApprovalField{
		{ID: "title", Label: "What it is for"},
		{ID: "amount", Label: "Amount", Type: "number"},
		{ID: "category", Label: "Category"},
		{ID: "costCentre", Label: "Cost centre"},
		{ID: "reason", Label: "Reason"},
	},
}

const lineKey = "expense"

// changedLine is expenseLine after Budi leaves: the supervisor step is Tono's,
// by name, and the rest is unchanged. change.go deploys it.
var changedLine = func() metis.ApprovalLine {
	line := expenseLine
	line.Steps = append([]metis.ApprovalStep(nil), expenseLine.Steps...)
	line.Steps[0] = metis.ApprovalStep{ID: "supervisor", Name: "Supervisor approval", Users: []string{"tono"}}
	return line
}()

// The approval line is what the UI draws across the top of a request: the
// start, the three approvals and the end, each marked with where the request
// stands and what can be done there now.
type lineStep struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// State is done, now, later, rejected, returned (sent back to revise) or
	// skipped.
	State string `json:"state"`
	// Who did the step or is waiting to.
	Who string `json:"who,omitempty"`
	// Comment is what the person who did it said.
	Comment string `json:"comment,omitempty"`
	// Actions are the buttons the step offers now, and Actor the one person
	// who may press them.
	Actions []string `json:"actions,omitempty"`
	Actor   string   `json:"actor,omitempty"`
	// TaskID is the metis task an action decides.
	TaskID string `json:"taskId,omitempty"`
}

const (
	stateDone     = "done"
	stateNow      = "now"
	stateLater    = "later"
	stateRejected = "rejected"
	stateReturned = "returned"
	stateSkipped  = "skipped"
)

// approvalLine draws a request. Everything it needs is in what metis says
// about the request: each step's state, who decided and what they said.
func approvalLine(req *metis.ApprovalRequest) []lineStep {
	start := lineStep{ID: "submitted", Name: "Submitted", State: stateDone, Who: "by " + displayName(req.SubmittedBy)}
	if req.Round > 1 {
		start.Who = fmt.Sprintf("resubmitted by %s (round %d)", displayName(req.SubmittedBy), req.Round)
	}
	if req.Status == metis.RequestSentBack && req.Revision != nil {
		start.State, start.Who = stateNow, displayName(req.SubmittedBy)+" to revise and resubmit"
		start.Actions, start.Actor, start.TaskID = []string{"resubmit"}, req.SubmittedBy, req.Revision.TaskID
	}
	line := []lineStep{start}

	for _, s := range req.Steps {
		step := lineStep{ID: s.ID, Name: s.Name}
		if s.Decision != nil {
			step.Comment = s.Decision.Comment
		}
		switch s.Status {
		case metis.StepWaiting:
			// Who the open task is offered to, which after a move is the
			// new line's approver, not the old one's.
			approver := approverIn(s.Users, s.Groups)
			if len(s.Waiting.Users)+len(s.Waiting.Groups) > 0 {
				approver = approverIn(s.Waiting.Users, s.Waiting.Groups)
			}
			step.State, step.Who = stateNow, "Waiting for "+approver.Name+" ("+approver.Role+")"
			step.Actions, step.Actor, step.TaskID = []string{"approve", "reject", "revise"}, approver.Username, s.Waiting.TaskID
		case metis.StepApproved:
			step.State, step.Who = stateDone, "Approved by "+displayName(s.Decision.By)
		case metis.StepRejected:
			step.State, step.Who = stateRejected, "Rejected by "+displayName(s.Decision.By)
		case metis.StepSentBack:
			step.State, step.Who = stateReturned, "Sent back by "+displayName(s.Decision.By)
		default:
			if req.Done() {
				step.State = stateSkipped
			} else {
				approver := approverIn(s.Users, s.Groups)
				step.State, step.Who = stateLater, approver.Name+" ("+approver.Role+")"
			}
		}
		line = append(line, step)
	}

	end := lineStep{ID: "completed", Name: "Completed", State: stateLater}
	switch req.Status {
	case metis.RequestApproved:
		end.State, end.Who = stateDone, "Approved"
	case metis.RequestRejected, metis.RequestWithdrawn, metis.RequestStopped:
		end.State = stateSkipped
	}
	return append(line, end)
}

// history is the request's trail in words, newest first.
func history(req *metis.ApprovalRequest) []historyEntry {
	out := []historyEntry{{At: req.SubmittedAt, Text: displayName(req.SubmittedBy) + " submitted the request", Step: "submitted"}}
	for _, d := range req.Trail {
		text := displayName(d.By) + " " + map[string]string{
			metis.DecisionApprove:  "approved",
			metis.DecisionReject:   "rejected the request",
			metis.DecisionRevise:   "sent it back to revise",
			metis.DecisionResubmit: "resubmitted it",
			metis.DecisionWithdraw: "withdrew it",
		}[d.Decision]
		if d.Comment != "" {
			text += ": " + d.Comment
		}
		out = append(out, historyEntry{At: d.At, Text: text, Step: d.Step})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func displayName(username string) string {
	if p, ok := personNamed(username); ok {
		return p.Name
	}
	if username == "" {
		return "someone"
	}
	return username
}

// approverIn is the sample person a step is offered to: the first named,
// or else the first in one of its groups.
func approverIn(users, groups []string) person {
	for _, u := range users {
		if p, ok := personNamed(u); ok {
			return p
		}
	}
	for _, p := range people {
		for _, g := range groups {
			if p.Group == g {
				return p
			}
		}
	}
	return person{Name: "nobody", Role: "no one is in the group"}
}
