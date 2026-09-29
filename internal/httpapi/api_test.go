package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func TestServerBasics(t *testing.T) {
	dist := fstest.MapFS{"mf-manifest.json": {Data: []byte(`{}`)}, "assets/a.js": {Data: []byte("x")}}
	s := newServer(t, WithRemote(dist))
	if w := call(s, "GET", "/ui/mf-manifest.json", "", nil, ""); w.Code != 200 {
		t.Fatal("remote")
	}
	if w := call(s, "GET", Prefix+"/calendar?from=2026-01-01&to=2026-01-31", "", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("no verifier refuses")
	}
	if w := call(s, "GET", Prefix+"/health", "", nil, ""); w.Code != http.StatusNotImplemented {
		t.Fatal("unwired health")
	}
	if len(s.Missing()) == 0 || len(s.Declared()) != 49 {
		t.Fatalf("declared %d", len(s.Declared()))
	}
	e := newEnv(t)
	if m := e.s.Missing(); len(m) != 0 {
		t.Fatalf("routes without handlers: %v", m)
	}
	if w := call(e.s, "GET", Prefix+"/health", "", nil, ""); w.Code != 200 || w.json(t)["status"] != "ok" {
		t.Fatal("health")
	}
	if w := call(e.s, "GET", Prefix+"/me", "bogus", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("bad token")
	}
	if w := call(e.s, "GET", Prefix+"/stats", "maria", nil, ""); w.Code != http.StatusForbidden {
		t.Fatal("route permission")
	}
	if w := call(e.s, "GET", "/api/hr/v1/nothing", "hana", nil, ""); w.Code != http.StatusNotFound {
		t.Fatal("unknown route")
	}
	if w := callJSON(e.s, "POST", Prefix+"/pools", "hana", map[string]any{"name": "x", "bogus": 1}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("schema validation: %d %s", w.Code, w.Body)
	}
	if w := call(e.s, "POST", Prefix+"/pools", "hana", strings.NewReader("{"), "application/json"); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed: %d", w.Code)
	}
}

