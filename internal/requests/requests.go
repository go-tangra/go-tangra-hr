// Package requests is the leave request lifecycle (spec FR-010–FR-023,
// FR-030–FR-038, FR-050–FR-056): creation with a server-side day count and
// overlap/balance checks, routed approval, rejection, cancellation and
// revocation with exact charges and refunds, the signing workflow and its
// outcomes, lists, the team calendar and the signed document download.
//
// Identity, names and departments never come from request bodies (SR-002);
// approval rights are recomputed from the current department tree for every
// decision (SR-003); request details are only shown to their owner, approvers,
// managers, reviewer and HR readers (SR-004). E-mails are queued in the
// outbox inside the same transaction as the state change (research D8).
package requests

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/charges"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/routing"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// People answers questions about the tenant's members.
type People interface {
	Active(ctx context.Context, tenant, userID string) (bool, error)
	Names(ctx context.Context, tenant string, ids []string) (map[string]string, error)
	Admins(ctx context.Context, tenant string) ([]string, error)
}

// Signer drives the signing module (research D1). Start creates and sends the
// submission (idempotent by key); State returns a terminal outcome
// (store.Outcome*) or "" while signing is in progress.
type Signer interface {
	Start(ctx context.Context, in StartInput) (submissionID string, err error)
	Cancel(ctx context.Context, tenantID, submissionID, reasonCode string) error
	Delete(ctx context.Context, tenantID, submissionID string) error
	State(ctx context.Context, tenantID, submissionID string) (string, error)
	Document(ctx context.Context, tenantID, submissionID string) (io.ReadCloser, string, error)
}

// StartInput is everything the signing module needs for one approval.
type StartInput struct {
	TenantID       string
	Request        store.Request
	Type           store.AbsenceType
	ApproverID     string
	EmployeeName   string
	ApproverName   string
	DepartmentName string
	IdempotencyKey string
}

// Events publishes hr.* events after commits (ids and status only).
type Events interface {
	RequestChanged(ctx context.Context, tenantID, requestID, status string, users []string)
	ReviewChanged(ctx context.Context, tenantID string, users []string)
	CalendarChanged(ctx context.Context, tenantID string, from, to time.Time)
}

// Deps wires the service.
type Deps struct {
	Store    repo.Store
	Audit    audit.Recorder
	Checker  authz.Checker
	People   People
	Signing  Signer // nil: signing-required types cannot be approved
	Events   Events // nil: no events
	Calendar func(ctx context.Context, tenantID string) (leavedays.Calendar, error)
	Tree     func(ctx context.Context, tenantID string) (routing.Tree, error)
	Now      func() time.Time
	Limits   Limits
	Links    Links
}

// Limits bound inputs.
type Limits struct {
	MaxRequestDays  int // calendar days of one request (default 366)
	MaxCalendarDays int // calendar range (default 93)
}

// Links builds e-mail links.
type Links struct {
	PortalBaseURL string
}

// Service is the request lifecycle.
type Service struct{ d Deps }

// New builds the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Limits.MaxRequestDays <= 0 {
		d.Limits.MaxRequestDays = 366
	}
	if d.Limits.MaxCalendarDays <= 0 {
		d.Limits.MaxCalendarDays = 93
	}
	return &Service{d: d}
}

func (s *Service) record(ctx context.Context, subj authz.Subjects, t audit.EventType, id string, err error, detail map[string]any) {
	e := audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectRequest, SubjectID: id, Outcome: audit.OutcomeOK, Details: detail}
	if err != nil {
		e.Outcome, e.Reason = audit.OutcomeRefused, reasonOf(err)
	}
	audit.Emit(ctx, s.d.Audit, e)
}

func reasonOf(err error) string {
	if e, ok := apperr.As(err); ok {
		return e.Reason
	}
	return "error"
}

func (s *Service) require(ctx context.Context, subj authz.Subjects, perm string) error {
	if err := authz.Require(ctx, s.d.Checker, subj, perm); err != nil {
		return apperr.Forbidden
	}
	return nil
}

