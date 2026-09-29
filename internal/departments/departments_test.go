package departments

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

var ctx = context.Background()

type people struct {
	active map[string]string
	err    error
}

func (p people) Active(_ context.Context, _, u string) (bool, error) {
	_, ok := p.active[u]
	return ok, p.err
}

func (p people) Names(_ context.Context, _ string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := p.active[id]; ok {
			out[id] = n
		}
	}
	return out, p.err
}

type rerouter struct{ n int }

func (r *rerouter) Reroute(context.Context, string) error { r.n++; return nil }

type rec struct{ got []audit.Event }

func (r *rec) Record(_ context.Context, e audit.Event) error { r.got = append(r.got, e); return nil }

var (
	admin = authz.User(tn, "hana", nil)
	emp   = authz.User(tn, "maria", nil)
)

func newSvc() (*Service, *memstore.Mem, *rerouter, *people) {
	m, rr := memstore.New(), &rerouter{}
	p := &people{active: map[string]string{"ivan": "Ivan", "petar": "Petar", "maria": "Maria", "hana": "Hana"}}
	s := New(Deps{Store: m, Audit: &rec{}, Checker: authz.Static{"hana": {authz.Calendar, authz.Manage}, "maria": {authz.Calendar}},
		People: p, Rerouter: rr, Now: func() time.Time { return time.Unix(1790000000, 0).UTC() }, MaxDepth: 3})
	return s, m, rr, p
}

func is(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v, want %v", what, err, want)
	}
}

