package metis

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// TestIntSurvivesJSONRoundTrip is the reason the typed accessors exist. An int
// written into a process comes back as a float64, because JSON has one number
// type — so the obvious assertion is the wrong one, on every number the engine
// ever sends.
func TestIntSurvivesJSONRoundTrip(t *testing.T) {
	sent := Variables{"amount": 900}

	encoded, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var received Variables
	if err := json.Unmarshal(encoded, &received); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// What a caller reaching for the map directly would find.
	if _, isInt := received["amount"].(int); isInt {
		t.Fatal("test is built on a false premise: JSON now decodes ints as int")
	}
	if _, isFloat := received["amount"].(float64); !isFloat {
		t.Fatalf("amount decoded as %T, expected float64", received["amount"])
	}

	// What the accessor makes of it.
	amount, ok := received.Int("amount")
	if !ok || amount != 900 {
		t.Errorf("Int(amount) = %d, %v; want 900, true", amount, ok)
	}
}

func TestVariablesNumbers(t *testing.T) {
	vars := Variables{
		"float":    900.0,
		"fraction": 2.5,
		"int":      42,
		"int64":    int64(43),
		"number":   json.Number("44"),
		"text":     "45",
		"nothing":  nil,
	}

	t.Run("Int accepts whole numbers in any shape", func(t *testing.T) {
		for _, key := range []string{"float", "int", "int64", "number"} {
			if _, ok := vars.Int(key); !ok {
				t.Errorf("Int(%q) refused a whole number", key)
			}
		}
	})

	t.Run("Int refuses a fraction rather than truncating", func(t *testing.T) {
		// Truncating 2.5 to 2 would hide a bug in whoever produced it.
		if got, ok := vars.Int("fraction"); ok {
			t.Errorf("Int(fraction) = %d, true; want refusal", got)
		}
		// As a float it is perfectly readable.
		if got, ok := vars.Float64("fraction"); !ok || got != 2.5 {
			t.Errorf("Float64(fraction) = %v, %v; want 2.5, true", got, ok)
		}
	})

	t.Run("non-numbers and absent keys are refused, not guessed", func(t *testing.T) {
		for _, key := range []string{"text", "nothing", "absent"} {
			if _, ok := vars.Int(key); ok {
				t.Errorf("Int(%q) invented a number", key)
			}
			if _, ok := vars.Float64(key); ok {
				t.Errorf("Float64(%q) invented a number", key)
			}
		}
	})

	t.Run("Or forms fall back", func(t *testing.T) {
		if got := vars.IntOr("absent", 7); got != 7 {
			t.Errorf("IntOr = %d, want 7", got)
		}
		if got := vars.IntOr("int", 7); got != 42 {
			t.Errorf("IntOr = %d, want 42", got)
		}
		if got := vars.Float64Or("absent", 1.5); got != 1.5 {
			t.Errorf("Float64Or = %v, want 1.5", got)
		}
	})

	t.Run("Int64 refuses values float64 cannot hold exactly", func(t *testing.T) {
		// Converting an out-of-range float64 to int64 is undefined in Go, so
		// these have to be screened by the range check rather than by the
		// whole-number test — including NaN, which compares false against
		// everything and so slips past a naive bounds check.
		for name, value := range map[string]float64{
			"huge":      1e30,
			"tiny":      -1e30,
			"NaN":       math.NaN(),
			"+Inf":      math.Inf(1),
			"-Inf":      math.Inf(-1),
			"max int64": math.MaxInt64, // not exactly representable as float64
		} {
			vars := Variables{"n": value}
			if got, ok := vars.Int64("n"); ok {
				t.Errorf("Int64(%s) = %d, true; want refusal", name, got)
			}
			if got, ok := vars.Int("n"); ok {
				t.Errorf("Int(%s) = %d, true; want refusal", name, got)
			}
		}

		// The boundary the other way: values well inside the range still work.
		fine := Variables{"n": 1e15}
		if got, ok := fine.Int64("n"); !ok || got != 1_000_000_000_000_000 {
			t.Errorf("Int64(1e15) = %d, %v", got, ok)
		}
	})
}

func TestVariablesStringsAndBools(t *testing.T) {
	vars := Variables{"name": "acme", "urgent": true, "count": 3}

	if got, ok := vars.String("name"); !ok || got != "acme" {
		t.Errorf("String = %q, %v", got, ok)
	}
	if _, ok := vars.String("count"); ok {
		t.Error("String read a number as a string")
	}
	if got := vars.StringOr("absent", "anonymous"); got != "anonymous" {
		t.Errorf("StringOr = %q", got)
	}
	if got, ok := vars.Bool("urgent"); !ok || !got {
		t.Errorf("Bool = %v, %v", got, ok)
	}
	if got := vars.BoolOr("absent", true); !got {
		t.Error("BoolOr did not fall back")
	}
	if got := vars.BoolOr("count", false); got {
		t.Error("BoolOr read a number as a bool")
	}
}

