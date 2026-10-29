package metis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// A simulation runs a process on the real engine against a virtual clock,
// inside a transaction that is always rolled back: nothing is kept, nothing
// is sent, no service is called. Steps the outside world would answer —
// people, services, messages — are answered by the request instead.
//
// Simulating is a designer's. Saved cases, drafts and replays need a server
// newer than **Metis v0.5.0**: an older one has no case routes (404,
// [IsNotFound]) and refuses a draft or a replay with a 400 ([IsInvalid]).

// SimulationOutcome is how a simulated run ended.
type SimulationOutcome string

const (
	// SimulationOutcomeCompleted reached an end event.
	SimulationOutcomeCompleted SimulationOutcome = "completed"
	// SimulationOutcomeNeedsAnswer stopped at a step nothing in the request answered
	// — [SimulationRun.AwaitingNode]. Not an error: it is the process asking,
	// and how a case is built up one answer at a time.
	SimulationOutcomeNeedsAnswer SimulationOutcome = "needs_answer"
	// SimulationOutcomeIncident is the engine refusing: a gateway that matched
	// nothing, a missing default flow. Usually what the simulation was for.
	SimulationOutcomeIncident SimulationOutcome = "incident"
	// SimulationOutcomeStepBudget hit [SimulationRequest.MaxSteps], which in practice
	// means a loop nobody intended.
	SimulationOutcomeStepBudget SimulationOutcome = "step_budget"
	// SimulationOutcomeTimeout took longer than a simulation is allowed to.
	SimulationOutcomeTimeout SimulationOutcome = "timeout"
)

// SimulationAnswerKind names what a step is answered for.
type SimulationAnswerKind string

const (
	// AnswerPerson answers a user or manual task: who did it, how long they
	// took, and what they entered.
	AnswerPerson SimulationAnswerKind = "person"
	// AnswerService answers a service or send task: what it returned, or how
	// it failed.
	AnswerService SimulationAnswerKind = "service"
	// AnswerMessage answers a waiting catch event: when the message arrived.
	AnswerMessage SimulationAnswerKind = "message"
)

// SimulationAnswer is what the outside world says when the process asks, by
// the step it asks at. A step is answered once.
type SimulationAnswer struct {
	// Node is the step's BPMN element ID.
	Node string               `json:"node"`
	Kind SimulationAnswerKind `json:"kind"`

	// Actor and After answer a person's task: who, and how long after it was
	// reached, as an ISO-8601 duration such as "PT24H".
	Actor string `json:"actor,omitempty"`
	After string `json:"after,omitempty"`

	// Returns is what a service hands back, or what a person entered on
	// their task, merged into the variables as a completion would be.
	Returns Variables `json:"returns,omitempty"`
	// Fails is an error code instead of a result, to exercise a boundary
	// error event.
	Fails string `json:"fails,omitempty"`

	// ArrivesAfter answers a message wait, as an ISO-8601 duration.
	ArrivesAfter string `json:"arrives_after,omitempty"`
}

// SimulationRequest is one run: what runs, what it starts with, and how it is
// answered.
//
// What runs is one of: a deployed process, by DefinitionKey and Version; a
// draft, by Definition or BPMNXML; or, with ReplayInstanceID, a real instance's
// case on whatever the first two name, or on its own process as it is live.
type SimulationRequest struct {
	// DefinitionKey is the process, [Definition.Key]; Version pins one of its
	// deployed versions, and zero is the live one.
	DefinitionKey string
	Version       int

	// Definition and BPMNXML run a model that has not been deployed, checked
	// as a deploy checks it and never saved. At most one may be set:
	// Definition is the JSON body POST /api/v1/definitions takes, BPMNXML a
	// .bpmn file.
	Definition json.RawMessage
	BPMNXML    []byte

	// ReplayInstanceID runs a real instance's case: what it was started with,
	// who did its steps and when, what its services answered. Variables and
	// Answers given here win over what was read. The run says where it went
	// somewhere the instance did not in [SimulationRun.Replay]. Nothing about
	// the instance changes.
	ReplayInstanceID string

	Variables Variables
	Answers   []SimulationAnswer

	// ClockStart is where virtual time begins. Zero is now; set it when you
	// assert on a trace, which otherwise moves with the wall clock.
	ClockStart time.Time
	// Seed fixes every estimate: the same seed, version and answers give the
	// same trace.
	Seed int64
	// MaxSteps stops a runaway loop. Zero is the server's 500; it allows at
	// most 5,000.
	MaxSteps int
}

