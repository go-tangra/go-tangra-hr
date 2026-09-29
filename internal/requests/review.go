package requests

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// review checks the caller may decide r under the current routing (SR-003)
// and returns the recomputed approvers. Callers unrelated to the request get
// "not found".
func (s *Service) review(ctx context.Context, subj authz.Subjects, r store.Request) ([]string, error) {
	f, tree, err := s.facts(ctx, r)
	if err != nil {
		return nil, err
	}
	approvers, err := s.approvers(ctx, tree, r.TenantID, r.UserID)
	if err != nil {
		return nil, err
	}
	f.ApproverIDs = approvers
	if !authz.CanViewRequest(ctx, s.d.Checker, subj, f) && !authz.Allowed(ctx, s.d.Checker, subj, authz.Manage) {
		return nil, apperr.NotFound
	}
	switch err := authz.Review(ctx, s.d.Checker, subj, f); {
	case errors.Is(err, authz.ErrSelfReview):
		return nil, apperr.SelfReview
	case err != nil:
		return nil, apperr.NotRouted
	}
	return approvers, nil
}

func checkNotes(notes string) error {
	if utf8.RuneCountInString(notes) > 1000 {
		return apperr.Validation.WithField("notes")
	}
	return nil
}

// Approve approves a pending request (FR-018). A signing-required type
// starts the signing workflow instead (FR-031): the request awaits signing
// and nothing is charged until the submission completes.
func (s *Service) Approve(ctx context.Context, subj authz.Subjects, id, notes string) (r store.Request, err error) {
	var charged float64
	defer func() {
		s.record(ctx, subj, audit.RequestApprove, id, err, map[string]any{"status_to": r.Status, "charged_days": charged})
	}()
	if err = checkNotes(notes); err != nil {
		return r, err
	}
	cur, err := s.d.Store.GetRequest(ctx, subj.TenantID, id)
	if err != nil {
		return r, mapErr(err)
	}
	approvers, err := s.review(ctx, subj, cur)
	if err != nil {
		return r, err
	}
	if cur.Status != store.StatusPending {
		return cur, apperr.InvalidTransition
	}
	t, err := s.d.Store.GetAbsenceType(ctx, subj.TenantID, cur.AbsenceTypeID)
	if err != nil {
		return r, mapErr(err)
	}
	if t.RequiresSigning {
		return s.startSigning(ctx, subj, cur, t, approvers, notes)
	}
	names := s.names(ctx, subj.TenantID, []string{cur.UserID, subj.UserID})
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		locked, err := tx.LockRequest(ctx, subj.TenantID, id)
		if err != nil {
			return err
		}
		if locked.Status != store.StatusPending || locked.Version != cur.Version {
			return apperr.Conflict
		}
		if _, err := s.charge(ctx, tx, locked, t, false); err != nil {
			return err
		}
		now := s.d.Now()
		locked.Status, locked.ReviewedBy, locked.ReviewedAt, locked.ReviewNotes = store.StatusApproved, subj.UserID, &now, strings.TrimSpace(notes)
		locked.ApproverIDs, locked.UpdatedAt = approvers, now
		if r, err = tx.UpdateRequest(ctx, locked); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, r, t, KeyApproved, r.UserID, names, nil)
	})
	if err = mapErr(err); err != nil {
		return r, err
	}
	if t.Deducts {
		charged = r.Days.Float()
	}
	s.changed(ctx, r, true)
	return r, nil
}

// startSigning creates the submission first (idempotent by request and
// attempt), then records it; if recording fails the submission is cancelled
// so no document is left waiting for a request that stayed pending (FR-032).
func (s *Service) startSigning(ctx context.Context, subj authz.Subjects, cur store.Request, t store.AbsenceType, approvers []string,
	notes string) (r store.Request, err error) {
	defer func() {
		detail := map[string]any{"submission_id": r.SubmissionID}
		s.record(ctx, subj, audit.RequestSigningStart, cur.ID, err, detail)
	}()
	if s.d.Signing == nil || t.Signing == nil {
		return cur, apperr.SigningUnavailable
	}
	ok, err := s.d.People.Active(ctx, subj.TenantID, cur.UserID)
	if err != nil {
		return cur, apperr.TemporarilyUnavailable
	}
	if !ok {
		return cur, apperr.SignerInactive
	}
	names := s.names(ctx, subj.TenantID, []string{cur.UserID, subj.UserID})
	attempt := cur.SigningAttempt + 1
	sub, err := s.d.Signing.Start(ctx, StartInput{TenantID: subj.TenantID, Request: cur, Type: t, ApproverID: subj.UserID,
		EmployeeName: names[cur.UserID], ApproverName: names[subj.UserID], DepartmentName: s.departmentName(ctx, subj.TenantID, cur.UserID),
		IdempotencyKey: cur.ID + ":" + strconv.Itoa(attempt)})
	if err != nil {
		if _, ok := apperr.As(err); ok {
			return cur, err
		}
		return cur, apperr.SigningUnavailable
	}
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		locked, err := tx.LockRequest(ctx, subj.TenantID, cur.ID)
		if err != nil {
			return err
		}
		if locked.Status != store.StatusPending || locked.Version != cur.Version {
			return apperr.Conflict
		}
		now := s.d.Now()
		locked.Status, locked.ReviewedBy, locked.ReviewedAt, locked.ReviewNotes = store.StatusAwaitingSigning, subj.UserID, &now, strings.TrimSpace(notes)
		locked.SubmissionID, locked.SigningNote, locked.SigningStartedAt, locked.SigningAttempt = sub, "", &now, attempt
		locked.ApproverIDs, locked.UpdatedAt = approvers, now
		r, err = tx.UpdateRequest(ctx, locked)
		return err
	})
	if err = mapErr(err); err != nil {
		s.cancelSubmission(ctx, subj.TenantID, sub, CancelHR)
		return cur, err
	}
	s.changed(ctx, r, true)
	return r, nil
}

