package app

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra/v4"

	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/config"
	"github.com/go-tangra/go-tangra-hr/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-hr/v4/internal/people"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

const appTenant = "11111111-1111-7111-8111-111111111111"

type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, token string) (authclient.Identity, error) {
	if token == "user" {
		return authclient.Identity{UserID: "u1", TenantID: appTenant}, nil
	}
	return authclient.Identity{}, errors.New("unauthenticated")
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

func options() Options {
	return Options{
		Verifier: fakeVerifier{}, Checker: authz.Static{"u1": {authz.Calendar, authz.Request}},
		Directory: &people.Fake{Users: map[string][]people.Contact{appTenant: {{UserID: "u1", DisplayName: "User One"}}}},
		Repo:      memstore.New(), Stream: stream.NewMemory(),
		Freya: []freya.Option{freya.WithInsecureLocalDev(), freya.WithAllowAllPolicy()},
	}
}

func TestBuildWiresTheService(t *testing.T) {
	cfg := testConfig()
	cfg.TaskScheduler.Enabled = true
	a, err := Build(context.Background(), cfg, options())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer a.Close()
	if a.Freya == nil || a.Repo == nil || a.HTTP == nil || a.Hub == nil || a.Audit == nil || a.Events.Pub == nil || a.People == nil ||
		a.Signing == nil || a.Catalog == nil || a.Departments == nil || a.Allowances == nil || a.Requests == nil || a.Backup == nil ||
		a.Consumer == nil || a.Outbox == nil || a.Tasks == nil {
		t.Fatalf("app not fully wired: %+v", a)
	}
	if len(a.workers) != 3 { // consumer + outbox + registrar
		t.Fatalf("workers %d", len(a.workers))
	}
	if m := a.HTTP.Missing(); len(m) != 0 {
		t.Fatalf("routes without handlers: %v", m)
	}
	do := func(path, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://localhost"+path, nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		a.HTTP.Handler().ServeHTTP(w, r)
		return w
	}
	if w := do("/api/hr/v1/health", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"store":"ok"`) {
		t.Fatalf("health: %d %s", w.Code, w.Body)
	}
	if w := do("/api/hr/v1/requests", ""); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if w := do("/api/hr/v1/requests", "user"); w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("requests: %d %s", w.Code, w.Body)
	}
	if w := do("/api/hr/v1/stats", "user"); w.Code != 403 {
		t.Fatalf("missing permission: %d", w.Code)
	}
	if w := do("/api/hr/v1/people", "user"); w.Code != 200 || !strings.Contains(w.Body.String(), "User One") {
		t.Fatalf("people: %d %s", w.Code, w.Body)
	}
	// Without a reachable notification module mail is a retryable failure.
	ln := &lazyNotify{app: a, service: "notification"}
	if res, err := ln.SendKey(context.Background(), "t", "hr.request_approved", "a@b", nil, ""); err != nil || !res.Retryable {
		t.Fatalf("unreachable notification: %+v %v", res, err)
	}
	// Without a reachable signing module the adapter reports it unavailable.
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := a.Signing.Templates(sctx, appTenant); err == nil {
		t.Fatal("signing down")
	}
	if _, ok := schedulerCaller("example.org")(context.Background()); ok {
		t.Fatal("a call without a verified peer has no caller")
	}
	h := a.health()
	if h["store"] != "ok" || h["event_bus"] != "ok" {
		t.Fatalf("health map: %v", h)
	}
	// Run and stop.
	rctx, rcancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(rctx) }()
	time.Sleep(200 * time.Millisecond)
	rcancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
	a.Close()
	a.Close() // idempotent
}

func TestBuildFailures(t *testing.T) {
	cfg := testConfig()
	cfg.MeshEnroll = config.MeshEnroll{Enabled: true, TokenFile: "/nonexistent"}
	if _, err := Build(context.Background(), cfg, options()); err == nil {
		t.Fatal("missing enroll token")
	}
	o := options()
	o.Repo = nil
	if _, err := Build(context.Background(), testConfig(), o); err == nil {
		t.Fatal("unreachable database")
	}
	o = options()
	o.Stream = nil
	c := testConfig()
	c.Valkey.CAFile = "/nonexistent"
	if _, err := Build(context.Background(), c, o); err == nil {
		t.Fatal("missing valkey ca")
	}
	o = options()
	o.Checker, o.Verifier = nil, nil
	a, err := Build(context.Background(), testConfig(), o)
	if err != nil {
		t.Fatalf("auth wiring: %v", err)
	}
	a.Close()
}
