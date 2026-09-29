// Package memstore is an in-memory repo.Store with the semantics of the
// database implementation (repodb): tenant scoping (a foreign id is "not
// found"), case-insensitive unique names, optimistic request versions,
// foreign-key style in-use refusals, the overlap exclusion of active
// requests, the used ≥ 0 check and transactions that roll back on error (Tx
// serialises all transactions, which also gives the Lock* methods their
// row-lock meaning). It backs every service unit test.
package memstore

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

type data struct {
	tenants    map[string]store.TenantState
	types      map[string]store.AbsenceType
	pools      map[string]store.Pool
	allowances map[string]store.Allowance
	requests   map[string]store.Request
	charges    map[string][]store.Charge // request id → charges
	depts      map[string]store.Department
	members    map[string]store.Member // tenant/user
	holidays   map[string]store.Holiday
	outcomes   map[string]store.SigningOutcome // tenant/submission
	runs       []store.CarryOverRun
	mail       map[string]store.Mail
	audit      []store.AuditRow
}

func newData() *data {
	return &data{tenants: map[string]store.TenantState{}, types: map[string]store.AbsenceType{}, pools: map[string]store.Pool{},
		allowances: map[string]store.Allowance{}, requests: map[string]store.Request{}, charges: map[string][]store.Charge{},
		depts: map[string]store.Department{}, members: map[string]store.Member{}, holidays: map[string]store.Holiday{},
		outcomes: map[string]store.SigningOutcome{}, mail: map[string]store.Mail{}}
}

func (d *data) clone() *data {
	c := newData()
	maps.Copy(c.tenants, d.tenants)
	for k, v := range d.types {
		c.types[k] = cloneType(v)
	}
	maps.Copy(c.pools, d.pools)
	maps.Copy(c.allowances, d.allowances)
	for k, v := range d.requests {
		c.requests[k] = cloneRequest(v)
	}
	for k, v := range d.charges {
		c.charges[k] = slices.Clone(v)
	}
	maps.Copy(c.depts, d.depts)
	maps.Copy(c.members, d.members)
	maps.Copy(c.holidays, d.holidays)
	maps.Copy(c.outcomes, d.outcomes)
	c.runs = slices.Clone(d.runs)
	for k, v := range d.mail {
		v.Vars = maps.Clone(v.Vars)
		c.mail[k] = v
	}
	c.audit = slices.Clone(d.audit)
	return c
}

func cloneType(t store.AbsenceType) store.AbsenceType {
	t.Metadata = maps.Clone(t.Metadata)
	if t.CarryOverCap != nil {
		v := *t.CarryOverCap
		t.CarryOverCap = &v
	}
	if t.Signing != nil {
		s := *t.Signing
		s.Fields = maps.Clone(s.Fields)
		t.Signing = &s
	}
	return t
}

func clonePool(p store.Pool) store.Pool {
	if p.CarryOverCap != nil {
		v := *p.CarryOverCap
		p.CarryOverCap = &v
	}
	return p
}

func cloneRequest(r store.Request) store.Request {
	r.ApproverIDs = slices.Clone(r.ApproverIDs)
	if r.ReviewedAt != nil {
		v := *r.ReviewedAt
		r.ReviewedAt = &v
	}
	if r.SigningStartedAt != nil {
		v := *r.SigningStartedAt
		r.SigningStartedAt = &v
	}
	return r
}

// Mem implements repo.Store in memory.
type Mem struct {
	mu   sync.Mutex
	txMu sync.Mutex
	d    *data
	// Err, when set, is returned by every call (failure injection).
	Err error
	// FailOn, when set, makes calls to the named method return FailErr.
	FailOn  string
	FailErr error
}

var _ repo.Store = (*Mem)(nil)

// New returns an empty store.
func New() *Mem { return &Mem{d: newData()} }

// SetErr injects err into every subsequent call (nil clears it).
func (m *Mem) SetErr(err error) { m.mu.Lock(); m.Err = err; m.mu.Unlock() }

// Fail makes the named method fail with err (empty name clears it).
func (m *Mem) Fail(method string, err error) {
	m.mu.Lock()
	m.FailOn, m.FailErr = method, err
	m.mu.Unlock()
}

func (m *Mem) lock(method string) (func(), error) {
	m.mu.Lock()
	if m.Err != nil {
		err := m.Err
		m.mu.Unlock()
		return func() {}, err
	}
	if m.FailOn != "" && m.FailOn == method {
		err := m.FailErr
		m.mu.Unlock()
		return func() {}, err
	}
	return m.mu.Unlock, nil
}

// Audit returns the recorded audit rows (tests).
func (m *Mem) Audit() []store.AuditRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.d.audit)
}

// Mail returns every queued mail ordered by id (tests).
func (m *Mem) Mail() []store.Mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Collect(maps.Values(m.d.mail))
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Outcomes returns every recorded signing outcome (tests).
func (m *Mem) Outcomes() []store.SigningOutcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Collect(maps.Values(m.d.outcomes))
}

