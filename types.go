package metis

import "time"

// The types here are deliberately leaner than the server's: they carry the
// fields an integrating application acts on, and unknown fields in responses
// are ignored, so newer servers keep working with older SDKs.

// ProcessStatus is where an instance is in its life. See [Instance.Status].
type ProcessStatus string

const (
	// ProcessActive is running, or waiting on a task, message or timer.
	ProcessActive ProcessStatus = "active"
	// ProcessCompleted reached an end event. Terminal.
	ProcessCompleted ProcessStatus = "completed"
	// ProcessSuspended was paused by an operator. It resumes where it stopped.
	ProcessSuspended ProcessStatus = "suspended"
	// ProcessFailed hit an incident nothing handled. Terminal without operator
	// intervention.
	ProcessFailed ProcessStatus = "failed"
)

// IsFinished reports whether the instance has stopped for good — completed or
// failed. A suspended instance is not finished; it is waiting for an operator.
func (s ProcessStatus) IsFinished() bool {
	return s == ProcessCompleted || s == ProcessFailed
}

// TaskStatus is where a human task is in its life. See [UserTask.Status].
type TaskStatus string

const (
	// TaskUnclaimed is in the inbox, nobody working it. The server also calls
	// this "pending".
	TaskUnclaimed TaskStatus = "unclaimed"
	// TaskClaimed is held by its assignee, who is expected to complete it.
	TaskClaimed TaskStatus = "claimed"
	// TaskCompleted was finished and the instance moved on. Terminal.
	TaskCompleted TaskStatus = "completed"
	// TaskCanceled will not be worked — its instance ended or was withdrawn.
	// Terminal.
	TaskCanceled TaskStatus = "canceled"
	// TaskDelegated was handed to somebody else to act on.
	TaskDelegated TaskStatus = "delegated"
	// TaskEscalated was raised for attention, typically on a missed due date.
	TaskEscalated TaskStatus = "escalated"
)

// IsOpen reports whether the task still needs somebody — anything but
// completed or canceled. This is the predicate an inbox filters on.
func (s TaskStatus) IsOpen() bool {
	return s != TaskCompleted && s != TaskCanceled
}

// PageInfo reports where a listing sits in its full result set.
type PageInfo struct {
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	HasMore  bool  `json:"has_more"`
}

// Definition is a deployed process template — a parsed BPMN diagram. It does
// not run; [Client.StartProcess] creates an [Instance] of it.
//
// Key and ID are different on purpose. Key is the process ID from the diagram
// and is stable across deployments — it is what you start a process by.
// ID names one *version*: redeploying the same key mints a new ID, and running
// instances keep the one they started with.
type Definition struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	Version int    `json:"version"`
	// CreatedAt is when this version was deployed. Listings are ordered by it,
	// newest first.
	CreatedAt time.Time `json:"created_at,omitzero"`
	// Nodes is the diagram's steps. Empty in a listing — the server selects
	// scalar columns there and never loads the graph — so use
	// [Client.ListDefinitionNodes] to read the steps of one definition.
	Nodes []Node `json:"nodes,omitempty"`
}

// DefinitionRef is the former name of [Definition].
//
// Deprecated: use [Definition]. This alias will be removed in a future release.
type DefinitionRef = Definition

// NodeType is the kind of BPMN element a node is — which step of the diagram
// this is. See [UserTask.Type].
type NodeType string