func TestVariablesHasDistinguishesAbsentFromNil(t *testing.T) {
	vars := Variables{"present": nil}

	if !vars.Has("present") {
		t.Error("Has said a present-but-nil key was absent")
	}
	if vars.Has("absent") {
		t.Error("Has invented a key")
	}
}

func TestVariablesTime(t *testing.T) {
	when := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	vars := Variables{
		"wire":   when.Format(time.RFC3339),
		"native": when,
		"junk":   "not a time",
	}

	for _, key := range []string{"wire", "native"} {
		got, ok := vars.Time(key)
		if !ok || !got.Equal(when) {
			t.Errorf("Time(%q) = %v, %v", key, got, ok)
		}
	}
	if _, ok := vars.Time("junk"); ok {
		t.Error("Time parsed a non-time")
	}
}

func TestVariablesNested(t *testing.T) {
	// Shaped as it arrives from the wire: nested objects are map[string]any.
	vars := Variables{
		"customer": map[string]any{"name": "acme", "tier": 2.0},
		"lines":    []any{"a", "b"},
		"flat":     "no",
	}

	customer, ok := vars.Map("customer")
	if !ok {
		t.Fatal("Map did not read a nested object")
	}
	if got := customer.StringOr("name", ""); got != "acme" {
		t.Errorf("nested name = %q", got)
	}
	if got, ok := customer.Int("tier"); !ok || got != 2 {
		t.Errorf("nested tier = %d, %v", got, ok)
	}
	if _, ok := vars.Map("flat"); ok {
		t.Error("Map read a string as an object")
	}

	lines, ok := vars.Slice("lines")
	if !ok || len(lines) != 2 {
		t.Errorf("Slice = %v, %v", lines, ok)
	}
	if _, ok := vars.Slice("flat"); ok {
		t.Error("Slice read a string as an array")
	}
}

func TestVariablesDecode(t *testing.T) {
	vars := Variables{"amount": 900, "customer": "acme"}

	var order struct {
		Amount   float64 `json:"amount"`
		Customer string  `json:"customer"`
		Missing  string  `json:"missing"`
	}
	if err := vars.Decode(&order); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if order.Amount != 900 || order.Customer != "acme" {
		t.Errorf("decoded = %+v", order)
	}
	if order.Missing != "" {
		t.Errorf("Missing = %q, want the zero value", order.Missing)
	}
}

func TestVariablesMergeDoesNotMutate(t *testing.T) {
	// Merging into variables you got from a task must not edit the task.
	original := Variables{"a": 1, "b": 2}
	merged := original.Merge(Variables{"b": 3, "c": 4})

	if got := merged.IntOr("b", 0); got != 3 {
		t.Errorf("merged b = %d, want the overlay's 3", got)
	}
	if got := merged.IntOr("c", 0); got != 4 {
		t.Errorf("merged c = %d", got)
	}
	if got := original.IntOr("b", 0); got != 2 {
		t.Errorf("Merge mutated the receiver: b = %d, want 2", got)
	}
	if original.Has("c") {
		t.Error("Merge added a key to the receiver")
	}
}

// A nil map is what an absent "variables" field decodes to, so every accessor
// has to survive it — a task with no variables is ordinary, not an error.
func TestNilVariablesAreSafeToRead(t *testing.T) {
	var vars Variables

	if vars.Has("anything") {
		t.Error("Has on nil found something")
	}
	if _, ok := vars.String("k"); ok {
		t.Error("String on nil found something")
	}
	if _, ok := vars.Int("k"); ok {
		t.Error("Int on nil found something")
	}
	if _, ok := vars.Float64("k"); ok {
		t.Error("Float64 on nil found something")
	}
	if _, ok := vars.Bool("k"); ok {
		t.Error("Bool on nil found something")
	}
	if _, ok := vars.Time("k"); ok {
		t.Error("Time on nil found something")
	}
	if _, ok := vars.Map("k"); ok {
		t.Error("Map on nil found something")
	}
	if _, ok := vars.Slice("k"); ok {
		t.Error("Slice on nil found something")
	}
	if got := vars.StringOr("k", "fallback"); got != "fallback" {
		t.Errorf("StringOr on nil = %q", got)
	}
	if got := vars.Merge(Variables{"a": 1}); got.IntOr("a", 0) != 1 {
		t.Error("Merge onto nil lost the overlay")
	}
}

// Every numeric shape a value can arrive in, through every numeric accessor.
//
// The accessors are the package's central safety claim — they never panic and
// never invent a number — and each is a type switch whose arms are easy to add
// and easy to leave untested. A table is the only way to be sure the arm for
// int32 does what the arm for int64 does.
func TestNumericAccessorsAcceptEveryShape(t *testing.T) {
	// Each of these is 42 as some Go type, which is how it arrives depending on
	// whether it crossed the wire, was decoded with UseNumber, or was built by
	// the caller.
	shapes := map[string]any{
		"float64":     float64(42),
		"float32":     float32(42),
		"int":         int(42),
		"int32":       int32(42),
		"int64":       int64(42),
		"json.Number": json.Number("42"),
	}

	for name, value := range shapes {
		t.Run(name, func(t *testing.T) {
			vars := Variables{"n": value}

			if got, ok := vars.Float64("n"); !ok || got != 42 {
				t.Errorf("Float64 = %v, %v; want 42, true", got, ok)
			}
			if got := vars.Float64Or("n", -1); got != 42 {
				t.Errorf("Float64Or = %v; want 42", got)
			}
			if got, ok := vars.Int("n"); !ok || got != 42 {
				t.Errorf("Int = %d, %v; want 42, true", got, ok)
			}
			if got := vars.IntOr("n", -1); got != 42 {
				t.Errorf("IntOr = %d; want 42", got)
			}
			if got, ok := vars.Int64("n"); !ok || got != 42 {
				t.Errorf("Int64 = %d, %v; want 42, true", got, ok)
			}
		})
	}
}

