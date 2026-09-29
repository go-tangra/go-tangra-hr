package requests

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/routing"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

func (f *fx) apply(t *testing.T, o Outcome) *store.Request {
	t.Helper()
	var got *store.Request
	if err := f.m.Tx(ctx, tn, func(tx repo.Store) error {
		var err error
		got, err = f.s.ApplyOutcome(ctx, tx, o)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.s.Applied(ctx, got)
	return got
}

func TestSigningWorkflow(t *testing.T) {
	f := setup(t)
	r, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.signed.ID, Start: d(2026, 8, 3), End: d(2026, 8, 4)})
	a, err := f.s.Approve(ctx, petar, r.ID, "ok")
	if err != nil || a.Status != store.StatusAwaitingSigning || a.SubmissionID == "" || a.SigningAttempt != 1 || a.SigningStartedAt == nil {
		t.Fatalf("approve → awaiting: %+v %v", a, err)
	}
	st := f.signer.started[0]
	if st.ApproverID != "petar" || st.EmployeeName != "Maria" || st.ApproverName != "Petar" || st.DepartmentName != "Platform" ||
		st.IdempotencyKey != r.ID+":1" {
		t.Fatalf("start input: %+v", st)
	}
	if f.m.Mail() != nil && f.mailTo(KeyApproved, "maria") != 0 {
		t.Fatal("no approval mail before signing completes")
	}
	// Nothing charged yet.
	var signedAllowance store.Allowance
	list, _, _ := f.m.ListAllowances(ctx, tn, repo.AllowanceFilter{AbsenceTypeID: f.signed.ID, All: true})
	signedAllowance = list[0]
	if signedAllowance.Used != 0 {
		t.Fatal("charged before completion")
	}

	// Declined → back to pending, mails, re-approve starts attempt 2.
	got := f.apply(t, Outcome{TenantID: tn, SubmissionID: a.SubmissionID, Outcome: store.OutcomeDeclined, Source: store.SourceEvent, EventID: "1-0"})
	if got == nil || got.Status != store.StatusPending || got.SigningNote != store.OutcomeDeclined || got.SubmissionID != "" || got.ReviewedBy != "" {
		t.Fatalf("declined: %+v", got)
	}
	if f.mailTo(KeyFailed, "maria") != 1 || f.mailTo(KeyFailed, "petar") != 1 {
		t.Fatal("failure mails")
	}
	// A repeat of the same outcome is a no-op.
	if again := f.apply(t, Outcome{TenantID: tn, SubmissionID: a.SubmissionID, Outcome: store.OutcomeDeclined, Source: store.SourceReconcile}); again != nil {
		t.Fatal("outcome applied twice")
	}
	a2, err := f.s.Approve(ctx, petar, r.ID, "")
	if err != nil || a2.SigningAttempt != 2 || f.signer.started[1].IdempotencyKey != r.ID+":2" {
		t.Fatalf("re-approve: %+v %v", a2, err)
	}
	// Completed → approved and charged once.
	done := f.apply(t, Outcome{TenantID: tn, SubmissionID: a2.SubmissionID, Outcome: store.OutcomeCompleted, Source: store.SourceEvent})
	if done == nil || done.Status != store.StatusApproved || f.allowance(t, signedAllowance.ID).Used != 20 || f.mailTo(KeyApproved, "maria") != 1 {
		t.Fatalf("completed: %+v", done)
	}
	if again := f.apply(t, Outcome{TenantID: tn, SubmissionID: a2.SubmissionID, Outcome: store.OutcomeCompleted, Source: store.SourceReconcile}); again != nil ||
		f.allowance(t, signedAllowance.ID).Used != 20 {
		t.Fatal("charged twice")
	}
	// Unknown submissions are recorded and ignored.
	if f.apply(t, Outcome{TenantID: tn, SubmissionID: store.NewID(), Outcome: store.OutcomeCompleted, Source: store.SourceEvent}) != nil {
		t.Fatal("unknown applied")
	}
	results := map[string]int{}
	for _, o := range f.m.Outcomes() {
		results[o.Result]++
	}
	if results[store.ResultApplied] != 2 || results[store.ResultIgnoredUnknown] != 1 {
		t.Fatalf("outcome records: %v", results)
	}

	// Download: participants and HR admins only.
	rc, name, err := f.s.SignedDocument(ctx, maria, r.ID)
	if err != nil || name != "leave.pdf" {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	if string(b) != "%PDF" {
		t.Fatal("document bytes")
	}
	if _, _, err := f.s.SignedDocument(ctx, hana, r.ID); err != nil {
		t.Fatal("hr admin download")
	}
	_, _, err = f.s.SignedDocument(ctx, vera, r.ID)
	is(t, err, apperr.NotFound, "viewer download")
	_, _, err = f.s.SignedDocument(ctx, ana, r.ID)
	is(t, err, apperr.NotFound, "colleague download")
	f.signer.docErr = errors.New("unreachable")
	_, _, err = f.s.SignedDocument(ctx, maria, r.ID)
	is(t, err, apperr.SigningUnavailable, "signing down")
	f.signer.docErr = apperr.NotSigned
	_, _, err = f.s.SignedDocument(ctx, maria, r.ID)
	is(t, err, apperr.NotSigned, "refusal passthrough")
	f.signer.docErr = nil
	plain, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 1), End: d(2026, 9, 1)})
	_, _, err = f.s.SignedDocument(ctx, maria, plain.ID)
	is(t, err, apperr.NotSigned, "no document")
	_, _, err = f.s.SignedDocument(ctx, maria, "0190f7c2-6a3e-7c1a-9b2e-000000000000")
	is(t, err, apperr.NotFound, "missing request")

	// Revoke keeps the document (no delete), refunds.
	if _, err := f.s.Revoke(ctx, petar, r.ID, "x"); err != nil || len(f.signer.deleted) != 0 || f.allowance(t, signedAllowance.ID).Used != 0 {
		t.Fatalf("revoke signed: %v", err)
	}
}

