package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/go-tangra/go-tangra-scheduler/sdk/v4/pkg/taskexec"

	"github.com/go-tangra/go-tangra-hr/v4/internal/allowances"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-hr/v4/internal/people"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/routing"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

const (
	tn    = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	other = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
)

var ctx = context.Background()

type fakeRequests struct {
	reconciled []string
	cancelled  []string
	reroutes   int
	err        error
	cancelErr  error
}

func (f *fakeRequests) Reconcile(_ context.Context, tenant string, _ time.Time, _ int) (int, int, error) {
	f.reconciled = append(f.reconciled, tenant)
	return 2, 1, f.err
}
func (f *fakeRequests) Reroute(context.Context, string) error { f.reroutes++; return f.err }
func (f *fakeRequests) CancelOpen(_ context.Context, _, u string) (int, error) {
	f.cancelled = append(f.cancelled, u)
	return 1, f.cancelErr
}

type rec struct{ got []audit.Event }

func (r *rec) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		panic(err)
	}
	r.got = append(r.got, e)
	return nil
}

func req(tenant, payload string) sdk.Request {
	return sdk.Request{TenantID: tenant, Payload: json.RawMessage(payload)}
}

func setup(t *testing.T) (*Runner, *memstore.Mem, *fakeRequests, *people.Fake) {
	t.Helper()
	m := memstore.New()
	_ = m.EnsureTenant(ctx, tn)
	_ = m.EnsureTenant(ctx, other)
	fr := &fakeRequests{}
	dir := &people.Fake{Users: map[string][]people.Contact{tn: {{UserID: "maria", DisplayName: "Maria P."}}}}
	now := time.Date(2027, 1, 1, 0, 30, 0, 0, time.UTC)
	al := allowances.New(allowances.Deps{Store: m, Checker: authz.Static{},
		Tree:     func(context.Context, string) (routing.Tree, error) { return routing.Tree{}, nil },
		Calendar: func(context.Context, string) (leavedays.Calendar, error) { return leavedays.NewCalendar(nil), nil },
		Now:      func() time.Time { return now }})
	return &Runner{Store: m, Allowances: al, Requests: fr, Directory: dir, Audit: &rec{}, Now: func() time.Time { return now },
		Forget: func(string) {}}, m, fr, dir
}

func TestDescriptors(t *testing.T) {
	ds := Descriptors()
	if len(ds) != 3 || ds[0].Platform || !ds[1].Platform || !ds[2].Platform || !Platform()[TypeSync] || Platform()[TypeCarryOver] {
		t.Fatalf("descriptors: %+v", ds)
	}
	for _, d := range ds {
		var v any
		if err := json.Unmarshal([]byte(d.PayloadSchema), &v); err != nil || !strings.HasPrefix(d.Type, "hr:") {
			t.Fatalf("%s schema: %v", d.Type, err)
		}
	}
	r := &Runner{}
	if len(r.Handlers()) != 3 || r.now().IsZero() {
		t.Fatal("handlers")
	}
	r.warn(ctx, "no logger")
}

func TestCarryOver(t *testing.T) {
	r, m, _, _ := setup(t)
	ty := store.AbsenceType{ID: store.NewID(), TenantID: tn, Name: "Annual", Active: true, Deducts: true}
	_ = m.CreateAbsenceType(ctx, ty)
	_ = m.CreateAllowance(ctx, store.Allowance{ID: store.NewID(), TenantID: tn, UserID: "maria", Year: 2026, AbsenceTypeID: ty.ID, Total: 200})
	res := r.CarryOver(ctx, req(tn, `{}`))
	if !res.Success || !strings.Contains(res.Message, "carried 2026 into 2027: 1 created") {
		t.Fatalf("carry: %+v", res)
	}
	if list, _, _ := m.ListAllowances(ctx, tn, repo.AllowanceFilter{Year: 2027, All: true}); len(list) != 1 || list[0].Carried != 200 {
		t.Fatalf("next year: %+v", list)
	}
	if res := r.CarryOver(ctx, req(tn, `{"source_year":2030}`)); !res.Success {
		t.Fatal("explicit year")
	}
	if res := r.CarryOver(ctx, req("", `{}`)); !res.Permanent {
		t.Fatal("platform request refused")
	}
	if res := r.CarryOver(ctx, req(tn, `{"x":1}`)); !res.Permanent {
		t.Fatal("unknown payload field")
	}
	m.SetErr(errors.New("db"))
	if res := r.CarryOver(ctx, req(tn, `{}`)); res.Success || res.Permanent {
		t.Fatal("retry on outage")
	}
}

