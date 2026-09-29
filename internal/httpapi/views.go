package httpapi

import (
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/allowances"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

func days(t leavedays.Tenths) float64 { return t.Float() }

func capView(c *leavedays.Tenths) *float64 {
	if c == nil {
		return nil
	}
	v := c.Float()
	return &v
}

func dateString(t time.Time) string { return t.Format("2006-01-02") }

type absenceTypeView struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	Color            string                 `json:"color"`
	Icon             string                 `json:"icon"`
	SortOrder        int                    `json:"sort_order"`
	Active           bool                   `json:"active"`
	Metadata         map[string]any         `json:"metadata"`
	Deducts          bool                   `json:"deducts"`
	RequiresApproval bool                   `json:"requires_approval"`
	PoolID           string                 `json:"pool_id"`
	CarryOverCap     *float64               `json:"carry_over_cap"`
	RequiresSigning  bool                   `json:"requires_signing"`
	Signing          *store.SigningSettings `json:"signing"`
}

func viewType(t store.AbsenceType) absenceTypeView {
	meta := t.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	return absenceTypeView{ID: t.ID, Name: t.Name, Description: t.Description, Color: t.Color, Icon: t.Icon, SortOrder: t.SortOrder,
		Active: t.Active, Metadata: meta, Deducts: t.Deducts, RequiresApproval: t.RequiresApproval, PoolID: t.PoolID,
		CarryOverCap: capView(t.CarryOverCap), RequiresSigning: t.RequiresSigning, Signing: t.Signing}
}

type poolView struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Color        string   `json:"color"`
	Icon         string   `json:"icon"`
	CarryOverCap *float64 `json:"carry_over_cap"`
}

func viewPool(p store.Pool) poolView {
	return poolView{ID: p.ID, Name: p.Name, Description: p.Description, Color: p.Color, Icon: p.Icon, CarryOverCap: capView(p.CarryOverCap)}
}

type allowanceView struct {
	ID            string  `json:"id"`
	UserID        string  `json:"user_id"`
	Year          int     `json:"year"`
	AbsenceTypeID string  `json:"absence_type_id"`
	PoolID        string  `json:"pool_id"`
	Total         float64 `json:"total_days"`
	Carried       float64 `json:"carried_over"`
	Used          float64 `json:"used_days"`
	Remaining     float64 `json:"remaining"`
	Notes         string  `json:"notes"`
}

func viewAllowance(a store.Allowance) allowanceView {
	return allowanceView{ID: a.ID, UserID: a.UserID, Year: a.Year, AbsenceTypeID: a.AbsenceTypeID, PoolID: a.PoolID, Total: days(a.Total),
		Carried: days(a.Carried), Used: days(a.Used), Remaining: days(a.Remaining()), Notes: a.Notes}
}

type balanceLineView struct {
	Kind          string   `json:"kind"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Color         string   `json:"color"`
	AllowanceID   string   `json:"allowance_id"`
	Total         float64  `json:"total"`
	Carried       float64  `json:"carried"`
	Used          float64  `json:"used"`
	Pending       float64  `json:"pending"`
	Remaining     float64  `json:"remaining"`
	MemberTypeIDs []string `json:"member_type_ids"`
}

func viewLine(l allowances.Line) balanceLineView {
	ids := l.MemberTypeIDs
	if ids == nil {
		ids = []string{}
	}
	return balanceLineView{Kind: l.Kind, ID: l.ID, Name: l.Name, Color: l.Color, AllowanceID: l.AllowanceID, Total: days(l.Total),
		Carried: days(l.Carried), Used: days(l.Used), Pending: days(l.Pending), Remaining: days(l.Remaining), MemberTypeIDs: ids}
}

type requestView struct {
	ID            string     `json:"id"`
	UserID        string     `json:"user_id"`
	UserName      string     `json:"user_name"`
	AbsenceTypeID string     `json:"absence_type_id"`
	StartDate     string     `json:"start_date"`
	EndDate       string     `json:"end_date"`
	HalfStart     bool       `json:"half_start"`
	HalfEnd       bool       `json:"half_end"`
	Days          float64    `json:"days"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason"`
	Notes         string     `json:"notes"`
	ApproverIDs   []string   `json:"approver_ids"`
	ApproverNames []string   `json:"approver_names"`
	ReviewedBy    string     `json:"reviewed_by"`
	ReviewerName  string     `json:"reviewer_name"`
	ReviewedAt    *time.Time `json:"reviewed_at"`
	ReviewNotes   string     `json:"review_notes"`
	Signing       bool       `json:"signing"`
	SigningNote   string     `json:"signing_note"`
	CreatedAt     time.Time  `json:"created_at"`
	CanReview     bool       `json:"can_review"`
	CanEdit       bool       `json:"can_edit"`
	CanCancel     bool       `json:"can_cancel"`
}

type holidayView struct {
	ID        string `json:"id"`
	Date      string `json:"date"`
	Name      string `json:"name"`
	Recurring bool   `json:"recurring"`
}

func viewHoliday(h store.Holiday) holidayView {
	return holidayView{ID: h.ID, Date: dateString(h.Date), Name: h.Name, Recurring: h.Recurring}
}

type departmentView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	ParentID    string   `json:"parent_id"`
	ManagerID   string   `json:"manager_id"`
	ManagerName string   `json:"manager_name"`
	MemberIDs   []string `json:"member_ids"`
}

type personView struct {
	UserID       string `json:"user_id"`
	Name         string `json:"name"`
	DepartmentID string `json:"department_id"`
}