// The element kinds the engine recognises.
const (
	NodeStartEvent             NodeType = "startEvent"
	NodeEndEvent               NodeType = "endEvent"
	NodeErrorEndEvent          NodeType = "errorEndEvent"
	NodeTerminateEndEvent      NodeType = "terminateEndEvent"
	NodeUserTask               NodeType = "userTask"
	NodeServiceTask            NodeType = "serviceTask"
	NodeScriptTask             NodeType = "scriptTask"
	NodeManualTask             NodeType = "manualTask"
	NodeBusinessRuleTask       NodeType = "businessRuleTask"
	NodeExclusiveGateway       NodeType = "exclusiveGateway"
	NodeParallelGateway        NodeType = "parallelGateway"
	NodeInclusiveGateway       NodeType = "inclusiveGateway"
	NodeEventBasedGateway      NodeType = "eventBasedGateway"
	NodeIntermediateCatchEvent NodeType = "intermediateCatchEvent"
	NodeIntermediateThrowEvent NodeType = "intermediateThrowEvent"
	NodeBoundaryEvent          NodeType = "boundaryEvent"
	NodeCallActivity           NodeType = "callActivity"
	NodeSubProcess             NodeType = "subProcess"
	NodeMessageEvent           NodeType = "messageEvent"
	NodeSignalEvent            NodeType = "signalEvent"
	NodeTimerEvent             NodeType = "timerEvent"
	NodeEscalationThrowEvent   NodeType = "escalationThrowEvent"
	NodeCompensationThrowEvent NodeType = "compensationThrowEvent"
	// NodePool and NodeLane describe a collaboration's structure rather than a
	// step in it. The engine produces them when a diagram has pools; they never
	// appear on a task.
	NodePool NodeType = "pool"
	NodeLane NodeType = "lane"
)

// IsHumanStep reports whether a node of this type waits for a person —
// [NodeUserTask], which has a form to fill in, or [NodeManualTask], which is
// work done outside the system and merely acknowledged.
func (t NodeType) IsHumanStep() bool {
	return t == NodeUserTask || t == NodeManualTask
}

// Node identifies one step of a definition — a BPMN element, under the ID the
// diagram gave it.
//
// How much of it arrives depends on where it came from:
//
//   - From [Client.ListDefinitionNodes] and [ParseNodes], everything: the
//     diagram is being read directly, so ID, Name and Type are all there.
//   - On a [UserTask], the ID and — from servers new enough to send it — the
//     Type. [UserTask.Type] carries the same kind and does not depend on the
//     server's age, so it is the one to read.
//   - On an [ExternalTask], the ID alone. The engine does not store the node's
//     kind against an external task, so there is nothing to send.
//
// Name is empty everywhere except when parsed from a definition. A task can be
// renamed after it is created, which detaches its name from the diagram's
// label, and the server declines to guess which of the two a caller wanted —
// [UserTask.Name] is the label to display.
type Node struct {
	ID string `json:"id"`
	// Name is the diagram's label. Only populated when the node came from a
	// parsed definition; see the type's doc.
	Name string `json:"name"`
	// Type is the element kind. On a task, prefer [UserTask.Type], which every
	// server sends.
	Type NodeType `json:"type"`
}

// NodeRef is the former name of [Node].
//
// Deprecated: use [Node]. This alias will be removed in a future release.
type NodeRef = Node

// Instance is one execution of a [Definition] — the thing that actually runs.
// Many instances of one definition run at once, each with its own Variables.
type Instance struct {
	ID string `json:"id"`
	// Status is active, completed, suspended or failed. Compare against the
	// ProcessStatus constants rather than string literals.
	Status ProcessStatus `json:"status"`
	// Definition is the version this instance is executing. Nil in responses
	// that do not expand it.
	Definition *Definition `json:"definition,omitempty"`
	// Variables is the instance's data as of this response.
	Variables Variables `json:"variables,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// IsFinished reports whether the instance has stopped for good.
func (i Instance) IsFinished() bool { return i.Status.IsFinished() }

// ProcessInstance is the former name of [Instance].
//
// Deprecated: use [Instance]. This alias will be removed in a future release.
type ProcessInstance = Instance

// User identifies a person well enough to display them.
type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name,omitempty"`
}

// UserRef is the former name of [User].
//
// Deprecated: use [User]. This alias will be removed in a future release.
type UserRef = User

