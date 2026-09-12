package metis

import (
	"encoding/xml"
	"fmt"
)

// maxSubProcessDepth bounds how far ParseNodes will descend into nested
// sub-processes.
//
// The walk below calls itself, and a Go stack overflow cannot be recovered —
// it takes the process down rather than failing the call. encoding/xml stops
// its own decoding around ten thousand levels, but relying on that means the
// safety of this package depends on an implementation detail of another one.
// A hundred is far past any diagram a person has drawn, and being refused is a
// better answer than a crash.
const maxSubProcessDepth = 100

// bpmnDefinitions is the root <definitions> element.
//
// The struct tags name elements by their local name, with no namespace, which
// is how encoding/xml matches them: <bpmn:userTask>, <semantic:userTask> and a
// bare <userTask> all land in the same field. That is deliberate — diagrams
// exported by different tools use different prefixes for the same BPMN.
type bpmnDefinitions struct {
	XMLName   xml.Name      `xml:"definitions"`
	Processes []bpmnProcess `xml:"process"`
}

// bpmnProcess is one <process>, and the shape a <subProcess> reuses for its
// children.
type bpmnProcess struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name,attr"`

	StartEvents             []bpmnFlowNode   `xml:"startEvent"`
	EndEvents               []bpmnEndEvent   `xml:"endEvent"`
	UserTasks               []bpmnFlowNode   `xml:"userTask"`
	ServiceTasks            []bpmnFlowNode   `xml:"serviceTask"`
	ScriptTasks             []bpmnFlowNode   `xml:"scriptTask"`
	ManualTasks             []bpmnFlowNode   `xml:"manualTask"`
	BusinessRuleTasks       []bpmnFlowNode   `xml:"businessRuleTask"`
	ExclusiveGateways       []bpmnFlowNode   `xml:"exclusiveGateway"`
	ParallelGateways        []bpmnFlowNode   `xml:"parallelGateway"`
	InclusiveGateways       []bpmnFlowNode   `xml:"inclusiveGateway"`
	EventBasedGateways      []bpmnFlowNode   `xml:"eventBasedGateway"`
	IntermediateCatchEvents []bpmnFlowNode   `xml:"intermediateCatchEvent"`
	IntermediateThrowEvents []bpmnFlowNode   `xml:"intermediateThrowEvent"`
	BoundaryEvents          []bpmnFlowNode   `xml:"boundaryEvent"`
	CallActivities          []bpmnFlowNode   `xml:"callActivity"`
	SubProcesses            []bpmnSubProcess `xml:"subProcess"`
}

// bpmnFlowNode is any element that is only a step: an id and a label.
type bpmnFlowNode struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

// bpmnEndEvent is an <endEvent>, which carries its flavour as a child element
// rather than in its name.
type bpmnEndEvent struct {
	ID                       string    `xml:"id,attr"`
	Name                     string    `xml:"name,attr"`
	TerminateEventDefinition *struct{} `xml:"terminateEventDefinition"`
	ErrorEventDefinition     *struct{} `xml:"errorEventDefinition"`
}

// nodeType reports which kind of end event this is.
func (e bpmnEndEvent) nodeType() NodeType {
	switch {
	case e.TerminateEventDefinition != nil:
		return NodeTerminateEndEvent
	case e.ErrorEventDefinition != nil:
		return NodeErrorEndEvent
	default:
		return NodeEndEvent
	}
}

// bpmnSubProcess is a <subProcess>: at once a step and a container of steps.
//
// ID and Name are declared here rather than inherited from the embedded
// bpmnProcess, and that is load-bearing. Embedding two structs that each
// declare `id,attr` makes the field ambiguous, and encoding/xml resolves an
// ambiguity by silently dropping the field — which parses every sub-process
// with an empty ID and leaves its children unreachable. A field declared
// directly is shallower than an embedded one, so it wins outright.
type bpmnSubProcess struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name,attr"`

	bpmnProcess
}

