// Package tasks holds the scheduled task types hr executes for the scheduler
// module (no hidden timers in the service, Constitution VII):
//
//   - hr:carry-over (tenant-scoped) carries unused days of a year into the
//     next one (FR-048, FR-049);
//   - hr:reconcile-signing (platform) checks requests awaiting signing too
//     long against the signing module and applies missed outcomes (FR-034);
//   - hr:sync-members (platform) marks members who left the tenant inactive,
//     cancels their open requests, refreshes names and re-routes approvals
//     (FR-044; auth publishes no removal event, research D5).
//
// The SDK executor server verifies that the caller is the scheduler; the
// handlers never log payloads, names or addresses, and answer Retry on
// outages.
package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/sdk/v4/pkg/schedulerclient"
	sdk "github.com/go-tangra/go-tangra-scheduler/sdk/v4/pkg/taskexec"

	"github.com/go-tangra/go-tangra-hr/v4/internal/allowances"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/people"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// Task types.
const (
	TypeCarryOver = "hr:carry-over"
	TypeReconcile = "hr:reconcile-signing"
	TypeSync      = "hr:sync-members"
)

const (
	carrySchema = `{"type":"object","properties":{"source_year":{"type":"integer","minimum":2000,"maximum":2098,` +
		`"description":"Year whose unused days are carried into the next year (default: the previous year)."}},"additionalProperties":false}`
	reconcileSchema = `{"type":"object","properties":{"older_than_minutes":{"type":"integer","minimum":1,"maximum":1440,` +
		`"description":"Check requests awaiting signing for longer than this (default from the hr configuration)."}},"additionalProperties":false}`
	syncSchema = `{"type":"object","properties":{},"additionalProperties":false}`
)

// Descriptors are the task types hr registers with the scheduler.
func Descriptors() []schedulerclient.Descriptor {
	return []schedulerclient.Descriptor{
		{Type: TypeCarryOver, DisplayName: "Carry over leave allowances",
			Description:   "Carries each person's unused leave days of a year into the next year's allowance, within the carry-over cap of the absence type or pool.",
			PayloadSchema: carrySchema, DefaultCron: "30 0 1 1 *", DefaultMaxRetry: 2},
		{Type: TypeReconcile, DisplayName: "Reconcile leave signing",
			Description:   "Checks leave requests awaiting signing against the signing module and applies completed, declined, expired or cancelled documents that were missed.",
			PayloadSchema: reconcileSchema, DefaultCron: "*/15 * * * *", DefaultMaxRetry: 1, Platform: true},
		{Type: TypeSync, DisplayName: "Sync HR members",
			Description:   "Marks people who left a tenant inactive, cancels their open leave requests, refreshes names and re-routes approvals.",
			PayloadSchema: syncSchema, DefaultCron: "0 * * * *", DefaultMaxRetry: 1, Platform: true},
	}
}

// Platform lists the platform-scoped types for the SDK server.
func Platform() map[string]bool { return map[string]bool{TypeReconcile: true, TypeSync: true} }

// Requests is the part of the request service the tasks use.
type Requests interface {
	Reconcile(ctx context.Context, tenant string, cutoff time.Time, limit int) (checked, applied int, err error)
	Reroute(ctx context.Context, tenant string) error
	CancelOpen(ctx context.Context, tenant, userID string) (int, error)
}

