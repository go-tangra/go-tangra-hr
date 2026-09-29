package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-notification/sdk/v4/pkg/notifyclient"

	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-hr/v4/internal/people"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

var ctx = context.Background()

type sender struct {
	sent []string
	res  map[string]notifyclient.Result
	err  error
}

func (s *sender) SendKey(_ context.Context, _, key, to string, vars map[string]string, _ string) (notifyclient.Result, error) {
	if s.err != nil {
		return notifyclient.Result{}, s.err
	}
	if r, ok := s.res[to]; ok {
		return r, nil
	}
	s.sent = append(s.sent, key+"→"+to+":"+vars["EmployeeName"])
	return notifyclient.Result{Sent: true}, nil
}

type rec struct{ got []audit.Event }

func (r *rec) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		panic(err)
	}
	r.got = append(r.got, e)
	return nil
}

func queue(t *testing.T, m *memstore.Mem, user string, at time.Time) store.Mail {
	t.Helper()
	ml := store.Mail{ID: store.NewID(), TenantID: tn, Key: "hr.request_approved", UserID: user, Vars: map[string]string{"EmployeeName": "Maria"},
		NextAt: at, CreatedAt: at}
	if err := m.EnqueueMail(ctx, ml); err != nil {
		t.Fatal(err)
	}
	return ml
}

func TestDrain(t *testing.T) {
	m, s, r := memstore.New(), &sender{res: map[string]notifyclient.Result{
		"retry@example.org":  {Retryable: true, Reason: "channel down"},
		"refuse@example.org": {Reason: "template missing"},
	}}, &rec{}
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	dir := &people.Fake{Users: map[string][]people.Contact{tn: {
		{UserID: "maria", Email: "maria@example.org"}, {UserID: "retry", Email: "retry@example.org"},
		{UserID: "refuse", Email: "refuse@example.org"}, {UserID: "nomail"},
	}}}
	w := &Worker{Store: m, Contacts: dir, Sender: s, Audit: r, Now: func() time.Time { return now }, MaxAttempts: 3}
	queue(t, m, "maria", now.Add(-time.Minute))
	queue(t, m, "retry", now.Add(-time.Minute))
	queue(t, m, "refuse", now.Add(-time.Minute))
	queue(t, m, "nomail", now.Add(-time.Minute))
	queue(t, m, "gone", now.Add(-time.Minute))
	queue(t, m, "maria", now.Add(time.Hour)) // not due
	n, err := w.Drain(ctx)
	if err != nil || n != 1 || len(s.sent) != 1 || s.sent[0] != "hr.request_approved→maria@example.org:Maria" {
		t.Fatalf("drain: %d %v %v", n, err, s.sent)
	}
	left := m.Mail()
	if len(left) != 2 {
		t.Fatalf("left: %+v", left)
	}
	var retried store.Mail
	for _, l := range left {
		if l.UserID == "retry" {
			retried = l
		}
	}
	if retried.Attempts != 1 || !retried.NextAt.Equal(now.Add(30*time.Second)) || retried.LastError != "send_failed" {
		t.Fatalf("retry: %+v", retried)
	}
	reasons := map[string]bool{}
	for _, e := range r.got {
		reasons[e.Reason] = true
	}
	if !reasons["refused"] || !reasons["contact_missing"] || len(r.got) != 3 {
		t.Fatalf("audit: %+v", r.got)
	}
	// Retries run out.
	for i := 0; i < 3; i++ {
		now = now.Add(2 * time.Hour)
		_, _ = w.Drain(ctx)
	}
	for _, l := range m.Mail() {
		if l.UserID == "retry" {
			t.Fatal("retry mail kept after max attempts")
		}
	}
	// Auth down: retried later, not dropped.
	queue(t, m, "maria", now)
	dir.Err = errors.New("auth down")
	if n, _ := w.Drain(ctx); n != 0 {
		t.Fatal("sent without contacts")
	}
	dir.Err = nil
	for _, l := range m.Mail() {
		if l.UserID == "maria" && l.LastError != "contacts_unavailable" {
			t.Fatalf("contacts retry: %+v", l)
		}
	}
	// Transport error retries.
	now = now.Add(time.Hour)
	s.err = errors.New("unreachable")
	_, _ = w.Drain(ctx)
	s.err = nil
	m.SetErr(errors.New("db"))
	if _, err := w.Drain(ctx); err == nil {
		t.Fatal("store error")
	}
	m.SetErr(nil)
	now = now.Add(time.Hour)
	m.Fail("DeleteMailSystem", errors.New("db"))
	if _, err := w.Drain(ctx); err == nil {
		t.Fatal("delete error after send")
	}
	m.Fail("", nil)
}

func TestRunAndDefaults(t *testing.T) {
	m := memstore.New()
	w := &Worker{Store: m, Contacts: &people.Fake{}, Sender: &sender{}, Interval: 5 * time.Millisecond}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	m.SetErr(errors.New("db"))
	if err := w.Run(cctx); err != nil {
		t.Fatal(err)
	}
	d := &Worker{}
	d.defaults()
	if d.Interval != 2*time.Second || d.Batch != 50 || d.MaxAttempts != 5 || d.Now == nil || d.Log == nil {
		t.Fatal("defaults")
	}
	if backoff(1) != 30*time.Second || backoff(2) != 2*time.Minute || backoff(10) != time.Hour {
		t.Fatal("backoff")
	}
	// retry/drop store errors are logged only.
	m.SetErr(errors.New("db"))
	w.defaults()
	w.retry(ctx, store.Mail{ID: "x", TenantID: tn, Key: "hr.x"}, "r")
	w.MaxAttempts = 1
	w.retry(ctx, store.Mail{ID: "x", TenantID: tn, Key: "hr.x"}, "r")
}
