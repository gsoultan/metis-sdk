package metis

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"slices"
	"strings"
)

// Flow is one <process> of a diagram, with its steps in the order work
// reaches them. A diagram with several pools has a Flow for each.
type Flow struct {
	ID   string
	Name string
	// Steps are the steps of the kinds asked for, in flow order.
	Steps []*FlowStep

	byID map[string]*FlowStep
}

// Step returns the step with the given element ID — a task's
// [UserTask.NodeID] — or nil when the flow has no such step of the kinds
// asked for.
func (f *Flow) Step(id string) *FlowStep { return f.byID[id] }

// FlowStep is one step of a [Flow], linked to the steps that come next and
// before it.
type FlowStep struct {
	Node
	// Order is the step's place in its flow, from 1: a step comes after every
	// step that leads to it, except along a loop back. Steps on parallel or
	// alternative branches are ordered as the diagram declares them.
	Order int
	// Lane is the name of the lane — usually a role, such as "Checker" — the
	// step sits in, empty when the diagram has none.
	Lane string
	// SubProcess is the sub-process directly holding the step, nil at the top
	// level of its flow.
	SubProcess *SubProcess
	// Documentation is the step's instructions, as the diagram wrote them.
	Documentation string
	// Assignment says who a human step is for; nil on other kinds of step.
	Assignment *Assignment
	// MultiInstance is set when the step runs once per item or per person —
	// "three managers approve in parallel" — and nil when it runs once.
	MultiInstance *MultiInstance

	flow           *Flow
	next, previous []*FlowStep
}

// Flow returns the flow the step belongs to.
func (s *FlowStep) Flow() *Flow { return s.flow }

// SubProcess is a sub-process that holds steps, and the one holding it.
type SubProcess struct {
	Node
	// Parent is the sub-process this one sits in, nil at the top level.
	Parent *SubProcess
}

// Depth is how many sub-processes deep the step sits: 0 at the top level of
// its flow. A list indents by it.
func (s *FlowStep) Depth() int {
	depth := 0
	for sub := s.SubProcess; sub != nil; sub = sub.Parent {
		depth++
	}
	return depth
}

// Assignment is who a user task or manual task is meant for, and the form it
// asks them to fill in, as the diagram configures it. Expressions such as
// ${initiator} are kept as written; the engine resolves them when the task is
// created.
type Assignment struct {
	// Assignee is the one person the task goes to directly.
	Assignee string
	// CandidateUsers and CandidateGroups are offered the task, and one of
	// them claims it.
	CandidateUsers  []string
	CandidateGroups []string
	// FormKey names the form the person fills in.
	FormKey string
}

// MultiInstance is how a step that runs more than once does so.
type MultiInstance struct {
	// Sequential runs one after another; otherwise they run in parallel.
	Sequential bool
	// Cardinality is how many runs, a number or an expression as written;
	// empty when the runs come from Collection instead.
	Cardinality string
	// Collection is the variable holding one item per run, and
	// ElementVariable the name each run sees its item under.
	Collection      string
	ElementVariable string
	// CompletionCondition ends the step before every run has finished, such
	// as ${nrOfCompletedInstances >= 2}; empty when every run must finish.
	CompletionCondition string
}

// Next returns the steps that work reaches next from this one, passing over
// gateways, events and steps of the kinds not asked for. A Checker who can
// approve or send back has both the next step and the earlier one; see
// [FlowStep.SendsBackTo]. Empty for the last step.
func (s *FlowStep) Next() []*FlowStep { return s.next }

// Previous returns the steps that have this one among their [FlowStep.Next].
func (s *FlowStep) Previous() []*FlowStep { return s.previous }

// SendsBackTo reports whether work goes from this step back to an earlier or
// the same one — a rejection, a rework — rather than forward.
func (s *FlowStep) SendsBackTo(other *FlowStep) bool {
	return other.Order <= s.Order && slices.Contains(s.next, other)
}

// MarshalJSON writes the links as element IDs, since the steps point at each
// other and a loop back would otherwise never end. Only the holding
// sub-process itself is written, not the ones above it.
func (s *FlowStep) MarshalJSON() ([]byte, error) {
	ids := func(steps []*FlowStep) []string {
		out := make([]string, len(steps))
		for i, step := range steps {
			out[i] = step.ID
		}
		return out
	}
	var subProcess *Node
	if s.SubProcess != nil {
		subProcess = &s.SubProcess.Node
	}
	return json.Marshal(struct {
		Node
		Order         int            `json:"order"`
		Lane          string         `json:"lane,omitempty"`
		SubProcess    *Node          `json:"sub_process,omitempty"`
		Documentation string         `json:"documentation,omitempty"`
		Assignment    *Assignment    `json:"assignment,omitempty"`
		MultiInstance *MultiInstance `json:"multi_instance,omitempty"`
		Next          []string       `json:"next"`
		Previous      []string       `json:"previous"`
	}{s.Node, s.Order, s.Lane, subProcess, s.Documentation, s.Assignment, s.MultiInstance, ids(s.next), ids(s.previous)})
}

