// Package repotest is the behavioural contract of repo.Store, run against
// both implementations: memstore (unit tests) and repodb (the integration
// suite against TimescaleDB). Keeping one suite guarantees the fake the
// service tests rely on behaves like the database.
package repotest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// Tenants used by the suite (valid uuids for the database).
const (
	TenantA = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	TenantB = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
)

var ctx = context.Background()

func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// Date is a calendar date.
func Date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func ptr[T any](v T) *T { return &v }

// Type returns an absence type of tenant.
func Type(tenant, name string) store.AbsenceType {
	n := now()
	return store.AbsenceType{ID: store.NewID(), TenantID: tenant, Name: name, Color: "#336699", Icon: "lucide:sun", Active: true,
		Metadata: map[string]any{"code": "AL"}, Deducts: true, RequiresApproval: true,
		Audit: store.Audit{CreatedAt: n, CreatedBy: "u1", UpdatedAt: n, UpdatedBy: "u1"}}
}

// Pool returns a pool of tenant.
func Pool(tenant, name string) store.Pool {
	n := now()
	return store.Pool{ID: store.NewID(), TenantID: tenant, Name: name, Color: "#112233", CarryOverCap: ptr(leavedays.Tenths(50)),
		Audit: store.Audit{CreatedAt: n, UpdatedAt: n}}
}

// Allowance returns an allowance of user for a type (or pool when typeID == "").
func Allowance(tenant, user string, year int, typeID, poolID string, total leavedays.Tenths) store.Allowance {
	n := now()
	return store.Allowance{ID: store.NewID(), TenantID: tenant, UserID: user, Year: year, AbsenceTypeID: typeID, PoolID: poolID,
		Total: total, Notes: "n", Audit: store.Audit{CreatedAt: n, UpdatedAt: n}}
}

// Request returns a pending request.
func Request(tenant, user, typeID string, start, end time.Time) store.Request {
	n := now()
	return store.Request{ID: store.NewID(), TenantID: tenant, UserID: user, AbsenceTypeID: typeID, Start: start, End: end,
		Days: 10, Status: store.StatusPending, Reason: "r", ApproverIDs: []string{"petar"}, Version: 1, CreatedAt: n, CreatedBy: user, UpdatedAt: n}
}

// Run executes the contract against a fresh store from mk.
func Run(t *testing.T, mk func(*testing.T) repo.Store) {
	t.Run("types and pools", func(t *testing.T) { testTypesPools(t, mk(t)) })
	t.Run("allowances", func(t *testing.T) { testAllowances(t, mk(t)) })
	t.Run("requests", func(t *testing.T) { testRequests(t, mk(t)) })
	t.Run("departments and members", func(t *testing.T) { testDepartments(t, mk(t)) })
	t.Run("holidays", func(t *testing.T) { testHolidays(t, mk(t)) })
	t.Run("tenants outcomes mail audit", func(t *testing.T) { testMisc(t, mk(t)) })
	t.Run("tx rollback", func(t *testing.T) { testTx(t, mk(t)) })
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func is(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v, want %v", what, err, want)
	}
}

