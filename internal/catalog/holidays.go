package catalog

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
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

// HolidayInput is the editable part of a holiday.
type HolidayInput struct {
	Date      time.Time
	Name      string
	Recurring bool
}

func checkHoliday(in HolidayInput) (HolidayInput, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return in, err
	}
	d := leavedays.Date(in.Date)
	if d.Year() < 2000 || d.Year() > 2099 {
		return in, apperr.Validation.WithField("date")
	}
	return HolidayInput{Date: d, Name: name, Recurring: in.Recurring}, nil
}

// CreateHoliday adds a public holiday.
func (s *Service) CreateHoliday(ctx context.Context, subj authz.Subjects, in HolidayInput) (h store.Holiday, err error) {
	defer func() { s.record(ctx, subj, audit.HolidayCreate, audit.SubjectHoliday, h.ID, err, nil) }()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return h, err
	}
	if in, err = checkHoliday(in); err != nil {
		return h, err
	}
	h = store.Holiday{ID: store.NewID(), TenantID: subj.TenantID, Date: in.Date, Name: in.Name, Recurring: in.Recurring,
		CreatedAt: s.d.Now(), CreatedBy: subj.ActorID()}
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		if err := tx.EnsureTenant(ctx, subj.TenantID); err != nil {
			return err
		}
		return tx.CreateHoliday(ctx, h)
	})
	return h, mapErr(err, apperr.Duplicate)
}

// UpdateHoliday changes a holiday (existing requests keep their day counts, FR-047).
func (s *Service) UpdateHoliday(ctx context.Context, subj authz.Subjects, id string, in HolidayInput) (h store.Holiday, err error) {
	defer func() { s.record(ctx, subj, audit.HolidayUpdate, audit.SubjectHoliday, id, err, nil) }()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return h, err
	}
	if in, err = checkHoliday(in); err != nil {
		return h, err
	}
	if h, err = s.d.Store.GetHoliday(ctx, subj.TenantID, id); err != nil {
		return h, mapErr(err, apperr.Conflict)
	}
	h.Date, h.Name, h.Recurring = in.Date, in.Name, in.Recurring
	return h, mapErr(s.d.Store.UpdateHoliday(ctx, h), apperr.Duplicate)
}

// DeleteHoliday removes a holiday.
func (s *Service) DeleteHoliday(ctx context.Context, subj authz.Subjects, id string) (err error) {
	defer func() { s.record(ctx, subj, audit.HolidayDelete, audit.SubjectHoliday, id, err, nil) }()
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return err
	}
	return mapErr(s.d.Store.DeleteHoliday(ctx, subj.TenantID, id), apperr.Conflict)
}

// ListHolidays lists holidays; year > 0 keeps the ones falling in that year
// (recurring ones shown on that year's date).
func (s *Service) ListHolidays(ctx context.Context, subj authz.Subjects, year int) ([]store.Holiday, error) {
	if err := s.require(ctx, subj, authz.Calendar); err != nil {
		return nil, err
	}
	all, err := s.d.Store.ListHolidays(ctx, subj.TenantID)
	if err != nil || year == 0 {
		return all, err
	}
	return InYear(all, year), nil
}

