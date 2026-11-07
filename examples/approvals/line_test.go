package main

import (
	"slices"
	"testing"

	metis "github.com/gsoultan/metis-sdk"
)

// The line is drawn from what metis says about a request, so each state a
// person can see is checked here without a server.
func TestApprovalLine(t *testing.T) {
	step := func(id, status string, groups ...string) metis.StepState {
		s := metis.StepState{ID: id, Name: id, Status: status, Groups: groups}
		switch status {
		case metis.StepWaiting:
			s.Waiting = &metis.Waiting{TaskID: "t-" + id}
		case metis.StepApproved, metis.StepRejected, metis.StepSentBack:
			s.Decision = &metis.Decision{By: "budi", Comment: "noted"}
		}
		return s
	}
	cases := []struct {
		name  string
		req   metis.ApprovalRequest
		want  []string
		actor string
	}{
		{
			name: "waiting for the supervisor",
			req: metis.ApprovalRequest{Status: metis.RequestInProgress, SubmittedBy: "rina", Round: 1, Steps: []metis.StepState{
				step("supervisor", metis.StepWaiting, "supervisors"), step("unit-head", metis.StepPending, "unit-heads"), step("finance", metis.StepPending, "finance")}},
			want:  []string{stateDone, stateNow, stateLater, stateLater, stateLater},
			actor: "budi",
		},
		{
			name: "sent back by the supervisor",
			req: metis.ApprovalRequest{Status: metis.RequestSentBack, SubmittedBy: "rina", Round: 1, Revision: &metis.Waiting{TaskID: "t-r"}, Steps: []metis.StepState{
				step("supervisor", metis.StepSentBack), step("unit-head", metis.StepPending), step("finance", metis.StepPending)}},
			want:  []string{stateNow, stateReturned, stateLater, stateLater, stateLater},
			actor: "rina",
		},
		{
			name: "rejected by the unit head",
			req: metis.ApprovalRequest{Status: metis.RequestRejected, SubmittedBy: "rina", Round: 2, Steps: []metis.StepState{
				step("supervisor", metis.StepApproved), step("unit-head", metis.StepRejected), step("finance", metis.StepPending)}},
			want: []string{stateDone, stateDone, stateRejected, stateSkipped, stateSkipped},
		},
		{
			name: "approved",
			req: metis.ApprovalRequest{Status: metis.RequestApproved, SubmittedBy: "rina", Round: 1, Steps: []metis.StepState{
				step("supervisor", metis.StepApproved), step("unit-head", metis.StepApproved), step("finance", metis.StepApproved)}},
			want: []string{stateDone, stateDone, stateDone, stateDone, stateDone},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := approvalLine(&c.req)
			var got []string
			actor := ""
			for _, s := range line {
				got = append(got, s.State)
				if s.State == stateNow {
					actor = s.Actor
					if s.TaskID == "" || len(s.Actions) == 0 {
						t.Errorf("%s is now but offers nothing: %+v", s.ID, s)
					}
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("states %v, want %v", got, c.want)
			}
			if actor != c.actor {
				t.Errorf("actor %q, want %q", actor, c.actor)
			}
		})
	}
}

// The history says who did what, newest first.
func TestHistory(t *testing.T) {
	req := &metis.ApprovalRequest{SubmittedBy: "rina", Trail: []metis.Decision{
		{Step: "supervisor", Decision: metis.DecisionRevise, By: "budi", Comment: "Receipt?"},
		{Step: "revise", Decision: metis.DecisionResubmit, By: "rina"},
	}}
	got := history(req)
	if len(got) != 3 || got[0].Text != "Rina resubmitted it" || got[1].Text != "Budi sent it back to revise: Receipt?" || got[2].Text != "Rina submitted the request" {
		t.Errorf("history %+v", got)
	}
}
