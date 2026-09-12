package metis

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// A complete little diagram: two human steps, an external one, a gateway, and
// an end event, under the namespace prefix a real export carries.
const sampleBPMN = `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL" id="defs">
  <bpmn:process id="refund" name="Refund a customer">
    <bpmn:startEvent id="start" name="Request arrives"/>
    <bpmn:serviceTask id="charge" name="Reverse the charge" topic="reverse-charge"/>
    <bpmn:userTask id="approve" name="Approve the refund"/>
    <bpmn:exclusiveGateway id="big-enough" name="Over 1000?"/>
    <bpmn:userTask id="escalate" name="Second approval"/>
    <bpmn:manualTask id="file" name="File the paperwork"/>
    <bpmn:endEvent id="done" name="Refunded"/>
    <bpmn:sequenceFlow id="f1" sourceRef="start" targetRef="charge"/>
  </bpmn:process>
</bpmn:definitions>`

func TestParseNodesReadsEveryKind(t *testing.T) {
	nodes, err := ParseNodes([]byte(sampleBPMN))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}

	for id, want := range map[string]NodeType{
		"start":      NodeStartEvent,
		"charge":     NodeServiceTask,
		"approve":    NodeUserTask,
		"big-enough": NodeExclusiveGateway,
		"escalate":   NodeUserTask,
		"file":       NodeManualTask,
		"done":       NodeEndEvent,
	} {
		node, found := byID[id]
		if !found {
			t.Errorf("node %q missing", id)
			continue
		}
		if node.Type != want {
			t.Errorf("node %q is %q, want %q", id, node.Type, want)
		}
	}

	if got := byID["approve"].Name; got != "Approve the refund" {
		t.Errorf("name = %q", got)
	}
	// A sequenceFlow is a connection, not a step.
	if _, found := byID["f1"]; found {
		t.Error("a sequence flow was returned as a node")
	}
}

// The namespace prefix a diagram uses is the exporting tool's choice, and the
// same process arrives as bpmn:, semantic: or no prefix at all. Matching on
// local name is what keeps all three working.
func TestParseNodesIgnoresNamespacePrefixes(t *testing.T) {
	prefixed := ParseOrFail(t, sampleBPMN)

	bare := ParseOrFail(t, `<definitions xmlns="http://www.omg.org/spec/BPMN/20100524/MODEL">
	  <process id="refund"><userTask id="approve" name="Approve the refund"/></process>
	</definitions>`)
	other := ParseOrFail(t, `<semantic:definitions xmlns:semantic="http://www.omg.org/spec/BPMN/20100524/MODEL">
	  <semantic:process id="refund"><semantic:userTask id="approve" name="Approve the refund"/></semantic:process>
	</semantic:definitions>`)

	for _, nodes := range [][]Node{bare, other} {
		userTasks := NodesOfType(nodes, NodeUserTask)
		if len(userTasks) != 1 || userTasks[0].ID != "approve" {
			t.Errorf("got %+v", userTasks)
		}
	}
	if len(NodesOfType(prefixed, NodeUserTask)) != 2 {
		t.Error("the prefixed sample lost its user tasks")
	}
}

