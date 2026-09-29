//go:build integration

// Package integration runs the whole hr service (app.Build) against a real
// TimescaleDB under the NOBYPASSRLS app role (and, for the signing outcome
// consumer, a real Valkey): concurrent approvals against one allowance and
// the overlap exclusion (T042, SC-005), the signing outcome consumer with
// restart replay and the reconcile task (T053, SC-002/SC-003), the
// end-to-end leak test (T063, SC-008) and cross-tenant / colleague isolation
// of every parameterised route (T064, SC-004). Run with:
//
//	go test -tags integration ./tests/integration/
//
// It skips cleanly when Docker/testcontainers is unavailable.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra-notification/sdk/v4/pkg/notifyclient"
	"github.com/go-tangra/go-tangra-signing/sdk/v4/pkg/signingclient"
	"github.com/go-tangra/go-tangra/v4"

	"github.com/go-tangra/go-tangra-hr/v4/internal/app"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/config"
	"github.com/go-tangra/go-tangra-hr/v4/internal/people"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream/valkeykv"
)

const (
	tenantA = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	tenantB = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
	prefix  = "/api/hr/v1"
)

var ctx = context.Background()

// ---- containers (one of each per package run)

type dbEnv struct{ adminDSN, appDSN string }

var (
	dbOnce sync.Once
	dbVal  dbEnv
	dbErr  error

	vkOnce sync.Once
	vkAddr string
	vkErr  error
)

// startDB starts one TimescaleDB for the package: migrated as postgres, the
// service connecting as hr_app (NOBYPASSRLS), as the stack's init-db does.
func startDB(t *testing.T) dbEnv {
	t.Helper()
	dbOnce.Do(func() {
		c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image: "timescale/timescaledb:latest-pg16", ExposedPorts: []string{"5432/tcp"},
				Env:        map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "hr"},
				WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
			}, Started: true,
		})
		if err != nil {
			dbErr = err
			return
		}
		host, _ := c.Host(ctx)
		port, _ := c.MappedPort(ctx, "5432/tcp")
		dbVal = dbEnv{
			adminDSN: "postgres://postgres:test@" + host + ":" + port.Port() + "/hr?sslmode=disable",
			appDSN:   "postgres://hr_app:app@" + host + ":" + port.Port() + "/hr?sslmode=disable",
		}
		conn, err := pgx.Connect(ctx, dbVal.adminDSN)
		if err != nil {
			dbErr = err
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "CREATE ROLE hr_app LOGIN PASSWORD 'app' NOBYPASSRLS"); err != nil {
			dbErr = err
			return
		}
		dbErr = store.Migrate(ctx, dbVal.adminDSN)
	})
	if dbErr != nil {
		t.Skipf("database unavailable: %v", dbErr)
	}
	return dbVal
}

// startValkey starts one Valkey for the package (plaintext, dev only).
func startValkey(t *testing.T) string {
	t.Helper()
	vkOnce.Do(func() {
		c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image: "valkey/valkey:8", ExposedPorts: []string{"6379/tcp"},
				WaitingFor: wait.ForLog("Ready to accept connections").WithStartupTimeout(time.Minute),
			}, Started: true,
		})
		if err != nil {
			vkErr = err
			return
		}
		host, _ := c.Host(ctx)
		port, _ := c.MappedPort(ctx, "6379/tcp")
		vkAddr = host + ":" + port.Port()
	})
	if vkErr != nil {
		t.Skipf("valkey unavailable: %v", vkErr)
	}
	return vkAddr
}

// ---- identities: hana (HR administrator), vera (HR viewer), maria and
// petar (employees; petar manages the department), cal (calendar viewer),
// outsider (HR administrator of tenant B).

type verifier map[string]authclient.Identity

func (v verifier) Verify(_ context.Context, tok string) (authclient.Identity, error) {
	id, ok := v[tok]
	if !ok {
		return authclient.Identity{}, errors.New("unauthenticated")
	}
	return id, nil
}

var tokens = verifier{
	"hana": {UserID: "hana", TenantID: tenantA}, "vera": {UserID: "vera", TenantID: tenantA}, "maria": {UserID: "maria", TenantID: tenantA},
	"petar": {UserID: "petar", TenantID: tenantA}, "cal": {UserID: "cal", TenantID: tenantA}, "outsider": {UserID: "outsider", TenantID: tenantB},
}

