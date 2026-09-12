package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// --- Projects ---------------------------------------------------------------

// ListProjects lists the projects the caller's organization owns. Most other
// calls take a project ID; this is where an integration discovers it.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var out struct {
		Projects []Project `json:"projects"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/projects", nil, &out)
	return out.Projects, err
}

// --- Definitions ------------------------------------------------------------

// ImportDefinition deploys BPMN 2.0 XML into a project and returns the new
// definition's ID. Redeploying the same process key creates the next version;
// running instances keep the version they started with.
func (c *Client) ImportDefinition(ctx context.Context, projectID string, xml []byte) (string, error) {
	if projectID == "" {
		return "", errors.New("metis: ImportDefinition needs a project ID — a definition without a project is invisible to its own organization")
	}
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/definitions/import", map[string]any{
		"project_id": projectID,
		"xml":        xml, // marshals to base64, which is what the server decodes
	}, &out)
	if err != nil {
		return "", err
	}
	return out.ID, nil
}

// ExportDefinition returns a deployed definition as BPMN 2.0 XML, by the
// definition's version ID — [Definition.ID], not its Key.
func (c *Client) ExportDefinition(ctx context.Context, definitionID string) ([]byte, error) {
	var out struct {
		XML []byte `json:"xml"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/definitions/"+url.PathEscape(definitionID)+"/export", nil, &out)
	return out.XML, err
}

// ListDefinitionsOptions filters and pages a definition listing. The zero value
// lists the first page at the server's default size.
type ListDefinitionsOptions struct {
	// ProjectID limits the listing to one project. Empty lists across the
	// caller's whole organization — unlike the instance listing, which requires
	// one.
	ProjectID string
	// Page is 1-based. Zero means the first page.
	Page int
	// PageSize is how many definitions per page. Zero means the server's
	// default.
	PageSize int
}

// ListDefinitions returns deployed process definitions, newest first.
//
// This is where an integration discovers a definition ID it did not deploy
// itself — the ID [Client.ExportDefinition] and [Client.ListUserTaskNodes]
// take. [Client.ImportDefinition] returns one too, but only for a process you
// just deployed in the same program.
//
// A project keeps *every version of every process it has ever had*, so this is
// paged and the list is longer than it looks: one process redeployed twenty
// times is twenty entries, distinguished by [Definition.Version] and sharing a
// [Definition.Key]. To start a process you want the Key, and the engine picks
// the latest version itself — see [Client.StartProcess].
//
// [Definition.Nodes] is empty here whatever the diagram holds: the server
// selects scalar columns for a listing and never loads the graph. Use
// [Client.ListDefinitionNodes] for one definition's steps.
func (c *Client) ListDefinitions(ctx context.Context, opts ListDefinitionsOptions) ([]Definition, *PageInfo, error) {
	query := url.Values{}
	if opts.ProjectID != "" {
		query.Set("project_id", opts.ProjectID)
	}
	if opts.Page > 0 {
		query.Set("page", fmt.Sprint(opts.Page))
	}
	if opts.PageSize > 0 {
		query.Set("page_size", fmt.Sprint(opts.PageSize))
	}
	path := "/api/v1/definitions"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}

	var out struct {
		Page        *PageInfo    `json:"page"`
		Definitions []Definition `json:"definitions"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Definitions, out.Page, err
}

// ListDefinitionNodes returns every node of a deployed definition — the steps
// of the diagram, each with its BPMN element ID, its label, and its
// [NodeType]. Nodes inside sub-processes are included.
//
// definitionID is the version ID, [Definition.ID], as returned by
// [Client.ImportDefinition].
//
// This exports the definition and parses the BPMN client-side, because the
// engine has no endpoint that returns a definition's nodes as data: the
// definition listing selects scalar columns only and never populates them.
// So it costs one round trip and the size of the diagram — worth caching if
// you are asking per request rather than per deployment.
func (c *Client) ListDefinitionNodes(ctx context.Context, definitionID string) ([]Node, error) {
	bpmnXML, err := c.ExportDefinition(ctx, definitionID)
	if err != nil {
		return nil, err
	}
	return ParseNodes(bpmnXML)
}

// ListUserTaskNodes returns the human steps of a deployed definition — its
// <userTask> elements, in the order the diagram declares them.
//
//	nodes, err := client.ListUserTaskNodes(ctx, definitionID)
//	for _, node := range nodes {
//		fmt.Println(node.ID, "—", node.Name)
//	}
//
// This is the *design-time* question: which human steps this process has,
// whether or not anything has reached them. For the steps a running instance
// has actually arrived at, list its tasks — see [Client.ListTasks].
//
// Only [NodeUserTask] is returned. A <manualTask> is also work for a person but
// a different element, and the engine types it separately; to treat them alike,
// use [Client.ListDefinitionNodes] with [NodesOfType] or [NodeType.IsHumanStep]:
//
//	nodes, err := client.ListDefinitionNodes(ctx, definitionID)
//	human := metis.NodesOfType(nodes, metis.NodeUserTask, metis.NodeManualTask)
func (c *Client) ListUserTaskNodes(ctx context.Context, definitionID string) ([]Node, error) {
	nodes, err := c.ListDefinitionNodes(ctx, definitionID)
	if err != nil {
		return nil, err
	}
	return NodesOfType(nodes, NodeUserTask), nil
}

// --- Instances --------------------------------------------------------------

// StartProcess starts an [Instance] of the definition deployed under
// definitionKey in the given project, seeded with variables, and returns the
// new instance's ID.
//
// definitionKey is the process ID from the diagram — [Definition.Key], not
// [Definition.ID]. The latest deployed version of that key is used, so a
// redeploy takes effect on the next start without changing this call.
func (c *Client) StartProcess(ctx context.Context, projectID, definitionKey string, variables Variables) (string, error) {
	var out struct {
		InstanceID string `json:"instance_id"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/process/start", map[string]any{
		"project_id":     projectID,
		"definition_key": definitionKey,
		"variables":      variables,
	}, &out)
	if err != nil {
		return "", err
	}
	return out.InstanceID, nil
}

