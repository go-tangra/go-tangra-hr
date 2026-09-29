// Package app wires the hr service: configuration -> Freya runtime (mesh
// identity, admin listener) -> store/audit/events -> the domain services ->
// the mesh HTTP surface (reached only through the gateway) and the scheduler
// task executor (module-to-module), plus gateway registration, permission
// seeding and the background workers (signing-outcome consumer, mail
// outbox). It refuses to start without a store, an event bus and a gateway
// issuer (config.Validate).
package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-lcm/sdk/v4/pkg/lcmidentity"
	"github.com/go-tangra/go-tangra/v4"
	"google.golang.org/grpc"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra-portal/sdk/v4/pkg/gatewayclient"

	"github.com/go-tangra/go-tangra-hr/v4/internal/allowances"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/backup"
	"github.com/go-tangra/go-tangra-hr/v4/internal/catalog"
	"github.com/go-tangra/go-tangra-hr/v4/internal/config"
	"github.com/go-tangra/go-tangra-hr/v4/internal/consumer"
	"github.com/go-tangra/go-tangra-hr/v4/internal/departments"
	"github.com/go-tangra/go-tangra-hr/v4/internal/events"
	"github.com/go-tangra/go-tangra-hr/v4/internal/httpapi"
	"github.com/go-tangra/go-tangra-hr/v4/internal/outbox"
	"github.com/go-tangra/go-tangra-hr/v4/internal/people"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-hr/v4/internal/requests"
	"github.com/go-tangra/go-tangra-hr/v4/internal/signing"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream/valkeykv"
	"github.com/go-tangra/go-tangra-hr/v4/internal/tasks"
	"github.com/go-tangra/go-tangra-hr/v4/pkg/hrmanifest"
)

// Options override infrastructure (tests) and attach optional parts.
type Options struct {
	Logger    slog.Handler
	Verifier  httpapi.Verifier
	Checker   authz.Checker    // API-permission checker override (default: auth Authorization/Check)
	Directory people.Directory // member directory override (tests: people.Fake)
	Notify    outbox.Sender    // notification client override (tests)
	Signing   signing.Module   // signing module client override (tests)
	Repo      repo.Store       // store override (tests: memstore); skips the DB
	Stream    stream.Client    // event-bus client override (tests: stream.NewMemory())
	Now       func() time.Time // clock override (tests)
	Freya     []freya.Option
	Migrate   bool
	Remote    fs.FS // built federated UI remote (nil serves no remote)
}

// App is the wired service.
type App struct {
	Cfg      config.Config
	Log      *slog.Logger
	Freya    *freya.App
	Store    *store.Store
	Repo     repo.Store
	Audit    *audit.Writer
	Verifier httpapi.Verifier
	Checker  authz.Checker
	Hub      *stream.Hub
	Events   events.Emitter
	HTTP     *httpapi.Server
	Now      func() time.Time

	Directory   people.Directory
	People      *people.Cache
	Signing     *signing.Adapter
	Catalog     *catalog.Service
	Departments *departments.Service
	Allowances  *allowances.Service
	Requests    *requests.Service
	Backup      *backup.Service
	Consumer    *consumer.Consumer
	Outbox      *outbox.Worker
	Tasks       *tasks.Runner

	streamClient stream.Client
	workers      []func(context.Context)
	closers      []func()
}