// UserTask is a step waiting for a *person* — a BPMN userTask, sitting in an
// inbox. It does not expire: it waits as long as the person takes.
//
// The cycle is list, claim, complete — [Client.ListTasks], [Client.ClaimTask],
// [Client.CompleteTask]. Claiming is what stops two people working it at once.
//
// Its sibling is [ExternalTask], which is the same idea for work a *program*
// does. If you are writing a service that performs a process step rather than a
// screen a human uses, that is the type you want.
type UserTask struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Type is the kind of BPMN element this task came from — normally
	// [NodeUserTask], and [NodeManualTask] for a step with no form to fill in.
	// This is the field that carries the node's kind; Node.Type does not.
	Type NodeType `json:"type,omitempty"`
	// Status is unclaimed, claimed, completed, canceled, delegated or
	// escalated. Compare against the TaskStatus constants.
	Status TaskStatus `json:"status"`
	// Priority as set on the diagram; higher is more urgent. Zero when unset.
	Priority int `json:"priority,omitempty"`
	// DueDate is when this was meant to be done. Nil when the task has no
	// deadline — passing it does not cancel the task.
	DueDate *time.Time `json:"due_date,omitempty"`
	// FormKey names the form your UI should render for this task, as the
	// diagram set it. The SDK does not interpret it.
	FormKey string `json:"form_key,omitempty"`
	// Assignee is whoever holds the task. Nil while unclaimed.
	Assignee *User `json:"assignee,omitempty"`
	// Node is the step of the definition this task was created for.
	Node *Node `json:"node,omitempty"`
	// Instance is the execution this task belongs to.
	Instance *Instance `json:"instance,omitempty"`
	// Variables the task was created with. What you pass to CompleteTask is
	// written back over these.
	Variables Variables `json:"variables,omitempty"`
	// CreatedAt is when the instance reached this step. Listings are ordered by
	// it, newest first.
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// IsOpen reports whether the task still needs somebody.
func (t UserTask) IsOpen() bool { return t.Status.IsOpen() }

// AssigneeUsername returns who holds the task, or "" when nobody does.
func (t UserTask) AssigneeUsername() string {
	if t.Assignee == nil {
		return ""
	}
	return t.Assignee.Username
}

// InstanceID returns the ID of the instance this task belongs to, or "" when
// the response did not expand it.
func (t UserTask) InstanceID() string {
	if t.Instance == nil {
		return ""
	}
	return t.Instance.ID
}

// NodeID returns the BPMN element ID this task was created for — the ID from
// the diagram — or "" when the response carried no node.
func (t UserTask) NodeID() string {
	if t.Node == nil {
		return ""
	}
	return t.Node.ID
}

// Task is the former name of [UserTask].
//
// Deprecated: use [UserTask]. The package has two kinds of task and the bare
// name did not say which. This alias will be removed in a future release.
type Task = UserTask

// ExternalTask is a step waiting for a *program* — a BPMN serviceTask published
// on a topic, for your own service to perform. Charge a card, send a letter,
// call an API the engine cannot reach itself.
//
// Unlike a [UserTask] it carries a *lock* rather than a claim, because a
// program can crash: the task is yours only until LockExpiration, after which
// it returns to the pool and another worker takes it. That is also why handlers
// must be idempotent — see [Worker].
//
// [Worker] runs the fetch-work-report loop for you. The pieces are
// [Client.FetchAndLock], [Client.CompleteExternalTask] and
// [Client.FailExternalTask] if you would rather drive it yourself.
type ExternalTask struct {
	ID string `json:"id"`
	// Topic is the queue name from the diagram; a worker subscribes to one.
	Topic string `json:"topic"`
	// Retries left after this attempt. Zero means this is the last one, and
	// failing it leaves the task for an operator.
	Retries int `json:"retries,omitempty"`
	// LockExpiration is when this task stops being yours. Work past it may be
	// done twice, by you and by whoever picks it up next.
	LockExpiration *time.Time `json:"lock_expiration,omitempty"`
	// Node is the step of the definition this task was created for.
	Node *Node `json:"node,omitempty"`
	// ProcessInstance is the execution this task belongs to.
	ProcessInstance *Instance `json:"process_instance,omitempty"`
	// Variables the step was reached with — the input to your work.
	Variables Variables `json:"variables,omitempty"`
}

// InstanceID returns the ID of the instance this task belongs to, or "" when
// the response did not expand it.
func (t ExternalTask) InstanceID() string {
	if t.ProcessInstance == nil {
		return ""
	}
	return t.ProcessInstance.ID
}

// NodeID returns the BPMN element ID this task was created for, or "" when the
// response carried no node.
func (t ExternalTask) NodeID() string {
	if t.Node == nil {
		return ""
	}
	return t.Node.ID
}

