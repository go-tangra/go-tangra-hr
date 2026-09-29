// Package repo is the hr module's storage contract. Two implementations
// satisfy it: repodb (pgx over TimescaleDB with row-level security) and
// memstore (the in-memory fake the service tests run against). The shared
// behaviour is pinned by repotest.
//
// Every tenant method takes the tenant explicitly and runs under that
// tenant's RLS scope; methods named *System run under the system scope (the
// event consumer, scheduled task types, the mail outbox worker). Tx runs fn
// against a transaction-bound Store: everything fn does commits or rolls back
// together, and Lock* methods inside it hold the row locks until the end.
package repo

import (
	"context"
	"errors"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// Sentinel errors.
var (
	ErrNotFound = errors.New("repo: not found")
	ErrConflict = errors.New("repo: conflict") // uniqueness, version or in-use violation
	ErrOverlap  = errors.New("repo: overlapping request")
)

// AllowanceFilter narrows ListAllowances.
type AllowanceFilter struct {
	UserID        string
	UserIDs       []string // nil = anyone
	Year          int      // 0 = any
	AbsenceTypeID string
	PoolID        string
	All           bool // no paging
	Page          int
	PageSize      int
}

// RequestFilter narrows ListRequests.
type RequestFilter struct {
	UserID        string
	UserIDs       []string // nil = anyone (department or managed filter)
	AbsenceTypeID string
	Statuses      []string // nil = any
	From, To      *time.Time
	ApproverID    string // routed to this user
	All           bool   // no paging
	Page          int
	PageSize      int
}

// Store is the complete storage contract.
type Store interface {
	// Tx runs fn in one transaction scoped to tenantID.
	Tx(ctx context.Context, tenantID string, fn func(Store) error) error

	// Tenants (research D4).
	EnsureTenant(ctx context.Context, tenantID string) error
	TenantsSystem(ctx context.Context) ([]store.TenantState, error)
	SetCursor(ctx context.Context, tenantID, cursor string, at time.Time) error
	SetMembersSynced(ctx context.Context, tenantID string, at time.Time) error

	// Absence types.
	CreateAbsenceType(ctx context.Context, t store.AbsenceType) error
	GetAbsenceType(ctx context.Context, tenantID, id string) (store.AbsenceType, error)
	ListAbsenceTypes(ctx context.Context, tenantID string) ([]store.AbsenceType, error)
	UpdateAbsenceType(ctx context.Context, t store.AbsenceType) error
	AbsenceTypeInUse(ctx context.Context, tenantID, id string) (bool, error)
	DeleteAbsenceType(ctx context.Context, tenantID, id string) error

	// Pools.
	CreatePool(ctx context.Context, p store.Pool) error
	GetPool(ctx context.Context, tenantID, id string) (store.Pool, error)
	ListPools(ctx context.Context, tenantID string) ([]store.Pool, error)
	UpdatePool(ctx context.Context, p store.Pool) error
	PoolInUse(ctx context.Context, tenantID, id string) (bool, error)
	DeletePool(ctx context.Context, tenantID, id string) error

	// Allowances.
	CreateAllowance(ctx context.Context, a store.Allowance) error
	GetAllowance(ctx context.Context, tenantID, id string) (store.Allowance, error)
	ListAllowances(ctx context.Context, tenantID string, f AllowanceFilter) ([]store.Allowance, int, error)
	// FindAllowance is the allowance of a person, year and type or pool.
	FindAllowance(ctx context.Context, tenantID, userID string, year int, absenceTypeID, poolID string) (store.Allowance, error)
	UpdateAllowance(ctx context.Context, a store.Allowance) error
	// LockAllowances reads ids with row locks, in id order (Tx only).
	LockAllowances(ctx context.Context, tenantID string, ids []string) (map[string]store.Allowance, error)
	// AddUsed adds delta to used days (the CHECK keeps used ≥ 0).
	AddUsed(ctx context.Context, tenantID, id string, delta leavedays.Tenths, at time.Time) error
	AllowanceInUse(ctx context.Context, tenantID, id string) (bool, error)
	DeleteAllowance(ctx context.Context, tenantID, id string) error

	// Requests.
	// CreateRequest returns ErrOverlap when an active request of the person
	// overlaps (exclusion constraint).
	CreateRequest(ctx context.Context, r store.Request) error
	GetRequest(ctx context.Context, tenantID, id string) (store.Request, error)
	// LockRequest is GetRequest holding the row lock until the end of Tx.
	LockRequest(ctx context.Context, tenantID, id string) (store.Request, error)
	// UpdateRequest stores r when the stored version equals r.Version (else
	// ErrConflict), bumps the version and returns the stored request.
	UpdateRequest(ctx context.Context, r store.Request) (store.Request, error)
	DeleteRequest(ctx context.Context, tenantID, id string) error
	ListRequests(ctx context.Context, tenantID string, f RequestFilter) ([]store.Request, int, error)
	// Overlaps reports an active request of userID overlapping [start, end].
	Overlaps(ctx context.Context, tenantID, userID string, start, end time.Time, excludeID string) (bool, error)
	RequestBySubmission(ctx context.Context, tenantID, submissionID string) (store.Request, error)
	// AwaitingSigning lists requests awaiting signing since before cutoff.
	AwaitingSigning(ctx context.Context, tenantID string, before time.Time, limit int) ([]store.Request, error)

	// Charges.
	AddCharges(ctx context.Context, charges []store.Charge) error
	Charges(ctx context.Context, tenantID, requestID string) ([]store.Charge, error)
	DeleteCharges(ctx context.Context, tenantID, requestID string) error

	// Departments.
	CreateDepartment(ctx context.Context, d store.Department) error
	GetDepartment(ctx context.Context, tenantID, id string) (store.Department, error)
	ListDepartments(ctx context.Context, tenantID string) ([]store.Department, error)
	UpdateDepartment(ctx context.Context, d store.Department) error
	// DepartmentInUse reports members or sub-departments.
	DepartmentInUse(ctx context.Context, tenantID, id string) (bool, error)
	DeleteDepartment(ctx context.Context, tenantID, id string) error

	// Members.
	UpsertMember(ctx context.Context, m store.Member) error
	GetMember(ctx context.Context, tenantID, userID string) (store.Member, error)
	ListMembers(ctx context.Context, tenantID string) ([]store.Member, error)

	// Holidays.
	CreateHoliday(ctx context.Context, h store.Holiday) error
	GetHoliday(ctx context.Context, tenantID, id string) (store.Holiday, error)
	ListHolidays(ctx context.Context, tenantID string) ([]store.Holiday, error)
	UpdateHoliday(ctx context.Context, h store.Holiday) error
	DeleteHoliday(ctx context.Context, tenantID, id string) error

	// Signing outcomes: RecordOutcome inserts once; a repeat returns false.
	RecordOutcome(ctx context.Context, o store.SigningOutcome) (bool, error)

	// Carry-over runs.
	CreateCarryOverRun(ctx context.Context, r store.CarryOverRun) error

	// Mail outbox.
	EnqueueMail(ctx context.Context, m store.Mail) error
	DueMailSystem(ctx context.Context, now time.Time, limit int) ([]store.Mail, error)
	DeleteMailSystem(ctx context.Context, id string) error
	RetryMailSystem(ctx context.Context, id string, attempts int, next time.Time, lastError string) error

	// Audit.
	AppendAudit(ctx context.Context, row store.AuditRow) error
}