// GetInstance returns one process instance, including its current variables
// and status.
func (c *Client) GetInstance(ctx context.Context, instanceID string) (*Instance, error) {
	var out struct {
		Instance Instance `json:"instance"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/instances/"+url.PathEscape(instanceID), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out.Instance, nil
}

// ErrNoInstances is what [Client.LatestInstance] returns when the project has
// never run the process at all. It is not a server error — the request
// succeeded and the answer was empty — so [IsNotFound] is false for it.
var ErrNoInstances = errors.New("metis: the project has no process instances")

// ListInstancesOptions filters and pages an instance listing.
type ListInstancesOptions struct {
	// ProjectID is required: the server refuses an empty or malformed one with
	// a 400 rather than widening the query to every instance in the
	// organization.
	ProjectID string
	// Page is 1-based. Zero means the first page.
	Page int
	// PageSize is how many instances per page. Zero means the server's default.
	PageSize int
}

// ListInstances returns a project's process instances, **most recent first**.
// The [*PageInfo] reports how many there are in total, and whether more pages
// follow.
//
//	instances, page, err := client.ListInstances(ctx, metis.ListInstancesOptions{
//		ProjectID: projectID,
//		PageSize:  50,
//	})
func (c *Client) ListInstances(ctx context.Context, opts ListInstancesOptions) ([]Instance, *PageInfo, error) {
	if opts.ProjectID == "" {
		return nil, nil, errors.New("metis: ListInstances needs a project ID — the server refuses an empty one rather than listing the whole organization")
	}
	query := url.Values{"project_id": {opts.ProjectID}}
	if opts.Page > 0 {
		query.Set("page", fmt.Sprint(opts.Page))
	}
	if opts.PageSize > 0 {
		query.Set("page_size", fmt.Sprint(opts.PageSize))
	}

	var out struct {
		Instances []Instance `json:"instances"`
		Page      *PageInfo  `json:"page"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/instances?"+query.Encode(), nil, &out)
	return out.Instances, out.Page, err
}

// LatestInstance returns the project's most recently created process instance —
// the last run — or [ErrNoInstances] when there have been none.
//
//	instance, err := client.LatestInstance(ctx, projectID)
//	switch {
//	case errors.Is(err, metis.ErrNoInstances):
//		// nothing has run yet
//	case err != nil:
//		return err
//	}
//
// It asks for a single row rather than a page it would throw away.
//
// "Latest" is by creation time across the whole project, so it is the last
// process *started*, which is not necessarily the last one to finish or the one
// you started yourself. The server offers no filter by definition, so this
// cannot be narrowed to one process type; when that matters, keep the ID that
// [Client.StartProcess] returned instead of looking it up afterwards.
func (c *Client) LatestInstance(ctx context.Context, projectID string) (*Instance, error) {
	instances, _, err := c.ListInstances(ctx, ListInstancesOptions{
		ProjectID: projectID,
		PageSize:  1,
	})
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, ErrNoInstances
	}
	// The server orders by created_at descending, so the newest is first.
	return &instances[0], nil
}