// Runner executes the task types.
type Runner struct {
	Store          repo.Store
	Allowances     *allowances.Service
	Requests       Requests
	Directory      people.Directory
	Forget         func(tenant string) // drops cached member lists after a sync
	Audit          audit.Recorder
	Log            *slog.Logger
	Now            func() time.Time
	ReconcileAfter time.Duration
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func (r *Runner) warn(ctx context.Context, msg string, args ...any) {
	if r.Log != nil {
		r.Log.WarnContext(ctx, msg, args...)
	}
}

// Handlers maps the task types to their handlers for sdk.NewServer.
func (r *Runner) Handlers() map[string]sdk.Handler {
	return map[string]sdk.Handler{TypeCarryOver: r.CarryOver, TypeReconcile: r.Reconcile, TypeSync: r.Sync}
}

func (r *Runner) record(ctx context.Context, t audit.EventType, tenant, kind, id string, detail map[string]any) {
	audit.Emit(ctx, r.Audit, audit.Event{TenantID: tenant, EventType: t, ActorKind: audit.ActorSystem, ActorID: "scheduler",
		SubjectKind: kind, SubjectID: id, Outcome: audit.OutcomeOK, Details: detail})
}

// CarryOver runs hr:carry-over for the task's tenant.
func (r *Runner) CarryOver(ctx context.Context, req sdk.Request) sdk.Result {
	if req.TenantID == "" {
		return sdk.Permanent("hr:carry-over is tenant-scoped")
	}
	var p struct {
		SourceYear int `json:"source_year"`
	}
	if err := sdk.DecodeStrict(req.Payload, &p); err != nil {
		return sdk.Permanent("invalid payload: " + err.Error())
	}
	if p.SourceYear == 0 {
		p.SourceYear = r.now().Year() - 1
	}
	plan, err := r.Allowances.CarryOver(ctx, authz.System(req.TenantID), p.SourceYear, true)
	if err != nil {
		return sdk.Retry("carry-over failed")
	}
	r.record(ctx, audit.TaskCarryOver, req.TenantID, audit.SubjectTenant, req.TenantID,
		map[string]any{"source_year": p.SourceYear, "created": plan.Created, "updated": plan.Updated})
	return sdk.OK(fmt.Sprintf("carried %d into %d: %d created, %d updated", p.SourceYear, p.SourceYear+1, plan.Created, plan.Updated))
}

// Reconcile runs hr:reconcile-signing across tenants.
func (r *Runner) Reconcile(ctx context.Context, req sdk.Request) sdk.Result {
	if req.TenantID != "" {
		return sdk.Permanent("hr:reconcile-signing is platform-scoped")
	}
	var p struct {
		OlderThan int `json:"older_than_minutes"`
	}
	if err := sdk.DecodeStrict(req.Payload, &p); err != nil {
		return sdk.Permanent("invalid payload: " + err.Error())
	}
	after := r.ReconcileAfter
	if p.OlderThan > 0 {
		after = time.Duration(p.OlderThan) * time.Minute
	}
	if after <= 0 {
		after = 10 * time.Minute
	}
	tenants, err := r.Store.TenantsSystem(ctx)
	if err != nil {
		return sdk.Retry("storage unavailable")
	}
	checked, applied, failed := 0, 0, 0
	for _, t := range tenants {
		c, a, err := r.Requests.Reconcile(ctx, t.TenantID, r.now().Add(-after), 500)
		checked, applied = checked+c, applied+a
		if err != nil {
			failed++
			r.warn(ctx, "reconcile", "tenant", t.TenantID, "err", err)
			continue
		}
		if c > 0 {
			r.record(ctx, audit.TaskReconcile, t.TenantID, audit.SubjectTenant, t.TenantID, map[string]any{"checked": c, "applied": a})
		}
	}
	msg := fmt.Sprintf("checked %d, applied %d", checked, applied)
	if failed > 0 {
		return sdk.Retry(fmt.Sprintf("%s; %d tenants failed", msg, failed))
	}
	return sdk.OK(msg)
}

// Sync runs hr:sync-members across tenants.
func (r *Runner) Sync(ctx context.Context, req sdk.Request) sdk.Result {
	if req.TenantID != "" {
		return sdk.Permanent("hr:sync-members is platform-scoped")
	}
	var p struct{}
	if err := sdk.DecodeStrict(req.Payload, &p); err != nil {
		return sdk.Permanent("invalid payload: " + err.Error())
	}
	tenants, err := r.Store.TenantsSystem(ctx)
	if err != nil {
		return sdk.Retry("storage unavailable")
	}
	left, failed := 0, 0
	for _, t := range tenants {
		n, err := r.syncTenant(ctx, t.TenantID)
		left += n
		if err != nil {
			failed++
			r.warn(ctx, "member sync", "tenant", t.TenantID, "err", err)
		}
	}
	msg := fmt.Sprintf("%d tenants, %d members left", len(tenants), left)
	if failed > 0 {
		return sdk.Retry(fmt.Sprintf("%s; %d tenants failed", msg, failed))
	}
	return sdk.OK(msg)
}

// syncTenant reconciles HR's member rows (and open requests) of one tenant
// with auth's active members and returns how many members left.
func (r *Runner) syncTenant(ctx context.Context, tenant string) (int, error) {
	active, err := r.Directory.Members(ctx, tenant)
	if err != nil {
		return 0, err
	}
	names := make(map[string]string, len(active))
	for _, m := range active {
		names[m.UserID] = m.DisplayName
	}
	rows, err := r.Store.ListMembers(ctx, tenant)
	if err != nil {
		return 0, err
	}
	now := r.now()
	known := map[string]store.Member{}
	for _, m := range rows {
		known[m.UserID] = m
	}
	// People with open requests but no member row are checked too.
	open, _, err := r.Store.ListRequests(ctx, tenant, repo.RequestFilter{Statuses: []string{store.StatusPending, store.StatusAwaitingSigning}, All: true})
	if err != nil {
		return 0, err
	}
	for _, rq := range open {
		if _, ok := known[rq.UserID]; !ok {
			known[rq.UserID] = store.Member{TenantID: tenant, UserID: rq.UserID, Active: true}
		}
	}
	left := 0
	for uid, m := range known {
		name, isActive := names[uid]
		switch {
		case isActive && (!m.Active || m.DisplayName != name):
			m.Active, m.DisplayName, m.SyncedAt = true, name, now
			if err := r.Store.UpsertMember(ctx, m); err != nil {
				return left, err
			}
		case !isActive && m.Active:
			m.Active, m.SyncedAt = false, now
			if err := r.Store.UpsertMember(ctx, m); err != nil {
				return left, err
			}
			n, err := r.Requests.CancelOpen(ctx, tenant, uid)
			if err != nil {
				return left, err
			}
			left++
			r.record(ctx, audit.MemberDeactivated, tenant, audit.SubjectMember, uid, map[string]any{"cancelled_requests": n})
		}
	}
	if r.Forget != nil {
		r.Forget(tenant)
	}
	if err := r.Requests.Reroute(ctx, tenant); err != nil {
		return left, err
	}
	if err := r.Store.SetMembersSynced(ctx, tenant, now); err != nil {
		return left, err
	}
	r.record(ctx, audit.TaskSync, tenant, audit.SubjectTenant, tenant, map[string]any{"left": left, "active": len(active)})
	return left, nil
}
