package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// valid returns a Config that passes both the framework and module Validate.
func valid() Config {
	c := Default()
	c.ServiceName = "hr"
	c.TrustDomain = "example.org"
	c.Authz.Path = "/etc/hr/policy.yaml"
	c.DB.DSN = "postgres://localhost/hr"
	c.Valkey.Addresses = []string{"valkey:6379"}
	c.Gateway.Issuer = "https://gw.example.org"
	c.Links.PortalBaseURL = "https://portal.example.org:8443"
	return c
}

func prod() Config {
	c := valid()
	c.Env = "production"
	c.DB.DSN = "postgres://db/hr?sslmode=verify-full"
	return c
}

func TestDefaultSecure(t *testing.T) {
	d := Default()
	if d.Valkey.AllowPlaintext || !d.Events.Enabled || d.MeshEnroll.Insecure || !d.Consumer.Enabled {
		t.Fatalf("insecure defaults: %+v %+v %+v", d.Valkey, d.Events, d.Consumer)
	}
	if d.PlatformTenantID != DefaultPlatformTenant || d.Gateway.Service != "gateway" || d.Auth.Service != "auth" ||
		d.Notification.Service != "notification" || d.Signing.Service != "signing" ||
		d.TaskScheduler.Service != "scheduler" || d.TaskScheduler.Enabled {
		t.Fatalf("defaults: %+v", d)
	}
	l := d.Limits
	if l.MaxCalendarDays != 93 || l.MaxRequestDays != 366 || l.MaxImportBytes != 64<<10 || l.MaxImportLines != 500 ||
		l.MaxBackupBytes != 256<<20 || l.MaxDepartmentTree != 10 || l.MaxPageSize != 100 {
		t.Fatalf("limit defaults: %+v", l)
	}
	if d.Reconcile.OlderThanMinutes != 10 || d.Outbox.MaxAttempts != 5 {
		t.Fatalf("worker defaults: %+v %+v", d.Reconcile, d.Outbox)
	}
	if err := Default().Validate(); err == nil {
		t.Fatal("Default() must not validate without required fields")
	}
}

func TestValidateOK(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	c := prod()
	c.MeshEnroll = MeshEnroll{Enabled: true}
	c.TaskScheduler.Enabled = true
	if err := c.Validate(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
}

func TestValidateRefusals(t *testing.T) {
	cases := map[string]func(*Config){
		"framework":    func(c *Config) { c.ServiceName = "" },
		"no dsn":       func(c *Config) { c.DB.DSN = "" },
		"prod sslmode": func(c *Config) { c.Env = "production" },
		"max conns":    func(c *Config) { c.DB.MaxConns = 1000 },
		"no valkey":    func(c *Config) { c.Valkey.Addresses = nil },
		"prod plaintext": func(c *Config) {
			*c = prod()
			c.Valkey.AllowPlaintext = true
		},
		"no gateway":  func(c *Config) { c.Gateway.Service = "" },
		"http issuer": func(c *Config) { c.Gateway.Issuer = "http://gw" },
		"bad issuer":  func(c *Config) { c.Gateway.Issuer = "://" },
		"prod insecure mesh": func(c *Config) {
			*c = prod()
			c.MeshEnroll = MeshEnroll{Enabled: true, Insecure: true}
		},
		"platform tenant":   func(c *Config) { c.PlatformTenantID = "platform" },
		"no portal":         func(c *Config) { c.Links.PortalBaseURL = "" },
		"http portal":       func(c *Config) { c.Links.PortalBaseURL = "http://portal" },
		"portal path":       func(c *Config) { c.Links.PortalBaseURL = "https://portal/x" },
		"bad portal":        func(c *Config) { c.Links.PortalBaseURL = "://" },
		"auth service":      func(c *Config) { c.Auth.Service = "Auth!" },
		"notify service":    func(c *Config) { c.Notification.Service = "" },
		"signing service":   func(c *Config) { c.Signing.Service = "-s" },
		"scheduler missing": func(c *Config) { c.TaskScheduler = TaskScheduler{Enabled: true} },
		"scheduler name":    func(c *Config) { c.TaskScheduler.Service = "Sched" },
		"page size":         func(c *Config) { c.Limits.MaxPageSize = 0 },
		"calendar":          func(c *Config) { c.Limits.MaxCalendarDays = 1 },
		"request days":      func(c *Config) { c.Limits.MaxRequestDays = 0 },
		"import bytes":      func(c *Config) { c.Limits.MaxImportBytes = 1 },
		"import lines":      func(c *Config) { c.Limits.MaxImportLines = 0 },
		"backup":            func(c *Config) { c.Limits.MaxBackupBytes = 1 },
		"depth":             func(c *Config) { c.Limits.MaxDepartmentTree = 0 },
		"block":             func(c *Config) { c.Consumer.BlockMillis = 1 },
		"batch":             func(c *Config) { c.Consumer.Batch = 0 },
		"refresh":           func(c *Config) { c.Consumer.TenantsRefreshS = 0 },
		"reconcile":         func(c *Config) { c.Reconcile.OlderThanMinutes = 0 },
		"outbox interval":   func(c *Config) { c.Outbox.IntervalMillis = 1 },
		"outbox batch":      func(c *Config) { c.Outbox.Batch = 0 },
		"outbox attempts":   func(c *Config) { c.Outbox.MaxAttempts = 100 },
	}
	for name, mut := range cases {
		c := valid()
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hr.yaml")
	body := `service_name: hr
trust_domain: example.org
authz: {path: /etc/hr/policy.yaml}
db: {dsn: "postgres://localhost/hr"}
valkey: {addresses: ["valkey:6379"], allow_plaintext: true}
gateway: {issuer: "https://gw.example.org"}
links: {portal_base_url: "https://portal.example.org"}
limits_hr: {max_calendar_days: 62}
reconcile: {older_than_minutes: 30}
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Limits.MaxCalendarDays != 62 || c.Limits.MaxRequestDays != 366 || c.ReconcileAfter() != 30*time.Minute {
		t.Fatalf("loaded: %+v %+v", c.Limits, c.Reconcile)
	}
	if err := os.WriteFile(p, []byte(body+"bogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown key accepted: %v", err)
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestWarningsAndDurations(t *testing.T) {
	c := valid()
	c.Valkey.AllowPlaintext = true
	c.MeshEnroll = MeshEnroll{Enabled: true, Insecure: true}
	c.Events.Enabled = false
	c.Consumer.Enabled = false
	w := strings.Join(c.Warnings(), "\n")
	for _, want := range []string{"valkey.allow_plaintext", "mesh_enroll.insecure", "events.enabled=false",
		"consumer.enabled=false", "task_scheduler.enabled=false"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %s: %s", want, w)
		}
	}
	s := valid()
	s.TaskScheduler.Enabled = true
	if len(s.Warnings()) != len(s.Config.Warnings()) {
		t.Fatalf("unexpected warnings: %v", s.Warnings())
	}
	d := Default()
	if d.ConsumerBlock() != 2*time.Second || d.TenantsRefresh() != 30*time.Second || d.OutboxInterval() != 2*time.Second ||
		d.ReconcileAfter() != 10*time.Minute {
		t.Fatal("durations")
	}
}
