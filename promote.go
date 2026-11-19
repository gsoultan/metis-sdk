package metis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// --- Promoting models between environments ---------------------------------
//
// A promotion is a set of process and decision versions cut where they were
// designed and tested, then imported into the next environment staged and
// made live there together. Each call works on the environment the client
// points at: one client for development, another for production, each at
// its environment's address. Needs a server with the releases routes.

// Promotion is a set of model versions as one environment holds it.
type Promotion struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	// Hash is the SHA-256 of the content; two environments holding the same
	// promotion hold the same hash.
	Hash string `json:"hash"`
	// Origin is "cut" where it was cut and "imported" where it was imported.
	Origin string `json:"origin"`
	// State is "cut", "staged", "live" or "superseded".
	State       string          `json:"state"`
	Items       []PromotionItem `json:"items"`
	Connections []string        `json:"connections"`
	CutAt       time.Time       `json:"cut_at"`
	CutBy       string          `json:"cut_by"`
	ActedBy     string          `json:"acted_by"`
	ImportedBy  string          `json:"imported_by,omitempty"`
	ApprovedBy  string          `json:"approved_by,omitempty"`
	LiveAt      *time.Time      `json:"live_at,omitempty"`
	Previous    string          `json:"previous,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	// Checked is what the promotion passed where it was cut. Nil for one cut
	// by a server that did not check promotions at the cut.
	Checked *PromotionChecks `json:"checked,omitempty"`
	// LiveNote is another environment's signed note that the promotion went
	// live there, when one was attached here with [Client.AttachLiveNote].
	LiveNote *PromotionLiveNote `json:"live_note,omitempty"`
}

// PromotionLiveNote is one environment's signed word that a promotion went
// live there. A production with a staging before it can require staging's
// note before the same promotion goes live in production.
type PromotionLiveNote struct {
	Format    string    `json:"format"`
	Release   string    `json:"release"`
	Hash      string    `json:"hash"`
	LiveAt    time.Time `json:"live_at"`
	Key       string    `json:"key"`
	Signature string    `json:"signature"`
}

// PromotionChecks is what a promotion passed where it was cut: its
// decisions' own tests and its processes' saved simulation cases. It is part
// of the signed content, so an environment that trusts the signature knows
// the promotion was checked where it was made.
type PromotionChecks struct {
	At              time.Time `json:"at"`
	DecisionTests   int       `json:"decision_tests"`
	SimulationCases int       `json:"simulation_cases"`
}

// PromotionItem is one model of a promotion.
type PromotionItem struct {
	// Kind is "process" or "decision".
	Kind string `json:"kind"`
	Key  string `json:"key"`
	// Version is the version this environment holds it as. Zero means the key
	// is new here and is stored when the promotion goes live.
	Version int `json:"version"`
	// SourceVersion is the version it was where it was cut.
	SourceVersion int `json:"source_version"`
}

// CutPromotion asks for a promotion of a project's live models.
type CutPromotion struct {
	Name string
	// Processes are process keys. The decisions they consult and the
	// processes they call are carried with them.
	Processes []string
	// Decisions are decision keys to carry besides those.
	Decisions []string
}

func promotionsPath(projectID string) string {
	return "/api/v1/projects/" + url.PathEscape(projectID) + "/releases"
}

func (c *Client) promotion(ctx context.Context, method, path string, body any) (*Promotion, error) {
	var out struct {
		Release Promotion `json:"release"`
	}
	if err := c.do(ctx, method, path, body, &out); err != nil {
		return nil, err
	}
	return &out.Release, nil
}

// CutPromotion records the live versions of the named models in the
// environment the client points at. Nothing that runs changes. The server
// first runs the decisions' own tests and the processes' saved simulation
// cases there, and refuses the cut, listing what failed, if any fail: that
// refusal is [IsInvalid].
func (c *Client) CutPromotion(ctx context.Context, projectID string, req CutPromotion) (*Promotion, error) {
	if req.Name == "" {
		return nil, errors.New("metis: cut promotion: a name is needed")
	}
	processes, decisions := req.Processes, req.Decisions
	if processes == nil {
		processes = []string{}
	}
	if decisions == nil {
		decisions = []string{}
	}
	return c.promotion(ctx, http.MethodPost, promotionsPath(projectID),
		map[string]any{"name": req.Name, "processes": processes, "decisions": decisions})
}

// ImportPromotion imports a promotion cut in another environment of the same
// project into the one the client points at, staged: stored and checked,
// not live. An empty fromEnvironment is the installation's own runtime.
func (c *Client) ImportPromotion(ctx context.Context, projectID, fromEnvironment, name string) (*Promotion, error) {
	return c.promotion(ctx, http.MethodPost, promotionsPath(projectID)+"/import",
		map[string]any{"from_environment": fromEnvironment, "name": name})
}

// ApprovePromotion records that someone other than who cut and who imported
// a staged promotion agrees it may go live. A protected environment needs it.
func (c *Client) ApprovePromotion(ctx context.Context, projectID, name string) (*Promotion, error) {
	return c.promotion(ctx, http.MethodPost, promotionsPath(projectID)+"/"+url.PathEscape(name)+"/approve", nil)
}

// GoLive makes every model of a staged promotion live together, once its
// decisions pass their tests and its processes their saved simulation cases.
func (c *Client) GoLive(ctx context.Context, projectID, name string) (*Promotion, error) {
	return c.promotion(ctx, http.MethodPost, promotionsPath(projectID)+"/"+url.PathEscape(name)+"/go-live", nil)
}

// RollBackPromotion makes the promotion that was live before the live one
// live again.
func (c *Client) RollBackPromotion(ctx context.Context, projectID string) (*Promotion, error) {
	return c.promotion(ctx, http.MethodPost, promotionsPath(projectID)+"/rollback", nil)
}

// GetPromotion reads one promotion.
func (c *Client) GetPromotion(ctx context.Context, projectID, name string) (*Promotion, error) {
	return c.promotion(ctx, http.MethodGet, promotionsPath(projectID)+"/"+url.PathEscape(name), nil)
}

// ListPromotions lists a project's promotions, newest first.
func (c *Client) ListPromotions(ctx context.Context, projectID string) ([]Promotion, error) {
	var out struct {
		Releases []Promotion `json:"releases"`
	}
	if err := c.do(ctx, http.MethodGet, promotionsPath(projectID), nil, &out); err != nil {
		return nil, err
	}
	return out.Releases, nil
}

// ExportPromotion is a promotion as a file signed by the installation the
// client points at, for another installation to import with
// [Client.ImportPromotionFile]. The server needs a release signing key.
func (c *Client) ExportPromotion(ctx context.Context, projectID, name string) ([]byte, error) {
	var out struct {
		File json.RawMessage `json:"file"`
	}
	if err := c.do(ctx, http.MethodGet, promotionsPath(projectID)+"/"+url.PathEscape(name)+"/export", nil, &out); err != nil {
		return nil, err
	}
	return out.File, nil
}

// ImportPromotionFile imports a file [Client.ExportPromotion] returned, from
// an installation the target trusts, staged.
func (c *Client) ImportPromotionFile(ctx context.Context, projectID string, file []byte) (*Promotion, error) {
	if !json.Valid(file) {
		return nil, errors.New("metis: import promotion file: the file is not JSON")
	}
	return c.promotion(ctx, http.MethodPost, promotionsPath(projectID)+"/import-file",
		map[string]any{"file": json.RawMessage(file)})
}

// LiveNote is the environment the client points at's signed note that a
// promotion went live there, for the next environment to attach with
// [Client.AttachLiveNote]. Only a promotion that has gone live has one; the
// server needs a release signing key.
func (c *Client) LiveNote(ctx context.Context, projectID, name string) ([]byte, error) {
	var out struct {
		Note json.RawMessage `json:"note"`
	}
	if err := c.do(ctx, http.MethodGet, promotionsPath(projectID)+"/"+url.PathEscape(name)+"/live-note", nil, &out); err != nil {
		return nil, err
	}
	return out.Note, nil
}

// AttachLiveNote attaches a note [Client.LiveNote] returned from another
// environment to a staged promotion. It is refused unless it is for exactly
// this promotion and signed by a key the target trusts. A production whose
// server requires one goes live only with it.
func (c *Client) AttachLiveNote(ctx context.Context, projectID, name string, note []byte) (*Promotion, error) {
	if !json.Valid(note) {
		return nil, errors.New("metis: attach live note: the note is not JSON")
	}
	return c.promotion(ctx, http.MethodPost, promotionsPath(projectID)+"/"+url.PathEscape(name)+"/live-note",
		map[string]any{"note": json.RawMessage(note)})
}