// mapErr maps storage errors to refusals.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repo.ErrNotFound):
		return apperr.NotFound
	case errors.Is(err, repo.ErrOverlap):
		return apperr.Overlap
	case errors.Is(err, repo.ErrConflict):
		return apperr.Conflict
	case errors.Is(err, charges.ErrInsufficient):
		ye := &charges.YearError{}
		errors.As(err, &ye)
		return apperr.InsufficientAllowance.WithDetail(map[string]any{"year": ye.Year, "requested": ye.Requested.Float(),
			"remaining": ye.Remaining.Float()})
	case errors.Is(err, charges.ErrNoAllowance):
		ye := &charges.YearError{}
		errors.As(err, &ye)
		return apperr.NoAllowance.WithDetail(map[string]any{"year": ye.Year})
	}
	return err
}

func (s *Service) today() time.Time { return leavedays.Date(s.d.Now()) }

// ---------------------------------------------------------------- helpers

// facts are the relationships of a request under the current tree; the
// approvers are recomputed (SR-003) for pending/awaiting requests.
func (s *Service) facts(ctx context.Context, r store.Request) (authz.Facts, routing.Tree, error) {
	tree, err := s.d.Tree(ctx, r.TenantID)
	if err != nil {
		return authz.Facts{}, tree, err
	}
	f := authz.Facts{OwnerID: r.UserID, ApproverIDs: r.ApproverIDs, ManagerIDs: tree.Managers(r.UserID), ReviewedBy: r.ReviewedBy}
	return f, tree, nil
}

func (s *Service) approvers(ctx context.Context, tree routing.Tree, tenant, userID string) ([]string, error) {
	admins, err := s.d.People.Admins(ctx, tenant)
	if err != nil {
		return nil, apperr.TemporarilyUnavailable
	}
	return tree.Approvers(userID, admins), nil
}

// canView reports whether subj may read r's details.
func (s *Service) canView(ctx context.Context, subj authz.Subjects, r store.Request) (bool, error) {
	f, _, err := s.facts(ctx, r)
	if err != nil {
		return false, err
	}
	return authz.CanViewRequest(ctx, s.d.Checker, subj, f), nil
}

// load returns a request visible to subj (404 otherwise).
func (s *Service) load(ctx context.Context, subj authz.Subjects, id string) (store.Request, error) {
	r, err := s.d.Store.GetRequest(ctx, subj.TenantID, id)
	if err != nil {
		return r, mapErr(err)
	}
	ok, err := s.canView(ctx, subj, r)
	if err != nil {
		return store.Request{}, err
	}
	if !ok {
		return store.Request{}, apperr.NotFound
	}
	return r, nil
}

// perYear splits a request's stored days by calendar year (the stored total
// wins over later holiday changes, FR-047: the difference goes to the last
// year).
func (s *Service) perYear(ctx context.Context, r store.Request) (map[int]leavedays.Tenths, error) {
	if r.Start.Year() == r.End.Year() {
		return map[int]leavedays.Tenths{r.Start.Year(): r.Days}, nil
	}
	cal, err := s.d.Calendar(ctx, r.TenantID)
	if err != nil {
		return nil, err
	}
	per, err := cal.PerYear(leavedays.Span{Start: r.Start, End: r.End, HalfStart: r.HalfStart, HalfEnd: r.HalfEnd})
	if err != nil {
		per = map[int]leavedays.Tenths{}
	}
	var sum leavedays.Tenths
	for _, v := range per {
		sum += v
	}
	per[r.End.Year()] = max(per[r.End.Year()]+r.Days-sum, 0)
	if per[r.End.Year()] == 0 {
		delete(per, r.End.Year())
	}
	return per, nil
}