func ParseOrFail(t *testing.T, xml string) []Node {
	t.Helper()
	nodes, err := ParseNodes([]byte(xml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return nodes
}

// A user task inside a sub-process is still a human step of the process, so it
// has to come back. This is also the case that broke in the engine's own
// parser: a sub-process whose id parsed empty took its children with it.
func TestParseNodesDescendsIntoSubProcesses(t *testing.T) {
	nodes := ParseOrFail(t, `<definitions>
	  <process id="p">
	    <userTask id="top" name="Top level"/>
	    <subProcess id="review" name="Review stage">
	      <userTask id="inner" name="Inner approval"/>
	      <subProcess id="deep" name="Deeper">
	        <userTask id="deepest" name="Deepest approval"/>
	      </subProcess>
	    </subProcess>
	  </process>
	</definitions>`)

	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}

	// The sub-process itself keeps its ID and name — the ambiguous-embedding
	// bug showed up as exactly these being empty.
	sub, found := byID["review"]
	if !found || sub.Type != NodeSubProcess || sub.Name != "Review stage" {
		t.Errorf("sub-process = %+v (found %v)", sub, found)
	}

	for _, id := range []string{"top", "inner", "deepest"} {
		node, found := byID[id]
		if !found {
			t.Errorf("user task %q was not reached", id)
			continue
		}
		if node.Type != NodeUserTask {
			t.Errorf("%q is %q", id, node.Type)
		}
	}
	if got := len(NodesOfType(nodes, NodeUserTask)); got != 3 {
		t.Errorf("found %d user tasks, want 3", got)
	}
}

// The walk calls itself, and a Go stack overflow cannot be recovered — it takes
// the process down rather than failing the call. The depth bound is what makes
// a hostile document an error instead.
func TestParseNodesRefusesUnboundedNesting(t *testing.T) {
	depth := maxSubProcessDepth + 10

	var b strings.Builder
	b.WriteString(`<definitions><process id="p">`)
	for i := range depth {
		fmt.Fprintf(&b, `<subProcess id="s%d">`, i)
	}
	b.WriteString(strings.Repeat(`</subProcess>`, depth))
	b.WriteString(`</process></definitions>`)

	if _, err := ParseNodes([]byte(b.String())); err == nil {
		t.Fatal("a document nested past the bound was accepted")
	}

	// And the bound is generous enough that real diagrams pass it.
	var ok strings.Builder
	ok.WriteString(`<definitions><process id="p">`)
	for i := range 10 {
		fmt.Fprintf(&ok, `<subProcess id="s%d">`, i)
	}
	ok.WriteString(`<userTask id="buried" name="Buried"/>`)
	ok.WriteString(strings.Repeat(`</subProcess>`, 10))
	ok.WriteString(`</process></definitions>`)

	nodes, err := ParseNodes([]byte(ok.String()))
	if err != nil {
		t.Fatalf("ten levels was refused: %v", err)
	}
	if len(NodesOfType(nodes, NodeUserTask)) != 1 {
		t.Error("the buried user task was lost")
	}
}

// These hold because encoding/xml is strict: it refuses an entity it was not
// given and never fetches an external one. Pinned because they are one line
// from being untrue — the tempting fix for "invalid character entity" on some
// other tool's export is Strict = false or an Entity map, and either turns
// every case here into a working attack.
func TestParseNodesRefusesHostileXML(t *testing.T) {
	cases := map[string]string{
		"an entity that expands exponentially": `<?xml version="1.0"?>
<!DOCTYPE definitions [
 <!ENTITY lol "lol">
 <!ENTITY lol1 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
 <!ENTITY lol2 "&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;">
 <!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">
]>
<definitions><process id="&lol3;"><userTask id="t"/></process></definitions>`,

		"an external entity naming a local file": `<?xml version="1.0"?>
<!DOCTYPE definitions [ <!ENTITY xxe SYSTEM "file:///etc/passwd"> ]>
<definitions><process id="&xxe;"><userTask id="t"/></process></definitions>`,

		"an external entity naming the cloud metadata endpoint": `<?xml version="1.0"?>
<!DOCTYPE definitions [ <!ENTITY xxe SYSTEM "http://169.254.169.254/latest/meta-data/iam/"> ]>
<definitions><process id="&xxe;"><userTask id="t"/></process></definitions>`,
	}

	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseNodes([]byte(payload)); err == nil {
				t.Fatal("the parser accepted it")
			}
		})
	}
}

