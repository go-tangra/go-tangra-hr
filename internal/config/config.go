// Package config loads and validates the hr service configuration: the Freya
// framework config plus the module's own sections. Every value is explicit;
// insecure opt-outs are named and surfaced at start (Constitution I/VII). The
// service refuses to start without a store, an event bus and a gateway issuer.
//
// The module's yaml keys never collide with the framework sections the
// embedded config already owns (server, admin, discovery, limits, identity,
// authz): the module's bounds live under "limits_hr".
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	fconfig "github.com/go-tangra/go-tangra/v4/config"
	"gopkg.in/yaml.v3"
)

// DefaultPlatformTenant is the tenant of the stack's platform administrators.
const DefaultPlatformTenant = "00000000-0000-0000-0000-000000000001"

// Config is the hr service configuration. The embedded framework config
// (inline) carries service_name, trust_domain, env, identity, authz, limits,
// admin, discovery and server (grpc_addr/http_addr).
type Config struct {
	fconfig.Config `yaml:",inline"`

	DB               DB            `yaml:"db"`
	Valkey           Valkey        `yaml:"valkey"`
	Gateway          Gateway       `yaml:"gateway"`
	MeshEnroll       MeshEnroll    `yaml:"mesh_enroll"`
	Events           Events        `yaml:"events"`
	PlatformTenantID string        `yaml:"platform_tenant_id"`
	Auth             Service       `yaml:"auth"`
	Notification     Service       `yaml:"notification"`
	Signing          Service       `yaml:"signing"`
	TaskScheduler    TaskScheduler `yaml:"task_scheduler"`
	Links            Links         `yaml:"links"`
	Limits           Limits        `yaml:"limits_hr"`
	Consumer         Consumer      `yaml:"consumer"`
	Reconcile        Reconcile     `yaml:"reconcile"`
	Outbox           Outbox        `yaml:"outbox"`
}

// DB configures the PostgreSQL/TimescaleDB store.
type DB struct {
	DSN        string `yaml:"dsn"`
	MigrateDSN string `yaml:"migrate_dsn"`
	MaxConns   int32  `yaml:"max_conns"`
}

// Valkey configures the platform event bus.
type Valkey struct {
	Addresses      []string `yaml:"addresses"`
	Username       string   `yaml:"username"`
	Password       string   `yaml:"password"`
	AllowPlaintext bool     `yaml:"allow_plaintext"`
	CAFile         string   `yaml:"ca_file"`
}

// Gateway names the application gateway and the platform token issuer.
type Gateway struct {
	Service string `yaml:"service"`
	Issuer  string `yaml:"issuer"`
}

// MeshEnroll configures how the module obtains its own mesh SVID by
// enrolling with lcm over the network (identity.provider=provided).
type MeshEnroll struct {
	Enabled       bool   `yaml:"enabled"`
	EnrollURL     string `yaml:"enroll_url"`
	LCMGRPCTarget string `yaml:"lcm_grpc"`
	TenantID      string `yaml:"tenant_id"`
	TokenFile     string `yaml:"token_file"`
	StateFile     string `yaml:"state_file"`
	Insecure      bool   `yaml:"insecure"`
}

// Events toggles the platform event publisher (hr.* events for the UI).
type Events struct {
	Enabled bool `yaml:"enabled"`
}

// Service names a module dialled over the mesh by its discovery name.
type Service struct {
	Service string `yaml:"service"`
}

// TaskScheduler configures the scheduler module integration. The
// TaskExecutor service is always served (the mesh policy admits only the
// scheduler); Enabled only controls registering the task types.
type TaskScheduler struct {
	Enabled bool   `yaml:"enabled"`
	Service string `yaml:"service"`
}

// Links builds the portal links placed in e-mails.
type Links struct {
	PortalBaseURL string `yaml:"portal_base_url"` // e.g. https://portal.example.org:8443
}

// Limits bound inputs and responses (spec SR-009).
type Limits struct {
	MaxPageSize       int   `yaml:"max_page_size"`
	MaxCalendarDays   int   `yaml:"max_calendar_days"`
	MaxRequestDays    int   `yaml:"max_request_days"`
	MaxImportBytes    int64 `yaml:"max_import_bytes"`
	MaxImportLines    int   `yaml:"max_import_lines"`
	MaxBackupBytes    int64 `yaml:"max_backup_bytes"`
	MaxDepartmentTree int   `yaml:"max_department_depth"`
}