// allowancesFor finds the allowance of each year a request touches.
func allowancesFor(ctx context.Context, tx repo.Store, r store.Request, t store.AbsenceType, years map[int]leavedays.Tenths) (map[int]store.Allowance, error) {
	out := map[int]store.Allowance{}
	for y := range years {
		typeID, poolID := t.ID, ""
		if t.PoolID != "" {
			typeID, poolID = "", t.PoolID
		}
		a, err := tx.FindAllowance(ctx, r.TenantID, r.UserID, y, typeID, poolID)
		if errors.Is(err, repo.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[y] = a
	}
	return out, nil
}

// charge debits a request of a deducting type (inside tx) and returns the
// allowances left overdrawn (only possible with allowOverdraw).
func (s *Service) charge(ctx context.Context, tx repo.Store, r store.Request, t store.AbsenceType, allowOverdraw bool) ([]charges.Allowance, error) {
	if !t.Deducts {
		return nil, nil
	}
	years, err := s.perYear(ctx, r)
	if err != nil {
		return nil, err
	}
	found, err := allowancesFor(ctx, tx, r, t, years)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(found))
	for _, a := range found {
		ids = append(ids, a.ID)
	}
	locked, err := tx.LockAllowances(ctx, r.TenantID, ids)
	if err != nil {
		return nil, err
	}
	byYear := map[int]charges.Allowance{}
	for y, a := range found {
		l := locked[a.ID]
		byYear[y] = charges.Allowance{ID: l.ID, Year: y, Total: l.Total, Carried: l.Carried, Used: l.Used}
	}
	plan, err := charges.Plan(years, byYear, allowOverdraw)
	if err != nil {
		return nil, err
	}
	now := s.d.Now()
	rows := make([]store.Charge, 0, len(plan))
	for _, c := range plan {
		if err := tx.AddUsed(ctx, r.TenantID, c.AllowanceID, c.Days, now); err != nil {
			return nil, err
		}
		rows = append(rows, store.Charge{RequestID: r.ID, TenantID: r.TenantID, AllowanceID: c.AllowanceID, Year: c.Year, Days: c.Days})
	}
	if err := tx.AddCharges(ctx, rows); err != nil {
		return nil, err
	}
	return charges.Overdrawn(plan, byYear), nil
}

// refund returns exactly what a request was charged (inside tx).
func (s *Service) refund(ctx context.Context, tx repo.Store, r store.Request) (leavedays.Tenths, error) {
	cs, err := tx.Charges(ctx, r.TenantID, r.ID)
	if err != nil || len(cs) == 0 {
		return 0, err
	}
	ids := make([]string, 0, len(cs))
	planned := make([]charges.Charge, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, c.AllowanceID)
		planned = append(planned, charges.Charge{AllowanceID: c.AllowanceID, Year: c.Year, Days: c.Days})
	}
	locked, err := tx.LockAllowances(ctx, r.TenantID, ids)
	if err != nil {
		return 0, err
	}
	used := map[string]leavedays.Tenths{}
	for id, a := range locked {
		used[id] = a.Used
	}
	var total leavedays.Tenths
	now := s.d.Now()
	for id, days := range charges.Refund(planned, used) {
		if err := tx.AddUsed(ctx, r.TenantID, id, -days, now); err != nil {
			return 0, err
		}
		total += days
	}
	return total, tx.DeleteCharges(ctx, r.TenantID, r.ID)
}

// ------------------------------------------------------------------ create

// Input is a new request.
type Input struct {
	UserID        string // "" = the caller
	AbsenceTypeID string
	Start, End    time.Time
	HalfStart     bool
	HalfEnd       bool
	Reason        string
	Notes         string
}

// Preview is the result of checking a request without creating it.
type Preview struct {
	Days      leavedays.Tenths
	PerYear   map[int]leavedays.Tenths
	Approvers []string // user ids
	Overlaps  bool
	// Remaining is the balance after this request per year (deducting types
	// with an allowance only).
	Remaining map[int]leavedays.Tenths
	Status    string // pending or approved (no approval needed)
}