func TestSigningFailuresAndCancellation(t *testing.T) {
	f := setup(t)
	r, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.signed.ID, Start: d(2026, 8, 3), End: d(2026, 8, 4)})

	f.signer.startErr = errors.New("unreachable")
	_, err := f.s.Approve(ctx, petar, r.ID, "")
	is(t, err, apperr.SigningUnavailable, "signing down")
	f.signer.startErr = apperr.SigningTemplateInvalid
	_, err = f.s.Approve(ctx, petar, r.ID, "")
	is(t, err, apperr.SigningTemplateInvalid, "template refusal")
	f.signer.startErr = nil
	if cur, _ := f.m.GetRequest(ctx, tn, r.ID); cur.Status != store.StatusPending {
		t.Fatal("stays pending")
	}
	f.people.inactive["maria"] = true
	_, err = f.s.Approve(ctx, petar, r.ID, "")
	is(t, err, apperr.SignerInactive, "inactive employee")
	f.people.inactive["maria"] = false
	f.people.err = errors.New("down")
	_, err = f.s.Approve(ctx, petar, r.ID, "")
	if err == nil {
		t.Fatal("people error")
	}
	f.people.err = nil
	// Recording failure cancels the created submission.
	f.m.Fail("UpdateRequest", errors.New("db"))
	if _, err := f.s.Approve(ctx, petar, r.ID, ""); err == nil || len(f.signer.cancelled) != 1 || !strings.HasSuffix(f.signer.cancelled[0], "/"+CancelHR) {
		t.Fatalf("orphan cancelled: %v %v", err, f.signer.cancelled)
	}
	f.m.Fail("", nil)
	noSigner := *f.s
	noSigner.d.Signing = nil
	_, err = noSigner.Approve(ctx, petar, r.ID, "")
	is(t, err, apperr.SigningUnavailable, "no signing module")

	// Reject while awaiting cancels the submission; cancel by owner too.
	a, _ := f.s.Approve(ctx, petar, r.ID, "")
	if _, err := f.s.Reject(ctx, petar, r.ID, "no"); err != nil {
		t.Fatal(err)
	}
	if last := f.signer.cancelled[len(f.signer.cancelled)-1]; last != a.SubmissionID+"/"+CancelRejected {
		t.Fatalf("reject cancel: %s", last)
	}
	// An outcome for a rejected request is ignored.
	if f.apply(t, Outcome{TenantID: tn, SubmissionID: a.SubmissionID, Outcome: store.OutcomeCompleted, Source: store.SourceEvent}) != nil {
		t.Fatal("applied to rejected")
	}
	if err := f.s.Delete(ctx, maria, r.ID); err != nil || len(f.signer.deleted) != 1 || f.signer.deleted[0] != a.SubmissionID {
		t.Fatalf("delete removes submission: %v", err)
	}
	r2, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.signed.ID, Start: d(2026, 8, 10), End: d(2026, 8, 10)})
	a2, _ := f.s.Approve(ctx, petar, r2.ID, "")
	if _, err := f.s.Cancel(ctx, maria, r2.ID); err != nil ||
		f.signer.cancelled[len(f.signer.cancelled)-1] != a2.SubmissionID+"/"+CancelHR {
		t.Fatalf("owner cancel: %v", err)
	}
}

