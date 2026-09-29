package consumer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/requests"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

var ctx = context.Background()

type applier struct {
	mu      sync.Mutex
	got     []requests.Outcome
	applied int
	err     error
}

func (a *applier) ApplyOutcome(_ context.Context, tx repo.Store, o requests.Outcome) (*store.Request, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return nil, a.err
	}
	first, err := tx.RecordOutcome(ctx, store.SigningOutcome{TenantID: o.TenantID, SubmissionID: o.SubmissionID, Outcome: o.Outcome,
		Source: o.Source, EventID: o.EventID, Result: store.ResultIgnoredUnknown})
	if err != nil || !first {
		return nil, err
	}
	a.got = append(a.got, o)
	return &store.Request{ID: "r"}, nil
}

func (a *applier) Applied(context.Context, *store.Request) { a.mu.Lock(); a.applied++; a.mu.Unlock() }

func (a *applier) outcomes() []requests.Outcome {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]requests.Outcome(nil), a.got...)
}

func publish(t *testing.T, mem *stream.Memory, typ, data string) string {
	t.Helper()
	id, err := mem.XAdd(ctx, stream.Key(tn), map[string]string{"to": "*", "type": typ, "data": data}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func setup(t *testing.T) (*memstore.Mem, *stream.Memory, *applier, *Consumer) {
	t.Helper()
	m, mem, a := memstore.New(), stream.NewMemory(), &applier{}
	if err := m.EnsureTenant(ctx, tn); err != nil {
		t.Fatal(err)
	}
	return m, mem, a, &Consumer{Store: m, Stream: mem, Apply: a, Block: 10 * time.Millisecond, Retry: 5 * time.Millisecond,
		Refresh: 20 * time.Millisecond}
}

func cursor(t *testing.T, m *memstore.Mem) string {
	t.Helper()
	ts, _ := m.TenantsSystem(ctx)
	return ts[0].StreamCursor
}

func TestStepPersistsCursorAndSkips(t *testing.T) {
	m, mem, a, c := setup(t)
	publish(t, mem, "hr.calendar.changed", `{}`)
	id1 := publish(t, mem, TypeCompleted, `{"submission_id":"`+sub+`"}`)
	id2 := publish(t, mem, "signing.inbox", `{}`)
	next, err := c.Step(ctx, tn, "0-0")
	if err != nil || next != id2 || cursor(t, m) != id2 || len(a.outcomes()) != 1 || a.outcomes()[0].EventID != id1 || a.applied != 1 {
		t.Fatalf("step: %s %v cursor=%s outcomes=%+v", next, err, cursor(t, m), a.outcomes())
	}
	// Nothing new: cursor unchanged.
	if again, err := c.Step(ctx, tn, next); err != nil || again != next {
		t.Fatal("idle step")
	}
	// Apply failure: cursor stays before the failing event (after skipped ones).
	skip := publish(t, mem, "x.y", `{}`)
	publish(t, mem, TypeExpired, `{"submission_id":"`+sub+`"}`)
	a.err = errors.New("db down")
	got, err := c.Step(ctx, tn, next)
	if err == nil || got != skip || cursor(t, m) != skip {
		t.Fatalf("failure: %s %v %s", got, err, cursor(t, m))
	}
	a.err = errors.New("db down")
	if got, err := c.Step(ctx, tn, skip); err == nil || got != skip {
		t.Fatal("failure without skipped entries keeps the cursor")
	}
	a.err = nil
	if _, err := c.Step(ctx, tn, skip); err != nil || len(a.outcomes()) != 1 {
		t.Fatal("replayed outcome of the same submission is recorded once")
	}
	mem.SetErr(errors.New("valkey down"))
	if _, err := c.Step(ctx, tn, skip); err == nil {
		t.Fatal("read error")
	}
	mem.SetErr(nil)
	publish(t, mem, "x.y", `{}`)
	m.Fail("SetCursor", errors.New("db"))
	if _, err := c.Step(ctx, tn, skip); err == nil {
		t.Fatal("cursor write error")
	}
	m.Fail("", nil)
}

func TestRunReplaysAfterRestart(t *testing.T) {
	m, mem, a, c := setup(t)
	old := publish(t, mem, TypeCompleted, `{"submission_id":"0190f7c2-6a3e-7c1a-9b2e-000000000001"}`) // before hr ever started
	_ = old
	run := func(c *Consumer) (stop func()) {
		cctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = c.Run(cctx); close(done) }()
		return func() { cancel(); <-done }
	}
	stop := run(c)
	waitFor(t, func() bool { return cursor(t, m) != "" })
	publish(t, mem, TypeCompleted, `{"submission_id":"0190f7c2-6a3e-7c1a-9b2e-000000000002"}`)
	waitFor(t, func() bool { return len(a.outcomes()) == 1 })
	stop()
	if a.outcomes()[0].SubmissionID != "0190f7c2-6a3e-7c1a-9b2e-000000000002" {
		t.Fatalf("first start begins at the tail: %+v", a.outcomes())
	}
	// Published while hr is down; a new consumer resumes from the persisted cursor.
	publish(t, mem, TypeExpired, `{"submission_id":"0190f7c2-6a3e-7c1a-9b2e-000000000003"}`)
	c2 := &Consumer{Store: m, Stream: mem, Apply: a, Block: 10 * time.Millisecond, Retry: 5 * time.Millisecond, Refresh: 20 * time.Millisecond}
	stop = run(c2)
	waitFor(t, func() bool { return len(a.outcomes()) == 2 })
	// A tenant registered later is picked up at the next refresh.
	other := "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
	_ = m.EnsureTenant(ctx, other)
	waitFor(t, func() bool {
		ts, _ := m.TenantsSystem(ctx)
		for _, s := range ts {
			if s.TenantID == other && s.StreamCursor != "" {
				return true
			}
		}
		return false
	})
	stop()
}

func TestRunSurvivesErrors(t *testing.T) {
	m, mem, _, c := setup(t)
	m.Fail("TenantsSystem", errors.New("db"))
	cctx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	_ = c.Run(cctx) // registry errors are logged and retried
	m.Fail("", nil)
	mem.SetErr(errors.New("valkey down"))
	cctx2, cancel2 := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel2()
	_ = c.Run(cctx2) // cannot read the tail: retried until the context ends
	if cursor(t, m) != "" {
		t.Fatal("no cursor without the tail")
	}
	mem.SetErr(nil)
	publish(t, mem, "x.y", `{}`)
	m.Fail("SetCursor", errors.New("db"))
	c3 := &Consumer{Store: m, Stream: mem, Apply: &applier{}, Block: 5 * time.Millisecond, Retry: 5 * time.Millisecond}
	cctx3, cancel3 := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel3()
	_ = c3.Run(cctx3)
	m.Fail("", nil)
	// Steps that fail are retried.
	c4 := &Consumer{Store: m, Stream: mem, Apply: &applier{err: errors.New("x")}, Block: 5 * time.Millisecond, Retry: 5 * time.Millisecond}
	_ = m.SetCursor(ctx, tn, "0-0", time.Now())
	publish(t, mem, TypeCompleted, `{"submission_id":"`+sub+`"}`)
	cctx4, cancel4 := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel4()
	_ = c4.Run(cctx4)
	var d Consumer
	d.defaults()
	if d.Block != 2*time.Second || d.Batch != 100 || d.Refresh != 30*time.Second || d.Retry != time.Second || d.Log == nil {
		t.Fatal("defaults")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
