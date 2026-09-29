package allowances

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/routing"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

var ctx = context.Background()

type rec struct{ got []audit.Event }

func (r *rec) Record(_ context.Context, e audit.Event) error { r.got = append(r.got, e); return nil }

var (
	admin  = authz.User(tn, "hana", nil)
	viewer = authz.User(tn, "vera", nil)
	maria  = authz.User(tn, "maria", nil)
	petar  = authz.User(tn, "petar", nil)
	ivan   = authz.User(tn, "ivan", nil)
)

func d(y int, m time.Month, dd int) time.Time { return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) }

func ptr[T any](v T) *T { return &v }

type fixture struct {
	s               *Service
	m               *memstore.Mem
	annual, sick    store.AbsenceType
	inPool, unpaid  store.AbsenceType
	pool            store.Pool
	treeErr, calErr error
}

func setup(t *testing.T) *fixture {
	t.Helper()
	m := memstore.New()
	f := &fixture{m: m}
	f.pool = store.Pool{ID: store.NewID(), TenantID: tn, Name: "Vacation", Color: "#111111", CarryOverCap: ptr(leavedays.Tenths(50))}
	f.annual = store.AbsenceType{ID: store.NewID(), TenantID: tn, Name: "Annual", Active: true, Deducts: true, RequiresApproval: true,
		CarryOverCap: ptr(leavedays.Tenths(30))}
	f.inPool = store.AbsenceType{ID: store.NewID(), TenantID: tn, Name: "Pooled", Active: true, Deducts: true, PoolID: f.pool.ID}
	f.sick = store.AbsenceType{ID: store.NewID(), TenantID: tn, Name: "Sick", Active: true}
	f.unpaid = store.AbsenceType{ID: store.NewID(), TenantID: tn, Name: "Unpaid", Active: true, Deducts: true}
	for _, err := range []error{m.CreatePool(ctx, f.pool), m.CreateAbsenceType(ctx, f.annual), m.CreateAbsenceType(ctx, f.inPool),
		m.CreateAbsenceType(ctx, f.sick), m.CreateAbsenceType(ctx, f.unpaid)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	tree := routing.Tree{
		Departments: map[string]routing.Department{"eng": {ID: "eng", ManagerID: "ivan"}, "plat": {ID: "plat", ParentID: "eng", ManagerID: "petar"}},
		MemberOf:    map[string]string{"maria": "plat", "petar": "plat", "ana": "eng"},
	}
	f.s = New(Deps{Store: m, Audit: &rec{},
		Checker: authz.Static{"hana": {authz.Calendar, authz.Request, authz.Read, authz.Manage}, "vera": {authz.Calendar, authz.Read},
			"maria": {authz.Calendar, authz.Request}, "petar": {authz.Calendar, authz.Request}, "ivan": {authz.Calendar, authz.Request}},
		Tree: func(context.Context, string) (routing.Tree, error) { return tree, f.treeErr },
		Calendar: func(context.Context, string) (leavedays.Calendar, error) {
			return leavedays.NewCalendar([]leavedays.Holiday{{Date: d(2026, 12, 31)}}), f.calErr
		},
		Now: func() time.Time { return d(2026, 9, 29) }})
	return f
}

func is(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v, want %v", what, err, want)
	}
}

