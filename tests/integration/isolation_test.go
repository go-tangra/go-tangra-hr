//go:build integration

package integration

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-hr/v4/internal/httpapi"
)

// T064 / SC-004: the administrator of tenant B (every hr permission) gets
// "not found" on every parameterised route of the OpenAPI document that
// names a tenant-A object, changes nothing, and sees none of tenant A in its
// listings or backup; an employee gets "not found" for a colleague's request
// detail, decisions, document, allowance and balance.
func TestTenantIsolation(t *testing.T) {
	e := newEnv(t, envOpt{})
	f := e.setup(t, fixtureOpt{})
	pending := e.request(t, "maria", f.annual, "2026-08-03", "2026-08-04", map[string]any{"reason": "Sea"})
	approved := e.request(t, "maria", f.annual, "2026-09-07", "2026-09-07", nil)
	expect(t, e.json("POST", "/requests/"+approved+"/approve", "petar", nil), 200, "approve")
	r := e.json("POST", "/holidays", "hana", map[string]any{"date": "2026-12-24", "name": "Eve"})
	expect(t, r, 201, "holiday")
	holiday := str(r.json(t)["id"])
	aIDs := []string{f.pool, f.annual, f.unpaid, f.signed, f.annualAllowance, f.signedAllowance, f.department, pending, approved, holiday}

	ids := map[string]string{"/absence-types/{id}": f.signed, "/pools/{id}": f.pool, "/allowances/{id}": f.annualAllowance,
		"/requests/{id}": pending, "/departments/{id}": f.department, "/holidays/{id}": holiday}
	bodies := map[string]any{
		"updateAbsenceType": map[string]any{"name": "x"}, "updatePool": map[string]any{"name": "x"},
		"updateAllowance": map[string]any{"user_id": "maria", "year": 2026, "pool_id": f.pool, "total_days": 1},
		"updateRequest":   map[string]any{"reason": "x"},
		"approveRequest":  map[string]any{"notes": "x"}, "rejectRequest": map[string]any{"notes": "x"}, "revokeRequest": map[string]any{"notes": "x"},
		"updateDepartment":     map[string]any{"name": "x"},
		"setDepartmentMembers": map[string]any{"add": []string{"outsider"}},
		"updateHoliday":        map[string]any{"date": "2026-08-06", "name": "Y"},
	}
	doc, err := httpapi.LoadDocument()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for p, item := range doc.Paths.Map() {
		if !strings.Contains(p, "{") {
			continue
		}
		for method, op := range item.Operations() {
			path := strings.TrimPrefix(p, prefix)
			for pfx, id := range ids {
				if strings.HasPrefix(path, pfx) {
					path = strings.Replace(path, "{id}", id, 1)
				}
			}
			path = strings.ReplaceAll(path, "{user_id}", "maria")
			if strings.Contains(path, "{") {
				t.Errorf("%s %s: unmapped parameter", method, p)
				continue
			}
			res := e.json(method, path, "outsider", bodies[op.OperationID])
			checked++
			switch {
			case res.Code == 404:
			case op.OperationID == "getBalance" && res.Code == 200:
				// The balance of a user id in the caller's own tenant: empty.
				if lines, _ := res.json(t)["lines"].([]any); len(lines) != 0 {
					t.Errorf("tenant B sees tenant A balance lines: %s", res.Body)
				}
			default:
				t.Errorf("%s %s (%s): tenant B got %d: %s", method, path, op.OperationID, res.Code, res.Body)
			}
			for _, id := range aIDs {
				if res.Code != 404 && strings.Contains(res.Body.String(), id) {
					t.Errorf("%s %s answered tenant B with tenant-A id %s", method, path, id)
				}
			}
		}
	}
	if checked < 20 {
		t.Fatalf("only %d parameterised routes checked", checked)
	}
	t.Logf("%d parameterised routes checked as tenant B", checked)
	// Nothing of tenant A changed.
	if st := e.status(t, pending); st["status"] != "pending" || st["reason"] != "Sea" {
		t.Fatalf("pending request changed by tenant B: %v", st)
	}
	if st := e.status(t, approved); st["status"] != "approved" {
		t.Fatalf("approved request changed by tenant B: %v", st)
	}
	if al := e.allowance(t, f.annualAllowance); al["total_days"] != float64(20) {
		t.Fatalf("allowance changed by tenant B: %v", al)
	}
	for table, n := range map[string]int{"hr_absence_types": 3, "hr_pools": 1, "hr_allowances": 2, "hr_departments": 1, "hr_holidays": 1, "hr_requests": 2} {
		if c := e.count(t, "SELECT count(*) FROM "+table+" WHERE tenant_id = $1", tenantA); c != n {
			t.Errorf("%s: %d rows of tenant A, want %d", table, c, n)
		}
	}
	if c := e.count(t, "SELECT count(*) FROM hr_members WHERE user_id = 'outsider'"); c != 0 {
		t.Error("tenant B added a member to a tenant-A department")
	}

	// Listings and the backup of tenant B show nothing of tenant A.
	for _, path := range []string{"/requests?view=all", "/requests?view=review", "/requests", "/allowances", "/absence-types?all=true", "/pools",
		"/departments", "/holidays?year=2026", "/people", "/calendar?from=2026-08-01&to=2026-09-30", "/stats", "/me"} {
		r := e.json("GET", path, "outsider", nil)
		expect(t, r, 200, "tenant B "+path)
		for _, id := range aIDs {
			if strings.Contains(r.Body.String(), id) {
				t.Errorf("%s of tenant B lists tenant-A id %s", path, id)
			}
		}
		for _, name := range []string{"Maria", "Petar", "Platform", "Annual leave", "Sea"} {
			if strings.Contains(r.Body.String(), name) {
				t.Errorf("%s of tenant B shows tenant-A %q", path, name)
			}
		}
	}
	ex := e.call("POST", prefix+"/backup/export", "outsider", nil, "")
	expect(t, ex, 200, "tenant B export")
	_, content := unpack(t, ex.Body.Bytes())
	for _, id := range aIDs {
		if strings.Contains(content, id) {
			t.Errorf("tenant B's backup contains %s", id)
		}
	}
	// Importing tenant A's archive as tenant B moves every row into tenant B
	// (fresh tenant); tenant A is untouched.
	exA := e.call("POST", prefix+"/backup/export", "hana", nil, "")
	expect(t, exA, 200, "tenant A export")
	before := e.count(t, "SELECT count(*) FROM hr_requests WHERE tenant_id = $1", tenantA)
	im := e.call("POST", prefix+"/backup/import?mode=overwrite", "outsider", bytes.NewReader(exA.Body.Bytes()), "application/gzip")
	// Tenant A's ids already exist (in tenant A): the import is refused as a
	// whole instead of writing across tenants.
	if im.Code != 422 || !strings.Contains(im.Body.String(), "conflicting_rows") {
		t.Fatalf("tenant B import of tenant A's archive: %d %s", im.Code, im.Body)
	}
	if after := e.count(t, "SELECT count(*) FROM hr_requests WHERE tenant_id = $1", tenantA); after != before {
		t.Fatal("tenant B's import changed tenant A")
	}
	if st := e.status(t, pending); st["status"] != "pending" || st["reason"] != "Sea" {
		t.Fatalf("tenant A request after tenant B's import: %v", st)
	}
}