func testTypesPools(t *testing.T, s repo.Store) {
	p := Pool(TenantA, "Vacation")
	must(t, s.CreatePool(ctx, p))
	is(t, s.CreatePool(ctx, Pool(TenantA, "vacation")), repo.ErrConflict, "pool name case-insensitive")
	must(t, s.CreatePool(ctx, Pool(TenantB, "Vacation")))
	got, err := s.GetPool(ctx, TenantA, p.ID)
	if err != nil || got.Name != "Vacation" || got.CarryOverCap == nil || *got.CarryOverCap != 50 {
		t.Fatalf("get pool: %+v %v", got, err)
	}
	_, err = s.GetPool(ctx, TenantB, p.ID)
	is(t, err, repo.ErrNotFound, "foreign pool")

	a := Type(TenantA, "Annual leave")
	a.PoolID = p.ID
	a.CarryOverCap = ptr(leavedays.Tenths(30))
	a.RequiresSigning = true
	a.Signing = &store.SigningSettings{TemplateID: "t1", TemplateName: "Form", EmployeeParty: "p1", ApproverParty: "p2",
		Fields: map[string]string{"f1": "employee_name"}}
	must(t, s.CreateAbsenceType(ctx, a))
	b := Type(TenantA, "Sick")
	b.Deducts, b.SortOrder = false, 5
	must(t, s.CreateAbsenceType(ctx, b))
	is(t, s.CreateAbsenceType(ctx, Type(TenantA, "SICK")), repo.ErrConflict, "type name")
	foreignPool := Type(TenantB, "X")
	foreignPool.PoolID = p.ID
	is(t, s.CreateAbsenceType(ctx, foreignPool), repo.ErrConflict, "foreign pool reference")

	ga, err := s.GetAbsenceType(ctx, TenantA, a.ID)
	if err != nil || ga.Signing == nil || ga.Signing.Fields["f1"] != "employee_name" || ga.Metadata["code"] != "AL" ||
		ga.PoolID != p.ID || *ga.CarryOverCap != 30 {
		t.Fatalf("get type: %+v %v", ga, err)
	}
	list, err := s.ListAbsenceTypes(ctx, TenantA)
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("list types (sort order): %+v %v", list, err)
	}
	b.Name, b.Active = "Sick leave", false
	must(t, s.UpdateAbsenceType(ctx, b))
	b.Name = "annual LEAVE"
	is(t, s.UpdateAbsenceType(ctx, b), repo.ErrConflict, "rename to taken")
	ghost := Type(TenantA, "Ghost")
	is(t, s.UpdateAbsenceType(ctx, ghost), repo.ErrNotFound, "update missing type")

	used, err := s.PoolInUse(ctx, TenantA, p.ID)
	if err != nil || !used {
		t.Fatal("pool with members not in use")
	}
	is(t, s.DeletePool(ctx, TenantA, p.ID), repo.ErrConflict, "delete pool with members")
	pools, err := s.ListPools(ctx, TenantA)
	if err != nil || len(pools) != 1 {
		t.Fatalf("list pools: %v %v", pools, err)
	}
	p.Description = "shared"
	must(t, s.UpdatePool(ctx, p))
	is(t, s.UpdatePool(ctx, Pool(TenantA, "missing")), repo.ErrNotFound, "update missing pool")
	p2 := Pool(TenantA, "Other")
	must(t, s.CreatePool(ctx, p2))
	p2.Name = "VACATION"
	is(t, s.UpdatePool(ctx, p2), repo.ErrConflict, "pool rename taken")
	must(t, s.DeletePool(ctx, TenantA, p2.ID))
	is(t, s.DeletePool(ctx, TenantA, p2.ID), repo.ErrNotFound, "delete deleted pool")

	inUse, err := s.AbsenceTypeInUse(ctx, TenantA, b.ID)
	if err != nil || inUse {
		t.Fatal("unused type in use")
	}
	must(t, s.DeleteAbsenceType(ctx, TenantA, b.ID))
	is(t, s.DeleteAbsenceType(ctx, TenantA, b.ID), repo.ErrNotFound, "delete deleted type")
	_, err = s.GetAbsenceType(ctx, TenantB, a.ID)
	is(t, err, repo.ErrNotFound, "foreign type")
}

