package main

import (
	"testing"

	metis "github.com/gsoultan/metis-sdk"
)

// The line is worked out from the request's variables and where it stands,
// so each state a person can see is checked here without a server.
func TestApprovalLine(t *testing.T) {
	flows, err := metis.ParseFlow(processXML("http://localhost:8090"), lineTypes...)
	if err != nil || len(flows) != 1 {
		t.Fatalf("process.bpmn: %v (%d flows)", err, len(flows))
	}
	steps := flows[0].Steps

	waitingAt := func(step string) []metis.Token {
		return []metis.Token{{Step: metis.StepRef{ID: step}, Status: metis.TokenActive}}
	}
	cases := []struct {
		name     string
		instance metis.Instance
		open     map[string]string
		want     []string
		actor    string
	}{
		{
			name:     "waiting for the supervisor",
			instance: metis.Instance{Status: metis.ProcessActive, Variables: metis.Variables{"submittedBy": "rina"}, Tokens: waitingAt("supervisor-approval")},
			open:     map[string]string{"supervisor-approval": "t1"},
			want:     []string{stateDone, stateNow, stateLater, stateLater, stateLater},
			actor:    "budi",
		},
		{
			name: "sent back by the unit head",
			instance: metis.Instance{Status: metis.ProcessActive, Tokens: waitingAt("revise"), Variables: metis.Variables{
				"submittedBy": "rina", "supervisorDecision": "approve", "unitHeadDecision": "revise",
			}},
			open:  map[string]string{"revise": "t2"},
			want:  []string{stateNow, stateDone, stateReturned, stateLater, stateLater},
			actor: "rina",
		},
		{
			name: "rejected by finance",
			instance: metis.Instance{Status: metis.ProcessCompleted, Variables: metis.Variables{
				"submittedBy": "rina", "supervisorDecision": "approve", "unitHeadDecision": "approve", "financeDecision": "reject",
			}},
			want: []string{stateDone, stateDone, stateDone, stateRejected, stateSkipped},
		},
		{
			name:     "not enough budget",
			instance: metis.Instance{Status: metis.ProcessCompleted, Variables: metis.Variables{"submittedBy": "rina", "budgetOk": false}},
			want:     []string{stateDone, stateSkipped, stateSkipped, stateSkipped, stateSkipped},
		},
		{
			name: "approved and paid",
			instance: metis.Instance{Status: metis.ProcessCompleted, Variables: metis.Variables{
				"submittedBy": "rina", "budgetOk": true, "supervisorDecision": "approve", "unitHeadDecision": "approve", "financeDecision": "approve",
			}},
			want: []string{stateDone, stateDone, stateDone, stateDone, stateDone},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := approvalLine(steps, &c.instance, c.open)
			if len(line) != len(c.want) {
				t.Fatalf("the line has %d steps, want %d: %+v", len(line), len(c.want), line)
			}
			actor := ""
			for i, step := range line {
				if step.State != c.want[i] {
					t.Errorf("%s is %s, want %s", step.Name, step.State, c.want[i])
				}
				if len(step.Actions) > 0 {
					if step.TaskID == "" {
						t.Errorf("%s offers %v with no task to complete", step.Name, step.Actions)
					}
					actor = step.Actor
				}
			}
			if actor != c.actor {
				t.Errorf("the line's actions are for %q, want %q", actor, c.actor)
			}
		})
	}
}
