package metis

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// approvalXML is a requester's case: Submit, a Checker who passes it on or
// sends it back, a Review, a notification nobody waits on, and an Approval —
// declared out of order, with the flows first, as some tools export them.
const approvalXML = `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL" xmlns:camunda="http://camunda.org/schema/1.0/bpmn">
  <bpmn:process id="loan" isExecutable="true">
    <bpmn:laneSet>
      <bpmn:lane id="l1" name="Requester"><bpmn:flowNodeRef>submit</bpmn:flowNodeRef></bpmn:lane>
      <bpmn:lane id="l2" name="Checker">
        <bpmn:flowNodeRef>check</bpmn:flowNodeRef>
        <bpmn:flowNodeRef>review</bpmn:flowNodeRef>
      </bpmn:lane>
      <bpmn:lane id="l3" name="Manager"><bpmn:flowNodeRef> approve </bpmn:flowNodeRef></bpmn:lane>
    </bpmn:laneSet>
    <bpmn:sequenceFlow id="f1" sourceRef="start" targetRef="submit"/>
    <bpmn:sequenceFlow id="f2" sourceRef="submit" targetRef="check"/>
    <bpmn:sequenceFlow id="f3" sourceRef="check" targetRef="ok"/>
    <bpmn:sequenceFlow id="f4" sourceRef="ok" targetRef="review"/>
    <bpmn:sequenceFlow id="f5" sourceRef="ok" targetRef="submit"/>
    <bpmn:sequenceFlow id="f6" sourceRef="review" targetRef="notify"/>
    <bpmn:sequenceFlow id="f7" sourceRef="notify" targetRef="approve"/>
    <bpmn:sequenceFlow id="f8" sourceRef="approve" targetRef="end"/>
    <bpmn:userTask id="approve" name="Approval" camunda:candidateGroups="managers, directors" camunda:formKey="approval-form">
      <bpmn:documentation> Approve or reject the loan. </bpmn:documentation>
      <bpmn:multiInstanceLoopCharacteristics isSequential="false" camunda:collection="approvers" camunda:elementVariable="approver">
        <bpmn:completionCondition>${nrOfCompletedInstances >= 2}</bpmn:completionCondition>
      </bpmn:multiInstanceLoopCharacteristics>
    </bpmn:userTask>
    <bpmn:endEvent id="end"/>
    <bpmn:serviceTask id="notify" name="Notify"/>
    <bpmn:userTask id="review" name="Review"/>
    <bpmn:exclusiveGateway id="ok" name="OK?"/>
    <bpmn:userTask id="check" name="Checker"/>
    <bpmn:userTask id="submit" name="Submit" camunda:assignee="${initiator}"/>
    <bpmn:startEvent id="start"/>
  </bpmn:process>
</bpmn:definitions>`

func names(steps []*FlowStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Name
	}
	return out
}

func ids(steps []*FlowStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.ID
	}
	return out
}

// oneFlow parses a document that must hold exactly one process.
func oneFlow(t *testing.T, doc string, types ...NodeType) *Flow {
	t.Helper()
	flows, err := ParseFlow([]byte(doc), types...)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 {
		t.Fatalf("%d flows, want 1", len(flows))
	}
	return flows[0]
}

