package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// CurrentUser says who the client's token signs in.
//
// After [Client.Login] it is known without a request. With a token given by
// [WithToken] it is read once, from GET /api/v1/users/me, which needs **Metis
// v0.5.0** or later; an older server answers [IsNotFound].
func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	c.mu.RLock()
	me := c.me
	c.mu.RUnlock()
	if me != nil {
		copied := *me
		return &copied, nil
	}

	var out struct {
		User User `json:"user"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/users/me", nil, &out); err != nil {
		return nil, err
	}
	if out.User.Username == "" {
		return nil, errors.New("metis: the server did not say who the token signs in")
	}
	c.mu.Lock()
	c.me = &out.User
	c.mu.Unlock()
	copied := out.User
	return &copied, nil
}

// ErrTaskTakenByOther means somebody else holds the task, or completed it.
var ErrTaskTakenByOther = errors.New("metis: the task is somebody else's")

// ErrTaskClosed means the task is no longer open: it was completed, or
// withdrawn because its step no longer needs it.
var ErrTaskClosed = errors.New("metis: the task is no longer open")

// TaskConflictError is a task that could not be worked as asked, with the
// task as it was read afterwards, so the caller can say who has it. It
// matches [ErrTaskTakenByOther] or [ErrTaskClosed] with errors.Is.
type TaskConflictError struct {
	Task UserTask
	err  error
}

func (e *TaskConflictError) Error() string {
	if holder := e.Task.AssigneeUsername(); holder != "" {
		return fmt.Sprintf("%v: task %s is %s, by %s", e.err, e.Task.ID, e.Task.Status, holder)
	}
	return fmt.Sprintf("%v: task %s is %s", e.err, e.Task.ID, e.Task.Status)
}

func (e *TaskConflictError) Unwrap() error { return e.err }

// conflict says why me cannot work task, or nil when it can.
func conflict(task *UserTask, me string) error {
	holder := task.AssigneeUsername()
	switch {
	case task.Status == TaskCompleted && holder == me:
		return nil // done, by me
	case task.Status == TaskCompleted && holder != "":
		return &TaskConflictError{Task: *task, err: ErrTaskTakenByOther}
	case !task.IsOpen():
		return &TaskConflictError{Task: *task, err: ErrTaskClosed}
	case holder != "" && holder != me:
		return &TaskConflictError{Task: *task, err: ErrTaskTakenByOther}
	}
	return nil
}

// ambiguous reports whether a write may have happened although it failed: no
// answer came, or the server faltered partway.
func ambiguous(err error) bool {
	if _, ok := errors.AsType[*transportError](err); ok {
		return true
	}
	return IsServerError(err)
}

// CompleteTaskSafely completes a task the signed-in user may finish, and can
// be called again after a failure without completing it twice.
//
// [Client.CompleteTask] is one write, sent once. When its answer is lost the
// caller cannot tell a completion that happened from one that did not, and
// the server answers a second attempt at a completed task with a 400. This
// reads the task first and again after any failure:
//
//   - completed by the signed-in user already: nil, and nothing is sent;
//   - held or completed by somebody else: a [*TaskConflictError] matching
//     [ErrTaskTakenByOther];
//   - withdrawn: a [*TaskConflictError] matching [ErrTaskClosed];
//   - still open after a failure: the failure, and the call is safe to make
//     again.
//
// The variables of a completion that already happened are not compared with
// these: the first one to land is what the instance has. It needs to know who
// is signed in; see [Client.CurrentUser].
func (c *Client) CompleteTaskSafely(ctx context.Context, taskID string, variables Variables) error {
	if taskID == "" {
		return errors.New("metis: complete task: no task ID")
	}
	me, err := c.CurrentUser(ctx)
	if err != nil {
		return err
	}
	task, err := c.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if err := conflict(task, me.Username); err != nil || task.Status == TaskCompleted {
		return err
	}

	sent := c.CompleteTask(ctx, taskID, variables)
	if sent == nil {
		return nil
	}
	if !ambiguous(sent) && !IsInvalid(sent) && !IsUnauthorized(sent) {
		return sent
	}
	// Whether it went through, or what got there first, is in the task now.
	after, err := c.GetTask(ctx, taskID)
	if err != nil {
		return sent
	}
	if err := conflict(after, me.Username); err != nil {
		return err
	}
	if after.Status == TaskCompleted {
		return nil
	}
	return sent
}

// ClaimAndComplete claims a task for the signed-in user if it is not theirs
// already, then completes it as [Client.CompleteTaskSafely] does — the whole
// of working one item off a shared queue.
//
//	err := client.ClaimAndComplete(ctx, task.ID, metis.Variables{"approved": true})
//	if errors.Is(err, metis.ErrTaskTakenByOther) {
//		// somebody was quicker: pick the next one
//	}
//
// When two people claim at once the server lets one through and refuses the
// other with a 400; that refusal comes back as a [*TaskConflictError] naming
// who has it.
func (c *Client) ClaimAndComplete(ctx context.Context, taskID string, variables Variables) error {
	if taskID == "" {
		return errors.New("metis: claim and complete: no task ID")
	}
	me, err := c.CurrentUser(ctx)
	if err != nil {
		return err
	}
	task, err := c.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if err := conflict(task, me.Username); err != nil || task.Status == TaskCompleted {
		return err
	}
	if task.AssigneeUsername() != me.Username {
		if claimErr := c.ClaimTask(ctx, taskID); claimErr != nil {
			after, err := c.GetTask(ctx, taskID)
			if err != nil {
				return claimErr
			}
			if err := conflict(after, me.Username); err != nil {
				return err
			}
			if after.AssigneeUsername() != me.Username {
				return claimErr
			}
			// The claim landed although its answer did not.
		}
	}
	return c.CompleteTaskSafely(ctx, taskID, variables)
}

// StartProcessOnce starts a process for a case unless an instance of that
// process was already started for it, and returns the instance either way.
// started says whether this call started it.
//
// The case is named by its business key, as [WithBusinessKey] gives it, so
// the call can be made again after a start whose answer was lost — or by a
// handler that receives the same order twice — without a second instance:
//
//	inst, started, err := client.StartProcessOnce(ctx, project, "order", "ORDER-4411", vars)
//
// An instance of another process with the same key does not count, and a
// finished one does: a case that ran is not started again. Several instances
// already found give the newest.
//
// It looks, then starts; the server keeps no lock between the two and does not
// make business keys unique. Two callers that start the same case at the same
// moment can both start it, so give one case to one caller at a time — a queue
// partitioned by the key, say. Needs a server newer than **Metis v0.5.0**, for
// the business key; an older one answers [ErrFilterUnsupported].
func (c *Client) StartProcessOnce(ctx context.Context, projectID, definitionKey, businessKey string, variables Variables, opts ...StartOption) (inst *Instance, started bool, err error) {
	if projectID == "" || definitionKey == "" || businessKey == "" {
		return nil, false, errors.New("metis: start process once: a project, a process key and a business key are all needed")
	}
	found, err := c.instanceForCase(ctx, projectID, definitionKey, businessKey)
	if err != nil || found != nil {
		return found, false, err
	}

	opts = append(opts[:len(opts):len(opts)], WithBusinessKey(businessKey))
	id, startErr := c.StartProcess(ctx, projectID, definitionKey, variables, opts...)
	if startErr == nil {
		return &Instance{ID: id, BusinessKey: businessKey}, true, nil
	}
	if !ambiguous(startErr) {
		return nil, false, startErr
	}
	// The start may have happened. If it did, the instance is there now.
	found, err = c.instanceForCase(ctx, projectID, definitionKey, businessKey)
	if err != nil || found == nil {
		return nil, false, startErr
	}
	return found, true, nil
}

// instanceForCase finds the newest instance of process key started for
// businessKey, or nil.
func (c *Client) instanceForCase(ctx context.Context, projectID, key, businessKey string) (*Instance, error) {
	var candidates []Instance
	for inst, err := range c.AllInstances(ctx, ListInstancesOptions{ProjectID: projectID, BusinessKey: businessKey}) {
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, inst)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	versions, err := c.ListVersions(ctx, projectID, key)
	if err != nil {
		return nil, err
	}
	ofKey := make(map[string]bool, len(versions))
	for _, v := range versions {
		ofKey[v.ID] = true
	}
	// Listings are newest first.
	for _, inst := range candidates {
		if inst.Definition != nil && ofKey[inst.Definition.ID] {
			return &inst, nil
		}
	}
	return nil, nil
}
