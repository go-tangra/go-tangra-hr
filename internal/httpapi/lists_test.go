package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-hr/v4/internal/repo/repotest"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// The list endpoints answer the list contract (go-tangra specs/032): invalid
// page, page_size, sort or order values are validation_failed naming the
// parameter and never echoing the value.
func TestListParameterRefusals(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/requests", "/allowances", "/holidays", "/absence-types"} {
		for q, param := range map[string]string{
			"sort=evil_column": "sort", "order=sideways": "order", "page=0": "page", "page=abc": "page",
			"page_size=0": "page_size", "page_size=201": "page_size", "page=2147483648": "page",
		} {
			w := call(e.s, "GET", Prefix+path+"?"+q, "hana", nil, "")
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s?%s: %d %s", path, q, w.Code, w.Body)
			}
			body := w.json(t)
			detail, _ := body["detail"].(map[string]any)
			if body["reason"] != ReasonValidationFailed || detail["param"] != param {
				t.Fatalf("%s?%s: %s", path, q, w.Body)
			}
			if v := q[strings.Index(q, "=")+1:]; strings.Contains(w.Body.String(), v) && v != "0" {
				t.Fatalf("%s?%s echoes the value: %s", path, q, w.Body)
			}
		}
	}
}

// parseList enforces the same rules when the validator is not in front.
func TestParseListDefence(t *testing.T) {
	for q, param := range map[string]string{"sort=nope": "sort", "order=x": "order", "page=-1": "page", "page_size=999": "page_size"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/x?"+q, nil)
		if _, ok := parseList(w, r, store.RequestList); ok || w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"param":"`+param+`"`) {
			t.Fatalf("%s: %v %d %s", q, ok, w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	req, ok := parseList(w, httptest.NewRequest("GET", "/x", nil), store.AllowanceList)
	if !ok || req.Page != 1 || req.PageSize != 50 || req.Sort != "year" || req.Order != "desc" {
		t.Fatalf("defaults: %+v", req)
	}
}

func pageOf(t *testing.T, w resp) (items []map[string]any, total, page int) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	m := w.json(t)
	for _, k := range []string{"items", "total", "page", "page_size", "sort", "order"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("page lacks %s: %s", k, w.Body)
		}
	}
	for _, it := range m["items"].([]any) {
		items = append(items, it.(map[string]any))
	}
	return items, int(m["total"].(float64)), int(m["page"].(float64))
}

func names(items []map[string]any, key string) string {
	var out []string
	for _, it := range items {
		out = append(out, str(it[key]))
	}
	return strings.Join(out, ",")
}

func TestHolidaysAndTypesArePaged(t *testing.T) {
	e := newEnv(t)
	for _, h := range []struct{ date, name string }{{"2026-01-01", "New year"}, {"2026-03-03", "liberation"}, {"2026-05-01", "Labour"},
		{"2026-05-06", "army"}, {"2026-12-25", "Christmas"}} {
		expect(t, callJSON(e.s, "POST", Prefix+"/holidays", "hana", map[string]any{"date": h.date, "name": h.name}), 201, "holiday")
	}
	items, total, page := pageOf(t, call(e.s, "GET", Prefix+"/holidays?year=2026&page_size=2", "maria", nil, ""))
	if total != 5 || page != 1 || names(items, "date") != "2026-01-01,2026-03-03" {
		t.Fatalf("holidays default (date asc): %d %s", total, names(items, "date"))
	}
	items, _, _ = pageOf(t, call(e.s, "GET", Prefix+"/holidays?year=2026&page=2&page_size=2&sort=name&order=desc", "maria", nil, ""))
	if names(items, "name") != "Labour,Christmas" {
		t.Fatalf("holidays by name desc page 2: %s", names(items, "name"))
	}
	items, _, page = pageOf(t, call(e.s, "GET", Prefix+"/holidays?year=2026&page=99&page_size=2", "maria", nil, ""))
	if page != 3 || names(items, "name") != "Christmas" {
		t.Fatalf("past the end is the last page: %d %s", page, names(items, "name"))
	}

	for i, n := range []string{"zeta", "Alpha", "beta"} {
		expect(t, callJSON(e.s, "POST", Prefix+"/absence-types", "hana", map[string]any{"name": n, "sort_order": 3 - i}), 201, "type")
	}
	items, total, _ = pageOf(t, call(e.s, "GET", Prefix+"/absence-types?all=true", "hana", nil, ""))
	if total != 3 || names(items, "name") != "beta,Alpha,zeta" {
		t.Fatalf("types by sort order: %s", names(items, "name"))
	}
	items, _, _ = pageOf(t, call(e.s, "GET", Prefix+"/absence-types?sort=name&page_size=2", "cal", nil, ""))
	if names(items, "name") != "Alpha,beta" {
		t.Fatalf("types by name: %s", names(items, "name"))
	}
}

func TestRequestsAndAllowancesSortByName(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ty := repotest.Type(tenantA, "Annual")
	if err := e.m.CreateAbsenceType(ctx, ty); err != nil {
		t.Fatal(err)
	}
	for i, u := range []string{"maria", "petar", "vera"} {
		if err := e.m.UpsertMember(ctx, store.Member{TenantID: tenantA, UserID: u, DisplayName: map[string]string{"maria": "maria", "petar": "Bob", "vera": ""}[u], Active: true}); err != nil {
			t.Fatal(err)
		}
		r := repotest.Request(tenantA, u, ty.ID, repotest.Date(2026, 7, 6+i), repotest.Date(2026, 7, 6+i))
		if err := e.m.CreateRequest(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := e.m.CreateAllowance(ctx, repotest.Allowance(tenantA, u, 2026, ty.ID, "", 200)); err != nil {
			t.Fatal(err)
		}
	}
	items, total, _ := pageOf(t, call(e.s, "GET", Prefix+"/requests?view=all&sort=user", "hana", nil, ""))
	if total != 3 || names(items, "user_id") != "petar,maria,vera" {
		t.Fatalf("requests by name (nameless last): %s", names(items, "user_id"))
	}
	items, _, _ = pageOf(t, call(e.s, "GET", Prefix+"/requests?view=all&sort=user&order=desc&page_size=2", "hana", nil, ""))
	if names(items, "user_id") != "maria,petar" {
		t.Fatalf("requests by name desc: %s", names(items, "user_id"))
	}
	items, _, _ = pageOf(t, call(e.s, "GET", Prefix+"/requests?view=all", "hana", nil, ""))
	if names(items, "user_id") != "vera,petar,maria" {
		t.Fatalf("requests default (start desc): %s", names(items, "user_id"))
	}
	items, total, _ = pageOf(t, call(e.s, "GET", Prefix+"/allowances?year=2026&sort=user", "vera", nil, ""))
	if total != 3 || names(items, "user_id") != "petar,maria,vera" {
		t.Fatalf("allowances by name: %s", names(items, "user_id"))
	}
	if _, total, _ := pageOf(t, call(e.s, "GET", Prefix+"/allowances", "maria", nil, "")); total != 1 {
		t.Fatalf("own allowances only: %d", total)
	}
}