// Build wires the service.
func Build(ctx context.Context, cfg config.Config, o Options) (a *App, err error) {
	authz.PlatformTenant = cfg.PlatformTenantID // its admins and owners act as platform administrators
	a = &App{Cfg: cfg}
	handler := o.Logger
	if handler == nil {
		handler = slog.NewJSONHandler(os.Stderr, nil)
	}
	a.Log = slog.New(handler)
	built := a
	defer func() { // release what was opened when wiring fails half-way
		if err != nil {
			built.Close()
		}
	}()
	a.Now = o.Now
	if a.Now == nil {
		a.Now = func() time.Time { return time.Now().UTC() }
	}
	if err = a.buildRuntime(ctx, cfg, handler, o.Freya); err != nil {
		return nil, err
	}
	if err = a.buildStorage(ctx, cfg, o); err != nil {
		return nil, err
	}
	if err = a.buildPeers(ctx, cfg, o); err != nil {
		return nil, err
	}
	if err = a.buildEvents(cfg, o.Stream); err != nil {
		return nil, err
	}
	a.buildServices(cfg, o)
	a.wireScheduler()

	// Mesh HTTP surface (reached only through the gateway).
	hopts := []httpapi.Option{httpapi.WithVerifier(a.Verifier), httpapi.WithChecker(a.Checker), httpapi.WithLogger(a.Log)}
	if o.Remote != nil {
		hopts = append(hopts, httpapi.WithRemote(o.Remote))
	}
	if a.HTTP, err = httpapi.NewHandler(a.Freya, hopts...); err != nil {
		return nil, err
	}
	l := cfg.Limits
	a.HTTP.Register(httpapi.Deps{Hub: a.Hub, Health: a.health, Store: a.Repo, Checker: a.Checker, Tree: a.Departments.Tree, People: a.People,
		Catalog: a.Catalog, Allowances: a.Allowances, Departments: a.Departments, Requests: a.Requests, Signing: a.Signing, Backup: a.Backup,
		MaxBackup: l.MaxBackupBytes, MaxImport: l.MaxImportBytes, MaxPageSize: l.MaxPageSize, Now: a.Now})
	a.Freya.HTTP().HandlePrefix("/", a.HTTP.Handler())
	return a, nil
}

// buildServices wires the domain services and the background workers.
func (a *App) buildServices(cfg config.Config, o Options) {
	l := cfg.Limits
	a.Directory = o.Directory
	if a.Directory == nil {
		a.Directory = people.Client{Dial: func(ctx context.Context) (grpc.ClientConnInterface, error) {
			return a.Freya.Client(ctx, cfg.Auth.Service)
		}}
	}
	a.People = &people.Cache{Dir: a.Directory, Checker: a.Checker}
	a.Signing = &signing.Adapter{Now: a.Now, Dial: a.signingModule(cfg, o.Signing)}
	a.Catalog = catalog.New(catalog.Deps{Store: a.Repo, Audit: a.Audit, Checker: a.Checker, Signing: a.Signing, Now: a.Now,
		Limits: catalog.Limits{MaxImportBytes: l.MaxImportBytes, MaxImportLines: l.MaxImportLines}})
	a.Departments = departments.New(departments.Deps{Store: a.Repo, Audit: a.Audit, Checker: a.Checker, People: a.People, Now: a.Now,
		MaxDepth: l.MaxDepartmentTree})
	a.Allowances = allowances.New(allowances.Deps{Store: a.Repo, Audit: a.Audit, Checker: a.Checker, Tree: a.Departments.Tree,
		Calendar: a.Catalog.Calendar, Now: a.Now})
	var ev requests.Events
	if a.Events.Pub != nil {
		ev = a.Events
	}
	a.Requests = requests.New(requests.Deps{Store: a.Repo, Audit: a.Audit, Checker: a.Checker, People: a.People, Signing: a.Signing,
		Events: ev, Calendar: a.Catalog.Calendar, Tree: a.Departments.Tree, Now: a.Now,
		Limits: requests.Limits{MaxRequestDays: l.MaxRequestDays, MaxCalendarDays: l.MaxCalendarDays},
		Links:  requests.Links{PortalBaseURL: cfg.Links.PortalBaseURL}})
	// The department service re-routes open requests after changes; the
	// request service needs the department tree first, so the department
	// service is rebuilt with the rerouter (Tree does not depend on it).
	a.Departments = departments.New(departments.Deps{Store: a.Repo, Audit: a.Audit, Checker: a.Checker, People: a.People, Now: a.Now,
		MaxDepth: l.MaxDepartmentTree, Rerouter: a.Requests})
	a.Backup = backup.New(backup.Deps{Store: a.Repo, Audit: a.Audit, Checker: a.Checker, Now: a.Now, MaxBytes: l.MaxBackupBytes})
	if cfg.Consumer.Enabled {
		a.Consumer = &consumer.Consumer{Store: a.Repo, Stream: a.streamClient, Apply: a.Requests, Block: cfg.ConsumerBlock(),
			Batch: cfg.Consumer.Batch, Refresh: cfg.TenantsRefresh(), Log: a.Log, Now: a.Now}
		a.workers = append(a.workers, func(ctx context.Context) { _ = a.Consumer.Run(ctx) })
	}
	notify := o.Notify
	if notify == nil {
		notify = &lazyNotify{app: a, service: cfg.Notification.Service}
	}
	a.Outbox = &outbox.Worker{Store: a.Repo, Contacts: a.Directory, Sender: notify, Audit: a.Audit, Log: a.Log, Now: a.Now,
		Interval: cfg.OutboxInterval(), Batch: cfg.Outbox.Batch, MaxAttempts: cfg.Outbox.MaxAttempts}
	a.workers = append(a.workers, func(ctx context.Context) { _ = a.Outbox.Run(ctx) })
}