// ParseFlow returns each process of a BPMN document with its steps of the
// given kinds — user tasks, say — in the order work reaches them, each linked
// to the steps that come next and before:
//
//	flows, err := metis.ParseFlow(xml, metis.NodeUserTask)
//	for _, step := range flows[0].Steps {
//		fmt.Println(step.Order, step.Name, step.Lane)
//		for _, next := range step.Next() {
//			if step.SendsBackTo(next) {
//				fmt.Println("   can send back to", next.Name)
//			} else {
//				fmt.Println("   then", next.Name)
//			}
//		}
//	}
//
// With no kinds given, every step is returned. Gateways, events and steps of
// other kinds are walked through, not returned. Sub-processes are entered:
// their steps come where the sub-process stands, with [FlowStep.SubProcess]
// naming it. A call activity is a step; the process it calls is not opened.
//
// The order is a topological order of the sequence flows, with loops back
// set aside and ties broken by the document's order. It says what must come
// before what; a step listed earlier on a parallel branch is not done
// earlier.
//
// The same limits as [ParseNodes] hold on untrusted input, and an element ID
// used twice is refused.
func ParseFlow(bpmnXML []byte, types ...NodeType) ([]*Flow, error) {
	var doc flowDocument
	if err := xml.Unmarshal(bpmnXML, &doc); err != nil {
		return nil, fmt.Errorf("metis: parse BPMN: %w", err)
	}
	g := newFlowGraph()
	for p, process := range doc.Processes {
		lanes := map[string]string{}
		if err := collectLanes(process.Children, lanes, 0); err != nil {
			return nil, err
		}
		if err := g.addContainer(p, -1, process.Children, lanes, "", 0); err != nil {
			return nil, err
		}
	}
	flows := make([]*Flow, len(doc.Processes))
	for p, process := range doc.Processes {
		flows[p] = &Flow{ID: process.ID, Name: process.Name, byID: map[string]*FlowStep{}}
	}
	g.fill(flows, types)
	return flows, nil
}

// ListUserTasksInOrder returns the user tasks of a deployed definition in the
// order work reaches them, each linked to the next and previous user task —
// the chain a case travels, "Submit → Checker → Approval", for a progress bar
// or an approval matrix. See [ParseFlow].
//
// It is the design: which steps come after which. Where one running instance
// stands is [Client.ListTasks] with its InstanceID; a task's
// [UserTask.NodeID] finds its step with [Flow.Step].
func (c *Client) ListUserTasksInOrder(ctx context.Context, definitionID string) ([]*Flow, error) {
	bpmnXML, err := c.ExportDefinition(ctx, definitionID)
	if err != nil {
		return nil, err
	}
	return ParseFlow(bpmnXML, NodeUserTask)
}

// flowDocument keeps every child of each <process> in document order, which
// the per-kind slices of bpmnProcess lose and the tie-breaking below needs.
type flowDocument struct {
	XMLName   xml.Name      `xml:"definitions"`
	Processes []flowElement `xml:"process"`
}

type flowElement struct {
	XMLName    xml.Name
	ID         string `xml:"id,attr"`
	Name       string `xml:"name,attr"`
	SourceRef  string `xml:"sourceRef,attr"`
	TargetRef  string `xml:"targetRef,attr"`
	AttachedTo string `xml:"attachedToRef,attr"`
	// A tag without a namespace matches the attribute in any namespace, so
	// these read camunda:assignee and a bare assignee alike, as the server does.
	Assignee        string        `xml:"assignee,attr"`
	CandidateUsers  string        `xml:"candidateUsers,attr"`
	CandidateGroups string        `xml:"candidateGroups,attr"`
	FormKey         string        `xml:"formKey,attr"`
	IsSequential    bool          `xml:"isSequential,attr"`
	Collection      string        `xml:"collection,attr"`
	ElementVariable string        `xml:"elementVariable,attr"`
	Text            string        `xml:",chardata"`
	Children        []flowElement `xml:",any"`
}

