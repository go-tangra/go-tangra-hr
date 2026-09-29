package routing

import (
	"errors"
	"slices"
	"testing"
)

// Engineering (ivan) › Platform (petar) › Infra (no manager); Sales (no manager).
func tree() Tree {
	return Tree{
		Departments: map[string]Department{
			"eng":   {ID: "eng", ManagerID: "ivan"},
			"plat":  {ID: "plat", ParentID: "eng", ManagerID: "petar"},
			"infra": {ID: "infra", ParentID: "plat"},
			"sales": {ID: "sales"},
		},
		MemberOf: map[string]string{"maria": "plat", "petar": "plat", "ivan": "eng", "ana": "infra", "sam": "sales", "bob": "ghost"},
	}
}

var admins = []string{"hana", "zoe", "hana", "ivan"}

func TestApprovers(t *testing.T) {
	tr := tree()
	cases := map[string][]string{
		"maria":  {"petar"},               // own department manager
		"petar":  {"ivan"},                // manager → ancestor's manager
		"ivan":   {"hana", "zoe"},         // top manager → HR admins (deduplicated, sorted)
		"ana":    {"petar"},               // department without manager → nearest ancestor
		"sam":    {"hana", "ivan", "zoe"}, // no manager anywhere
		"nobody": {"hana", "ivan", "zoe"}, // unassigned
		"bob":    {"hana", "ivan", "zoe"}, // assigned to a missing department
	}
	for who, want := range cases {
		if got := tr.Approvers(who, admins); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", who, got, want)
		}
	}
	// Inactive manager is skipped.
	tr.Active = func(u string) bool { return u != "petar" }
	if got := tr.Approvers("maria", admins); !slices.Equal(got, []string{"ivan"}) {
		t.Fatalf("inactive manager: %v", got)
	}
	// HR admin requesting is excluded from the fallback.
	if got := tr.Approvers("hana", []string{"hana"}); len(got) != 0 {
		t.Fatalf("self fallback: %v", got)
	}
}

func TestManagersAndManaged(t *testing.T) {
	tr := tree()
	if got := tr.Managers("ana"); !slices.Equal(got, []string{"petar", "ivan"}) {
		t.Fatalf("managers ana: %v", got)
	}
	if got := tr.Managers("petar"); !slices.Equal(got, []string{"ivan"}) {
		t.Fatalf("managers petar: %v", got)
	}
	if got := tr.Managed("ivan"); !slices.Equal(got, []string{"eng", "infra", "plat"}) {
		t.Fatalf("managed ivan: %v", got)
	}
	if got := tr.Managed("petar"); !slices.Equal(got, []string{"infra", "plat"}) {
		t.Fatalf("managed petar: %v", got)
	}
	if got := tr.Managed("maria"); len(got) != 0 {
		t.Fatalf("managed maria: %v", got)
	}
	// A cycle (corrupt data) terminates.
	tr.Departments["eng"] = Department{ID: "eng", ParentID: "infra", ManagerID: "ivan"}
	if got := tr.Managers("ana"); !slices.Equal(got, []string{"petar", "ivan"}) {
		t.Fatalf("cycle: %v", got)
	}
}

func TestCheckParent(t *testing.T) {
	tr := tree()
	if tr.Depth("infra") != 3 || tr.Depth("eng") != 1 || tr.Depth("missing") != 0 {
		t.Fatal("depth")
	}
	if err := tr.CheckParent("new", "infra", 10); err != nil {
		t.Fatal(err)
	}
	if err := tr.CheckParent("new", "", 1); err != nil {
		t.Fatal(err)
	}
	if err := tr.CheckParent("new", "infra", 3); !errors.Is(err, ErrDepth) {
		t.Fatalf("depth new: %v", err)
	}
	if err := tr.CheckParent("new", "missing", 10); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown: %v", err)
	}
	if err := tr.CheckParent("eng", "infra", 10); !errors.Is(err, ErrCycle) {
		t.Fatalf("cycle: %v", err)
	}
	if err := tr.CheckParent("plat", "plat", 10); !errors.Is(err, ErrCycle) {
		t.Fatalf("self parent: %v", err)
	}
	// Moving plat (height 2) under sales (depth 1) → depth 3.
	if err := tr.CheckParent("plat", "sales", 3); err != nil {
		t.Fatal(err)
	}
	if err := tr.CheckParent("plat", "sales", 2); !errors.Is(err, ErrDepth) {
		t.Fatalf("subtree depth: %v", err)
	}
	// Moving eng (height 3) to the top with max 2.
	if err := tr.CheckParent("eng", "", 2); !errors.Is(err, ErrDepth) {
		t.Fatalf("top depth: %v", err)
	}
	if err := tr.CheckParent("eng", "", 3); err != nil {
		t.Fatal(err)
	}
}