func testAllowances(t *testing.T, s repo.Store) {
	ty := Type(TenantA, "Annual")
	must(t, s.CreateAbsenceType(ctx, ty))
	p := Pool(TenantA, "Vacation")
	must(t, s.CreatePool(ctx, p))
	a := Allowance(TenantA, "maria", 2026, ty.ID, "", 200)
	a.Carried = 30
	must(t, s.CreateAllowance(ctx, a))
	is(t, s.CreateAllowance(ctx, Allowance(TenantA, "maria", 2026, ty.ID, "", 100)), repo.ErrConflict, "duplicate type allowance")
	pa := Allowance(TenantA, "maria", 2026, "", p.ID, 100)
	must(t, s.CreateAllowance(ctx, pa))
	is(t, s.CreateAllowance(ctx, Allowance(TenantA, "maria", 2026, "", p.ID, 1)), repo.ErrConflict, "duplicate pool allowance")
	must(t, s.CreateAllowance(ctx, Allowance(TenantA, "ivan", 2027, ty.ID, "", 100)))
	is(t, s.CreateAllowance(ctx, Allowance(TenantB, "x", 2026, ty.ID, "", 100)), repo.ErrConflict, "foreign type reference")

	f, err := s.FindAllowance(ctx, TenantA, "maria", 2026, ty.ID, "")
	if err != nil || f.ID != a.ID || f.Remaining() != 230 {
		t.Fatalf("find: %+v %v", f, err)
	}
	fp, err := s.FindAllowance(ctx, TenantA, "maria", 2026, "", p.ID)
	if err != nil || fp.ID != pa.ID {
		t.Fatalf("find pool: %+v %v", fp, err)
	}
	_, err = s.FindAllowance(ctx, TenantA, "maria", 2025, ty.ID, "")
	is(t, err, repo.ErrNotFound, "find missing year")

	items, total, err := s.ListAllowances(ctx, TenantA, repo.AllowanceFilter{Year: 2026, All: true})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("list by year: %d %v", total, err)
	}
	items, total, err = s.ListAllowances(ctx, TenantA, repo.AllowanceFilter{Page: 1, PageSize: 1})
	if err != nil || total != 3 || len(items) != 1 || items[0].Year != 2027 {
		t.Fatalf("paged list (year desc): %+v %d %v", items, total, err)
	}
	items, _, _ = s.ListAllowances(ctx, TenantA, repo.AllowanceFilter{UserIDs: []string{"ivan"}, All: true})
	if len(items) != 1 {
		t.Fatalf("user ids filter: %d", len(items))
	}
	items, _, _ = s.ListAllowances(ctx, TenantA, repo.AllowanceFilter{UserID: "maria", PoolID: p.ID, All: true})
	if len(items) != 1 || items[0].ID != pa.ID {
		t.Fatal("pool filter")
	}
	items, _, _ = s.ListAllowances(ctx, TenantA, repo.AllowanceFilter{AbsenceTypeID: ty.ID, Page: 9, PageSize: 10})
	if len(items) != 0 {
		t.Fatal("page past end")
	}

	must(t, s.Tx(ctx, TenantA, func(tx repo.Store) error {
		locked, err := tx.LockAllowances(ctx, TenantA, []string{pa.ID, a.ID, "0190f7c2-6a3e-7c1a-9b2e-000000000000"})
		if err != nil || len(locked) != 2 {
			t.Fatalf("lock: %v %v", locked, err)
		}
		return tx.AddUsed(ctx, TenantA, a.ID, 45, now())
	}))
	g, _ := s.GetAllowance(ctx, TenantA, a.ID)
	if g.Used != 45 {
		t.Fatalf("used = %v", g.Used)
	}
	is(t, s.AddUsed(ctx, TenantA, a.ID, -50, now()), repo.ErrConflict, "used below zero")
	must(t, s.AddUsed(ctx, TenantA, a.ID, -45, now()))
	is(t, s.AddUsed(ctx, TenantB, a.ID, 1, now()), repo.ErrNotFound, "foreign add used")

	a.Total, a.Notes, a.Used = 250, "changed", 99
	must(t, s.UpdateAllowance(ctx, a))
	g, _ = s.GetAllowance(ctx, TenantA, a.ID)
	if g.Total != 250 || g.Notes != "changed" || g.Used != 0 {
		t.Fatalf("update keeps used: %+v", g)
	}
	a.UserID = "ivan"
	a.Year = 2027
	is(t, s.UpdateAllowance(ctx, a), repo.ErrConflict, "update to taken key")
	is(t, s.UpdateAllowance(ctx, Allowance(TenantA, "x", 2026, ty.ID, "", 1)), repo.ErrNotFound, "update missing")

	inUse, err := s.AllowanceInUse(ctx, TenantA, pa.ID)
	if err != nil || inUse {
		t.Fatal("unused allowance in use")
	}
	must(t, s.DeleteAllowance(ctx, TenantA, pa.ID))
	is(t, s.DeleteAllowance(ctx, TenantA, pa.ID), repo.ErrNotFound, "delete deleted")
	_, err = s.GetAllowance(ctx, TenantB, a.ID)
	is(t, err, repo.ErrNotFound, "foreign allowance")
	is(t, s.DeletePool(ctx, TenantA, p.ID), nil, "pool free again")
	inUse, _ = s.AbsenceTypeInUse(ctx, TenantA, ty.ID)
	if !inUse {
		t.Fatal("type with allowance not in use")
	}
	is(t, s.DeleteAbsenceType(ctx, TenantA, ty.ID), repo.ErrConflict, "delete type with allowance")
}

