package audit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

type memStore struct {
	mu   sync.Mutex
	rows []store.AuditRow
	err  error
}

func (m *memStore) AppendAudit(_ context.Context, r store.AuditRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.rows = append(m.rows, r)
	return nil
}

func (m *memStore) all() []store.AuditRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.AuditRow(nil), m.rows...)
}

func ok(t EventType) Event {
	return Event{TenantID: tn, EventType: t, ActorKind: ActorUser, ActorID: "u1", SubjectKind: SubjectRequest, SubjectID: "r1", Outcome: OutcomeOK}
}

func TestVocabulary(t *testing.T) {
	for _, want := range []string{"absence_type.create", "pool.delete", "allowance.update", "request.create",
		"request.approve", "request.signing_start", "request.signing_outcome", "request.overdraw", "department.move",
		"department.members", "holiday.import", "carryover.run", "member.deactivated", "mail.failed", "backup.export",
		"task.carryover", "task.reconcile", "task.sync", "access.refused"} {
		if !Known(want) {
			t.Errorf("vocabulary lacks %s", want)
		}
	}
	if len(Vocabulary) != 38 || Known("task.explode") {
		t.Fatalf("vocabulary size %d / unknown event accepted", len(Vocabulary))
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(ok(AbsenceTypeCreate)); err != nil {
		t.Fatal(err)
	}
	bad := []func(*Event){
		func(e *Event) { e.EventType = "nope" },
		func(e *Event) { e.TenantID = "" },
		func(e *Event) { e.ActorKind = "robot" },
		func(e *Event) { e.SubjectKind = "planet" },
		func(e *Event) { e.Outcome = "maybe" },
	}
	for i, mut := range bad {
		e := ok(AbsenceTypeCreate)
		mut(&e)
		if Validate(e) == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	for _, k := range []string{ActorService, ActorSystem} {
		e := ok(TaskReconcile)
		e.ActorKind, e.SubjectKind, e.TenantID = k, SubjectSystem, NilTenant
		if err := Validate(e); err != nil {
			t.Errorf("actor %s: %v", k, err)
		}
	}
	for _, sk := range []string{SubjectAbsenceType, SubjectPool, SubjectAllowance, SubjectDepartment, SubjectHoliday, SubjectMember, SubjectTenant, SubjectBackup, SubjectSystem} {
		e := ok(RequestApprove)
		e.SubjectKind = sk
		if err := Validate(e); err != nil {
			t.Errorf("subject %s: %v", sk, err)
		}
	}
	for _, o := range []string{OutcomeRefused, OutcomeError} {
		e := ok(AccessRefused)
		e.Outcome = o
		if err := Validate(e); err != nil {
			t.Errorf("outcome %s: %v", o, err)
		}
	}
}

func TestRedaction(t *testing.T) {
	d := Redact(map[string]any{
		"reason": "sick", "review_notes": "no", "notes": "n", "display_name": "Maria", "user_name": "m", "email": "a@b",
		"recipients": []any{"a@b"}, "vars": map[string]any{"X": 1}, "body": "b", "content": "c", "client_secret": "s",
		"token": "t", "password": "p", "credential": "c",
		"status_to": "approved", "days": 35, "long": strings.Repeat("x", 400),
		"nested": map[string]any{"token": "t", "keep": "v", "list": []any{map[string]any{"password": "p", "n": 1}, "s"}},
	})
	for _, k := range []string{"reason", "review_notes", "notes", "display_name", "user_name", "email", "recipients", "vars",
		"body", "content", "client_secret", "token", "password", "credential"} {
		if _, found := d[k]; found {
			t.Errorf("%s leaked", k)
		}
	}
	if d["status_to"] != "approved" || d["days"] != 35 || len(d["long"].(string)) != 256 {
		t.Fatalf("kept values: %+v", d)
	}
	nested := d["nested"].(map[string]any)
	if _, found := nested["token"]; found || nested["keep"] != "v" {
		t.Fatalf("nested: %+v", nested)
	}
	item := nested["list"].([]any)[0].(map[string]any)
	if _, found := item["password"]; found || item["n"] != 1 {
		t.Fatalf("list item: %+v", item)
	}
}

func TestWriterRecordsAndFlushes(t *testing.T) {
	st := &memStore{}
	w := NewWriter(st, nil)
	e := ok(AbsenceTypeUpdate)
	e.Reason = strings.Repeat("r", 300)
	e.Details = map[string]any{"fields": []any{"color"}, "to": "active", "reason": "never"}
	if err := w.Record(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := w.Record(context.Background(), Event{EventType: "bogus"}); err == nil {
		t.Fatal("invalid event queued")
	}
	w.Flush(context.Background())
	rows := st.all()
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	if r.Action != "absence_type.update" || r.ID == "" || r.At.IsZero() || len(r.Reason) != 256 || r.Detail["to"] != "active" {
		t.Fatalf("row = %+v", r)
	}
	if _, found := r.Detail["reason"]; found {
		t.Fatal("reason in audit detail")
	}
	w.Close()
	w.Close() // idempotent
	if err := w.Record(context.Background(), ok(AbsenceTypeCreate)); err == nil {
		t.Fatal("record after close")
	}
	w.Flush(context.Background()) // no-op after close
	if w.Dropped() != 1 {
		t.Fatalf("dropped = %d", w.Dropped())
	}
}

func TestWriterErrorsAndBackpressure(t *testing.T) {
	st := &memStore{err: errors.New("db down")}
	var mu sync.Mutex
	var errs []error
	w := newWriter(st, func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() }, 1)
	// not started: the second record overflows the queue of 1
	_ = w.Record(context.Background(), ok(AbsenceTypeCreate))
	_ = w.Record(context.Background(), ok(AbsenceTypeCreate))
	if w.Dropped() != 1 {
		t.Fatalf("dropped = %d", w.Dropped())
	}
	w.tick = time.Millisecond
	w.start()
	w.Flush(context.Background())
	time.Sleep(5 * time.Millisecond)
	w.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(errs) < 2 {
		t.Fatalf("errors = %v", errs)
	}
	nw := newWriter(st, nil, 1)
	nw.onError(errors.New("ignored")) // default handler is a no-op
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nw.Flush(ctx) // not started + cancelled: returns
}

type recorder struct{ got []Event }

func (r *recorder) Record(_ context.Context, e Event) error { r.got = append(r.got, e); return nil }

func TestEmitAndActorOf(t *testing.T) {
	Emit(context.Background(), nil, ok(AbsenceTypeCreate)) // nil recorder: no-op
	r := &recorder{}
	Emit(context.Background(), r, ok(AbsenceTypeCreate))
	if len(r.got) != 1 {
		t.Fatal("emit")
	}
	for in, want := range map[string]string{"user": ActorUser, "service": ActorService, "system": ActorSystem, "": ActorUser} {
		if ActorOf(in) != want {
			t.Errorf("ActorOf(%q) = %q", in, ActorOf(in))
		}
	}
}