// body is the request as the server reads it, for one project. Only what is
// set is sent: the server refuses fields it does not know, so a field an older
// server lacks must not go out unless the caller asked for it.
func (r SimulationRequest) body(projectID string) map[string]any {
	body := map[string]any{"project_id": projectID}
	if r.DefinitionKey != "" {
		body["definition_key"] = r.DefinitionKey
	}
	if r.Version > 0 {
		body["version"] = r.Version
	}
	if len(r.Definition) > 0 {
		body["definition"] = r.Definition
	}
	if len(r.BPMNXML) > 0 {
		body["bpmn_xml"] = string(r.BPMNXML)
	}
	if r.ReplayInstanceID != "" {
		body["replay_instance_id"] = r.ReplayInstanceID
	}
	if r.Variables != nil {
		body["variables"] = r.Variables
	}
	if r.Answers != nil {
		body["answers"] = r.Answers
	}
	if !r.ClockStart.IsZero() {
		body["clock_start"] = r.ClockStart
	}
	if r.Seed != 0 {
		body["seed"] = r.Seed
	}
	if r.MaxSteps > 0 {
		body["max_steps"] = r.MaxSteps
	}
	return body
}

func (r SimulationRequest) check() error {
	if len(r.Definition) > 0 && len(r.BPMNXML) > 0 {
		return errors.New("metis: a simulation runs one draft — set Definition or BPMNXML, not both")
	}
	if r.DefinitionKey == "" && len(r.Definition) == 0 && len(r.BPMNXML) == 0 && r.ReplayInstanceID == "" {
		return errors.New("metis: a simulation needs something to run — a DefinitionKey, a draft, or an instance to replay")
	}
	return nil
}

// SimulationRun is a whole simulated run, every step of it.
type SimulationRun struct {
	RunID      string `json:"run_id"`
	Definition struct {
		Key     string `json:"key"`
		Version int    `json:"version"`
		ID      string `json:"id"`
		// Draft says an undeployed model ran; Version and ID are then zero.
		Draft bool `json:"draft,omitempty"`
	} `json:"definition"`

	Outcome SimulationOutcome `json:"outcome"`
	// EndedAtNode is the end event a completed run reached.
	EndedAtNode string `json:"ended_at_node,omitempty"`
	// AwaitingNode and AwaitingLabel are the step a [SimulationOutcomeNeedsAnswer]
	// run stopped at, and what the diagram calls it.
	AwaitingNode  string `json:"awaiting_node,omitempty"`
	AwaitingLabel string `json:"awaiting_label,omitempty"`

	VirtualDurationMs int64                `json:"virtual_duration_ms"`
	Variables         Variables            `json:"variables"`
	Incidents         []SimulationIncident `json:"incidents"`
	Steps             []SimulationStep     `json:"steps"`

	// Replay is set when the run replayed a real instance.
	Replay *SimulationReplay `json:"replay,omitempty"`
}

// VirtualDuration is how much virtual time the run took.
func (r SimulationRun) VirtualDuration() time.Duration {
	return time.Duration(r.VirtualDurationMs) * time.Millisecond
}

// EndNode is where the run finished, or, for one that needs an answer, where
// it stopped to ask. Empty for any other outcome.
func (r SimulationRun) EndNode() string {
	if r.Outcome == SimulationOutcomeNeedsAnswer {
		return r.AwaitingNode
	}
	return r.EndedAtNode
}

// SimulationStep is one thing that happened, at its virtual time.
type SimulationStep struct {
	Index  int       `json:"i"`
	Clock  time.Time `json:"clock"`
	Node   string    `json:"node"`
	Event  string    `json:"event"`
	Tokens []string  `json:"tokens"`
	Flow   string    `json:"flow,omitempty"`
	// VariablesDelta is only what this step changed.
	VariablesDelta Variables `json:"variables_delta,omitempty"`
	// Note is the step in words, for a person.
	Note     string              `json:"note"`
	Decision *SimulationDecision `json:"decision,omitempty"`
	Incident *SimulationIncident `json:"incident,omitempty"`
}

// SimulationDecision is what a gateway or decision table decided.
type SimulationDecision struct {
	Expression string `json:"expression"`
	Result     string `json:"result"`
	// Chose is the flow taken; empty when nothing matched, which raises an
	// incident.
	Chose string `json:"chose,omitempty"`
	Rule  string `json:"rule,omitempty"`
}