func testRequests(t *testing.T, s repo.Store) {
	ty := Type(TenantA, "Annual")
	must(t, s.CreateAbsenceType(ctx, ty))
	al := Allowance(TenantA, "maria", 2026, ty.ID, "", 200)
	must(t, s.CreateAllowance(ctx, al))

	r := Request(TenantA, "maria", ty.ID, Date(2026, 8, 3), Date(2026, 8, 7))
	must(t, s.CreateRequest(ctx, r))
	is(t, s.CreateRequest(ctx, Request(TenantA, "maria", ty.ID, Date(2026, 8, 7), Date(2026, 8, 10))), repo.ErrOverlap, "overlap")
	must(t, s.CreateRequest(ctx, Request(TenantA, "ivan", ty.ID, Date(2026, 8, 3), Date(2026, 8, 7))))
	adj := Request(TenantA, "maria", ty.ID, Date(2026, 8, 8), Date(2026, 8, 9))
	must(t, s.CreateRequest(ctx, adj))
	is(t, s.CreateRequest(ctx, Request(TenantB, "maria", ty.ID, Date(2026, 1, 1), Date(2026, 1, 2))), repo.ErrConflict, "foreign type")

	ov, err := s.Overlaps(ctx, TenantA, "maria", Date(2026, 8, 5), Date(2026, 8, 5), "")
	if err != nil || !ov {
		t.Fatal("overlap not reported")
	}
	ov, _ = s.Overlaps(ctx, TenantA, "maria", Date(2026, 8, 5), Date(2026, 8, 5), r.ID)
	if ov {
		t.Fatal("excluded request reported")
	}

	// Version check and status change.
	got, err := s.GetRequest(ctx, TenantA, r.ID)
	if err != nil || got.Version != 1 || got.ApproverIDs[0] != "petar" || !got.Start.Equal(Date(2026, 8, 3)) {
		t.Fatalf("get: %+v %v", got, err)
	}
	stale := got
	got.Status = store.StatusAwaitingSigning
	got.SubmissionID = "0190f7c2-6a3e-7c1a-9b2e-aaaaaaaaaaaa"
	started := now().Add(-time.Hour)
	got.SigningStartedAt = &started
	got.ReviewedBy, got.ReviewedAt = "petar", ptr(now())
	upd, err := s.UpdateRequest(ctx, got)
	if err != nil || upd.Version != 2 {
		t.Fatalf("update: %+v %v", upd, err)
	}
	_, err = s.UpdateRequest(ctx, stale)
	is(t, err, repo.ErrConflict, "stale version")
	_, err = s.UpdateRequest(ctx, Request(TenantA, "x", ty.ID, Date(2026, 1, 1), Date(2026, 1, 1)))
	is(t, err, repo.ErrNotFound, "update missing request")

	bySub, err := s.RequestBySubmission(ctx, TenantA, got.SubmissionID)
	if err != nil || bySub.ID != r.ID {
		t.Fatalf("by submission: %v", err)
	}
	_, err = s.RequestBySubmission(ctx, TenantB, got.SubmissionID)
	is(t, err, repo.ErrNotFound, "foreign submission")
	waiting, err := s.AwaitingSigning(ctx, TenantA, now(), 10)
	if err != nil || len(waiting) != 1 || waiting[0].ID != r.ID {
		t.Fatalf("awaiting: %v %v", waiting, err)
	}
	waiting, _ = s.AwaitingSigning(ctx, TenantA, started.Add(-time.Minute), 10)
	if len(waiting) != 0 {
		t.Fatal("awaiting cutoff")
	}

	// Lists.
	items, total, err := s.ListRequests(ctx, TenantA, repo.RequestFilter{All: true})
	if err != nil || total != 3 || len(items) != 3 || items[0].ID != adj.ID {
		t.Fatalf("list all (start desc): %d %v", total, err)
	}
	items, _, _ = s.ListRequests(ctx, TenantA, repo.RequestFilter{UserID: "maria", Statuses: []string{store.StatusPending}, All: true})
	if len(items) != 1 || items[0].ID != adj.ID {
		t.Fatal("status filter")
	}
	from, to := Date(2026, 8, 1), Date(2026, 8, 7)
	items, _, _ = s.ListRequests(ctx, TenantA, repo.RequestFilter{From: &from, To: &to, All: true})
	if len(items) != 2 {
		t.Fatalf("range filter: %d", len(items))
	}
	items, _, _ = s.ListRequests(ctx, TenantA, repo.RequestFilter{ApproverID: "petar", UserIDs: []string{"ivan"}, AbsenceTypeID: ty.ID, All: true})
	if len(items) != 1 || items[0].UserID != "ivan" {
		t.Fatal("approver + users filter")
	}
	items, total, _ = s.ListRequests(ctx, TenantA, repo.RequestFilter{Page: 2, PageSize: 2})
	if len(items) != 1 || total != 3 {
		t.Fatal("paging")
	}

	// Charges.
	ch := []store.Charge{{RequestID: r.ID, TenantID: TenantA, AllowanceID: al.ID, Year: 2026, Days: 40}}
	must(t, s.AddCharges(ctx, ch))
	is(t, s.AddCharges(ctx, ch), repo.ErrConflict, "duplicate charge")
	cs, err := s.Charges(ctx, TenantA, r.ID)
	if err != nil || len(cs) != 1 || cs[0].Days != 40 {
		t.Fatalf("charges: %v %v", cs, err)
	}
	inUse, _ := s.AllowanceInUse(ctx, TenantA, al.ID)
	if !inUse {
		t.Fatal("charged allowance not in use")
	}
	is(t, s.DeleteAllowance(ctx, TenantA, al.ID), repo.ErrConflict, "delete charged allowance")
	must(t, s.DeleteCharges(ctx, TenantA, r.ID))
	cs, _ = s.Charges(ctx, TenantA, r.ID)
	if len(cs) != 0 {
		t.Fatal("charges not deleted")
	}

	// Cancelled requests no longer block, and a re-activation that overlaps is refused.
	cur, _ := s.GetRequest(ctx, TenantA, adj.ID)
	cur.Status = store.StatusCancelled
	cur, err = s.UpdateRequest(ctx, cur)
	must(t, err)
	must(t, s.CreateRequest(ctx, Request(TenantA, "maria", ty.ID, Date(2026, 8, 9), Date(2026, 8, 9))))
	cur.Status = store.StatusPending
	_, err = s.UpdateRequest(ctx, cur)
	is(t, err, repo.ErrOverlap, "reactivate overlapping")

	must(t, s.AddCharges(ctx, ch))
	must(t, s.DeleteRequest(ctx, TenantA, r.ID)) // charges cascade
	is(t, s.DeleteRequest(ctx, TenantA, r.ID), repo.ErrNotFound, "delete deleted")
	_, err = s.GetRequest(ctx, TenantB, adj.ID)
	is(t, err, repo.ErrNotFound, "foreign request")
	must(t, s.DeleteAllowance(ctx, TenantA, al.ID))
}

