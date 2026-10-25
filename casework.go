package metis

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// TaskChanges is what [Client.UpdateTask] changes on a task. A nil field, or
// a false Clear flag, leaves that part of the task as it is.
type TaskChanges struct {
	Name     *string
	Priority *int
	// DueDate sets the due date. ClearDueDate removes it, and wins over
	// DueDate when both are set.
	DueDate      *time.Time
	ClearDueDate bool
	// Reason is why the task changes. The server requires one from anybody
	// but the person holding the task, and keeps it in the task's history.
	Reason string
}

// UpdateTask renames a task, reprioritizes it, or moves or removes its due
// date — to escalate a case that cannot wait, or give one more time:
//
//	due := time.Now().Add(4 * time.Hour)
//	err := client.UpdateTask(ctx, taskID, metis.TaskChanges{
//		DueDate: &due,
//		Reason:  "customer escalated",
//	})
//
// Changes with nothing in them are refused without a request.
func (c *Client) UpdateTask(ctx context.Context, taskID string, changes TaskChanges) error {
	if taskID == "" {
		return errors.New("metis: UpdateTask needs a task ID")
	}
	body := map[string]any{}
	if changes.Name != nil {
		body["name"] = *changes.Name
	}
	if changes.Priority != nil {
		body["priority"] = *changes.Priority
	}
	switch {
	case changes.ClearDueDate:
		// Null, not absent: an absent due_date keeps the one the task has.
		body["due_date"] = nil
	case changes.DueDate != nil:
		body["due_date"] = changes.DueDate.UTC().Format(time.RFC3339)
	}
	if len(body) == 0 {
		return errors.New("metis: UpdateTask was given no changes")
	}
	if changes.Reason != "" {
		body["reason"] = changes.Reason
	}
	return c.do(ctx, http.MethodPut, "/api/v1/tasks/"+url.PathEscape(taskID), body, nil)
}

// ActivateAdHocTask starts one step of an ad-hoc sub-process — case work,
// where a person decides which optional steps a case needs ("request a second
// valuation") rather than the diagram deciding. The sub-process and the step
// are node IDs from the diagram; [Client.ListDefinitionNodes] lists them.
//
// The reason is optional and kept in the instance's history when given.
func (c *Client) ActivateAdHocTask(ctx context.Context, instanceID, subProcessNodeID, taskNodeID, reason string) error {
	if instanceID == "" || subProcessNodeID == "" || taskNodeID == "" {
		return errors.New("metis: ActivateAdHocTask needs an instance ID, the ad-hoc sub-process's node ID and the step's node ID")
	}
	body := map[string]any{
		"instance_id":         instanceID,
		"sub_process_node_id": subProcessNodeID,
		"task_node_id":        taskNodeID,
	}
	if reason != "" {
		body["reason"] = reason
	}
	return c.do(ctx, http.MethodPost, "/api/v1/processes/adhoc/activate", body, nil)
}