// SimulationIncident is a refusal the engine raised during a run.
type SimulationIncident struct {
	Node    string `json:"node"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// SimulationReplay is what a replay read off the real instance, and where the
// run and the instance went apart.
type SimulationReplay struct {
	Instance InstanceRef `json:"instance_id"`
	// Version is the version the instance ran on, Status where it is now.
	Version int    `json:"version"`
	Status  string `json:"status"`

	// StartedWith and Answers are the case read off the instance, before any
	// the request gave were laid over them — the makings of a saved case.
	StartedWith Variables          `json:"started_with"`
	Answers     []SimulationAnswer `json:"answers"`

	// Path is the steps the instance reached, in order. OnlyInRun and
	// OnlyInInstance are the steps only one of the two reached; both empty
	// means the same path.
	Path           []string `json:"path"`
	OnlyInRun      []string `json:"only_in_run"`
	OnlyInInstance []string `json:"only_in_instance"`

	// Notes say in words what could not be read off the instance.
	Notes []string `json:"notes"`
}

// SamePath reports whether the run reached exactly the steps the instance did.
func (r SimulationReplay) SamePath() bool {
	return len(r.OnlyInRun) == 0 && len(r.OnlyInInstance) == 0
}

// Simulate runs one simulation in a project and returns the whole trace.
//
//	run, err := client.Simulate(ctx, projectID, metis.SimulationRequest{
//		DefinitionKey: "expense",
//		Variables:     metis.Variables{"amount": 4200},
//		Answers: []metis.SimulationAnswer{
//			{Node: "approve", Kind: metis.AnswerPerson, Actor: "marc", After: "PT2H",
//				Returns: metis.Variables{"approved": true}},
//		},
//	})
//
// A run that ends in an incident or stops for an answer is a result, not an
// error: err is for a request that could not be run at all, such as an unknown
// process.
func (c *Client) Simulate(ctx context.Context, projectID string, req SimulationRequest) (*SimulationRun, error) {
	if err := req.check(); err != nil {
		return nil, err
	}
	var out struct {
		Run SimulationRun `json:"run"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/simulations", req.body(projectID), &out); err != nil {
		return nil, err
	}
	return &out.Run, nil
}

// MaxSimulationBatch is the most runs one [Client.SimulateBatch] takes.
const MaxSimulationBatch = 20

