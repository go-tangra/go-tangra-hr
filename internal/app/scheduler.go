package app

import (
	"context"

	"google.golang.org/grpc"

	schedulerv1 "github.com/go-tangra/go-tangra-scheduler/sdk/v4/api/proto/scheduler/v1"
	"github.com/go-tangra/go-tangra-scheduler/sdk/v4/pkg/schedulerclient"
	sdktask "github.com/go-tangra/go-tangra-scheduler/sdk/v4/pkg/taskexec"
	"github.com/go-tangra/go-tangra/v4/authn"

	"github.com/go-tangra/go-tangra-hr/v4/internal/tasks"
)

// wireScheduler serves scheduler.v1.TaskExecutor (always: the mesh policy
// admits only svc/scheduler and the SDK server re-checks the peer) and, when
// task_scheduler.enabled, keeps hr's task types registered with the scheduler.
func (a *App) wireScheduler() {
	cfg := a.Cfg
	a.Tasks = &tasks.Runner{Store: a.Repo, Allowances: a.Allowances, Requests: a.Requests, Directory: a.Directory, Forget: a.People.Forget,
		Audit: a.Audit, Log: a.Log, Now: a.Now, ReconcileAfter: cfg.ReconcileAfter()}
	scheduler := cfg.TaskScheduler.Service
	if scheduler == "" {
		scheduler = sdktask.DefaultScheduler
	}
	schedulerv1.RegisterTaskExecutorServer(a.Freya.GRPC(), sdktask.NewServer(a.Tasks.Handlers(), sdktask.Options{
		Caller: schedulerCaller(cfg.Config.TrustDomain), Scheduler: scheduler, Platform: tasks.Platform(), Log: a.Log,
	}))
	if !cfg.TaskScheduler.Enabled {
		return
	}
	reg := &schedulerclient.Registrar{
		Dial: func(ctx context.Context) (grpc.ClientConnInterface, error) {
			return a.Freya.Client(ctx, cfg.TaskScheduler.Service)
		},
		Types: tasks.Descriptors(),
		Log:   a.Log,
	}
	a.workers = append(a.workers, reg.Run)
}

// schedulerCaller returns the verified peer's service name, only for a peer
// of hr's own trust domain.
func schedulerCaller(trustDomain string) func(ctx context.Context) (string, bool) {
	return func(ctx context.Context) (string, bool) {
		p, ok := authn.FromContext(ctx)
		if !ok || p.ID.TrustDomain() != trustDomain {
			return "", false
		}
		return p.ID.ServiceName(), true
	}
}