// Runs returns the carry-over runs (tests).
func (m *Mem) Runs() []store.CarryOverRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.d.runs)
}

// PutRequest stores r as is (tests).
func (m *Mem) PutRequest(r store.Request) {
	m.mu.Lock()
	m.d.requests[r.ID] = cloneRequest(r)
	m.mu.Unlock()
}

// Tx runs fn; every change fn makes is undone when it returns an error.
func (m *Mem) Tx(_ context.Context, tenantID string, fn func(repo.Store) error) error {
	m.txMu.Lock()
	defer m.txMu.Unlock()
	unlock, err := m.lock("Tx")
	if err != nil {
		unlock()
		return err
	}
	if tenantID == "" {
		unlock()
		return repo.ErrNotFound
	}
	snap := m.d.clone()
	unlock()
	if err := fn(m); err != nil {
		m.mu.Lock()
		m.d = snap
		m.mu.Unlock()
		return err
	}
	return nil
}

func key(a, b string) string { return a + "/" + b }

// ----------------------------------------------------------------- tenants

// EnsureTenant implements repo.Store.
func (m *Mem) EnsureTenant(_ context.Context, tenantID string) error {
	unlock, err := m.lock("EnsureTenant")
	defer unlock()
	if err != nil {
		return err
	}
	if _, ok := m.d.tenants[tenantID]; !ok {
		m.d.tenants[tenantID] = store.TenantState{TenantID: tenantID, CursorAt: time.Now().UTC()}
	}
	return nil
}

// TenantsSystem implements repo.Store.
func (m *Mem) TenantsSystem(context.Context) ([]store.TenantState, error) {
	unlock, err := m.lock("TenantsSystem")
	defer unlock()
	if err != nil {
		return nil, err
	}
	out := slices.Collect(maps.Values(m.d.tenants))
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID < out[j].TenantID })
	return out, nil
}

// SetCursor implements repo.Store.
func (m *Mem) SetCursor(_ context.Context, tenantID, cursor string, at time.Time) error {
	unlock, err := m.lock("SetCursor")
	defer unlock()
	if err != nil {
		return err
	}
	t, ok := m.d.tenants[tenantID]
	if !ok {
		return repo.ErrNotFound
	}
	t.StreamCursor, t.CursorAt = cursor, at
	m.d.tenants[tenantID] = t
	return nil
}

// SetMembersSynced implements repo.Store.
func (m *Mem) SetMembersSynced(_ context.Context, tenantID string, at time.Time) error {
	unlock, err := m.lock("SetMembersSynced")
	defer unlock()
	if err != nil {
		return err
	}
	t, ok := m.d.tenants[tenantID]
	if !ok {
		return repo.ErrNotFound
	}
	t.MembersSyncedAt = &at
	m.d.tenants[tenantID] = t
	return nil
}

// ----------------------------------------------------------- absence types

func (m *Mem) typeNameTaken(t store.AbsenceType) bool {
	for _, o := range m.d.types {
		if o.TenantID == t.TenantID && o.ID != t.ID && strings.EqualFold(o.Name, t.Name) {
			return true
		}
	}
	return false
}

func (m *Mem) poolOK(tenantID, poolID string) bool {
	if poolID == "" {
		return true
	}
	p, ok := m.d.pools[poolID]
	return ok && p.TenantID == tenantID
}

// CreateAbsenceType implements repo.Store.
func (m *Mem) CreateAbsenceType(_ context.Context, t store.AbsenceType) error {
	unlock, err := m.lock("CreateAbsenceType")
	defer unlock()
	if err != nil {
		return err
	}
	if _, ok := m.d.types[t.ID]; ok || m.typeNameTaken(t) || !m.poolOK(t.TenantID, t.PoolID) {
		return repo.ErrConflict
	}
	m.d.types[t.ID] = cloneType(t)
	return nil
}

// GetAbsenceType implements repo.Store.
func (m *Mem) GetAbsenceType(_ context.Context, tenantID, id string) (store.AbsenceType, error) {
	unlock, err := m.lock("GetAbsenceType")
	defer unlock()
	if err != nil {
		return store.AbsenceType{}, err
	}
	t, ok := m.d.types[id]
	if !ok || t.TenantID != tenantID {
		return store.AbsenceType{}, repo.ErrNotFound
	}
	return cloneType(t), nil
}

