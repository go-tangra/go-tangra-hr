package catalog

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
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
const other = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"

var ctx = context.Background()

type rec struct{ got []audit.Event }

func (r *rec) Record(_ context.Context, e audit.Event) error { r.got = append(r.got, e); return nil }

type fakeSigning struct {
	err error
}

func (f fakeSigning) CheckSettings(_ context.Context, _ string, s store.SigningSettings) (store.SigningSettings, error) {
	if f.err != nil {
		return s, f.err
	}
	s.TemplateName = "Leave form"
	return s, nil
}

var (
	admin   = authz.User(tn, "hana", nil)
	viewer  = authz.User(tn, "vera", nil)
	emp     = authz.User(tn, "maria", nil)
	nobody  = authz.User(tn, "x", nil)
	foreign = authz.User(other, "hana", nil)
)

func perms() authz.Static {
	return authz.Static{"hana": {authz.Calendar, authz.Request, authz.Read, authz.Manage}, "vera": {authz.Calendar, authz.Read},
		"maria": {authz.Calendar, authz.Request}}
}

func newSvc() (*Service, *memstore.Mem, *rec) {
	m, r := memstore.New(), &rec{}
	fixed := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	return New(Deps{Store: m, Audit: r, Checker: perms(), Signing: fakeSigning{}, Now: func() time.Time { return fixed }}), m, r
}

func ptr[T any](v T) *T { return &v }

func is(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v, want %v", what, err, want)
	}
}