// departmentName is the name of the person's department ("" when none).
func (s *Service) departmentName(ctx context.Context, tenant, userID string) string {
	m, err := s.d.Store.GetMember(ctx, tenant, userID)
	if err != nil || m.DepartmentID == "" {
		return ""
	}
	d, err := s.d.Store.GetDepartment(ctx, tenant, m.DepartmentID)
	if err != nil {
		return ""
	}
	return d.Name
}

// Reject rejects a pending or awaiting-signing request with notes (FR-018);
// a running submission is cancelled (FR-037).
func (s *Service) Reject(ctx context.Context, subj authz.Subjects, id, notes string) (r store.Request, err error) {
	var from string
	defer func() {
		s.record(ctx, subj, audit.RequestReject, id, err, map[string]any{"status_from": from, "status_to": r.Status})
	}()
	if err = checkNotes(notes); err != nil {
		return r, err
	}
	cur, err := s.d.Store.GetRequest(ctx, subj.TenantID, id)
	if err != nil {
		return r, mapErr(err)
	}
	if _, err = s.review(ctx, subj, cur); err != nil {
		return r, err
	}
	t, err := s.d.Store.GetAbsenceType(ctx, subj.TenantID, cur.AbsenceTypeID)
	if err != nil {
		return r, mapErr(err)
	}
	names := s.names(ctx, subj.TenantID, []string{cur.UserID, subj.UserID})
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		locked, err := tx.LockRequest(ctx, subj.TenantID, id)
		if err != nil {
			return err
		}
		from = locked.Status
		if locked.Status != store.StatusPending && locked.Status != store.StatusAwaitingSigning {
			return apperr.InvalidTransition
		}
		now := s.d.Now()
		locked.Status, locked.ReviewedBy, locked.ReviewedAt, locked.ReviewNotes = store.StatusRejected, subj.UserID, &now, strings.TrimSpace(notes)
		locked.UpdatedAt = now
		if r, err = tx.UpdateRequest(ctx, locked); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, r, t, KeyRejected, r.UserID, names, nil)
	})
	if err = mapErr(err); err != nil {
		return r, err
	}
	if from == store.StatusAwaitingSigning {
		s.cancelSubmission(ctx, r.TenantID, r.SubmissionID, CancelRejected)
	}
	s.changed(ctx, r, true)
	return r, nil
}

// Revoke revokes an approved request with a reason and refunds exactly what
// was charged (FR-018). A signed document is kept as a record (FR-037).
func (s *Service) Revoke(ctx context.Context, subj authz.Subjects, id, reason string) (r store.Request, err error) {
	var refunded float64
	defer func() {
		s.record(ctx, subj, audit.RequestRevoke, id, err, map[string]any{"status_to": r.Status, "refunded_days": refunded})
	}()
	if err = checkNotes(reason); err != nil {
		return r, err
	}
	cur, err := s.d.Store.GetRequest(ctx, subj.TenantID, id)
	if err != nil {
		return r, mapErr(err)
	}
	if _, err = s.review(ctx, subj, cur); err != nil {
		return r, err
	}
	t, err := s.d.Store.GetAbsenceType(ctx, subj.TenantID, cur.AbsenceTypeID)
	if err != nil {
		return r, mapErr(err)
	}
	names := s.names(ctx, subj.TenantID, []string{cur.UserID, subj.UserID})
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		locked, err := tx.LockRequest(ctx, subj.TenantID, id)
		if err != nil {
			return err
		}
		if locked.Status != store.StatusApproved {
			return apperr.InvalidTransition
		}
		days, err := s.refund(ctx, tx, locked)
		if err != nil {
			return err
		}
		refunded = days.Float()
		now := s.d.Now()
		locked.Status, locked.ReviewedBy, locked.ReviewedAt, locked.ReviewNotes = store.StatusRevoked, subj.UserID, &now, strings.TrimSpace(reason)
		locked.UpdatedAt = now
		if r, err = tx.UpdateRequest(ctx, locked); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, r, t, KeyRevoked, r.UserID, names, nil)
	})
	if err = mapErr(err); err != nil {
		return r, err
	}
	s.changed(ctx, r, true)
	return r, nil
}