func TestCRUDAndVisibility(t *testing.T) {
	f := setup(t)
	s := f.s
	a, err := s.Create(ctx, admin, Input{UserID: "maria", Year: 2026, AbsenceTypeID: f.annual.ID, Total: 200, Carried: 20, Notes: "n"})
	if err != nil || a.CreatedBy != "hana" {
		t.Fatalf("create: %+v %v", a, err)
	}
	p, err := s.Create(ctx, admin, Input{UserID: "maria", Year: 2026, PoolID: f.pool.ID, Total: 100})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Create(ctx, admin, Input{UserID: "maria", Year: 2026, AbsenceTypeID: f.annual.ID, Total: 1})
	is(t, err, apperr.Duplicate, "duplicate")
	bad := map[string]Input{
		"user":      {Year: 2026, AbsenceTypeID: f.annual.ID},
		"year":      {UserID: "x", Year: 1999, AbsenceTypeID: f.annual.ID},
		"both":      {UserID: "x", Year: 2026, AbsenceTypeID: f.annual.ID, PoolID: f.pool.ID},
		"neither":   {UserID: "x", Year: 2026},
		"total":     {UserID: "x", Year: 2026, AbsenceTypeID: f.annual.ID, Total: -1},
		"carried":   {UserID: "x", Year: 2026, AbsenceTypeID: f.annual.ID, Carried: 99999},
		"notes":     {UserID: "x", Year: 2026, AbsenceTypeID: f.annual.ID, Notes: strings.Repeat("x", 2001)},
		"no deduct": {UserID: "x", Year: 2026, AbsenceTypeID: f.sick.ID},
		"in pool":   {UserID: "x", Year: 2026, AbsenceTypeID: f.inPool.ID},
		"no type":   {UserID: "x", Year: 2026, AbsenceTypeID: "0190f7c2-6a3e-7c1a-9b2e-000000000000"},
		"no pool":   {UserID: "x", Year: 2026, PoolID: "0190f7c2-6a3e-7c1a-9b2e-000000000000"},
	}
	for name, in := range bad {
		if _, err := s.Create(ctx, admin, in); !errors.Is(err, apperr.Validation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err = s.Create(ctx, maria, Input{UserID: "maria", Year: 2027, AbsenceTypeID: f.annual.ID})
	is(t, err, apperr.Forbidden, "employee create")

	// Visibility: own, manager (direct and ancestor), viewer; others refused.
	for _, who := range []authz.Subjects{maria, petar, ivan, viewer, admin} {
		if _, err := s.Get(ctx, who, a.ID); err != nil {
			t.Errorf("%s get: %v", who.UserID, err)
		}
	}
	_, err = s.Get(ctx, authz.User(tn, "ana", nil), a.ID)
	is(t, err, apperr.NotFound, "colleague get")
	_, err = s.Get(ctx, admin, "0190f7c2-6a3e-7c1a-9b2e-000000000000")
	is(t, err, apperr.NotFound, "missing")

	_, _ = s.Create(ctx, admin, Input{UserID: "ana", Year: 2026, AbsenceTypeID: f.annual.ID, Total: 100})
	if list, total, err := s.List(ctx, viewer, Filter{Year: 2026}); err != nil || total != 3 || len(list) != 3 {
		t.Fatalf("viewer list: %d %v", total, err)
	}
	if _, total, _ := s.List(ctx, maria, Filter{}); total != 2 {
		t.Fatalf("own list: %d", total)
	}
	if _, total, _ := s.List(ctx, petar, Filter{}); total != 2 {
		t.Fatalf("petar manages plat (maria): %d", total)
	}
	if _, total, _ := s.List(ctx, ivan, Filter{}); total != 3 {
		t.Fatalf("ivan manages eng + plat: %d", total)
	}
	_, _, err = s.List(ctx, authz.User(tn, "x", nil), Filter{})
	is(t, err, apperr.Forbidden, "no permission list")

	// Update keeps used days and refuses re-keying a charged allowance.
	_ = f.m.AddUsed(ctx, tn, a.ID, 30, d(2026, 9, 1))
	up, err := s.Update(ctx, admin, a.ID, Input{UserID: "maria", Year: 2026, AbsenceTypeID: f.annual.ID, Total: 150, Carried: 20})
	if err != nil || up.Total != 150 || up.Used != 30 {
		t.Fatalf("update: %+v %v", up, err)
	}
	_, err = s.Update(ctx, admin, a.ID, Input{UserID: "ana", Year: 2026, AbsenceTypeID: f.annual.ID, Total: 150})
	is(t, err, apperr.InUse, "re-key charged")
	_, err = s.Update(ctx, admin, p.ID, Input{UserID: "maria", Year: 2026, PoolID: f.pool.ID, Total: -5})
	is(t, err, apperr.Validation, "update invalid")
	_, err = s.Update(ctx, admin, p.ID, Input{UserID: "maria", Year: 2026, AbsenceTypeID: f.annual.ID, Total: 5})
	is(t, err, apperr.Duplicate, "update onto taken key")
	_, err = s.Update(ctx, admin, "0190f7c2-6a3e-7c1a-9b2e-000000000000", Input{})
	is(t, err, apperr.NotFound, "update missing")
	_, err = s.Update(ctx, maria, a.ID, Input{})
	is(t, err, apperr.Forbidden, "employee update")

	is(t, s.Delete(ctx, maria, p.ID), apperr.Forbidden, "employee delete")
	if err := s.Delete(ctx, admin, p.ID); err != nil {
		t.Fatal(err)
	}
	is(t, s.Delete(ctx, admin, p.ID), apperr.NotFound, "delete deleted")

	f.treeErr = errors.New("db")
	if _, err := s.Get(ctx, maria, a.ID); err == nil {
		t.Fatal("tree error on get")
	}
	if _, _, err := s.List(ctx, maria, Filter{}); err == nil {
		t.Fatal("tree error on list")
	}
	if _, err := s.Balance(ctx, maria, "maria", 2026); err == nil {
		t.Fatal("tree error on balance")
	}
	f.treeErr = nil
	f.m.Fail("GetAbsenceType", errors.New("db"))
	if _, err := s.Create(ctx, admin, Input{UserID: "z", Year: 2026, AbsenceTypeID: f.annual.ID}); err == nil || errors.Is(err, apperr.Validation) {
		t.Fatal("type lookup error")
	}
	f.m.Fail("GetPool", errors.New("db"))
	if _, err := s.Create(ctx, admin, Input{UserID: "z", Year: 2026, PoolID: f.pool.ID}); err == nil || errors.Is(err, apperr.Validation) {
		t.Fatal("pool lookup error")
	}
	f.m.Fail("", nil)
}

func TestBalance(t *testing.T) {
	f := setup(t)
	s := f.s
	a, _ := s.Create(ctx, admin, Input{UserID: "maria", Year: 2026, AbsenceTypeID: f.annual.ID, Total: 200, Carried: 20})
	p, _ := s.Create(ctx, admin, Input{UserID: "maria", Year: 2026, PoolID: f.pool.ID, Total: 100})
	_ = f.m.AddUsed(ctx, tn, a.ID, 50, d(2026, 5, 1))
	mk := func(typeID string, start, end time.Time, days leavedays.Tenths, status string) {
		r := store.Request{ID: store.NewID(), TenantID: tn, UserID: "maria", AbsenceTypeID: typeID, Start: start, End: end, Days: days,
			Status: status, Version: 1}
		if err := f.m.CreateRequest(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	mk(f.annual.ID, d(2026, 10, 5), d(2026, 10, 6), 20, store.StatusPending)
	mk(f.inPool.ID, d(2026, 11, 2), d(2026, 11, 2), 10, store.StatusAwaitingSigning)
	mk(f.inPool.ID, d(2026, 12, 28), d(2027, 1, 5), 70, store.StatusPending) // 2026: 28,29,30 (31 holiday) = 3 days
	mk(f.sick.ID, d(2026, 9, 1), d(2026, 9, 1), 10, store.StatusPending)     // not deducting
	mk(f.annual.ID, d(2026, 8, 3), d(2026, 8, 3), 10, store.StatusApproved)  // not pending

	lines, err := s.Balance(ctx, maria, "maria", 2026)
	if err != nil || len(lines) != 2 {
		t.Fatalf("balance: %+v %v", lines, err)
	}
	al, pl := lines[0], lines[1]
	if al.Kind != "type" || al.AllowanceID != a.ID || al.Used != 50 || al.Remaining != 170 || al.Pending != 20 {
		t.Fatalf("annual line: %+v", al)
	}
	if pl.Kind != "pool" || pl.AllowanceID != p.ID || pl.Pending != 40 || len(pl.MemberTypeIDs) != 1 || pl.MemberTypeIDs[0] != f.inPool.ID {
		t.Fatalf("pool line: %+v", pl)
	}
	if lines, _ := s.Balance(ctx, petar, "maria", 2027); len(lines) != 0 {
		t.Fatal("2027 has no allowances")
	}
	_, err = s.Balance(ctx, authz.User(tn, "ana", nil), "maria", 2026)
	is(t, err, apperr.NotFound, "colleague balance")
	_, err = s.Balance(ctx, maria, "maria", 1990)
	is(t, err, apperr.Validation, "year")
	f.calErr = errors.New("db")
	if _, err := s.Balance(ctx, maria, "maria", 2026); err == nil {
		t.Fatal("calendar error")
	}
	f.calErr = nil
	for _, m := range []string{"ListRequests", "ListPools", "ListAbsenceTypes", "ListAllowances"} {
		f.m.Fail(m, errors.New("db"))
		if _, err := s.Balance(ctx, maria, "maria", 2026); err == nil {
			t.Fatalf("%s error hidden", m)
		}
	}
}

func TestCarryOver(t *testing.T) {
	f := setup(t)
	s := f.s
	mk := func(user string, typeID, poolID string, total, used leavedays.Tenths) store.Allowance {
		a, err := s.Create(ctx, admin, Input{UserID: user, Year: 2026, AbsenceTypeID: typeID, PoolID: poolID, Total: total})
		if err != nil {
			t.Fatal(err)
		}
		if used > 0 {
			_ = f.m.AddUsed(ctx, tn, a.ID, used, d(2026, 6, 1))
		}
		return a
	}
	mk("a", f.annual.ID, "", 200, 180) // unused 2 → 2
	mk("b", f.annual.ID, "", 200, 150) // unused 5 → cap 3
	mk("c", "", f.pool.ID, 200, 110)   // unused 9 → pool cap 5
	mk("d", f.unpaid.ID, "", 100, 120) // overdrawn → 0, no cap
	mk("e", f.unpaid.ID, "", 100, 40)  // no cap → 6
	existing, _ := s.Create(ctx, admin, Input{UserID: "b", Year: 2027, AbsenceTypeID: f.annual.ID, Total: 250})

	prev, err := s.CarryOver(ctx, admin, 2026, false)
	if err != nil || prev.Created != 4 || prev.Updated != 1 || prev.RunID != "" {
		t.Fatalf("preview: %+v %v", prev, err)
	}
	want := map[string]leavedays.Tenths{"a": 20, "b": 30, "c": 50, "d": 0, "e": 60}
	for _, it := range prev.Items {
		if it.Carried != want[it.UserID] {
			t.Errorf("%s carried %v want %v", it.UserID, it.Carried, want[it.UserID])
		}
	}
	if list, _, _ := f.m.ListAllowances(ctx, tn, repoFilter(2027)); len(list) != 1 {
		t.Fatal("preview wrote")
	}
	run, err := s.CarryOver(ctx, admin, 2026, true)
	if err != nil || run.RunID == "" || run.Created != 4 || run.Updated != 1 {
		t.Fatalf("run: %+v %v", run, err)
	}
	next, _, _ := f.m.ListAllowances(ctx, tn, repoFilter(2027))
	if len(next) != 5 {
		t.Fatalf("2027 allowances: %d", len(next))
	}
	for _, a := range next {
		if a.Carried != want[a.UserID] || a.CarriedFromRun != run.RunID {
			t.Errorf("%s: carried %v run %q", a.UserID, a.Carried, a.CarriedFromRun)
		}
		if a.ID == existing.ID && a.Total != 250 {
			t.Error("existing total kept")
		}
		if a.UserID == "a" && a.Total != 200 {
			t.Error("created total copied")
		}
	}
	again, err := s.CarryOver(ctx, admin, 2026, true)
	if err != nil || again.Created != 0 || again.Updated != 0 {
		t.Fatalf("re-run: %+v %v", again, err)
	}
	if len(f.m.Runs()) != 2 {
		t.Fatal("runs recorded")
	}
	if _, err := s.CarryOver(ctx, authz.System(tn), 2026, false); err != nil {
		t.Fatal("system subject (task) refused")
	}
	_, err = s.CarryOver(ctx, maria, 2026, false)
	is(t, err, apperr.Forbidden, "employee")
	_, err = s.CarryOver(ctx, admin, 2099, false)
	is(t, err, apperr.Validation, "year")
	for _, m := range []string{"ListPools", "ListAbsenceTypes", "ListAllowances"} {
		f.m.Fail(m, errors.New("db"))
		if _, err := s.CarryOver(ctx, admin, 2026, false); err == nil {
			t.Fatalf("%s error hidden", m)
		}
	}
	f.m.Fail("", nil)
	// Apply failures roll back.
	_, _ = s.Create(ctx, admin, Input{UserID: "z", Year: 2030, AbsenceTypeID: f.annual.ID, Total: 10})
	_, _ = s.Create(ctx, admin, Input{UserID: "y", Year: 2030, AbsenceTypeID: f.annual.ID, Total: 10})
	_, _ = s.Create(ctx, admin, Input{UserID: "y", Year: 2031, AbsenceTypeID: f.annual.ID, Total: 10})
	for _, m := range []string{"CreateAllowance", "GetAllowance", "UpdateAllowance", "CreateCarryOverRun"} {
		f.m.Fail(m, errors.New("db"))
		if _, err := s.CarryOver(ctx, admin, 2030, true); err == nil {
			t.Fatalf("%s failure hidden", m)
		}
	}
	f.m.Fail("", nil)
	if list, _, _ := f.m.ListAllowances(ctx, tn, repoFilter(2031)); len(list) != 1 {
		t.Fatal("rolled back")
	}
	f.m.Fail("ListAllowances", nil)
	if New(Deps{}).d.Now == nil {
		t.Fatal("default now")
	}
}

func repoFilter(year int) repo.AllowanceFilter { return repo.AllowanceFilter{Year: year, All: true} }
