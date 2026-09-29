// Package outbox delivers the e-mails queued with request changes (research
// D8, spec FR-054): one worker drains hr_mail_outbox, resolves each
// recipient's address through auth's Contacts at send time (addresses are
// never stored) and sends the keyed notification template through the
// notification module. Failures back off and retry up to MaxAttempts; a
// recipient without an address, a permanent refusal or the last failed
// attempt drops the mail and records mail.failed (codes only).
package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/go-tangra/go-tangra-notification/sdk/v4/pkg/notifyclient"

	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/people"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// Sender is the notification client (notifyclient.Client or a lazy dialer).
type Sender interface {
	SendKey(ctx context.Context, tenantID, key, recipient string, vars map[string]string, correlationID string) (notifyclient.Result, error)
}

// Contacts resolves members' e-mail addresses.
type Contacts interface {
	Contacts(ctx context.Context, tenant string, ids []string) (map[string]people.Contact, error)
}

// Worker drains the outbox.
type Worker struct {
	Store       repo.Store
	Contacts    Contacts
	Sender      Sender
	Audit       audit.Recorder
	Log         *slog.Logger
	Now         func() time.Time
	Interval    time.Duration // poll interval (default 2 s)
	Batch       int           // mails per poll (default 50)
	MaxAttempts int           // default 5
}

func (w *Worker) defaults() {
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.Interval <= 0 {
		w.Interval = 2 * time.Second
	}
	if w.Batch <= 0 {
		w.Batch = 50
	}
	if w.MaxAttempts <= 0 {
		w.MaxAttempts = 5
	}
	if w.Log == nil {
		w.Log = slog.New(slog.DiscardHandler)
	}
}

// Run drains the outbox until ctx ends (a worker of the app).
func (w *Worker) Run(ctx context.Context) error {
	w.defaults()
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		if _, err := w.Drain(ctx); err != nil && ctx.Err() == nil {
			w.Log.Warn("outbox: drain", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// backoff is the wait before attempt n+1 (30 s, 2 min, 8 min, …, ≤ 1 h).
func backoff(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempts && d < time.Hour; i++ {
		d *= 4
	}
	return min(d, time.Hour)
}

// Drain sends the mails due now and returns how many were sent.
func (w *Worker) Drain(ctx context.Context) (int, error) {
	w.defaults()
	due, err := w.Store.DueMailSystem(ctx, w.Now(), w.Batch)
	if err != nil || len(due) == 0 {
		return 0, err
	}
	byTenant := map[string][]string{}
	for _, m := range due {
		byTenant[m.TenantID] = append(byTenant[m.TenantID], m.UserID)
	}
	contacts := map[string]map[string]people.Contact{}
	for tenant, ids := range byTenant {
		c, err := w.Contacts.Contacts(ctx, tenant, ids)
		if err != nil {
			c = nil // auth down: retry these mails later
		}
		contacts[tenant] = c
	}
	sent := 0
	for _, m := range due {
		c := contacts[m.TenantID]
		if c == nil {
			w.retry(ctx, m, "contacts_unavailable")
			continue
		}
		ct, ok := c[m.UserID]
		if !ok || ct.Email == "" {
			w.drop(ctx, m, "contact_missing")
			continue
		}
		res, err := w.Sender.SendKey(ctx, m.TenantID, m.Key, ct.Email, m.Vars, m.ID)
		switch {
		case err == nil && res.Sent:
			if err := w.Store.DeleteMailSystem(ctx, m.ID); err != nil {
				return sent, err
			}
			sent++
		case err != nil || res.Retryable:
			w.retry(ctx, m, "send_failed")
		default:
			w.drop(ctx, m, "refused")
		}
	}
	return sent, nil
}

func (w *Worker) retry(ctx context.Context, m store.Mail, reason string) {
	attempts := m.Attempts + 1
	if attempts >= w.MaxAttempts {
		w.drop(ctx, m, reason)
		return
	}
	if err := w.Store.RetryMailSystem(ctx, m.ID, attempts, w.Now().Add(backoff(attempts)), reason); err != nil {
		w.Log.Warn("outbox: retry", "err", err)
	}
}

func (w *Worker) drop(ctx context.Context, m store.Mail, reason string) {
	if err := w.Store.DeleteMailSystem(ctx, m.ID); err != nil {
		w.Log.Warn("outbox: drop", "err", err)
	}
	audit.Emit(ctx, w.Audit, audit.Event{TenantID: m.TenantID, EventType: audit.MailFailed, ActorKind: audit.ActorSystem,
		ActorID: audit.ActorSystem, SubjectKind: audit.SubjectMember, SubjectID: m.UserID, Outcome: audit.OutcomeError, Reason: reason,
		Details: map[string]any{"template": m.Key, "attempts": m.Attempts + 1}})
}
