package telemetry

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instruments are ynf's metrics, made from the registry's names.
type instruments struct {
	runs    metric.Int64Counter
	tokens  metric.Int64Counter
	cost    metric.Float64Counter
	expired metric.Int64Counter
}

func newInstruments(m metric.Meter) instruments {
	var i instruments
	i.runs, _ = m.Int64Counter(MetricRunCount, metric.WithUnit("{run}"), metric.WithDescription("Runs, by outcome, lane and model."))
	i.tokens, _ = m.Int64Counter(MetricRunTokens, metric.WithUnit("{token}"), metric.WithDescription("Tokens runs used, by model and kind."))
	i.cost, _ = m.Float64Counter(MetricRunCost, metric.WithUnit("USD"), metric.WithDescription("What runs cost, by model, as the runner reports it."))
	i.expired, _ = m.Int64Counter(MetricLeaseExpired, metric.WithUnit("{lease}"), metric.WithDescription("Leases found expired when a step claimed the item."))
	return i
}

// limiter holds a metric attribute to the limit the registry declares for it: past that many
// distinct values, a new one is counted as "other", so a mistake can never make a series per item.
type limiter struct {
	mu   sync.Mutex
	seen map[string]map[string]struct{}
}

func (l *limiter) value(name, v string) string {
	limit := CardinalityLimits[name]
	if limit == 0 {
		return v
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]map[string]struct{}{}
	}
	s := l.seen[name]
	if s == nil {
		s = map[string]struct{}{}
		l.seen[name] = s
	}
	if _, ok := s[v]; !ok {
		if len(s) >= limit {
			return "other"
		}
		s[v] = struct{}{}
	}
	return v
}

// Usage is what a run spent, as its runner reports it.
type Usage struct {
	InputTokens, OutputTokens, CacheReadTokens int
	CostUSD                                    float64
}

// RunFinished counts a finished run: by outcome, lane and model, with what it spent by model.
// The attributes are the low-cardinality ones the registry names, never an item key or a run id.
func (t *T) RunFinished(ctx context.Context, outcome, lane, model string, u Usage) {
	if t == nil {
		return
	}
	if model == "" {
		model = "unknown"
	}
	model = t.limiter.value(AttrGenAiResponseModel, Scrub(model))
	lane = t.limiter.value(AttrLane, lane)
	outcome = t.limiter.value(AttrOutcome, outcome)
	m := t.cur.Load().m
	m.runs.Add(ctx, 1, metric.WithAttributes(
		attribute.String(AttrOutcome, outcome), attribute.String(AttrLane, lane), attribute.String(AttrGenAiResponseModel, model)))
	for typ, n := range map[string]int{TokenTypeInput: u.InputTokens, TokenTypeOutput: u.OutputTokens, TokenTypeCacheRead: u.CacheReadTokens} {
		if n > 0 {
			m.tokens.Add(ctx, int64(n), metric.WithAttributes(
				attribute.String(AttrGenAiResponseModel, model), attribute.String(AttrTokenType, typ)))
		}
	}
	if u.CostUSD > 0 {
		m.cost.Add(ctx, u.CostUSD, metric.WithAttributes(attribute.String(AttrGenAiResponseModel, model)))
	}
}

// LeaseExpired counts a lease found expired when a step claimed its item: its holder died or
// stalled.
func (t *T) LeaseExpired(ctx context.Context, lane string) {
	if t == nil {
		return
	}
	t.cur.Load().m.expired.Add(ctx, 1, metric.WithAttributes(attribute.String(AttrLane, t.limiter.value(AttrLane, lane))))
}