type prepared struct {
	req  store.Request
	typ  store.AbsenceType
	per  map[int]leavedays.Tenths
	tree routing.Tree
}

func (s *Service) prepare(ctx context.Context, subj authz.Subjects, in Input) (prepared, error) {
	var p prepared
	if in.UserID == "" {
		in.UserID = subj.UserID
	}
	if err := authz.ActFor(ctx, s.d.Checker, subj, in.UserID); err != nil {
		return p, apperr.Forbidden
	}
	if in.UserID != subj.UserID {
		ok, err := s.d.People.Active(ctx, subj.TenantID, in.UserID)
		if err != nil {
			return p, apperr.TemporarilyUnavailable
		}
		if !ok {
			return p, apperr.Validation.WithField("user_id")
		}
	}
	switch {
	case utf8.RuneCountInString(in.Reason) > 1000:
		return p, apperr.Validation.WithField("reason")
	case utf8.RuneCountInString(in.Notes) > 2000:
		return p, apperr.Validation.WithField("notes")
	}
	t, err := s.d.Store.GetAbsenceType(ctx, subj.TenantID, in.AbsenceTypeID)
	if errors.Is(err, repo.ErrNotFound) || (err == nil && !t.Active) {
		return p, apperr.Validation.WithField("absence_type_id")
	} else if err != nil {
		return p, err
	}
	cal, err := s.d.Calendar(ctx, subj.TenantID)
	if err != nil {
		return p, err
	}
	span := leavedays.Span{Start: in.Start, End: in.End, HalfStart: in.HalfStart, HalfEnd: in.HalfEnd, MaxDays: s.d.Limits.MaxRequestDays}
	if y := leavedays.Date(in.Start).Year(); y < 2000 || y > 2099 {
		return p, apperr.Validation.WithField("start_date")
	}
	per, err := cal.PerYear(span)
	switch {
	case errors.Is(err, leavedays.ErrRange), errors.Is(err, leavedays.ErrSpan):
		return p, apperr.Validation.WithField("end_date")
	case errors.Is(err, leavedays.ErrHalf):
		return p, apperr.Validation.WithField("half_end")
	case errors.Is(err, leavedays.ErrZero):
		return p, apperr.ZeroDays
	}
	var days leavedays.Tenths
	for _, v := range per {
		days += v
	}
	tree, err := s.d.Tree(ctx, subj.TenantID)
	if err != nil {
		return p, err
	}
	now := s.d.Now()
	p = prepared{typ: t, per: per, tree: tree, req: store.Request{ID: store.NewID(), TenantID: subj.TenantID, UserID: in.UserID,
		AbsenceTypeID: t.ID, Start: leavedays.Date(in.Start), End: leavedays.Date(in.End), HalfStart: in.HalfStart, HalfEnd: in.HalfEnd,
		Days: days, Status: store.StatusPending, Reason: strings.TrimSpace(in.Reason), Notes: in.Notes, Version: 1, CreatedAt: now,
		CreatedBy: subj.ActorID(), UpdatedAt: now}}
	if t.RequiresApproval {
		if p.req.ApproverIDs, err = s.approvers(ctx, tree, subj.TenantID, in.UserID); err != nil {
			return p, err
		}
	} else {
		p.req.Status = store.StatusApproved
	}
	return p, nil
}

// Preview checks a request without writing it (day count, balance after,
// approvers, overlap).
func (s *Service) Preview(ctx context.Context, subj authz.Subjects, in Input) (Preview, error) {
	p, err := s.prepare(ctx, subj, in)
	if err != nil {
		return Preview{}, err
	}
	out := Preview{Days: p.req.Days, PerYear: p.per, Approvers: p.req.ApproverIDs, Status: p.req.Status, Remaining: map[int]leavedays.Tenths{}}
	if out.Overlaps, err = s.d.Store.Overlaps(ctx, subj.TenantID, p.req.UserID, p.req.Start, p.req.End, ""); err != nil {
		return out, err
	}
	if p.typ.Deducts {
		found, err := allowancesFor(ctx, s.d.Store, p.req, p.typ, p.per)
		if err != nil {
			return out, err
		}
		for y, a := range found {
			out.Remaining[y] = a.Remaining() - p.per[y]
		}
	}
	return out, nil
}

