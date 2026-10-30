package metis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// InstancePage is one page of a project's instances together with the
// project-wide counts the server sends with it.
type InstancePage struct {
	Instances []Instance
	// Page is nil when the server did not page.
	Page *PageInfo
	// StatusCounts counts the whole project's instances by status, not this
	// page's: a page of completed runs can still say how many failed.
	StatusCounts map[ProcessStatus]int64
	// NeedsAttention counts the project's instances holding an unresolved
	// incident. The engine leaves such an instance active, so it is not one of
	// the StatusCounts.
	NeedsAttention int64
	// NeedsAttentionOnPage are the instances on this page that hold one.
	NeedsAttentionOnPage []InstanceRef
}

// ListInstancesPage is [Client.ListInstances] with the counts a dashboard or a
// health check wants, read in the same request:
//
//	page, err := client.ListInstancesPage(ctx, metis.ListInstancesOptions{ProjectID: projectID, PageSize: 1})
//	if page.StatusCounts[metis.ProcessFailed] > 0 || page.NeedsAttention > 0 {
//		// alert
//	}
func (c *Client) ListInstancesPage(ctx context.Context, opts ListInstancesOptions) (*InstancePage, error) {
	query, err := opts.query()
	if err != nil {
		return nil, err
	}
	var out struct {
		Instances    []Instance `json:"instances"`
		Page         *PageInfo  `json:"page"`
		StatusCounts []struct {
			Status ProcessStatus `json:"status"`
			Total  int64         `json:"total"`
		} `json:"status_counts"`
		NeedsAttentionTotal int64         `json:"needs_attention_total"`
		NeedsAttentionIDs   []InstanceRef `json:"needs_attention_ids"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/instances?"+query.Encode(), nil, &out); err != nil {
		return nil, err
	}
	if err := verifyBusinessKeyFilter(opts.BusinessKey, out.Instances); err != nil {
		return nil, err
	}
	page := &InstancePage{
		Instances:            out.Instances,
		Page:                 out.Page,
		StatusCounts:         make(map[ProcessStatus]int64, len(out.StatusCounts)),
		NeedsAttention:       out.NeedsAttentionTotal,
		NeedsAttentionOnPage: out.NeedsAttentionIDs,
	}
	for _, count := range out.StatusCounts {
		page.StatusCounts[count.Status] += count.Total
	}
	return page, nil
}

// ListSubProcesses returns the instances a call activity in the given instance
// started, so a failure can be followed from the parent to the child that
// holds it. An instance that called nothing has none.
func (c *Client) ListSubProcesses(ctx context.Context, parentInstanceID string) ([]Instance, error) {
	if parentInstanceID == "" {
		return nil, errors.New("metis: ListSubProcesses needs an instance ID")
	}
	var out struct {
		Instances []Instance `json:"instances"`
	}
	path := "/api/v1/instances/" + url.PathEscape(parentInstanceID) + "/subprocesses"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Instances, nil
}

// WaitingProcess is where one process's running work sits right now.
type WaitingProcess struct {
	Process ProcessRef `json:"-"`
	Name    string     `json:"name"`
	// Instances counts the running instances sitting on at least one step. One
	// on two parallel branches is one instance, waiting in two places.
	Instances int           `json:"instances"`
	Steps     []WaitingStep `json:"steps"`
}

// UnmarshalJSON reads the process from key.
func (w *WaitingProcess) UnmarshalJSON(data []byte) error {
	type plain WaitingProcess
	var wire struct {
		plain
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*w = WaitingProcess(wire.plain)
	w.Process.Key = wire.Key
	return nil
}

// WaitingStep is one step and how much running work sits on it.
type WaitingStep struct {
	Step    StepRef `json:"-"`
	Waiting int     `json:"waiting"`
}

// UnmarshalJSON reads the step from node_id.
func (w *WaitingStep) UnmarshalJSON(data []byte) error {
	var wire struct {
		NodeID  string `json:"node_id"`
		Waiting int    `json:"waiting"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*w = WaitingStep{Step: StepRef{ID: wire.NodeID}, Waiting: wire.Waiting}
	return nil
}

// ListWaiting says, for each process in the project, on which steps its
// running instances are sitting and how many — the question behind "why has
// nothing finished today?". It is counted on the server across the whole
// project, not from a page.
func (c *Client) ListWaiting(ctx context.Context, projectID string) ([]WaitingProcess, error) {
	if projectID == "" {
		return nil, errors.New("metis: ListWaiting needs a project ID")
	}
	var out struct {
		Processes []WaitingProcess `json:"processes"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(projectID)+"/waiting", nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Processes {
		out.Processes[i].Process.Project = ProjectRef{ID: projectID}
	}
	return out.Processes, nil
}

// Deadlines is a project's open human work as far as due dates go.
type Deadlines struct {
	// Tasks are open tasks with a due date, overdue ones first and then the
	// soonest, as many as the server sends.
	Tasks []DeadlineTask `json:"tasks"`
	// WithDeadline and WithoutDeadline count every open task in the project,
	// not only those sent.
	WithDeadline    int `json:"with_deadline"`
	WithoutDeadline int `json:"without_deadline"`
}

// Overdue returns the tasks whose due date is before now.
func (d Deadlines) Overdue(now time.Time) []DeadlineTask {
	var late []DeadlineTask
	for _, task := range d.Tasks {
		if task.DueDate.Before(now) {
			late = append(late, task)
		}
	}
	return late
}

// DeadlineTask is one open task with a due date, and the process it is part of.
type DeadlineTask struct {
	Task        TaskRef    `json:"id"`
	Name        string     `json:"name"`
	Step        StepRef    `json:"-"`
	Status      TaskStatus `json:"status"`
	Priority    int        `json:"priority"`
	Assignee    string     `json:"assignee,omitempty"`
	DueDate     time.Time  `json:"due_date"`
	Process     ProcessRef `json:"-"`
	ProcessName string     `json:"process_name"`
}

// UnmarshalJSON reads the step and the process from node_id and process_key.
func (d *DeadlineTask) UnmarshalJSON(data []byte) error {
	type plain DeadlineTask
	var wire struct {
		plain
		NodeID     string `json:"node_id"`
		ProcessKey string `json:"process_key"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*d = DeadlineTask(wire.plain)
	d.Step = StepRef{ID: wire.NodeID, Name: d.Name}
	d.Process.Key = wire.ProcessKey
	return nil
}

// ListDeadlines returns the project's open tasks that have a due date, overdue
// first, with how many open tasks have one and how many do not — to remind
// people or escalate before a service level is missed.
func (c *Client) ListDeadlines(ctx context.Context, projectID string) (*Deadlines, error) {
	if projectID == "" {
		return nil, errors.New("metis: ListDeadlines needs a project ID")
	}
	var out struct {
		Deadlines Deadlines `json:"deadlines"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(projectID)+"/deadlines", nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Deadlines.Tasks {
		out.Deadlines.Tasks[i].Process.Project = ProjectRef{ID: projectID}
	}
	return &out.Deadlines, nil
}
