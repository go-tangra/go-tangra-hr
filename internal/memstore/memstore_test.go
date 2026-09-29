package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo/repotest"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

func TestContract(t *testing.T) {
	repotest.Run(t, func(*testing.T) repo.Store { return New() })
}

func TestFailureInjection(t *testing.T) {
	m := New()
	boom := errors.New("down")
	ctx := context.Background()
	m.SetErr(boom)
	if _, err := m.ListPools(ctx, repotest.TenantA); !errors.Is(err, boom) {
		t.Fatal("global error")
	}
	if err := m.Tx(ctx, repotest.TenantA, func(repo.Store) error { return nil }); !errors.Is(err, boom) {
		t.Fatal("tx error")
	}
	m.SetErr(nil)
	m.Fail("CreatePool", boom)
	if err := m.CreatePool(ctx, repotest.Pool(repotest.TenantA, "x")); !errors.Is(err, boom) {
		t.Fatal("method error")
	}
	m.Fail("", nil)
	if err := m.CreatePool(ctx, repotest.Pool(repotest.TenantA, "x")); err != nil {
		t.Fatal(err)
	}
}

// TestEveryMethodHonoursInjectedErrors drives every method with a global
// error so the failure paths of the fake stay covered.
func TestEveryMethodHonoursInjectedErrors(t *testing.T) {
	m := New()
	boom := errors.New("down")
	m.SetErr(boom)
	ctx := context.Background()
	a, d := repotest.TenantA, time.Now()
	calls := []error{
		m.EnsureTenant(ctx, a), m.SetCursor(ctx, a, "", d), m.SetMembersSynced(ctx, a, d),
		m.CreateAbsenceType(ctx, store.AbsenceType{}), m.UpdateAbsenceType(ctx, store.AbsenceType{}), m.DeleteAbsenceType(ctx, a, ""),
		m.CreatePool(ctx, store.Pool{}), m.UpdatePool(ctx, store.Pool{}), m.DeletePool(ctx, a, ""),
		m.CreateAllowance(ctx, store.Allowance{}), m.UpdateAllowance(ctx, store.Allowance{}), m.AddUsed(ctx, a, "", 1, d), m.DeleteAllowance(ctx, a, ""),
		m.CreateRequest(ctx, store.Request{}), m.DeleteRequest(ctx, a, ""),
		m.AddCharges(ctx, nil), m.DeleteCharges(ctx, a, ""),
		m.CreateDepartment(ctx, store.Department{}), m.UpdateDepartment(ctx, store.Department{}), m.DeleteDepartment(ctx, a, ""),
		m.UpsertMember(ctx, store.Member{}),
		m.CreateHoliday(ctx, store.Holiday{}), m.UpdateHoliday(ctx, store.Holiday{}), m.DeleteHoliday(ctx, a, ""),
		m.CreateCarryOverRun(ctx, store.CarryOverRun{}), m.EnqueueMail(ctx, store.Mail{}), m.DeleteMailSystem(ctx, ""),
		m.RetryMailSystem(ctx, "", 0, d, ""), m.AppendAudit(ctx, store.AuditRow{}),
	}
	add := func(_ any, err error) { calls = append(calls, err) }
	add(m.TenantsSystem(ctx))
	add(m.GetAbsenceType(ctx, a, ""))
	add(m.ListAbsenceTypes(ctx, a))
	add(m.AbsenceTypeInUse(ctx, a, ""))
	add(m.GetPool(ctx, a, ""))
	add(m.PoolInUse(ctx, a, ""))
	add(m.GetAllowance(ctx, a, ""))
	add(m.FindAllowance(ctx, a, "", 0, "", ""))
	add(m.LockAllowances(ctx, a, nil))
	add(m.AllowanceInUse(ctx, a, ""))
	add(m.GetRequest(ctx, a, ""))
	add(m.LockRequest(ctx, a, ""))
	add(m.UpdateRequest(ctx, store.Request{}))
	add(m.Overlaps(ctx, a, "", d, d, ""))
	add(m.RequestBySubmission(ctx, a, ""))
	add(m.AwaitingSigning(ctx, a, d, 1))
	add(m.Charges(ctx, a, ""))
	add(m.GetDepartment(ctx, a, ""))
	add(m.ListDepartments(ctx, a))
	add(m.DepartmentInUse(ctx, a, ""))
	add(m.GetMember(ctx, a, ""))
	add(m.ListMembers(ctx, a))
	add(m.GetHoliday(ctx, a, ""))
	add(m.ListHolidays(ctx, a))
	add(m.RecordOutcome(ctx, store.SigningOutcome{}))
	add(m.DueMailSystem(ctx, d, 1))
	add(m.ListPools(ctx, a))
	_, _, err := m.ListAllowances(ctx, a, repo.AllowanceFilter{})
	calls = append(calls, err)
	_, _, err = m.ListRequests(ctx, a, repo.RequestFilter{})
	calls = append(calls, err)
	for i, err := range calls {
		if !errors.Is(err, boom) {
			t.Errorf("call %d: %v", i, err)
		}
	}
}

