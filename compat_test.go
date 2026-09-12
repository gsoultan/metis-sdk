package metis

import (
	"context"
	"testing"
)

// The rename ships as aliases, so this file is the proof that "no code breaks"
// is true rather than intended. Every line here uses only the old spelling; it
// compiling at all is most of the test.
func TestDeprecatedTypeNamesStillWork(t *testing.T) {
	var (
		task     Task            // → UserTask
		instance ProcessInstance // → Instance
		def      DefinitionRef   // → Definition
		node     NodeRef         // → Node
		user     UserRef         // → User
		project  ProjectRef      // → Project
	)

	// Aliases, not conversions: the old and new names are the same type, so
	// these assignments need no cast in either direction.
	var (
		_ UserTask   = task
		_ Instance   = instance
		_ Definition = def
		_ Node       = node
		_ User       = user
		_ Project    = project
	)
	var _ Task = UserTask{}

	// And the old names still satisfy the API they were used with.
	task.Instance = &instance
	task.Node = &node
	task.Assignee = &user
	instance.Definition = &def

	if got := task.AssigneeUsername(); got != "" {
		t.Errorf("AssigneeUsername = %q", got)
	}
	_ = project
}

// Variables went from an alias to a defined type. Go's assignability rule —
// identical underlying types, one of them unnamed — is what keeps that from
// breaking callers, and this is where that rule gets checked rather than
// assumed.
func TestPlainMapsStillPassAsVariables(t *testing.T) {
	raw := map[string]any{"amount": 900}

	// Assignment, argument, and return, with no conversion written anywhere.
	var vars Variables = raw
	if got, ok := vars.Int("amount"); !ok || got != 900 {
		t.Errorf("Int = %d, %v", got, ok)
	}

	accept := func(v Variables) int { return v.IntOr("amount", 0) }
	if got := accept(raw); got != 900 {
		t.Errorf("passing a map[string]any gave %d", got)
	}

	// And back the other way, for callers holding the concrete map type.
	var back map[string]any = vars
	if back["amount"] != 900 {
		t.Errorf("back = %v", back["amount"])
	}
}

// A Handler is a plain func type, so the signature callers already wrote has to
// keep satisfying it.
func TestHandlerSignatureUnchanged(t *testing.T) {
	var handler Handler = func(_ context.Context, task *ExternalTask) (Variables, error) {
		return Variables{"seen": task.ID}, nil
	}

	vars, err := handler(t.Context(), &ExternalTask{ID: "t-1"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := vars.StringOr("seen", ""); got != "t-1" {
		t.Errorf("seen = %q", got)
	}
}