// InYear returns the holidays falling in year, recurring ones moved to that
// year (a recurring 29 February only in leap years), ordered by date.
func InYear(all []store.Holiday, year int) []store.Holiday {
	var out []store.Holiday
	for _, h := range all {
		switch {
		case !h.Recurring && h.Date.Year() == year:
			out = append(out, h)
		case h.Recurring && year >= h.Date.Year():
			d := time.Date(year, h.Date.Month(), h.Date.Day(), 0, 0, 0, 0, time.UTC)
			if d.Month() != h.Date.Month() {
				continue // 29 February outside a leap year
			}
			h.Date = d
			out = append(out, h)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Date.Before(out[j-1].Date); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Calendar builds the tenant's working-day calendar (no permission check:
// services call it for their own day counts).
func (s *Service) Calendar(ctx context.Context, tenantID string) (leavedays.Calendar, error) {
	hs, err := s.d.Store.ListHolidays(ctx, tenantID)
	if err != nil {
		return leavedays.Calendar{}, err
	}
	out := make([]leavedays.Holiday, len(hs))
	for i, h := range hs {
		out[i] = leavedays.Holiday{Date: h.Date, Recurring: h.Recurring}
	}
	return leavedays.NewCalendar(out), nil
}

// LineError is a refused import line.
type LineError struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"` // date | name | format | duplicate_in_file
}

// ImportResult summarises a holiday import.
type ImportResult struct {
	Created int         `json:"created"`
	Skipped int         `json:"skipped"` // date already present
	Errors  []LineError `json:"errors"`
	DryRun  bool        `json:"dry_run"`
}

// ParseImport reads "YYYY-MM-DD,name[,yearly]" lines (blank lines and lines
// starting with # are skipped; the third field accepts yearly, recurring,
// true, 1). It never fails as a whole: bad lines are reported.
func ParseImport(data []byte, maxLines int) ([]HolidayInput, []LineError) {
	var out []HolidayInput
	var errs []LineError
	seen := map[time.Time]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 4096), 4096)
	n, lines := 0, 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines++
		if lines > maxLines {
			errs = append(errs, LineError{Line: n, Reason: "too_many_lines"})
			break
		}
		if !utf8.ValidString(line) {
			errs = append(errs, LineError{Line: n, Reason: "format"})
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 || len(parts) > 3 {
			errs = append(errs, LineError{Line: n, Reason: "format"})
			continue
		}
		d, err := time.Parse("2006-01-02", strings.TrimSpace(parts[0]))
		if err != nil {
			errs = append(errs, LineError{Line: n, Reason: "date"})
			continue
		}
		rec := false
		if len(parts) == 3 {
			switch strings.ToLower(strings.TrimSpace(parts[2])) {
			case "yearly", "recurring", "true", "1":
				rec = true
			case "", "once", "false", "0":
			default:
				errs = append(errs, LineError{Line: n, Reason: "format"})
				continue
			}
		}
		in, err := checkHoliday(HolidayInput{Date: d, Name: parts[1], Recurring: rec})
		if err != nil {
			reason := "name"
			if e, _ := apperr.As(err); e.Field == "date" {
				reason = "date"
			}
			errs = append(errs, LineError{Line: n, Reason: reason})
			continue
		}
		if seen[in.Date] {
			errs = append(errs, LineError{Line: n, Reason: "duplicate_in_file"})
			continue
		}
		seen[in.Date] = true
		out = append(out, in)
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, LineError{Line: n + 1, Reason: "format"})
	}
	return out, errs
}

// ImportHolidays imports a holiday file. Nothing is written when any line is
// refused or dryRun is set; dates already present are skipped.
func (s *Service) ImportHolidays(ctx context.Context, subj authz.Subjects, data []byte, dryRun bool) (res ImportResult, err error) {
	defer func() {
		s.record(ctx, subj, audit.HolidayImport, audit.SubjectTenant, subj.TenantID, err,
			map[string]any{"created": res.Created, "skipped": res.Skipped, "errors": len(res.Errors), "dry_run": dryRun})
	}()
	res.DryRun = dryRun
	if err = s.require(ctx, subj, authz.Manage); err != nil {
		return res, err
	}
	if int64(len(data)) > s.d.Limits.MaxImportBytes {
		return res, apperr.PayloadTooLarge
	}
	items, lineErrs := ParseImport(data, s.d.Limits.MaxImportLines)
	res.Errors = lineErrs
	existing, err := s.d.Store.ListHolidays(ctx, subj.TenantID)
	if err != nil {
		return res, err
	}
	have := map[time.Time]bool{}
	for _, h := range existing {
		have[h.Date] = true
	}
	var todo []store.Holiday
	now := s.d.Now()
	for _, in := range items {
		if have[in.Date] {
			res.Skipped++
			continue
		}
		todo = append(todo, store.Holiday{ID: store.NewID(), TenantID: subj.TenantID, Date: in.Date, Name: in.Name, Recurring: in.Recurring,
			CreatedAt: now, CreatedBy: subj.ActorID()})
	}
	res.Created = len(todo)
	if len(lineErrs) > 0 {
		res.Created = 0
		if !dryRun {
			return res, apperr.Validation.WithField("file").WithDetail(map[string]any{"errors": len(lineErrs)})
		}
		return res, nil
	}
	if dryRun || len(todo) == 0 {
		return res, nil
	}
	err = s.d.Store.Tx(ctx, subj.TenantID, func(tx repo.Store) error {
		if err := tx.EnsureTenant(ctx, subj.TenantID); err != nil {
			return err
		}
		for _, h := range todo {
			if err := tx.CreateHoliday(ctx, h); err != nil {
				return fmt.Errorf("holiday %s: %w", h.Date.Format("2006-01-02"), err)
			}
		}
		return nil
	})
	if err != nil {
		res.Created = 0
	}
	return res, mapErr(err, apperr.Duplicate)
}
