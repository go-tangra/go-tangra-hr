package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
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
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

const (
	ta = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	tb = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
)

var ctx = context.Background()

type rec struct{ got []audit.Event }

func (r *rec) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		panic(err)
	}
	r.got = append(r.got, e)
	return nil
}

var perms = authz.Static{"hana": {authz.Manage}, "hanb": {authz.Manage}}

func d(y int, m time.Month, dd int) time.Time { return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) }

func seed(t *testing.T, m *memstore.Mem) {
	t.Helper()
	cap5 := leavedays.Tenths(50)
	p := store.Pool{ID: store.NewID(), TenantID: ta, Name: "Vacation", CarryOverCap: &cap5}
	ty := store.AbsenceType{ID: store.NewID(), TenantID: ta, Name: "Annual", Active: true, Deducts: true, RequiresApproval: true, PoolID: p.ID,
		Metadata: map[string]any{"k": "v"}}
	eng := store.Department{ID: store.NewID(), TenantID: ta, Name: "Engineering", ManagerID: "ivan"}
	plat := store.Department{ID: store.NewID(), TenantID: ta, ParentID: eng.ID, Name: "Platform"}
	al := store.Allowance{ID: store.NewID(), TenantID: ta, UserID: "maria", Year: 2026, PoolID: p.ID, Total: 200, Used: 30}
	r := store.Request{ID: store.NewID(), TenantID: ta, UserID: "maria", AbsenceTypeID: ty.ID, Start: d(2026, 8, 3), End: d(2026, 8, 5),
		Days: 30, Status: store.StatusApproved, SubmissionID: "0190f7c2-6a3e-7c1a-9b2e-aaaaaaaaaaaa", Version: 1}
	for _, err := range []error{m.CreatePool(ctx, p), m.CreateAbsenceType(ctx, ty), m.CreateDepartment(ctx, eng), m.CreateDepartment(ctx, plat),
		m.UpsertMember(ctx, store.Member{TenantID: ta, UserID: "maria", DepartmentID: plat.ID, DisplayName: "Maria", Active: true}),
		m.CreateHoliday(ctx, store.Holiday{ID: store.NewID(), TenantID: ta, Date: d(2026, 3, 3), Name: "Liberation", Recurring: true}),
		m.CreateAllowance(ctx, al), m.CreateRequest(ctx, r),
		m.AddCharges(ctx, []store.Charge{{RequestID: r.ID, TenantID: ta, AllowanceID: al.ID, Year: 2026, Days: 30}})} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	m, r := memstore.New(), &rec{}
	seed(t, m)
	s := New(Deps{Store: m, Audit: r, Checker: perms, Now: func() time.Time { return d(2026, 9, 29) }})
	var buf bytes.Buffer
	if err := s.Export(ctx, authz.User(ta, "hana", nil), &buf); err != nil {
		t.Fatal(err)
	}
	a, err := s.Read(bytes.NewReader(buf.Bytes()))
	if raw, _ := json.Marshal(a); strings.Contains(string(raw), "@") {
		t.Fatal("archive must not contain e-mail addresses")
	}
	if err != nil || count(a) != (Counts{1, 1, 2, 1, 1, 1, 1, 1}) {
		t.Fatalf("archive: %+v %v", count(a), err)
	}
	// Restore into an empty tenant: the other tenant id is never used.
	// (Ids are kept, so the round trip uses a fresh store for tenant B.)
	m2 := memstore.New()
	s2 := New(Deps{Store: m2, Checker: perms})
	res, err := s2.Import(ctx, authz.User(tb, "hanb", nil), ModeSkip, bytes.NewReader(buf.Bytes()))
	if err != nil || res.Created != (Counts{1, 1, 2, 1, 1, 1, 1, 1}) {
		t.Fatalf("import: %+v %v", res, err)
	}
	als, _, _ := m2.ListAllowances(ctx, tb, repo.AllowanceFilter{All: true})
	reqs, _, _ := m2.ListRequests(ctx, tb, repo.RequestFilter{All: true})
	if len(als) != 1 || als[0].Used != 30 || len(reqs) != 1 || reqs[0].SubmissionID == "" {
		t.Fatalf("restored: %+v %+v", als, reqs)
	}
	if cs, _ := m2.Charges(ctx, tb, reqs[0].ID); len(cs) != 1 {
		t.Fatal("charges restored")
	}
	// Re-import: skip keeps everything; overwrite updates what it may.
	res, err = s2.Import(ctx, authz.User(tb, "hanb", nil), ModeSkip, bytes.NewReader(buf.Bytes()))
	if err != nil || total(res.Created) != 0 || res.Skipped != 8 {
		t.Fatalf("skip: %+v %v", res, err)
	}
	_ = m2.AddUsed(ctx, tb, als[0].ID, 10, time.Now())
	res, err = s2.Import(ctx, authz.User(tb, "hanb", nil), ModeOverwrite, bytes.NewReader(buf.Bytes()))
	if err != nil || res.Updated.Allowances != 1 || res.Skipped != 1 {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
	if a, _ := m2.GetAllowance(ctx, tb, als[0].ID); a.Used != 30 {
		t.Fatalf("used restored to the archive: %v", a.Used)
	}
	if len(r.got) != 1 || r.got[0].EventType != audit.BackupExport {
		t.Fatal("audit")
	}
}

func gz(t *testing.T, v any) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_ = json.NewEncoder(zw).Encode(v)
	_ = zw.Close()
	return b.Bytes()
}