// ParseNodes returns every node in a BPMN 2.0 document — the steps of the
// diagram, each with its element ID, its label, and its [NodeType].
//
// This is what [Client.ListDefinitionNodes] runs on the XML the server exports.
// Use it directly to inspect a diagram you have not deployed yet:
//
//	nodes, err := metis.ParseNodes(xml)
//	for _, node := range metis.NodesOfType(nodes, metis.NodeUserTask) {
//		fmt.Println(node.ID, node.Name)
//	}
//
// Nodes inside sub-processes are included, flattened into the same list — a
// human step is a human step wherever it sits, and BPMN element IDs are unique
// across the document. Every <process> in the file contributes, so a
// collaboration with several pools returns all of their nodes.
//
// Ordering is by element kind, then document order within a kind. So the result
// of filtering to a single type — which is what [Client.ListUserTaskNodes]
// does — is in the order the diagram declares them.
//
// # On untrusted input
//
// Entity attacks are refused, not by anything here but because encoding/xml is
// strict: it rejects an entity it was not given and never fetches an external
// one. Both properties are pinned by tests, because the tempting fix for
// "invalid character entity" on some other tool's export — Strict = false, or
// handing the decoder an Entity map — would quietly turn a billion-laughs
// payload or a file:///etc/passwd reference into a working attack.
//
// Nesting is bounded here, at [maxSubProcessDepth], because the walk recurses
// and a stack overflow is not a recoverable error.
//
// What is not bounded is the size of bpmnXML itself; that is the caller's to
// decide. Documents fetched through this client are, since every response body
// is read under a limit.
func ParseNodes(bpmnXML []byte) ([]Node, error) {
	// The XMLName field on bpmnDefinitions is what rejects a document whose root
	// is not <definitions>: encoding/xml checks the tag and fails the unmarshal
	// with "expected element type <definitions> but have <html>". It is load
	// bearing rather than decorative — without it, any well-formed XML would
	// parse to zero nodes and look like an empty diagram.
	var definitions bpmnDefinitions
	if err := xml.Unmarshal(bpmnXML, &definitions); err != nil {
		return nil, fmt.Errorf("metis: parse BPMN: %w", err)
	}

	var nodes []Node
	for _, process := range definitions.Processes {
		collected, err := collectNodes(process, 0)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, collected...)
	}
	return nodes, nil
}

// collectNodes flattens one process, and the sub-processes below it, into nodes.
func collectNodes(process bpmnProcess, depth int) ([]Node, error) {
	if depth > maxSubProcessDepth {
		return nil, fmt.Errorf("metis: parse BPMN: sub-processes nested deeper than %d levels", maxSubProcessDepth)
	}

	var nodes []Node
	add := func(flowNodes []bpmnFlowNode, nodeType NodeType) {
		for _, flowNode := range flowNodes {
			nodes = append(nodes, Node{ID: flowNode.ID, Name: flowNode.Name, Type: nodeType})
		}
	}

	add(process.StartEvents, NodeStartEvent)
	add(process.UserTasks, NodeUserTask)
	add(process.ServiceTasks, NodeServiceTask)
	add(process.ScriptTasks, NodeScriptTask)
	add(process.ManualTasks, NodeManualTask)
	add(process.BusinessRuleTasks, NodeBusinessRuleTask)
	add(process.ExclusiveGateways, NodeExclusiveGateway)
	add(process.ParallelGateways, NodeParallelGateway)
	add(process.InclusiveGateways, NodeInclusiveGateway)
	add(process.EventBasedGateways, NodeEventBasedGateway)
	add(process.IntermediateCatchEvents, NodeIntermediateCatchEvent)
	add(process.IntermediateThrowEvents, NodeIntermediateThrowEvent)
	add(process.BoundaryEvents, NodeBoundaryEvent)
	add(process.CallActivities, NodeCallActivity)

	// An end event's kind is in its children, not its element name.
	for _, endEvent := range process.EndEvents {
		nodes = append(nodes, Node{ID: endEvent.ID, Name: endEvent.Name, Type: endEvent.nodeType()})
	}

	for _, subProcess := range process.SubProcesses {
		nodes = append(nodes, Node{ID: subProcess.ID, Name: subProcess.Name, Type: NodeSubProcess})
		nested, err := collectNodes(subProcess.bpmnProcess, depth+1)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, nested...)
	}

	return nodes, nil
}

// NodesOfType returns the nodes whose type is one of those given, keeping their
// order. It returns nil rather than an empty slice when nothing matches, so a
// caller can test the result with len or against nil either way.
//
//	humanSteps := metis.NodesOfType(nodes, metis.NodeUserTask, metis.NodeManualTask)
func NodesOfType(nodes []Node, types ...NodeType) []Node {
	var matched []Node
	for _, node := range nodes {
		for _, wanted := range types {
			if node.Type == wanted {
				matched = append(matched, node)
				break
			}
		}
	}
	return matched
}
