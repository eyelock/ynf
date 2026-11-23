package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
)

// TestARunTimeoutFollowsMaxWall: a max_wall longer than the engine's default is not cut short at
// the default (the clock is shortened: a one-minute ceiling stands for the hour), and the executor
// waits a margin past it so the runner's own budget stop comes first.
func TestARunTimeoutFollowsMaxWall(t *testing.T) {
	ceiling := time.Minute
	for _, c := range []struct {
		name string
		b    *policy.Budgets
		want time.Duration
	}{
		{"no budgets", nil, ceiling},
		{"no max_wall", &policy.Budgets{}, ceiling},
		{"shorter than the ceiling", &policy.Budgets{MaxWall: "10s"}, 10*time.Second + wallMargin},
		{"longer than the ceiling", &policy.Budgets{MaxWall: "2h"}, 2*time.Hour + wallMargin},
		{"not a duration", &policy.Budgets{MaxWall: "soon"}, ceiling},
	} {
		if got := runTimeout(ceiling, c.b); got != c.want {
			t.Errorf("%s: timeout %s, want %s", c.name, got, c.want)
		}
	}
	if got := runTimeout(ceiling, &policy.Budgets{MaxWall: "2m"}); got <= 2*time.Minute {
		t.Errorf("a 2m max_wall run is killed at %s, before its budget is spent", got)
	}
}

// TestADeadlineIsRecordedAsBudget: a run the executor stopped at its deadline is a budget, not an
// error; any other executor failure stays an error.
func TestADeadlineIsRecordedAsBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if got := executorOutcome(fmt.Errorf("ynh: %w", ctx.Err())); got != runner.Budget {
		t.Errorf("deadline recorded as %q, want %q", got, runner.Budget)
	}
	if got := executorOutcome(errors.New("docker: no such image")); got != runner.Error {
		t.Errorf("other failure recorded as %q, want %q", got, runner.Error)
	}
	if got := executorOutcome(context.Canceled); got != runner.Error {
		t.Errorf("cancellation recorded as %q, want %q", got, runner.Error)
	}
}