// flowNodeTypes maps the elements that are steps to their kind. An end event
// is typed by its children; see endEventType.
var flowNodeTypes = map[string]NodeType{
	"startEvent":             NodeStartEvent,
	"endEvent":               NodeEndEvent,
	"userTask":               NodeUserTask,
	"serviceTask":            NodeServiceTask,
	"scriptTask":             NodeScriptTask,
	"manualTask":             NodeManualTask,
	"businessRuleTask":       NodeBusinessRuleTask,
	"exclusiveGateway":       NodeExclusiveGateway,
	"parallelGateway":        NodeParallelGateway,
	"inclusiveGateway":       NodeInclusiveGateway,
	"eventBasedGateway":      NodeEventBasedGateway,
	"intermediateCatchEvent": NodeIntermediateCatchEvent,
	"intermediateThrowEvent": NodeIntermediateThrowEvent,
	"boundaryEvent":          NodeBoundaryEvent,
	"callActivity":           NodeCallActivity,
	"subProcess":             NodeSubProcess,
	"adHocSubProcess":        NodeSubProcess,
}

func endEventType(e flowElement) NodeType {
	for _, child := range e.Children {
		switch child.XMLName.Local {
		case "terminateEventDefinition":
			return NodeTerminateEndEvent
		case "errorEventDefinition":
			return NodeErrorEndEvent
		}
	}
	return NodeEndEvent
}

