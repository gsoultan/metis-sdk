package main

import (
	metis "github.com/gsoultan/metis-sdk"
)

// The approval line is what the UI draws across the top of a request: the
// start, the three approvals, and the end, each marked with where the request
// stands and what can be done there now.
//
// The script, the budget API call, the decision table and the payment worker
// run between those steps. They are the system's work, not a step anyone waits
// on, so the line leaves them out; the request's history lists what they did.
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
	// Actions are the buttons the step offers now — submit, resubmit,
	// approve, reject, revise — and Actor the one person who may press them.
	Actions []string `json:"actions,omitempty"`
	Actor   string   `json:"actor,omitempty"`
	// TaskID is the metis task an action completes.
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

// lineTypes are the kinds of step ParseFlow is asked for. The revise task is a
// user task too; it is drawn as the start step coming round again, not as a
// step of its own.
var lineTypes = []metis.NodeType{metis.NodeStartEvent, metis.NodeUserTask}

const reviseStep = "revise"

// approvalLine works out the line for one request. steps come from the
// process definition, instance carries where it stands and its variables, and
// open maps each step with an open task to that task's ID.
//
// An approval's state is read from its decision rather than from whether the
// request has passed through it: a request sent back and resubmitted passes
// through the supervisor twice, and the second time is a new decision. The
// revise step clears all three, so a resubmitted request shows every approval
// still to come.
func approvalLine(steps []*metis.FlowStep, instance *metis.Instance, open map[string]string) []lineStep {
	vars := instance.Variables
	finished := instance.IsFinished()
	submitter := vars.StringOr("submittedBy", "")

	line := make([]lineStep, 0, len(steps)+1)
	stopped := false // an approver rejected it, or the budget could not cover it
	for _, s := range steps {
		if s.ID == reviseStep {
			continue
		}
		step := lineStep{ID: s.ID, Name: s.Name}

		if s.Type == metis.NodeStartEvent {
			step.State, step.Who = stateDone, "by "+displayName(submitter)
			if by := vars.StringOr("resubmittedBy", ""); by != "" {
				step.Who = "resubmitted by " + displayName(by)
			}
			if instance.WaitingAt(reviseStep) {
				step.State, step.Who = stateNow, displayName(submitter)+" to revise and resubmit"
				step.Actions, step.Actor, step.TaskID = []string{"resubmit"}, submitter, open[reviseStep]
			}
			if ok, known := vars.Bool("budgetOk"); known && !ok {
				step.Who += " · not enough budget left"
				stopped = true
			}
			line = append(line, step)
			continue
		}

		prefix, isApproval := approvals[s.ID]
		if !isApproval {
			continue
		}
		decision := vars.StringOr(prefix+"Decision", "")
		by := displayName(vars.StringOr(prefix+"By", ""))
		step.Comment = vars.StringOr(prefix+"Comment", "")
		switch {
		case instance.WaitingAt(s.ID):
			approver := approverOf(s.ID)
			step.State, step.Who = stateNow, "Waiting for "+approver.Name+" ("+approver.Role+")"
			step.Actions, step.Actor, step.TaskID = []string{"approve", "reject", "revise"}, approver.Username, open[s.ID]
			step.Comment = ""
		case stopped:
			step.State, step.Comment = stateSkipped, ""
		case decision == "approve":
			step.State, step.Who = stateDone, "Approved by "+by
		case decision == "reject":
			step.State, step.Who, stopped = stateRejected, "Rejected by "+by, true
		case decision == "revise":
			step.State, step.Who = stateReturned, "Sent back by "+by
		case finished:
			step.State, step.Comment = stateSkipped, ""
		default:
			approver := approverOf(s.ID)
			step.State, step.Who, step.Comment = stateLater, approver.Name+" ("+approver.Role+")", ""
		}
		line = append(line, step)
	}

	end := lineStep{ID: "completed", Name: "Completed", State: stateLater}
	switch {
	case instance.Status == metis.ProcessCompleted && !stopped:
		end.State, end.Who = stateDone, "Paid · "+vars.StringOr("paymentReference", "")
	case finished || stopped:
		end.State = stateSkipped
	}
	return append(line, end)
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

// approverOf is the person in the group an approval step is offered to.
func approverOf(stepID string) person {
	for _, p := range people {
		if p.Group == groupOfStep[stepID] {
			return p
		}
	}
	return person{Name: "nobody", Role: "no one is in the group"}
}

// groupOfStep is the candidate group each approval step names in process.bpmn.
var groupOfStep = map[string]string{
	"supervisor-approval": "supervisors",
	"unit-head-approval":  "unit-heads",
	"finance-approval":    "finance",
}
