package metis

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

const loanCases = `{"cases":[
	{"id":"c-1","name":"approved","scenario":{"variables":{"amount":10},"answers":[],"seed":1},"expect":{"outcome":"completed"}},
	{"id":"c-2","name":"rejected","scenario":{"variables":{"amount":9000},"answers":[],"seed":2},"expect":{"ends_at":"rejected"}}]}`

const loanVersions = `{"versions":[
	{"id":"d-3","key":"loan","version":3,"live":true,"running_instances":0},
	{"id":"d-2","key":"loan","version":2,"running_instances":7},
	{"id":"d-1","key":"loan","version":1,"running_instances":0}]}`

const loanPlan = `{"plan":{"source_key":"loan","source_version":2,"target_version":3,"target_id":"d-3","instances":7,
	"moves":[{"from":"Review","to":"Review2","tokens":4,"tasks":4,"jobs":0,"tasks_claimed":1,"mapped":true}],
	"warnings":["two tasks are claimed"],
	"compliance_holds":[{"node_id":"Checker","name":"Four eyes","note":"skipped","instances":2}],
	"actions":[{"node_id":"Old","name":"Old step","kind":"skip","reason":"gone"}],
	"removed_nodes":["Old"],"requires_second_approver":true,"second_approver_reasons":["holds"]},
	"applied":false,"passed_over":[],"passed_over_in_all":0}`

// releaseServer answers every route a release reads, with runs as the
// simulation results; override replaces the answer of a route.
func releaseServer(f *fakeServer, runs string, override map[string]reply) {
	routes := map[string]reply{
		"GET /api/v1/projects/p1/simulation-cases/loan": ok(loanCases),
		"POST /api/v1/simulations/batch":                ok(runs),
		"GET /api/v1/definitions/live-versions":         ok(`{"live":{"loan":2}}`),
		"POST /api/v1/definitions/import":               ok(`{"id":"d-3"}`),
		"GET /api/v1/definitions/versions":              ok(loanVersions),
		"POST /api/v1/definitions/versions/migrate":     ok(loanPlan),
	}
	maps.Copy(routes, override)
	for pattern, r := range routes {
		f.respond(pattern, r.status, r.body)
	}
}

const passingRuns = `{"runs":[
	{"outcome":"completed","ended_at_node":"approved","variables":{}},
	{"outcome":"completed","ended_at_node":"rejected","variables":{}}]}`

