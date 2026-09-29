// Package catalog manages the tenant's absence types, allowance pools and
// public holidays (spec FR-001, FR-002, FR-004, FR-045–FR-047). Reading the
// active types and the holidays needs the calendar permission; the full lists
// need hr:read; every change needs hr:manage. Types, pools in use by
// allowances, requests or member types cannot be deleted.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// SigningChecker validates an absence type's signing settings against the
// signing module and returns them normalised (template name filled in).
type SigningChecker interface {
	CheckSettings(ctx context.Context, tenantID string, s store.SigningSettings) (store.SigningSettings, error)
}

// Deps wires the service.
type Deps struct {
	Store   repo.Store
	Audit   audit.Recorder
	Checker authz.Checker
	Signing SigningChecker // nil: signing settings are refused (signing_unavailable)
	Now     func() time.Time
	Limits  Limits
}

// Limits bound the holiday import.
type Limits struct {
	MaxImportBytes int64
	MaxImportLines int
}

// Service is the catalog.
type Service struct{ d Deps }

// New builds the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Limits.MaxImportBytes <= 0 {
		d.Limits.MaxImportBytes = 64 << 10
	}
	if d.Limits.MaxImportLines <= 0 {
		d.Limits.MaxImportLines = 500
	}
	return &Service{d: d}
}

func (s *Service) record(ctx context.Context, subj authz.Subjects, t audit.EventType, kind, id string, err error, detail map[string]any) {
	e := audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: kind, SubjectID: id, Outcome: audit.OutcomeOK, Details: detail}
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

// notFound maps repo.ErrNotFound; conflicts map to conflict.
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

