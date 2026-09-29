// Package departments manages HR's department tree and member assignments
// (spec FR-040–FR-044, US5, decision 2): v4 auth has no org units, so HR keeps
// departments (name, optional parent, optional manager) and assigns each
// tenant member to at most one of them. The tree drives approval routing
// (internal/routing), manager read rights and the calendar grouping. Every
// change that can move approvals recomputes the routing of open requests.
package departments

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/audit"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/routing"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// People answers who the tenant's active members are.
type People interface {
	Active(ctx context.Context, tenant, userID string) (bool, error)
	Names(ctx context.Context, tenant string, ids []string) (map[string]string, error)
}

// Rerouter recomputes the approvers of a tenant's open requests.
type Rerouter interface {
	Reroute(ctx context.Context, tenantID string) error
}

// Deps wires the service.
type Deps struct {
	Store    repo.Store
	Audit    audit.Recorder
	Checker  authz.Checker
	People   People
	Rerouter Rerouter // nil: no rerouting (tests)
	Now      func() time.Time
	MaxDepth int
}

// Service manages departments.
type Service struct{ d Deps }

// New builds the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.MaxDepth <= 0 {
		d.MaxDepth = 10
	}
	return &Service{d: d}
}

// Input is the editable part of a department.
type Input struct {
	Name      string
	ParentID  string
	ManagerID string
}

func (s *Service) record(ctx context.Context, subj authz.Subjects, t audit.EventType, id string, err error, detail map[string]any) {
	e := audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectDepartment, SubjectID: id, Outcome: audit.OutcomeOK, Details: detail}
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

// Tree loads the tenant's routing tree. Members marked inactive by the member
// sync are not active; people HR has no row for are.
func (s *Service) Tree(ctx context.Context, tenantID string) (routing.Tree, error) {
	depts, err := s.d.Store.ListDepartments(ctx, tenantID)
	if err != nil {
		return routing.Tree{}, err
	}
	members, err := s.d.Store.ListMembers(ctx, tenantID)
	if err != nil {
		return routing.Tree{}, err
	}
	t := routing.Tree{Departments: make(map[string]routing.Department, len(depts)), MemberOf: map[string]string{}}
	inactive := map[string]bool{}
	for _, d := range depts {
		t.Departments[d.ID] = routing.Department{ID: d.ID, ParentID: d.ParentID, ManagerID: d.ManagerID}
	}
	for _, m := range members {
		if m.DepartmentID != "" {
			t.MemberOf[m.UserID] = m.DepartmentID
		}
		if !m.Active {
			inactive[m.UserID] = true
		}
	}
	t.Active = func(u string) bool { return !inactive[u] }
	return t, nil
}

func (s *Service) checkManager(ctx context.Context, tenant, managerID string) error {
	if managerID == "" {
		return nil
	}
	ok, err := s.d.People.Active(ctx, tenant, managerID)
	if err != nil {
		return apperr.TemporarilyUnavailable
	}
	if !ok {
		return apperr.Validation.WithField("manager_id")
	}
	return nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, "\r\n\t/") {
		return "", apperr.Validation.WithField("name")
	}
	return name, nil
}

func treeErr(err error) error {
	switch {
	case errors.Is(err, routing.ErrCycle):
		return apperr.Cycle
	case errors.Is(err, routing.ErrDepth):
		return apperr.Validation.WithField("parent_id").WithDetail(map[string]any{"reason": "too_deep"})
	case errors.Is(err, routing.ErrUnknown):
		return apperr.Validation.WithField("parent_id")
	}
	return err
}

func (s *Service) reroute(ctx context.Context, tenant string) {
	if s.d.Rerouter != nil {
		// Best effort: the member sync task reroutes again; a failure here
		// only leaves "To review" lists stale until then (decisions re-check
		// the routing anyway, research D3).
		_ = s.d.Rerouter.Reroute(ctx, tenant)
	}
}

// Create adds a department.
func (s *Service) Create(ctx context.Context, subj authz.Subjects, in Input) (d store.Department, err error) {
	defer func() {
		s.record(ctx, subj, audit.DepartmentCreate, d.ID, err, map[string]any{"manager_set": d.ManagerID != ""})
	}()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return d, err
	}
	name, err := cleanName(in.Name)
	if err != nil {
		return d, err
	}
	now := s.d.Now()
	d = store.Department{ID: store.NewID(), TenantID: subj.TenantID, ParentID: in.ParentID, Name: name, ManagerID: in.ManagerID,
		Audit: store.Audit{CreatedAt: now, CreatedBy: subj.ActorID(), UpdatedAt: now, UpdatedBy: subj.ActorID()}}
	if err = s.checkManager(ctx, subj.TenantID, in.ManagerID); err != nil {
		return d, err
	}
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		tree, err := (&Service{d: Deps{Store: tx}}).Tree(ctx, subj.TenantID)
		if err != nil {
			return err
		}
		if err := tree.CheckParent(d.ID, d.ParentID, s.d.MaxDepth); err != nil {
			return treeErr(err)
		}
		if err := tx.EnsureTenant(ctx, subj.TenantID); err != nil {
			return err
		}
		return tx.CreateDepartment(ctx, d)
	})
	if err = mapErr(err, apperr.Duplicate); err == nil && d.ManagerID != "" {
		s.reroute(ctx, subj.TenantID)
	}
	return d, err
}