// GetTimeline returns an instance's audit trail — the plain-language record of
// what happened, in order. Use [AuditEntry.Text] for the line to show a person.
func (c *Client) GetTimeline(ctx context.Context, instanceID string) ([]AuditEntry, error) {
	var out struct {
		Entries []AuditEntry `json:"entries"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/instances/"+url.PathEscape(instanceID)+"/audit", nil, &out)
	return out.Entries, err
}

// GetExecutionPath returns the route an instance took — the steps it reached,
// in the order it first reached them, with a count of how often each was
// entered.
//
//	path, err := client.GetExecutionPath(ctx, instanceID)
//	if path.Reached("escalate") { ... }
//	if path.Looped() { ... }
//
// It is built from the audit trail, so the nodes carry BPMN element IDs and no
// labels; [Client.ListDefinitionNodes] has those. A step the instance is
// *currently* sitting at has been reached and so appears here — the path is
// where it has been, not only where it finished.
func (c *Client) GetExecutionPath(ctx context.Context, instanceID string) (*ExecutionPath, error) {
	var out struct {
		Nodes       []Node         `json:"nodes"`
		Frequencies map[string]int `json:"frequencies"`
		// This endpoint reports failures inline as well as by status code.
		Error string `json:"error"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/instances/"+url.PathEscape(instanceID)+"/path", nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("metis: execution path: %s", out.Error)
	}
	return &ExecutionPath{Nodes: out.Nodes, Frequencies: out.Frequencies}, nil
}

// --- Incidents --------------------------------------------------------------

