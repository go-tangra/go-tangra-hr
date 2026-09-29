package requests

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

func TestCreateAndPreview(t *testing.T) {
	f := setup(t)
	in := Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 8, 3), End: d(2026, 8, 7), HalfEnd: true, Reason: " holiday "}
	p, err := f.s.Preview(ctx, maria, in)
	if err != nil || p.Days != 35 || p.Status != store.StatusPending || len(p.Approvers) != 1 || p.Approvers[0] != "petar" ||
		p.Remaining[2026] != 65 || p.Overlaps {
		t.Fatalf("preview: %+v %v", p, err)
	}
	r, err := f.s.Create(ctx, maria, in)
	if err != nil || r.Days != 35 || r.Reason != "holiday" || r.UserID != "maria" || r.ApproverIDs[0] != "petar" {
		t.Fatalf("create: %+v %v", r, err)
	}
	if f.mailTo(KeySubmitted, "petar") != 1 || f.events.changes == 0 || f.events.cal == 0 {
		t.Fatal("approver mail / events")
	}
	if m := f.m.Mail()[0]; m.Vars["EmployeeName"] != "Maria" || m.Vars["Days"] != "3.5" || m.Vars["StartDate"] != "03.08.2026" ||
		m.Vars["ReviewURL"] != "https://portal.example.org/hr/review" {
		t.Fatalf("mail vars: %+v", m.Vars)
	}
	if p, _ := f.s.Preview(ctx, maria, in); !p.Overlaps {
		t.Fatal("preview overlap")
	}
	_, err = f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 8, 7), End: d(2026, 8, 10)})
	is(t, err, apperr.Overlap, "overlap")

	// Petar's own request routes to Ivan; Ivan's to HR admins.
	rp, _ := f.s.Create(ctx, petar, Input{AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 1), End: d(2026, 9, 1)})
	ri, _ := f.s.Create(ctx, ivan, Input{AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 1), End: d(2026, 9, 1)})
	if rp.Status != store.StatusApproved || ri.ReviewedAt == nil {
		t.Fatal("sick leave needs no approval")
	}
	x := f.signed
	x.ID, x.Name, x.RequiresSigning, x.Signing = store.NewID(), "Home office", false, nil
	x.Deducts = false
	_ = f.m.CreateAbsenceType(ctx, x)
	rp2, _ := f.s.Create(ctx, petar, Input{AbsenceTypeID: x.ID, Start: d(2026, 9, 2), End: d(2026, 9, 2)})
	ri2, _ := f.s.Create(ctx, ivan, Input{AbsenceTypeID: x.ID, Start: d(2026, 9, 2), End: d(2026, 9, 2)})
	if rp2.ApproverIDs[0] != "ivan" || ri2.ApproverIDs[0] != "hana" {
		t.Fatalf("routing: %v %v", rp2.ApproverIDs, ri2.ApproverIDs)
	}

	// HR admin files for someone else; employees cannot.
	if _, err := f.s.Create(ctx, hana, Input{UserID: "ana", AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 7), End: d(2026, 9, 7)}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Create(ctx, maria, Input{UserID: "ana", AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 8), End: d(2026, 9, 8)})
	is(t, err, apperr.Forbidden, "employee for another")
	f.people.inactive["gone"] = true
	_, err = f.s.Create(ctx, hana, Input{UserID: "gone", AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 8), End: d(2026, 9, 8)})
	is(t, err, apperr.Validation, "inactive target")
	f.people.err = errors.New("auth down")
	_, err = f.s.Create(ctx, hana, Input{UserID: "ana", AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 9), End: d(2026, 9, 9)})
	is(t, err, apperr.TemporarilyUnavailable, "auth down")
	f.people.err = nil

	bad := map[string]Input{
		"reason": {AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10), Reason: strings.Repeat("x", 1001)},
		"notes":  {AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10), Notes: strings.Repeat("x", 2001)},
		"type":   {AbsenceTypeID: "0190f7c2-6a3e-7c1a-9b2e-000000000000", Start: d(2026, 9, 10), End: d(2026, 9, 10)},
		"range":  {AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 10), End: d(2026, 9, 9)},
		"span":   {AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 10), End: d(2028, 9, 9)},
		"half":   {AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10), HalfEnd: true},
		"year":   {AbsenceTypeID: f.sick.ID, Start: d(1999, 9, 10), End: d(1999, 9, 10)},
	}
	for name, b := range bad {
		if _, err := f.s.Create(ctx, maria, b); !errors.Is(err, apperr.Validation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err = f.s.Create(ctx, maria, Input{AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 12), End: d(2026, 9, 13)})
	is(t, err, apperr.ZeroDays, "weekend only")
	_, err = f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 9, 14), End: d(2026, 10, 30)})
	is(t, err, apperr.InsufficientAllowance, "balance")
	_, err = f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2027, 9, 14), End: d(2027, 9, 14)})
	is(t, err, apperr.NoAllowance, "no allowance for year")
	if e, _ := apperr.As(err); e.Detail["year"] != 2027 {
		t.Fatalf("detail: %+v", e.Detail)
	}
	_, err = f.s.Create(ctx, stray, Input{AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10)})
	is(t, err, apperr.Forbidden, "no permission")

	// Auto-approved deducting type is charged at once.
	auto := f.pooled
	_ = auto
	pooledAuto := f.pooled
	pooledAuto.RequiresApproval = false
	_ = f.m.UpdateAbsenceType(ctx, pooledAuto)
	ra, err := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.pooled.ID, Start: d(2026, 9, 21), End: d(2026, 9, 22)})
	if err != nil || ra.Status != store.StatusApproved || f.allowance(t, f.mariaPool.ID).Used != 20 {
		t.Fatalf("auto charge: %+v %v", ra, err)
	}
	if cs, _ := f.m.Charges(ctx, tn, ra.ID); len(cs) != 1 || cs[0].AllowanceID != f.mariaPool.ID {
		t.Fatal("charge row")
	}

	f.calErr = errors.New("db")
	if _, err := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10)}); err == nil {
		t.Fatal("calendar error")
	}
	f.calErr, f.treeErr = nil, errors.New("db")
	if _, err := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10)}); err == nil {
		t.Fatal("tree error")
	}
	f.treeErr = nil
	f.people.adminErr = errors.New("down")
	_, err = f.s.Create(ctx, ivan, Input{AbsenceTypeID: x.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10)})
	is(t, err, apperr.TemporarilyUnavailable, "admins unavailable")
	f.people.adminErr = nil
	f.m.Fail("GetAbsenceType", errors.New("db"))
	if _, err := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10)}); err == nil {
		t.Fatal("type error")
	}
	f.m.Fail("Overlaps", errors.New("db"))
	if _, err := f.s.Preview(ctx, maria, Input{AbsenceTypeID: f.sick.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10)}); err == nil {
		t.Fatal("overlap error")
	}
	f.m.Fail("FindAllowance", errors.New("db"))
	if _, err := f.s.Preview(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10)}); err == nil {
		t.Fatal("allowance error on preview")
	}
	if _, err := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 9, 10), End: d(2026, 9, 10)}); err == nil {
		t.Fatal("allowance error on create")
	}
	f.m.Fail("", nil)
	if _, err := f.s.Preview(ctx, stray, in); err == nil {
		t.Fatal("preview permission")
	}
}

