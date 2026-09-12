package metis

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// Variables is the data a process instance carries: what you seed a process
// with, what a task hands back, and what the engine passes between steps.
//
//	metis.Variables{"amount": 900, "customer": "acme", "urgent": true}
//
// # Reading them back
//
// Values come off the wire as any, and JSON has one number type, so **every
// number the server sends is a float64** — including one you originally sent as
// an int. That makes the obvious thing wrong:
//
//	amount := vars["amount"].(int)      // panics: it is a float64
//	name   := vars["name"].(string)     // panics if the key is absent
//
// The accessors below never panic. Each reports whether the key was present and
// held the type asked for, and [Variables.Int] accepts a whole float64 because
// that is what an int becomes in transit:
//
//	amount, ok := vars.Int("amount")    // 900, true
//	name, ok   := vars.String("name")   // "", false when absent
//	urgent     := vars.BoolOr("urgent", false)
//
// The zero value — a nil Variables — is safe to read from; every accessor
// reports not-found rather than panicking. Writing to a nil map still panics,
// as with any Go map.
type Variables map[string]any

// Has reports whether key is present, whatever its type and even if its value
// is nil. Use it to tell "absent" from "present but empty".
func (v Variables) Has(key string) bool {
	_, ok := v[key]
	return ok
}

// String returns the value at key when it is a string.
func (v Variables) String(key string) (string, bool) {
	s, ok := v[key].(string)
	return s, ok
}

// StringOr returns the string at key, or fallback when it is absent or is not
// a string.
func (v Variables) StringOr(key, fallback string) string {
	if s, ok := v.String(key); ok {
		return s
	}
	return fallback
}

// Bool returns the value at key when it is a bool.
func (v Variables) Bool(key string) (bool, bool) {
	b, ok := v[key].(bool)
	return b, ok
}

// BoolOr returns the bool at key, or fallback when it is absent or is not a
// bool.
func (v Variables) BoolOr(key string, fallback bool) bool {
	if b, ok := v.Bool(key); ok {
		return b
	}
	return fallback
}