// A fraction is readable as a float and refused as an integer, in whichever
// shape it arrives — truncating 42.5 to 42 would hide a bug in whoever produced
// it, and the refusal has to be consistent across the type switch's arms.
func TestNumericAccessorsRefuseFractionsInEveryShape(t *testing.T) {
	for name, value := range map[string]any{
		"float64":     float64(42.5),
		"float32":     float32(42.5),
		"json.Number": json.Number("42.5"),
	} {
		t.Run(name, func(t *testing.T) {
			vars := Variables{"n": value}

			if got, ok := vars.Float64("n"); !ok || got != 42.5 {
				t.Errorf("Float64 = %v, %v; want 42.5, true", got, ok)
			}
			if got, ok := vars.Int("n"); ok {
				t.Errorf("Int = %d, true; want a refusal", got)
			}
			if got, ok := vars.Int64("n"); ok {
				t.Errorf("Int64 = %d, true; want a refusal", got)
			}
			if got := vars.IntOr("n", -1); got != -1 {
				t.Errorf("IntOr = %d; want the fallback", got)
			}
		})
	}
}

// json.Number carries text, so it is the one shape that can hold something
// that is not a number at all.
func TestJSONNumberThatIsNotANumber(t *testing.T) {
	vars := Variables{"n": json.Number("not-a-number")}

	if got, ok := vars.Float64("n"); ok {
		t.Errorf("Float64 = %v, true; want a refusal", got)
	}
	if got, ok := vars.Int64("n"); ok {
		t.Errorf("Int64 = %d, true; want a refusal", got)
	}
	if got := vars.Float64Or("n", 1.5); got != 1.5 {
		t.Errorf("Float64Or = %v; want the fallback", got)
	}

	// An integer accessor must also refuse a json.Number holding a fraction,
	// which Int64 reads through a different path than a float64 does.
	fraction := Variables{"n": json.Number("2.5")}
	if got, ok := fraction.Int64("n"); ok {
		t.Errorf("Int64(json.Number 2.5) = %d, true; want a refusal", got)
	}
}

// Non-numeric values reach the type switch's default arm on every accessor.
func TestNumericAccessorsRefuseNonNumbers(t *testing.T) {
	vars := Variables{
		"string": "42",
		"bool":   true,
		"slice":  []any{42},
		"map":    map[string]any{"n": 42},
		"nil":    nil,
	}

	for key := range vars {
		t.Run(key, func(t *testing.T) {
			if got, ok := vars.Float64(key); ok {
				t.Errorf("Float64 = %v, true; want a refusal", got)
			}
			if got, ok := vars.Int(key); ok {
				t.Errorf("Int = %d, true; want a refusal", got)
			}
			if got, ok := vars.Int64(key); ok {
				t.Errorf("Int64 = %d, true; want a refusal", got)
			}
		})
	}
}

// Map accepts both the shape the wire produces and the shape a caller builds,
// and Decode has to report a target it cannot fill rather than leaving a
// half-populated struct.
func TestMapAndDecodeEdges(t *testing.T) {
	t.Run("a nested Variables, not just map[string]any", func(t *testing.T) {
		vars := Variables{"customer": Variables{"name": "acme"}}
		nested, ok := vars.Map("customer")
		if !ok || nested.StringOr("name", "") != "acme" {
			t.Errorf("Map = %+v, %v", nested, ok)
		}
	})

	t.Run("Decode reports a mismatch", func(t *testing.T) {
		vars := Variables{"amount": "not a number"}
		var target struct {
			Amount float64 `json:"amount"`
		}
		if err := vars.Decode(&target); err == nil {
			t.Error("decoding a string into a float64 field was accepted")
		}
	})

	t.Run("Decode refuses a non-pointer", func(t *testing.T) {
		var target struct{}
		if err := (Variables{"a": 1}).Decode(target); err == nil {
			t.Error("decoding into a non-pointer was accepted")
		}
	})
}

// InstanceID on a task whose instance was expanded — the covered half was the
// nil case.
func TestUserTaskInstanceIDWhenExpanded(t *testing.T) {
	task := UserTask{Instance: &Instance{ID: "inst-1"}}
	if got := task.InstanceID(); got != "inst-1" {
		t.Errorf("InstanceID = %q", got)
	}
}
