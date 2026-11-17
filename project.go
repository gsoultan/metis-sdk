package metis

import (
	"context"
	"time"
)

// ProjectCalls is one project's calls, with its ID given once. Everything a
// project holds, processes, decisions, approval lines and People, is reached
// from here:
//
//	p := client.Project(projectID)
//	id, err := p.Import(ctx, bpmn)
//	instanceID, err := p.Start(ctx, "expense", metis.Variables{"amount": 120})
//	res, err := p.People().Deactivate(ctx, budiID, &metis.HeldWork{...})
//
// The Client methods it calls stay, for code that already uses them. A
// task is not a project's, so its calls stay on the Client: [Client.Inbox],
// [Client.GetTask], [Client.ClaimTask], [Client.CompleteTask].
type ProjectCalls struct {
	c  *Client
	id string
}

// Project is the calls on one project, with its ID given once.
func (c *Client) Project(projectID string) ProjectCalls { return ProjectCalls{c: c, id: projectID} }

// ID is the project's ID.
func (p ProjectCalls) ID() string { return p.id }

// Approvals is the project's approval lines.
func (p ProjectCalls) Approvals() Approvals { return p.c.Approvals(p.id) }

// People is the project's directory of people: who they are, and making them
// active or not.
func (p ProjectCalls) People() ProjectPeople { return ProjectPeople{c: p.c, project: p.id} }

// Import deploys a BPMN file as a new version and returns its definition ID.
// See [Client.ImportDefinition].
func (p ProjectCalls) Import(ctx context.Context, bpmnXML []byte) (string, error) {
	return p.c.ImportDefinition(ctx, p.id, bpmnXML)
}

// Release deploys a version and tries it before it goes live. See
// [Client.ReleaseVersion].
func (p ProjectCalls) Release(ctx context.Context, processKey string, bpmnXML []byte, opts ReleaseOptions) (*ReleaseReport, error) {
	return p.c.ReleaseVersion(ctx, p.id, processKey, bpmnXML, opts)
}

// Start starts the live version of a process and returns the instance ID.
// See [Client.StartProcess].
func (p ProjectCalls) Start(ctx context.Context, processKey string, variables Variables, opts ...StartOption) (string, error) {
	return p.c.StartProcess(ctx, p.id, processKey, variables, opts...)
}

// StartOnce starts a process once per business key. See
// [Client.StartProcessOnce].
func (p ProjectCalls) StartOnce(ctx context.Context, processKey, businessKey string, variables Variables, opts ...StartOption) (*Instance, bool, error) {
	return p.c.StartProcessOnce(ctx, p.id, processKey, businessKey, variables, opts...)
}

// StartAndWait starts a process and waits for it to end. See
// [Client.StartAndWait].
func (p ProjectCalls) StartAndWait(ctx context.Context, processKey string, variables Variables, wait WaitOptions, opts ...StartOption) (*Instance, error) {
	return p.c.StartAndWait(ctx, p.id, processKey, variables, wait, opts...)
}

// SendMessage delivers a message to the instance waiting for it. See
// [Client.SendMessage].
func (p ProjectCalls) SendMessage(ctx context.Context, messageName, correlationKey string, variables Variables) error {
	return p.c.SendMessage(ctx, p.id, messageName, correlationKey, variables)
}

// Broadcast sends a signal to every instance waiting for it. See
// [Client.BroadcastSignal].
func (p ProjectCalls) Broadcast(ctx context.Context, signalName string, variables Variables) error {
	return p.c.BroadcastSignal(ctx, p.id, signalName, variables)
}

// Decide evaluates a decision table. See [Client.EvaluateDecision].
func (p ProjectCalls) Decide(ctx context.Context, decisionKey string, variables Variables, opts ...EvaluateOption) (*DecisionResult, error) {
	return p.c.EvaluateDecision(ctx, p.id, decisionKey, variables, opts...)
}

// Simulate runs a process without starting it. See [Client.Simulate].
func (p ProjectCalls) Simulate(ctx context.Context, req SimulationRequest) (*SimulationRun, error) {
	return p.c.Simulate(ctx, p.id, req)
}

// Versions lists a process's versions. See [Client.ListVersions].
func (p ProjectCalls) Versions(ctx context.Context, processKey string) ([]VersionStatus, error) {
	return p.c.ListVersions(ctx, p.id, processKey)
}

// LiveVersions is the version each process starts on now. See
// [Client.ListLiveVersions].
func (p ProjectCalls) LiveVersions(ctx context.Context) (map[string]int, error) {
	return p.c.ListLiveVersions(ctx, p.id)
}

// Promote makes a version live now. See [Client.PromoteVersion].
func (p ProjectCalls) Promote(ctx context.Context, processKey string, version int) error {
	return p.c.PromoteVersion(ctx, p.id, processKey, version)
}

// Schedule makes a version live at a time. See [Client.ScheduleVersion].
func (p ProjectCalls) Schedule(ctx context.Context, processKey string, version int, at time.Time) error {
	return p.c.ScheduleVersion(ctx, p.id, processKey, version, at)
}

// CancelSchedule calls off a scheduled version. See
// [Client.CancelScheduledVersion].
func (p ProjectCalls) CancelSchedule(ctx context.Context, releaseID string) error {
	return p.c.CancelScheduledVersion(ctx, p.id, releaseID)
}

// RetryFailed retries the project's failed work. See [Client.RetryFailed].
func (p ProjectCalls) RetryFailed(ctx context.Context, opts RetryFailedOptions) (*BatchPreview, error) {
	return p.c.RetryFailed(ctx, p.id, opts)
}

// ProjectPeople is a project's directory of people.
type ProjectPeople struct {
	c       *Client
	project string
}

// List is everybody in the directory, active or not.
func (pp ProjectPeople) List(ctx context.Context) ([]Participant, error) {
	return pp.c.ListParticipants(ctx, pp.project)
}

// HeldTasks is the open tasks a person holds.
func (pp ProjectPeople) HeldTasks(ctx context.Context, participantID string) ([]HeldTask, error) {
	return pp.c.ListHeldTasks(ctx, pp.project, participantID)
}

// Deactivate stops a person signing in and getting work, and settles the
// tasks they hold as work says; nil keeps them with the person. See
// [Client.SetParticipantActive].
func (pp ProjectPeople) Deactivate(ctx context.Context, participantID string, work *HeldWork) (ActivationResult, error) {
	return pp.c.SetParticipantActive(ctx, pp.project, participantID, false, work)
}

// Activate lets a person sign in and get work again. Nothing they handed on
// comes back.
func (pp ProjectPeople) Activate(ctx context.Context, participantID string) error {
	_, err := pp.c.SetParticipantActive(ctx, pp.project, participantID, true, nil)
	return err
}

// Stranded is the open tasks nobody can act on. See [Client.ListStrandedTasks].
func (pp ProjectPeople) Stranded(ctx context.Context) ([]StrandedTask, error) {
	return pp.c.ListStrandedTasks(ctx, pp.project)
}