// AuditEntry is one line of an instance's business timeline — what happened, in
// order, in language meant for a person rather than a log parser.
type AuditEntry struct {
	Type      string    `json:"type"`
	NodeID    string    `json:"node_id,omitempty"`
	NodeName  string    `json:"node_name,omitempty"`
	Message   string    `json:"message"`
	Narrative string    `json:"narrative,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Text returns the line to show a person: the narrative when the engine wrote
// one, falling back to the message.
func (e AuditEntry) Text() string {
	if e.Narrative != "" {
		return e.Narrative
	}
	return e.Message
}

// Project is an organization's container: definitions are deployed into one,
// and most calls take its ID. [Client.ListProjects] is where an integration
// discovers it.
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ProjectRef is the former name of [Project].
//
// Deprecated: use [Project]. This alias will be removed in a future release.
type ProjectRef = Project

// PageOptions selects one window of a listing that has no filters of its own —
// everything it narrows by is already in the path. Listings that do have
// filters carry them in their own options type, such as [ListTasksOptions].
//
// The zero value asks for the first page at the server's default size.
type PageOptions struct {
	// Page is 1-based. Zero means the first page.
	Page int
	// PageSize is how many rows per page. Zero means the server's default.
	PageSize int
}

// IncidentStatus is whether an incident still needs an operator.
type IncidentStatus string

const (
	// IncidentOpen is unresolved: the step it belongs to is stuck.
	IncidentOpen IncidentStatus = "open"
	// IncidentResolved was dealt with, by an operator or by a later retry.
	IncidentResolved IncidentStatus = "resolved"
)

// Incident is a failure the engine could not handle itself — a service task
// whose retries ran out, a script that threw, an expression that would not
// evaluate. It is why an [Instance] sits at [ProcessFailed], and what an
// operator resolves to get it moving again.
//
// Read them with [Client.ListIncidents], which is the answer to "the process
// failed, but why".
type Incident struct {
	ID string `json:"id"`
	// Error is the engine's account of what went wrong, in the words of
	// whatever failed.
	Error string `json:"error"`
	// Status is open or resolved.
	Status IncidentStatus `json:"status"`
	// Node is the step that failed. Carries the BPMN element ID; see [Node] for
	// what else arrives.
	Node *Node `json:"node,omitempty"`
	// Instance is the execution this happened in.
	Instance *Instance `json:"instance,omitempty"`
	// Definition is the version that was running.
	Definition *Definition `json:"definition,omitempty"`
	CreatedAt  time.Time   `json:"created_at,omitzero"`
	// ResolvedAt is when it stopped blocking, or nil while it still does.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// IsOpen reports whether the incident still needs attention.
func (i Incident) IsOpen() bool { return i.Status == IncidentOpen }

// NodeID returns the BPMN element the incident happened at, or "" when the
// response carried no node.
func (i Incident) NodeID() string {
	if i.Node == nil {
		return ""
	}
	return i.Node.ID
}

// ExecutionPath is the route an instance actually took — the steps it reached,
// in the order it first reached them, with a count of how often each was
// entered.
//
// The counts are what make a loop visible: a node the instance passed through
// four times appears once in Nodes and four times in Frequencies.
type ExecutionPath struct {
	// Nodes are the steps reached, first-visit order, each carrying its BPMN
	// element ID. Name and Type are empty — the path is built from the audit
	// trail, which records ids. [Client.ListDefinitionNodes] has the labels.
	Nodes []Node `json:"nodes"`
	// Frequencies counts entries per node ID.
	Frequencies map[string]int `json:"frequencies,omitempty"`
}

// Visits reports how many times the instance entered nodeID — zero when it
// never reached it.
func (p ExecutionPath) Visits(nodeID string) int { return p.Frequencies[nodeID] }

// Reached reports whether the instance ever entered nodeID.
func (p ExecutionPath) Reached(nodeID string) bool { return p.Frequencies[nodeID] > 0 }

// Looped reports whether any step was entered more than once.
func (p ExecutionPath) Looped() bool {
	for _, count := range p.Frequencies {
		if count > 1 {
			return true
		}
	}
	return false
}
