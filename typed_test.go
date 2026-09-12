package metis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type chargeInput struct {
	ChargeID string  `json:"chargeID"`
	Amount   float64 `json:"amount"`
	Count    int     `json:"count"`
}

type chargeResult struct {
	Reversed bool   `json:"reversed"`
	Receipt  string `json:"receipt"`
}

// The point of the typed worker: the handler reads fields, and an int declared
// as an int arrives as one — the float64 problem is encoding/json's to solve
// once the shape is declared, rather than the caller's at every key.
func TestTypedWorkerDecodesInputAndEncodesOutput(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/external-tasks/fetch-and-lock", 200, `{"tasks":[{
		"id":"task-1","topic":"reverse-charge",
		"variables":{"chargeID":"ch_9","amount":42.5,"count":3}
	}]}`)
	f.respond("POST /api/v1/external-tasks/task-1/complete", 200, `{}`)

	var (
		mu   sync.Mutex
		seen chargeInput
		task *TypedTask[chargeInput]
	)
	done := make(chan struct{})
	var once sync.Once

	worker := NewTypedWorker(client, "reverse-charge", "w-1",
		WorkerOptions{PollInterval: time.Millisecond},
		func(_ context.Context, got *TypedTask[chargeInput]) (chargeResult, error) {
			mu.Lock()
			seen, task = got.Input, got
			mu.Unlock()
			once.Do(func() { close(done) })
			return chargeResult{Reversed: true, Receipt: "r_1"}, nil
		})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the handler was never called")
	}
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if seen.ChargeID != "ch_9" || seen.Amount != 42.5 {
		t.Errorf("input = %+v", seen)
	}
	// Declared as an int, so it is an int — no whole-number dance at the call
	// site, which is the whole argument for the typed form.
	if seen.Count != 3 {
		t.Errorf("count = %d, want 3", seen.Count)
	}
	// The task itself is embedded and reachable.
	if task.ID != "task-1" || task.Topic != "reverse-charge" {
		t.Errorf("task = %+v", task.ExternalTask)
	}
	// And the untyped variables are still there for anything the struct omits.
	if got, ok := task.Variables.String("chargeID"); !ok || got != "ch_9" {
		t.Errorf("raw variables lost: %v", ok)
	}
}

// A struct returned by the handler has to reach the engine as named variables.
func TestTypedWorkerReportsStructuredOutput(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/external-tasks/fetch-and-lock", 200,
		`{"tasks":[{"id":"task-1","topic":"t","variables":{"chargeID":"ch_9"}}]}`)
	f.respond("POST /api/v1/external-tasks/task-1/complete", 200, `{}`)

	done := make(chan struct{})
	var once sync.Once
	worker := NewTypedWorker(client, "t", "w-1",
		WorkerOptions{PollInterval: time.Millisecond},
		func(_ context.Context, _ *TypedTask[chargeInput]) (chargeResult, error) {
			once.Do(func() { close(done) })
			return chargeResult{Reversed: true, Receipt: "r_1"}, nil
		})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() { _ = worker.Run(ctx) }()
	<-done

	complete := waitForRequest(t, f, "/api/v1/external-tasks/task-1/complete")
	cancel()

	vars, ok := complete.Body["variables"].(map[string]any)
	if !ok {
		t.Fatalf("no variables in %#v", complete.Body)
	}
	if vars["reversed"] != true || vars["receipt"] != "r_1" {
		t.Errorf("variables = %#v", vars)
	}
}

// Variables that do not fit the declared input fail the task, not the worker —
// and the message has to name what did not fit, because the cause is a diagram
// and a worker that have drifted apart.
func TestTypedWorkerFailsTheTaskOnUndecodableInput(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/external-tasks/fetch-and-lock", 200, `{"tasks":[{
		"id":"task-1","topic":"reverse-charge","retries":3,
		"variables":{"amount":"not a number"}
	}]}`)
	f.respond("POST /api/v1/external-tasks/task-1/failure", 200, `{}`)

	called := false
	worker := NewTypedWorker(client, "reverse-charge", "w-1",
		WorkerOptions{PollInterval: time.Millisecond},
		func(_ context.Context, _ *TypedTask[chargeInput]) (chargeResult, error) {
			called = true
			return chargeResult{}, nil
		})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() { _ = worker.Run(ctx) }()

	failure := waitForRequest(t, f, "/api/v1/external-tasks/task-1/failure")
	cancel()

	if called {
		t.Error("the handler ran on input that did not decode")
	}
	message, _ := failure.Body["error_message"].(string)
	for _, want := range []string{"task-1", "reverse-charge"} {
		if !strings.Contains(message, want) {
			t.Errorf("failure message %q does not mention %q", message, want)
		}
	}
	// Retries are decremented as with any failure, so it lands for an operator
	// rather than spinning: retrying will not make the shape fit.
	if failure.Body["retries"] != float64(2) {
		t.Errorf("retries = %v, want 2", failure.Body["retries"])
	}
}

// Taking Variables as the input type is the escape hatch for a loose shape, and
// it must not be mangled by the round trip.
func TestTypedWorkerAcceptsVariablesAtBothEnds(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/external-tasks/fetch-and-lock", 200,
		`{"tasks":[{"id":"task-1","topic":"t","variables":{"anything":"goes"}}]}`)
	f.respond("POST /api/v1/external-tasks/task-1/complete", 200, `{}`)

	done := make(chan struct{})
	var once sync.Once
	worker := NewTypedWorker(client, "t", "w-1",
		WorkerOptions{PollInterval: time.Millisecond},
		func(_ context.Context, task *TypedTask[Variables]) (Variables, error) {
			once.Do(func() { close(done) })
			if got := task.Input.StringOr("anything", ""); got != "goes" {
				t.Errorf("input = %+v", task.Input)
			}
			return Variables{"echoed": true}, nil
		})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() { _ = worker.Run(ctx) }()
	<-done

	complete := waitForRequest(t, f, "/api/v1/external-tasks/task-1/complete")
	cancel()

	vars, _ := complete.Body["variables"].(map[string]any)
	if vars["echoed"] != true {
		t.Errorf("variables = %#v", vars)
	}
}

// waitForRequest blocks until the SDK has sent one to path.
func waitForRequest(t *testing.T, f *fakeServer, path string) recordedRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, request := range f.snapshot() {
			if request.Path == path {
				return request
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no request to %s", path)
	return recordedRequest{}
}

func TestVariablesAs(t *testing.T) {
	// Shaped as it arrives from the wire.
	encoded, err := json.Marshal(Variables{"amount": 900, "customer": "acme"})
	if err != nil {
		t.Fatal(err)
	}
	var vars Variables
	if err := json.Unmarshal(encoded, &vars); err != nil {
		t.Fatal(err)
	}

	type order struct {
		Amount   int    `json:"amount"`
		Customer string `json:"customer"`
		Missing  string `json:"missing"`
	}

	got, err := vars.As[order]()
	if err != nil {
		t.Fatalf("As: %v", err)
	}
	// Declared int, decoded int — the float64 problem does not reach the caller
	// once the shape is declared.
	if got.Amount != 900 || got.Customer != "acme" {
		t.Errorf("order = %+v", got)
	}
	if got.Missing != "" {
		t.Errorf("Missing = %q, want the zero value", got.Missing)
	}

	// A shape that cannot hold the data is an error, not a silent zero.
	if _, err := vars.As[[]string](); err == nil {
		t.Error("decoding an object into a slice was accepted")
	}
}

func TestVariablesOf(t *testing.T) {
	t.Run("a struct becomes named variables", func(t *testing.T) {
		vars, err := VariablesOf(chargeResult{Reversed: true, Receipt: "r_1"})
		if err != nil {
			t.Fatalf("VariablesOf: %v", err)
		}
		if !vars.BoolOr("reversed", false) || vars.StringOr("receipt", "") != "r_1" {
			t.Errorf("vars = %+v", vars)
		}
	})

	t.Run("Variables passes through untouched", func(t *testing.T) {
		original := Variables{"a": 1}
		got, err := VariablesOf(original)
		if err != nil {
			t.Fatalf("VariablesOf: %v", err)
		}
		// Not merely equal — the same map, since round-tripping it would turn
		// the 1 into a float64 for no reason.
		if got["a"] != 1 {
			t.Errorf("a = %#v, want the untouched int", got["a"])
		}
	})

	t.Run("a lone value has no variable name to file it under", func(t *testing.T) {
		if _, err := VariablesOf(42); err == nil {
			t.Error("a bare number was accepted as a variable set")
		}
		if _, err := VariablesOf("hello"); err == nil {
			t.Error("a bare string was accepted as a variable set")
		}
	})

	t.Run("nil is nothing to write back, not an error", func(t *testing.T) {
		vars, err := VariablesOf[Variables](nil)
		if err != nil {
			t.Fatalf("VariablesOf(nil): %v", err)
		}
		if len(vars) != 0 {
			t.Errorf("vars = %+v", vars)
		}
	})
}

// The generic form and the pointer form have to agree; the first is built on
// the second.
func TestAsAndDecodeAgree(t *testing.T) {
	vars := Variables{"amount": 900.0, "customer": "acme"}
	type order struct {
		Amount   float64 `json:"amount"`
		Customer string  `json:"customer"`
	}

	viaGeneric, err := vars.As[order]()
	if err != nil {
		t.Fatalf("As: %v", err)
	}
	var viaPointer order
	if err := vars.Decode(&viaPointer); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if viaGeneric != viaPointer {
		t.Errorf("As gave %+v, Decode gave %+v", viaGeneric, viaPointer)
	}
	_ = errors.New
}