func TestHelpers(t *testing.T) {
	m := New()
	ctx := context.Background()
	ty := repotest.Type(repotest.TenantA, "A")
	if err := m.CreateAbsenceType(ctx, ty); err != nil {
		t.Fatal(err)
	}
	r := repotest.Request(repotest.TenantA, "maria", ty.ID, repotest.Date(2026, 1, 5), repotest.Date(2026, 1, 5))
	now := time.Now()
	r.SigningStartedAt, r.ReviewedAt = &now, &now
	m.PutRequest(r)
	if got, _ := m.GetRequest(ctx, repotest.TenantA, r.ID); got.SigningStartedAt == nil {
		t.Fatal("PutRequest")
	}
	_ = m.EnqueueMail(ctx, store.Mail{ID: "b", TenantID: repotest.TenantA, Key: "hr.x", NextAt: now})
	_ = m.EnqueueMail(ctx, store.Mail{ID: "a", TenantID: repotest.TenantA, Key: "hr.x", NextAt: now})
	if ms := m.Mail(); len(ms) != 2 || ms[0].ID != "a" {
		t.Fatal("Mail")
	}
	if due, _ := m.DueMailSystem(ctx, now, 0); len(due) != 2 || due[0].ID != "a" {
		t.Fatal("due tie order")
	}
	_, _ = m.RecordOutcome(ctx, store.SigningOutcome{TenantID: "t", SubmissionID: "s"})
	_ = m.CreateCarryOverRun(ctx, store.CarryOverRun{ID: "r"})
	_ = m.AppendAudit(ctx, store.AuditRow{ID: "x"})
	if len(m.Outcomes()) != 1 || len(m.Runs()) != 1 || len(m.Audit()) != 1 {
		t.Fatal("inspection helpers")
	}
	// Rollback restores cloned nested values.
	cp := repotest.Pool(repotest.TenantA, "P")
	_ = m.CreatePool(ctx, cp)
	_ = m.Tx(ctx, repotest.TenantA, func(repo.Store) error { return errors.New("x") })
	if p, err := m.GetPool(ctx, repotest.TenantA, cp.ID); err != nil || p.CarryOverCap == nil {
		t.Fatal("rollback lost pool")
	}
	// Two-sided allowance and missing references are conflicts.
	if err := m.CreateAllowance(ctx, store.Allowance{ID: "z", TenantID: repotest.TenantA, AbsenceTypeID: ty.ID, PoolID: cp.ID}); !errors.Is(err, repo.ErrConflict) {
		t.Fatal("type and pool accepted")
	}
	if err := m.AddCharges(ctx, []store.Charge{{RequestID: "missing", AllowanceID: "x", Days: 1}}); !errors.Is(err, repo.ErrConflict) {
		t.Fatal("charge of missing request accepted")
	}
}