func TestRefusals(t *testing.T) {
	m := memstore.New()
	seed(t, m)
	s := New(Deps{Store: m, Checker: perms})
	emp := authz.User(ta, "maria", nil)
	if err := s.Export(ctx, emp, &bytes.Buffer{}); !errors.Is(err, apperr.Forbidden) {
		t.Fatal("employee export")
	}
	if _, err := s.Import(ctx, emp, ModeSkip, nil); !errors.Is(err, apperr.Forbidden) {
		t.Fatal("employee import")
	}
	admin := authz.User(ta, "hana", nil)
	if _, err := s.Import(ctx, admin, "merge", nil); !errors.Is(err, apperr.Validation) {
		t.Fatal("mode")
	}
	for name, data := range map[string][]byte{
		"not gzip":     []byte("plain"),
		"not json":     gz(t, "x")[:10],
		"wrong schema": gz(t, map[string]any{"schema": 9, "module": "hr"}),
		"wrong module": gz(t, map[string]any{"schema": 1, "module": "signing"}),
		"unknown key":  gz(t, map[string]any{"schema": 1, "module": "hr", "extra": 1}),
	} {
		if _, err := s.Import(ctx, admin, ModeSkip, bytes.NewReader(data)); !errors.Is(err, apperr.InvalidBackup) {
			t.Errorf("%s: %v", name, err)
		}
	}
	small := New(Deps{Store: m, Checker: perms, MaxBytes: 200})
	if err := small.Export(ctx, admin, &bytes.Buffer{}); !errors.Is(err, apperr.PayloadTooLarge) {
		t.Fatalf("export limit: %v", err)
	}
	big := gz(t, map[string]any{"schema": 1, "module": "hr", "pools": []map[string]any{{"Name": strings.Repeat("x", 5000)}}})
	if _, err := small.Read(bytes.NewReader(big)); err == nil {
		t.Fatal("import limit")
	}
	// Conflicting rows (a request overlapping an existing one) refuse the whole import.
	var buf bytes.Buffer
	_ = s.Export(ctx, admin, &buf)
	m3 := memstore.New()
	s3 := New(Deps{Store: m3, Checker: perms})
	a, _ := s3.Read(bytes.NewReader(buf.Bytes()))
	a.Requests = append(a.Requests, a.Requests[0])
	a.Requests[1].ID = store.NewID()
	if _, err := s3.Import(ctx, authz.User(tb, "hanb", nil), ModeSkip, bytes.NewReader(gz(t, a))); !errors.Is(err, apperr.InvalidBackup) {
		t.Fatalf("conflict: %v", err)
	}
	if ps, _ := m3.ListPools(ctx, tb); len(ps) != 0 {
		t.Fatal("rolled back")
	}
	m3.SetErr(errors.New("db"))
	if _, err := s3.Import(ctx, authz.User(tb, "hanb", nil), ModeSkip, bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("store error")
	}
	for _, method := range []string{"ListPools", "ListAbsenceTypes", "ListDepartments", "ListMembers", "ListHolidays", "ListAllowances", "ListRequests", "Charges"} {
		m.Fail(method, errors.New("db"))
		if err := s.Export(ctx, admin, &bytes.Buffer{}); err == nil {
			t.Fatalf("%s error hidden", method)
		}
	}
	m.Fail("", nil)
	m4 := memstore.New()
	s4 := New(Deps{Store: m4, Checker: perms})
	for _, method := range []string{"CreatePool", "CreateAbsenceType", "CreateDepartment", "UpsertMember", "CreateHoliday", "CreateAllowance", "CreateRequest", "AddCharges"} {
		m4.Fail(method, errors.New("db"))
		if _, err := s4.Import(ctx, authz.User(tb, "hanb", nil), ModeSkip, bytes.NewReader(buf.Bytes())); err == nil {
			t.Fatalf("%s error hidden", method)
		}
	}
	m4.Fail("", nil)
	if New(Deps{}).d.MaxBytes != 256<<20 {
		t.Fatal("default limit")
	}
}

// FuzzRead feeds arbitrary bytes (gzip-wrapped half the time) to Read.
func FuzzRead(f *testing.F) {
	f.Add([]byte(`{"schema":1,"module":"hr"}`), true)
	f.Add([]byte("junk"), false)
	f.Fuzz(func(t *testing.T, data []byte, wrap bool) {
		if wrap {
			var b bytes.Buffer
			zw := gzip.NewWriter(&b)
			_, _ = zw.Write(data)
			_ = zw.Close()
			data = b.Bytes()
		}
		s := New(Deps{MaxBytes: 1 << 16})
		if a, err := s.Read(bytes.NewReader(data)); err == nil && (a.Schema != SchemaVersion || a.Module != "hr") {
			t.Fatal("accepted a wrong archive")
		}
	})
}
