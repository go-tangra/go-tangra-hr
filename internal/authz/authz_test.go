package authz

import (
	"context"
	"errors"
	"testing"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

var bg = context.Background()

func perms() Static {
	return Static{
		"hana":  {Calendar, Request, Read, Manage}, // HR administrator
		"vera":  {Calendar, Read},                  // HR viewer
		"maria": {Calendar, Request},               // employee
		"petar": {Calendar, Request},               // manager (by department)
		"cal":   {Calendar},                        // calendar viewer
	}
}

func TestRequire(t *testing.T) {
	c := perms()
	if err := Require(bg, c, User(tn, "maria", nil), Request); err != nil {
		t.Fatal(err)
	}
	if err := Require(bg, c, User(tn, "maria", nil), Manage); !errors.Is(err, ErrForbidden) {
		t.Fatalf("employee manage: %v", err)
	}
	if err := Require(bg, c, User(tn, "maria", nil), "hr:unknown"); !errors.Is(err, ErrForbidden) {
		t.Fatal("unknown permission accepted")
	}
	if err := Require(bg, nil, User(tn, "hana", nil), Read); err == nil {
		t.Fatal("nil checker accepted")
	}
	if err := Require(bg, c, User("", "hana", nil), Read); err == nil {
		t.Fatal("no tenant accepted")
	}
	if err := Require(bg, c, System(tn), Manage); err != nil {
		t.Fatal("system refused")
	}
	if err := Require(bg, c, Service("spiffe://example.org/svc/x"), Read); err == nil {
		t.Fatal("service accepted")
	}
	if !Allowed(bg, c, User(tn, "vera", nil), Read) || Allowed(bg, c, User(tn, "vera", nil), Manage) {
		t.Fatal("Allowed")
	}
	f := CheckerFunc(func(_ context.Context, tenant, user, perm string) bool {
		return tenant == tn && user == "u" && perm == Calendar
	})
	if !f.Has(bg, tn, "u", Calendar) || f.Has(bg, tn, "u", Read) {
		t.Fatal("CheckerFunc")
	}
}

func TestSubjects(t *testing.T) {
	u := User(tn, "maria", []string{"member"})
	if !u.IsMember() || u.ActorID() != "maria" || !u.HasRole("member") || u.HasRole("admin") || u.IsPlatformAdmin() {
		t.Fatalf("user: %+v", u)
	}
	if s := System(tn); s.ActorID() != ActorSystem || !s.IsPlatformAdmin() || s.IsMember() || s.TenantID != tn {
		t.Fatal("system")
	}
	if s := Service("spiffe://x"); s.IsPlatformAdmin() || s.IsMember() {
		t.Fatal("service")
	}
	if (Subjects{ActorKind: ActorService}).ActorID() != ActorService {
		t.Fatal("actor fallback")
	}
	if !User(tn, "x", []string{RolePlatformAdmin}).IsPlatformAdmin() {
		t.Fatal("platform-admin role")
	}
	if !User(PlatformTenant, "x", []string{"owner"}).IsPlatformAdmin() || !User(PlatformTenant, "x", []string{"admin"}).IsPlatformAdmin() {
		t.Fatal("platform tenant admins")
	}
	if User(PlatformTenant, "x", []string{"member"}).IsPlatformAdmin() || User(tn, "x", []string{"admin"}).IsPlatformAdmin() {
		t.Fatal("non platform admins")
	}
	if RequirePlatformAdmin(System(tn)) != nil || RequirePlatformAdmin(u) == nil {
		t.Fatal("RequirePlatformAdmin")
	}
	if !Known(Manage) || Known("x") {
		t.Fatal("Known")
	}
	if r, a, ok := Split(Manage); !ok || r != "hr" || a != "manage" {
		t.Fatal("Split")
	}
	if _, _, ok := Split("bad"); ok {
		t.Fatal("Split bad")
	}
}

func TestRelations(t *testing.T) {
	c := perms()
	f := Facts{OwnerID: "maria", ApproverIDs: []string{"petar"}, ManagerIDs: []string{"petar", "ivan"}, ReviewedBy: "petar"}
	maria, petar, ivan, hana, vera, other := User(tn, "maria", nil), User(tn, "petar", nil), User(tn, "ivan", nil),
		User(tn, "hana", nil), User(tn, "vera", nil), User(tn, "other", nil)

	// Acting for someone.
	if ActFor(bg, c, maria, "maria") != nil || ActFor(bg, c, maria, "ivan") == nil || ActFor(bg, c, hana, "maria") != nil {
		t.Fatal("ActFor")
	}
	// Person visibility.
	for _, s := range []Subjects{maria, petar, ivan, vera, System(tn)} {
		if !CanViewPerson(bg, c, s, f) {
			t.Fatalf("view person %s", s.UserID)
		}
	}
	if CanViewPerson(bg, c, other, f) {
		t.Fatal("other sees person")
	}
	// Request visibility: approver not a manager, reviewer.
	g := Facts{OwnerID: "maria", ApproverIDs: []string{"hana2"}, ReviewedBy: "rev"}
	if !CanViewRequest(bg, c, User(tn, "hana2", nil), g) || !CanViewRequest(bg, c, User(tn, "rev", nil), g) || CanViewRequest(bg, c, other, g) {
		t.Fatal("CanViewRequest")
	}
	// Review.
	if !errors.Is(Review(bg, c, maria, f), ErrSelfReview) {
		t.Fatal("self review")
	}
	if !errors.Is(Review(bg, c, User(tn, "hana", nil), Facts{OwnerID: "hana"}), ErrSelfReview) {
		t.Fatal("hr admin self review")
	}
	if Review(bg, c, petar, f) != nil || Review(bg, c, hana, f) != nil || Review(bg, c, System(tn), f) != nil {
		t.Fatal("routed/manage/system review refused")
	}
	if !errors.Is(Review(bg, c, ivan, f), ErrNotRouted) || !errors.Is(Review(bg, c, vera, f), ErrNotRouted) {
		t.Fatal("unrouted review accepted")
	}
	// Download.
	for _, s := range []Subjects{maria, petar, ivan, hana, System(tn), User(tn, "rev", nil)} {
		if !CanDownload(bg, c, s, Facts{OwnerID: "maria", ApproverIDs: []string{"petar"}, ManagerIDs: []string{"ivan"}, ReviewedBy: "rev"}) {
			t.Fatalf("download %s", s.UserID)
		}
	}
	if CanDownload(bg, c, vera, f) || CanDownload(bg, c, other, f) {
		t.Fatal("download by viewer/other")
	}
	if Is(Service("x"), "x") || In(Service("x"), []string{"x"}) || Is(maria, "") {
		t.Fatal("Is/In")
	}
}
