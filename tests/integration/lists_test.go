//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"
)

// TestServerSideLists drives the list contract (go-tangra specs/032) through
// the whole service over the real database: every request sort field in both
// directions, the member-name sort included, pages each record exactly once;
// totals count the caller's tenant only; invalid parameters are
// validation_failed naming the parameter; internal whole-list callers (the
// calendar, the balance) still see every record.
func TestServerSideLists(t *testing.T) {
	e := newEnv(t, envOpt{})
	f := e.setup(t, fixtureOpt{})
	conn := e.admin(t)
	for u, name := range map[string]string{"maria": "maria", "petar": "Bob", "hana": ""} {
		if _, err := conn.Exec(ctx, `INSERT INTO hr_members (tenant_id, user_id, display_name) VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, user_id) DO UPDATE SET display_name = EXCLUDED.display_name`, tenantA, u, name); err != nil {
			t.Fatal(err)
		}
	}
	for i, u := range []string{"maria", "petar", "hana"} {
		for j := range 4 {
			day := fmt.Sprintf("2026-09-%02d", 1+j*7+i)
			e.request(t, u, f.unpaid, day, day, nil)
		}
	}
	// Tenant B has a request of its own.
	r := e.json("POST", "/absence-types", "outsider", map[string]any{"name": "Leave", "deducts": false})
	expect(t, r, 201, "tenant B type")
	e.request(t, "outsider", str(r.json(t)["id"]), "2026-09-01", "2026-09-01", nil)

	for _, field := range []string{"start_date", "end_date", "status", "days", "created_at", "user"} {
		for _, dir := range []string{"asc", "desc"} {
			seen := map[string]bool{}
			var order []string
			for page := 1; ; page++ {
				r := e.json("GET", fmt.Sprintf("/requests?view=all&page=%d&page_size=5&sort=%s&order=%s", page, field, dir), "hana", nil)
				expect(t, r, 200, field+" "+dir)
				m := r.json(t)
				if m["sort"] != field || m["order"] != dir || int(m["page"].(float64)) != page || int(m["total"].(float64)) != 12 {
					t.Fatalf("%s %s page %d: %s", field, dir, page, r.Body)
				}
				items := m["items"].([]any)
				for _, it := range items {
					x := it.(map[string]any)
					id := str(x["id"])
					if seen[id] {
						t.Fatalf("%s %s: %s twice", field, dir, id)
					}
					seen[id] = true
					order = append(order, str(x["user_id"]))
				}
				if page*5 >= 12 {
					break
				}
			}
			if len(seen) != 12 {
				t.Fatalf("%s %s: %d of 12", field, dir, len(seen))
			}
			if field == "user" {
				want := map[string]string{"asc": "petar", "desc": "maria"}[dir]
				if order[0] != want || order[11] != "hana" {
					t.Fatalf("user %s: %v (nameless last)", dir, order)
				}
			}
		}
	}
	// A page past the end is the last page.
	r = e.json("GET", "/requests?view=all&page=50&page_size=5", "hana", nil)
	expect(t, r, 200, "past the end")
	if m := r.json(t); int(m["page"].(float64)) != 3 || len(m["items"].([]any)) != 2 {
		t.Fatalf("past the end: %s", r.Body)
	}
	// Tenant B counts its own rows only.
	r = e.json("GET", "/requests?view=all", "outsider", nil)
	expect(t, r, 200, "tenant B list")
	if m := r.json(t); int(m["total"].(float64)) != 1 {
		t.Fatalf("tenant B total: %s", r.Body)
	}
	// Allowances: name sort over the joined member and type/pool names.
	r = e.json("GET", "/allowances?year=2026&sort=type&order=desc", "hana", nil)
	expect(t, r, 200, "allowances by type")
	if m := r.json(t); int(m["total"].(float64)) != 2 || m["items"].([]any)[0].(map[string]any)["pool_id"] != f.pool {
		t.Fatalf("allowances by type desc (Vacation first): %s", r.Body)
	}
	// Invalid parameters.
	for q, param := range map[string]string{"sort=user_id": "sort", "order=up": "order", "page=0": "page", "page_size=500": "page_size"} {
		r := e.json("GET", "/requests?"+q, "hana", nil)
		if r.Code != http.StatusUnprocessableEntity || r.reason() != "validation_failed" || r.json(t)["detail"].(map[string]any)["param"] != param {
			t.Fatalf("%s: %d %s", q, r.Code, r.Body)
		}
	}
	// Whole-list callers are unaffected by paging: the calendar shows every request.
	r = e.json("GET", "/calendar?from=2026-09-01&to=2026-09-30", "hana", nil)
	expect(t, r, 200, "calendar")
	if n := len(r.json(t)["events"].([]any)); n != 12 {
		t.Fatalf("calendar events: %d", n)
	}
}