func TestApproveRejectRevoke(t *testing.T) {
	f := setup(t)
	r, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 8, 3), End: d(2026, 8, 7)})
	_, err := f.s.Approve(ctx, maria, r.ID, "")
	is(t, err, apperr.SelfReview, "self approval")
	_, err = f.s.Approve(ctx, ivan, r.ID, "")
	is(t, err, apperr.NotRouted, "ancestor manager is not the approver")
	_, err = f.s.Approve(ctx, vera, r.ID, "")
	is(t, err, apperr.NotRouted, "viewer")
	_, err = f.s.Approve(ctx, ana, r.ID, "")
	is(t, err, apperr.NotFound, "unrelated colleague")
	_, err = f.s.Approve(ctx, petar, r.ID, strings.Repeat("x", 1001))
	is(t, err, apperr.Validation, "notes")
	a, err := f.s.Approve(ctx, petar, r.ID, "ok")
	if err != nil || a.Status != store.StatusApproved || a.ReviewedBy != "petar" || a.ReviewNotes != "ok" {
		t.Fatalf("approve: %+v %v", a, err)
	}
	if f.allowance(t, f.mariaAnnual.ID).Used != 40 || f.mailTo(KeyApproved, "maria") != 1 {
		t.Fatal("charged 4 days (holiday skipped) + mail")
	}
	_, err = f.s.Approve(ctx, petar, r.ID, "")
	is(t, err, apperr.InvalidTransition, "approve twice")
	_, err = f.s.Reject(ctx, petar, r.ID, "")
	is(t, err, apperr.InvalidTransition, "reject approved")

	// Revoke refunds exactly.
	_, err = f.s.Revoke(ctx, maria, r.ID, "")
	is(t, err, apperr.SelfReview, "self revoke")
	rv, err := f.s.Revoke(ctx, hana, r.ID, "business need")
	if err != nil || rv.Status != store.StatusRevoked || f.allowance(t, f.mariaAnnual.ID).Used != 0 || f.mailTo(KeyRevoked, "maria") != 1 {
		t.Fatalf("revoke: %+v %v", rv, err)
	}
	if cs, _ := f.m.Charges(ctx, tn, r.ID); len(cs) != 0 {
		t.Fatal("charges removed")
	}
	_, err = f.s.Revoke(ctx, hana, r.ID, "")
	is(t, err, apperr.InvalidTransition, "revoke twice")
	_, err = f.s.Revoke(ctx, hana, r.ID, strings.Repeat("x", 1001))
	is(t, err, apperr.Validation, "revoke notes")

	// Reject with notes.
	r2, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 8, 10), End: d(2026, 8, 10)})
	rj, err := f.s.Reject(ctx, petar, r2.ID, " not now ")
	if err != nil || rj.Status != store.StatusRejected || rj.ReviewNotes != "not now" || f.mailTo(KeyRejected, "maria") != 1 {
		t.Fatalf("reject: %+v %v", rj, err)
	}
	if m := f.m.Mail(); m[len(m)-1].Vars["ReviewNotes"] != "not now" || m[len(m)-1].Vars["ReviewerName"] != "Petar" {
		t.Fatal("reject mail vars")
	}
	_, err = f.s.Reject(ctx, petar, r2.ID, strings.Repeat("x", 1001))
	is(t, err, apperr.Validation, "reject notes")

	// Balance changed after creation: approval refused under lock.
	r3, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 9, 1), End: d(2026, 9, 4)})
	_ = f.m.AddUsed(ctx, tn, f.mariaAnnual.ID, 80, f.now)
	_, err = f.s.Approve(ctx, petar, r3.ID, "")
	is(t, err, apperr.InsufficientAllowance, "balance at approval")

	// HR admin approves anything but their own; missing requests.
	if _, err := f.s.Reject(ctx, hana, r3.ID, ""); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []func() error{
		func() error { _, e := f.s.Approve(ctx, hana, "0190f7c2-6a3e-7c1a-9b2e-000000000000", ""); return e },
		func() error { _, e := f.s.Reject(ctx, hana, "0190f7c2-6a3e-7c1a-9b2e-000000000000", ""); return e },
		func() error { _, e := f.s.Revoke(ctx, hana, "0190f7c2-6a3e-7c1a-9b2e-000000000000", ""); return e },
	} {
		is(t, fn(), apperr.NotFound, "missing")
	}
	f.treeErr = errors.New("db")
	for _, fn := range []func() error{
		func() error { _, e := f.s.Approve(ctx, hana, r3.ID, ""); return e },
		func() error { _, e := f.s.Reject(ctx, hana, r3.ID, ""); return e },
		func() error { _, e := f.s.Revoke(ctx, hana, r3.ID, ""); return e },
	} {
		if err := fn(); err == nil {
			t.Fatal("tree error hidden")
		}
	}
	f.treeErr = nil
	f.people.adminErr = errors.New("down")
	_, err = f.s.Approve(ctx, petar, r3.ID, "")
	is(t, err, apperr.TemporarilyUnavailable, "admins down")
	f.people.adminErr = nil
}

