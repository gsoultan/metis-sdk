package metis

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// Why nobody can act on a [StrandedTask].
const (
	// StrandedAssigneeGone: the task is held by a name that is neither an
	// account in the organization nor a person of the project.
	StrandedAssigneeGone = "assignee_gone"
	// StrandedAssigneeInactive: the task is held by a person who was
	// deactivated.
	StrandedAssigneeInactive = "assignee_inactive"
	// StrandedNoCandidate: the task is offered to people and groups and none
	// of them has anybody left who can take it.
	StrandedNoCandidate = "no_candidate"
)

// StrandedTask is an open task nobody can act on. A task names who may do it
// by username and group name, so when that person leaves or is deactivated,
// or the group is deleted or left with nobody active, the task waits for
// ever. Hand it to somebody who can with [Client.AssignTask].
type StrandedTask struct {
	TaskID     string    `json:"task_id"`
	Name       string    `json:"name"`
	NodeID     string    `json:"node_id,omitempty"`
	InstanceID string    `json:"instance_id,omitempty"`
	Since      time.Time `json:"since,omitzero"`
	// Kind is one of the Stranded* constants.
	Kind string `json:"kind"`
	// Assignee is who holds it, when somebody does.
	Assignee string `json:"assignee,omitempty"`
	// Missing names each person and group the task names, and why it cannot
	// act.
	Missing []StrandedName `json:"missing"`
}

// StrandedName is one name a [StrandedTask] uses.
type StrandedName struct {
	Name string `json:"name"`
	// Kind is "person" or "group".
	Kind string `json:"kind"`
	// Why is "gone" (nobody or no group of that name), "inactive" (a
	// deactivated person) or "empty" (a group with nobody in it who can act).
	Why string `json:"why"`
}

// ListStrandedTasks lists a project's open tasks that nobody can act on,
// longest waiting first, with who each waits for and why they cannot act.
//
// It needs the operator role or above, and a Metis server that has
// GET /api/v1/participants/stranded-tasks: an older one answers 404
// ([IsNotFound]).
//
//	stuck, err := client.ListStrandedTasks(ctx, projectID)
//	for _, t := range stuck {
//		err = client.AssignTask(ctx, t.TaskID, "sari", metis.WithReason("Budi has left"))
//	}
func (c *Client) ListStrandedTasks(ctx context.Context, projectID string) ([]StrandedTask, error) {
	if projectID == "" {
		return nil, errors.New("metis: ListStrandedTasks needs a project ID")
	}
	var out struct {
		Tasks []StrandedTask `json:"tasks"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/participants/stranded-tasks?"+url.Values{"project_id": {projectID}}.Encode(), nil, &out)
	return out.Tasks, err
}
