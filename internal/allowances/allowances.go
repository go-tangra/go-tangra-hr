// Package allowances manages yearly leave allowances, balances and the
// year-end carry-over (spec FR-003–FR-005, FR-048, FR-049, US1, US7). HR
// administrators change allowances; people read their own balance, managers
// their departments', HR viewers everyone's. Used days change only through
// request charges (internal/requests), never through this package.
package allowances

import (
	"context"
	"errors"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/routing"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// Deps wires the service.
type Deps struct {
	Store    repo.Store
	Audit    audit.Recorder
	Checker  authz.Checker
	Tree     func(ctx context.Context, tenantID string) (routing.Tree, error)
	Calendar func(ctx context.Context, tenantID string) (leavedays.Calendar, error)
	Now      func() time.Time
}

// Service manages allowances.
type Service struct{ d Deps }

// New builds the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

func (s *Service) record(ctx context.Context, subj authz.Subjects, t audit.EventType, kind, id string, err error, detail map[string]any) {
	e := audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: kind, SubjectID: id, Outcome: audit.OutcomeOK, Details: detail}
	if err != nil {
		e.Outcome, e.Reason = audit.OutcomeRefused, "error"
		if ae, ok := apperr.As(err); ok {
			e.Reason = ae.Reason
		}
	}
	audit.Emit(ctx, s.d.Audit, e)
}

func (s *Service) require(ctx context.Context, subj authz.Subjects, perm string) error {
	if err := authz.Require(ctx, s.d.Checker, subj, perm); err != nil {
		return apperr.Forbidden
	}
	return nil
}

func mapErr(err error, conflict *apperr.Error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repo.ErrNotFound):
		return apperr.NotFound
	case errors.Is(err, repo.ErrConflict):
		return conflict
	}
	return err
}

// facts returns the relationships of a person (for visibility checks).
func (s *Service) facts(ctx context.Context, tenant, userID string) (authz.Facts, error) {
	tree, err := s.d.Tree(ctx, tenant)
	if err != nil {
		return authz.Facts{}, err
	}
	return authz.Facts{OwnerID: userID, ManagerIDs: tree.Managers(userID)}, nil
}

// visibleUsers returns nil (everyone) for hr:read, else the caller and the
// members of the departments they manage.
func (s *Service) visibleUsers(ctx context.Context, subj authz.Subjects) ([]string, error) {
	if authz.Allowed(ctx, s.d.Checker, subj, authz.Read) {
		return nil, nil
	}
	if err := s.require(ctx, subj, authz.Request); err != nil {
		return nil, err
	}
	tree, err := s.d.Tree(ctx, subj.TenantID)
	if err != nil {
		return nil, err
	}
	managed := map[string]bool{}
	for _, d := range tree.Managed(subj.UserID) {
		managed[d] = true
	}
	users := []string{subj.UserID}
	for u, d := range tree.MemberOf {
		if managed[d] && u != subj.UserID {
			users = append(users, u)
		}
	}
	sort.Strings(users[1:])
	return users, nil
}

// Input is the editable part of an allowance.
type Input struct {
	UserID        string
	Year          int
	AbsenceTypeID string
	PoolID        string
	Total         leavedays.Tenths
	Carried       leavedays.Tenths
	Notes         string
}

func (s *Service) build(ctx context.Context, tenant string, in Input, a store.Allowance) (store.Allowance, error) {
	switch {
	case in.UserID == "" || len(in.UserID) > 128:
		return a, apperr.Validation.WithField("user_id")
	case in.Year < 2000 || in.Year > 2099:
		return a, apperr.Validation.WithField("year")
	case (in.AbsenceTypeID == "") == (in.PoolID == ""):
		return a, apperr.Validation.WithField("absence_type_id")
	case in.Total < 0 || in.Total > 3650:
		return a, apperr.Validation.WithField("total_days")
	case in.Carried < 0 || in.Carried > 3650:
		return a, apperr.Validation.WithField("carried_over")
	case utf8.RuneCountInString(in.Notes) > 2000:
		return a, apperr.Validation.WithField("notes")
	}
	if in.AbsenceTypeID != "" {
		t, err := s.d.Store.GetAbsenceType(ctx, tenant, in.AbsenceTypeID)
		if errors.Is(err, repo.ErrNotFound) {
			return a, apperr.Validation.WithField("absence_type_id")
		} else if err != nil {
			return a, err
		}
		if !t.Deducts || t.PoolID != "" {
			// A type in a pool draws from the pool's allowance.
			return a, apperr.Validation.WithField("absence_type_id")
		}
	} else if _, err := s.d.Store.GetPool(ctx, tenant, in.PoolID); errors.Is(err, repo.ErrNotFound) {
		return a, apperr.Validation.WithField("pool_id")
	} else if err != nil {
		return a, err
	}
	a.UserID, a.Year, a.AbsenceTypeID, a.PoolID = in.UserID, in.Year, in.AbsenceTypeID, in.PoolID
	a.Total, a.Carried, a.Notes = in.Total, in.Carried, in.Notes
	return a, nil
}

