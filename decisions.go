package metis

import (
	"context"
	"errors"
	"net/http"
)

// DecisionResult is what one evaluation of a decision table answered, and
// which lines of the table answered it.
type DecisionResult struct {
	// Values are the outputs, by output name. Under a COLLECT hit policy an
	// output holds the list of what every matching line gave.
	Values Variables `json:"values"`
	// MatchedRules are the positions, from zero, of the lines that produced
	// the result, in table order: one under FIRST, every match under COLLECT.
	// A position is only meaningful against the version that produced it.
	MatchedRules []int `json:"matched_rules,omitempty"`
	// MatchedRuleIDs are the same lines by ID, which survives the table being
	// edited. Empty for lines saved without one.
	MatchedRuleIDs []string `json:"matched_rule_ids,omitempty"`

	// DecisionKey, DecisionName and DecisionVersion say which table, and which
	// version of it, decided — what an audit record of the decision needs.
	DecisionKey     string `json:"decision_key,omitempty"`
	DecisionName    string `json:"decision_name,omitempty"`
	DecisionVersion int    `json:"decision_version,omitempty"`
}

// Matched reports whether any line of the table applied. A table with no
// matching line answers with no values rather than with an error.
func (r DecisionResult) Matched() bool { return len(r.MatchedRules) > 0 || len(r.MatchedRuleIDs) > 0 }

// EvaluateOption adjusts one [Client.EvaluateDecision] call.
type EvaluateOption func(*evaluateRequest)

type evaluateRequest struct {
	ProjectID string    `json:"project_id"`
	Key       string    `json:"key"`
	Version   int       `json:"version,omitempty"`
	Variables Variables `json:"variables,omitempty"`
}

// WithDecisionVersion evaluates one version of the table rather than the live
// one — to compare a proposed policy with the current one on the same input.
func WithDecisionVersion(version int) EvaluateOption {
	return func(r *evaluateRequest) { r.Version = version }
}

// EvaluateDecision runs a project's DMN decision table on the given inputs and
// returns its outputs, without starting a process:
//
//	result, err := client.EvaluateDecision(ctx, projectID, "credit-limit", metis.Variables{
//		"score": 712, "income": 54000,
//	})
//	if err != nil {
//		return err
//	}
//	limit, _ := result.Values.Int("limit")
//
// The key is the decision's key, which is unique within a project; the live
// version answers unless [WithDecisionVersion] names another. An unknown key
// or a missing input is [IsInvalid].
func (c *Client) EvaluateDecision(ctx context.Context, projectID, key string, variables Variables, opts ...EvaluateOption) (*DecisionResult, error) {
	if projectID == "" || key == "" {
		return nil, errors.New("metis: EvaluateDecision needs a project ID and a decision key — keys are unique only within a project")
	}
	req := evaluateRequest{ProjectID: projectID, Key: key, Variables: variables}
	for _, opt := range opts {
		opt(&req)
	}
	var out struct {
		Result DecisionResult `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/decisions/evaluate", req, &out); err != nil {
		return nil, err
	}
	return &out.Result, nil
}