// Update renames, moves or changes the manager of a department.
func (s *Service) Update(ctx context.Context, subj authz.Subjects, id string, in Input) (d store.Department, err error) {
	var moved bool
	defer func() {
		t := audit.DepartmentUpdate
		if moved {
			t = audit.DepartmentMove
		}
		s.record(ctx, subj, t, id, err, map[string]any{"manager_set": d.ManagerID != ""})
	}()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return d, err
	}
	name, err := cleanName(in.Name)
	if err != nil {
		return d, err
	}
	if err = s.checkManager(ctx, subj.TenantID, in.ManagerID); err != nil {
		return d, err
	}
	var rerouteNeeded bool
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		cur, err := tx.GetDepartment(ctx, subj.TenantID, id)
		if err != nil {
			return err
		}
		moved = cur.ParentID != in.ParentID
		rerouteNeeded = moved || cur.ManagerID != in.ManagerID
		if moved {
			tree, err := (&Service{d: Deps{Store: tx}}).Tree(ctx, subj.TenantID)
			if err != nil {
				return err
			}
			if err := tree.CheckParent(id, in.ParentID, s.d.MaxDepth); err != nil {
				return treeErr(err)
			}
		}
		d = cur
		d.Name, d.ParentID, d.ManagerID, d.UpdatedAt, d.UpdatedBy = name, in.ParentID, in.ManagerID, s.d.Now(), subj.ActorID()
		return tx.UpdateDepartment(ctx, d)
	})
	if err = mapErr(err, apperr.Duplicate); err == nil && rerouteNeeded {
		s.reroute(ctx, subj.TenantID)
	}
	return d, err
}

// Delete removes an empty department (no members, no sub-departments).
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string) (err error) {
	defer func() { s.record(ctx, subj, audit.DepartmentDelete, id, err, nil) }()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return err
	}
	return mapErr(s.d.Store.DeleteDepartment(ctx, subj.TenantID, id), apperr.NotEmpty)
}

// List returns the tenant's departments (calendar permission: the calendar
// groups by department).
func (s *Service) List(ctx context.Context, subj authz.Subjects) ([]store.Department, error) {
	if err := s.require(ctx, subj, authz.Calendar); err != nil {
		return nil, err
	}
	return s.d.Store.ListDepartments(ctx, subj.TenantID)
}

// Members returns HR's member rows (department assignments).
func (s *Service) Members(ctx context.Context, subj authz.Subjects) ([]store.Member, error) {
	if err := s.require(ctx, subj, authz.Calendar); err != nil {
		return nil, err
	}
	return s.d.Store.ListMembers(ctx, subj.TenantID)
}

// SetMembers assigns active members to department id (moving them from any
// other department) and unassigns members of id.
func (s *Service) SetMembers(ctx context.Context, subj authz.Subjects, id string, add, remove []string) (err error) {
	defer func() {
		s.record(ctx, subj, audit.DepartmentMembers, id, err, map[string]any{"added": len(add), "removed": len(remove)})
	}()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return err
	}
	if len(add)+len(remove) > 1000 {
		return apperr.Validation.WithField("members")
	}
	for _, u := range add {
		if slices.Contains(remove, u) || u == "" {
			return apperr.Validation.WithField("members")
		}
		ok, err := s.d.People.Active(ctx, subj.TenantID, u)
		if err != nil {
			return apperr.TemporarilyUnavailable
		}
		if !ok {
			return apperr.Validation.WithField(u)
		}
	}
	names, err := s.d.People.Names(ctx, subj.TenantID, add)
	if err != nil {
		return apperr.TemporarilyUnavailable
	}
	now := s.d.Now()
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		if _, err := tx.GetDepartment(ctx, subj.TenantID, id); err != nil {
			return err
		}
		for _, u := range add {
			m, err := tx.GetMember(ctx, subj.TenantID, u)
			if err != nil && !errors.Is(err, repo.ErrNotFound) {
				return err
			}
			m.TenantID, m.UserID, m.DepartmentID, m.Active, m.SyncedAt = subj.TenantID, u, id, true, now
			if n := names[u]; n != "" {
				m.DisplayName = n
			}
			if err := tx.UpsertMember(ctx, m); err != nil {
				return err
			}
		}
		for _, u := range remove {
			m, err := tx.GetMember(ctx, subj.TenantID, u)
			if errors.Is(err, repo.ErrNotFound) || (err == nil && m.DepartmentID != id) {
				continue
			}
			if err != nil {
				return err
			}
			m.DepartmentID = ""
			if err := tx.UpsertMember(ctx, m); err != nil {
				return err
			}
		}
		return nil
	})
	if err = mapErr(err, apperr.Conflict); err == nil {
		s.reroute(ctx, subj.TenantID)
	}
	return err
}