func TestConcurrentApprovalsNeverOverdraw(t *testing.T) {
	f := setup(t)
	var ids []string
	for i := 0; i < 8; i++ {
		day := d(2026, 9, 1).AddDate(0, 0, 7*i) // Tuesdays: 3 days each, 8×3 = 24 > 10 available
		r, err := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: day, End: day.AddDate(0, 0, 2)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := f.s.Approve(ctx, petar, id, ""); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	a := f.allowance(t, f.mariaAnnual.ID)
	if ok != 3 || a.Used != 90 || a.Remaining() < 0 {
		t.Fatalf("approved %d, used %v", ok, a.Used)
	}
}

func TestCancelUpdateDelete(t *testing.T) {
	f := setup(t)
	r, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 8, 3), End: d(2026, 8, 4), Reason: "a"})
	u, err := f.s.Update(ctx, maria, r.ID, " b ", "notes")
	if err != nil || u.Reason != "b" || u.Notes != "notes" {
		t.Fatalf("update: %+v %v", u, err)
	}
	_, err = f.s.Update(ctx, petar, r.ID, "x", "")
	is(t, err, apperr.Forbidden, "approver edits")
	_, err = f.s.Update(ctx, ana, r.ID, "x", "")
	is(t, err, apperr.NotFound, "colleague edits")
	_, err = f.s.Update(ctx, maria, r.ID, strings.Repeat("x", 1001), "")
	is(t, err, apperr.Validation, "reason")
	_, err = f.s.Update(ctx, maria, r.ID, "", strings.Repeat("x", 2001))
	is(t, err, apperr.Validation, "notes")
	is(t, f.s.Delete(ctx, maria, r.ID), apperr.InvalidTransition, "delete pending")
	_, err = f.s.Cancel(ctx, petar, r.ID)
	is(t, err, apperr.Forbidden, "approver cancels")
	_, err = f.s.Cancel(ctx, ana, r.ID)
	is(t, err, apperr.NotFound, "colleague cancels")
	c, err := f.s.Cancel(ctx, maria, r.ID)
	if err != nil || c.Status != store.StatusCancelled {
		t.Fatal(err)
	}
	_, err = f.s.Update(ctx, maria, r.ID, "x", "")
	is(t, err, apperr.InvalidTransition, "edit cancelled")
	_, err = f.s.Cancel(ctx, maria, r.ID)
	is(t, err, apperr.InvalidTransition, "cancel twice")
	is(t, f.s.Delete(ctx, petar, r.ID), apperr.Forbidden, "approver deletes")
	is(t, f.s.Delete(ctx, ana, r.ID), apperr.NotFound, "colleague deletes")
	if err := f.s.Delete(ctx, maria, r.ID); err != nil {
		t.Fatal(err)
	}
	is(t, f.s.Delete(ctx, maria, r.ID), apperr.NotFound, "delete twice")

	// Approved in the future: cancel refunds; started: refused.
	fut, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 8, 10), End: d(2026, 8, 11)})
	_, _ = f.s.Approve(ctx, petar, fut.ID, "")
	if _, err := f.s.Cancel(ctx, hana, fut.ID); err != nil || f.allowance(t, f.mariaAnnual.ID).Used != 0 {
		t.Fatalf("cancel approved: %v", err)
	}
	past, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 8, 17), End: d(2026, 8, 17)})
	_, _ = f.s.Approve(ctx, petar, past.ID, "")
	f.now = d(2026, 8, 17)
	_, err = f.s.Cancel(ctx, maria, past.ID)
	is(t, err, apperr.InvalidTransition, "cancel started leave")
	if _, err := f.s.Update(ctx, maria, "0190f7c2-6a3e-7c1a-9b2e-000000000000", "", ""); !errors.Is(err, apperr.NotFound) {
		t.Fatal("update missing")
	}
}