func testDepartments(t *testing.T, s repo.Store) {
	eng := store.Department{ID: store.NewID(), TenantID: TenantA, Name: "Engineering", ManagerID: "ivan"}
	must(t, s.CreateDepartment(ctx, eng))
	plat := store.Department{ID: store.NewID(), TenantID: TenantA, ParentID: eng.ID, Name: "Platform", ManagerID: "petar"}
	must(t, s.CreateDepartment(ctx, plat))
	is(t, s.CreateDepartment(ctx, store.Department{ID: store.NewID(), TenantID: TenantA, ParentID: eng.ID, Name: "platform"}), repo.ErrConflict, "sibling name")
	must(t, s.CreateDepartment(ctx, store.Department{ID: store.NewID(), TenantID: TenantA, Name: "Platform"})) // other parent
	is(t, s.CreateDepartment(ctx, store.Department{ID: store.NewID(), TenantID: TenantA, Name: "engineering"}), repo.ErrConflict, "top-level name")
	is(t, s.CreateDepartment(ctx, store.Department{ID: store.NewID(), TenantID: TenantB, ParentID: eng.ID, Name: "X"}), repo.ErrConflict, "foreign parent")

	got, err := s.GetDepartment(ctx, TenantA, plat.ID)
	if err != nil || got.ParentID != eng.ID || got.ManagerID != "petar" {
		t.Fatalf("get: %+v %v", got, err)
	}
	list, err := s.ListDepartments(ctx, TenantA)
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %v %v", list, err)
	}
	plat.ManagerID = ""
	must(t, s.UpdateDepartment(ctx, plat))
	is(t, s.UpdateDepartment(ctx, store.Department{ID: store.NewID(), TenantID: TenantA, Name: "N"}), repo.ErrNotFound, "update missing")

	must(t, s.UpsertMember(ctx, store.Member{TenantID: TenantA, UserID: "maria", DepartmentID: plat.ID, DisplayName: "Maria", Active: true, SyncedAt: now()}))
	must(t, s.UpsertMember(ctx, store.Member{TenantID: TenantA, UserID: "ivan", DisplayName: "Ivan", Active: true, SyncedAt: now()}))
	is(t, s.UpsertMember(ctx, store.Member{TenantID: TenantB, UserID: "x", DepartmentID: plat.ID}), repo.ErrConflict, "foreign department")
	mb, err := s.GetMember(ctx, TenantA, "maria")
	if err != nil || mb.DepartmentID != plat.ID || mb.DisplayName != "Maria" {
		t.Fatalf("member: %+v %v", mb, err)
	}
	_, err = s.GetMember(ctx, TenantB, "maria")
	is(t, err, repo.ErrNotFound, "foreign member")
	mb.DisplayName, mb.Active = "Maria P.", false
	must(t, s.UpsertMember(ctx, mb))
	members, _ := s.ListMembers(ctx, TenantA)
	if len(members) != 2 || members[1].UserID != "maria" || members[1].Active {
		t.Fatalf("members: %+v", members)
	}

	inUse, _ := s.DepartmentInUse(ctx, TenantA, eng.ID)
	if !inUse {
		t.Fatal("parent not in use")
	}
	is(t, s.DeleteDepartment(ctx, TenantA, eng.ID), repo.ErrConflict, "delete with children")
	is(t, s.DeleteDepartment(ctx, TenantA, plat.ID), repo.ErrConflict, "delete with members")
	mb.DepartmentID = ""
	must(t, s.UpsertMember(ctx, mb))
	must(t, s.DeleteDepartment(ctx, TenantA, plat.ID))
	is(t, s.DeleteDepartment(ctx, TenantA, plat.ID), repo.ErrNotFound, "delete deleted")
}

