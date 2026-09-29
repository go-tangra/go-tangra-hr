package requests

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-hr/v4/internal/routing"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

var ctx = context.Background()

func d(y int, m time.Month, dd int) time.Time { return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) }

// Engineering (ivan) › Platform (petar); maria and petar in Platform, ana in
// Engineering. hana is the HR administrator, vera an HR viewer.
var (
	maria = authz.User(tn, "maria", nil)
	petar = authz.User(tn, "petar", nil)
	ivan  = authz.User(tn, "ivan", nil)
	ana   = authz.User(tn, "ana", nil)
	hana  = authz.User(tn, "hana", nil)
	vera  = authz.User(tn, "vera", nil)
	stray = authz.User(tn, "stray", nil)
)

type fakePeople struct {
	mu       sync.Mutex
	inactive map[string]bool
	err      error
	adminErr error
}

func (p *fakePeople) Active(_ context.Context, _, u string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.inactive[u], p.err
}

func (p *fakePeople) Names(_ context.Context, _ string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if id != "" {
			out[id] = strings.ToUpper(id[:1]) + id[1:]
		}
	}
	return out, p.err
}

func (p *fakePeople) Admins(context.Context, string) ([]string, error) {
	return []string{"hana"}, p.adminErr
}

type fakeSigner struct {
	mu        sync.Mutex
	started   []StartInput
	cancelled []string
	deleted   []string
	state     map[string]string
	startErr  error
	stateErr  error
	docErr    error
	n         int
}

func (f *fakeSigner) Start(_ context.Context, in StartInput) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.started = append(f.started, in)
	f.n++
	return store.NewID(), nil
}

func (f *fakeSigner) Cancel(_ context.Context, _, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id+"/"+reason)
	return nil
}

func (f *fakeSigner) Delete(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeSigner) State(_ context.Context, _, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state[id], f.stateErr
}

func (f *fakeSigner) Document(context.Context, string, string) (io.ReadCloser, string, error) {
	if f.docErr != nil {
		return nil, "", f.docErr
	}
	return io.NopCloser(strings.NewReader("%PDF")), "leave.pdf", nil
}

type fakeEvents struct {
	mu      sync.Mutex
	changes int
	review  int
	cal     int
}

func (e *fakeEvents) RequestChanged(context.Context, string, string, string, []string) {
	e.mu.Lock()
	e.changes++
	e.mu.Unlock()
}
func (e *fakeEvents) ReviewChanged(context.Context, string, []string) {
	e.mu.Lock()
	e.review++
	e.mu.Unlock()
}
func (e *fakeEvents) CalendarChanged(context.Context, string, time.Time, time.Time) {
	e.mu.Lock()
	e.cal++
	e.mu.Unlock()
}

type rec struct {
	mu  sync.Mutex
	got []audit.Event
}

func (r *rec) Record(_ context.Context, e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := audit.Validate(e); err != nil {
		panic(err)
	}
	r.got = append(r.got, e)
	return nil
}

type fx struct {
	s       *Service
	m       *memstore.Mem
	people  *fakePeople
	signer  *fakeSigner
	events  *fakeEvents
	audit   *rec
	tree    routing.Tree
	now     time.Time
	treeErr error
	calErr  error

	annual, sick, pooled, signed store.AbsenceType
	pool                         store.Pool
	mariaAnnual, mariaPool       store.Allowance
}