// ListAbsenceTypes implements repo.Store (sort order, then name).
func (m *Mem) ListAbsenceTypes(_ context.Context, tenantID string) ([]store.AbsenceType, error) {
	unlock, err := m.lock("ListAbsenceTypes")
	defer unlock()
	if err != nil {
		return nil, err
	}
	var out []store.AbsenceType
	for _, t := range m.d.types {
		if t.TenantID == tenantID {
			out = append(out, cloneType(t))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// UpdateAbsenceType implements repo.Store.
func (m *Mem) UpdateAbsenceType(_ context.Context, t store.AbsenceType) error {
	unlock, err := m.lock("UpdateAbsenceType")
	defer unlock()
	if err != nil {
		return err
	}
	o, ok := m.d.types[t.ID]
	if !ok || o.TenantID != t.TenantID {
		return repo.ErrNotFound
	}
	if m.typeNameTaken(t) || !m.poolOK(t.TenantID, t.PoolID) {
		return repo.ErrConflict
	}
	m.d.types[t.ID] = cloneType(t)
	return nil
}

func (m *Mem) typeInUse(tenantID, id string) bool {
	for _, a := range m.d.allowances {
		if a.TenantID == tenantID && a.AbsenceTypeID == id {
			return true
		}
	}
	for _, r := range m.d.requests {
		if r.TenantID == tenantID && r.AbsenceTypeID == id {
			return true
		}
	}
	return false
}

// AbsenceTypeInUse implements repo.Store.
func (m *Mem) AbsenceTypeInUse(_ context.Context, tenantID, id string) (bool, error) {
	unlock, err := m.lock("AbsenceTypeInUse")
	defer unlock()
	if err != nil {
		return false, err
	}
	return m.typeInUse(tenantID, id), nil
}

// DeleteAbsenceType implements repo.Store.
func (m *Mem) DeleteAbsenceType(_ context.Context, tenantID, id string) error {
	unlock, err := m.lock("DeleteAbsenceType")
	defer unlock()
	if err != nil {
		return err
	}
	t, ok := m.d.types[id]
	if !ok || t.TenantID != tenantID {
		return repo.ErrNotFound
	}
	if m.typeInUse(tenantID, id) {
		return repo.ErrConflict
	}
	delete(m.d.types, id)
	return nil
}

// ------------------------------------------------------------------ pools

func (m *Mem) poolNameTaken(p store.Pool) bool {
	for _, o := range m.d.pools {
		if o.TenantID == p.TenantID && o.ID != p.ID && strings.EqualFold(o.Name, p.Name) {
			return true
		}
	}
	return false
}

// CreatePool implements repo.Store.
func (m *Mem) CreatePool(_ context.Context, p store.Pool) error {
	unlock, err := m.lock("CreatePool")
	defer unlock()
	if err != nil {
		return err
	}
	if _, ok := m.d.pools[p.ID]; ok || m.poolNameTaken(p) {
		return repo.ErrConflict
	}
	m.d.pools[p.ID] = clonePool(p)
	return nil
}

// GetPool implements repo.Store.
func (m *Mem) GetPool(_ context.Context, tenantID, id string) (store.Pool, error) {
	unlock, err := m.lock("GetPool")
	defer unlock()
	if err != nil {
		return store.Pool{}, err
	}
	p, ok := m.d.pools[id]
	if !ok || p.TenantID != tenantID {
		return store.Pool{}, repo.ErrNotFound
	}
	return clonePool(p), nil
}

// ListPools implements repo.Store (by name).
func (m *Mem) ListPools(_ context.Context, tenantID string) ([]store.Pool, error) {
	unlock, err := m.lock("ListPools")
	defer unlock()
	if err != nil {
		return nil, err
	}
	var out []store.Pool
	for _, p := range m.d.pools {
		if p.TenantID == tenantID {
			out = append(out, clonePool(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// UpdatePool implements repo.Store.
func (m *Mem) UpdatePool(_ context.Context, p store.Pool) error {
	unlock, err := m.lock("UpdatePool")
	defer unlock()
	if err != nil {
		return err
	}
	o, ok := m.d.pools[p.ID]
	if !ok || o.TenantID != p.TenantID {
		return repo.ErrNotFound
	}
	if m.poolNameTaken(p) {
		return repo.ErrConflict
	}
	m.d.pools[p.ID] = clonePool(p)
	return nil
}

func (m *Mem) poolInUse(tenantID, id string) bool {
	for _, t := range m.d.types {
		if t.TenantID == tenantID && t.PoolID == id {
			return true
		}
	}
	for _, a := range m.d.allowances {
		if a.TenantID == tenantID && a.PoolID == id {
			return true
		}
	}
	return false
}

// PoolInUse implements repo.Store.
func (m *Mem) PoolInUse(_ context.Context, tenantID, id string) (bool, error) {
	unlock, err := m.lock("PoolInUse")
	defer unlock()
	if err != nil {
		return false, err
	}
	return m.poolInUse(tenantID, id), nil
}

// DeletePool implements repo.Store.
func (m *Mem) DeletePool(_ context.Context, tenantID, id string) error {
	unlock, err := m.lock("DeletePool")
	defer unlock()
	if err != nil {
		return err
	}
	p, ok := m.d.pools[id]
	if !ok || p.TenantID != tenantID {
		return repo.ErrNotFound
	}
	if m.poolInUse(tenantID, id) {
		return repo.ErrConflict
	}
	delete(m.d.pools, id)
	return nil
}

// ------------------------------------------------------------- allowances

func (m *Mem) allowanceTaken(a store.Allowance) bool {
	for _, o := range m.d.allowances {
		if o.TenantID == a.TenantID && o.ID != a.ID && o.UserID == a.UserID && o.Year == a.Year &&
			o.AbsenceTypeID == a.AbsenceTypeID && o.PoolID == a.PoolID {
			return true
		}
	}
	return false
}

func (m *Mem) allowanceRefsOK(a store.Allowance) bool {
	if (a.AbsenceTypeID == "") == (a.PoolID == "") {
		return false
	}
	if a.AbsenceTypeID != "" {
		t, ok := m.d.types[a.AbsenceTypeID]
		return ok && t.TenantID == a.TenantID
	}
	return m.poolOK(a.TenantID, a.PoolID)
}

// CreateAllowance implements repo.Store.
func (m *Mem) CreateAllowance(_ context.Context, a store.Allowance) error {
	unlock, err := m.lock("CreateAllowance")
	defer unlock()
	if err != nil {
		return err
	}
	if _, ok := m.d.allowances[a.ID]; ok || m.allowanceTaken(a) || !m.allowanceRefsOK(a) || a.Used < 0 {
		return repo.ErrConflict
	}
	m.d.allowances[a.ID] = a
	return nil
}

// GetAllowance implements repo.Store.
func (m *Mem) GetAllowance(_ context.Context, tenantID, id string) (store.Allowance, error) {
	unlock, err := m.lock("GetAllowance")
	defer unlock()
	if err != nil {
		return store.Allowance{}, err
	}
	a, ok := m.d.allowances[id]
	if !ok || a.TenantID != tenantID {
		return store.Allowance{}, repo.ErrNotFound
	}
	return a, nil
}

// ListAllowances implements repo.Store (year desc, user, id).
func (m *Mem) ListAllowances(_ context.Context, tenantID string, f repo.AllowanceFilter) ([]store.Allowance, int, error) {
	unlock, err := m.lock("ListAllowances")
	defer unlock()
	if err != nil {
		return nil, 0, err
	}
	var out []store.Allowance
	for _, a := range m.d.allowances {
		if a.TenantID != tenantID || (f.UserID != "" && a.UserID != f.UserID) || (f.UserIDs != nil && !slices.Contains(f.UserIDs, a.UserID)) ||
			(f.Year != 0 && a.Year != f.Year) || (f.AbsenceTypeID != "" && a.AbsenceTypeID != f.AbsenceTypeID) ||
			(f.PoolID != "" && a.PoolID != f.PoolID) {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Year != out[j].Year {
			return out[i].Year > out[j].Year
		}
		if out[i].UserID != out[j].UserID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].ID < out[j].ID
	})
	return paginate(out, f.All, f.Page, f.PageSize)
}

func paginate[T any](all []T, noPaging bool, page, size int) ([]T, int, error) {
	total := len(all)
	if noPaging {
		return all, total, nil
	}
	p, s := store.Page(page, size, 500)
	start := (p - 1) * s
	if start >= total {
		return nil, total, nil
	}
	return all[start:min(start+s, total)], total, nil
}

// FindAllowance implements repo.Store.
func (m *Mem) FindAllowance(_ context.Context, tenantID, userID string, year int, absenceTypeID, poolID string) (store.Allowance, error) {
	unlock, err := m.lock("FindAllowance")
	defer unlock()
	if err != nil {
		return store.Allowance{}, err
	}
	for _, a := range m.d.allowances {
		if a.TenantID == tenantID && a.UserID == userID && a.Year == year && a.AbsenceTypeID == absenceTypeID && a.PoolID == poolID {
			return a, nil
		}
	}
	return store.Allowance{}, repo.ErrNotFound
}

// UpdateAllowance implements repo.Store (used days are kept: they change
// only through AddUsed).
func (m *Mem) UpdateAllowance(_ context.Context, a store.Allowance) error {
	unlock, err := m.lock("UpdateAllowance")
	defer unlock()
	if err != nil {
		return err
	}
	o, ok := m.d.allowances[a.ID]
	if !ok || o.TenantID != a.TenantID {
		return repo.ErrNotFound
	}
	if m.allowanceTaken(a) || !m.allowanceRefsOK(a) {
		return repo.ErrConflict
	}
	a.Used = o.Used
	m.d.allowances[a.ID] = a
	return nil
}

// LockAllowances implements repo.Store.
func (m *Mem) LockAllowances(_ context.Context, tenantID string, ids []string) (map[string]store.Allowance, error) {
	unlock, err := m.lock("LockAllowances")
	defer unlock()
	if err != nil {
		return nil, err
	}
	out := map[string]store.Allowance{}
	for _, id := range ids {
		if a, ok := m.d.allowances[id]; ok && a.TenantID == tenantID {
			out[id] = a
		}
	}
	return out, nil
}

// AddUsed implements repo.Store.
func (m *Mem) AddUsed(_ context.Context, tenantID, id string, delta leavedays.Tenths, at time.Time) error {
	unlock, err := m.lock("AddUsed")
	defer unlock()
	if err != nil {
		return err
	}
	a, ok := m.d.allowances[id]
	if !ok || a.TenantID != tenantID {
		return repo.ErrNotFound
	}
	if a.Used+delta < 0 {
		return repo.ErrConflict
	}
	a.Used += delta
	a.UpdatedAt = at
	m.d.allowances[id] = a
	return nil
}

func (m *Mem) allowanceInUse(id string) bool {
	for _, cs := range m.d.charges {
		for _, c := range cs {
			if c.AllowanceID == id {
				return true
			}
		}
	}
	return false
}

// AllowanceInUse implements repo.Store.
func (m *Mem) AllowanceInUse(_ context.Context, tenantID, id string) (bool, error) {
	unlock, err := m.lock("AllowanceInUse")
	defer unlock()
	if err != nil {
		return false, err
	}
	a, ok := m.d.allowances[id]
	return ok && a.TenantID == tenantID && m.allowanceInUse(id), nil
}

// DeleteAllowance implements repo.Store.
func (m *Mem) DeleteAllowance(_ context.Context, tenantID, id string) error {
	unlock, err := m.lock("DeleteAllowance")
	defer unlock()
	if err != nil {
		return err
	}
	a, ok := m.d.allowances[id]
	if !ok || a.TenantID != tenantID {
		return repo.ErrNotFound
	}
	if m.allowanceInUse(id) {
		return repo.ErrConflict
	}
	delete(m.d.allowances, id)
	return nil
}

// --------------------------------------------------------------- requests

func active(status string) bool { return slices.Contains(store.ActiveStatuses, status) }

func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	return !aEnd.Before(bStart) && !bEnd.Before(aStart)
}

func (m *Mem) overlap(r store.Request) bool {
	if !active(r.Status) {
		return false
	}
	for _, o := range m.d.requests {
		if o.ID != r.ID && o.TenantID == r.TenantID && o.UserID == r.UserID && active(o.Status) && overlaps(o.Start, o.End, r.Start, r.End) {
			return true
		}
	}
	return false
}

// CreateRequest implements repo.Store.
func (m *Mem) CreateRequest(_ context.Context, r store.Request) error {
	unlock, err := m.lock("CreateRequest")
	defer unlock()
	if err != nil {
		return err
	}
	t, ok := m.d.types[r.AbsenceTypeID]
	if _, dup := m.d.requests[r.ID]; dup || !ok || t.TenantID != r.TenantID || r.Days <= 0 || r.End.Before(r.Start) {
		return repo.ErrConflict
	}
	if m.overlap(r) {
		return repo.ErrOverlap
	}
	if r.Version == 0 {
		r.Version = 1
	}
	m.d.requests[r.ID] = cloneRequest(r)
	return nil
}

func (m *Mem) getRequest(method, tenantID, id string) (store.Request, error) {
	unlock, err := m.lock(method)
	defer unlock()
	if err != nil {
		return store.Request{}, err
	}
	r, ok := m.d.requests[id]
	if !ok || r.TenantID != tenantID {
		return store.Request{}, repo.ErrNotFound
	}
	return cloneRequest(r), nil
}

// GetRequest implements repo.Store.
func (m *Mem) GetRequest(_ context.Context, tenantID, id string) (store.Request, error) {
	return m.getRequest("GetRequest", tenantID, id)
}

// LockRequest implements repo.Store.
func (m *Mem) LockRequest(_ context.Context, tenantID, id string) (store.Request, error) {
	return m.getRequest("LockRequest", tenantID, id)
}

// UpdateRequest implements repo.Store.
func (m *Mem) UpdateRequest(_ context.Context, r store.Request) (store.Request, error) {
	unlock, err := m.lock("UpdateRequest")
	defer unlock()
	if err != nil {
		return store.Request{}, err
	}
	o, ok := m.d.requests[r.ID]
	if !ok || o.TenantID != r.TenantID {
		return store.Request{}, repo.ErrNotFound
	}
	if o.Version != r.Version {
		return store.Request{}, repo.ErrConflict
	}
	if r.SubmissionID != "" {
		for _, x := range m.d.requests {
			if x.ID != r.ID && x.SubmissionID == r.SubmissionID {
				return store.Request{}, repo.ErrConflict
			}
		}
	}
	if m.overlap(r) {
		return store.Request{}, repo.ErrOverlap
	}
	r.Version++
	m.d.requests[r.ID] = cloneRequest(r)
	return cloneRequest(r), nil
}

// DeleteRequest implements repo.Store (charges cascade).
func (m *Mem) DeleteRequest(_ context.Context, tenantID, id string) error {
	unlock, err := m.lock("DeleteRequest")
	defer unlock()
	if err != nil {
		return err
	}
	r, ok := m.d.requests[id]
	if !ok || r.TenantID != tenantID {
		return repo.ErrNotFound
	}
	delete(m.d.requests, id)
	delete(m.d.charges, id)
	return nil
}

// ListRequests implements repo.Store (start date desc, id desc).
func (m *Mem) ListRequests(_ context.Context, tenantID string, f repo.RequestFilter) ([]store.Request, int, error) {
	unlock, err := m.lock("ListRequests")
	defer unlock()
	if err != nil {
		return nil, 0, err
	}
	var out []store.Request
	for _, r := range m.d.requests {
		if r.TenantID != tenantID || (f.UserID != "" && r.UserID != f.UserID) || (f.UserIDs != nil && !slices.Contains(f.UserIDs, r.UserID)) ||
			(f.AbsenceTypeID != "" && r.AbsenceTypeID != f.AbsenceTypeID) || (f.Statuses != nil && !slices.Contains(f.Statuses, r.Status)) ||
			(f.From != nil && r.End.Before(*f.From)) || (f.To != nil && r.Start.After(*f.To)) ||
			(f.ApproverID != "" && !slices.Contains(r.ApproverIDs, f.ApproverID)) {
			continue
		}
		out = append(out, cloneRequest(r))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.After(out[j].Start)
		}
		return out[i].ID > out[j].ID
	})
	return paginate(out, f.All, f.Page, f.PageSize)
}

// Overlaps implements repo.Store.
func (m *Mem) Overlaps(_ context.Context, tenantID, userID string, start, end time.Time, excludeID string) (bool, error) {
	unlock, err := m.lock("Overlaps")
	defer unlock()
	if err != nil {
		return false, err
	}
	return m.overlap(store.Request{ID: excludeID, TenantID: tenantID, UserID: userID, Start: start, End: end, Status: store.StatusPending}), nil
}

// RequestBySubmission implements repo.Store.
func (m *Mem) RequestBySubmission(_ context.Context, tenantID, submissionID string) (store.Request, error) {
	unlock, err := m.lock("RequestBySubmission")
	defer unlock()
	if err != nil {
		return store.Request{}, err
	}
	for _, r := range m.d.requests {
		if r.TenantID == tenantID && submissionID != "" && r.SubmissionID == submissionID {
			return cloneRequest(r), nil
		}
	}
	return store.Request{}, repo.ErrNotFound
}

// AwaitingSigning implements repo.Store (oldest first).
func (m *Mem) AwaitingSigning(_ context.Context, tenantID string, before time.Time, limit int) ([]store.Request, error) {
	unlock, err := m.lock("AwaitingSigning")
	defer unlock()
	if err != nil {
		return nil, err
	}
	var out []store.Request
	for _, r := range m.d.requests {
		if r.TenantID == tenantID && r.Status == store.StatusAwaitingSigning && r.SigningStartedAt != nil && r.SigningStartedAt.Before(before) {
			out = append(out, cloneRequest(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SigningStartedAt.Before(*out[j].SigningStartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---------------------------------------------------------------- charges

// AddCharges implements repo.Store.
func (m *Mem) AddCharges(_ context.Context, charges []store.Charge) error {
	unlock, err := m.lock("AddCharges")
	defer unlock()
	if err != nil {
		return err
	}
	for _, c := range charges {
		r, ok := m.d.requests[c.RequestID]
		a, aok := m.d.allowances[c.AllowanceID]
		if !ok || !aok || r.TenantID != c.TenantID || a.TenantID != c.TenantID || c.Days <= 0 {
			return repo.ErrConflict
		}
		for _, o := range m.d.charges[c.RequestID] {
			if o.AllowanceID == c.AllowanceID {
				return repo.ErrConflict
			}
		}
		m.d.charges[c.RequestID] = append(m.d.charges[c.RequestID], c)
	}
	return nil
}

// Charges implements repo.Store.
func (m *Mem) Charges(_ context.Context, tenantID, requestID string) ([]store.Charge, error) {
	unlock, err := m.lock("Charges")
	defer unlock()
	if err != nil {
		return nil, err
	}
	var out []store.Charge
	for _, c := range m.d.charges[requestID] {
		if c.TenantID == tenantID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AllowanceID < out[j].AllowanceID })
	return out, nil
}

// DeleteCharges implements repo.Store.
func (m *Mem) DeleteCharges(_ context.Context, tenantID, requestID string) error {
	unlock, err := m.lock("DeleteCharges")
	defer unlock()
	if err != nil {
		return err
	}
	if r, ok := m.d.requests[requestID]; ok && r.TenantID == tenantID {
		delete(m.d.charges, requestID)
	}
	return nil
}

// ------------------------------------------------------------ departments

func (m *Mem) deptNameTaken(d store.Department) bool {
	for _, o := range m.d.depts {
		if o.TenantID == d.TenantID && o.ID != d.ID && o.ParentID == d.ParentID && strings.EqualFold(o.Name, d.Name) {
			return true
		}
	}
	return false
}

func (m *Mem) parentOK(d store.Department) bool {
	if d.ParentID == "" {
		return true
	}
	p, ok := m.d.depts[d.ParentID]
	return ok && p.TenantID == d.TenantID && d.ParentID != d.ID
}

// CreateDepartment implements repo.Store.
func (m *Mem) CreateDepartment(_ context.Context, d store.Department) error {
	unlock, err := m.lock("CreateDepartment")
	defer unlock()
	if err != nil {
		return err
	}
	if _, ok := m.d.depts[d.ID]; ok || m.deptNameTaken(d) || !m.parentOK(d) {
		return repo.ErrConflict
	}
	m.d.depts[d.ID] = d
	return nil
}

// GetDepartment implements repo.Store.
func (m *Mem) GetDepartment(_ context.Context, tenantID, id string) (store.Department, error) {
	unlock, err := m.lock("GetDepartment")
	defer unlock()
	if err != nil {
		return store.Department{}, err
	}
	d, ok := m.d.depts[id]
	if !ok || d.TenantID != tenantID {
		return store.Department{}, repo.ErrNotFound
	}
	return d, nil
}

// ListDepartments implements repo.Store (by name).
func (m *Mem) ListDepartments(_ context.Context, tenantID string) ([]store.Department, error) {
	unlock, err := m.lock("ListDepartments")
	defer unlock()
	if err != nil {
		return nil, err
	}
	var out []store.Department
	for _, d := range m.d.depts {
		if d.TenantID == tenantID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Name, out[j].Name) {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// UpdateDepartment implements repo.Store.
func (m *Mem) UpdateDepartment(_ context.Context, d store.Department) error {
	unlock, err := m.lock("UpdateDepartment")
	defer unlock()
	if err != nil {
		return err
	}
	o, ok := m.d.depts[d.ID]
	if !ok || o.TenantID != d.TenantID {
		return repo.ErrNotFound
	}
	if m.deptNameTaken(d) || !m.parentOK(d) {
		return repo.ErrConflict
	}
	m.d.depts[d.ID] = d
	return nil
}

func (m *Mem) deptInUse(tenantID, id string) bool {
	for _, d := range m.d.depts {
		if d.TenantID == tenantID && d.ParentID == id {
			return true
		}
	}
	for _, mb := range m.d.members {
		if mb.TenantID == tenantID && mb.DepartmentID == id {
			return true
		}
	}
	return false
}

// DepartmentInUse implements repo.Store.
func (m *Mem) DepartmentInUse(_ context.Context, tenantID, id string) (bool, error) {
	unlock, err := m.lock("DepartmentInUse")
	defer unlock()
	if err != nil {
		return false, err
	}
	return m.deptInUse(tenantID, id), nil
}

// DeleteDepartment implements repo.Store.
func (m *Mem) DeleteDepartment(_ context.Context, tenantID, id string) error {
	unlock, err := m.lock("DeleteDepartment")
	defer unlock()
	if err != nil {
		return err
	}
	d, ok := m.d.depts[id]
	if !ok || d.TenantID != tenantID {
		return repo.ErrNotFound
	}
	if m.deptInUse(tenantID, id) {
		return repo.ErrConflict
	}
	delete(m.d.depts, id)
	return nil
}

// ---------------------------------------------------------------- members

// UpsertMember implements repo.Store.
func (m *Mem) UpsertMember(_ context.Context, mb store.Member) error {
	unlock, err := m.lock("UpsertMember")
	defer unlock()
	if err != nil {
		return err
	}
	if mb.DepartmentID != "" {
		d, ok := m.d.depts[mb.DepartmentID]
		if !ok || d.TenantID != mb.TenantID {
			return repo.ErrConflict
		}
	}
	m.d.members[key(mb.TenantID, mb.UserID)] = mb
	return nil
}

// GetMember implements repo.Store.
func (m *Mem) GetMember(_ context.Context, tenantID, userID string) (store.Member, error) {
	unlock, err := m.lock("GetMember")
	defer unlock()
	if err != nil {
		return store.Member{}, err
	}
	mb, ok := m.d.members[key(tenantID, userID)]
	if !ok {
		return store.Member{}, repo.ErrNotFound
	}
	return mb, nil
}

// ListMembers implements repo.Store (by user id).
func (m *Mem) ListMembers(_ context.Context, tenantID string) ([]store.Member, error) {
	unlock, err := m.lock("ListMembers")
	defer unlock()
	if err != nil {
		return nil, err
	}
	var out []store.Member
	for _, mb := range m.d.members {
		if mb.TenantID == tenantID {
			out = append(out, mb)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

// --------------------------------------------------------------- holidays

func (m *Mem) holidayTaken(h store.Holiday) bool {
	for _, o := range m.d.holidays {
		if o.TenantID == h.TenantID && o.ID != h.ID && o.Date.Equal(h.Date) {
			return true
		}
	}
	return false
}

// CreateHoliday implements repo.Store.
func (m *Mem) CreateHoliday(_ context.Context, h store.Holiday) error {
	unlock, err := m.lock("CreateHoliday")
	defer unlock()
	if err != nil {
		return err
	}
	h.Date = leavedays.Date(h.Date)
	if _, ok := m.d.holidays[h.ID]; ok || m.holidayTaken(h) {
		return repo.ErrConflict
	}
	m.d.holidays[h.ID] = h
	return nil
}

// GetHoliday implements repo.Store.
func (m *Mem) GetHoliday(_ context.Context, tenantID, id string) (store.Holiday, error) {
	unlock, err := m.lock("GetHoliday")
	defer unlock()
	if err != nil {
		return store.Holiday{}, err
	}
	h, ok := m.d.holidays[id]
	if !ok || h.TenantID != tenantID {
		return store.Holiday{}, repo.ErrNotFound
	}
	return h, nil
}

// ListHolidays implements repo.Store (by date).
func (m *Mem) ListHolidays(_ context.Context, tenantID string) ([]store.Holiday, error) {
	unlock, err := m.lock("ListHolidays")
	defer unlock()
	if err != nil {
		return nil, err
	}
	var out []store.Holiday
	for _, h := range m.d.holidays {
		if h.TenantID == tenantID {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// UpdateHoliday implements repo.Store.
func (m *Mem) UpdateHoliday(_ context.Context, h store.Holiday) error {
	unlock, err := m.lock("UpdateHoliday")
	defer unlock()
	if err != nil {
		return err
	}
	o, ok := m.d.holidays[h.ID]
	if !ok || o.TenantID != h.TenantID {
		return repo.ErrNotFound
	}
	h.Date = leavedays.Date(h.Date)
	if m.holidayTaken(h) {
		return repo.ErrConflict
	}
	m.d.holidays[h.ID] = h
	return nil
}

// DeleteHoliday implements repo.Store.
func (m *Mem) DeleteHoliday(_ context.Context, tenantID, id string) error {
	unlock, err := m.lock("DeleteHoliday")
	defer unlock()
	if err != nil {
		return err
	}
	h, ok := m.d.holidays[id]
	if !ok || h.TenantID != tenantID {
		return repo.ErrNotFound
	}
	delete(m.d.holidays, id)
	return nil
}

// ------------------------------------------------------ outcomes and runs

// RecordOutcome implements repo.Store.
func (m *Mem) RecordOutcome(_ context.Context, o store.SigningOutcome) (bool, error) {
	unlock, err := m.lock("RecordOutcome")
	defer unlock()
	if err != nil {
		return false, err
	}
	k := key(o.TenantID, o.SubmissionID)
	if _, ok := m.d.outcomes[k]; ok {
		return false, nil
	}
	m.d.outcomes[k] = o
	return true, nil
}

// CreateCarryOverRun implements repo.Store.
func (m *Mem) CreateCarryOverRun(_ context.Context, r store.CarryOverRun) error {
	unlock, err := m.lock("CreateCarryOverRun")
	defer unlock()
	if err != nil {
		return err
	}
	m.d.runs = append(m.d.runs, r)
	return nil
}

// ------------------------------------------------------------------- mail

// EnqueueMail implements repo.Store.
func (m *Mem) EnqueueMail(_ context.Context, ml store.Mail) error {
	unlock, err := m.lock("EnqueueMail")
	defer unlock()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(ml.Key, "hr.") {
		return repo.ErrConflict
	}
	ml.Vars = maps.Clone(ml.Vars)
	m.d.mail[ml.ID] = ml
	return nil
}

// DueMailSystem implements repo.Store (oldest due first).
func (m *Mem) DueMailSystem(_ context.Context, now time.Time, limit int) ([]store.Mail, error) {
	unlock, err := m.lock("DueMailSystem")
	defer unlock()
	if err != nil {
		return nil, err
	}
	var out []store.Mail
	for _, ml := range m.d.mail {
		if !ml.NextAt.After(now) {
			ml.Vars = maps.Clone(ml.Vars)
			out = append(out, ml)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].NextAt.Equal(out[j].NextAt) {
			return out[i].NextAt.Before(out[j].NextAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// DeleteMailSystem implements repo.Store.
func (m *Mem) DeleteMailSystem(_ context.Context, id string) error {
	unlock, err := m.lock("DeleteMailSystem")
	defer unlock()
	if err != nil {
		return err
	}
	delete(m.d.mail, id)
	return nil
}

// RetryMailSystem implements repo.Store.
func (m *Mem) RetryMailSystem(_ context.Context, id string, attempts int, next time.Time, lastError string) error {
	unlock, err := m.lock("RetryMailSystem")
	defer unlock()
	if err != nil {
		return err
	}
	ml, ok := m.d.mail[id]
	if !ok {
		return repo.ErrNotFound
	}
	ml.Attempts, ml.NextAt, ml.LastError = attempts, next, lastError
	m.d.mail[id] = ml
	return nil
}

// ------------------------------------------------------------------ audit

// AppendAudit implements repo.Store.
func (m *Mem) AppendAudit(_ context.Context, row store.AuditRow) error {
	unlock, err := m.lock("AppendAudit")
	defer unlock()
	if err != nil {
		return err
	}
	m.d.audit = append(m.d.audit, row)
	return nil
}
