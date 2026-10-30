package metis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// A reference names another object the server holds without carrying it.
//
// The server answers with IDs where one object points at another — an
// incident at its instance, a deadline at its process — and the SDK keeps
// them as references rather than as half-filled objects, so a caller never
// mistakes an ID for the thing itself. Each reference loads what it names
// with Get, or acts on it directly; none goes to the server until asked. Each
// type takes only its own kind, so an instance ID cannot be passed where a
// task is meant.
//
// A reference is written to and read from JSON as the bare ID, as the server
// sends it, and is zero when the server sent none.

// InstanceRef names a process instance.
type InstanceRef struct{ ID string }

// Get loads the instance.
func (r InstanceRef) Get(ctx context.Context, c *Client) (*Instance, error) {
	if r.IsZero() {
		return nil, errEmptyRef("instance")
	}
	return c.GetInstance(ctx, r.ID)
}

// Incidents lists what failed in the instance.
func (r InstanceRef) Incidents(ctx context.Context, c *Client) ([]Incident, error) {
	if r.IsZero() {
		return nil, errEmptyRef("instance")
	}
	return c.ListIncidents(ctx, r.ID)
}

// Tasks lists the instance's human tasks.
func (r InstanceRef) Tasks(ctx context.Context, c *Client) ([]UserTask, error) {
	if r.IsZero() {
		return nil, errEmptyRef("instance")
	}
	tasks, _, err := c.ListTasks(ctx, ListTasksOptions{InstanceID: r.ID})
	return tasks, err
}

func (r InstanceRef) IsZero() bool                 { return r.ID == "" }
func (r InstanceRef) String() string               { return r.ID }
func (r InstanceRef) MarshalJSON() ([]byte, error) { return json.Marshal(r.ID) }
func (r *InstanceRef) UnmarshalJSON(data []byte) error {
	return unmarshalRefID(data, &r.ID)
}

// IncidentRef names an incident.
type IncidentRef struct{ ID string }

// Resolve marks the incident dealt with. See [Client.ResolveIncident].
func (r IncidentRef) Resolve(ctx context.Context, c *Client) error {
	if r.IsZero() {
		return errEmptyRef("incident")
	}
	return c.ResolveIncident(ctx, r.ID)
}

func (r IncidentRef) IsZero() bool                 { return r.ID == "" }
func (r IncidentRef) String() string               { return r.ID }
func (r IncidentRef) MarshalJSON() ([]byte, error) { return json.Marshal(r.ID) }
func (r *IncidentRef) UnmarshalJSON(data []byte) error {
	return unmarshalRefID(data, &r.ID)
}

// TaskRef names a human task.
type TaskRef struct{ ID string }

// Get loads the task.
func (r TaskRef) Get(ctx context.Context, c *Client) (*UserTask, error) {
	if r.IsZero() {
		return nil, errEmptyRef("task")
	}
	return c.GetTask(ctx, r.ID)
}

func (r TaskRef) IsZero() bool                 { return r.ID == "" }
func (r TaskRef) String() string               { return r.ID }
func (r TaskRef) MarshalJSON() ([]byte, error) { return json.Marshal(r.ID) }
func (r *TaskRef) UnmarshalJSON(data []byte) error {
	return unmarshalRefID(data, &r.ID)
}

// ProjectRef names a project.
//
// It replaces the deprecated alias of the same name for [Project]: a
// reference is not the project, and a project is now [Project] only.
type ProjectRef struct{ ID string }

// Get loads the project.
func (r ProjectRef) Get(ctx context.Context, c *Client) (*Project, error) {
	if r.IsZero() {
		return nil, errEmptyRef("project")
	}
	return c.GetProject(ctx, r.ID)
}

// Process names one of the project's processes by its key.
func (r ProjectRef) Process(key string) ProcessRef { return ProcessRef{Project: r, Key: key} }

func (r ProjectRef) IsZero() bool                 { return r.ID == "" }
func (r ProjectRef) String() string               { return r.ID }
func (r ProjectRef) MarshalJSON() ([]byte, error) { return json.Marshal(r.ID) }
func (r *ProjectRef) UnmarshalJSON(data []byte) error {
	return unmarshalRefID(data, &r.ID)
}

// ProcessRef names a process — every version deployed under one key in one
// project. The server sends a process as its key, and the project beside it
// or implied by the call, so a ProcessRef is filled in by the SDK rather than
// read from one JSON field. Its Project is zero where the server's answer does
// not say which project.
type ProcessRef struct {
	Project ProjectRef
	Key     string
}

// Versions lists the process's deployed versions, which is live, and what
// each still runs.
func (r ProcessRef) Versions(ctx context.Context, c *Client) ([]VersionStatus, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	return c.ListVersions(ctx, r.Project.ID, r.Key)
}

// Start starts an instance of the live version. See [Client.StartProcess].
func (r ProcessRef) Start(ctx context.Context, c *Client, variables Variables, opts ...StartOption) (InstanceRef, error) {
	if err := r.check(); err != nil {
		return InstanceRef{}, err
	}
	id, err := c.StartProcess(ctx, r.Project.ID, r.Key, variables, opts...)
	return InstanceRef{ID: id}, err
}