// SC-004: employees never read or decide colleagues' requests.
func TestColleagueIsolation(t *testing.T) {
	e := newEnv(t, envOpt{})
	f := e.setup(t, fixtureOpt{})
	r := e.json("POST", "/allowances", "hana", map[string]any{"user_id": "petar", "year": 2026, "pool_id": f.pool, "total_days": 20})
	expect(t, r, 201, "petar allowance")
	petarAllowance := str(r.json(t)["id"])
	petarReq := e.request(t, "petar", f.annual, "2026-08-10", "2026-08-11", map[string]any{"reason": "Petar private"})
	approvedReq := e.request(t, "petar", f.annual, "2026-09-14", "2026-09-14", nil)
	expect(t, e.json("POST", "/requests/"+approvedReq+"/approve", "hana", nil), 200, "hana approves petar")

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/requests/" + petarReq, nil},
		{"PUT", "/requests/" + petarReq, map[string]any{"reason": "x"}},
		{"DELETE", "/requests/" + petarReq, nil},
		{"POST", "/requests/" + petarReq + "/approve", map[string]any{"notes": "x"}},
		{"POST", "/requests/" + petarReq + "/reject", map[string]any{"notes": "x"}},
		{"POST", "/requests/" + petarReq + "/cancel", nil},
		{"POST", "/requests/" + approvedReq + "/revoke", map[string]any{"notes": "x"}},
		{"GET", "/requests/" + approvedReq + "/signed-document", nil},
		{"GET", "/allowances/" + petarAllowance, nil},
		{"GET", "/balance/petar?year=2026", nil},
	} {
		for _, who := range []string{"maria", "cal"} {
			res := e.json(c.method, c.path, who, c.body)
			if res.Code != 404 && !(who == "cal" && res.Code == 403) {
				t.Errorf("%s %s %s: %d %s", who, c.method, c.path, res.Code, res.Body)
			}
			if strings.Contains(res.Body.String(), "Petar private") {
				t.Errorf("%s read petar's reason via %s %s", who, c.method, c.path)
			}
		}
	}
	// Maria's own lists show nothing of petar's requests; the calendar shows
	// the absence without its reason.
	for _, path := range []string{"/requests", "/requests?view=review"} {
		res := e.json("GET", path, "maria", nil)
		expect(t, res, 200, "maria "+path)
		if strings.Contains(res.Body.String(), petarReq) || strings.Contains(res.Body.String(), approvedReq) {
			t.Errorf("maria's %s lists petar's requests", path)
		}
	}
	expect(t, e.json("GET", "/requests?view=all", "maria", nil), 403, "maria all requests")
	cal := e.json("GET", "/calendar?from=2026-08-01&to=2026-09-30", "maria", nil)
	expect(t, cal, 200, "calendar")
	if strings.Contains(cal.Body.String(), "Petar private") {
		t.Error("reason in the calendar")
	}
	// Petar's request is untouched.
	if st := e.status(t, petarReq); st["status"] != "pending" || st["reason"] != "Petar private" {
		t.Fatalf("petar's request changed: %v", st)
	}
	if st := e.status(t, approvedReq); st["status"] != "approved" {
		t.Fatalf("petar's approved request changed: %v", st)
	}
}