func testHolidays(t *testing.T, s repo.Store) {
	h := store.Holiday{ID: store.NewID(), TenantID: TenantA, Date: Date(2026, 3, 3), Name: "Liberation Day", Recurring: true, CreatedAt: now()}
	must(t, s.CreateHoliday(ctx, h))
	is(t, s.CreateHoliday(ctx, store.Holiday{ID: store.NewID(), TenantID: TenantA, Date: Date(2026, 3, 3), Name: "Dup"}), repo.ErrConflict, "same date")
	must(t, s.CreateHoliday(ctx, store.Holiday{ID: store.NewID(), TenantID: TenantB, Date: Date(2026, 3, 3), Name: "Other tenant"}))
	h2 := store.Holiday{ID: store.NewID(), TenantID: TenantA, Date: Date(2026, 1, 1), Name: "New Year"}
	must(t, s.CreateHoliday(ctx, h2))
	list, err := s.ListHolidays(ctx, TenantA)
	if err != nil || len(list) != 2 || list[0].ID != h2.ID || !list[1].Recurring {
		t.Fatalf("list (by date): %+v %v", list, err)
	}
	h2.Date = Date(2026, 3, 3)
	is(t, s.UpdateHoliday(ctx, h2), repo.ErrConflict, "move onto taken date")
	h2.Date, h2.Name = Date(2026, 1, 2), "Moved"
	must(t, s.UpdateHoliday(ctx, h2))
	g, err := s.GetHoliday(ctx, TenantA, h2.ID)
	if err != nil || g.Name != "Moved" || !g.Date.Equal(Date(2026, 1, 2)) {
		t.Fatalf("get: %+v %v", g, err)
	}
	_, err = s.GetHoliday(ctx, TenantB, h2.ID)
	is(t, err, repo.ErrNotFound, "foreign holiday")
	is(t, s.UpdateHoliday(ctx, store.Holiday{ID: store.NewID(), TenantID: TenantA, Date: Date(2027, 1, 1), Name: "x"}), repo.ErrNotFound, "update missing")
	must(t, s.DeleteHoliday(ctx, TenantA, h2.ID))
	is(t, s.DeleteHoliday(ctx, TenantA, h2.ID), repo.ErrNotFound, "delete deleted")
}

