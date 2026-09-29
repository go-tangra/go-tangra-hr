// Package events publishes hr state changes to the shared platform event bus
// (platform:events:<tenant>, contracts/hr-api.md "Events published") so the
// UI refreshes lists, the "To review" badge and the calendar. Payloads carry
// ids, status codes and dates only — never reasons, notes, names or e-mail
// addresses (SR-007). Publishing is best effort and never fails the caller.
package events

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/requests"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

// Event types published to platform:events:<tenant>.
const (
	RequestChanged  = "hr.request.changed"
	ReviewChanged   = "hr.review.changed"
	CalendarChanged = "hr.calendar.changed"
)

// Types lists every event type the module publishes.
var Types = []string{RequestChanged, ReviewChanged, CalendarChanged}

// Publisher emits an event to a tenant's stream, to every subscriber (users
// nil) or to the listed user ids only.
type Publisher interface {
	Publish(ctx context.Context, tenantID string, users []string, eventType string, payload any)
}

// HubPublisher publishes through the stream hub (nil hub is a no-op).
type HubPublisher struct{ Hub *stream.Hub }

// Publish is best effort and never fails the caller's operation.
func (p HubPublisher) Publish(ctx context.Context, tenantID string, users []string, eventType string, payload any) {
	if p.Hub == nil || tenantID == "" {
		return
	}
	_, _ = p.Hub.PublishID(ctx, tenantID, users, len(users) == 0, eventType, payload, true)
}

// Emitter implements requests.Events. The zero value (nil Pub) is a no-op.
type Emitter struct{ Pub Publisher }

var _ requests.Events = Emitter{}

func targets(users []string) []string {
	out := slices.Compact(slices.Sorted(slices.Values(users)))
	return slices.DeleteFunc(out, func(u string) bool { return u == "" })
}

// RequestChanged tells the requester and approvers a request changed.
func (e Emitter) RequestChanged(ctx context.Context, tenantID, requestID, status string, users []string) {
	if t := targets(users); e.Pub != nil && len(t) > 0 {
		e.Pub.Publish(ctx, tenantID, t, RequestChanged, map[string]string{"request_id": requestID, "status": status})
	}
}

// ReviewChanged tells approvers their "To review" list changed.
func (e Emitter) ReviewChanged(ctx context.Context, tenantID string, users []string) {
	if t := targets(users); e.Pub != nil && len(t) > 0 {
		e.Pub.Publish(ctx, tenantID, t, ReviewChanged, map[string]string{})
	}
}

// CalendarChanged tells everyone of the tenant that a date range changed.
func (e Emitter) CalendarChanged(ctx context.Context, tenantID string, from, to time.Time) {
	if e.Pub != nil {
		e.Pub.Publish(ctx, tenantID, nil, CalendarChanged, map[string]string{"from": from.Format("2006-01-02"), "to": to.Format("2006-01-02")})
	}
}

// Recorded is one captured event (Recorder).
type Recorded struct {
	TenantID string
	Users    []string
	Type     string
	Payload  any
}

// Recorder is an in-memory Publisher for tests.
type Recorder struct {
	mu     sync.Mutex
	events []Recorded
}

// Publish implements Publisher.
func (r *Recorder) Publish(_ context.Context, tenantID string, users []string, eventType string, p any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, Recorded{TenantID: tenantID, Users: slices.Clone(users), Type: eventType, Payload: p})
}

// Events returns a copy of the captured events.
func (r *Recorder) Events() []Recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

var (
	_ Publisher = HubPublisher{}
	_ Publisher = (*Recorder)(nil)
)
