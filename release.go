package metis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// --- Planning a migration ---------------------------------------------------

// MigrationRequest asks what moving running instances from one version of a
// process to another would do.
type MigrationRequest struct {
	// Source is the version the instances run on now, Target the one they
	// would move to. Both are definition IDs of one process.
	Source, Target string
	// NodeMapping sends work on a step of Source to a differently named step
	// of Target. A step left out is carried across under the same ID.
	NodeMapping map[string]string
	// Instances narrows the plan to these instances. Empty means every
	// instance on Source.
	Instances []string
}

// MigrationPlan is what a migration would do: where the work sits and where
// it would land, and what stands in the way.
type MigrationPlan struct {
	// Source and Target are the two versions. Each carries its ID, key and
	// version number; nothing else is sent.
	Source, Target Definition
	// Instances is how many instances of Source the plan covers.
	Instances int
	// Moves are each step with work on it, and where that work would land.
	Moves []MigrationMove
	// Refusals are why the plan would not be applied. Empty when it would.
	Refusals []string
	// Warnings are what somebody should read first. They do not block.
	Warnings []string
	// ComplianceHolds are controls the move would take from instances that
	// have not passed them yet.
	ComplianceHolds []ComplianceHold
	// Actions are the steps whose work would be decided, skipped or
	// cancelled, rather than moved.
	Actions []MigrationAction
	// Removed are the steps Target no longer has.
	Removed []StepRef
	// RequiresSecondApprover says an apply would wait for a second
	// administrator, for the reasons given.
	RequiresSecondApprover bool
	SecondApproverReasons  []string
}

// CanApply reports whether nothing refuses the plan.
func (p MigrationPlan) CanApply() bool { return len(p.Refusals) == 0 }

// MigrationMove is one step's work in a plan.
type MigrationMove struct {
	From, To StepRef
	// Tokens, Tasks and Jobs are the work on From: paths, open tasks and
	// scheduled jobs. TasksClaimed and TasksDelegated are among Tasks;
	// Events counts waiting event subscriptions.
	Tokens, Tasks, Jobs          int
	TasksClaimed, TasksDelegated int
	Events                       int
	// Mapped is true when NodeMapping sent the work to a step of another ID.
	Mapped bool
}

// ComplianceHold is a control step a migration would take from instances
// that have not passed it.
type ComplianceHold struct {
	Step      StepRef
	Note      string
	Instances int
}

// MigrationAction is a step whose work a migration decides rather than
// moves.
type MigrationAction struct {
	Step   StepRef
	Kind   string
	Reason string
}