func TestSetupFlow(t *testing.T) {
	e := newEnv(t)
	s := e.s
	// Pool, types, allowance.
	p := callJSON(s, "POST", Prefix+"/pools", "hana", map[string]any{"name": "Vacation", "color": "#112233", "carry_over_cap": 5})
	expect(t, p, 201, "pool")
	poolID := str(p.json(t)["id"])
	at := callJSON(s, "POST", Prefix+"/absence-types", "hana", map[string]any{"name": "Annual leave", "deducts": true, "pool_id": poolID,
		"color": "#336699", "carry_over_cap": 2.5})
	expect(t, at, 201, "type")
	typeID := str(at.json(t)["id"])
	if at.json(t)["carry_over_cap"] != 2.5 || at.json(t)["requires_approval"] != true || at.json(t)["active"] != true {
		t.Fatalf("type defaults: %s", at.Body)
	}
	expect(t, callJSON(s, "POST", Prefix+"/absence-types", "hana", map[string]any{"name": "X", "carry_over_cap": 1.25}), 422, "cap decimals")
	sick := callJSON(s, "POST", Prefix+"/absence-types", "hana", map[string]any{"name": "Sick", "requires_approval": false, "active": true})
	expect(t, sick, 201, "sick")
	signed := callJSON(s, "POST", Prefix+"/absence-types", "hana", map[string]any{"name": "Signed", "requires_signing": true,
		"signing": map[string]any{"template_id": "t1", "employee_party": "p1", "approver_party": "p2", "fields": map[string]any{"name": "employee_name"}}})
	expect(t, signed, 201, "signed type")
	if sg := signed.json(t)["signing"].(map[string]any); sg["template_name"] != "Leave form" {
		t.Fatal("template name")
	}
	chk := call(s, "GET", Prefix+"/absence-types/"+str(signed.json(t)["id"])+"/signing-check", "hana", nil, "")
	expect(t, chk, 200, "signing check")
	if chk.json(t)["ok"] != true {
		t.Fatal("check ok")
	}
	if c := call(s, "GET", Prefix+"/absence-types/"+typeID+"/signing-check", "hana", nil, ""); c.json(t)["ok"] != false {
		t.Fatal("no signing")
	}
	tpls := call(s, "GET", Prefix+"/signing/templates", "hana", nil, "")
	expect(t, tpls, 200, "templates")
	expect(t, call(s, "GET", Prefix+"/signing/templates", "maria", nil, ""), 403, "employee templates")
	expect(t, call(s, "GET", Prefix+"/absence-types", "cal", nil, ""), 200, "calendar viewer types")
	expect(t, call(s, "GET", Prefix+"/absence-types?all=true", "cal", nil, ""), 403, "all needs read")
	expect(t, call(s, "GET", Prefix+"/absence-types/"+typeID, "maria", nil, ""), 200, "get type")
	upd := callJSON(s, "PUT", Prefix+"/absence-types/"+typeID, "hana", map[string]any{"name": "Annual", "deducts": true, "pool_id": poolID})
	expect(t, upd, 200, "update type")
	expect(t, callJSON(s, "PUT", Prefix+"/pools/"+poolID, "hana", map[string]any{"name": "Holidays"}), 200, "update pool")
	expect(t, call(s, "GET", Prefix+"/pools/"+poolID, "vera", nil, ""), 200, "get pool")
	expect(t, call(s, "GET", Prefix+"/pools", "vera", nil, ""), 200, "list pools")
	expect(t, call(s, "DELETE", Prefix+"/pools/"+poolID, "hana", nil, ""), 409, "pool in use")
	al := callJSON(s, "POST", Prefix+"/allowances", "hana", map[string]any{"user_id": "maria", "year": 2026, "pool_id": poolID, "total_days": 20})
	expect(t, al, 201, "allowance")
	alID := str(al.json(t)["id"])
	expect(t, callJSON(s, "POST", Prefix+"/allowances", "hana", map[string]any{"user_id": "maria", "year": 2026, "pool_id": poolID, "total_days": 0.25}), 422, "decimals")
	expect(t, callJSON(s, "PUT", Prefix+"/allowances/"+alID, "hana", map[string]any{"user_id": "maria", "year": 2026, "pool_id": poolID,
		"total_days": 22, "carried_over": 1.5}), 200, "update allowance")
	expect(t, call(s, "GET", Prefix+"/allowances/"+alID, "maria", nil, ""), 200, "own allowance")
	lst := call(s, "GET", Prefix+"/allowances?year=2026", "vera", nil, "")
	if lst.json(t)["total"] != float64(1) {
		t.Fatalf("allowance list: %s", lst.Body)
	}
	bal := call(s, "GET", Prefix+"/balance/maria?year=2026", "maria", nil, "")
	expect(t, bal, 200, "balance")
	line := bal.json(t)["lines"].([]any)[0].(map[string]any)
	if line["remaining"] != 23.5 || line["kind"] != "pool" {
		t.Fatalf("balance line: %v", line)
	}
	expect(t, call(s, "GET", Prefix+"/balance/hana", "maria", nil, ""), 404, "other balance")

	// Departments.
	dep := callJSON(s, "POST", Prefix+"/departments", "hana", map[string]any{"name": "Platform", "manager_id": "petar"})
	expect(t, dep, 201, "department")
	depID := str(dep.json(t)["id"])
	if dep.json(t)["manager_name"] != "Petar" {
		t.Fatal("manager name")
	}
	expect(t, callJSON(s, "PUT", Prefix+"/departments/"+depID+"/members", "hana", map[string]any{"add": []string{"maria", "petar"}}), 204, "members")
	dl := call(s, "GET", Prefix+"/departments", "maria", nil, "")
	if ids := dl.json(t)["items"].([]any)[0].(map[string]any)["member_ids"].([]any); len(ids) != 2 {
		t.Fatalf("members: %v", ids)
	}
	expect(t, callJSON(s, "PUT", Prefix+"/departments/"+depID, "hana", map[string]any{"name": "Platform team", "manager_id": "petar"}), 200, "rename")
	me := call(s, "GET", Prefix+"/me", "petar", nil, "")
	if m := me.json(t); len(m["manages"].([]any)) != 1 || m["department_id"] != depID {
		t.Fatalf("me: %v", m)
	}
	pp := call(s, "GET", Prefix+"/people?department="+depID+"&q=mar", "cal", nil, "")
	if items := pp.json(t)["items"].([]any); len(items) != 1 {
		t.Fatalf("people filter: %v", items)
	}

	// Holidays.
	hol := callJSON(s, "POST", Prefix+"/holidays", "hana", map[string]any{"date": "2026-08-05", "name": "Test holiday"})
	expect(t, hol, 201, "holiday")
	holID := str(hol.json(t)["id"])
	expect(t, callJSON(s, "PUT", Prefix+"/holidays/"+holID, "hana", map[string]any{"date": "2026-08-05", "name": "Renamed"}), 200, "update holiday")
	imp := call(s, "POST", Prefix+"/holidays/import?dry_run=true", "hana", strings.NewReader("2026-12-24,Eve\n2026-12-25,Christmas,yearly\n"), "text/plain")
	expect(t, imp, 200, "import dry run")
	if imp.json(t)["created"] != float64(2) {
		t.Fatal("dry run count")
	}
	bad := call(s, "POST", Prefix+"/holidays/import", "hana", strings.NewReader("junk\n"), "text/plain")
	expect(t, bad, 422, "import errors")
	if hl := call(s, "GET", Prefix+"/holidays?year=2026", "maria", nil, ""); len(hl.json(t)["items"].([]any)) != 1 {
		t.Fatal("holiday list")
	}

	// Requests.
	pv := callJSON(s, "POST", Prefix+"/requests/preview", "maria", map[string]any{"absence_type_id": typeID, "start_date": "2026-08-03",
		"end_date": "2026-08-07", "half_end": true})
	expect(t, pv, 200, "preview")
	if m := pv.json(t); m["days"] != 3.5 || m["approver_names"].([]any)[0] != "Petar" || m["remaining"].(map[string]any)["2026"] != 20.0 {
		t.Fatalf("preview: %v", m)
	}
	cr := callJSON(s, "POST", Prefix+"/requests", "maria", map[string]any{"absence_type_id": typeID, "start_date": "2026-08-03",
		"end_date": "2026-08-07", "reason": "Sea"})
	expect(t, cr, 201, "create request")
	reqID := str(cr.json(t)["id"])
	if m := cr.json(t); m["days"] != 4.0 || m["can_edit"] != true || m["can_review"] != false || m["user_name"] != "Maria" {
		t.Fatalf("request view: %v", m)
	}
	expect(t, callJSON(s, "POST", Prefix+"/requests", "maria", map[string]any{"absence_type_id": typeID, "start_date": "bad",
		"end_date": "2026-08-07"}), 422, "bad date")
	expect(t, callJSON(s, "PUT", Prefix+"/requests/"+reqID, "maria", map[string]any{"reason": "Mountains"}), 200, "edit")
	rv := call(s, "GET", Prefix+"/requests?view=review", "petar", nil, "")
	if items := rv.json(t)["items"].([]any); len(items) != 1 || items[0].(map[string]any)["can_review"] != true {
		t.Fatalf("review list: %v", items)
	}
	expect(t, call(s, "GET", Prefix+"/requests/"+reqID, "cal", nil, ""), 404, "calendar viewer details")
	expect(t, callJSON(s, "POST", Prefix+"/requests/"+reqID+"/approve", "maria", nil), 403, "self approval")
	ap := callJSON(s, "POST", Prefix+"/requests/"+reqID+"/approve", "petar", map[string]any{"notes": "ok"})
	expect(t, ap, 200, "approve")
	if ap.json(t)["status"] != "approved" || ap.json(t)["reviewer_name"] != "Petar" {
		t.Fatalf("approved: %s", ap.Body)
	}
	cal := call(s, "GET", Prefix+"/calendar?from=2026-08-01&to=2026-08-31&department="+depID, "cal", nil, "")
	expect(t, cal, 200, "calendar")
	if m := cal.json(t); len(m["events"].([]any)) != 1 || len(m["holidays"].([]any)) != 1 || len(m["people"].([]any)) != 2 {
		t.Fatalf("calendar: %v", m)
	}
	if strings.Contains(cal.Body.String(), "Mountains") {
		t.Fatal("reason leaked into the calendar")
	}
	expect(t, call(s, "GET", Prefix+"/calendar?from=2026-08-01", "cal", nil, ""), 422, "calendar without to")
	st := call(s, "GET", Prefix+"/stats", "vera", nil, "")
	if m := st.json(t); m["approved"] != float64(1) || m["absence_types"] != float64(3) {
		t.Fatalf("stats: %v", m)
	}
	expect(t, callJSON(s, "POST", Prefix+"/requests/"+reqID+"/revoke", "petar", map[string]any{"notes": "need you"}), 200, "revoke")
	expect(t, call(s, "DELETE", Prefix+"/requests/"+reqID, "maria", nil, ""), 409, "delete revoked")
	r2 := callJSON(s, "POST", Prefix+"/requests", "maria", map[string]any{"absence_type_id": typeID, "start_date": "2026-09-01", "end_date": "2026-09-01"})
	r2ID := str(r2.json(t)["id"])
	expect(t, callJSON(s, "POST", Prefix+"/requests/"+r2ID+"/reject", "petar", map[string]any{"notes": "no"}), 200, "reject")
	expect(t, call(s, "DELETE", Prefix+"/requests/"+r2ID, "maria", nil, ""), 204, "delete rejected")
	r3 := callJSON(s, "POST", Prefix+"/requests", "maria", map[string]any{"absence_type_id": typeID, "start_date": "2026-09-08", "end_date": "2026-09-08"})
	expect(t, callJSON(s, "POST", Prefix+"/requests/"+str(r3.json(t)["id"])+"/cancel", "maria", nil), 200, "cancel")
	expect(t, call(s, "GET", Prefix+"/requests/"+str(r3.json(t)["id"])+"/signed-document", "maria", nil, ""), 409, "not signed")
	if l := call(s, "GET", Prefix+"/requests?view=all&status=cancelled&from=2026-01-01&to=2026-12-31", "vera", nil, ""); l.json(t)["total"] != float64(1) {
		t.Fatal("list all")
	}
	expect(t, call(s, "GET", Prefix+"/requests?from=x", "maria", nil, ""), 422, "bad from")

	// Carry-over and backup.
	cp := callJSON(s, "POST", Prefix+"/carry-over/preview", "hana", map[string]any{"source_year": 2026})
	expect(t, cp, 200, "carry preview")
	if items := cp.json(t)["items"].([]any); len(items) != 1 || items[0].(map[string]any)["carried"] != 23.5 { // the PUT without carry_over_cap cleared the cap
		t.Fatalf("carry items: %v", items)
	}
	expect(t, callJSON(s, "POST", Prefix+"/carry-over", "hana", map[string]any{"source_year": 2026}), 200, "carry run")
	ex := call(s, "POST", Prefix+"/backup/export", "hana", nil, "")
	expect(t, ex, 200, "export")
	if ex.Header().Get("Content-Type") != "application/gzip" || !strings.Contains(ex.Header().Get("Content-Disposition"), "hr-") {
		t.Fatal("export headers")
	}
	archive := ex.Body.Bytes()
	im := call(s, "POST", Prefix+"/backup/import?mode=skip", "hana", bytes.NewReader(archive), "application/gzip")
	expect(t, im, 200, "import")
	expect(t, call(s, "POST", Prefix+"/backup/export", "vera", nil, ""), 403, "viewer export")
	expect(t, call(s, "POST", Prefix+"/backup/import", "hana", strings.NewReader("junk"), "application/gzip"), 422, "bad archive")

	// Deletes.
	expect(t, call(s, "DELETE", Prefix+"/holidays/"+holID, "hana", nil, ""), 204, "delete holiday")
	expect(t, call(s, "DELETE", Prefix+"/absence-types/"+str(sick.json(t)["id"]), "hana", nil, ""), 204, "delete type")
	expect(t, call(s, "DELETE", Prefix+"/allowances/"+alID, "hana", nil, ""), 204, "allowance free again after the revoke refund")
	expect(t, callJSON(s, "PUT", Prefix+"/departments/"+depID+"/members", "hana", map[string]any{"remove": []string{"maria", "petar"}}), 204, "unassign")
	expect(t, call(s, "DELETE", Prefix+"/departments/"+depID, "hana", nil, ""), 204, "delete department")
}