// Create files a request (FR-010–FR-013). A type without approval is
// approved and charged at once; otherwise the routed approvers are e-mailed.
func (s *Service) Create(ctx context.Context, subj authz.Subjects, in Input) (r store.Request, err error) {
	defer func() {
		s.record(ctx, subj, audit.RequestCreate, r.ID, err, map[string]any{"user_id": r.UserID, "type_id": r.AbsenceTypeID,
			"days": r.Days.Float(), "status": r.Status})
	}()
	p, err := s.prepare(ctx, subj, in)
	if err != nil {
		return p.req, err
	}
	r = p.req
	if r.Status == store.StatusApproved {
		r.ReviewedAt = &r.CreatedAt // approved on creation (no approval needed)
	}
	names := s.names(ctx, subj.TenantID, append([]string{r.UserID}, r.ApproverIDs...))
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		if err := tx.EnsureTenant(ctx, subj.TenantID); err != nil {
			return err
		}
		if p.typ.Deducts && r.Status == store.StatusPending {
			// Early balance check (the binding one runs at approval, under lock).
			found, err := allowancesFor(ctx, tx, r, p.typ, p.per)
			if err != nil {
				return err
			}
			byYear := map[int]charges.Allowance{}
			for y, a := range found {
				byYear[y] = charges.Allowance{ID: a.ID, Year: y, Total: a.Total, Carried: a.Carried, Used: a.Used}
			}
			if _, err := charges.Plan(p.per, byYear, false); err != nil {
				return err
			}
		}
		if err := tx.CreateRequest(ctx, r); err != nil {
			return err
		}
		if r.Status == store.StatusApproved {
			_, err := s.charge(ctx, tx, r, p.typ, false)
			return err
		}
		for _, a := range r.ApproverIDs {
			if err := s.enqueue(ctx, tx, r, p.typ, KeySubmitted, a, names, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err = mapErr(err); err != nil {
		return r, err
	}
	s.changed(ctx, r, true)
	return r, nil
}

// ------------------------------------------------------------- edit/cancel

// Update edits reason and notes of a pending request (owner or hr:manage).
func (s *Service) Update(ctx context.Context, subj authz.Subjects, id, reason, notes string) (r store.Request, err error) {
	defer func() { s.record(ctx, subj, audit.RequestUpdate, id, err, map[string]any{"status": r.Status}) }()
	if utf8.RuneCountInString(reason) > 1000 {
		return r, apperr.Validation.WithField("reason")
	}
	if utf8.RuneCountInString(notes) > 2000 {
		return r, apperr.Validation.WithField("notes")
	}
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		cur, err := tx.LockRequest(ctx, subj.TenantID, id)
		if err != nil {
			return err
		}
		if !authz.Is(subj, cur.UserID) && !authz.Allowed(ctx, s.d.Checker, subj, authz.Manage) {
			if ok, _ := s.canView(ctx, subj, cur); !ok {
				return repo.ErrNotFound
			}
			return apperr.Forbidden
		}
		if cur.Status != store.StatusPending {
			return apperr.InvalidTransition
		}
		cur.Reason, cur.Notes, cur.UpdatedAt = strings.TrimSpace(reason), notes, s.d.Now()
		r, err = tx.UpdateRequest(ctx, cur)
		return err
	})
	return r, mapErr(err)
}

