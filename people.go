package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// People are the members of a project's own directory: the employees,
// customers or suppliers a process hands work to, as opposed to the Users who
// run Metis. A person signs in to one project, sees only the tasks the
// process gives them, and works those tasks through routes of their own under
// /api/v1/people/. A People token is refused everywhere else, as a User's
// token is refused there.
//
// Two ways in:
//
//   - [Client.LoginPerson], with the person's project, username and password.
//   - One of the person's API keys, made with [Client.CreatePersonAPIKey] and
//     passed to [WithToken]. This is how a person's own system works their
//     tasks for them. A key works tasks; it cannot make or revoke keys.
//
// Everything here needs a Metis server newer than **v0.5.0**, and People
// sign-in has to be switched on there (METIS_PEOPLE_TOKEN_KEY). An older
// server has none of these routes: it answers 404 ([IsNotFound]) to a User's
// token, and 401 ([IsUnauthorized]) to a request with no token it knows,
// which is every People sign-in.
//
// A Person is one of them, as the server knows them once they have signed in.
type Person struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
}

// PersonSession is what signing a person in gives back.
type PersonSession struct {
	Person Person
	// ExpiresAt is when the token stops working. Sign in again after it.
	ExpiresAt time.Time
}

// LoginPerson signs a person in to their project and stores the token on the
// client for every later call, as [Client.Login] does for a User.
//
// The client then speaks as that person: the People calls work, and the rest
// of the API refuses the token. Use a separate client for each.
func (c *Client) LoginPerson(ctx context.Context, projectID, username, password string) (*PersonSession, error) {
	if projectID == "" {
		return nil, errors.New("metis: LoginPerson needs a project ID — a person belongs to one project's directory")
	}
	var out struct {
		Person    *Person   `json:"person"`
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/people/login", map[string]string{
		"project_id": projectID,
		"username":   username,
		"password":   password,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Token == "" || out.Person == nil {
		return nil, errors.New("metis: the person sign-in answered without a token")
	}

	c.mu.Lock()
	c.token = out.Token
	c.mu.Unlock()
	return &PersonSession{Person: *out.Person, ExpiresAt: out.ExpiresAt}, nil
}

// CurrentPerson returns who the client's token signs in, as the directory has
// them now. It works with an API key as well as with a sign-in.
func (c *Client) CurrentPerson(ctx context.Context) (*Person, error) {
	var out struct {
		Person Person `json:"person"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/people/me", nil, &out); err != nil {
		return nil, err
	}
	return &out.Person, nil
}

// ListPersonTasks returns a page of the signed-in person's work: the tasks
// they hold and the ones they may take. Who they are is the token's, never
// an argument.
func (c *Client) ListPersonTasks(ctx context.Context, page PageOptions) ([]UserTask, *PageInfo, error) {
	path := "/api/v1/people/tasks"
	if query := page.query(); len(query) > 0 {
		path += "?" + query.Encode()
	}
	// This route writes the page's numbers beside the tasks rather than
	// under "page", as the other listings do.
	var out struct {
		Tasks    []UserTask `json:"tasks"`
		Total    int64      `json:"total"`
		Page     int        `json:"page"`
		PageSize int        `json:"page_size"`
		HasMore  bool       `json:"has_more"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, nil, err
	}
	info := &PageInfo{Total: out.Total, Page: out.Page, PageSize: out.PageSize, HasMore: out.HasMore}
	return out.Tasks, info, nil
}

// GetPersonTask returns one of the signed-in person's tasks. A task that is
// not theirs to see answers 404 ([IsNotFound]), as one that does not exist
// does.
func (c *Client) GetPersonTask(ctx context.Context, taskID string) (*UserTask, error) {
	if taskID == "" {
		return nil, errors.New("metis: GetPersonTask needs a task ID")
	}
	var out struct {
		Task *UserTask `json:"task"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/people/tasks/"+url.PathEscape(taskID), nil, &out); err != nil {
		return nil, err
	}
	if out.Task == nil {
		return nil, fmt.Errorf("metis: the server answered for task %s without the task", taskID)
	}
	return out.Task, nil
}

// ClaimPersonTask takes a task for the signed-in person, so nobody else in
// the directory works it at the same time.
func (c *Client) ClaimPersonTask(ctx context.Context, taskID string) error {
	return c.personTaskAction(ctx, "ClaimPersonTask", taskID, "claim", nil)
}

// ReleasePersonTask gives a task the signed-in person holds back, for
// somebody else to take.
func (c *Client) ReleasePersonTask(ctx context.Context, taskID string) error {
	return c.personTaskAction(ctx, "ReleasePersonTask", taskID, "release", nil)
}

// CompletePersonTask finishes a task as the signed-in person, writing the
// form's variables into the process. As with [Client.CompleteTask], only the
// variables the step's form declares may be set; anything else is refused
// with a 400 ([IsInvalid]). Pass nil when the step asks for nothing.
func (c *Client) CompletePersonTask(ctx context.Context, taskID string, variables Variables) error {
	return c.personTaskAction(ctx, "CompletePersonTask", taskID, "complete",
		map[string]any{"variables": variables})
}

func (c *Client) personTaskAction(ctx context.Context, call, taskID, action string, body any) error {
	if taskID == "" {
		return fmt.Errorf("metis: %s needs a task ID", call)
	}
	return c.do(ctx, http.MethodPost, "/api/v1/people/tasks/"+url.PathEscape(taskID)+"/"+action, body, nil)
}

// PersonAPIKey is one of a person's API keys, as listed. The key itself is
// shown once, when it is made, and never again: what is kept here is enough to
// recognise it.
type PersonAPIKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Prefix is the start of the key, to tell keys apart by.
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Revoked reports whether the key no longer works.
func (k PersonAPIKey) Revoked() bool { return k.RevokedAt != nil }

// ListPersonAPIKeys returns the signed-in person's API keys, revoked ones
// included. It needs the person's sign-in: an API key cannot list keys.
func (c *Client) ListPersonAPIKeys(ctx context.Context) ([]PersonAPIKey, error) {
	var out struct {
		Keys []PersonAPIKey `json:"keys"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/people/keys", nil, &out)
	return out.Keys, err
}

// CreatePersonAPIKey makes an API key for the signed-in person and returns it
// with the secret token that is the key. The token is shown this once: store
// it where the system that will use it can read it, and pass it to
// [WithToken] there. It needs the person's sign-in: an API key cannot make
// another.
//
// name says which system the key is for, so the person can tell their keys
// apart and revoke the right one.
func (c *Client) CreatePersonAPIKey(ctx context.Context, name string) (*PersonAPIKey, string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, "", errors.New("metis: CreatePersonAPIKey needs a name — say which system the key is for")
	}
	var out struct {
		Key   *PersonAPIKey `json:"key"`
		Token string        `json:"token"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/people/keys", map[string]string{"name": name}, &out); err != nil {
		return nil, "", err
	}
	if out.Key == nil || out.Token == "" {
		return nil, "", errors.New("metis: the server made no key")
	}
	return out.Key, out.Token, nil
}

// RevokePersonAPIKey stops one of the signed-in person's API keys working, at
// once. It needs the person's sign-in.
func (c *Client) RevokePersonAPIKey(ctx context.Context, keyID string) error {
	if keyID == "" {
		return errors.New("metis: RevokePersonAPIKey needs a key ID")
	}
	return c.do(ctx, http.MethodDelete, "/api/v1/people/keys/"+url.PathEscape(keyID), nil, nil)
}

// TaskLink is what a task link mailed to a person opens: the task and who it
// is for, without anybody signing in.
type TaskLink struct {
	Task   UserTask
	Person Person
	// ExpiresAt is when the link stops working.
	ExpiresAt time.Time
}

// ReadTaskLink shows the task a task link is for, and changes nothing. token
// is the link's token. This is for a page of your own that people reach from
// the link; it needs no sign-in, and the client's own token is not used to
// decide anything.
//
// The token goes in the request body, never the address, so it stays out of
// access logs.
func (c *Client) ReadTaskLink(ctx context.Context, token string) (*TaskLink, error) {
	if token == "" {
		return nil, errors.New("metis: ReadTaskLink needs the link's token")
	}
	var out struct {
		Task      *UserTask `json:"task"`
		Person    *Person   `json:"person"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/people/links/read", map[string]string{"token": token}, &out); err != nil {
		return nil, err
	}
	if out.Task == nil || out.Person == nil {
		return nil, errors.New("metis: the server answered the task link without its task")
	}
	return &TaskLink{Task: *out.Task, Person: *out.Person, ExpiresAt: out.ExpiresAt}, nil
}

// CompleteByTaskLink completes the task a link is for, as the link's person,
// and uses the link up. As with [Client.CompletePersonTask], only the
// variables the step's form declares may be set.
func (c *Client) CompleteByTaskLink(ctx context.Context, token string, variables Variables) error {
	if token == "" {
		return errors.New("metis: CompleteByTaskLink needs the link's token")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/people/links/complete", map[string]any{
		"token":     token,
		"variables": variables,
	}, nil)
}