// UnmarshalJSON reads the server's plan, whose steps and versions are bare
// IDs and numbers.
func (p *MigrationPlan) UnmarshalJSON(data []byte) error {
	var wire struct {
		SourceKey       string   `json:"source_key"`
		SourceVersion   int      `json:"source_version"`
		TargetVersion   int      `json:"target_version"`
		TargetID        string   `json:"target_id"`
		Instances       int      `json:"instances"`
		Refusals        []string `json:"refusals"`
		Warnings        []string `json:"warnings"`
		RemovedNodes    []string `json:"removed_nodes"`
		SecondApprover  bool     `json:"requires_second_approver"`
		ApproverReasons []string `json:"second_approver_reasons"`
		Moves           []struct {
			From, To            string
			Tokens, Tasks, Jobs int
			TasksClaimed        int `json:"tasks_claimed"`
			TasksDelegated      int `json:"tasks_delegated"`
			Events              int
			Mapped              bool
		} `json:"moves"`
		ComplianceHolds []struct {
			NodeID    string `json:"node_id"`
			Name      string `json:"name"`
			Note      string `json:"note"`
			Instances int    `json:"instances"`
		} `json:"compliance_holds"`
		Actions []struct {
			NodeID string `json:"node_id"`
			Name   string `json:"name"`
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*p = MigrationPlan{
		Source:                 Definition{Key: wire.SourceKey, Version: wire.SourceVersion},
		Target:                 Definition{ID: wire.TargetID, Key: wire.SourceKey, Version: wire.TargetVersion},
		Instances:              wire.Instances,
		Refusals:               wire.Refusals,
		Warnings:               wire.Warnings,
		RequiresSecondApprover: wire.SecondApprover,
		SecondApproverReasons:  wire.ApproverReasons,
	}
	for _, m := range wire.Moves {
		p.Moves = append(p.Moves, MigrationMove{
			From: StepRef{ID: m.From}, To: StepRef{ID: m.To},
			Tokens: m.Tokens, Tasks: m.Tasks, Jobs: m.Jobs,
			TasksClaimed: m.TasksClaimed, TasksDelegated: m.TasksDelegated,
			Events: m.Events, Mapped: m.Mapped,
		})
	}
	for _, h := range wire.ComplianceHolds {
		p.ComplianceHolds = append(p.ComplianceHolds, ComplianceHold{Step: StepRef{ID: h.NodeID, Name: h.Name}, Note: h.Note, Instances: h.Instances})
	}
	for _, a := range wire.Actions {
		p.Actions = append(p.Actions, MigrationAction{Step: StepRef{ID: a.NodeID, Name: a.Name}, Kind: a.Kind, Reason: a.Reason})
	}
	for _, id := range wire.RemovedNodes {
		p.Removed = append(p.Removed, StepRef{ID: id})
	}
	return nil
}

// PlanMigration asks the server what moving running instances from one
// version to another would do, and changes nothing: it is always a dry run.
//
// Applying a migration rewrites cases that are running — somebody's order,
// somebody's claim — and the server can require acknowledgements and a second
// administrator for it, so this client only plans. Apply a plan from the
// designer, where those are given. An administrator's; it needs **Metis
// v0.3.0** or later.
func (c *Client) PlanMigration(ctx context.Context, req MigrationRequest) (*MigrationPlan, error) {
	if req.Source == "" || req.Target == "" {
		return nil, errors.New("metis: plan migration: both the source and the target version are needed")
	}
	body := map[string]any{
		"source_definition_id": req.Source,
		"target_definition_id": req.Target,
		// Always said: the server applies only on an explicit false.
		"dry_run": true,
	}
	if len(req.NodeMapping) > 0 {
		body["node_mapping"] = req.NodeMapping
	}
	if len(req.Instances) > 0 {
		body["instances"] = req.Instances
	}
	var out struct {
		Plan MigrationPlan `json:"plan"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/definitions/versions/migrate", body, &out); err != nil {
		return nil, err
	}
	return &out.Plan, nil
}

// --- Releasing a version ----------------------------------------------------

// ErrReleaseBlocked means a release was stopped before anything was deployed:
// a saved simulation case failed on the new diagram, or there were none and
// they were required.
var ErrReleaseBlocked = errors.New("metis: release blocked; nothing was deployed")

// ReleaseOptions bounds [Client.ReleaseVersion].
type ReleaseOptions struct {
	// RequireCases refuses a release of a process with no saved simulation
	// case. Without it such a release goes ahead untried.
	RequireCases bool
	// PlanMigrations asks, for every older version still running instances,
	// what moving them to the new version would do. Nothing is moved.
	PlanMigrations bool
	// NodeMapping is used for those plans; see [MigrationRequest].
	NodeMapping map[string]string
}

// ReleaseReport is what a release did and what it leaves to decide.
type ReleaseReport struct {
	// Cases are the saved simulation cases, run on the new diagram before it
	// was deployed.
	Cases []SimulationCaseResult
	// Released is the version deployed, nil when the release was blocked.
	Released *VersionStatus
	// PreviousLive is the version new instances started on before, zero when
	// the process had none.
	PreviousLive int
	// Draining are the older versions that still run instances. They finish
	// on the version they started on unless migrated.
	Draining []VersionStatus
	// Migrations are the plans asked for with PlanMigrations, one per
	// draining version, in the same order.
	Migrations []MigrationPlan
}

// Failed returns the cases that did not pass.
func (r ReleaseReport) Failed() []SimulationCaseResult {
	var failed []SimulationCaseResult
	for _, c := range r.Cases {
		if !c.Passed() {
			failed = append(failed, c)
		}
	}
	return failed
}

// ReleaseVersion deploys a new version of a process only after its saved
// simulation cases pass on it, and reports what that leaves running:
//
//	report, err := client.ReleaseVersion(ctx, projectID, "expense", bpmn,
//		metis.ReleaseOptions{RequireCases: true, PlanMigrations: true})
//	if errors.Is(err, metis.ErrReleaseBlocked) {
//		for _, r := range report.Failed() {
//			log.Printf("%s: %s", r.Case.Name, strings.Join(r.Misses, "; "))
//		}
//	}
//
// In order, it:
//   - checks that the diagram holds the process processKey, before anything
//     else;
//   - runs every saved case of the process on the diagram as a draft, and
//     stops with [ErrReleaseBlocked] if any fails;
//   - deploys the diagram, which makes it live for new instances;
//   - reads the versions back for the report, and plans migrations if asked.
//
// Running instances are never moved. An error after the deploy says so and
// comes with the report as far as it got. Trying a draft needs a server newer
// than **Metis v0.5.0**.
func (c *Client) ReleaseVersion(ctx context.Context, projectID, processKey string, bpmnXML []byte, opts ReleaseOptions) (*ReleaseReport, error) {
	if projectID == "" || processKey == "" {
		return nil, errors.New("metis: release version: a project and a process key are needed")
	}
	flows, err := ParseFlow(bpmnXML)
	if err != nil {
		return nil, err
	}
	if !holdsProcess(flows, processKey) {
		return nil, fmt.Errorf("metis: release version: the diagram holds no process %q", processKey)
	}

	report := &ReleaseReport{}
	report.Cases, err = c.RunSimulationCases(ctx, projectID, processKey, SimulationRequest{BPMNXML: bpmnXML})
	if err != nil {
		return report, fmt.Errorf("metis: release version: run the saved cases: %w", err)
	}
	if failed := report.Failed(); len(failed) > 0 {
		return report, fmt.Errorf("%w: %d of %d saved cases failed on the new diagram", ErrReleaseBlocked, len(failed), len(report.Cases))
	}
	if len(report.Cases) == 0 && opts.RequireCases {
		return report, fmt.Errorf("%w: process %q has no saved simulation case", ErrReleaseBlocked, processKey)
	}

	live, err := c.ListLiveVersions(ctx, projectID)
	if err != nil {
		return report, err
	}
	report.PreviousLive = live[processKey]

	id, err := c.ImportDefinition(ctx, projectID, bpmnXML)
	if err != nil {
		return report, err
	}
	deployed := func(err error) error {
		return fmt.Errorf("metis: release version: deployed as %s, then: %w", id, err)
	}

	versions, err := c.ListVersions(ctx, projectID, processKey)
	if err != nil {
		return report, deployed(err)
	}
	for _, v := range versions {
		switch {
		case v.ID == id:
			released := v
			report.Released = &released
		case v.RunningInstances > 0:
			report.Draining = append(report.Draining, v)
		}
	}
	if report.Released == nil {
		return report, deployed(fmt.Errorf("version %s is not among the versions of %q", id, processKey))
	}

	if opts.PlanMigrations {
		for _, v := range report.Draining {
			plan, err := c.PlanMigration(ctx, MigrationRequest{Source: v.ID, Target: id, NodeMapping: opts.NodeMapping})
			if err != nil {
				return report, deployed(fmt.Errorf("plan the migration of version %d: %w", v.Version, err))
			}
			report.Migrations = append(report.Migrations, *plan)
		}
	}
	return report, nil
}

func holdsProcess(flows []*Flow, key string) bool {
	for _, f := range flows {
		if f.ID == key {
			return true
		}
	}
	return false
}