// collectLanes maps each flow node to the innermost lane naming it.
func collectLanes(children []flowElement, lanes map[string]string, depth int) error {
	if depth > maxSubProcessDepth {
		return fmt.Errorf("metis: parse BPMN: lanes nested deeper than %d levels", maxSubProcessDepth)
	}
	for _, child := range children {
		switch child.XMLName.Local {
		case "laneSet", "childLaneSet":
			if err := collectLanes(child.Children, lanes, depth+1); err != nil {
				return err
			}
		case "lane":
			for _, ref := range child.Children {
				if ref.XMLName.Local == "flowNodeRef" {
					lanes[strings.TrimSpace(ref.Text)] = child.Name
				}
			}
			// Nested lanes are visited after, so the innermost name wins.
			if err := collectLanes(child.Children, lanes, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// flowVertex is one step while the graph is built.
type flowVertex struct {
	node    Node
	process int // index into the document's processes
	parent  int // the sub-process holding it, -1 at the top level
	lane    string

	documentation string
	assignment    *Assignment
	multiInstance *MultiInstance
}

// stepDetails reads what a step's element configures beyond its kind.
func stepDetails(e flowElement, nodeType NodeType) (doc string, who *Assignment, multi *MultiInstance) {
	for _, child := range e.Children {
		switch child.XMLName.Local {
		case "documentation":
			doc = strings.TrimSpace(child.Text)
		case "multiInstanceLoopCharacteristics":
			multi = &MultiInstance{
				Sequential:      child.IsSequential,
				Collection:      child.Collection,
				ElementVariable: child.ElementVariable,
			}
			for _, part := range child.Children {
				switch part.XMLName.Local {
				case "loopCardinality":
					multi.Cardinality = strings.TrimSpace(part.Text)
				case "completionCondition":
					multi.CompletionCondition = strings.TrimSpace(part.Text)
				}
			}
		}
	}
	if nodeType.IsHumanStep() {
		who = &Assignment{
			Assignee:        e.Assignee,
			CandidateUsers:  splitList(e.CandidateUsers),
			CandidateGroups: splitList(e.CandidateGroups),
			FormKey:         e.FormKey,
		}
	}
	return doc, who, multi
}

// splitList reads a comma-separated attribute, as candidateGroups is written.
func splitList(list string) []string {
	var out []string
	for item := range strings.SplitSeq(list, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

type flowGraph struct {
	vertices []flowVertex // in document order
	index    map[string]int
	edges    [][]int // successors, in document order of the flows
	// pendingFlows are sequence flows whose ends are resolved once every
	// container has been read, since a flow may precede the nodes it joins.
	pendingFlows []flowElement
	// inner holds, per sub-process, the steps directly inside it.
	inner map[int][]int
}

func newFlowGraph() *flowGraph {
	return &flowGraph{index: map[string]int{}, inner: map[int][]int{}}
}

func (g *flowGraph) addContainer(process, parent int, children []flowElement, lanes map[string]string, lane string, depth int) error {
	if depth > maxSubProcessDepth {
		return fmt.Errorf("metis: parse BPMN: sub-processes nested deeper than %d levels", maxSubProcessDepth)
	}
	for _, child := range children {
		local := child.XMLName.Local
		if local == "sequenceFlow" {
			g.pendingFlows = append(g.pendingFlows, child)
			continue
		}
		nodeType, isStep := flowNodeTypes[local]
		if !isStep || child.ID == "" {
			continue
		}
		if _, dup := g.index[child.ID]; dup {
			return fmt.Errorf("metis: parse BPMN: element ID %q is used twice", child.ID)
		}
		if local == "endEvent" {
			nodeType = endEventType(child)
		}
		stepLane := lane
		if named, ok := lanes[child.ID]; ok {
			stepLane = named
		}
		at := len(g.vertices)
		g.index[child.ID] = at
		doc, who, multi := stepDetails(child, nodeType)
		g.vertices = append(g.vertices, flowVertex{
			node:          Node{ID: child.ID, Name: child.Name, Type: nodeType},
			process:       process,
			parent:        parent,
			lane:          stepLane,
			documentation: doc,
			assignment:    who,
			multiInstance: multi,
		})
		g.edges = append(g.edges, nil)
		if parent >= 0 {
			g.inner[parent] = append(g.inner[parent], at)
		}
		if nodeType == NodeSubProcess {
			if err := g.addContainer(process, at, child.Children, lanes, stepLane, depth+1); err != nil {
				return err
			}
		}
		if local == "boundaryEvent" && child.AttachedTo != "" {
			// Resolved with the flows: the host may come later in the file.
			g.pendingFlows = append(g.pendingFlows, flowElement{SourceRef: child.AttachedTo, TargetRef: child.ID})
		}
	}
	return nil
}

// link resolves the flows, then routes a sub-process's flows through its
// inside: into the steps nothing inside leads to, and out of the steps that
// lead nowhere.
func (g *flowGraph) link() {
	for _, flow := range g.pendingFlows {
		from, okFrom := g.index[flow.SourceRef]
		to, okTo := g.index[flow.TargetRef]
		if okFrom && okTo {
			g.edges[from] = append(g.edges[from], to)
		}
	}
	// What the diagram drew, before any routing below rewrites the edges:
	// whether a step is entered, and whether it leads anywhere, is a question
	// about the drawing, and an inner sub-process's rerouting must not change
	// the answer for the one around it.
	drawn := make([][]int, len(g.edges))
	for i, succ := range g.edges {
		drawn[i] = slices.Clone(succ)
	}
	// Innermost first, so an outer sub-process's exits are already routed
	// through the inner ones when it is reached. Steps are in document order
	// and a sub-process precedes its contents, so reverse order is innermost
	// first.
	for sub := len(g.vertices) - 1; sub >= 0; sub-- {
		inside := g.inner[sub]
		if len(inside) == 0 {
			continue
		}
		within := make(map[int]bool, len(inside))
		for _, step := range inside {
			within[step] = true
		}
		entered := map[int]bool{}
		for _, step := range inside {
			for _, to := range drawn[step] {
				if within[to] {
					entered[to] = true
				}
			}
		}
		// A boundary event on the sub-process can fire while any activity
		// inside runs, however deep, so it leaves from each of them, not from
		// the way in.
		var exits, interrupts []int
		for _, to := range drawn[sub] {
			if g.vertices[to].node.Type == NodeBoundaryEvent {
				interrupts = append(interrupts, to)
			} else {
				exits = append(exits, to)
			}
		}
		g.edges[sub] = nil
		for _, step := range inside {
			if !entered[step] && g.vertices[step].node.Type != NodeBoundaryEvent {
				g.edges[sub] = append(g.edges[sub], step)
			}
			if len(drawn[step]) == 0 {
				g.edges[step] = append(g.edges[step], exits...)
			}
		}
		if len(interrupts) == 0 {
			continue
		}
		for _, step := range g.within(sub) {
			if isActivity(g.vertices[step].node.Type) {
				g.edges[step] = append(g.edges[step], interrupts...)
			}
		}
	}
}

// within returns every step inside a sub-process, at any depth. Nesting is
// bounded on the way in, and a stack keeps the walk iterative.
func (g *flowGraph) within(sub int) []int {
	var all []int
	pending := slices.Clone(g.inner[sub])
	for len(pending) > 0 {
		step := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		all = append(all, step)
		pending = append(pending, g.inner[step]...)
	}
	slices.Sort(all)
	return all
}

// fill links the graph and puts the wanted steps into their flows.
func (g *flowGraph) fill(flows []*Flow, types []NodeType) {
	g.link()
	wanted := func(i int) bool {
		return len(types) == 0 || slices.Contains(types, g.vertices[i].node.Type)
	}
	subProcesses := make(map[int]*SubProcess)
	var subProcessAt func(i int) *SubProcess
	subProcessAt = func(i int) *SubProcess {
		if i < 0 {
			return nil
		}
		if sub, ok := subProcesses[i]; ok {
			return sub
		}
		// The parent is created first and nesting is bounded on the way in,
		// so this recursion is as deep as the diagram's, at most.
		sub := &SubProcess{Node: g.vertices[i].node, Parent: subProcessAt(g.vertices[i].parent)}
		subProcesses[i] = sub
		return sub
	}

	built := make(map[int]*FlowStep)
	for _, i := range g.topologicalOrder() {
		if !wanted(i) {
			continue
		}
		v := g.vertices[i]
		flow := flows[v.process]
		step := &FlowStep{
			Node:          v.node,
			Order:         len(flow.Steps) + 1,
			Lane:          v.lane,
			SubProcess:    subProcessAt(v.parent),
			Documentation: v.documentation,
			Assignment:    v.assignment,
			MultiInstance: v.multiInstance,
			flow:          flow,
		}
		flow.Steps = append(flow.Steps, step)
		flow.byID[step.ID] = step
		built[i] = step
	}
	for _, flow := range flows {
		for _, step := range flow.Steps {
			for _, next := range g.nextWanted(g.index[step.ID], wanted) {
				target := built[next]
				step.next = append(step.next, target)
				target.previous = append(target.previous, step)
			}
		}
	}
}

// nextWanted walks forward from a step to the first wanted steps on each path,
// in flow order.
func (g *flowGraph) nextWanted(from int, wanted func(int) bool) []int {
	seen := map[int]bool{from: true}
	queue := slices.Clone(g.edges[from])
	var found []int
	for len(queue) > 0 {
		step := queue[0]
		queue = queue[1:]
		if seen[step] {
			if step == from && !slices.Contains(found, from) {
				found = append(found, from) // a loop straight back to itself
			}
			continue
		}
		seen[step] = true
		if wanted(step) {
			found = append(found, step)
			continue
		}
		queue = append(queue, g.edges[step]...)
	}
	return found
}

// topologicalOrder orders the steps so each comes after what leads to it,
// once the flows that loop back have been set aside. Ties go to document
// order.
func (g *flowGraph) topologicalOrder() []int {
	n := len(g.vertices)
	back := make([]map[int]bool, n)
	state := make([]uint8, n) // 0 unseen, 1 on the path, 2 done
	hasIncoming := make([]bool, n)
	for _, succ := range g.edges {
		for _, to := range succ {
			hasIncoming[to] = true
		}
	}
	// Iterative depth-first search, so a long diagram cannot exhaust the
	// stack. Roots are the steps nothing leads to, then anything left over:
	// a cycle nothing enters.
	type frame struct{ step, next int }
	visit := func(root int) {
		stack := []frame{{root, 0}}
		state[root] = 1
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.next == len(g.edges[top.step]) {
				state[top.step] = 2
				stack = stack[:len(stack)-1]
				continue
			}
			to := g.edges[top.step][top.next]
			top.next++
			switch state[to] {
			case 0:
				state[to] = 1
				stack = append(stack, frame{to, 0})
			case 1:
				if back[top.step] == nil {
					back[top.step] = map[int]bool{}
				}
				back[top.step][to] = true
			}
		}
	}
	for i := range n {
		if !hasIncoming[i] && state[i] == 0 {
			visit(i)
		}
	}
	for i := range n {
		if state[i] == 0 {
			visit(i)
		}
	}

	indegree := make([]int, n)
	for from, succ := range g.edges {
		for _, to := range succ {
			if !back[from][to] {
				indegree[to]++
			}
		}
	}
	var ready []int
	for i := range n {
		if indegree[i] == 0 {
			ready = append(ready, i)
		}
	}
	order := make([]int, 0, n)
	for len(ready) > 0 {
		// The earliest-declared ready step goes first. ready stays sorted.
		step := ready[0]
		ready = ready[1:]
		order = append(order, step)
		for _, to := range g.edges[step] {
			if back[step][to] {
				continue
			}
			indegree[to]--
			if indegree[to] == 0 {
				at, _ := slices.BinarySearch(ready, to)
				ready = slices.Insert(ready, at, to)
			}
		}
	}
	return order
}

// isActivity reports whether work waits on a step of this kind — what a
// boundary event can interrupt — as opposed to a gateway or an event.
func isActivity(t NodeType) bool {
	switch t {
	case NodeUserTask, NodeServiceTask, NodeScriptTask, NodeManualTask,
		NodeBusinessRuleTask, NodeCallActivity, NodeSubProcess:
		return true
	}
	return false
}