func TestParseFlowOrdersUserTasksAsWorkReachesThem(t *testing.T) {
	flow := oneFlow(t, approvalXML, NodeUserTask)
	if flow.ID != "loan" {
		t.Errorf("flow ID = %q", flow.ID)
	}
	if got, want := names(flow.Steps), []string{"Submit", "Checker", "Review", "Approval"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i, step := range flow.Steps {
		if step.Order != i+1 || step.Flow() != flow || flow.Step(step.ID) != step {
			t.Errorf("%s: Order %d, flow %p, lookup %p", step.ID, step.Order, step.Flow(), flow.Step(step.ID))
		}
		if step.SubProcess != nil || step.Depth() != 0 {
			t.Errorf("%s is placed in a sub-process", step.ID)
		}
	}
	cases := []struct {
		id, lane       string
		next, previous []string
	}{
		{"submit", "Requester", []string{"check"}, []string{"check"}},
		{"check", "Checker", []string{"review", "submit"}, []string{"submit"}},
		{"review", "Checker", []string{"approve"}, []string{"check"}},
		{"approve", "Manager", []string{}, []string{"review"}},
	}
	for _, c := range cases {
		step := flow.Step(c.id)
		if step.Lane != c.lane || !slices.Equal(ids(step.Next()), c.next) || !slices.Equal(ids(step.Previous()), c.previous) {
			t.Errorf("%s = lane %q next %v previous %v; want %q %v %v",
				c.id, step.Lane, ids(step.Next()), ids(step.Previous()), c.lane, c.next, c.previous)
		}
	}
	check, submit, review := flow.Step("check"), flow.Step("submit"), flow.Step("review")
	if !check.SendsBackTo(submit) || check.SendsBackTo(review) || submit.SendsBackTo(check) {
		t.Error("SendsBackTo cannot tell a send-back from a step forward")
	}
	if flow.Step("ok") != nil {
		t.Error("a gateway was returned among the user tasks")
	}
}

func TestParseFlowReadsWhoAStepIsFor(t *testing.T) {
	flow := oneFlow(t, approvalXML, NodeUserTask)

	if who := flow.Step("submit").Assignment; who == nil || who.Assignee != "${initiator}" {
		t.Errorf("submit assignment = %+v", who)
	}
	approve := flow.Step("approve")
	if who := approve.Assignment; who == nil || !slices.Equal(who.CandidateGroups, []string{"managers", "directors"}) || who.FormKey != "approval-form" {
		t.Errorf("approve assignment = %+v", who)
	}
	if approve.Documentation != "Approve or reject the loan." {
		t.Errorf("documentation = %q", approve.Documentation)
	}
	multi := approve.MultiInstance
	if multi == nil || multi.Sequential || multi.Collection != "approvers" || multi.ElementVariable != "approver" ||
		multi.CompletionCondition != "${nrOfCompletedInstances >= 2}" {
		t.Errorf("multi-instance = %+v", multi)
	}
	if flow.Step("check").MultiInstance != nil {
		t.Error("a step that runs once has a multi-instance")
	}

	all := oneFlow(t, approvalXML)
	if all.Step("notify").Assignment != nil {
		t.Error("a service task has an assignment")
	}
}

func TestParseFlowWithNoKindsReturnsEveryStep(t *testing.T) {
	flow := oneFlow(t, approvalXML)
	want := []string{"", "Submit", "Checker", "OK?", "Review", "Notify", "Approval", ""}
	if got := names(flow.Steps); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if flow.Steps[0].Type != NodeStartEvent || flow.Steps[len(flow.Steps)-1].Type != NodeEndEvent {
		t.Errorf("ends = %s, %s", flow.Steps[0].Type, flow.Steps[len(flow.Steps)-1].Type)
	}
}

func TestParseFlowEntersSubProcesses(t *testing.T) {
	doc := `<definitions><process id="p">
		<startEvent id="s"/><sequenceFlow sourceRef="s" targetRef="a"/>
		<userTask id="a" name="Apply"/><sequenceFlow sourceRef="a" targetRef="sub"/>
		<subProcess id="sub" name="Checks">
			<startEvent id="ss"/><sequenceFlow sourceRef="ss" targetRef="k1"/>
			<userTask id="k1" name="KYC"/><sequenceFlow sourceRef="k1" targetRef="inner"/>
			<subProcess id="inner" name="Credit checks">
				<userTask id="k2" name="Credit"/>
			</subProcess>
			<sequenceFlow sourceRef="inner" targetRef="se"/>
			<endEvent id="se"/>
		</subProcess>
		<sequenceFlow sourceRef="sub" targetRef="z"/>
		<userTask id="z" name="Sign"/>
		<boundaryEvent id="late" attachedToRef="sub"/><sequenceFlow sourceRef="late" targetRef="esc"/>
		<userTask id="esc" name="Escalate"/>
	</process></definitions>`
	flow := oneFlow(t, doc, NodeUserTask)
	if got := names(flow.Steps); !slices.Equal(got, []string{"Apply", "KYC", "Credit", "Sign", "Escalate"}) {
		t.Fatalf("order = %v", got)
	}
	kyc, credit, sign := flow.Step("k1"), flow.Step("k2"), flow.Step("z")
	if kyc.SubProcess == nil || kyc.SubProcess.ID != "sub" || kyc.Depth() != 1 {
		t.Errorf("KYC sits in %+v at depth %d", kyc.SubProcess, kyc.Depth())
	}
	if credit.SubProcess.ID != "inner" || credit.SubProcess.Parent != kyc.SubProcess || credit.Depth() != 2 {
		t.Errorf("Credit sits in %+v at depth %d", credit.SubProcess, credit.Depth())
	}
	if sign.SubProcess != nil {
		t.Errorf("Sign sits in %+v", sign.SubProcess)
	}
	if got := ids(flow.Step("a").Next()); !slices.Equal(got, []string{"k1"}) {
		t.Errorf("Apply next = %v, want into the sub-process", got)
	}
	if got := ids(credit.Next()); !slices.Equal(got, []string{"z", "esc"}) {
		t.Errorf("Credit next = %v, want out of both sub-processes, or escalated", got)
	}
	// The timer on the sub-process can fire during any activity inside it.
	if got := ids(flow.Step("esc").Previous()); !slices.Equal(got, []string{"k1", "k2"}) {
		t.Errorf("Escalate previous = %v, want the steps inside the sub-process", got)
	}
}

func TestParseFlowParallelBranchesFollowTheFile(t *testing.T) {
	doc := `<definitions><process id="p">
		<startEvent id="s"/><parallelGateway id="fork"/><parallelGateway id="join"/>
		<userTask id="legal" name="Legal"/><userTask id="finance" name="Finance"/><userTask id="sign" name="Sign"/>
		<sequenceFlow sourceRef="s" targetRef="fork"/>
		<sequenceFlow sourceRef="fork" targetRef="finance"/><sequenceFlow sourceRef="fork" targetRef="legal"/>
		<sequenceFlow sourceRef="legal" targetRef="join"/><sequenceFlow sourceRef="finance" targetRef="join"/>
		<sequenceFlow sourceRef="join" targetRef="sign"/>
	</process></definitions>`
	flow := oneFlow(t, doc, NodeUserTask)
	if got := names(flow.Steps); !slices.Equal(got, []string{"Legal", "Finance", "Sign"}) {
		t.Fatalf("order = %v", got)
	}
	if got := ids(flow.Step("sign").Previous()); !slices.Equal(got, []string{"legal", "finance"}) {
		t.Errorf("Sign previous = %v", got)
	}
}

func TestParseFlowSurvivesACycleNothingEnters(t *testing.T) {
	doc := `<definitions><process id="p">
		<userTask id="a" name="A"/><userTask id="b" name="B"/>
		<sequenceFlow sourceRef="a" targetRef="b"/><sequenceFlow sourceRef="b" targetRef="a"/>
		<userTask id="self" name="Self"/><sequenceFlow sourceRef="self" targetRef="self"/>
	</process></definitions>`
	flow := oneFlow(t, doc, NodeUserTask)
	if got := names(flow.Steps); !slices.Equal(got, []string{"A", "B", "Self"}) {
		t.Fatalf("order = %v", got)
	}
	self := flow.Step("self")
	if !slices.Equal(self.Next(), []*FlowStep{self}) || !self.SendsBackTo(self) {
		t.Errorf("a step that loops to itself has next %v", ids(self.Next()))
	}
}

func TestParseFlowKeepsEachPoolApart(t *testing.T) {
	doc := `<definitions>
		<process id="buyer" name="Buyer"><userTask id="order" name="Order"/></process>
		<process id="seller" name="Seller"><userTask id="ship" name="Ship"/></process>
	</definitions>`
	flows, err := ParseFlow([]byte(doc), NodeUserTask)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 2 || flows[0].Name != "Buyer" || names(flows[1].Steps)[0] != "Ship" {
		t.Fatalf("flows = %+v", flows)
	}
	if flows[1].Steps[0].Order != 1 || flows[1].Step("order") != nil {
		t.Error("the pools were numbered or looked up together")
	}
}

func TestFlowStepJSONNamesItsLinks(t *testing.T) {
	flow := oneFlow(t, approvalXML, NodeUserTask)
	encoded, err := json.Marshal(flow.Step("check"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ID       string   `json:"id"`
		Order    int      `json:"order"`
		Lane     string   `json:"lane"`
		Next     []string `json:"next"`
		Previous []string `json:"previous"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID != "check" || decoded.Order != 2 || decoded.Lane != "Checker" ||
		!slices.Equal(decoded.Next, []string{"review", "submit"}) || !slices.Equal(decoded.Previous, []string{"submit"}) {
		t.Errorf("json = %s", encoded)
	}
}

func TestParseFlowRefusesWhatParseNodesRefuses(t *testing.T) {
	if _, err := ParseFlow([]byte(`<html/>`)); err == nil {
		t.Error("a document that is not BPMN was parsed")
	}
	dup := `<definitions><process id="p"><userTask id="a"/><userTask id="a"/></process></definitions>`
	if _, err := ParseFlow([]byte(dup)); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("duplicate IDs: %v", err)
	}
	deep := "<definitions><process id=\"p\">" + nestedSubProcesses(maxSubProcessDepth+2) + "</process></definitions>"
	if _, err := ParseFlow([]byte(deep)); err == nil {
		t.Error("unbounded nesting was accepted")
	}
}

func nestedSubProcesses(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `<subProcess id="s%d">`, i)
	}
	b.WriteString(strings.Repeat("</subProcess>", n))
	return b.String()
}

func TestListUserTasksInOrder(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/definitions/def-1/export", 200, `{"xml": "`+base64.StdEncoding.EncodeToString([]byte(approvalXML))+`"}`)

	flows, err := client.ListUserTasksInOrder(t.Context(), "def-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(flows[0].Steps); !slices.Equal(got, []string{"Submit", "Checker", "Review", "Approval"}) {
		t.Errorf("order = %v", got)
	}
}