func (a *App) buildRuntime(ctx context.Context, cfg config.Config, handler slog.Handler, extra []freya.Option) error {
	fopts := append([]freya.Option{freya.WithLogger(handler)}, extra...)
	if cfg.MeshEnroll.Enabled {
		raw, err := os.ReadFile(cfg.MeshEnroll.TokenFile)
		if err != nil {
			return fmt.Errorf("hr: mesh enroll token: %w", err)
		}
		prov, err := lcmidentity.NewNet(ctx, lcmidentity.NetConfig{
			EnrollURL: cfg.MeshEnroll.EnrollURL, LCMGRPCTarget: cfg.MeshEnroll.LCMGRPCTarget,
			TenantID: cfg.MeshEnroll.TenantID, TrustDomain: cfg.Config.TrustDomain, ServiceName: cfg.Config.ServiceName,
			EnrollmentToken: strings.TrimSpace(string(raw)), Insecure: cfg.MeshEnroll.Insecure, StateFile: cfg.MeshEnroll.StateFile,
		})
		if err != nil {
			return fmt.Errorf("hr: mesh enroll: %w", err)
		}
		a.closers = append(a.closers, func() { _ = prov.Close() })
		fopts = append(fopts, freya.WithIdentityProvider(prov))
	}
	f, err := freya.New(cfg.Config, fopts...)
	if err != nil {
		return err
	}
	a.Freya = f
	a.closers = append(a.closers, f.Close)
	return nil
}

func (a *App) buildStorage(ctx context.Context, cfg config.Config, o Options) (err error) {
	a.Repo = o.Repo
	if a.Repo == nil {
		if o.Migrate {
			mdsn := cfg.DB.MigrateDSN
			if mdsn == "" {
				mdsn = cfg.DB.DSN
			}
			if err = store.Migrate(ctx, mdsn); err != nil {
				return err
			}
		}
		if a.Store, err = store.Open(ctx, cfg.DB.DSN, cfg.DB.MaxConns); err != nil {
			return err
		}
		a.closers = append(a.closers, a.Store.Close)
		a.Repo = repodb.New(a.Store)
	}
	a.Audit = audit.NewWriter(a.Repo, func(err error) { a.Log.Error("audit write failed", "err", err) })
	a.closers = append(a.closers, a.Audit.Close)
	return nil
}

// buildPeers wires what the module asks auth: the platform-token verifier and
// the permission checker. The connection is dialled lazily by the framework,
// so the module starts while auth is down.
func (a *App) buildPeers(ctx context.Context, cfg config.Config, o Options) error {
	a.Verifier, a.Checker = o.Verifier, o.Checker
	if a.Verifier == nil || a.Checker == nil {
		conn, err := a.Freya.Client(ctx, cfg.Auth.Service)
		if err != nil {
			return fmt.Errorf("auth client: %w", err)
		}
		if a.Verifier == nil {
			a.Verifier = authclient.New(authclient.Config{Issuer: cfg.Gateway.Issuer},
				authclient.GRPCKeys{Client: authv1.NewKeysClient(conn)},
				authclient.GRPCRevocations{Client: authv1.NewSessionsClient(conn)})
		}
		if a.Checker == nil {
			a.Checker = AuthPerms{Client: authv1.NewAuthorizationClient(conn)}
		}
	}
	return nil
}