// Create adds an allowance.
func (s *Service) Create(ctx context.Context, subj authz.Subjects, in Input) (a store.Allowance, err error) {
	defer func() {
		s.record(ctx, subj, audit.AllowanceCreate, audit.SubjectAllowance, a.ID, err, map[string]any{"user_id": in.UserID, "year": in.Year})
	}()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return a, err
	}
	now := s.d.Now()
	a = store.Allowance{ID: store.NewID(), TenantID: subj.TenantID,
		Audit: store.Audit{CreatedAt: now, CreatedBy: subj.ActorID(), UpdatedAt: now, UpdatedBy: subj.ActorID()}}
	if a, err = s.build(ctx, subj.TenantID, in, a); err != nil {
		return a, err
	}
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		if err := tx.EnsureTenant(ctx, subj.TenantID); err != nil {
			return err
		}
		return tx.CreateAllowance(ctx, a)
	})
	return a, mapErr(err, apperr.Duplicate)
}

// Update changes an allowance (used days are kept; lowering the total below
// the used days is allowed and shows as an overdraw).
func (s *Service) Update(ctx context.Context, subj authz.Subjects, id string, in Input) (a store.Allowance, err error) {
	defer func() {
		s.record(ctx, subj, audit.AllowanceUpdate, audit.SubjectAllowance, id, err, map[string]any{"user_id": in.UserID, "year": in.Year})
	}()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return a, err
	}
	cur, err := s.d.Store.GetAllowance(ctx, subj.TenantID, id)
	if err != nil {
		return a, mapErr(err, apperr.Conflict)
	}
	if cur.Used > 0 && (cur.UserID != in.UserID || cur.Year != in.Year || cur.AbsenceTypeID != in.AbsenceTypeID || cur.PoolID != in.PoolID) {
		return a, apperr.InUse // charged days belong to this person, year and type/pool
	}
	if a, err = s.build(ctx, subj.TenantID, in, cur); err != nil {
		return a, err
	}
	a.UpdatedAt, a.UpdatedBy = s.d.Now(), subj.ActorID()
	if err = mapErr(s.d.Store.UpdateAllowance(ctx, a), apperr.Duplicate); err != nil {
		return a, err
	}
	return s.d.Store.GetAllowance(ctx, subj.TenantID, id)
}

// Delete removes an allowance no request has been charged to.
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string) (err error) {
	defer func() { s.record(ctx, subj, audit.AllowanceDelete, audit.SubjectAllowance, id, err, nil) }()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return err
	}
	return mapErr(s.d.Store.DeleteAllowance(ctx, subj.TenantID, id), apperr.InUse)
}

// Get returns an allowance visible to the caller.
func (s *Service) Get(ctx context.Context, subj authz.Subjects, id string) (store.Allowance, error) {
	a, err := s.d.Store.GetAllowance(ctx, subj.TenantID, id)
	if err != nil {
		return a, mapErr(err, apperr.Conflict)
	}
	f, err := s.facts(ctx, subj.TenantID, a.UserID)
	if err != nil {
		return store.Allowance{}, err
	}
	if !authz.CanViewPerson(ctx, s.d.Checker, subj, f) {
		return store.Allowance{}, apperr.NotFound
	}
	return a, nil
}

// Filter narrows List.
type Filter struct {
	UserID        string
	Year          int
	AbsenceTypeID string
	PoolID        string
	List          listquery.Request // page, size and sort (store.AllowanceList)
}