var perms = authz.Static{
	"hana": authz.Permissions, "vera": {authz.Calendar, authz.Read}, "maria": {authz.Calendar, authz.Request},
	"petar": {authz.Calendar, authz.Request}, "cal": {authz.Calendar}, "outsider": authz.Permissions,
}

func directory() *people.Fake {
	return &people.Fake{Users: map[string][]people.Contact{
		tenantA: {
			{UserID: "hana", DisplayName: "Hana", Email: "hana@a.example"}, {UserID: "vera", DisplayName: "Vera", Email: "vera@a.example"},
			{UserID: "maria", DisplayName: "Maria", Email: "maria@a.example"}, {UserID: "petar", DisplayName: "Petar", Email: "petar@a.example"},
			{UserID: "cal", DisplayName: "Cal", Email: "cal@a.example"},
		},
		tenantB: {{UserID: "outsider", DisplayName: "Otto", Email: "otto@b.example"}},
	}}
}

// ---- captured side channels

// mails captures what the outbox worker sends.
type mails struct {
	mu   sync.Mutex
	sent []map[string]string
}

func (m *mails) SendKey(_ context.Context, _, key, to string, vars map[string]string, _ string) (notifyclient.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := map[string]string{"_key": key, "_to": to}
	for k, v := range vars {
		cp[k] = v
	}
	m.sent = append(m.sent, cp)
	return notifyclient.Result{Sent: true}, nil
}

func (m *mails) dump() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(m.sent)
	return string(b)
}

// signingFake is the signing module's module API: one active template, and
// submissions whose state the test sets (GetSubmission drives reconcile).
type signingFake struct {
	mu      sync.Mutex
	created []signingclient.CreateInput
	states  map[string]signingclient.State
	byKey   map[string]string
}

func newSigningFake() *signingFake {
	return &signingFake{states: map[string]signingclient.State{}, byKey: map[string]string{}}
}

func (f *signingFake) ListTemplates(context.Context, string) ([]signingclient.Template, error) {
	return []signingclient.Template{{ID: "t1", Name: "Leave form",
		Parties: []signingclient.Party{{ID: "p1", Name: "Employee"}, {ID: "p2", Name: "Approver"}},
		Fields:  []signingclient.Field{{ID: "name", Name: "Name", Type: "text", Party: "p1", TextValued: true}}}}, nil
}

func (f *signingFake) CreateAndSend(_ context.Context, in signingclient.CreateInput) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byKey[in.IdempotencyKey]; ok {
		return id, nil
	}
	id := store.NewID()
	f.created = append(f.created, in)
	f.byKey[in.IdempotencyKey] = id
	f.states[id] = signingclient.State{Status: "in_progress"}
	return id, nil
}

func (f *signingFake) GetSubmission(_ context.Context, _, id string) (signingclient.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.states[id]
	if !ok {
		return st, &signingclient.Error{Kind: signingclient.ErrNotFound, Reason: "not_found"}
	}
	return st, nil
}

func (f *signingFake) set(id string, st signingclient.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[id] = st
}

func (f *signingFake) Cancel(_ context.Context, _, id, reason string) error {
	f.set(id, signingclient.State{Status: "cancelled", CancelReasonCode: "cancelled"})
	return nil
}

func (f *signingFake) Delete(context.Context, string, string) error { return nil }

func (f *signingFake) FinalDocument(context.Context, string, string) (io.ReadCloser, string, error) {
	return io.NopCloser(strings.NewReader("%PDF-1.7 signed")), "leave.pdf", nil
}

func (f *signingFake) dump() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(f.created)
	return string(b)
}

// clock is the app's controllable clock (starts at the real time).
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// ---- the environment

type env struct {
	a       *app.App
	st      *store.Store
	db      dbEnv
	stream  stream.Client
	mem     *stream.Memory // nil when stream is Valkey
	mails   *mails
	signing *signingFake
	dir     *people.Fake
	clock   *clock
	logs    *bytes.Buffer
	logMu   *sync.Mutex
}