func testMisc(t *testing.T, s repo.Store) {
	must(t, s.EnsureTenant(ctx, TenantA))
	must(t, s.EnsureTenant(ctx, TenantA))
	must(t, s.EnsureTenant(ctx, TenantB))
	must(t, s.SetCursor(ctx, TenantA, "1700000000000-0", now()))
	must(t, s.SetMembersSynced(ctx, TenantB, now()))
	ts, err := s.TenantsSystem(ctx)
	if err != nil || len(ts) != 2 || ts[0].TenantID != TenantA || ts[0].StreamCursor != "1700000000000-0" || ts[1].MembersSyncedAt == nil {
		t.Fatalf("tenants: %+v %v", ts, err)
	}
	unknown := "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c77"
	is(t, s.SetCursor(ctx, unknown, "x", now()), repo.ErrNotFound, "cursor of unknown tenant")
	is(t, s.SetMembersSynced(ctx, unknown, now()), repo.ErrNotFound, "sync of unknown tenant")

	o := store.SigningOutcome{TenantID: TenantA, SubmissionID: "0190f7c2-6a3e-7c1a-9b2e-bbbbbbbbbbbb", Outcome: store.OutcomeCompleted,
		Source: store.SourceEvent, EventID: "1-0", Result: store.ResultIgnoredUnknown, ReceivedAt: now()}
	first, err := s.RecordOutcome(ctx, o)
	if err != nil || !first {
		t.Fatalf("first outcome: %v %v", first, err)
	}
	o.Outcome = store.OutcomeDeclined
	again, err := s.RecordOutcome(ctx, o)
	if err != nil || again {
		t.Fatal("repeat outcome recorded")
	}
	o.TenantID = TenantB
	other, _ := s.RecordOutcome(ctx, o)
	if !other {
		t.Fatal("other tenant outcome refused")
	}

	must(t, s.CreateCarryOverRun(ctx, store.CarryOverRun{ID: store.NewID(), TenantID: TenantA, SourceYear: 2026, CreatedAt: now(), Created: 2}))

	due := now().Add(-time.Minute)
	m1 := store.Mail{ID: store.NewID(), TenantID: TenantA, Key: "hr.request_approved", UserID: "maria", Vars: map[string]string{"A": "1"}, NextAt: due, CreatedAt: now()}
	m2 := store.Mail{ID: store.NewID(), TenantID: TenantB, Key: "hr.request_rejected", UserID: "ivan", NextAt: now().Add(time.Hour), CreatedAt: now()}
	must(t, s.EnqueueMail(ctx, m1))
	must(t, s.EnqueueMail(ctx, m2))
	is(t, s.EnqueueMail(ctx, store.Mail{ID: store.NewID(), TenantID: TenantA, Key: "signing.x", UserID: "u", NextAt: due}), repo.ErrConflict, "foreign key namespace")
	list, err := s.DueMailSystem(ctx, now(), 10)
	if err != nil || len(list) != 1 || list[0].ID != m1.ID || list[0].Vars["A"] != "1" {
		t.Fatalf("due mail: %+v %v", list, err)
	}
	must(t, s.RetryMailSystem(ctx, m1.ID, 1, now().Add(time.Hour), "retryable"))
	list, _ = s.DueMailSystem(ctx, now(), 10)
	if len(list) != 0 {
		t.Fatal("retried mail still due")
	}
	list, _ = s.DueMailSystem(ctx, now().Add(2*time.Hour), 1)
	if len(list) != 1 {
		t.Fatal("limit")
	}
	must(t, s.DeleteMailSystem(ctx, m1.ID))
	is(t, s.RetryMailSystem(ctx, m1.ID, 2, now(), ""), repo.ErrNotFound, "retry deleted")

	must(t, s.AppendAudit(ctx, store.AuditRow{ID: store.NewID(), TenantID: TenantA, At: now(), ActorKind: "user", ActorID: "u",
		Action: "request.create", SubjectKind: "request", SubjectID: "r", Outcome: "ok", Detail: map[string]any{"days": 1}}))
}

func testTx(t *testing.T, s repo.Store) {
	ty := Type(TenantA, "Annual")
	boom := errors.New("boom")
	err := s.Tx(ctx, TenantA, func(tx repo.Store) error {
		if err := tx.CreateAbsenceType(ctx, ty); err != nil {
			return err
		}
		return boom
	})
	is(t, err, boom, "tx error")
	_, err = s.GetAbsenceType(ctx, TenantA, ty.ID)
	is(t, err, repo.ErrNotFound, "rolled back")
	must(t, s.Tx(ctx, TenantA, func(tx repo.Store) error { return tx.CreateAbsenceType(ctx, ty) }))
	if _, err := s.GetAbsenceType(ctx, TenantA, ty.ID); err != nil {
		t.Fatal("committed row missing")
	}
	is(t, s.Tx(ctx, "", func(repo.Store) error { return nil }), repo.ErrNotFound, "tx without tenant")
}
