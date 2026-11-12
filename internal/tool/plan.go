package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const maxPlanSteps = 7

// Plan is the plan gate's checkpoint: the model proposes steps and the user
// approves them before anything changes. It is registered only when the gate
// is on, and Mutating routes it through approval.
type Plan struct{}

func NewPlan() *Plan         { return &Plan{} }
func (*Plan) Name() string   { return "plan" }
func (*Plan) Mutating() bool { return true }
func (*Plan) Description() string {
	return "Propose a short numbered plan before changing anything. The user approves or rejects it; changes are blocked until a plan is approved for the current request. Call it again before changing course."
}

func (*Plan) Schema() json.RawMessage {
	return objectSchema(map[string]any{
		"steps": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": fmt.Sprintf("1-%d short imperative steps.", maxPlanSteps),
		},
	}, "steps")
}

type planArgs struct {
	Steps []string `json:"steps"`
}

func (*Plan) Summarize(args json.RawMessage) string {
	var a planArgs
	_ = json.Unmarshal(args, &a)
	lines := make([]string, len(a.Steps))
	for i, s := range a.Steps {
		lines[i] = fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(s))
	}
	return strings.Join(lines, "\n")
}

// Validate runs before approval so the user is never asked to approve a plan
// the tool would reject.
func (*Plan) Validate(args json.RawMessage) error {
	var a planArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if len(a.Steps) == 0 || len(a.Steps) > maxPlanSteps {
		return fmt.Errorf("a plan needs 1-%d steps, got %d", maxPlanSteps, len(a.Steps))
	}
	for _, s := range a.Steps {
		if strings.TrimSpace(s) == "" {
			return errors.New("plan steps must not be empty")
		}
	}
	return nil
}

func (p *Plan) Run(ctx context.Context, args json.RawMessage) (string, error) {
	if err := p.Validate(args); err != nil {
		return "", err
	}
	return "plan approved; carry it out, and call plan again before changing course", nil
}
