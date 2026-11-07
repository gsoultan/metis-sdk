package metis

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// What deactivating a person does with the open tasks they hold.
const (
	// HeldWorkKeep leaves them with the person. Nobody can act on them until
	// somebody hands them over; [Client.ListStrandedTasks] lists them.
	HeldWorkKeep = "keep"
	// HeldWorkRelease puts them back on offer to whoever the step offers
	// them to.
	HeldWorkRelease = "release"
	// HeldWorkHandOver gives them to the one account named in To.
	HeldWorkHandOver = "hand_over"
)

// HeldWork says what deactivating a person does with the tasks they hold.
type HeldWork struct {
	// Do is one of the HeldWork* constants. Empty keeps the tasks.
	Do string `json:"do,omitempty"`
	// To is the account the tasks go to, for HeldWorkHandOver.
	To string `json:"to,omitempty"`
	// Reason is kept on each moved task's history. Required to move them.
	Reason string `json:"reason,omitempty"`
}

// HeldTask is one open task a person holds.
type HeldTask struct {
	TaskID     string    `json:"task_id"`
	Name       string    `json:"name"`
	InstanceID string    `json:"instance_id,omitempty"`
	Since      time.Time `json:"since,omitzero"`
}

// KeptTask is a held task that stayed with a deactivated person: every one
// when the work was kept, and any whose move the server refused, with Why.
type KeptTask struct {
	HeldTask
	Why string `json:"why,omitempty"`
}

// ActivationResult is what [Client.SetParticipantActive] did.
type ActivationResult struct {
	// Moved counts the held tasks handed over or released.
	Moved int        `json:"moved"`
	Kept  []KeptTask `json:"kept,omitempty"`
}

// Participant is a person in a project's People: who its processes can give
// work to, separate from the accounts that administer metis.
type Participant struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	// Active is false for somebody deactivated: they cannot sign in and get
	// no new work.
	Active bool `json:"active"`
}

// ListParticipants lists a project's People.
func (c *Client) ListParticipants(ctx context.Context, projectID string) ([]Participant, error) {
	if projectID == "" {
		return nil, errors.New("metis: ListParticipants needs a project ID")
	}
	var out struct {
		Participants []Participant `json:"participants"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/participants?"+url.Values{"project_id": {projectID}}.Encode(), nil, &out)
	return out.Participants, err
}

// ListHeldTasks lists the open tasks a person of the project holds: what
// deactivating them has to settle. participantID is the person's ID in the
// project's People, as [Client.ListParticipants] returns it.
//
// It needs the designer role or above, and a Metis server that has
// GET /api/v1/participants/{id}/held-tasks: an older one answers 404
// ([IsNotFound]).
func (c *Client) ListHeldTasks(ctx context.Context, projectID, participantID string) ([]HeldTask, error) {
	if projectID == "" || participantID == "" {
		return nil, errors.New("metis: ListHeldTasks needs a project ID and a participant ID")
	}
	var out struct {
		Tasks []HeldTask `json:"tasks"`
	}
	path := "/api/v1/participants/" + url.PathEscape(participantID) + "/held-tasks?" + url.Values{"project_id": {projectID}}.Encode()
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Tasks, err
}

// SetParticipantActive deactivates (active false) or reactivates a person
// of the project. A deactivated person cannot sign in and gets no new work.
//
// Deactivating settles the tasks they hold as work says: handed to one
// account, released, or kept (work nil). A move without a reason is refused
// before anything changes. A task whose move the server refuses stays with
// them and is named in the result's Kept, with why. Reactivating moves
// nothing back, and work is ignored.
//
//	res, err := client.SetParticipantActive(ctx, projectID, budiID, false, &metis.HeldWork{
//		Do: metis.HeldWorkHandOver, To: "sari", Reason: "Budi has left",
//	})
func (c *Client) SetParticipantActive(ctx context.Context, projectID, participantID string, active bool, work *HeldWork) (ActivationResult, error) {
	if projectID == "" || participantID == "" {
		return ActivationResult{}, errors.New("metis: SetParticipantActive needs a project ID and a participant ID")
	}
	body := struct {
		Active bool      `json:"active"`
		Work   *HeldWork `json:"work,omitempty"`
	}{Active: active}
	if !active {
		body.Work = work
	}
	var out struct {
		Result ActivationResult `json:"result"`
	}
	path := "/api/v1/participants/" + url.PathEscape(participantID) + "/active?" + url.Values{"project_id": {projectID}}.Encode()
	err := c.do(ctx, http.MethodPut, path, body, &out)
	return out.Result, err
}