// Consumer tunes the signing outcome consumer of the platform stream
// (research D2).
type Consumer struct {
	Enabled         bool `yaml:"enabled"`
	BlockMillis     int  `yaml:"block_ms"`
	Batch           int  `yaml:"batch"`
	TenantsRefreshS int  `yaml:"tenants_refresh_seconds"`
}

// Reconcile sets when a request awaiting signing is checked against the
// signing module by the reconcile task (FR-034).
type Reconcile struct {
	OlderThanMinutes int `yaml:"older_than_minutes"`
}

// Outbox tunes the e-mail outbox worker (research D8).
type Outbox struct {
	IntervalMillis int `yaml:"interval_ms"`
	Batch          int `yaml:"batch"`
	MaxAttempts    int `yaml:"max_attempts"`
}

// Default returns secure defaults on top of the Freya defaults.
func Default() Config {
	return Config{
		Config:           fconfig.Default(),
		DB:               DB{MaxConns: 16},
		Events:           Events{Enabled: true},
		Gateway:          Gateway{Service: "gateway"},
		PlatformTenantID: DefaultPlatformTenant,
		Auth:             Service{Service: "auth"},
		Notification:     Service{Service: "notification"},
		Signing:          Service{Service: "signing"},
		TaskScheduler:    TaskScheduler{Service: "scheduler"},
		Limits: Limits{MaxPageSize: 100, MaxCalendarDays: 93, MaxRequestDays: 366, MaxImportBytes: 64 << 10,
			MaxImportLines: 500, MaxBackupBytes: 256 << 20, MaxDepartmentTree: 10},
		Consumer:  Consumer{Enabled: true, BlockMillis: 2000, Batch: 100, TenantsRefreshS: 30},
		Reconcile: Reconcile{OlderThanMinutes: 10},
		Outbox:    Outbox{IntervalMillis: 2000, Batch: 50, MaxAttempts: 5},
	}
}

// Load reads YAML over Default(); unknown fields are rejected.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

var (
	uuidRE        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	serviceNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

func within[T int | int64](v, lo, hi T) bool { return v >= lo && v <= hi }

// Validate checks the Freya config and every module section.
func (c Config) Validate() error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	for _, check := range []func() error{c.validateInfra, c.validateServices, c.validateLimits, c.validateWorkers} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) validateInfra() error {
	prod := c.IsProduction()
	if c.DB.DSN == "" {
		return errors.New("config: db.dsn is required")
	}
	if prod && !strings.Contains(c.DB.DSN, "sslmode=verify-full") && !strings.Contains(c.DB.DSN, "sslmode=verify-ca") {
		return errors.New("config: db.dsn must use sslmode=verify-full (or verify-ca) in production")
	}
	if c.DB.MaxConns < 0 || c.DB.MaxConns > 256 {
		return errors.New("config: db.max_conns must be within [0, 256]")
	}
	if len(c.Valkey.Addresses) == 0 {
		return errors.New("config: valkey.addresses is required")
	}
	if prod && c.Valkey.AllowPlaintext {
		return errors.New("config: valkey.allow_plaintext is not permitted in production")
	}
	if c.Gateway.Service == "" {
		return errors.New("config: gateway.service is required")
	}
	if iu, err := url.Parse(c.Gateway.Issuer); err != nil || iu.Scheme != "https" || iu.Host == "" {
		return errors.New("config: gateway.issuer must be an https origin")
	}
	if prod && c.MeshEnroll.Enabled && c.MeshEnroll.Insecure {
		return errors.New("config: mesh_enroll.insecure is not permitted in production")
	}
	if !uuidRE.MatchString(c.PlatformTenantID) {
		return errors.New("config: platform_tenant_id must be a uuid")
	}
	if pu, err := url.Parse(c.Links.PortalBaseURL); err != nil || pu.Scheme != "https" || pu.Host == "" || (pu.Path != "" && pu.Path != "/") {
		return errors.New("config: links.portal_base_url must be an https origin")
	}
	return nil
}