// Cancel withdraws a request (owner or hr:manage): pending and awaiting
// signing (the submission is cancelled), or approved before it starts (the
// days are refunded). A started approved leave must be revoked (FR-017).
func (s *Service) Cancel(ctx context.Context, subj authz.Subjects, id string) (r store.Request, err error) {
	var from string
	defer func() {
		s.record(ctx, subj, audit.RequestCancel, id, err, map[string]any{"status_from": from, "status_to": r.Status})
	}()
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		cur, err := tx.LockRequest(ctx, subj.TenantID, id)
		if err != nil {
			return err
		}
		from = cur.Status
		if !authz.Is(subj, cur.UserID) && !authz.Allowed(ctx, s.d.Checker, subj, authz.Manage) {
			if ok, _ := s.canView(ctx, subj, cur); !ok {
				return repo.ErrNotFound
			}
			return apperr.Forbidden
		}
		switch cur.Status {
		case store.StatusPending, store.StatusAwaitingSigning:
		case store.StatusApproved:
			if !cur.Start.After(s.today()) {
				return apperr.InvalidTransition
			}
			if _, err := s.refund(ctx, tx, cur); err != nil {
				return err
			}
		default:
			return apperr.InvalidTransition
		}
		cur.Status, cur.UpdatedAt = store.StatusCancelled, s.d.Now()
		r, err = tx.UpdateRequest(ctx, cur)
		return err
	})
	if err = mapErr(err); err != nil {
		return r, err
	}
	if from == store.StatusAwaitingSigning {
		s.cancelSubmission(ctx, r.TenantID, r.SubmissionID, CancelHR)
	}
	s.changed(ctx, r, true)
	return r, nil
}

// Delete removes a rejected or cancelled request (owner or hr:manage) and its
// signing submission (FR-037).
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string) (err error) {
	defer func() { s.record(ctx, subj, audit.RequestDelete, id, err, nil) }()
	var sub string
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		cur, err := tx.LockRequest(ctx, subj.TenantID, id)
		if err != nil {
			return err
		}
		if !authz.Is(subj, cur.UserID) && !authz.Allowed(ctx, s.d.Checker, subj, authz.Manage) {
			if ok, _ := s.canView(ctx, subj, cur); !ok {
				return repo.ErrNotFound
			}
			return apperr.Forbidden
		}
		if cur.Status != store.StatusRejected && cur.Status != store.StatusCancelled {
			return apperr.InvalidTransition
		}
		sub = cur.SubmissionID
		return tx.DeleteRequest(ctx, subj.TenantID, id)
	})
	if err = mapErr(err); err != nil {
		return err
	}
	if sub != "" && s.d.Signing != nil {
		_ = s.d.Signing.Delete(ctx, subj.TenantID, sub) // best effort; signing's retention sweep catches leftovers
	}
	return nil
}

// Cancel reason codes sent to the signing module.
const (
	CancelHR         = "hr_cancelled"
	CancelRejected   = "hr_rejected"
	CancelMemberLeft = "member_left"
)

func (s *Service) cancelSubmission(ctx context.Context, tenant, submissionID, reason string) {
	if submissionID == "" || s.d.Signing == nil {
		return
	}
	// Best effort: a submission left running completes or expires in signing,
	// and the outcome is then ignored because the request is no longer
	// awaiting signing.
	_ = s.d.Signing.Cancel(ctx, tenant, submissionID, reason)
}

func (s *Service) names(ctx context.Context, tenant string, ids []string) map[string]string {
	if s.d.People == nil {
		return map[string]string{}
	}
	n, err := s.d.People.Names(ctx, tenant, slices.Compact(slices.Sorted(slices.Values(ids))))
	if err != nil || n == nil {
		return map[string]string{}
	}
	return n
}

// changed publishes the request and calendar events after a commit.
func (s *Service) changed(ctx context.Context, r store.Request, calendar bool) {
	if s.d.Events == nil {
		return
	}
	users := append([]string{r.UserID}, r.ApproverIDs...)
	s.d.Events.RequestChanged(ctx, r.TenantID, r.ID, r.Status, users)
	s.d.Events.ReviewChanged(ctx, r.TenantID, r.ApproverIDs)
	if calendar {
		s.d.Events.CalendarChanged(ctx, r.TenantID, r.Start, r.End)
	}
}