func TestParseNodesRejectsWhatIsNotBPMN(t *testing.T) {
	for name, payload := range map[string]string{
		"not XML at all":   `{"nodes": []}`,
		"truncated":        `<definitions><process id="p"><userTask id="t"/>`,
		"a different root": `<html><body><process id="p"/></body></html>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseNodes([]byte(payload)); err == nil {
				t.Error("accepted")
			}
		})
	}

	// A well-formed document with nothing in it is not an error; it is a
	// diagram with no steps, and nil is the honest answer.
	nodes, err := ParseNodes([]byte(`<definitions/>`))
	if err != nil {
		t.Fatalf("an empty definitions was refused: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("got %+v", nodes)
	}
}

// A collaboration has several pools, each its own <process>. All of them are
// part of the file, so all of their steps come back.
func TestParseNodesCoversEveryProcess(t *testing.T) {
	nodes := ParseOrFail(t, `<definitions>
	  <process id="buyer"><userTask id="order" name="Place the order"/></process>
	  <process id="seller"><userTask id="ship" name="Ship it"/></process>
	</definitions>`)

	if got := len(NodesOfType(nodes, NodeUserTask)); got != 2 {
		t.Fatalf("found %d user tasks across two processes, want 2", got)
	}
}

// An end event's flavour is in its children, not its element name.
func TestParseNodesTypesEndEvents(t *testing.T) {
	nodes := ParseOrFail(t, `<definitions><process id="p">
	  <endEvent id="plain" name="Done"/>
	  <endEvent id="boom"><errorEventDefinition errorRef="e"/></endEvent>
	  <endEvent id="halt"><terminateEventDefinition/></endEvent>
	</process></definitions>`)

	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	for id, want := range map[string]NodeType{
		"plain": NodeEndEvent,
		"boom":  NodeErrorEndEvent,
		"halt":  NodeTerminateEndEvent,
	} {
		if got := byID[id].Type; got != want {
			t.Errorf("%q is %q, want %q", id, got, want)
		}
	}
}

func TestNodesOfType(t *testing.T) {
	nodes := []Node{
		{ID: "a", Type: NodeUserTask},
		{ID: "b", Type: NodeServiceTask},
		{ID: "c", Type: NodeManualTask},
		{ID: "d", Type: NodeUserTask},
	}

	if got := NodesOfType(nodes, NodeUserTask); len(got) != 2 || got[0].ID != "a" || got[1].ID != "d" {
		t.Errorf("single type = %+v", got)
	}
	// Several types, and the original order is kept rather than grouped.
	if got := NodesOfType(nodes, NodeUserTask, NodeManualTask); len(got) != 3 || got[1].ID != "c" {
		t.Errorf("several types = %+v", got)
	}
	if got := NodesOfType(nodes, NodeTimerEvent); got != nil {
		t.Errorf("no match should be nil, got %+v", got)
	}
	if got := NodesOfType(nil, NodeUserTask); got != nil {
		t.Errorf("nil input = %+v", got)
	}
}

func TestNodeTypeIsHumanStep(t *testing.T) {
	for _, human := range []NodeType{NodeUserTask, NodeManualTask} {
		if !human.IsHumanStep() {
			t.Errorf("%q should be a human step", human)
		}
	}
	for _, machine := range []NodeType{NodeServiceTask, NodeScriptTask, NodeStartEvent, NodeSubProcess} {
		if machine.IsHumanStep() {
			t.Errorf("%q should not be a human step", machine)
		}
	}
}

// The client methods are the parser plus one export call, so what is worth
// asserting is that they hit the right endpoint and filter to the right kind.
func TestListUserTaskNodes(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/definitions/def-1/export", 200,
		`{"xml":"`+base64.StdEncoding.EncodeToString([]byte(sampleBPMN))+`"}`)

	nodes, err := client.ListUserTaskNodes(t.Context(), "def-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d user tasks: %+v", len(nodes), nodes)
	}
	// Document order within the kind, so a reader can follow the diagram.
	if nodes[0].ID != "approve" || nodes[1].ID != "escalate" {
		t.Errorf("order = %+v", nodes)
	}
	if nodes[0].Name != "Approve the refund" {
		t.Errorf("name = %q", nodes[0].Name)
	}
	// The manual task is a human step but a different element, and this method
	// promises only userTask.
	for _, node := range nodes {
		if node.Type != NodeUserTask {
			t.Errorf("returned a %q", node.Type)
		}
	}

	if path := f.last().Path; path != "/api/v1/definitions/def-1/export" {
		t.Errorf("path = %q", path)
	}
}

func TestListDefinitionNodesReturnsEverything(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/definitions/def-1/export", 200,
		`{"xml":"`+base64.StdEncoding.EncodeToString([]byte(sampleBPMN))+`"}`)

	nodes, err := client.ListDefinitionNodes(t.Context(), "def-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nodes) != 7 {
		t.Fatalf("got %d nodes, want 7: %+v", len(nodes), nodes)
	}
	// And the both-kinds-of-human-step recipe from the doc works.
	if got := NodesOfType(nodes, NodeUserTask, NodeManualTask); len(got) != 3 {
		t.Errorf("human steps = %+v", got)
	}
}

// A failure to export is the caller's answer, not a parse error about empty
// input — the distinction matters when the definition simply is not theirs.
func TestListUserTaskNodesPassesExportFailuresThrough(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/definitions/def-1/export", 404, `{"error":"no such definition"}`)

	if _, err := client.ListUserTaskNodes(t.Context(), "def-1"); !IsNotFound(err) {
		t.Errorf("err = %v, want the underlying 404", err)
	}
}
