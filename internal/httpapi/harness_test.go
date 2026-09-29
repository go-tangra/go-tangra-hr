package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra/v4/freyatest/testrt"
	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"

	"github.com/go-tangra/go-tangra-hr/v4/internal/allowances"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/backup"
	"github.com/go-tangra/go-tangra-hr/v4/internal/catalog"
	"github.com/go-tangra/go-tangra-hr/v4/internal/departments"
	"github.com/go-tangra/go-tangra-hr/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-hr/v4/internal/people"
	"github.com/go-tangra/go-tangra-hr/v4/internal/requests"
	"github.com/go-tangra/go-tangra-hr/v4/internal/signingmap"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

const (
	tenantA = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	tenantB = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
)

// Tokens: hana (HR administrator), vera (HR viewer), maria and petar
// (employees; petar manages Platform), cal (calendar viewer), outsider (HR
// administrator of tenant B).
type tokenVerifier map[string]authclient.Identity

func (v tokenVerifier) Verify(_ context.Context, tok string) (authclient.Identity, error) {
	id, ok := v[tok]
	if !ok {
		return authclient.Identity{}, errors.New("unauthenticated")
	}
	return id, nil
}

var testVerifier = tokenVerifier{
	"hana": {UserID: "hana", TenantID: tenantA}, "vera": {UserID: "vera", TenantID: tenantA}, "maria": {UserID: "maria", TenantID: tenantA},
	"petar": {UserID: "petar", TenantID: tenantA}, "cal": {UserID: "cal", TenantID: tenantA}, "outsider": {UserID: "outsider", TenantID: tenantB},
}

var testChecker = authz.Static{
	"hana": authz.Permissions, "vera": {authz.Calendar, authz.Read}, "maria": {authz.Calendar, authz.Request},
	"petar": {authz.Calendar, authz.Request}, "cal": {authz.Calendar}, "outsider": authz.Permissions,
}

type fakeSigning struct{ err error }

func (f fakeSigning) Templates(context.Context, string) ([]signingmap.Template, error) {
	return []signingmap.Template{{ID: "t1", Name: "Leave form", Parties: []signingmap.Party{{ID: "p1", Name: "Employee"}, {ID: "p2", Name: "Approver"}},
		Fields: []signingmap.Field{{ID: "name", Name: "Name", Type: "text", Party: "p1", TextValued: true}}}}, f.err
}

func (f fakeSigning) CheckSettings(_ context.Context, _ string, s store.SigningSettings) (store.SigningSettings, error) {
	s.TemplateName = "Leave form"
	return s, f.err
}

type env struct {
	s   *Server
	m   *memstore.Mem
	dir *people.Fake
	now time.Time
}

func newServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	s, err := NewHandler(testrt.New(t, testutil.MustCA("example.org"), "hr"), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{m: memstore.New(), now: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	now := func() time.Time { return e.now }
	e.dir = &people.Fake{Users: map[string][]people.Contact{tenantA: {
		{UserID: "hana", DisplayName: "Hana"}, {UserID: "vera", DisplayName: "Vera"}, {UserID: "maria", DisplayName: "Maria"},
		{UserID: "petar", DisplayName: "Petar"}, {UserID: "cal", DisplayName: "Cal"}}}}
	cache := &people.Cache{Dir: e.dir, Checker: testChecker, Now: now}
	cat := catalog.New(catalog.Deps{Store: e.m, Checker: testChecker, Signing: fakeSigning{}, Now: now})
	deps := departments.New(departments.Deps{Store: e.m, Checker: testChecker, People: cache, Now: now})
	al := allowances.New(allowances.Deps{Store: e.m, Checker: testChecker, Tree: deps.Tree, Calendar: cat.Calendar, Now: now})
	rq := requests.New(requests.Deps{Store: e.m, Checker: testChecker, People: cache, Calendar: cat.Calendar, Tree: deps.Tree, Now: now})
	bk := backup.New(backup.Deps{Store: e.m, Checker: testChecker, Now: now})
	e.s = newServer(t, WithVerifier(testVerifier), WithChecker(testChecker))
	hub := stream.NewHub(stream.NewMemory(), stream.Config{}, nil)
	t.Cleanup(hub.Close)
	e.s.Register(Deps{Hub: hub, Store: e.m, Checker: testChecker, Tree: deps.Tree, People: cache, Catalog: cat, Allowances: al, Departments: deps,
		Requests: rq, Signing: fakeSigning{}, Backup: bk, Now: now, Health: func() map[string]string { return map[string]string{"db": "ok"} }})
	return e
}

type resp struct{ *httptest.ResponseRecorder }

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil {
		t.Fatalf("not json (%d): %s", r.Code, r.Body)
	}
	return m
}

func call(s *Server, method, path, token string, body io.Reader, contentType string) resp {
	r := httptest.NewRequest(method, "https://localhost"+path, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if method != "GET" {
		r.Header.Set("X-CSRF-Token", "t")
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return resp{w}
}

func callJSON(s *Server, method, path, token string, v any) resp {
	if v == nil {
		return call(s, method, path, token, nil, "")
	}
	b, _ := json.Marshal(v)
	return call(s, method, path, token, bytes.NewReader(b), "application/json")
}

func expect(t *testing.T, r resp, status int, what string) {
	t.Helper()
	if r.Code != status {
		t.Fatalf("%s: status %d, want %d: %s", what, r.Code, status, r.Body)
	}
}

func str(v any) string { s, _ := v.(string); return s }

var _ = http.StatusOK
var _ = strings.Contains
