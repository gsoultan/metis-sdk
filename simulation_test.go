package metis

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The server refuses fields it does not know, and an older one does not know
// definition. Sending only what was set keeps a plain run working against it.
func TestASimulationSendsOnlyWhatWasSet(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/simulations", 200, `{"run":{"run_id":"r-1","outcome":"completed","ended_at_node":"done","variables":{}}}`)

	run, err := client.Simulate(t.Context(), "p-1", SimulationRequest{DefinitionKey: "expense"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != SimulationOutcomeCompleted || run.EndNode() != "done" {
		t.Errorf("run = %+v", run)
	}
	body := f.last().Body
	if !reflect.DeepEqual(body, map[string]any{"project_id": "p-1", "definition_key": "expense"}) {
		t.Errorf("body = %#v", body)
	}
}

func TestASimulationSendsEverythingItIsGiven(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/simulations", 200, `{"run":{"outcome":"needs_answer","awaiting_node":"approve"}}`)

	clock := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	run, err := client.Simulate(t.Context(), "p-1", SimulationRequest{
		BPMNXML:          []byte("<definitions/>"),
		ReplayInstanceID: "i-1",
		Variables:        Variables{"amount": 4200},
		Answers:          []SimulationAnswer{{Node: "approve", Kind: AnswerPerson, Actor: "marc", After: "PT2H"}},
		ClockStart:       clock,
		Seed:             7,
		MaxSteps:         50,
		Version:          2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.EndNode() != "approve" {
		t.Errorf("EndNode = %q, want where it stopped to ask", run.EndNode())
	}
	body := f.last().Body
	for field, want := range map[string]any{
		"bpmn_xml": "<definitions/>", "replay_instance_id": "i-1", "seed": float64(7),
		"max_steps": float64(50), "version": float64(2), "clock_start": "2026-10-22T09:00:00Z",
	} {
		if body[field] != want {
			t.Errorf("%s = %#v, want %#v", field, body[field], want)
		}
	}
	answers, _ := body["answers"].([]any)
	if len(answers) != 1 || answers[0].(map[string]any)["actor"] != "marc" {
		t.Errorf("answers = %#v", body["answers"])
	}
}

func TestADraftDefinitionGoesAsJSON(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/simulations", 200, `{"run":{"outcome":"completed","definition":{"draft":true}}}`)

	run, err := client.Simulate(t.Context(), "p-1", SimulationRequest{Definition: json.RawMessage(`{"key":"expense"}`)})
	if err != nil || !run.Definition.Draft {
		t.Fatalf("Simulate = %+v, %v", run, err)
	}
	if draft, _ := f.last().Body["definition"].(map[string]any); draft["key"] != "expense" {
		t.Errorf("definition = %#v", f.last().Body["definition"])
	}
}

func TestASimulationThatCannotRunIsNotSent(t *testing.T) {
	f, client := newFakeServer(t)
	for name, req := range map[string]SimulationRequest{
		"nothing to run": {Variables: Variables{"a": 1}},
		"two drafts":     {Definition: json.RawMessage(`{}`), BPMNXML: []byte("<x/>")},
	} {
		if _, err := client.Simulate(t.Context(), "p-1", req); err == nil {
			t.Errorf("%s: sent", name)
		}
		if _, err := client.SimulateBatch(t.Context(), "p-1", []SimulationRequest{req}); err == nil {
			t.Errorf("%s: sent in a batch", name)
		}
	}
	if _, err := client.SimulateBatch(t.Context(), "p-1", make([]SimulationRequest, MaxSimulationBatch+1)); err == nil {
		t.Error("a batch over the limit was sent")
	}
	if f.count() != 0 {
		t.Error("a simulation the SDK refused reached the server")
	}
}

func TestASimulationBatchKeepsItsOrder(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/simulations/batch", 200, `{"runs":[{"run_id":"r-1"},{"run_id":"r-2"}]}`)

	runs, err := client.SimulateBatch(t.Context(), "p-1", []SimulationRequest{
		{DefinitionKey: "a"}, {DefinitionKey: "b"},
	})
	if err != nil || len(runs) != 2 || runs[1].RunID != "r-2" {
		t.Fatalf("SimulateBatch = %+v, %v", runs, err)
	}
	scenarios, _ := f.last().Body["scenarios"].([]any)
	if len(scenarios) != 2 || scenarios[1].(map[string]any)["definition_key"] != "b" {
		t.Errorf("scenarios = %#v", f.last().Body["scenarios"])
	}
}

func TestASimulationBatchThatLosesARunSaysSo(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/simulations/batch", 200, `{"runs":[{"run_id":"r-1"}]}`)

	if _, err := client.SimulateBatch(t.Context(), "p-1", []SimulationRequest{{DefinitionKey: "a"}, {DefinitionKey: "b"}}); err == nil {
		t.Error("two runs asked for and one returned was taken as an answer")
	}
}

func TestAReplaySaysWhereItWentApart(t *testing.T) {
	var run SimulationRun
	if err := json.Unmarshal([]byte(`{"outcome":"completed","virtual_duration_ms":7200000,
		"replay":{"instance_id":"i-1","version":2,"status":"completed","started_with":{"amount":4200},
		"path":["start","approve","done"],"only_in_run":["cfo"],"only_in_instance":[]}}`), &run); err != nil {
		t.Fatal(err)
	}
	if run.VirtualDuration() != 2*time.Hour {
		t.Errorf("VirtualDuration = %v", run.VirtualDuration())
	}
	if run.Replay.SamePath() {
		t.Error("a run that reached a step the instance never did took the same path")
	}
	if !(SimulationReplay{}).SamePath() {
		t.Error("nothing apart is the same path")
	}
}

func TestAnExpectationSaysWhatDiffers(t *testing.T) {
	run := SimulationRun{
		Outcome:     SimulationOutcomeCompleted,
		EndedAtNode: "rejected",
		Variables:   Variables{"amount": 4200.0, "approver": "marc", "lines": []any{1.0, 2.0}},
	}

	passed := SimulationExpectation{
		Outcome:   SimulationOutcomeCompleted,
		EndsAt:    "rejected",
		Variables: Variables{"amount": 4200, "lines": []int{1, 2}},
	}
	if misses := passed.Check(run); len(misses) != 0 {
		t.Errorf("4200 and 4200.0 are one number, and [1,2] one list: %q", misses)
	}

	failed := SimulationExpectation{
		Outcome:   SimulationOutcomeNeedsAnswer,
		EndsAt:    "approved",
		Variables: Variables{"approver": "anne", "reason": "late"},
	}
	want := []string{
		"It was expected to stop to ask for an answer, and did not.",
		"It ended at rejected, not approved.",
		`approver is "marc", not "anne".`,
		`reason was expected to be "late", and was never set.`,
	}
	if got := failed.Check(run); !reflect.DeepEqual(got, want) {
		t.Errorf("misses =\n%q\nwant\n%q", got, want)
	}

	stuck := SimulationRun{Outcome: SimulationOutcomeIncident}
	got := SimulationExpectation{Outcome: SimulationOutcomeIncident, EndsAt: "approved"}.Check(stuck)
	if len(got) != 1 || !strings.Contains(got[0], "never got there") {
		t.Errorf("misses = %q", got)
	}
	if !strings.Contains(SimulationExpectation{Outcome: SimulationOutcomeIncident}.Check(run)[0], "raise an incident") {
		t.Error("an incident expected is not said in words")
	}
	if !strings.Contains(outcomeWords(SimulationOutcomeTimeout), "timeout") {
		t.Error("an outcome with no words is not named")
	}
}

func TestSavingSimulationCases(t *testing.T) {
	f, client := newFakeServer(t)
	saved := `{"case":{"id":"c-1","project_id":"p-1","process_key":"expense","name":"big expense",
		"scenario":{"variables":{},"answers":[],"seed":0},"expect":{"outcome":"completed"}}}`
	f.respond("POST /api/v1/projects/p-1/simulation-cases/expense", 200, saved)
	f.respond("PUT /api/v1/projects/p-1/simulation-cases/expense/c-1", 200, saved)
	f.respond("DELETE /api/v1/projects/p-1/simulation-cases/expense/c-1", 200, `{}`)

	created, err := client.CreateSimulationCase(t.Context(), "p-1", "expense", SimulationCase{
		Name: "big expense", Expect: SimulationExpectation{Outcome: SimulationOutcomeCompleted},
	})
	if err != nil || created.ID != "c-1" {
		t.Fatalf("CreateSimulationCase = %+v, %v", created, err)
	}
	body := f.last().Body
	// The server refuses a body naming the case's project, process or ID, and
	// a scenario with null where a list or an object belongs.
	if !reflect.DeepEqual(keys(body), []string{"expect", "name", "scenario"}) {
		t.Errorf("body = %#v", body)
	}
	scenario, _ := body["scenario"].(map[string]any)
	if _, ok := scenario["variables"].(map[string]any); !ok {
		t.Errorf("variables = %#v", scenario["variables"])
	}
	if _, ok := scenario["answers"].([]any); !ok {
		t.Errorf("answers = %#v", scenario["answers"])
	}

	if _, err := client.UpdateSimulationCase(t.Context(), "p-1", "expense", *created); err != nil {
		t.Fatal(err)
	}
	if last := f.last(); last.Method != http.MethodPut {
		t.Errorf("updated with %s", last.Method)
	}
	if _, err := client.UpdateSimulationCase(t.Context(), "p-1", "expense", SimulationCase{Name: "no id"}); err == nil {
		t.Error("a case with no ID was updated")
	}
	if err := client.DeleteSimulationCase(t.Context(), "p-1", "expense", "c-1"); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]any) []string { return sortedKeys(Variables(m)) }

func TestRunningEverySavedCase(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/projects/p-1/simulation-cases/expense", 200, `{"cases":[
		{"id":"c-1","name":"approved","scenario":{"variables":{"amount":10},"answers":[],"seed":3},
		 "expect":{"outcome":"completed","ends_at":"approved"}},
		{"id":"c-2","name":"no expectation","scenario":{"variables":{},"answers":[],"seed":0},"expect":{}},
		{"id":"c-3","name":"rejected","scenario":{"variables":{"amount":9000},"answers":[],"seed":0},
		 "expect":{"ends_at":"rejected"}}]}`)
	f.respond("POST /api/v1/simulations/batch", 200, `{"runs":[
		{"outcome":"completed","ended_at_node":"approved","variables":{}},
		{"outcome":"completed","ended_at_node":"approved","variables":{},"incidents":[]},
		{"outcome":"completed","ended_at_node":"approved","variables":{}}]}`)

	results, err := client.RunSimulationCases(t.Context(), "p-1", "expense", SimulationRequest{Version: 4, ReplayInstanceID: "i-1"})
	if err != nil {
		t.Fatal(err)
	}
	passed := []bool{results[0].Passed(), results[1].Passed(), results[2].Passed()}
	if !reflect.DeepEqual(passed, []bool{true, true, false}) {
		t.Errorf("passed = %v", passed)
	}
	if len(results[2].Misses) != 1 {
		t.Errorf("misses = %q", results[2].Misses)
	}

	scenarios, _ := f.last().Body["scenarios"].([]any)
	first, _ := scenarios[0].(map[string]any)
	if first["definition_key"] != "expense" || first["version"] != float64(4) || first["seed"] != float64(3) {
		t.Errorf("first case was sent as %#v", first)
	}
	if _, sent := first["replay_instance_id"]; sent {
		t.Error("a saved case was run as a replay")
	}
}

func TestRunningCasesOfAProcessWithNone(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/projects/p-1/simulation-cases/expense", 200, `{"cases":[]}`)

	results, err := client.RunSimulationCases(t.Context(), "p-1", "expense", SimulationRequest{})
	if err != nil || len(results) != 0 {
		t.Errorf("RunSimulationCases = %+v, %v", results, err)
	}
	if f.count() != 1 {
		t.Error("an empty suite was run")
	}
}