func truncate(t *testing.T, db dbEnv) {
	t.Helper()
	conn, err := pgx.Connect(ctx, db.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `TRUNCATE hr_mail_outbox, hr_carryover_runs, hr_tenants, hr_signing_outcomes, hr_holidays,
		hr_members, hr_request_charges, hr_requests, hr_departments, hr_allowances, hr_absence_types, hr_pools, hr_audit_events CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func testConfig() config.Config {
	c := config.Default()
	c.ServiceName, c.TrustDomain, c.Env = "hr", "example.org", "dev"
	c.DB.DSN = "postgres://unused"
	c.Valkey.Addresses, c.Valkey.AllowPlaintext = []string{"127.0.0.1:1"}, true
	c.Server.GRPCAddr, c.Server.HTTPAddr, c.Admin.Addr = "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0"
	c.Discovery.Static = map[string][]string{"lcm": {"127.0.0.1:1"}, "auth": {"127.0.0.1:1"}, "gateway": {"127.0.0.1:1"},
		"notification": {"127.0.0.1:1"}, "scheduler": {"127.0.0.1:1"}, "signing": {"127.0.0.1:1"}}
	c.Gateway.Service, c.Gateway.Issuer = "gateway", "https://localhost:8443"
	c.Links.PortalBaseURL = "https://localhost:8443"
	return c
}

type envOpt struct {
	valkey bool
	dir    *people.Fake
}

// newEnv wires the real service (app.Build) against the shared database
// (truncated) with the real repodb store, fakes for auth, notification and
// the signing module, and an in-process (or Valkey) event bus.
func newEnv(t *testing.T, o envOpt) *env {
	t.Helper()
	db := startDB(t)
	truncate(t, db)
	st, err := store.Open(ctx, db.appDSN, 16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := &env{st: st, db: db, mails: &mails{}, signing: newSigningFake(), clock: &clock{t: time.Now().UTC()},
		logs: &bytes.Buffer{}, logMu: &sync.Mutex{}, dir: o.dir}
	if e.dir == nil {
		e.dir = directory()
	}
	if o.valkey {
		addr := startValkey(t)
		c, err := valkeykv.New(valkeykv.Config{Addresses: []string{addr}, AllowPlaintext: true})
		if err != nil {
			t.Fatal(err)
		}
		// Streams persist across tests on the shared server: start clean.
		for _, tn := range []string{tenantA, tenantB} {
			if err := c.XTrimMinID(ctx, stream.Key(tn), "99999999999999-0"); err != nil {
				t.Fatal(err)
			}
		}
		e.stream = c
	} else {
		e.mem = stream.NewMemory()
		e.stream = e.mem
	}
	e.a, err = app.Build(ctx, testConfig(), app.Options{
		Verifier: tokens, Checker: perms, Directory: e.dir, Notify: e.mails, Signing: e.signing,
		Repo: repodb.New(st), Stream: e.stream, Now: e.clock.Now,
		Logger: slog.NewJSONHandler(lockedWriter{e.logMu, e.logs}, &slog.HandlerOptions{Level: slog.LevelDebug}),
		Freya:  []freya.Option{freya.WithInsecureLocalDev(), freya.WithAllowAllPolicy()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.a.Close)
	if o.valkey {
		t.Cleanup(e.stream.Close)
	}
	return e
}

func (e *env) logText() string {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	return e.logs.String()
}

func (e *env) admin(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, e.db.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return conn
}

// rows returns every row of table as JSON text (admin connection, no RLS).
func (e *env) rows(t *testing.T, table string) string {
	t.Helper()
	conn := e.admin(t)
	rs, err := conn.Query(ctx, "SELECT row_to_json(t)::text FROM "+table+" t")
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var b strings.Builder
	for rs.Next() {
		var s string
		if err := rs.Scan(&s); err != nil {
			t.Fatal(err)
		}
		b.WriteString(s + "\n")
	}
	return b.String()
}

func (e *env) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	conn := e.admin(t)
	var n int
	if err := conn.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ---- HTTP

type resp struct{ *httptest.ResponseRecorder }

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil {
		t.Fatalf("not json (%d): %s", r.Code, r.Body)
	}
	return m
}

func (r resp) reason() string {
	var m map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &m)
	s, _ := m["reason"].(string)
	return s
}

func (e *env) call(method, path, token string, body io.Reader, ct string) resp {
	r := httptest.NewRequest(method, "https://localhost"+path, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if method != http.MethodGet {
		r.Header.Set("X-CSRF-Token", "t")
	}
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	w := httptest.NewRecorder()
	e.a.HTTP.Handler().ServeHTTP(w, r)
	return resp{w}
}

func (e *env) json(method, path, token string, v any) resp {
	if v == nil {
		return e.call(method, prefix+path, token, nil, "")
	}
	b, _ := json.Marshal(v)
	return e.call(method, prefix+path, token, bytes.NewReader(b), "application/json")
}

func expect(t *testing.T, r resp, status int, what string) {
	t.Helper()
	if r.Code != status {
		t.Fatalf("%s: status %d, want %d: %s", what, r.Code, status, r.Body)
	}
}

func str(v any) string { s, _ := v.(string); return s }

// fixture is tenant A's basic setup.
type fixture struct {
	pool, annual, unpaid, signed string // absence type / pool ids
	annualAllowance              string
	signedAllowance              string
	department                   string
}

type fixtureOpt struct {
	annualDays, signedDays float64
	departmentName         string
}

// setup creates a pool with an annual-leave type, an unpaid (non-deducting)
// type, a signing-required deducting type, 2026 allowances for maria, and a
// department managed by petar with maria and petar as members.
func (e *env) setup(t *testing.T, o fixtureOpt) fixture {
	t.Helper()
	if o.annualDays == 0 {
		o.annualDays = 20
	}
	if o.signedDays == 0 {
		o.signedDays = 10
	}
	if o.departmentName == "" {
		o.departmentName = "Platform"
	}
	var f fixture
	r := e.json("POST", "/pools", "hana", map[string]any{"name": "Vacation"})
	expect(t, r, 201, "pool")
	f.pool = str(r.json(t)["id"])
	r = e.json("POST", "/absence-types", "hana", map[string]any{"name": "Annual leave", "deducts": true, "pool_id": f.pool})
	expect(t, r, 201, "annual type")
	f.annual = str(r.json(t)["id"])
	r = e.json("POST", "/absence-types", "hana", map[string]any{"name": "Unpaid leave", "deducts": false})
	expect(t, r, 201, "unpaid type")
	f.unpaid = str(r.json(t)["id"])
	r = e.json("POST", "/absence-types", "hana", map[string]any{"name": "Signed leave", "deducts": true, "requires_signing": true,
		"signing": map[string]any{"template_id": "t1", "employee_party": "p1", "approver_party": "p2", "fields": map[string]any{"name": "employee_name"}}})
	expect(t, r, 201, "signed type")
	f.signed = str(r.json(t)["id"])
	r = e.json("POST", "/allowances", "hana", map[string]any{"user_id": "maria", "year": 2026, "pool_id": f.pool, "total_days": o.annualDays})
	expect(t, r, 201, "annual allowance")
	f.annualAllowance = str(r.json(t)["id"])
	r = e.json("POST", "/allowances", "hana", map[string]any{"user_id": "maria", "year": 2026, "absence_type_id": f.signed, "total_days": o.signedDays})
	expect(t, r, 201, "signed allowance")
	f.signedAllowance = str(r.json(t)["id"])
	r = e.json("POST", "/departments", "hana", map[string]any{"name": o.departmentName, "manager_id": "petar"})
	expect(t, r, 201, "department")
	f.department = str(r.json(t)["id"])
	expect(t, e.json("PUT", "/departments/"+f.department+"/members", "hana", map[string]any{"add": []string{"maria", "petar"}}), 204, "members")
	return f
}

// request creates a request as token and returns its id.
func (e *env) request(t *testing.T, token, typeID, start, end string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"absence_type_id": typeID, "start_date": start, "end_date": end}
	for k, v := range extra {
		body[k] = v
	}
	r := e.json("POST", "/requests", token, body)
	expect(t, r, 201, "create request "+start)
	return str(r.json(t)["id"])
}

func (e *env) status(t *testing.T, id string) map[string]any {
	t.Helper()
	r := e.json("GET", "/requests/"+id, "hana", nil)
	expect(t, r, 200, "get request")
	return r.json(t)
}

func (e *env) allowance(t *testing.T, id string) map[string]any {
	t.Helper()
	r := e.json("GET", "/allowances/"+id, "hana", nil)
	expect(t, r, 200, "get allowance")
	return r.json(t)
}

// submission returns the signing submission id of a request.
func (e *env) submission(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := e.st.Tx(ctx, store.Scope{TenantID: tenantA}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(submission_id::text, '') FROM hr_requests WHERE id = $1`, id).Scan(&s)
	}); err != nil || s == "" {
		t.Fatalf("submission of %s: %q %v", id, s, err)
	}
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
