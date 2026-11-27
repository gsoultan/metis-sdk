package metis

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// PeopleSource is where a project's people are read from, for
// [Client.ImportPeople] and [Client.CheckPeopleImport]: a JSON endpoint of
// your application, or a query against your own PostgreSQL database.
type PeopleSource struct {
	// Kind is "http" or "postgres".
	Kind string `json:"kind"`

	// An endpoint answering a JSON array of people, or an object with the
	// array under data, items, results, users or participants. Headers
	// authenticate the call and are never returned.
	URL     string            `json:"url,omitempty"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// A query against your database. Needs the Query author role. The query
	// runs read-only.
	DSN   string `json:"dsn,omitempty"`
	Query string `json:"query,omitempty"`

	// Mapping names your fields the way metis reads them, keyed by metis's
	// name: external_id, username, display_name, email, groups, active. A
	// nested JSON field is named by its path, "profile.full_name". A field
	// not mapped is read under metis's own name.
	Mapping map[string]string `json:"mapping,omitempty"`

	// MoveOpenTasks lets a person renamed in your application carry the
	// open tasks naming their old username over to the new one. Without it
	// such a rename is reported in Problems and not applied.
	MoveOpenTasks bool `json:"move_open_tasks,omitempty"`
}

// ImportProblem is a row that was not imported, and why.
type ImportProblem struct {
	Line     int    `json:"line"`
	Username string `json:"username,omitempty"`
	Reason   string `json:"reason"`
}

// PeopleImport is what [Client.ImportPeople] did.
type PeopleImport struct {
	Created    int             `json:"created"`
	Updated    int             `json:"updated"`
	Unchanged  int             `json:"unchanged"`
	Renamed    int             `json:"renamed"`
	TasksMoved int             `json:"tasks_moved"`
	Groups     int             `json:"groups"`
	Problems   []ImportProblem `json:"problems,omitempty"`
}

// The outcomes a [CheckedPerson] can have.
const (
	ImportNew       = "new"
	ImportUnchanged = "unchanged"
	ImportUpdate    = "update"
	ImportLinked    = "linked"
	ImportRenamed   = "renamed"
	ImportConflict  = "conflict"
	ImportInvalid   = "invalid"
)

// CheckedPerson is one row of a check and what importing it would do.
type CheckedPerson struct {
	Line        int    `json:"line"`
	Outcome     string `json:"outcome"`
	Username    string `json:"username,omitempty"`
	ExternalID  string `json:"external_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	// Detail says what would change, or why the row cannot be imported.
	Detail string `json:"detail,omitempty"`
	// OpenTasks is, for a rename, how many open tasks name the old username.
	OpenTasks int `json:"open_tasks,omitempty"`
}

// PeopleImportCheck is what importing a source would do, worked out without
// writing anything.
type PeopleImportCheck struct {
	// Read is how many rows the source returned.
	Read int `json:"read"`
	// Counts are the rows by outcome (ImportNew, ImportUpdate, …).
	Counts map[string]int `json:"counts"`
	// Fields are the names your source used, for building a Mapping.
	Fields []string `json:"fields"`
	// Sample is the first record as your source sent it.
	Sample map[string]string `json:"sample,omitempty"`
	// Rows are every row that is not unchanged, in source order, up to a
	// limit the server sets; Truncated says it was reached.
	Rows      []CheckedPerson `json:"rows"`
	Truncated bool            `json:"truncated,omitempty"`
}

type peopleImportRequest struct {
	ProjectID string `json:"project_id"`
	PeopleSource
}

// ImportPeople reads a project's people from src. People are matched on
// their external ID first and their username after, so somebody renamed in
// your application is renamed in metis rather than added twice. A row that
// cannot be imported is reported in Problems and the rest still import.
//
// It needs the designer role or above, and a metis server that knows
// external IDs and mappings: an older one ignores them.
func (c *Client) ImportPeople(ctx context.Context, projectID string, src PeopleSource) (PeopleImport, error) {
	if projectID == "" {
		return PeopleImport{}, errors.New("metis: ImportPeople needs a project ID")
	}
	var out PeopleImport
	err := c.do(ctx, http.MethodPost, "/api/v1/participants/import", peopleImportRequest{projectID, src}, &out)
	return out, err
}

// CheckPeopleImport works out what [Client.ImportPeople] would do with the
// same source and writes nothing.
//
// It needs a metis server that has POST /api/v1/participants/import/check:
// an older one answers 404 ([IsNotFound]).
func (c *Client) CheckPeopleImport(ctx context.Context, projectID string, src PeopleSource) (PeopleImportCheck, error) {
	if projectID == "" {
		return PeopleImportCheck{}, errors.New("metis: CheckPeopleImport needs a project ID")
	}
	var out PeopleImportCheck
	err := c.do(ctx, http.MethodPost, "/api/v1/participants/import/check", peopleImportRequest{projectID, src}, &out)
	return out, err
}

// FindParticipantByExternalID finds the person in a project with your
// application's id for them, and so the username metis assigns their work
// to. It answers false when nobody in the project has that id.
func (c *Client) FindParticipantByExternalID(ctx context.Context, projectID, externalID string) (Participant, bool, error) {
	if projectID == "" || externalID == "" {
		return Participant{}, false, errors.New("metis: FindParticipantByExternalID needs a project ID and an external ID")
	}
	var out struct {
		Participants []Participant `json:"participants"`
	}
	query := url.Values{"project_id": {projectID}, "external_id": {externalID}}.Encode()
	if err := c.do(ctx, http.MethodGet, "/api/v1/participants?"+query, nil, &out); err != nil {
		return Participant{}, false, err
	}
	// Compared here too: a server that does not know external IDs ignores
	// the filter and answers everybody.
	for _, person := range out.Participants {
		if person.ExternalID == externalID {
			return person, true, nil
		}
	}
	return Participant{}, false, nil
}
