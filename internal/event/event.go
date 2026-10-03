// Package event is ynf's one event envelope (ADR-002): every adapter, push or pull, and ynf itself
// emits a CloudEvents 1.0 event with a type from ynf's closed vocabulary.
package event

import (
	"fmt"
	"time"
)

// Types are ynf's closed vocabulary. Provider event names never reach the decider.
const (
	TicketMatched = "ynf.ticket.matched" // a lane's search found the ticket
	RunFinished   = "ynf.run.finished"   // a runner finished; data carries the outcome
	TimerDue      = "ynf.timer.due"      // the item's next_due passed
	ActionDone    = "ynf.action.done"    // a forge action completed; data carries what it produced
	ForgeChanged  = "ynf.forge.changed"  // a webhook says something about the item changed; facts are re-probed
)

// Event is a CloudEvents 1.0 envelope.
type Event struct {
	SpecVersion string         `json:"specversion"`
	ID          string         `json:"id"`
	Source      string         `json:"source"`
	Type        string         `json:"type"`
	Subject     string         `json:"subject"`
	Time        time.Time      `json:"time"`
	Data        map[string]any `json:"data,omitempty"`
}

// New returns an event with the spec version set.
func New(id, source, typ, subject string, at time.Time, data map[string]any) Event {
	return Event{SpecVersion: "1.0", ID: id, Source: source, Type: typ, Subject: subject, Time: at.UTC(), Data: data}
}

// String reads well in logs.
func (e Event) String() string { return fmt.Sprintf("%s %s", e.Type, e.Subject) }

// Str reads a string field from Data, or "".
func (e Event) Str(key string) string {
	if v, ok := e.Data[key].(string); ok {
		return v
	}
	return ""
}

// Bool reads a bool field from Data, or false.
func (e Event) Bool(key string) bool {
	v, _ := e.Data[key].(bool)
	return v
}