// ListIncidents returns the failures the engine could not handle itself for one
// instance — why it is at [ProcessFailed], or why a step of it is stuck.
//
//	for _, incident := range incidents {
//		if incident.IsOpen() {
//			log.Printf("%s failed at %s: %s", instanceID, incident.NodeID(), incident.Error)
//		}
//	}
//
// Resolved incidents are included, so an instance that recovered still has its
// history here. Filter with [Incident.IsOpen] for what is still blocking.
func (c *Client) ListIncidents(ctx context.Context, instanceID string) ([]Incident, error) {
	var out struct {
		Incidents []Incident `json:"incidents"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/incidents/"+url.PathEscape(instanceID), nil, &out)
	return out.Incidents, err
}

// ResolveIncident marks an incident dealt with, so the engine stops treating it
// as blocking.
//
// This is an operator action, not a retry: it says the underlying problem is
// handled, and it does not by itself re-run the step that failed. Resolving one
// while the cause remains is how the same incident comes back.
func (c *Client) ResolveIncident(ctx context.Context, incidentID string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/incidents/"+url.PathEscape(incidentID)+"/resolve", nil, nil)
}

// --- Messages and signals ---------------------------------------------------

// SendMessage correlates a message into whatever instance is waiting on it.
//
// correlationKey selects which instance when several wait on the same message
// name. Pass "" only when the message name alone is unambiguous — an empty
// key matches every waiting subscription for that name.
func (c *Client) SendMessage(ctx context.Context, projectID, messageName, correlationKey string, variables Variables) error {
	return c.do(ctx, http.MethodPost, "/api/v1/processes/message", map[string]any{
		"project_id":      projectID,
		"message_name":    messageName,
		"correlation_key": correlationKey,
		"variables":       variables,
	}, nil)
}

// BroadcastSignal delivers a signal to every instance in the project waiting
// on it. Unlike a message, a signal has no single addressee.
func (c *Client) BroadcastSignal(ctx context.Context, projectID, signalName string, variables Variables) error {
	return c.do(ctx, http.MethodPost, "/api/v1/processes/signal", map[string]any{
		"project_id":  projectID,
		"signal_name": signalName,
		"variables":   variables,
	}, nil)
}

// --- Human tasks ------------------------------------------------------------

// ListTasksOptions filters and pages a task listing. The zero value lists the
// first page at the server's default size.
type ListTasksOptions struct {
	// ProjectID limits the listing to one project. Empty lists across the
	// caller's whole organization.
	ProjectID string
	// InstanceID limits the listing to one process instance — "what is this run
	// waiting on". It takes precedence over ProjectID, which it already
	// implies, since an instance belongs to exactly one project.
	InstanceID string
	// Page is 1-based. Zero means the first page.
	Page int
	// PageSize is how many tasks per page. Zero means the server's default.
	PageSize int
}

// ListTasks lists human tasks, newest first — the server orders by creation
// time descending. Without a ProjectID it lists across the caller's whole
// organization.
//
// It returns every status, including completed ones; filter with
// [TaskStatus.IsOpen] for an inbox view. The returned [*PageInfo] is nil when
// the server did not page.
//
// Set InstanceID to ask what one run is waiting on, rather than listing a
// project and matching [UserTask.InstanceID] yourself.
//
// Each task carries its BPMN element as [UserTask.NodeID] and its element kind
// as [UserTask.Type]. Node.Name is always empty — a task can be renamed, after
// which its name is no longer the diagram's label, so the server does not guess
// — and Node.Type is empty when talking to a server older than the one that
// began sending it. [UserTask.Type] is the field to read either way.
func (c *Client) ListTasks(ctx context.Context, opts ListTasksOptions) ([]UserTask, *PageInfo, error) {
	query := url.Values{}
	if opts.ProjectID != "" {
		query.Set("project_id", opts.ProjectID)
	}
	if opts.InstanceID != "" {
		query.Set("instance_id", opts.InstanceID)
	}
	if opts.Page > 0 {
		query.Set("page", fmt.Sprint(opts.Page))
	}
	if opts.PageSize > 0 {
		query.Set("page_size", fmt.Sprint(opts.PageSize))
	}
	path := "/api/v1/tasks"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}

	var out struct {
		Page  *PageInfo  `json:"page"`
		Tasks []UserTask `json:"tasks"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Tasks, out.Page, err
}

// ErrNoTasks is what [Client.LatestTask] returns when the project has no human
// tasks at all. The request succeeded and the answer was empty, so [IsNotFound]
// is false for it.
var ErrNoTasks = errors.New("metis: the project has no human tasks")

// LatestTask returns the most recently created human task matching opts — the
// last step to reach somebody's inbox — or [ErrNoTasks] when there are none.
//
// Scope is whatever opts narrows to. Give it an instance to ask where one run
// has got to, or a project for the newest task anywhere in it:
//
//	task, err := client.LatestTask(ctx, metis.ListTasksOptions{InstanceID: instanceID})
//	task, err := client.LatestTask(ctx, metis.ListTasksOptions{ProjectID: projectID})
//
// Page and PageSize in opts are ignored: this asks the server for a single row
// rather than a page it would throw away.
//
// It returns the newest task whatever its status, including one already
// completed, because that is what "latest" means. For the newest task somebody
// still has to act on, list and filter with [TaskStatus.IsOpen]:
//
//	tasks, _, err := client.ListTasks(ctx, opts)
//	for _, task := range tasks {          // already newest-first
//		if task.Status.IsOpen() {
//			return &task, nil
//		}
//	}
func (c *Client) LatestTask(ctx context.Context, opts ListTasksOptions) (*UserTask, error) {
	opts.Page, opts.PageSize = 0, 1
	tasks, _, err := c.ListTasks(ctx, opts)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, ErrNoTasks
	}
	// The server orders by created_at descending, so the newest is first.
	return &tasks[0], nil
}