func TestReleaseVersionDeploysAfterTheCasesPass(t *testing.T) {
	f, client := newFakeServer(t)
	releaseServer(f, passingRuns, nil)

	report, err := client.ReleaseVersion(t.Context(), "p1", "loan", []byte(approvalXML), ReleaseOptions{PlanMigrations: true, NodeMapping: map[string]string{"Review": "Review2"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Released == nil || report.Released.Version != 3 || len(report.Cases) != 2 || len(report.Failed()) != 0 {
		t.Errorf("report = %+v", report)
	}
	if report.PreviousLive != 2 || len(report.Draining) != 1 || report.Draining[0].Version != 2 {
		t.Errorf("previous = %d, draining = %+v; want v2 still running 7", report.PreviousLive, report.Draining)
	}
	if len(report.Migrations) != 1 {
		t.Fatalf("migrations = %+v, want one plan for v2", report.Migrations)
	}

	plan := report.Migrations[0]
	if plan.Source.Version != 2 || plan.Target.ID != "d-3" || plan.Target.Key != "loan" || plan.Instances != 7 || !plan.CanApply() {
		t.Errorf("plan = %+v", plan)
	}
	if m := plan.Moves[0]; m.From.ID != "Review" || m.To.ID != "Review2" || m.TasksClaimed != 1 || !m.Mapped {
		t.Errorf("move = %+v", m)
	}
	if plan.ComplianceHolds[0].Step.Name != "Four eyes" || plan.Actions[0].Step.ID != "Old" || plan.Removed[0].ID != "Old" || !plan.RequiresSecondApprover {
		t.Errorf("plan details = %+v", plan)
	}

	var migrate recordedRequest
	for _, r := range f.snapshot() {
		if r.Path == "/api/v1/definitions/versions/migrate" {
			migrate = r
		}
	}
	if migrate.Body["dry_run"] != true || migrate.Body["source_definition_id"] != "d-2" || migrate.Body["target_definition_id"] != "d-3" {
		t.Errorf("migrate body = %v; want a dry run from d-2 to d-3", migrate.Body)
	}
	if mapping, _ := migrate.Body["node_mapping"].(map[string]any); mapping["Review"] != "Review2" {
		t.Errorf("node_mapping = %v", migrate.Body["node_mapping"])
	}
}

func TestReleaseVersionIsBlockedByAFailingCase(t *testing.T) {
	f, client := newFakeServer(t)
	releaseServer(f, `{"runs":[
		{"outcome":"completed","ended_at_node":"approved","variables":{}},
		{"outcome":"completed","ended_at_node":"approved","variables":{}}]}`, nil)

	report, err := client.ReleaseVersion(t.Context(), "p1", "loan", []byte(approvalXML), ReleaseOptions{})
	if !errors.Is(err, ErrReleaseBlocked) {
		t.Fatalf("err = %v, want ErrReleaseBlocked", err)
	}
	if failed := report.Failed(); len(failed) != 1 || failed[0].Case.Name != "rejected" || report.Released != nil {
		t.Errorf("report = %+v; want the rejected case named and nothing released", report)
	}
	if n := sent(f, "POST", "/api/v1/definitions/import"); n != 0 {
		t.Errorf("imports = %d, want none", n)
	}
	if body, _ := f.snapshot()[1].Body["scenarios"].([]any); len(body) != 2 {
		t.Errorf("scenarios = %v", f.snapshot()[1].Body)
	} else if first, _ := body[0].(map[string]any); !strings.Contains(first["bpmn_xml"].(string), `id="loan"`) {
		t.Errorf("the cases did not run on the draft: %v", first)
	}
}

func TestReleaseVersionWithoutCases(t *testing.T) {
	f, client := newFakeServer(t)
	releaseServer(f, passingRuns, map[string]reply{"GET /api/v1/projects/p1/simulation-cases/loan": ok(`{"cases":[]}`)})

	if _, err := client.ReleaseVersion(t.Context(), "p1", "loan", []byte(approvalXML), ReleaseOptions{RequireCases: true}); !errors.Is(err, ErrReleaseBlocked) {
		t.Errorf("err = %v, want ErrReleaseBlocked when cases are required", err)
	}
	report, err := client.ReleaseVersion(t.Context(), "p1", "loan", []byte(approvalXML), ReleaseOptions{})
	if err != nil || report.Released == nil || report.Migrations != nil {
		t.Errorf("report = %+v, %v; want released untried, no plans unasked", report, err)
	}
}

func TestReleaseVersionRefusesBeforeDeploying(t *testing.T) {
	f, client := newFakeServer(t)
	releaseServer(f, passingRuns, map[string]reply{"GET /api/v1/projects/p1/simulation-cases/loan": {404, `{"error":"no such route"}`}})

	if _, err := client.ReleaseVersion(t.Context(), "p1", "expense", []byte(approvalXML), ReleaseOptions{}); err == nil || !strings.Contains(err.Error(), "expense") {
		t.Errorf("err = %v, want the missing process named", err)
	}
	if _, err := client.ReleaseVersion(t.Context(), "p1", "loan", []byte("<not bpmn"), ReleaseOptions{}); err == nil {
		t.Error("a broken diagram was released")
	}
	if _, err := client.ReleaseVersion(t.Context(), "", "loan", nil, ReleaseOptions{}); err == nil {
		t.Error("a release without a project was accepted")
	}
	if n := f.count(); n != 0 {
		t.Errorf("requests = %d, want none", n)
	}

	if _, err := client.ReleaseVersion(t.Context(), "p1", "loan", []byte(approvalXML), ReleaseOptions{}); !IsNotFound(err) {
		t.Errorf("err = %v, want 404 from an older server", err)
	}
}

func TestReleaseVersionAfterTheDeploy(t *testing.T) {
	f, client := newFakeServer(t)
	releaseServer(f, passingRuns, map[string]reply{"POST /api/v1/definitions/versions/migrate": {403, `{"error":"administrators only"}`}})
	report, err := client.ReleaseVersion(t.Context(), "p1", "loan", []byte(approvalXML), ReleaseOptions{PlanMigrations: true})
	if !IsUnauthorized(err) || !strings.Contains(err.Error(), "deployed as d-3") || report.Released == nil {
		t.Errorf("err = %v, want the 403 saying the version was deployed", err)
	}

	f2, client2 := newFakeServer(t)
	releaseServer(f2, passingRuns, map[string]reply{"POST /api/v1/definitions/import": {200, `{"id":"d-elsewhere"}`}})
	if _, err := client2.ReleaseVersion(t.Context(), "p1", "loan", []byte(approvalXML), ReleaseOptions{}); err == nil || !strings.Contains(err.Error(), "d-elsewhere") {
		t.Errorf("err = %v, want the stray version named", err)
	}

	f3, client3 := newFakeServer(t)
	releaseServer(f3, passingRuns, map[string]reply{"GET /api/v1/definitions/versions": {500, `{"error":"down"}`}})
	if _, err := client3.ReleaseVersion(t.Context(), "p1", "loan", []byte(approvalXML), ReleaseOptions{}); !IsServerError(err) || !strings.Contains(err.Error(), "deployed") {
		t.Errorf("err = %v", err)
	}

	f4, client4 := newFakeServer(t)
	releaseServer(f4, passingRuns, map[string]reply{"POST /api/v1/definitions/import": {400, `{"error":"invalid definition"}`}})
	if _, err := client4.ReleaseVersion(t.Context(), "p1", "loan", []byte(approvalXML), ReleaseOptions{}); !IsInvalid(err) {
		t.Errorf("err = %v, want 400", err)
	}

	f5, client5 := newFakeServer(t)
	releaseServer(f5, passingRuns, map[string]reply{"GET /api/v1/definitions/live-versions": {403, `{"error":"no"}`}})
	if _, err := client5.ReleaseVersion(t.Context(), "p1", "loan", []byte(approvalXML), ReleaseOptions{}); !IsUnauthorized(err) {
		t.Errorf("err = %v, want 403", err)
	}
}

func TestPlanMigrationNeedsBothVersions(t *testing.T) {
	f, client := newFakeServer(t)
	if _, err := client.PlanMigration(t.Context(), MigrationRequest{Source: "d-1"}); err == nil {
		t.Error("a plan without a target was asked for")
	}
	f.respond("POST /api/v1/definitions/versions/migrate", 200, loanPlan)
	plan, err := client.PlanMigration(t.Context(), MigrationRequest{Source: "d-2", Target: "d-3", Instances: []string{"i1"}})
	if err != nil || plan.Instances != 7 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if got := f.last().Body["instances"]; len(got.([]any)) != 1 {
		t.Errorf("instances = %v", got)
	}
	if _, sent := f.last().Body["node_mapping"]; sent {
		t.Error("an empty mapping was sent")
	}
	var refused MigrationPlan
	mustDecode(t, `{"refusals":["no"]}`, &refused)
	if refused.CanApply() {
		t.Error("a refused plan can apply")
	}
	if err := refused.UnmarshalJSON([]byte(`[`)); err == nil {
		t.Error("broken JSON decoded")
	}
}