func TestOverdrawOnCompletion(t *testing.T) {
	f := setup(t)
	r, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.signed.ID, Start: d(2026, 8, 3), End: d(2026, 8, 4)})
	r2, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.signed.ID, Start: d(2026, 8, 10), End: d(2026, 8, 10)})
	a, _ := f.s.Approve(ctx, petar, r.ID, "")
	a2, _ := f.s.Approve(ctx, petar, r2.ID, "")
	list, _, _ := f.m.ListAllowances(ctx, tn, repo.AllowanceFilter{AbsenceTypeID: f.signed.ID, All: true})
	_ = f.m.AddUsed(ctx, tn, list[0].ID, 95, f.now) // only 0.5 left
	done := f.apply(t, Outcome{TenantID: tn, SubmissionID: a.SubmissionID, Outcome: store.OutcomeCompleted, Source: store.SourceEvent})
	if done == nil || done.Status != store.StatusApproved || f.allowance(t, list[0].ID).Remaining() != -15 || f.mailTo(KeyOverdrawn, "hana") != 1 {
		t.Fatalf("overdraw: %+v", done)
	}
	if m := f.m.Mail(); m[len(m)-1].Vars["Remaining"] != "-1.5" || m[len(m)-1].Vars["Year"] != "2026" {
		t.Fatalf("overdraw vars: %+v", m[len(m)-1].Vars)
	}
	// Admin lookup failure still approves (audit keeps the overdraw).
	f.people.adminErr = errors.New("down")
	if got := f.apply(t, Outcome{TenantID: tn, SubmissionID: a2.SubmissionID, Outcome: store.OutcomeCompleted, Source: store.SourceEvent}); got == nil {
		t.Fatal("approved without admins")
	}
	f.people.adminErr = nil
	_ = f.m.AddUsed(ctx, tn, list[0].ID, -125, f.now)
	// Without an allowance for the year, completion fails (and is retried).
	r3, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.signed.ID, Start: d(2026, 8, 17), End: d(2026, 8, 17)})
	a3, _ := f.s.Approve(ctx, petar, r3.ID, "")
	_ = f.m.Tx(ctx, tn, func(tx repo.Store) error { return nil })
	f.m.Fail("FindAllowance", errors.New("db"))
	if err := f.m.Tx(ctx, tn, func(tx repo.Store) error {
		_, err := f.s.ApplyOutcome(ctx, tx, Outcome{TenantID: tn, SubmissionID: a3.SubmissionID, Outcome: store.OutcomeCompleted, Source: store.SourceEvent})
		return err
	}); err == nil {
		t.Fatal("charge error hidden")
	}
	f.m.Fail("RequestBySubmission", errors.New("db"))
	if err := f.m.Tx(ctx, tn, func(tx repo.Store) error {
		_, err := f.s.ApplyOutcome(ctx, tx, Outcome{TenantID: tn, SubmissionID: a3.SubmissionID, Outcome: store.OutcomeCompleted})
		return err
	}); err == nil {
		t.Fatal("lookup error hidden")
	}
	f.m.Fail("", nil)
}

func TestReconcile(t *testing.T) {
	f := setup(t)
	r1, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.signed.ID, Start: d(2026, 8, 3), End: d(2026, 8, 3)})
	r2, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.signed.ID, Start: d(2026, 8, 10), End: d(2026, 8, 10)})
	r3, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.signed.ID, Start: d(2026, 8, 17), End: d(2026, 8, 17)})
	a1, _ := f.s.Approve(ctx, petar, r1.ID, "")
	a2, _ := f.s.Approve(ctx, petar, r2.ID, "")
	_, _ = f.s.Approve(ctx, petar, r3.ID, "")
	f.signer.state[a1.SubmissionID] = store.OutcomeCompleted
	f.signer.state[a2.SubmissionID] = store.OutcomeExpired
	f.now = f.now.Add(20 * 60e9)
	checked, applied, err := f.s.Reconcile(ctx, tn, f.now.Add(-10*60e9), 100)
	if err != nil || checked != 3 || applied != 2 {
		t.Fatalf("reconcile: %d %d %v", checked, applied, err)
	}
	g1, _ := f.m.GetRequest(ctx, tn, r1.ID)
	g2, _ := f.m.GetRequest(ctx, tn, r2.ID)
	if g1.Status != store.StatusApproved || g2.Status != store.StatusPending || g2.SigningNote != store.OutcomeExpired {
		t.Fatalf("reconciled: %s %s", g1.Status, g2.Status)
	}
	if _, applied, _ := f.s.Reconcile(ctx, tn, f.now, 100); applied != 0 {
		t.Fatal("re-run applied again")
	}
	f.signer.stateErr = errors.New("down")
	if checked, applied, err := f.s.Reconcile(ctx, tn, f.now, 100); err != nil || checked != 1 || applied != 0 {
		t.Fatalf("state errors skipped: %d %d %v", checked, applied, err)
	}
	f.signer.stateErr = nil
	f.m.Fail("AwaitingSigning", errors.New("db"))
	if _, _, err := f.s.Reconcile(ctx, tn, f.now, 100); err == nil {
		t.Fatal("list error")
	}
	f.m.Fail("", nil)
	g3, _ := f.m.GetRequest(ctx, tn, r3.ID)
	f.signer.state[g3.SubmissionID] = store.OutcomeCompleted
	f.m.Fail("RecordOutcome", errors.New("db"))
	if _, _, err := f.s.Reconcile(ctx, tn, f.now, 100); err == nil {
		t.Fatal("apply error")
	}
	f.m.Fail("", nil)
	noSigner := *f.s
	noSigner.d.Signing = nil
	if c, a, err := noSigner.Reconcile(ctx, tn, f.now, 100); c != 0 || a != 0 || err != nil {
		t.Fatal("no signing: nothing to reconcile")
	}
}