var (
	colorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	iconRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9:-]{0,63}$`)
)

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, "\r\n\t") {
		return "", apperr.Validation.WithField("name")
	}
	return name, nil
}

func checkLook(description, color, icon string) error {
	if utf8.RuneCountInString(description) > 2000 {
		return apperr.Validation.WithField("description")
	}
	if color != "" && !colorRE.MatchString(color) {
		return apperr.Validation.WithField("color")
	}
	if icon != "" && !iconRE.MatchString(icon) {
		return apperr.Validation.WithField("icon")
	}
	return nil
}

func checkCap(c *leavedays.Tenths) error {
	if c != nil && (*c < 0 || *c > 3650) {
		return apperr.Validation.WithField("carry_over_cap")
	}
	return nil
}

// ------------------------------------------------------------ absence types

// TypeInput is the editable part of an absence type.
type TypeInput struct {
	Name             string
	Description      string
	Color            string
	Icon             string
	SortOrder        int
	Active           bool
	Metadata         map[string]any
	Deducts          bool
	RequiresApproval bool
	PoolID           string
	CarryOverCap     *leavedays.Tenths
	RequiresSigning  bool
	Signing          *store.SigningSettings
}

func (s *Service) buildType(ctx context.Context, tenant string, in TypeInput, t store.AbsenceType) (store.AbsenceType, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return t, err
	}
	if err := checkLook(in.Description, in.Color, in.Icon); err != nil {
		return t, err
	}
	if in.SortOrder < 0 || in.SortOrder > 10000 {
		return t, apperr.Validation.WithField("sort_order")
	}
	if in.Metadata == nil {
		in.Metadata = map[string]any{}
	}
	if raw, err := json.Marshal(in.Metadata); err != nil || len(raw) > 8<<10 {
		return t, apperr.Validation.WithField("metadata")
	}
	if err := checkCap(in.CarryOverCap); err != nil {
		return t, err
	}
	if in.PoolID != "" {
		if !in.Deducts {
			return t, apperr.Validation.WithField("pool_id")
		}
		if _, err := s.d.Store.GetPool(ctx, tenant, in.PoolID); errors.Is(err, repo.ErrNotFound) {
			return t, apperr.Validation.WithField("pool_id")
		} else if err != nil {
			return t, err
		}
	}
	var signing *store.SigningSettings
	if in.RequiresSigning {
		if !in.RequiresApproval {
			return t, apperr.Validation.WithField("requires_approval")
		}
		if in.Signing == nil {
			return t, apperr.Validation.WithField("signing")
		}
		if s.d.Signing == nil {
			return t, apperr.SigningUnavailable
		}
		checked, err := s.d.Signing.CheckSettings(ctx, tenant, *in.Signing)
		if err != nil {
			return t, err
		}
		signing = &checked
	}
	t.Name, t.Description, t.Color, t.Icon, t.SortOrder, t.Active = name, in.Description, in.Color, in.Icon, in.SortOrder, in.Active
	t.Metadata, t.Deducts, t.RequiresApproval, t.PoolID, t.CarryOverCap = in.Metadata, in.Deducts, in.RequiresApproval, in.PoolID, in.CarryOverCap
	t.RequiresSigning, t.Signing = in.RequiresSigning, signing
	return t, nil
}

// CreateType adds an absence type.
func (s *Service) CreateType(ctx context.Context, subj authz.Subjects, in TypeInput) (t store.AbsenceType, err error) {
	defer func() {
		s.record(ctx, subj, audit.AbsenceTypeCreate, audit.SubjectAbsenceType, t.ID, err, typeDetail(t))
	}()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return t, err
	}
	now := s.d.Now()
	t = store.AbsenceType{ID: store.NewID(), TenantID: subj.TenantID,
		Audit: store.Audit{CreatedAt: now, CreatedBy: subj.ActorID(), UpdatedAt: now, UpdatedBy: subj.ActorID()}}
	if t, err = s.buildType(ctx, subj.TenantID, in, t); err != nil {
		return t, err
	}
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		if err := tx.EnsureTenant(ctx, subj.TenantID); err != nil {
			return err
		}
		return tx.CreateAbsenceType(ctx, t)
	})
	return t, mapErr(err, apperr.Duplicate)
}

func typeDetail(t store.AbsenceType) map[string]any {
	return map[string]any{"deducts": t.Deducts, "requires_approval": t.RequiresApproval, "requires_signing": t.RequiresSigning}
}

// UpdateType replaces an absence type's settings.
func (s *Service) UpdateType(ctx context.Context, subj authz.Subjects, id string, in TypeInput) (t store.AbsenceType, err error) {
	defer func() { s.record(ctx, subj, audit.AbsenceTypeUpdate, audit.SubjectAbsenceType, id, err, typeDetail(t)) }()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return t, err
	}
	cur, err := s.d.Store.GetAbsenceType(ctx, subj.TenantID, id)
	if err != nil {
		return t, mapErr(err, apperr.Conflict)
	}
	if t, err = s.buildType(ctx, subj.TenantID, in, cur); err != nil {
		return t, err
	}
	t.UpdatedAt, t.UpdatedBy = s.d.Now(), subj.ActorID()
	return t, mapErr(s.d.Store.UpdateAbsenceType(ctx, t), apperr.Duplicate)
}

// DeleteType removes an unused absence type.
func (s *Service) DeleteType(ctx context.Context, subj authz.Subjects, id string) (err error) {
	defer func() { s.record(ctx, subj, audit.AbsenceTypeDelete, audit.SubjectAbsenceType, id, err, nil) }()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return err
	}
	return mapErr(s.d.Store.DeleteAbsenceType(ctx, subj.TenantID, id), apperr.InUse)
}

// GetType returns an absence type (active ones for the calendar permission,
// any with hr:read).
func (s *Service) GetType(ctx context.Context, subj authz.Subjects, id string) (store.AbsenceType, error) {
	if err := s.require(ctx, subj, authz.Calendar); err != nil {
		return store.AbsenceType{}, err
	}
	t, err := s.d.Store.GetAbsenceType(ctx, subj.TenantID, id)
	if err != nil {
		return t, mapErr(err, apperr.Conflict)
	}
	if !t.Active && !authz.Allowed(ctx, s.d.Checker, subj, authz.Read) {
		return store.AbsenceType{}, apperr.NotFound
	}
	return t, nil
}

// ListTypes lists absence types: active ones, or all with hr:read.
func (s *Service) ListTypes(ctx context.Context, subj authz.Subjects, all bool) ([]store.AbsenceType, error) {
	if err := s.require(ctx, subj, authz.Calendar); err != nil {
		return nil, err
	}
	if all {
		if err := s.require(ctx, subj, authz.Read); err != nil {
			return nil, err
		}
	}
	list, err := s.d.Store.ListAbsenceTypes(ctx, subj.TenantID)
	if err != nil || all {
		return list, err
	}
	out := list[:0]
	for _, t := range list {
		if t.Active {
			out = append(out, t)
		}
	}
	return out, nil
}

// ------------------------------------------------------------------- pools

// PoolInput is the editable part of a pool.
type PoolInput struct {
	Name         string
	Description  string
	Color        string
	Icon         string
	CarryOverCap *leavedays.Tenths
}

func buildPool(in PoolInput, p store.Pool) (store.Pool, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return p, err
	}
	if err := checkLook(in.Description, in.Color, in.Icon); err != nil {
		return p, err
	}
	if err := checkCap(in.CarryOverCap); err != nil {
		return p, err
	}
	p.Name, p.Description, p.Color, p.Icon, p.CarryOverCap = name, in.Description, in.Color, in.Icon, in.CarryOverCap
	return p, nil
}

// CreatePool adds a pool.
func (s *Service) CreatePool(ctx context.Context, subj authz.Subjects, in PoolInput) (p store.Pool, err error) {
	defer func() { s.record(ctx, subj, audit.PoolCreate, audit.SubjectPool, p.ID, err, nil) }()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return p, err
	}
	now := s.d.Now()
	p = store.Pool{ID: store.NewID(), TenantID: subj.TenantID, Audit: store.Audit{CreatedAt: now, CreatedBy: subj.ActorID(), UpdatedAt: now, UpdatedBy: subj.ActorID()}}
	if p, err = buildPool(in, p); err != nil {
		return p, err
	}
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		if err := tx.EnsureTenant(ctx, subj.TenantID); err != nil {
			return err
		}
		return tx.CreatePool(ctx, p)
	})
	return p, mapErr(err, apperr.Duplicate)
}

// UpdatePool replaces a pool's settings.
func (s *Service) UpdatePool(ctx context.Context, subj authz.Subjects, id string, in PoolInput) (p store.Pool, err error) {
	defer func() { s.record(ctx, subj, audit.PoolUpdate, audit.SubjectPool, id, err, nil) }()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return p, err
	}
	cur, err := s.d.Store.GetPool(ctx, subj.TenantID, id)
	if err != nil {
		return p, mapErr(err, apperr.Conflict)
	}
	if p, err = buildPool(in, cur); err != nil {
		return p, err
	}
	p.UpdatedAt, p.UpdatedBy = s.d.Now(), subj.ActorID()
	return p, mapErr(s.d.Store.UpdatePool(ctx, p), apperr.Duplicate)
}

// DeletePool removes a pool without member types or allowances.
func (s *Service) DeletePool(ctx context.Context, subj authz.Subjects, id string) (err error) {
	defer func() { s.record(ctx, subj, audit.PoolDelete, audit.SubjectPool, id, err, nil) }()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return err
	}
	return mapErr(s.d.Store.DeletePool(ctx, subj.TenantID, id), apperr.InUse)
}

// GetPool returns a pool (hr:read).
func (s *Service) GetPool(ctx context.Context, subj authz.Subjects, id string) (store.Pool, error) {
	if err := s.require(ctx, subj, authz.Read); err != nil {
		return store.Pool{}, err
	}
	p, err := s.d.Store.GetPool(ctx, subj.TenantID, id)
	return p, mapErr(err, apperr.Conflict)
}

// ListPools lists pools (hr:read).
func (s *Service) ListPools(ctx context.Context, subj authz.Subjects) ([]store.Pool, error) {
	if err := s.require(ctx, subj, authz.Read); err != nil {
		return nil, err
	}
	return s.d.Store.ListPools(ctx, subj.TenantID)
}