func TestDepartments(t *testing.T) {
	s, m, rr, p := newSvc()
	eng, err := s.Create(ctx, admin, Input{Name: " Engineering ", ManagerID: "ivan"})
	if err != nil || eng.Name != "Engineering" || rr.n != 1 {
		t.Fatalf("create: %+v %v reroutes=%d", eng, err, rr.n)
	}
	plat, err := s.Create(ctx, admin, Input{Name: "Platform", ParentID: eng.ID, ManagerID: "petar"})
	if err != nil {
		t.Fatal(err)
	}
	infra, err := s.Create(ctx, admin, Input{Name: "Infra", ParentID: plat.ID})
	if err != nil || rr.n != 2 {
		t.Fatalf("no manager → no reroute: %v %d", err, rr.n)
	}
	_, err = s.Create(ctx, admin, Input{Name: "Deep", ParentID: infra.ID})
	is(t, err, apperr.Validation, "too deep")
	_, err = s.Create(ctx, admin, Input{Name: "platform", ParentID: eng.ID})
	is(t, err, apperr.Duplicate, "sibling name")
	_, err = s.Create(ctx, admin, Input{Name: "X", ParentID: "0190f7c2-6a3e-7c1a-9b2e-000000000000"})
	is(t, err, apperr.Validation, "unknown parent")
	_, err = s.Create(ctx, admin, Input{Name: "X", ManagerID: "ghost"})
	is(t, err, apperr.Validation, "inactive manager")
	_, err = s.Create(ctx, admin, Input{Name: "a/b"})
	is(t, err, apperr.Validation, "name")
	_, err = s.Create(ctx, emp, Input{Name: "X"})
	is(t, err, apperr.Forbidden, "employee create")
	p.err = errors.New("auth down")
	_, err = s.Create(ctx, admin, Input{Name: "X", ManagerID: "ivan"})
	is(t, err, apperr.TemporarilyUnavailable, "auth down")
	p.err = nil

	// Move: cycle, depth, ok.
	_, err = s.Update(ctx, admin, eng.ID, Input{Name: "Engineering", ParentID: infra.ID, ManagerID: "ivan"})
	is(t, err, apperr.Cycle, "cycle")
	sales, _ := s.Create(ctx, admin, Input{Name: "Sales"})
	_, err = s.Update(ctx, admin, eng.ID, Input{Name: "Engineering", ParentID: sales.ID, ManagerID: "ivan"})
	is(t, err, apperr.Validation, "subtree too deep")
	before := rr.n
	up, err := s.Update(ctx, admin, plat.ID, Input{Name: "Platform team", ParentID: sales.ID, ManagerID: "petar"})
	if err != nil || up.ParentID != sales.ID || rr.n != before+1 {
		t.Fatalf("move: %+v %v", up, err)
	}
	before = rr.n
	if _, err := s.Update(ctx, admin, plat.ID, Input{Name: "Platform", ParentID: sales.ID, ManagerID: "petar"}); err != nil || rr.n != before {
		t.Fatal("rename without reroute")
	}
	_, err = s.Update(ctx, admin, "0190f7c2-6a3e-7c1a-9b2e-000000000000", Input{Name: "X"})
	is(t, err, apperr.NotFound, "update missing")
	_, err = s.Update(ctx, admin, plat.ID, Input{Name: ""})
	is(t, err, apperr.Validation, "update name")
	_, err = s.Update(ctx, admin, plat.ID, Input{Name: "X", ManagerID: "ghost"})
	is(t, err, apperr.Validation, "update manager")
	_, err = s.Update(ctx, emp, plat.ID, Input{Name: "X"})
	is(t, err, apperr.Forbidden, "employee update")

	// Members.
	if err := s.SetMembers(ctx, admin, plat.ID, []string{"maria", "petar"}, nil); err != nil {
		t.Fatal(err)
	}
	mb, _ := m.GetMember(ctx, tn, "maria")
	if mb.DepartmentID != plat.ID || mb.DisplayName != "Maria" || !mb.Active {
		t.Fatalf("member: %+v", mb)
	}
	if err := s.SetMembers(ctx, admin, eng.ID, []string{"maria"}, []string{"petar", "nobody"}); err != nil {
		t.Fatal(err) // petar is not in eng: removal is a no-op
	}
	mb, _ = m.GetMember(ctx, tn, "maria")
	pb, _ := m.GetMember(ctx, tn, "petar")
	if mb.DepartmentID != eng.ID || pb.DepartmentID != plat.ID {
		t.Fatal("move member")
	}
	if err := s.SetMembers(ctx, admin, plat.ID, nil, []string{"petar"}); err != nil {
		t.Fatal(err)
	}
	if pb, _ = m.GetMember(ctx, tn, "petar"); pb.DepartmentID != "" {
		t.Fatal("unassign")
	}
	is(t, s.SetMembers(ctx, admin, plat.ID, []string{"ghost"}, nil), apperr.Validation, "inactive member")
	is(t, s.SetMembers(ctx, admin, plat.ID, []string{"maria"}, []string{"maria"}), apperr.Validation, "add and remove")
	is(t, s.SetMembers(ctx, admin, plat.ID, make([]string, 1001), nil), apperr.Validation, "too many")
	is(t, s.SetMembers(ctx, admin, "0190f7c2-6a3e-7c1a-9b2e-000000000000", []string{"maria"}, nil), apperr.NotFound, "missing department")
	is(t, s.SetMembers(ctx, emp, plat.ID, []string{"maria"}, nil), apperr.Forbidden, "employee members")
	p.err = errors.New("down")
	is(t, s.SetMembers(ctx, admin, plat.ID, []string{"maria"}, nil), apperr.TemporarilyUnavailable, "auth down")
	p.err = nil

	// Tree for routing: members and inactive flags.
	mb.Active = false
	_ = m.UpsertMember(ctx, mb)
	tree, err := s.Tree(ctx, tn)
	if err != nil || tree.MemberOf["maria"] != eng.ID || tree.Active("maria") || !tree.Active("stranger") {
		t.Fatalf("tree: %v", err)
	}
	if got := tree.Approvers("petar", []string{"hana"}); !slices.Equal(got, []string{"hana"}) {
		t.Fatalf("unassigned petar routes to admins: %v", got)
	}

	// Lists and delete.
	if ds, err := s.List(ctx, emp); err != nil || len(ds) != 4 {
		t.Fatalf("list: %d %v", len(ds), err)
	}
	if ms, err := s.Members(ctx, emp); err != nil || len(ms) != 2 {
		t.Fatalf("members: %d %v", len(ms), err)
	}
	_, err = s.List(ctx, authz.User(tn, "x", nil))
	is(t, err, apperr.Forbidden, "list without calendar")
	_, err = s.Members(ctx, authz.User(tn, "x", nil))
	is(t, err, apperr.Forbidden, "members without calendar")
	is(t, s.Delete(ctx, admin, eng.ID), apperr.NotEmpty, "delete with members")
	is(t, s.Delete(ctx, emp, infra.ID), apperr.Forbidden, "employee delete")
	if err := s.Delete(ctx, admin, infra.ID); err != nil {
		t.Fatal(err)
	}
	is(t, s.Delete(ctx, admin, infra.ID), apperr.NotFound, "delete deleted")

	m.Fail("ListMembers", errors.New("db"))
	if _, err := s.Tree(ctx, tn); err == nil {
		t.Fatal("tree members error")
	}
	m.Fail("ListDepartments", errors.New("db"))
	if _, err := s.Tree(ctx, tn); err == nil {
		t.Fatal("tree departments error")
	}
	if _, err := s.Create(ctx, admin, Input{Name: "Z"}); err == nil {
		t.Fatal("create tree error")
	}
	if _, err := s.Update(ctx, admin, plat.ID, Input{Name: "Z", ParentID: eng.ID, ManagerID: "petar"}); err == nil {
		t.Fatal("update tree error")
	}
	m.Fail("GetMember", errors.New("db"))
	if err := s.SetMembers(ctx, admin, plat.ID, []string{"maria"}, nil); err == nil {
		t.Fatal("get member error")
	}
	if err := s.SetMembers(ctx, admin, plat.ID, nil, []string{"maria"}); err == nil {
		t.Fatal("get member error on remove")
	}
	m.Fail("UpsertMember", errors.New("db"))
	if err := s.SetMembers(ctx, admin, eng.ID, nil, []string{"maria"}); err == nil {
		t.Fatal("upsert error on remove")
	}
	if err := s.SetMembers(ctx, admin, eng.ID, []string{"maria"}, nil); err == nil {
		t.Fatal("upsert error on add")
	}
	m.Fail("", nil)
	if treeErr(errors.New("other")) == nil {
		t.Fatal("treeErr passthrough")
	}
	d := New(Deps{})
	if d.d.MaxDepth != 10 || d.d.Now == nil {
		t.Fatal("defaults")
	}
	d.reroute(ctx, tn) // nil rerouter
	_ = store.Member{}
}