func TestListAndGet(t *testing.T) {
	f := setup(t)
	r1, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 8, 3), End: d(2026, 8, 4), Reason: "private"})
	x := f.signed
	x.ID, x.Name, x.RequiresSigning, x.Signing, x.Deducts = store.NewID(), "Home office", false, nil, false
	_ = f.m.CreateAbsenceType(ctx, x)
	r2, _ := f.s.Create(ctx, ana, Input{AbsenceTypeID: x.ID, Start: d(2026, 8, 3), End: d(2026, 8, 3)})
	if _, n, _ := f.s.List(ctx, maria, ListFilter{}); n != 1 {
		t.Fatalf("mine: %d", n)
	}
	if l, n, _ := f.s.List(ctx, petar, ListFilter{View: ViewReview}); n != 1 || l[0].ID != r1.ID {
		t.Fatalf("petar review: %d", n)
	}
	if _, n, _ := f.s.List(ctx, ivan, ListFilter{View: ViewReview}); n != 1 {
		t.Fatalf("ivan reviews ana: %d", n)
	}
	if _, n, _ := f.s.List(ctx, ivan, ListFilter{View: ViewAll}); n != 2 {
		t.Fatalf("ivan manages eng+plat: %d", n)
	}
	if _, n, _ := f.s.List(ctx, petar, ListFilter{View: ViewAll}); n != 1 {
		t.Fatalf("petar manages plat (maria): %d", n)
	}
	if _, n, _ := f.s.List(ctx, vera, ListFilter{View: ViewAll, DepartmentID: "eng"}); n != 1 {
		t.Fatalf("viewer department filter: %d", n)
	}
	if _, n, _ := f.s.List(ctx, hana, ListFilter{View: ViewAll, Status: store.StatusPending}); n != 2 {
		t.Fatalf("admin all: %d", n)
	}
	_, _, err := f.s.List(ctx, maria, ListFilter{View: ViewAll})
	is(t, err, apperr.Forbidden, "employee all")
	_, _, err = f.s.List(ctx, maria, ListFilter{View: "bogus"})
	is(t, err, apperr.Validation, "view")
	_, _, err = f.s.List(ctx, authz.User(tn, "cal", nil), ListFilter{})
	is(t, err, apperr.Forbidden, "calendar viewer lists")

	for _, who := range []authz.Subjects{maria, petar, ivan, hana, vera} {
		if _, err := f.s.Get(ctx, who, r1.ID); err != nil {
			t.Errorf("%s get: %v", who.UserID, err)
		}
	}
	_, err = f.s.Get(ctx, ana, r1.ID)
	is(t, err, apperr.NotFound, "colleague reads details")
	_, err = f.s.Get(ctx, maria, r2.ID)
	is(t, err, apperr.NotFound, "maria reads ana")
	f.treeErr = errors.New("db")
	if _, _, err := f.s.List(ctx, maria, ListFilter{}); err == nil {
		t.Fatal("tree error on list")
	}
	if _, err := f.s.Get(ctx, maria, r1.ID); err == nil {
		t.Fatal("tree error on get")
	}
}