func (r ProcessRef) check() error {
	switch {
	case r.Key == "":
		return errEmptyRef("process")
	case r.Project.IsZero():
		return errors.New("metis: this process reference does not say which project it is in; build one with ProjectRef.Process")
	}
	return nil
}

func (r ProcessRef) IsZero() bool { return r.Key == "" }

// processWire is a process as the server writes it beside other fields.
type processWire struct {
	ProjectID  string `json:"project_id"`
	ProcessKey string `json:"process_key"`
}

func (w processWire) process() ProcessRef {
	return ProcessRef{Project: ProjectRef{ID: w.ProjectID}, Key: w.ProcessKey}
}

func (r ProcessRef) String() string { return r.Key }

// StepRef names a step of a diagram — a BPMN element — and carries the label
// the server sent with it, which may be empty. [StepRef.In] finds the step in
// a [Flow] read from the same diagram.
type StepRef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// In returns the step in flow, or nil when flow holds no step of that ID among
// the kinds it was read for.
func (r StepRef) In(flow *Flow) *FlowStep {
	if flow == nil {
		return nil
	}
	return flow.Step(r.ID)
}

func (r StepRef) IsZero() bool   { return r.ID == "" }
func (r StepRef) String() string { return r.ID }

// WebhookRef names an outgoing webhook.
type WebhookRef struct{ ID string }

// Deliveries reads the webhook's delivery log.
func (r WebhookRef) Deliveries(ctx context.Context, c *Client, opts ListDeliveriesOptions) (*DeliveryPage, error) {
	if r.IsZero() {
		return nil, errEmptyRef("webhook")
	}
	return c.ListOutgoingWebhookDeliveries(ctx, r.ID, opts)
}

func (r WebhookRef) IsZero() bool                 { return r.ID == "" }
func (r WebhookRef) String() string               { return r.ID }
func (r WebhookRef) MarshalJSON() ([]byte, error) { return json.Marshal(r.ID) }
func (r *WebhookRef) UnmarshalJSON(data []byte) error {
	return unmarshalRefID(data, &r.ID)
}

// DeliveryRef names one delivery of an outgoing webhook.
type DeliveryRef struct{ ID string }

// Retry sends the delivery again. See [Client.RetryOutgoingWebhookDelivery].
func (r DeliveryRef) Retry(ctx context.Context, c *Client) error {
	if r.IsZero() {
		return errEmptyRef("delivery")
	}
	return c.RetryOutgoingWebhookDelivery(ctx, r.ID)
}

// Stop gives up on the delivery. See [Client.StopOutgoingWebhookDelivery].
func (r DeliveryRef) Stop(ctx context.Context, c *Client) error {
	if r.IsZero() {
		return errEmptyRef("delivery")
	}
	return c.StopOutgoingWebhookDelivery(ctx, r.ID)
}

func (r DeliveryRef) IsZero() bool                 { return r.ID == "" }
func (r DeliveryRef) String() string               { return r.ID }
func (r DeliveryRef) MarshalJSON() ([]byte, error) { return json.Marshal(r.ID) }
func (r *DeliveryRef) UnmarshalJSON(data []byte) error {
	return unmarshalRefID(data, &r.ID)
}

// ReleaseRef names a cutover arranged with [Client.ScheduleVersion].
type ReleaseRef struct {
	Project ProjectRef
	ID      string
}

// Cancel drops the cutover. See [Client.CancelScheduledVersion].
func (r ReleaseRef) Cancel(ctx context.Context, c *Client) error {
	if r.IsZero() {
		return errEmptyRef("release")
	}
	return c.CancelScheduledVersion(ctx, r.Project.ID, r.ID)
}

func (r ReleaseRef) IsZero() bool   { return r.ID == "" }
func (r ReleaseRef) String() string { return r.ID }

// DecisionRef names one version of a decision table.
type DecisionRef struct {
	Project ProjectRef
	Key     string
	Name    string
	// Version is zero for the live version.
	Version int
}

// Evaluate runs this version of the table on other inputs.
func (r DecisionRef) Evaluate(ctx context.Context, c *Client, variables Variables) (*DecisionResult, error) {
	if r.Key == "" {
		return nil, errEmptyRef("decision")
	}
	var opts []EvaluateOption
	if r.Version > 0 {
		opts = append(opts, WithDecisionVersion(r.Version))
	}
	return c.EvaluateDecision(ctx, r.Project.ID, r.Key, variables, opts...)
}

func (r DecisionRef) IsZero() bool   { return r.Key == "" }
func (r DecisionRef) String() string { return r.Key }

// GetProject loads one project.
func (c *Client) GetProject(ctx context.Context, projectID string) (*Project, error) {
	if projectID == "" {
		return nil, errors.New("metis: GetProject needs a project ID")
	}
	var out struct {
		Project Project `json:"project"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(projectID), nil, &out); err != nil {
		return nil, err
	}
	return &out.Project, nil
}

func errEmptyRef(kind string) error {
	return errors.New("metis: the " + kind + " reference is empty; the server named no " + kind + " here")
}

// unmarshalRefID reads an ID sent as a string, with null as no ID.
func unmarshalRefID(data []byte, id *string) error {
	if string(data) == "null" {
		*id = ""
		return nil
	}
	return json.Unmarshal(data, id)
}