func TestTypes(t *testing.T) {
	s, m, r := newSvc()
	p, err := s.CreatePool(ctx, admin, PoolInput{Name: " Vacation ", Color: "#112233", CarryOverCap: ptr(leavedays.Tenths(50))})
	if err != nil || p.Name != "Vacation" {
		t.Fatalf("pool: %+v %v", p, err)
	}
	in := TypeInput{Name: "Annual leave", Color: "#336699", Icon: "lucide:sun", Active: true, Deducts: true, RequiresApproval: true,
		PoolID: p.ID, SortOrder: 1, Metadata: map[string]any{"code": "AL"}}
	al, err := s.CreateType(ctx, admin, in)
	if err != nil || al.PoolID != p.ID || al.CreatedBy != "hana" {
		t.Fatalf("create: %+v %v", al, err)
	}
	if ts, _ := m.TenantsSystem(ctx); len(ts) != 1 {
		t.Fatal("tenant registered")
	}
	_, err = s.CreateType(ctx, admin, TypeInput{Name: "annual LEAVE", Active: true})
	is(t, err, apperr.Duplicate, "duplicate name")
	_, err = s.CreateType(ctx, emp, in)
	is(t, err, apperr.Forbidden, "employee create")

	bad := map[string]TypeInput{
		"name":         {Name: " "},
		"long name":    {Name: strings.Repeat("x", 101)},
		"newline":      {Name: "a\nb"},
		"description":  {Name: "d", Description: strings.Repeat("x", 2001)},
		"color":        {Name: "c", Color: "red"},
		"icon":         {Name: "i", Icon: "Bad Icon"},
		"sort":         {Name: "s", SortOrder: -1},
		"metadata":     {Name: "m", Metadata: map[string]any{"x": strings.Repeat("y", 9000)}},
		"unencodable":  {Name: "m", Metadata: map[string]any{"x": func() {}}},
		"cap":          {Name: "c", CarryOverCap: ptr(leavedays.Tenths(-1))},
		"pool no ded":  {Name: "p", PoolID: p.ID},
		"missing pool": {Name: "p", Deducts: true, PoolID: "0190f7c2-6a3e-7c1a-9b2e-000000000000"},
		"sign no appr": {Name: "s", RequiresSigning: true, Signing: &store.SigningSettings{}},
		"sign no set":  {Name: "s", RequiresSigning: true, RequiresApproval: true},
	}
	for name, b := range bad {
		if _, err := s.CreateType(ctx, admin, b); !errors.Is(err, apperr.Validation) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Signing settings are validated and normalised by the signing checker.
	sig := TypeInput{Name: "Signed leave", Active: true, RequiresApproval: true, RequiresSigning: true,
		Signing: &store.SigningSettings{TemplateID: "t1", EmployeeParty: "p1", ApproverParty: "p2", Fields: map[string]string{"f": "days"}}}
	st, err := s.CreateType(ctx, admin, sig)
	if err != nil || st.Signing.TemplateName != "Leave form" {
		t.Fatalf("signing type: %+v %v", st, err)
	}
	s.d.Signing = fakeSigning{err: apperr.SigningTemplateInvalid}
	_, err = s.CreateType(ctx, admin, TypeInput{Name: "Other", RequiresApproval: true, RequiresSigning: true, Signing: &store.SigningSettings{}})
	is(t, err, apperr.SigningTemplateInvalid, "checker refusal")
	s.d.Signing = nil
	_, err = s.CreateType(ctx, admin, TypeInput{Name: "Other", RequiresApproval: true, RequiresSigning: true, Signing: &store.SigningSettings{}})
	is(t, err, apperr.SigningUnavailable, "no checker")

	// Update and read.
	in.Name, in.Active = "Annual", false
	up, err := s.UpdateType(ctx, admin, al.ID, in)
	if err != nil || up.Name != "Annual" || up.CreatedBy != "hana" {
		t.Fatalf("update: %+v %v", up, err)
	}
	_, err = s.UpdateType(ctx, admin, "0190f7c2-6a3e-7c1a-9b2e-000000000000", in)
	is(t, err, apperr.NotFound, "update missing")
	_, err = s.UpdateType(ctx, admin, al.ID, TypeInput{Name: ""})
	is(t, err, apperr.Validation, "update invalid")
	in.Name = "Signed leave"
	_, err = s.UpdateType(ctx, admin, al.ID, in)
	is(t, err, apperr.Duplicate, "update duplicate")
	_, err = s.UpdateType(ctx, viewer, al.ID, in)
	is(t, err, apperr.Forbidden, "viewer update")

	if _, err := s.GetType(ctx, viewer, al.ID); err != nil {
		t.Fatal("viewer reads inactive type")
	}
	_, err = s.GetType(ctx, emp, al.ID)
	is(t, err, apperr.NotFound, "employee reads inactive type")
	_, err = s.GetType(ctx, foreign, al.ID)
	is(t, err, apperr.NotFound, "foreign tenant")
	_, err = s.GetType(ctx, nobody, al.ID)
	is(t, err, apperr.Forbidden, "no permission")
	if got, err := s.GetType(ctx, emp, st.ID); err != nil || got.ID != st.ID {
		t.Fatal("employee reads active type")
	}
	active, _ := s.ListTypes(ctx, emp, false)
	all, _ := s.ListTypes(ctx, viewer, true)
	if len(active) != 1 || len(all) != 2 {
		t.Fatalf("lists: %d %d", len(active), len(all))
	}
	_, err = s.ListTypes(ctx, emp, true)
	is(t, err, apperr.Forbidden, "employee all")
	_, err = s.ListTypes(ctx, nobody, false)
	is(t, err, apperr.Forbidden, "no calendar")

	// Delete: in use, then free.
	_ = m.CreateAllowance(ctx, store.Allowance{ID: store.NewID(), TenantID: tn, UserID: "maria", Year: 2026, AbsenceTypeID: st.ID, Total: 10})
	is(t, s.DeleteType(ctx, admin, st.ID), apperr.InUse, "delete in use")
	is(t, s.DeleteType(ctx, emp, al.ID), apperr.Forbidden, "employee delete")
	if err := s.DeleteType(ctx, admin, al.ID); err != nil {
		t.Fatal(err)
	}
	is(t, s.DeleteType(ctx, admin, al.ID), apperr.NotFound, "delete deleted")

	var refused int
	for _, e := range r.got {
		if e.Outcome == audit.OutcomeRefused {
			refused++
		}
		if err := audit.Validate(e); err != nil {
			t.Fatalf("invalid audit event %+v: %v", e, err)
		}
	}
	if refused == 0 || len(r.got) < 10 {
		t.Fatalf("audit: %d events, %d refused", len(r.got), refused)
	}
	m.SetErr(errors.New("db down"))
	if _, err := s.ListTypes(ctx, emp, false); err == nil {
		t.Fatal("store error hidden")
	}
	if _, err := s.CreateType(ctx, admin, TypeInput{Name: "Z"}); err == nil {
		t.Fatal("store error on create hidden")
	}
	if _, err := s.CreateType(ctx, admin, TypeInput{Name: "Z", Deducts: true, PoolID: p.ID}); err == nil || errors.Is(err, apperr.Validation) {
		t.Fatal("pool lookup error must surface")
	}
}

func TestPools(t *testing.T) {
	s, _, _ := newSvc()
	p, err := s.CreatePool(ctx, admin, PoolInput{Name: "Vacation"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreatePool(ctx, admin, PoolInput{Name: "VACATION"})
	is(t, err, apperr.Duplicate, "duplicate")
	_, err = s.CreatePool(ctx, admin, PoolInput{Name: ""})
	is(t, err, apperr.Validation, "name")
	_, err = s.CreatePool(ctx, admin, PoolInput{Name: "x", Color: "bad"})
	is(t, err, apperr.Validation, "color")
	_, err = s.CreatePool(ctx, admin, PoolInput{Name: "x", CarryOverCap: ptr(leavedays.Tenths(99999))})
	is(t, err, apperr.Validation, "cap")
	_, err = s.CreatePool(ctx, emp, PoolInput{Name: "x"})
	is(t, err, apperr.Forbidden, "employee")
	up, err := s.UpdatePool(ctx, admin, p.ID, PoolInput{Name: "Holidays", Description: "d"})
	if err != nil || up.Name != "Holidays" {
		t.Fatal(err)
	}
	_, err = s.UpdatePool(ctx, admin, p.ID, PoolInput{Name: ""})
	is(t, err, apperr.Validation, "update invalid")
	_, err = s.UpdatePool(ctx, admin, "0190f7c2-6a3e-7c1a-9b2e-000000000000", PoolInput{Name: "x"})
	is(t, err, apperr.NotFound, "update missing")
	_, err = s.UpdatePool(ctx, emp, p.ID, PoolInput{Name: "x"})
	is(t, err, apperr.Forbidden, "employee update")
	if got, err := s.GetPool(ctx, viewer, p.ID); err != nil || got.Description != "d" {
		t.Fatal("viewer get")
	}
	_, err = s.GetPool(ctx, emp, p.ID)
	is(t, err, apperr.Forbidden, "employee get")
	if l, err := s.ListPools(ctx, viewer); err != nil || len(l) != 1 {
		t.Fatal("list")
	}
	_, err = s.ListPools(ctx, emp)
	is(t, err, apperr.Forbidden, "employee list")
	_, err = s.CreateType(ctx, admin, TypeInput{Name: "A", Deducts: true, PoolID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	is(t, s.DeletePool(ctx, admin, p.ID), apperr.InUse, "delete with members")
	is(t, s.DeletePool(ctx, emp, p.ID), apperr.Forbidden, "employee delete")
}