func (a *App) buildEvents(cfg config.Config, client stream.Client) error {
	if client == nil {
		sc := valkeykv.Config{Addresses: cfg.Valkey.Addresses, Username: cfg.Valkey.Username, Password: cfg.Valkey.Password, AllowPlaintext: cfg.Valkey.AllowPlaintext}
		if cfg.Valkey.CAFile != "" {
			pem, err := os.ReadFile(cfg.Valkey.CAFile)
			if err != nil {
				return fmt.Errorf("valkey ca: %w", err)
			}
			sc.CAPEM = pem
		}
		c, err := valkeykv.New(sc)
		if err != nil {
			return fmt.Errorf("event bus: %w", err)
		}
		client = c
	}
	a.streamClient = client
	a.Hub = stream.NewHub(client, stream.Config{}, a.Log)
	a.closers = append(a.closers, a.Hub.Close)
	if cfg.Events.Enabled {
		a.Events = events.Emitter{Pub: events.HubPublisher{Hub: a.Hub}}
	}
	return nil
}

// health reports component reachability for /health.
func (a *App) health() map[string]string {
	out := map[string]string{"store": "ok", "event_bus": "ok"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if a.Store != nil {
		if err := a.Store.Ping(ctx); err != nil {
			out["store"] = "unreachable"
		}
	}
	if err := a.streamClient.Ping(ctx); err != nil {
		out["event_bus"] = "unreachable"
	}
	return out
}

// Run starts the verifier, gateway registration, permission seeding, the
// background workers and the Freya runtime.
func (a *App) Run(ctx context.Context) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if v, ok := a.Verifier.(*authclient.Verifier); ok {
		go func() {
			for wctx.Err() == nil {
				if err := v.Start(wctx, func(err error) { a.Log.Warn("verifier", "err", err) }); err == nil {
					return
				}
				select {
				case <-wctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
		}()
	}
	go a.register(wctx)
	done := make(chan struct{}, len(a.workers))
	for _, w := range a.workers {
		w := w
		go func() {
			defer func() { done <- struct{}{} }()
			w(wctx)
		}()
	}
	go func() {
		for wctx.Err() == nil && !a.Freya.Ready() {
			time.Sleep(100 * time.Millisecond)
		}
		a.seedLoop(wctx)
	}()
	err := a.Freya.Run(ctx)
	cancel()
	for range a.workers {
		<-done
	}
	return err
}

// Close releases resources (idempotent).
func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
	a.closers = nil
}

// register keeps the gateway lease for the manifest.
func (a *App) register(ctx context.Context) {
	for ctx.Err() == nil && !a.Freya.Ready() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	man, err := hrmanifest.Manifest()
	if err != nil {
		a.Log.Error("gateway manifest", "err", err)
		return
	}
	httpEP, err := a.Freya.HTTP().Endpoint()
	if err != nil {
		a.Log.Error("gateway registration: http endpoint", "err", err)
		return
	}
	grpcEP, err := a.Freya.GRPC().Endpoint()
	if err != nil {
		a.Log.Error("gateway registration: grpc endpoint", "err", err)
		return
	}
	var client *gatewayclient.Client
	for ctx.Err() == nil && client == nil {
		conn, cerr := a.Freya.Client(ctx, a.Cfg.Gateway.Service)
		if cerr != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		client, err = gatewayclient.New(conn, gatewayclient.Options{Manifest: man, HTTPURL: "https://" + httpEP.Host, GRPCTarget: grpcEP.Host, Logger: a.Log,
			OnState: func(s gatewayclient.State) {
				a.Log.Info("gateway lease", "registered", s.Registered, "lease", s.LeaseID, "err", s.Err)
			}})
		if err != nil {
			a.Log.Error("gateway client", "err", err)
			return
		}
	}
	if client != nil {
		if err := client.Run(ctx); err != nil {
			a.Log.Error("gateway registration", "err", err)
		}
	}
}
