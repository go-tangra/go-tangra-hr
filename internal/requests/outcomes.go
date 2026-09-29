package requests

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// Outcome is one signing outcome to apply.
type Outcome struct {
	TenantID     string
	SubmissionID string
	Outcome      string // store.Outcome*
	Source       string // store.SourceEvent | store.SourceReconcile
	EventID      string
}

// ApplyOutcome applies a signing outcome inside tx (FR-033–FR-036, SR-005):
// recorded at most once per submission; applied only to the request of the
// same tenant that awaits signing with that submission. Completed →
// approved and charged (an overdraw is accepted: the document is signed, and
// HR administrators are told); declined, cancelled or expired → back to
// pending with a note, nothing charged, employee and approver told.
// It returns the changed request (nil when nothing changed).
func (s *Service) ApplyOutcome(ctx context.Context, tx repo.Store, o Outcome) (*store.Request, error) {
	now := s.d.Now()
	rec := store.SigningOutcome{TenantID: o.TenantID, SubmissionID: o.SubmissionID, Outcome: o.Outcome, Source: o.Source,
		EventID: o.EventID, ReceivedAt: now}
	r, err := tx.RequestBySubmission(ctx, o.TenantID, o.SubmissionID)
	switch {
	case errors.Is(err, repo.ErrNotFound):
		rec.Result = store.ResultIgnoredUnknown
	case err != nil:
		return nil, err
	case r.Status != store.StatusAwaitingSigning:
		rec.Result, rec.RequestID = store.ResultIgnoredStatus, r.ID
	default:
		rec.Result, rec.RequestID = store.ResultApplied, r.ID
	}
	first, err := tx.RecordOutcome(ctx, rec)
	if err != nil || !first || rec.Result != store.ResultApplied {
		if first {
			s.recordOutcome(ctx, rec)
		}
		return nil, err
	}
	if r, err = tx.LockRequest(ctx, o.TenantID, r.ID); err != nil {
		return nil, err
	}
	t, err := tx.GetAbsenceType(ctx, o.TenantID, r.AbsenceTypeID)
	if err != nil {
		return nil, err
	}
	names := s.names(ctx, o.TenantID, []string{r.UserID, r.ReviewedBy})
	reviewer := r.ReviewedBy
	if o.Outcome == store.OutcomeCompleted {
		overdrawn, err := s.charge(ctx, tx, r, t, true)
		if err != nil {
			return nil, err
		}
		r.Status, r.UpdatedAt = store.StatusApproved, now
		if r, err = tx.UpdateRequest(ctx, r); err != nil {
			return nil, err
		}
		if err := s.enqueue(ctx, tx, r, t, KeyApproved, r.UserID, names, nil); err != nil {
			return nil, err
		}
		if len(overdrawn) > 0 {
			if err := s.overdrawn(ctx, tx, r, t, overdrawn[0].Remaining().String(), names); err != nil {
				return nil, err
			}
		}
	} else {
		r.Status, r.SigningNote, r.SubmissionID, r.SigningStartedAt = store.StatusPending, o.Outcome, "", nil
		r.ReviewedBy, r.ReviewedAt, r.UpdatedAt = "", nil, now
		if r, err = tx.UpdateRequest(ctx, r); err != nil {
			return nil, err
		}
		for _, to := range slices.Compact([]string{r.UserID, reviewer}) {
			if err := s.enqueue(ctx, tx, r, t, KeyFailed, to, names, map[string]string{"Outcome": o.Outcome}); err != nil {
				return nil, err
			}
		}
	}
	s.recordOutcome(ctx, rec)
	return &r, nil
}