// ListTasksByAssignee returns the tasks held by one person, newest first — the
// listing behind "my inbox".
//
//	tasks, page, err := client.ListTasksByAssignee(ctx, "alice", metis.PageOptions{PageSize: 50})
//
// assignee is a username, the same spelling [UserTask.AssigneeUsername]
// returns. This is a different endpoint from [Client.ListTasks] and takes no
// project or instance filter, which is why it has its own paging-only options.
//
// It returns tasks in every status, including ones the person already
// completed; filter with [TaskStatus.IsOpen] for what is still theirs to do.
func (c *Client) ListTasksByAssignee(ctx context.Context, assignee string, page PageOptions) ([]UserTask, *PageInfo, error) {
	if assignee == "" {
		return nil, nil, errors.New("metis: ListTasksByAssignee needs a username — use ListTasks for an unfiltered listing")
	}
	query := url.Values{}
	if page.Page > 0 {
		query.Set("page", fmt.Sprint(page.Page))
	}
	if page.PageSize > 0 {
		query.Set("page_size", fmt.Sprint(page.PageSize))
	}
	path := "/api/v1/tasks/assignee/" + url.PathEscape(assignee)
	if len(query) > 0 {
		path += "?" + query.Encode()
	}

	var out struct {
		Page  *PageInfo  `json:"page"`
		Tasks []UserTask `json:"tasks"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Tasks, out.Page, err
}

// GetTask returns one human task.
func (c *Client) GetTask(ctx context.Context, taskID string) (*UserTask, error) {
	var out struct {
		Task UserTask `json:"task"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/tasks/"+url.PathEscape(taskID), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out.Task, nil
}

// ClaimTask takes a task for *the signed-in user*, so nobody else works it in
// parallel.
//
// Who claims it is the client's token — there is no way to claim on somebody
// else's behalf, and the server does not accept an override. To hand a task to
// a particular person, use [Client.AssignTask]; to give one back, use
// [Client.UnclaimTask].
func (c *Client) ClaimTask(ctx context.Context, taskID string) error {
	// An empty object rather than no body: the server decodes a JSON body here,
	// and a missing one is a decode error rather than an empty request.
	return c.do(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/claim",
		map[string]any{}, nil)
}

// UnclaimTask returns a claimed task to the inbox for somebody else to take.
func (c *Client) UnclaimTask(ctx context.Context, taskID string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/unclaim", nil, nil)
}

// AssignTask hands a task to a named person and marks it claimed by them. This
// is the "claim on behalf of" that [Client.ClaimTask] deliberately is not.
//
// username is the person's username, not their UUID — the wire field is spelled
// user_id for historical reasons, but a username is what the server stores.
//
// Not everyone may do this. The server allows it only for an administrator or
// for the person currently holding the task, and answers 403 otherwise — so an
// ordinary user can pass a task on, but cannot reach into the inbox and assign
// one they never held. Reassigning an *unclaimed* task is therefore an
// administrator's operation. Check with [IsUnauthorized].
func (c *Client) AssignTask(ctx context.Context, taskID, username string) error {
	if username == "" {
		return errors.New("metis: AssignTask needs a username — say who the task is for, or use UnclaimTask to return it to the inbox")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/assign", map[string]any{
		"user_id": username,
	}, nil)
}

// DelegateTask hands a task to somebody else to act on, leaving it theirs to
// finish. The task moves to [TaskDelegated].
//
// username is the person's username, not their UUID — as with
// [Client.AssignTask], the wire field is spelled user_id for historical
// reasons.
//
// Delegating and assigning differ in intent rather than mechanics: assigning
// says the task is now theirs, delegating says they are acting on it for you.
// Both are allowed only for an administrator or the person currently holding
// the task, and answer 403 otherwise — check with [IsUnauthorized].
func (c *Client) DelegateTask(ctx context.Context, taskID, username string) error {
	if username == "" {
		return errors.New("metis: DelegateTask needs a username — say who is to act on the task")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/delegate", map[string]any{
		"user_id": username,
	}, nil)
}

// CompleteTask finishes a task as the signed-in user, writing variables back
// into the process, after which the instance moves on.
//
// The variables are laid over the instance's existing ones; keys you do not
// mention keep their values. Pass nil to complete without writing any.
//
// As with [Client.ClaimTask], the acting user is the client's token. Completing
// a task somebody else holds is refused by the server.
func (c *Client) CompleteTask(ctx context.Context, taskID string, variables Variables) error {
	return c.do(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/complete", map[string]any{
		"variables": variables,
	}, nil)
}