func TestReconcile(t *testing.T) {
	r, m, fr, _ := setup(t)
	res := r.Reconcile(ctx, req("", `{"older_than_minutes":30}`))
	if !res.Success || len(fr.reconciled) != 2 || res.Message != "checked 4, applied 2" {
		t.Fatalf("reconcile: %+v", res)
	}
	r.ReconcileAfter = time.Minute
	if res := r.Reconcile(ctx, req("", `{}`)); !res.Success {
		t.Fatal("configured cutoff")
	}
	r.ReconcileAfter = 0
	fr.err = errors.New("signing down")
	if res := r.Reconcile(ctx, req("", ``)); res.Success || res.Permanent || !strings.Contains(res.Message, "2 tenants failed") {
		t.Fatalf("partial failure: %+v", res)
	}
	if res := r.Reconcile(ctx, req(tn, `{}`)); !res.Permanent {
		t.Fatal("tenant request refused")
	}
	if res := r.Reconcile(ctx, req("", `{"older_than_minutes":"x"}`)); !res.Permanent {
		t.Fatal("bad payload")
	}
	m.SetErr(errors.New("db"))
	if res := r.Reconcile(ctx, req("", `{}`)); res.Success {
		t.Fatal("registry outage")
	}
}

func TestSync(t *testing.T) {
	r, m, fr, dir := setup(t)
	ty := store.AbsenceType{ID: store.NewID(), TenantID: tn, Name: "Sick", Active: true}
	_ = m.CreateAbsenceType(ctx, ty)
	_ = m.UpsertMember(ctx, store.Member{TenantID: tn, UserID: "maria", DisplayName: "Maria", Active: true})
	_ = m.UpsertMember(ctx, store.Member{TenantID: tn, UserID: "ivan", DisplayName: "Ivan", Active: true})
	_ = m.UpsertMember(ctx, store.Member{TenantID: tn, UserID: "old", DisplayName: "Old", Active: false})
	_ = m.CreateRequest(ctx, store.Request{ID: store.NewID(), TenantID: tn, UserID: "norow", AbsenceTypeID: ty.ID,
		Start: time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), Days: 10, Status: store.StatusPending, Version: 1})
	dir.Users[tn] = append(dir.Users[tn], people.Contact{UserID: "old", DisplayName: "Back"})
	res := r.Sync(ctx, req("", `{}`))
	if !res.Success || res.Message != "2 tenants, 2 members left" {
		t.Fatalf("sync: %+v", res)
	}
	mb, _ := m.GetMember(ctx, tn, "maria")
	iv, _ := m.GetMember(ctx, tn, "ivan")
	ol, _ := m.GetMember(ctx, tn, "old")
	nr, _ := m.GetMember(ctx, tn, "norow")
	if mb.DisplayName != "Maria P." || !mb.Active || iv.Active || !ol.Active || ol.DisplayName != "Back" || nr.Active {
		t.Fatalf("members: %+v %+v %+v %+v", mb, iv, ol, nr)
	}
	if len(fr.cancelled) != 2 || fr.reroutes != 2 {
		t.Fatalf("cancelled %v reroutes %d", fr.cancelled, fr.reroutes)
	}
	if ts, _ := m.TenantsSystem(ctx); ts[0].MembersSyncedAt == nil {
		t.Fatal("sync time")
	}
	if res := r.Sync(ctx, req(tn, `{}`)); !res.Permanent {
		t.Fatal("tenant request refused")
	}
	if res := r.Sync(ctx, req("", `{"x":1}`)); !res.Permanent {
		t.Fatal("bad payload")
	}
	// Failures are retried.
	for _, fail := range []func(){
		func() { dir.Err = errors.New("auth down") },
		func() { m.Fail("ListMembers", errors.New("db")) },
		func() { m.Fail("ListRequests", errors.New("db")) },
		func() { fr.err = errors.New("x") },
		func() { m.Fail("SetMembersSynced", errors.New("db")) },
	} {
		fail()
		if res := r.Sync(ctx, req("", `{}`)); res.Success || res.Permanent {
			t.Fatalf("failure not retried: %+v", res)
		}
		dir.Err, fr.err = nil, nil
		m.Fail("", nil)
	}
	_ = m.UpsertMember(ctx, store.Member{TenantID: tn, UserID: "gone", Active: true})
	fr.cancelErr = errors.New("x")
	if res := r.Sync(ctx, req("", `{}`)); res.Success {
		t.Fatal("cancel error")
	}
	fr.cancelErr = nil
	_ = m.UpsertMember(ctx, store.Member{TenantID: tn, UserID: "gone2", Active: true})
	m.Fail("UpsertMember", errors.New("db"))
	if res := r.Sync(ctx, req("", `{}`)); res.Success {
		t.Fatal("upsert error")
	}
	m.Fail("", nil)
	_ = m.UpsertMember(ctx, store.Member{TenantID: tn, UserID: "maria", DisplayName: "Old name", Active: true})
	m.Fail("UpsertMember", errors.New("db"))
	if res := r.Sync(ctx, req("", `{}`)); res.Success {
		t.Fatal("upsert error on rename")
	}
	m.Fail("", nil)
	m.SetErr(errors.New("db"))
	if res := r.Sync(ctx, req("", `{}`)); res.Success {
		t.Fatal("registry outage")
	}
}