func (s *Service) overdrawn(ctx context.Context, tx repo.Store, r store.Request, t store.AbsenceType, remaining string, names map[string]string) error {
	admins, err := s.d.People.Admins(ctx, r.TenantID)
	if err != nil {
		admins = nil // the audit entry still records the overdraw
	}
	audit.Emit(ctx, s.d.Audit, audit.Event{TenantID: r.TenantID, EventType: audit.RequestOverdraw, ActorKind: audit.ActorSystem,
		ActorID: audit.ActorSystem, SubjectKind: audit.SubjectRequest, SubjectID: r.ID, Outcome: audit.OutcomeOK,
		Details: map[string]any{"days": r.Days.Float()}})
	for _, a := range admins {
		if err := s.enqueue(ctx, tx, r, t, KeyOverdrawn, a, names, map[string]string{"Year": strconv.Itoa(r.Start.Year()), "Remaining": remaining}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) recordOutcome(ctx context.Context, rec store.SigningOutcome) {
	audit.Emit(ctx, s.d.Audit, audit.Event{TenantID: rec.TenantID, EventType: audit.RequestSigningOutcome, ActorKind: audit.ActorSystem,
		ActorID: audit.ActorSystem, SubjectKind: audit.SubjectRequest, SubjectID: rec.RequestID, Outcome: audit.OutcomeOK,
		Details: map[string]any{"submission_id": rec.SubmissionID, "outcome": rec.Outcome, "source": rec.Source, "result": rec.Result}})
}

// Applied publishes the events of a request changed by ApplyOutcome (call
// after the transaction committed).
func (s *Service) Applied(ctx context.Context, r *store.Request) {
	if r != nil {
		s.changed(ctx, *r, true)
	}
}

// Reconcile checks the requests of tenant awaiting signing since before
// cutoff against the signing module and applies terminal outcomes (FR-034).
func (s *Service) Reconcile(ctx context.Context, tenant string, cutoff time.Time, limit int) (checked, applied int, err error) {
	if s.d.Signing == nil {
		return 0, 0, nil
	}
	waiting, err := s.d.Store.AwaitingSigning(ctx, tenant, cutoff, limit)
	if err != nil {
		return 0, 0, err
	}
	for _, r := range waiting {
		checked++
		state, err := s.d.Signing.State(ctx, tenant, r.SubmissionID)
		if err != nil || state == "" {
			continue
		}
		var changed *store.Request
		err = s.d.Store.Tx(ctx, tenant, func(tx repo.Store) error {
			var err error
			changed, err = s.ApplyOutcome(ctx, tx, Outcome{TenantID: tenant, SubmissionID: r.SubmissionID, Outcome: state, Source: store.SourceReconcile})
			return err
		})
		if err != nil {
			return checked, applied, err
		}
		if changed != nil {
			applied++
			s.Applied(ctx, changed)
		}
	}
	return checked, applied, nil
}

// Reroute recomputes the approvers of every open request of tenant (after
// department changes and member sync, research D3).
func (s *Service) Reroute(ctx context.Context, tenant string) error {
	tree, err := s.d.Tree(ctx, tenant)
	if err != nil {
		return err
	}
	admins, err := s.d.People.Admins(ctx, tenant)
	if err != nil {
		return err
	}
	open, _, err := s.d.Store.ListRequests(ctx, tenant, repo.RequestFilter{Statuses: []string{store.StatusPending, store.StatusAwaitingSigning}, All: true})
	if err != nil {
		return err
	}
	for _, r := range open {
		want := tree.Approvers(r.UserID, admins)
		if slices.Equal(want, r.ApproverIDs) {
			continue
		}
		var updated store.Request
		err := s.d.Store.Tx(ctx, tenant, func(tx repo.Store) error {
			cur, err := tx.LockRequest(ctx, tenant, r.ID)
			if err != nil {
				return err
			}
			cur.ApproverIDs, cur.UpdatedAt = want, s.d.Now()
			updated, err = tx.UpdateRequest(ctx, cur)
			return err
		})
		if err != nil {
			return err
		}
		if s.d.Events != nil {
			s.d.Events.ReviewChanged(ctx, tenant, append(slices.Clone(r.ApproverIDs), want...))
			s.d.Events.RequestChanged(ctx, tenant, updated.ID, updated.Status, append([]string{updated.UserID}, want...))
		}
	}
	return nil
}

// CancelOpen cancels the pending and awaiting-signing requests of a member
// who left the tenant (FR-044) and returns how many were cancelled.
func (s *Service) CancelOpen(ctx context.Context, tenant, userID string) (int, error) {
	open, _, err := s.d.Store.ListRequests(ctx, tenant, repo.RequestFilter{UserID: userID,
		Statuses: []string{store.StatusPending, store.StatusAwaitingSigning}, All: true})
	if err != nil {
		return 0, err
	}
	n := 0
	subj := authz.System(tenant)
	for _, r := range open {
		var cur store.Request
		err := s.d.Store.Tx(ctx, tenant, func(tx repo.Store) error {
			var err error
			if cur, err = tx.LockRequest(ctx, tenant, r.ID); err != nil {
				return err
			}
			if cur.Status != store.StatusPending && cur.Status != store.StatusAwaitingSigning {
				return nil
			}
			wasAwaiting := cur.Status == store.StatusAwaitingSigning
			cur.Status, cur.UpdatedAt = store.StatusCancelled, s.d.Now()
			if cur, err = tx.UpdateRequest(ctx, cur); err != nil {
				return err
			}
			if !wasAwaiting {
				cur.SubmissionID = ""
			}
			return nil
		})
		if err != nil {
			return n, err
		}
		n++
		s.record(ctx, subj, audit.RequestCancel, r.ID, nil, map[string]any{"status_from": r.Status, "status_to": store.StatusCancelled})
		s.cancelSubmission(ctx, tenant, cur.SubmissionID, CancelMemberLeft)
		s.changed(ctx, cur, true)
	}
	return n, nil
}
