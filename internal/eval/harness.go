package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Rate is one cell of the output table.
type Rate struct {
	Fixture string  `json:"fixture"`
	Attack  string  `json:"attack"`
	ClipLen int     `json:"clip_seconds"`
	Trials  int     `json:"trials"`
	TPR     float64 `json:"tpr"`
	FPR     float64 `json:"fpr"`
	// MeanConfidence and MeanNull show how much headroom a result had, which a
	// bare rate hides.
	MeanConfidence float64 `json:"mean_confidence"`
	MeanNull       float64 `json:"mean_null"`
	Collusion      bool    `json:"collusion,omitempty"`
}

type Report struct {
	Threshold float64 `json:"threshold"`
	Rates     []Rate  `json:"rates"`
}

func (r Report) Lookup(fixture, attack string, clip int) (Rate, bool) {
	for _, rate := range r.Rates {
		if rate.Fixture == fixture && rate.Attack == attack && rate.ClipLen == clip {
			return rate, true
		}
	}
	return Rate{}, false
}

func (r Report) Save(path string) error {
	sort.Slice(r.Rates, func(i, j int) bool {
		a, b := r.Rates[i], r.Rates[j]
		if a.Fixture != b.Fixture {
			return a.Fixture < b.Fixture
		}
		if a.Attack != b.Attack {
			return a.Attack < b.Attack
		}
		return a.ClipLen < b.ClipLen
	})
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("eval: encode report: %w", err)
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

func LoadReport(path string) (Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("eval: read baseline: %w", err)
	}
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return Report{}, fmt.Errorf("eval: decode baseline: %w", err)
	}
	return r, nil
}

// Regression is a measured drop against the committed baseline.
type Regression struct {
	Rate     Rate
	Baseline Rate
	Field    string
	Delta    float64
}

func (r Regression) String() string {
	return fmt.Sprintf("%s/%s/%ds: %s moved %+.3f (%.3f -> %.3f)",
		r.Rate.Fixture, r.Rate.Attack, r.Rate.ClipLen, r.Field, r.Delta,
		baselineValue(r.Baseline, r.Field), baselineValue(r.Rate, r.Field))
}

func baselineValue(r Rate, field string) float64 {
	if field == "fpr" {
		return r.FPR
	}
	return r.TPR
}

// CompareToBaseline reports cells that moved more than margin in the wrong
// direction. Detection is statistical, so a small wobble is expected and only a
// sustained drop is a regression.
//
// Collusion rows are skipped: v1 is documented not to resist collusion, and
// gating the build on a number nobody claims would be theatre.
func CompareToBaseline(current, baseline Report, margin float64) []Regression {
	var out []Regression
	for _, rate := range current.Rates {
		if rate.Collusion {
			continue
		}
		base, ok := baseline.Lookup(rate.Fixture, rate.Attack, rate.ClipLen)
		if !ok {
			continue
		}
		if drop := base.TPR - rate.TPR; drop > margin {
			out = append(out, Regression{Rate: rate, Baseline: base, Field: "tpr", Delta: -drop})
		}
		if rise := rate.FPR - base.FPR; rise > margin {
			out = append(out, Regression{Rate: rate, Baseline: base, Field: "fpr", Delta: rise})
		}
	}
	return out
}

// Markdown renders the table that belongs in the README, because it is the
// honest answer to the first question anyone asks.
func (r Report) Markdown() string {
	out := fmt.Sprintf("Confidence threshold: %.2f\n\n", r.Threshold)
	out += "| fixture | attack | clip | trials | TPR | FPR | mean confidence | mean null |\n"
	out += "|---|---|---|---|---|---|---|---|\n"
	for _, rate := range r.Rates {
		name := rate.Attack
		if rate.Collusion {
			name += " *(expected failure)*"
		}
		out += fmt.Sprintf("| %s | %s | %ds | %d | %.2f | %.3f | %.3f | %.3f |\n",
			rate.Fixture, name, rate.ClipLen, rate.Trials,
			rate.TPR, rate.FPR, rate.MeanConfidence, rate.MeanNull)
	}
	return out
}

// Run is implemented by the harness command; the interface keeps the
// measurement policy here and the orchestration in cmd.
type Runner interface {
	Run(ctx context.Context) (Report, error)
}
