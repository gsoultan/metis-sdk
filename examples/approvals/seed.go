package main

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	metis "github.com/gsoultan/metis-sdk"
)

//go:embed process.bpmn
var processBPMN string

//go:embed decision.json
var decisionJSON []byte

// processKey is the id of the <process> in process.bpmn.
const processKey = "expense-approval"

// processXML is the definition with the budget API's address filled in.
func processXML(publicURL string) []byte {
	return []byte(strings.ReplaceAll(processBPMN, "{{BUDGET_URL}}", publicURL+"/api/budget/check"))
}

// seed sets an installation up to run the sample: the three approver groups,
// the four people, the decision table, and the process. It is safe to run
// again; what exists already is left as it is, and the process is deployed as
// a new version.
//
// The process is deployed through the SDK. People, groups and decision tables
// are administration the SDK does not cover — it is a client for applications,
// not for running an installation — so those few calls go straight to the
// REST API, in admin below.
func seed(ctx context.Context, cfg config) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	client := metis.NewClient(cfg.MetisURL)
	if err := client.Login(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
		return fmt.Errorf("sign in as %s: %w", cfg.AdminUser, err)
	}
	projectID, err := findProject(ctx, client, cfg.Project)
	if err != nil {
		return err
	}

	a, err := newAdmin(ctx, cfg)
	if err != nil {
		return err
	}
	for _, p := range people {
		if p.Group != "" {
			if err := a.ensureGroup(ctx, p.Group); err != nil {
				return err
			}
		}
		if err := a.ensureUser(ctx, p, cfg.PeoplePassword); err != nil {
			return err
		}
	}
	log.Printf("✓ people: %d users, in groups requesters, supervisors, unit-heads and finance", len(people))

	if err := a.ensureDecision(ctx, projectID); err != nil {
		return err
	}
	log.Print("✓ decision table expense-classify")

	defID, err := client.ImportDefinition(ctx, projectID, processXML(cfg.PublicURL))
	if err != nil {
		return fmt.Errorf("deploy the process: %w", err)
	}
	log.Printf("✓ deployed %s as definition %s; the budget check calls %s/api/budget/check", processKey, defID, cfg.PublicURL)
	return nil
}

func findProject(ctx context.Context, client *metis.Client, name string) (string, error) {
	projects, err := client.ListProjects(ctx)
	if err != nil {
		return "", fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		if p.Name == name {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("no project named %q; set METIS_PROJECT to one of the %d there are", name, len(projects))
}

// admin makes the REST calls seed needs that the SDK has no method for.
type admin struct {
	base, token, org string
	http             *http.Client
}

func newAdmin(ctx context.Context, cfg config) (*admin, error) {
	a := &admin{base: strings.TrimRight(cfg.MetisURL, "/") + "/api/v1", http: &http.Client{Timeout: 30 * time.Second}}
	var login struct {
		Token string `json:"token"`
	}
	if err := a.call(ctx, http.MethodPost, "/login", map[string]string{"username": cfg.AdminUser, "password": cfg.AdminPassword}, &login); err != nil {
		return nil, fmt.Errorf("sign in for set-up: %w", err)
	}
	a.token = login.Token

	var orgs struct {
		Organizations []struct {
			ID string `json:"id"`
		} `json:"organizations"`
	}
	if err := a.call(ctx, http.MethodGet, "/organizations", nil, &orgs); err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	if len(orgs.Organizations) == 0 {
		return nil, errors.New("the administrator belongs to no organization")
	}
	a.org = orgs.Organizations[0].ID
	return a, nil
}

type named struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

func (a *admin) groupID(ctx context.Context, name string) (string, error) {
	var out struct {
		Groups []named `json:"groups"`
	}
	if err := a.call(ctx, http.MethodGet, "/organizations/"+a.org+"/groups", nil, &out); err != nil {
		return "", fmt.Errorf("list groups: %w", err)
	}
	for _, g := range out.Groups {
		if g.Name == name {
			return g.ID, nil
		}
	}
	return "", nil
}

func (a *admin) userID(ctx context.Context, username string) (string, error) {
	var out struct {
		Users []named `json:"users"`
	}
	if err := a.call(ctx, http.MethodGet, "/organizations/"+a.org+"/users", nil, &out); err != nil {
		return "", fmt.Errorf("list users: %w", err)
	}
	for _, u := range out.Users {
		if u.Username == username {
			return u.ID, nil
		}
	}
	return "", nil
}

func (a *admin) ensureGroup(ctx context.Context, name string) error {
	if id, err := a.groupID(ctx, name); err != nil || id != "" {
		return err
	}
	body := map[string]any{"group": map[string]any{"name": name, "description": "Approves expenses in the approvals sample"}}
	if err := a.call(ctx, http.MethodPost, "/organizations/"+a.org+"/groups", body, nil); err != nil {
		return fmt.Errorf("create group %s: %w", name, err)
	}
	return nil
}

func (a *admin) ensureUser(ctx context.Context, p person, password string) error {
	uid, err := a.userID(ctx, p.Username)
	if err != nil {
		return err
	}
	if uid == "" {
		body := map[string]any{
			"user": map[string]any{
				// A user created without an organization is refused on every
				// request they make.
				"organizations": []map[string]string{{"id": a.org}},
				"username":      p.Username,
				"full_name":     p.Name,
				"display_name":  p.Name,
				"email":         p.Username + "@example.invalid",
			},
			"password": password,
		}
		if err := a.call(ctx, http.MethodPost, "/users", body, nil); err != nil {
			return fmt.Errorf("create user %s: %w", p.Username, err)
		}
		if uid, err = a.userID(ctx, p.Username); err != nil || uid == "" {
			return fmt.Errorf("user %s was created and cannot be found: %v", p.Username, err)
		}
	}
	if p.Group == "" {
		return nil
	}
	gid, err := a.groupID(ctx, p.Group)
	if err != nil || gid == "" {
		return fmt.Errorf("group %s cannot be found: %v", p.Group, err)
	}
	if err := a.call(ctx, http.MethodPost, "/groups/"+gid+"/members/"+uid, nil, nil); err != nil && !isAlready(err) {
		return fmt.Errorf("add %s to %s: %w", p.Username, p.Group, err)
	}
	return nil
}

func (a *admin) ensureDecision(ctx context.Context, projectID string) error {
	var decision map[string]any
	if err := json.Unmarshal(decisionJSON, &decision); err != nil {
		return fmt.Errorf("decision.json: %w", err)
	}
	// A reference is an object, not an id.
	decision["project"] = map[string]string{"id": projectID}
	if err := a.call(ctx, http.MethodPost, "/decisions", map[string]any{"decision": decision}, nil); err != nil && !isAlready(err) {
		return fmt.Errorf("create decision expense-classify: %w", err)
	}
	return nil
}

// isAlready reports whether err says the thing being made exists already,
// which a second run of seed is expected to hear.
func isAlready(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already") || strings.Contains(msg, "exists") || strings.Contains(msg, "409")
}

func (a *admin) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	// Some routes answer 200 with {"error": "..."} rather than a status.
	var failure struct {
		Error string `json:"error"`
		Err   string `json:"err"`
	}
	_ = json.Unmarshal(raw, &failure)
	if msg := cmp.Or(failure.Error, failure.Err); resp.StatusCode >= 300 || msg != "" {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, cmp.Or(msg, strings.TrimSpace(string(raw))))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