func TestCalendar(t *testing.T) {
	f := setup(t)
	r, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 12, 21), End: d(2026, 12, 23), Reason: "secret"})
	_, _ = f.s.Create(ctx, ana, Input{AbsenceTypeID: f.sick.ID, Start: d(2026, 12, 22), End: d(2026, 12, 22)})
	rej, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 12, 28), End: d(2026, 12, 28)})
	_, _ = f.s.Reject(ctx, petar, rej.ID, "")
	people := []CalendarPerson{{UserID: "maria", Name: "Maria"}, {UserID: "ana", Name: "Ana"}, {UserID: "petar", Name: "Petar"}, {UserID: "gone", Name: "Gone"}}
	f.tree.Active = func(u string) bool { return u != "gone" }
	c, err := f.s.Calendar(ctx, authz.User(tn, "cal", nil), d(2026, 12, 14), d(2027, 1, 10), "", "", people)
	if err != nil || len(c.People) != 3 || len(c.Events) != 2 || len(c.Holidays) != 1 || !c.Holidays[0].Date.Equal(d(2026, 12, 25)) {
		t.Fatalf("calendar: %+v %v", c, err)
	}
	if c.People[0].DepartmentID != "plat" {
		t.Fatal("department filled from the tree")
	}
	c, _ = f.s.Calendar(ctx, maria, d(2026, 12, 14), d(2026, 12, 31), "plat", "", people)
	if len(c.People) != 2 || len(c.Events) != 1 || c.Events[0].ID != r.ID {
		t.Fatalf("department filter: %+v", c)
	}
	c, _ = f.s.Calendar(ctx, maria, d(2026, 12, 14), d(2026, 12, 31), "", "ana", people)
	if len(c.People) != 1 {
		t.Fatal("user filter")
	}
	_, err = f.s.Calendar(ctx, maria, d(2026, 1, 1), d(2026, 12, 31), "", "", people)
	is(t, err, apperr.Validation, "range too long")
	_, err = f.s.Calendar(ctx, maria, d(2026, 2, 1), d(2026, 1, 1), "", "", people)
	is(t, err, apperr.Validation, "reversed")
	_, err = f.s.Calendar(ctx, stray, d(2026, 1, 1), d(2026, 1, 2), "", "", people)
	is(t, err, apperr.Forbidden, "no calendar")
	// Holidays across a recurring 29 February and single dates.
	_ = f.m.CreateHoliday(ctx, store.Holiday{ID: store.NewID(), TenantID: tn, Date: d(2024, 2, 29), Name: "Leap", Recurring: true})
	if c, _ := f.s.Calendar(ctx, maria, d(2027, 2, 1), d(2027, 3, 1), "", "", nil); len(c.Holidays) != 0 {
		t.Fatal("no 29 Feb in 2027")
	}
	if c, _ := f.s.Calendar(ctx, maria, d(2028, 2, 1), d(2028, 3, 1), "", "", nil); len(c.Holidays) != 1 {
		t.Fatal("29 Feb 2028")
	}
	if c, _ := f.s.Calendar(ctx, maria, d(2019, 12, 1), d(2019, 12, 31), "", "", nil); len(c.Holidays) != 0 {
		t.Fatal("recurring before its first year")
	}
	for _, m := range []string{"ListHolidays", "ListRequests"} {
		f.m.Fail(m, errors.New("db"))
		if _, err := f.s.Calendar(ctx, maria, d(2026, 12, 14), d(2026, 12, 31), "", "", people); err == nil {
			t.Fatalf("%s error hidden", m)
		}
	}
	f.m.Fail("", nil)
	f.treeErr = errors.New("db")
	if _, err := f.s.Calendar(ctx, maria, d(2026, 12, 14), d(2026, 12, 31), "", "", people); err == nil {
		t.Fatal("tree error")
	}
	_ = leavedays.Tenths(0)
	_ = time.Now
}

