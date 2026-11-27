package metis

import (
	"context"
	"errors"
	"net/http"
)

// RuleFile is one Grule rule file: a .grl file, or Grule's JSON rules. The
// name is only for the report, which says where in which file a rule is.
type RuleFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// GruleImport is what importing a Grule rule set made, or would make.
type GruleImport struct {
	// Decisions are the decision tables, in the order the process runs them.
	Decisions []ImportedDecision `json:"decisions"`
	// Rules says, for every rule in the files, which table it became a line
	// of or why it was left out.
	Rules []GruleRuleOutcome `json:"rules"`
	// Results names the process variable each field a rule sets is written
	// to: Applicant.Discount is read back as Applicant_Discount.
	Results map[string]string `json:"results,omitempty"`
	// Warnings are about the rule set as a whole: rules of equal salience
	// whose order the files decided, keys already taken.
	Warnings []string `json:"warnings,omitempty"`
	// Stored is true once the tables were saved; a preview stores nothing.
	Stored bool `json:"stored,omitempty"`
	// ProcessID is the deployed process that runs the tables, when
	// [GruleDeploy] asked for one.
	ProcessID string `json:"process_id,omitempty"`
}

// Skipped are the rules that were left out, each with its reason.
func (g GruleImport) Skipped() []GruleRuleOutcome {
	var skipped []GruleRuleOutcome
	for _, rule := range g.Rules {
		if rule.Decision == "" {
			skipped = append(skipped, rule)
		}
	}
	return skipped
}

// ImportedDecision names one decision table an import made.
type ImportedDecision struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Version int    `json:"version,omitempty"`
}

// GruleRuleOutcome is what became of one rule.
type GruleRuleOutcome struct {
	Name string `json:"name"`
	// Position is where the rule is: "pricing.grl:12".
	Position string `json:"position"`
	// Decision is the key of the table the rule became a line of, empty when
	// it was left out; Reason then says why.
	Decision string   `json:"decision,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// GruleImportOption adjusts one [Client.ImportGruleRules] call.
type GruleImportOption func(*gruleImportRequest)

type gruleImportRequest struct {
	ProjectID string     `json:"project_id"`
	Key       string     `json:"key"`
	Name      string     `json:"name,omitempty"`
	Files     []RuleFile `json:"files"`
	Preview   bool       `json:"preview,omitempty"`
	Deploy    bool       `json:"deploy,omitempty"`
}

// GrulePreview reports what the import would make and stores nothing.
func GrulePreview() GruleImportOption {
	return func(r *gruleImportRequest) { r.Preview = true }
}

// GruleDeploy deploys the process that runs the tables, as well as storing
// the tables.
func GruleDeploy() GruleImportOption {
	return func(r *gruleImportRequest) { r.Deploy = true }
}

// GruleProcessName names the process; the key is used when none is given.
func GruleProcessName(name string) GruleImportOption {
	return func(r *gruleImportRequest) { r.Name = name }
}

// ImportGruleRules turns a Grule rule set into decision tables in a project,
// and with [GruleDeploy] a process that runs them in the order Grule would
// fire them:
//
//	report, err := client.ImportGruleRules(ctx, projectID, "pricing", files, metis.GrulePreview())
//	if err != nil {
//		return err
//	}
//	for _, rule := range report.Skipped() {
//		log.Printf("%s (%s): %s", rule.Name, rule.Position, rule.Reason)
//	}
//
// The key names the tables and the process: lowercase letters, digits and
// dashes, starting with a letter. A file that does not parse, a key already
// used, or a rule set from which nothing translates is [IsInvalid]; the
// message names the file and line.
func (c *Client) ImportGruleRules(ctx context.Context, projectID, key string, files []RuleFile, opts ...GruleImportOption) (*GruleImport, error) {
	if projectID == "" || key == "" {
		return nil, errors.New("metis: ImportGruleRules needs a project ID and a key to name the tables by")
	}
	if len(files) == 0 {
		return nil, errors.New("metis: ImportGruleRules needs at least one rule file")
	}
	req := gruleImportRequest{ProjectID: projectID, Key: key, Files: files}
	for _, opt := range opts {
		opt(&req)
	}
	var out struct {
		Import GruleImport `json:"import"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/decisions/import/grule", req, &out); err != nil {
		return nil, err
	}
	return &out.Import, nil
}