func setup(t *testing.T) *fx {
	t.Helper()
	f := &fx{m: memstore.New(), people: &fakePeople{inactive: map[string]bool{}}, signer: &fakeSigner{state: map[string]string{}},
		events: &fakeEvents{}, audit: &rec{}, now: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	f.tree = routing.Tree{
		Departments: map[string]routing.Department{"eng": {ID: "eng", ManagerID: "ivan"}, "plat": {ID: "plat", ParentID: "eng", ManagerID: "petar"}},
		MemberOf:    map[string]string{"maria": "plat", "petar": "plat", "ana": "eng"},
	}
	f.pool = store.Pool{ID: store.NewID(), TenantID: tn, Name: "Vacation"}
	f.annual = store.AbsenceType{ID: store.NewID(), TenantID: tn, Name: "Annual", Active: true, Deducts: true, RequiresApproval: true}
	f.sick = store.AbsenceType{ID: store.NewID(), TenantID: tn, Name: "Sick", Active: true}
	f.pooled = store.AbsenceType{ID: store.NewID(), TenantID: tn, Name: "Pooled", Active: true, Deducts: true, PoolID: f.pool.ID}
	f.signed = store.AbsenceType{ID: store.NewID(), TenantID: tn, Name: "Signed", Active: true, Deducts: true, RequiresApproval: true,
		RequiresSigning: true, Signing: &store.SigningSettings{TemplateID: "t", EmployeeParty: "p1", ApproverParty: "p2"}}
	f.mariaAnnual = store.Allowance{ID: store.NewID(), TenantID: tn, UserID: "maria", Year: 2026, AbsenceTypeID: f.annual.ID, Total: 100}
	f.mariaPool = store.Allowance{ID: store.NewID(), TenantID: tn, UserID: "maria", Year: 2026, PoolID: f.pool.ID, Total: 50}
	for _, err := range []error{f.m.CreatePool(ctx, f.pool), f.m.CreateAbsenceType(ctx, f.annual), f.m.CreateAbsenceType(ctx, f.sick),
		f.m.CreateAbsenceType(ctx, f.pooled), f.m.CreateAbsenceType(ctx, f.signed), f.m.CreateAllowance(ctx, f.mariaAnnual),
		f.m.CreateAllowance(ctx, f.mariaPool),
		f.m.CreateAllowance(ctx, store.Allowance{ID: store.NewID(), TenantID: tn, UserID: "maria", Year: 2026, AbsenceTypeID: f.signed.ID, Total: 100}),
		f.m.CreateHoliday(ctx, store.Holiday{ID: store.NewID(), TenantID: tn, Date: d(2026, 8, 5), Name: "Test holiday"}),
		f.m.CreateHoliday(ctx, store.Holiday{ID: store.NewID(), TenantID: tn, Date: d(2020, 12, 25), Name: "Christmas", Recurring: true}),
		f.m.CreateDepartment(ctx, store.Department{ID: "0190f7c2-6a3e-7c1a-9b2e-00000000dd01", TenantID: tn, Name: "Platform"}),
		f.m.UpsertMember(ctx, store.Member{TenantID: tn, UserID: "maria", DepartmentID: "0190f7c2-6a3e-7c1a-9b2e-00000000dd01", Active: true}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	checker := authz.Static{
		"hana":  {authz.Calendar, authz.Request, authz.Read, authz.Manage},
		"vera":  {authz.Calendar, authz.Read},
		"maria": {authz.Calendar, authz.Request}, "petar": {authz.Calendar, authz.Request}, "ivan": {authz.Calendar, authz.Request},
		"ana": {authz.Calendar, authz.Request}, "cal": {authz.Calendar},
	}
	f.s = New(Deps{Store: f.m, Audit: f.audit, Checker: checker, People: f.people, Signing: f.signer, Events: f.events,
		Calendar: func(ctx context.Context, tenant string) (leavedays.Calendar, error) {
			hs, _ := f.m.ListHolidays(ctx, tenant)
			var out []leavedays.Holiday
			for _, h := range hs {
				out = append(out, leavedays.Holiday{Date: h.Date, Recurring: h.Recurring})
			}
			return leavedays.NewCalendar(out), f.calErr
		},
		Tree: func(context.Context, string) (routing.Tree, error) { return f.tree, f.treeErr },
		Now:  func() time.Time { return f.now }, Links: Links{PortalBaseURL: "https://portal.example.org"}})
	return f
}

func (f *fx) allowance(t *testing.T, id string) store.Allowance {
	t.Helper()
	a, err := f.m.GetAllowance(ctx, tn, id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *fx) mailTo(key, user string) int {
	n := 0
	for _, m := range f.m.Mail() {
		if m.Key == key && m.UserID == user {
			n++
		}
	}
	return n
}

func is(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v, want %v", what, err, want)
	}
}
