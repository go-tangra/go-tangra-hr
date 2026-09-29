// Package store holds the hr module's persisted records (data-model.md), the
// pgx pool with tenant-scoped transactions and the embedded migrations.
package store

import (
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
)

// NilTenant is pinned under the system scope so the RLS cast stays valid; it
// also marks platform-level audit rows.
const NilTenant = "00000000-0000-0000-0000-000000000000"

// Request statuses (FR-016).
const (
	StatusPending         = "pending"
	StatusAwaitingSigning = "awaiting_signing"
	StatusApproved        = "approved"
	StatusRejected        = "rejected"
	StatusCancelled       = "cancelled"
	StatusRevoked         = "revoked"
)

// ActiveStatuses occupy the calendar and block overlaps.
var ActiveStatuses = []string{StatusPending, StatusAwaitingSigning, StatusApproved}

// Signing outcomes and their sources (hr_signing_outcomes).
const (
	OutcomeCompleted = "completed"
	OutcomeDeclined  = "declined"
	OutcomeCancelled = "cancelled"
	OutcomeExpired   = "expired"

	SourceEvent     = "event"
	SourceReconcile = "reconcile"

	ResultApplied        = "applied"
	ResultIgnoredStatus  = "ignored_status"
	ResultIgnoredUnknown = "ignored_unknown"
)

// Audit is who created and last changed a record.
type Audit struct {
	CreatedAt time.Time
	CreatedBy string
	UpdatedAt time.Time
	UpdatedBy string
}

// SigningSettings are an absence type's signing template settings (FR-030).
type SigningSettings struct {
	TemplateID    string            `json:"template_id"`
	TemplateName  string            `json:"template_name"`
	EmployeeParty string            `json:"employee_party"`
	ApproverParty string            `json:"approver_party"`
	Fields        map[string]string `json:"fields"` // template field id → leave value key
}

// AbsenceType is a kind of absence.
type AbsenceType struct {
	ID               string
	TenantID         string
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
	Signing          *SigningSettings
	Audit
}

// Pool is a shared allowance of several absence types.
type Pool struct {
	ID           string
	TenantID     string
	Name         string
	Description  string
	Color        string
	Icon         string
	CarryOverCap *leavedays.Tenths
	Audit
}

// Allowance is a person's yearly balance for one type or one pool.
type Allowance struct {
	ID             string
	TenantID       string
	UserID         string
	Year           int
	AbsenceTypeID  string
	PoolID         string
	Total          leavedays.Tenths
	Carried        leavedays.Tenths
	Used           leavedays.Tenths
	CarriedFromRun string
	Notes          string
	Audit
}

// Remaining is total + carried − used.
func (a Allowance) Remaining() leavedays.Tenths { return a.Total + a.Carried - a.Used }

// Request is a leave request.
type Request struct {
	ID               string
	TenantID         string
	UserID           string
	AbsenceTypeID    string
	Start            time.Time
	End              time.Time
	HalfStart        bool
	HalfEnd          bool
	Days             leavedays.Tenths
	Status           string
	Reason           string
	Notes            string
	ApproverIDs      []string
	ReviewedBy       string
	ReviewedAt       *time.Time
	ReviewNotes      string
	SubmissionID     string
	SigningNote      string
	SigningStartedAt *time.Time
	SigningAttempt   int
	Version          int
	CreatedAt        time.Time
	CreatedBy        string
	UpdatedAt        time.Time
}

// Charge is one allowance debit of a request.
type Charge struct {
	RequestID   string
	TenantID    string
	AllowanceID string
	Year        int
	Days        leavedays.Tenths
}

// Department is one node of the tenant's department tree.
type Department struct {
	ID        string
	TenantID  string
	ParentID  string
	Name      string
	ManagerID string
	Audit
}

// Member is HR's record of a tenant member.
type Member struct {
	TenantID     string
	UserID       string
	DepartmentID string
	DisplayName  string
	Active       bool
	SyncedAt     time.Time
}

// Holiday is a public holiday.
type Holiday struct {
	ID        string
	TenantID  string
	Date      time.Time
	Name      string
	Recurring bool
	CreatedAt time.Time
	CreatedBy string
}

// SigningOutcome records one received signing outcome (at most once).
type SigningOutcome struct {
	TenantID     string
	SubmissionID string
	Outcome      string
	Source       string
	EventID      string
	RequestID    string
	Result       string
	ReceivedAt   time.Time
}

// TenantState is a tenant known to HR with its stream cursor.
type TenantState struct {
	TenantID        string
	StreamCursor    string
	CursorAt        time.Time
	MembersSyncedAt *time.Time
}

// CarryOverRun records one carry-over run.
type CarryOverRun struct {
	ID         string
	TenantID   string
	SourceYear int
	CreatedAt  time.Time
	CreatedBy  string
	Created    int
	Updated    int
}

// Mail is one queued e-mail (outbox).
type Mail struct {
	ID        string
	TenantID  string
	Key       string
	UserID    string
	Vars      map[string]string
	Attempts  int
	NextAt    time.Time
	LastError string
	CreatedAt time.Time
}

// AuditRow is an append-only audit event.
type AuditRow struct {
	ID          string
	TenantID    string
	At          time.Time
	ActorKind   string
	ActorID     string
	Action      string
	SubjectKind string
	SubjectID   string
	Outcome     string
	Reason      string
	Detail      map[string]any
}

// Page normalises a page/page size pair (1-based page, size 1..max).
func Page(page, size, max int) (int, int) {
	if max <= 0 {
		max = 100
	}
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 25
	}
	if size > max {
		size = max
	}
	return page, size
}