// List lists allowances visible to the caller.
func (s *Service) List(ctx context.Context, subj authz.Subjects, f Filter) ([]store.Allowance, int, error) {
	users, err := s.visibleUsers(ctx, subj)
	if err != nil {
		return nil, 0, err
	}
	rf := repo.AllowanceFilter{UserID: f.UserID, UserIDs: users, Year: f.Year, AbsenceTypeID: f.AbsenceTypeID, PoolID: f.PoolID,
		List: f.List}
	return s.d.Store.ListAllowances(ctx, subj.TenantID, rf)
}

// Line is one balance line of a person's year.
type Line struct {
	Kind          string // pool | type
	ID            string // pool or type id
	Name          string
	Color         string
	AllowanceID   string
	Total         leavedays.Tenths
	Carried       leavedays.Tenths
	Used          leavedays.Tenths
	Pending       leavedays.Tenths // pending and awaiting-signing requests
	Remaining     leavedays.Tenths
	MemberTypeIDs []string
}

// Balance returns a person's balance lines for year (FR-005).
func (s *Service) Balance(ctx context.Context, subj authz.Subjects, userID string, year int) ([]Line, error) {
	if year < 2000 || year > 2099 {
		return nil, apperr.Validation.WithField("year")
	}
	f, err := s.facts(ctx, subj.TenantID, userID)
	if err != nil {
		return nil, err
	}
	if !authz.CanViewPerson(ctx, s.d.Checker, subj, f) {
		return nil, apperr.NotFound
	}
	return s.balance(ctx, subj.TenantID, userID, year)
}

func (s *Service) balance(ctx context.Context, tenant, userID string, year int) ([]Line, error) {
	allowances, _, err := s.d.Store.ListAllowances(ctx, tenant, repo.AllowanceFilter{UserID: userID, Year: year, All: true})
	if err != nil {
		return nil, err
	}
	types, err := s.d.Store.ListAbsenceTypes(ctx, tenant)
	if err != nil {
		return nil, err
	}
	pools, err := s.d.Store.ListPools(ctx, tenant)
	if err != nil {
		return nil, err
	}
	typeByID := map[string]store.AbsenceType{}
	members := map[string][]string{}
	for _, t := range types {
		typeByID[t.ID] = t
		if t.PoolID != "" {
			members[t.PoolID] = append(members[t.PoolID], t.ID)
		}
	}
	poolByID := map[string]store.Pool{}
	for _, p := range pools {
		poolByID[p.ID] = p
	}
	pending, err := s.pending(ctx, tenant, userID, year, typeByID)
	if err != nil {
		return nil, err
	}
	out := make([]Line, 0, len(allowances))
	for _, a := range allowances {
		l := Line{AllowanceID: a.ID, Total: a.Total, Carried: a.Carried, Used: a.Used, Remaining: a.Remaining()}
		if a.PoolID != "" {
			p := poolByID[a.PoolID]
			l.Kind, l.ID, l.Name, l.Color, l.MemberTypeIDs = "pool", p.ID, p.Name, p.Color, members[p.ID]
		} else {
			t := typeByID[a.AbsenceTypeID]
			l.Kind, l.ID, l.Name, l.Color, l.MemberTypeIDs = "type", t.ID, t.Name, t.Color, []string{t.ID}
		}
		l.Pending = pending[l.ID]
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// pending sums the days of open requests in year per pool (or type).
func (s *Service) pending(ctx context.Context, tenant, userID string, year int, types map[string]store.AbsenceType) (map[string]leavedays.Tenths, error) {
	from, to := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC)
	reqs, _, err := s.d.Store.ListRequests(ctx, tenant, repo.RequestFilter{UserID: userID, From: &from, To: &to,
		Statuses: []string{store.StatusPending, store.StatusAwaitingSigning}, All: true})
	if err != nil || len(reqs) == 0 {
		return map[string]leavedays.Tenths{}, err
	}
	cal, err := s.d.Calendar(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := map[string]leavedays.Tenths{}
	for _, r := range reqs {
		t, ok := types[r.AbsenceTypeID]
		if !ok || !t.Deducts {
			continue
		}
		key := t.ID
		if t.PoolID != "" {
			key = t.PoolID
		}
		days := r.Days
		if r.Start.Year() != r.End.Year() {
			per, err := cal.PerYear(leavedays.Span{Start: r.Start, End: r.End, HalfStart: r.HalfStart, HalfEnd: r.HalfEnd})
			if err != nil {
				continue
			}
			days = per[year]
		}
		out[key] += days
	}
	return out, nil
}