func TestRerouteAndCancelOpen(t *testing.T) {
	f := setup(t)
	r, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 8, 3), End: d(2026, 8, 3)})
	rs, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.signed.ID, Start: d(2026, 8, 10), End: d(2026, 8, 10)})
	as, _ := f.s.Approve(ctx, petar, rs.ID, "")
	// Petar leaves Platform's manager seat: requests route to Ivan.
	f.tree.Departments["plat"] = routing.Department{ID: "plat", ParentID: "eng"}
	if err := f.s.Reroute(ctx, tn); err != nil {
		t.Fatal(err)
	}
	g, _ := f.m.GetRequest(ctx, tn, r.ID)
	if !slices.Equal(g.ApproverIDs, []string{"ivan"}) {
		t.Fatalf("rerouted: %v", g.ApproverIDs)
	}
	if err := f.s.Reroute(ctx, tn); err != nil {
		t.Fatal("idempotent reroute")
	}
	n, err := f.s.CancelOpen(ctx, tn, "maria")
	if err != nil || n != 2 {
		t.Fatalf("cancel open: %d %v", n, err)
	}
	if last := f.signer.cancelled[len(f.signer.cancelled)-1]; last != as.SubmissionID+"/"+CancelMemberLeft {
		t.Fatalf("member left cancel: %v", f.signer.cancelled)
	}
	if n, _ := f.s.CancelOpen(ctx, tn, "maria"); n != 0 {
		t.Fatal("nothing left")
	}
	for _, m := range []string{"ListRequests"} {
		f.m.Fail(m, errors.New("db"))
		if err := f.s.Reroute(ctx, tn); err == nil {
			t.Fatal("reroute list error")
		}
		if _, err := f.s.CancelOpen(ctx, tn, "maria"); err == nil {
			t.Fatal("cancel list error")
		}
	}
	f.m.Fail("", nil)
	f.people.adminErr = errors.New("down")
	if err := f.s.Reroute(ctx, tn); err == nil {
		t.Fatal("admins error")
	}
	f.people.adminErr = nil
	f.treeErr = errors.New("db")
	if err := f.s.Reroute(ctx, tn); err == nil {
		t.Fatal("tree error")
	}
	f.treeErr = nil
	r3, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 9, 7), End: d(2026, 9, 7)})
	f.tree.Departments["plat"] = routing.Department{ID: "plat", ParentID: "eng", ManagerID: "petar"}
	f.m.Fail("LockRequest", errors.New("db"))
	if err := f.s.Reroute(ctx, tn); err == nil {
		t.Fatal("lock error on reroute")
	}
	if _, err := f.s.CancelOpen(ctx, tn, "maria"); err == nil {
		t.Fatal("lock error on cancel")
	}
	f.m.Fail("", nil)
	_ = r3
}

func TestNewDefaultsAndHelpers(t *testing.T) {
	s := New(Deps{})
	if s.d.Limits.MaxRequestDays != 366 || s.d.Limits.MaxCalendarDays != 93 || s.d.Now == nil {
		t.Fatal("defaults")
	}
	if reasonOf(errors.New("x")) != "error" || mapErr(errors.New("x")) == nil {
		t.Fatal("helpers")
	}
	s.changed(ctx, store.Request{}, true) // nil events
	s.d.Links.PortalBaseURL = "://bad"
	if s.link("/x") != "/x" {
		t.Fatal("bad base url falls back to the path")
	}
	s.cancelSubmission(ctx, tn, "", CancelHR)
	if n := s.names(ctx, tn, nil); n == nil {
		t.Fatal("names without people")
	}
}