// A leave spanning New Year is charged to each year's allowance (FR-015).
func TestTwoYearCharge(t *testing.T) {
	f := setup(t)
	next := store.Allowance{ID: store.NewID(), TenantID: tn, UserID: "maria", Year: 2027, AbsenceTypeID: f.annual.ID, Total: 100}
	if err := f.m.CreateAllowance(ctx, next); err != nil {
		t.Fatal(err)
	}
	r, err := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 12, 23), End: d(2027, 1, 5)})
	if err != nil {
		t.Fatal(err)
	}
	// 2026: 23, 24, 28, 29, 30, 31 (25 recurring holiday) = 6; 2027: 1, 4, 5 = 3.
	if r.Days != 90 {
		t.Fatalf("days = %v", r.Days)
	}
	if _, err := f.s.Approve(ctx, petar, r.ID, ""); err != nil {
		t.Fatal(err)
	}
	if f.allowance(t, f.mariaAnnual.ID).Used != 60 || f.allowance(t, next.ID).Used != 30 {
		t.Fatalf("split: %v %v", f.allowance(t, f.mariaAnnual.ID).Used, f.allowance(t, next.ID).Used)
	}
	if cs, _ := f.m.Charges(ctx, tn, r.ID); len(cs) != 2 {
		t.Fatalf("charges: %+v", cs)
	}
	// A holiday added later does not change what is charged (the stored days win).
	_ = f.m.CreateHoliday(ctx, store.Holiday{ID: store.NewID(), TenantID: tn, Date: d(2027, 1, 4), Name: "Late holiday"})
	if _, err := f.s.Revoke(ctx, petar, r.ID, ""); err != nil || f.allowance(t, next.ID).Used != 0 || f.allowance(t, f.mariaAnnual.ID).Used != 0 {
		t.Fatalf("refund: %v", err)
	}
	r2, _ := f.s.Create(ctx, maria, Input{AbsenceTypeID: f.annual.ID, Start: d(2026, 12, 23), End: d(2027, 1, 5)})
	_ = f.m.CreateHoliday(ctx, store.Holiday{ID: store.NewID(), TenantID: tn, Date: d(2027, 1, 5), Name: "Later still"})
	per, err := f.s.perYear(ctx, r2)
	if err != nil || per[2026]+per[2027] != r2.Days {
		t.Fatalf("stored total wins: %v %v", per, err)
	}
	f.calErr = errors.New("db")
	if _, err := f.s.perYear(ctx, r2); err == nil {
		t.Fatal("calendar error")
	}
	f.calErr = nil
	odd := r2
	odd.HalfEnd, odd.Start = true, d(2027, 1, 9) // start after end: the split fails, the stored days go to the last year
	odd.Start, odd.End = d(2026, 12, 26), d(2027, 1, 2)
	odd.HalfStart, odd.HalfEnd = false, false
	if per, _ := f.s.perYear(ctx, odd); per[2027] != odd.Days-per[2026] {
		t.Fatalf("remainder to the last year: %v", per)
	}
}