// Float64 returns the value at key as a float64. It accepts every numeric
// shape a value can arrive in: float64 from JSON, json.Number from a decoder
// configured for it, and the integer types a caller may have written into the
// map directly.
func (v Variables) Float64(key string) (float64, bool) {
	switch n := v[key].(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// Float64Or returns the number at key, or fallback when it is absent or is not
// a number.
func (v Variables) Float64Or(key string, fallback float64) float64 {
	if f, ok := v.Float64(key); ok {
		return f
	}
	return fallback
}

// Int returns the value at key as an int.
//
// A number that crossed the wire is a float64, so this accepts a float64 whose
// value is a whole number — 900.0 is the int 900. It refuses one with a
// fractional part rather than truncating silently: a quantity that arrived as
// 2.5 is a bug worth seeing, not a 2.
func (v Variables) Int(key string) (int, bool) {
	n, ok := v.Int64(key)
	if !ok {
		return 0, false
	}
	// On 32-bit builds an int64 can hold more than an int.
	if int64(int(n)) != n {
		return 0, false
	}
	return int(n), true
}

// IntOr returns the int at key, or fallback when it is absent, is not a
// number, or is not whole.
func (v Variables) IntOr(key string, fallback int) int {
	if n, ok := v.Int(key); ok {
		return n
	}
	return fallback
}

// Int64 returns the value at key as an int64, under the same whole-number rule
// as [Variables.Int].
func (v Variables) Int64(key string) (int64, bool) {
	switch n := v[key].(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case float64:
		return wholeToInt64(n)
	case float32:
		return wholeToInt64(float64(n))
	default:
		return 0, false
	}
}

// wholeToInt64 converts f when it is a whole number inside int64's range.
// Values past that range are refused rather than wrapped: float64 cannot
// represent every int64 exactly, so a silent conversion there would invent a
// number nobody sent.
func wholeToInt64(f float64) (int64, bool) {
	// Range and NaN are checked first, and must stay first: converting an
	// out-of-range float64 to int64 is undefined in Go, so the whole-number
	// test below cannot be the thing that screens for it.
	const limit = 1 << 62 // comfortably inside float64's exactly-representable range
	if math.IsNaN(f) || f >= limit || f <= -limit {
		return 0, false
	}
	if f != float64(int64(f)) {
		return 0, false
	}
	return int64(f), true
}

// Time returns the value at key as a time.Time. The engine sends timestamps as
// RFC 3339 strings; a time.Time a caller put into the map directly is returned
// as it is.
func (v Variables) Time(key string) (time.Time, bool) {
	switch t := v[key].(type) {
	case time.Time:
		return t, true
	case string:
		parsed, err := time.Parse(time.RFC3339, t)
		return parsed, err == nil
	default:
		return time.Time{}, false
	}
}

// Map returns the value at key when it is a nested object, as Variables — so
// the same accessors work at any depth:
//
//	if customer, ok := vars.Map("customer"); ok {
//		name := customer.StringOr("name", "")
//	}
func (v Variables) Map(key string) (Variables, bool) {
	switch m := v[key].(type) {
	case Variables:
		return m, true
	case map[string]any:
		return Variables(m), true
	default:
		return nil, false
	}
}

// Slice returns the value at key when it is a JSON array.
func (v Variables) Slice(key string) ([]any, bool) {
	s, ok := v[key].([]any)
	return s, ok
}

// Decode unmarshals the whole variable set into a struct, for callers who
// would rather declare their shape once than reach for keys:
//
//	var order struct {
//		Amount   float64 `json:"amount"`
//		Customer string  `json:"customer"`
//	}
//	if err := task.Variables.Decode(&order); err != nil { ... }
//
// Fields the process does not carry are left at their zero value, so a missing
// variable is indistinguishable from an empty one. When that difference
// matters, use [Variables.Has] or the accessors.
func (v Variables) Decode(target any) error {
	encoded, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

// As decodes the whole variable set into a T and returns it — the generic form
// of [Variables.Decode], for when you would rather have the value than pass a
// pointer:
//
//	order, err := task.Variables.As[Order]()
//
// A generic method needs Go 1.27, which go.mod already asks for. If that floor
// ever has to come down — the rest of the code compiles at 1.24 — this is the
// one declaration that would have to go back to a package-level function,
// metis.As[Order](vars), and its call sites with it.
//
// The same caveat as Decode applies: a variable the process does not carry
// leaves its field at the zero value, so absent and empty are
// indistinguishable. Use [Variables.Has] or the typed accessors when that
// difference matters.
//
// This is where a type parameter earns its place. There is deliberately no
// generic Get[T](key) accessor, because it cannot be honest: values arrive as
// any, JSON numbers are all float64, and converting one to the T a caller asked
// for means a hard-coded list of supported types inside a signature that claims
// to accept every type. Get[int] would work and Get[uint] would silently return
// false. [Variables.Int] and its siblings say exactly what they handle.
func (v Variables) As[T any]() (T, error) {
	var target T
	if err := v.Decode(&target); err != nil {
		return target, err
	}
	return target, nil
}

// VariablesOf turns a value into [Variables] — the inverse of [Variables.As],
// for handing a struct back to the engine:
//
//	vars, err := metis.VariablesOf(ChargeResult{Reversed: true})
//
// value must encode to a JSON object, so a struct or a map. A bare number or
// string is refused: process variables are a named set, and there is no name to
// file a lone value under.
func VariablesOf[T any](value T) (Variables, error) {
	// A Variables already is one; the round trip below would work but copies
	// for nothing, and it is the common case in a typed worker returning a map.
	if already, ok := any(value).(Variables); ok {
		return already, nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("metis: encode variables: %w", err)
	}
	var vars Variables
	if err := json.Unmarshal(encoded, &vars); err != nil {
		return nil, fmt.Errorf("metis: %T does not encode to a JSON object, so it has no variable names: %w", value, err)
	}
	return vars, nil
}

// Merge returns a copy of v with other's entries laid over it. Neither input is
// modified, so it is safe to merge into variables you got from a task.
func (v Variables) Merge(other Variables) Variables {
	merged := make(Variables, len(v)+len(other))
	for key, value := range v {
		merged[key] = value
	}
	for key, value := range other {
		merged[key] = value
	}
	return merged
}
