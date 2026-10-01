package requests

import (
	"context"
	"io"
	"slices"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// Views of List.
const (
	ViewMine   = "mine"
	ViewReview = "review"
	ViewAll    = "all"
)

// ListFilter narrows List.
type ListFilter struct {
	View          string
	UserID        string
	DepartmentID  string
	AbsenceTypeID string
	Status        string
	From, To      *time.Time
	List          listquery.Request // page, size and sort (store.RequestList)
}

// Get returns a request's details (SR-004).
func (s *Service) Get(ctx context.Context, subj authz.Subjects, id string) (store.Request, error) {
	return s.load(ctx, subj, id)
}

// List lists requests: "mine" (own), "review" (routed to the caller and
// open), "all" (hr:read: everyone; managers: their departments).
func (s *Service) List(ctx context.Context, subj authz.Subjects, f ListFilter) ([]store.Request, int, error) {
	if err := s.require(ctx, subj, authz.Request); err != nil && !authz.Allowed(ctx, s.d.Checker, subj, authz.Read) {
		return nil, 0, err
	}
	rf := repo.RequestFilter{AbsenceTypeID: f.AbsenceTypeID, From: f.From, To: f.To, List: f.List}
	if f.Status != "" {
		rf.Statuses = []string{f.Status}
	}
	tree, err := s.d.Tree(ctx, subj.TenantID)
	if err != nil {
		return nil, 0, err
	}
	switch f.View {
	case ViewMine, "":
		rf.UserID = subj.UserID
	case ViewReview:
		rf.ApproverID = subj.UserID
		if rf.Statuses == nil {
			rf.Statuses = []string{store.StatusPending, store.StatusAwaitingSigning}
		}
		rf.UserID = f.UserID
	case ViewAll:
		rf.UserID = f.UserID
		if !authz.Allowed(ctx, s.d.Checker, subj, authz.Read) {
			managed := map[string]bool{}
			for _, d := range tree.Managed(subj.UserID) {
				managed[d] = true
			}
			if len(managed) == 0 {
				return nil, 0, apperr.Forbidden
			}
			rf.UserIDs = []string{}
			for u, d := range tree.MemberOf {
				if managed[d] && u != subj.UserID {
					rf.UserIDs = append(rf.UserIDs, u)
				}
			}
			slices.Sort(rf.UserIDs)
		}
	default:
		return nil, 0, apperr.Validation.WithField("view")
	}
	if f.DepartmentID != "" {
		var inDept []string
		for u, d := range tree.MemberOf {
			if d == f.DepartmentID && (rf.UserIDs == nil || slices.Contains(rf.UserIDs, u)) {
				inDept = append(inDept, u)
			}
		}
		slices.Sort(inDept)
		rf.UserIDs = append([]string{}, inDept...)
	}
	return s.d.Store.ListRequests(ctx, subj.TenantID, rf)
}

// CalendarPerson is one row of the calendar.
type CalendarPerson struct {
	UserID       string
	Name         string
	DepartmentID string
}

// CalendarEvent is one absence bar (no reasons or notes, FR-056).
type CalendarEvent struct {
	ID            string
	UserID        string
	AbsenceTypeID string
	Start, End    time.Time
	HalfStart     bool
	HalfEnd       bool
	Days          leavedays.Tenths
	Status        string
}

// CalendarHoliday is a holiday inside the range.
type CalendarHoliday struct {
	Date time.Time
	Name string
}

// CalendarData is the calendar of a range.
type CalendarData struct {
	People   []CalendarPerson
	Events   []CalendarEvent
	Holidays []CalendarHoliday
}

// Calendar returns the team calendar for [from, to] (FR-055, FR-056):
// active members grouped by department, open and approved absences, and
// holidays. people lists the tenant's active members with names.
func (s *Service) Calendar(ctx context.Context, subj authz.Subjects, from, to time.Time, departmentID, userID string,
	people []CalendarPerson) (CalendarData, error) {
	var out CalendarData
	if err := s.require(ctx, subj, authz.Calendar); err != nil {
		return out, err
	}
	from, to = leavedays.Date(from), leavedays.Date(to)
	if to.Before(from) || int(to.Sub(from).Hours()/24)+1 > s.d.Limits.MaxCalendarDays {
		return out, apperr.Validation.WithField("to")
	}
	tree, err := s.d.Tree(ctx, subj.TenantID)
	if err != nil {
		return out, err
	}
	visible := map[string]bool{}
	for _, p := range people {
		p.DepartmentID = tree.MemberOf[p.UserID]
		if (departmentID != "" && p.DepartmentID != departmentID) || (userID != "" && p.UserID != userID) || !tree.Active(p.UserID) {
			continue
		}
		visible[p.UserID] = true
		out.People = append(out.People, p)
	}
	reqs, _, err := s.d.Store.ListRequests(ctx, subj.TenantID, repo.RequestFilter{From: &from, To: &to, Statuses: store.ActiveStatuses, All: true})
	if err != nil {
		return out, err
	}
	for _, r := range reqs {
		if !visible[r.UserID] {
			continue
		}
		out.Events = append(out.Events, CalendarEvent{ID: r.ID, UserID: r.UserID, AbsenceTypeID: r.AbsenceTypeID, Start: r.Start, End: r.End,
			HalfStart: r.HalfStart, HalfEnd: r.HalfEnd, Days: r.Days, Status: r.Status})
	}
	hs, err := s.d.Store.ListHolidays(ctx, subj.TenantID)
	if err != nil {
		return out, err
	}
	cal := map[time.Time]string{}
	for _, h := range hs {
		for y := from.Year(); y <= to.Year(); y++ {
			var d time.Time
			switch {
			case !h.Recurring && h.Date.Year() == y:
				d = h.Date
			case h.Recurring && y >= h.Date.Year():
				d = time.Date(y, h.Date.Month(), h.Date.Day(), 0, 0, 0, 0, time.UTC)
				if d.Month() != h.Date.Month() {
					continue
				}
			default:
				continue
			}
			if !d.Before(from) && !d.After(to) {
				cal[d] = h.Name
			}
		}
	}
	for d, n := range cal {
		out.Holidays = append(out.Holidays, CalendarHoliday{Date: d, Name: n})
	}
	slices.SortFunc(out.Holidays, func(a, b CalendarHoliday) int { return a.Date.Compare(b.Date) })
	return out, nil
}

// SignedDocument streams the signed PDF of a request to its owner, reviewer,
// approvers, managers and HR administrators (FR-038).
func (s *Service) SignedDocument(ctx context.Context, subj authz.Subjects, id string) (io.ReadCloser, string, error) {
	r, err := s.d.Store.GetRequest(ctx, subj.TenantID, id)
	if err != nil {
		return nil, "", mapErr(err)
	}
	f, _, err := s.facts(ctx, r)
	if err != nil {
		return nil, "", err
	}
	if !authz.CanDownload(ctx, s.d.Checker, subj, f) {
		return nil, "", apperr.NotFound
	}
	if r.SubmissionID == "" || r.Status != store.StatusApproved && r.Status != store.StatusRevoked || s.d.Signing == nil {
		return nil, "", apperr.NotSigned
	}
	rc, name, err := s.d.Signing.Document(ctx, subj.TenantID, r.SubmissionID)
	if err != nil {
		if _, ok := apperr.As(err); ok {
			return nil, "", err
		}
		return nil, "", apperr.SigningUnavailable
	}
	return rc, name, nil
}