func (c Config) validateServices() error {
	for name, s := range map[string]string{"auth.service": c.Auth.Service, "notification.service": c.Notification.Service,
		"signing.service": c.Signing.Service} {
		if !serviceNameRE.MatchString(s) {
			return fmt.Errorf("config: %s must be a discovery service name", name)
		}
	}
	if c.TaskScheduler.Enabled && c.TaskScheduler.Service == "" {
		return errors.New("config: task_scheduler.service is required when task_scheduler.enabled")
	}
	if c.TaskScheduler.Service != "" && !serviceNameRE.MatchString(c.TaskScheduler.Service) {
		return errors.New("config: task_scheduler.service must be a discovery service name")
	}
	return nil
}

func (c Config) validateLimits() error {
	l := c.Limits
	switch {
	case !within(l.MaxPageSize, 1, 500):
		return errors.New("config: limits_hr.max_page_size must be within [1, 500]")
	case !within(l.MaxCalendarDays, 7, 186):
		return errors.New("config: limits_hr.max_calendar_days must be within [7, 186]")
	case !within(l.MaxRequestDays, 1, 731):
		return errors.New("config: limits_hr.max_request_days must be within [1, 731]")
	case !within(l.MaxImportBytes, 1<<10, 1<<20):
		return errors.New("config: limits_hr.max_import_bytes must be within [1 KiB, 1 MiB]")
	case !within(l.MaxImportLines, 1, 10000):
		return errors.New("config: limits_hr.max_import_lines must be within [1, 10000]")
	case !within(l.MaxBackupBytes, 1<<20, 1<<30):
		return errors.New("config: limits_hr.max_backup_bytes must be within [1 MiB, 1 GiB]")
	case !within(l.MaxDepartmentTree, 1, 50):
		return errors.New("config: limits_hr.max_department_depth must be within [1, 50]")
	}
	return nil
}

func (c Config) validateWorkers() error {
	switch {
	case !within(c.Consumer.BlockMillis, 100, 60000):
		return errors.New("config: consumer.block_ms must be within [100, 60000]")
	case !within(c.Consumer.Batch, 1, 1000):
		return errors.New("config: consumer.batch must be within [1, 1000]")
	case !within(c.Consumer.TenantsRefreshS, 1, 3600):
		return errors.New("config: consumer.tenants_refresh_seconds must be within [1, 3600]")
	case !within(c.Reconcile.OlderThanMinutes, 1, 1440):
		return errors.New("config: reconcile.older_than_minutes must be within [1, 1440]")
	case !within(c.Outbox.IntervalMillis, 100, 60000):
		return errors.New("config: outbox.interval_ms must be within [100, 60000]")
	case !within(c.Outbox.Batch, 1, 1000):
		return errors.New("config: outbox.batch must be within [1, 1000]")
	case !within(c.Outbox.MaxAttempts, 1, 20):
		return errors.New("config: outbox.max_attempts must be within [1, 20]")
	}
	return nil
}

// Warnings lists accepted insecure opt-outs (surfaced at start).
func (c Config) Warnings() []string {
	w := c.Config.Warnings()
	if c.Valkey.AllowPlaintext {
		w = append(w, "valkey.allow_plaintext: event-bus traffic without TLS (development only)")
	}
	if c.MeshEnroll.Enabled && c.MeshEnroll.Insecure {
		w = append(w, "mesh_enroll.insecure: SVID enrollment without TLS (development only)")
	}
	if !c.Events.Enabled {
		w = append(w, "events.enabled=false: the UI receives no hr events")
	}
	if !c.Consumer.Enabled {
		w = append(w, "consumer.enabled=false: signing outcomes are applied only by the reconcile task")
	}
	if !c.TaskScheduler.Enabled {
		w = append(w, "task_scheduler.enabled=false: no carry-over, signing reconciliation or member sync tasks")
	}
	return w
}

// ConsumerBlock is how long one stream read waits.
func (c Config) ConsumerBlock() time.Duration {
	return time.Duration(c.Consumer.BlockMillis) * time.Millisecond
}

// TenantsRefresh is how often the consumer re-reads the tenant registry.
func (c Config) TenantsRefresh() time.Duration {
	return time.Duration(c.Consumer.TenantsRefreshS) * time.Second
}

// ReconcileAfter is how long a request may await signing before the
// reconcile task asks the signing module.
func (c Config) ReconcileAfter() time.Duration {
	return time.Duration(c.Reconcile.OlderThanMinutes) * time.Minute
}

// OutboxInterval is the mail outbox poll interval.
func (c Config) OutboxInterval() time.Duration {
	return time.Duration(c.Outbox.IntervalMillis) * time.Millisecond
}