// SimulateBatch runs up to [MaxSimulationBatch] simulations in one request,
// one after another, and returns their runs in the order given. Prefer it to
// many Simulate calls: each run holds a database transaction for its length,
// and a batch holds one at a time.
func (c *Client) SimulateBatch(ctx context.Context, projectID string, reqs []SimulationRequest) ([]SimulationRun, error) {
	if len(reqs) > MaxSimulationBatch {
		return nil, fmt.Errorf("metis: a simulation batch takes at most %d runs, and %d were given", MaxSimulationBatch, len(reqs))
	}
	scenarios := make([]map[string]any, 0, len(reqs))
	for i, req := range reqs {
		if err := req.check(); err != nil {
			return nil, fmt.Errorf("run %d: %w", i, err)
		}
		scenarios = append(scenarios, req.body(projectID))
	}
	var out struct {
		Runs []SimulationRun `json:"runs"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/simulations/batch", map[string]any{"scenarios": scenarios}, &out); err != nil {
		return nil, err
	}
	if len(out.Runs) != len(reqs) {
		return nil, fmt.Errorf("metis: %d simulations were asked for and %d runs came back", len(reqs), len(out.Runs))
	}
	return out.Runs, nil
}

// --- Saved cases ------------------------------------------------------------

// MaxSimulationCases is the most cases one process keeps.
const MaxSimulationCases = 20

// SimulationCase is a test case saved with a process: what it starts with,
// how it is answered, and what it must end with. It is kept by process key, so
// it runs against every version, the next one included.
type SimulationCase struct {
	ID string `json:"id,omitempty"`
	// Process is the process the case belongs to; the server fills it in.
	Process   ProcessRef            `json:"-"`
	Name      string                `json:"name"`
	Scenario  SimulationScenario    `json:"scenario"`
	Expect    SimulationExpectation `json:"expect"`
	CreatedAt time.Time             `json:"created_at,omitzero"`
	UpdatedAt time.Time             `json:"updated_at,omitzero"`
}

// UnmarshalJSON reads the process from project_id and process_key.
func (sc *SimulationCase) UnmarshalJSON(data []byte) error {
	type plain SimulationCase
	var wire struct {
		plain
		processWire
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*sc = SimulationCase(wire.plain)
	sc.Process = wire.process()
	return nil
}

// SimulationScenario is what a case starts with and how it is answered.
type SimulationScenario struct {
	Variables Variables          `json:"variables"`
	Answers   []SimulationAnswer `json:"answers"`
	Seed      int64              `json:"seed"`
}

// SimulationExpectation is what a case must end with. Every field is optional;
// one with none set asks nothing, and the case only keeps a scenario.
type SimulationExpectation struct {
	// Outcome is how the run must end: [SimulationOutcomeCompleted],
	// [SimulationOutcomeNeedsAnswer] or [SimulationOutcomeIncident]. The server refuses a
	// case expecting a step budget or a timeout, which are the simulation
	// giving up rather than a result.
	Outcome SimulationOutcome `json:"outcome,omitempty"`
	// EndsAt is the node a completed run must end at, or the node a run that
	// needs an answer must stop at.
	EndsAt string `json:"ends_at,omitempty"`
	// Variables are values the run must end holding, compared as JSON values,
	// so 4200 and 4200.0 are the same number.
	Variables Variables `json:"variables,omitempty"`
}

// IsSet reports whether the expectation asks anything of a run.
func (e SimulationExpectation) IsSet() bool {
	return e.Outcome != "" || e.EndsAt != "" || len(e.Variables) > 0
}

// Check compares a run with the expectation and returns each way it differs,
// in words, as the designer says them. None means it passed.
func (e SimulationExpectation) Check(run SimulationRun) []string {
	var misses []string
	if e.Outcome != "" && run.Outcome != e.Outcome {
		misses = append(misses, fmt.Sprintf("It was expected to %s, and did not.", outcomeWords(e.Outcome)))
	}
	if end := run.EndNode(); e.EndsAt != "" && end != e.EndsAt {
		if end == "" {
			misses = append(misses, fmt.Sprintf("It was expected to end at %s, and never got there.", e.EndsAt))
		} else {
			misses = append(misses, fmt.Sprintf("It ended at %s, not %s.", end, e.EndsAt))
		}
	}
	for _, name := range sortedKeys(e.Variables) {
		want := e.Variables[name]
		got, ok := run.Variables[name]
		switch {
		case !ok:
			misses = append(misses, fmt.Sprintf("%s was expected to be %s, and was never set.", name, jsonText(want)))
		case !sameJSON(got, want):
			misses = append(misses, fmt.Sprintf("%s is %s, not %s.", name, jsonText(got), jsonText(want)))
		}
	}
	return misses
}

func sortedKeys(v Variables) []string {
	keys := make([]string, 0, len(v))
	for key := range v {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func outcomeWords(outcome SimulationOutcome) string {
	switch outcome {
	case SimulationOutcomeCompleted:
		return "finish"
	case SimulationOutcomeNeedsAnswer:
		return "stop to ask for an answer"
	case SimulationOutcomeIncident:
		return "raise an incident"
	default:
		return "end with " + string(outcome)
	}
}

// sameJSON compares two values as JSON. encoding/json writes map keys sorted,
// so two objects with the same members encode the same, and a number encodes
// the same whichever Go type holds it.
func sameJSON(a, b any) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(left, right)
}

func jsonText(v any) string {
	text, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(text)
}

func simulationCasesPath(projectID, processKey string) string {
	return "/api/v1/projects/" + url.PathEscape(projectID) + "/simulation-cases/" + url.PathEscape(processKey)
}

// ListSimulationCases lists the cases saved with a process, oldest first.
func (c *Client) ListSimulationCases(ctx context.Context, projectID, processKey string) ([]SimulationCase, error) {
	var out struct {
		Cases []SimulationCase `json:"cases"`
	}
	err := c.do(ctx, http.MethodGet, simulationCasesPath(projectID, processKey), nil, &out)
	return out.Cases, err
}

// CreateSimulationCase saves a new case with a process and returns it as
// stored, with its ID. A process keeps at most [MaxSimulationCases].
func (c *Client) CreateSimulationCase(ctx context.Context, projectID, processKey string, sc SimulationCase) (*SimulationCase, error) {
	return c.saveSimulationCase(ctx, http.MethodPost, simulationCasesPath(projectID, processKey), sc)
}

// UpdateSimulationCase rewrites a saved case, [SimulationCase.ID], with sc's
// name, scenario and expectation.
func (c *Client) UpdateSimulationCase(ctx context.Context, projectID, processKey string, sc SimulationCase) (*SimulationCase, error) {
	if sc.ID == "" {
		return nil, errors.New("metis: UpdateSimulationCase needs the case's ID — use CreateSimulationCase for a new one")
	}
	return c.saveSimulationCase(ctx, http.MethodPut, simulationCasesPath(projectID, processKey)+"/"+url.PathEscape(sc.ID), sc)
}

func (c *Client) saveSimulationCase(ctx context.Context, method, path string, sc SimulationCase) (*SimulationCase, error) {
	scenario := sc.Scenario
	if scenario.Variables == nil {
		scenario.Variables = Variables{}
	}
	if scenario.Answers == nil {
		scenario.Answers = []SimulationAnswer{}
	}
	var out struct {
		Case *SimulationCase `json:"case"`
	}
	// The project, process and ID go in the address and nowhere else: the
	// server refuses a body carrying them.
	err := c.do(ctx, method, path, map[string]any{
		"name":     sc.Name,
		"scenario": scenario,
		"expect":   sc.Expect,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Case, nil
}

// DeleteSimulationCase removes a saved case.
func (c *Client) DeleteSimulationCase(ctx context.Context, projectID, processKey, caseID string) error {
	return c.do(ctx, http.MethodDelete, simulationCasesPath(projectID, processKey)+"/"+url.PathEscape(caseID), nil, nil)
}

// SimulationCaseResult is one saved case, run.
type SimulationCaseResult struct {
	Case SimulationCase
	Run  SimulationRun
	// Misses is each way the run differs from what the case expects, in
	// words. Empty when it passed.
	Misses []string
}

// Passed reports whether the run is what the case expects. A case that
// expects nothing passes when its run completes and raises no incident.
func (r SimulationCaseResult) Passed() bool {
	if !r.Case.Expect.IsSet() {
		return r.Run.Outcome == SimulationOutcomeCompleted && len(r.Run.Incidents) == 0
	}
	return len(r.Misses) == 0
}

// RunSimulationCases runs every case saved with a process and checks each
// against what it expects — the designer's *Run every case*, for a pipeline:
//
//	results, err := client.RunSimulationCases(ctx, projectID, "expense",
//		metis.SimulationRequest{BPMNXML: bpmn}) // the version about to deploy
//	for _, r := range results {
//		if !r.Passed() {
//			t.Errorf("%s: %s", r.Case.Name, strings.Join(r.Misses, " "))
//		}
//	}
//
// against says what the cases run on: its DefinitionKey and Version, its draft,
// its ClockStart and MaxSteps. The zero value runs them on the live version.
// Each case brings its own variables, answers and seed; against's are not used.
func (c *Client) RunSimulationCases(ctx context.Context, projectID, processKey string, against SimulationRequest) ([]SimulationCaseResult, error) {
	cases, err := c.ListSimulationCases(ctx, projectID, processKey)
	if err != nil || len(cases) == 0 {
		return nil, err
	}
	if against.DefinitionKey == "" && len(against.Definition) == 0 && len(against.BPMNXML) == 0 {
		against.DefinitionKey = processKey
	}
	against.ReplayInstanceID = ""

	results := make([]SimulationCaseResult, 0, len(cases))
	for start := 0; start < len(cases); start += MaxSimulationBatch {
		chunk := cases[start:min(start+MaxSimulationBatch, len(cases))]
		reqs := make([]SimulationRequest, 0, len(chunk))
		for _, sc := range chunk {
			req := against
			req.Variables = sc.Scenario.Variables
			req.Answers = sc.Scenario.Answers
			req.Seed = sc.Scenario.Seed
			reqs = append(reqs, req)
		}
		runs, err := c.SimulateBatch(ctx, projectID, reqs)
		if err != nil {
			return nil, err
		}
		for i, sc := range chunk {
			results = append(results, SimulationCaseResult{Case: sc, Run: runs[i], Misses: sc.Expect.Check(runs[i])})
		}
	}
	return results, nil
}