// TestTenantIsolation: an administrator of tenant B gets 404 for every
// tenant-A object.
func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	s := e.s
	p := callJSON(s, "POST", Prefix+"/pools", "hana", map[string]any{"name": "Vacation"})
	ty := callJSON(s, "POST", Prefix+"/absence-types", "hana", map[string]any{"name": "Annual", "requires_approval": false})
	al := callJSON(s, "POST", Prefix+"/allowances", "hana", map[string]any{"user_id": "maria", "year": 2026, "pool_id": str(p.json(t)["id"]), "total_days": 5})
	rq := callJSON(s, "POST", Prefix+"/requests", "maria", map[string]any{"absence_type_id": str(ty.json(t)["id"]), "start_date": "2026-08-03", "end_date": "2026-08-03"})
	d := callJSON(s, "POST", Prefix+"/departments", "hana", map[string]any{"name": "Eng"})
	h := callJSON(s, "POST", Prefix+"/holidays", "hana", map[string]any{"date": "2026-08-05", "name": "X"})
	ids := map[string]string{"pool": str(p.json(t)["id"]), "type": str(ty.json(t)["id"]), "allowance": str(al.json(t)["id"]),
		"request": str(rq.json(t)["id"]), "department": str(d.json(t)["id"]), "holiday": str(h.json(t)["id"])}
	for _, c := range []struct{ method, path string }{
		{"GET", "/pools/" + ids["pool"]}, {"PUT", "/pools/" + ids["pool"]}, {"DELETE", "/pools/" + ids["pool"]},
		{"GET", "/absence-types/" + ids["type"]}, {"PUT", "/absence-types/" + ids["type"]}, {"DELETE", "/absence-types/" + ids["type"]},
		{"GET", "/allowances/" + ids["allowance"]}, {"DELETE", "/allowances/" + ids["allowance"]},
		{"GET", "/requests/" + ids["request"]}, {"POST", "/requests/" + ids["request"] + "/cancel"},
		{"POST", "/requests/" + ids["request"] + "/revoke"}, {"GET", "/requests/" + ids["request"] + "/signed-document"},
		{"PUT", "/departments/" + ids["department"]}, {"DELETE", "/departments/" + ids["department"]},
		{"PUT", "/holidays/" + ids["holiday"]}, {"DELETE", "/holidays/" + ids["holiday"]},
	} {
		var body io.Reader
		ct := ""
		if c.method == "PUT" {
			ct = "application/json"
			switch {
			case strings.HasPrefix(c.path, "/holidays"):
				body = strings.NewReader(`{"date":"2026-08-06","name":"Y"}`)
			default:
				body = strings.NewReader(`{"name":"Y"}`)
			}
		}
		w := call(s, c.method, Prefix+c.path, "outsider", body, ct)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s by tenant B = %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	if l := call(s, "GET", Prefix+"/requests?view=all", "outsider", nil, ""); l.json(t)["total"] != float64(0) {
		t.Fatal("tenant B lists tenant A requests")
	}
	if l := call(s, "GET", Prefix+"/balance/maria?year=2026", "outsider", nil, ""); len(l.json(t)["lines"].([]any)) != 0 {
		t.Fatal("tenant B sees tenant A balance")
	}
}
