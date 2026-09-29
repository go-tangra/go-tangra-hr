// Package signingmap validates an absence type's signing settings against a
// signing template and renders the leave values prefilled into the template
// (spec FR-030, research D9). It replaces v3's hard-coded field names
// ("Name2", "TotalDays", …): administrators map template fields to a closed
// set of leave values. The package is pure.
package signingmap

import (
	"errors"
	"fmt"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
)

// Leave value keys (closed set).
const (
	EmployeeName = "employee_name"
	Department   = "department"
	AbsenceType  = "absence_type"
	StartDate    = "start_date"
	EndDate      = "end_date"
	Days         = "days"
	Reason       = "reason"
	ApproverName = "approver_name"
	Today        = "today"
)

// Keys lists every leave value key.
var Keys = []string{EmployeeName, Department, AbsenceType, StartDate, EndDate, Days, Reason, ApproverName, Today}

// MaxFields bounds a mapping.
const MaxFields = 50

// Errors of Validate (wrapped with the offending party or field).
var (
	ErrParty = errors.New("signingmap: invalid party")
	ErrField = errors.New("signingmap: invalid field")
	ErrValue = errors.New("signingmap: unknown leave value")
)

// Party is a template party.
type Party struct{ ID, Name string }

// Field is a template field.
type Field struct {
	ID, Name, Type, Party string
	TextValued            bool
}

// Template is a signing template as the module API lists it.
type Template struct {
	ID, Name string
	Parties  []Party
	Fields   []Field
}

// Settings are the signing settings of an absence type.
type Settings struct {
	EmployeeParty string
	ApproverParty string
	Fields        map[string]string // template field id → leave value key
}

func known(key string) bool {
	for _, k := range Keys {
		if k == key {
			return true
		}
	}
	return false
}

// Validate checks settings against tpl: two distinct existing parties, and
// every mapped field exists, accepts a value, and maps to a known key.
func Validate(s Settings, tpl Template) error {
	parties := map[string]bool{}
	for _, p := range tpl.Parties {
		parties[p.ID] = true
	}
	switch {
	case !parties[s.EmployeeParty]:
		return fmt.Errorf("%w: employee_party", ErrParty)
	case !parties[s.ApproverParty] || s.ApproverParty == s.EmployeeParty:
		return fmt.Errorf("%w: approver_party", ErrParty)
	case len(s.Fields) > MaxFields:
		return fmt.Errorf("%w: too many", ErrField)
	}
	fields := map[string]Field{}
	for _, f := range tpl.Fields {
		fields[f.ID] = f
	}
	for id, key := range s.Fields {
		f, ok := fields[id]
		if !ok || !f.TextValued {
			return fmt.Errorf("%w: %s", ErrField, id)
		}
		if !known(key) {
			return fmt.Errorf("%w: %s", ErrValue, key)
		}
	}
	return nil
}

// Values are the leave values of one approval.
type Values struct {
	EmployeeName string
	Department   string
	AbsenceType  string
	Start, End   time.Time
	Days         leavedays.Tenths
	Reason       string
	ApproverName string
	Today        time.Time
}

func date(t time.Time) string { return leavedays.Date(t).Format("02.01.2006") }

// Render returns the prefill (template field id → value) of settings.
func Render(s Settings, v Values) map[string]string {
	vals := map[string]string{
		EmployeeName: v.EmployeeName,
		Department:   v.Department,
		AbsenceType:  v.AbsenceType,
		StartDate:    date(v.Start),
		EndDate:      date(v.End),
		Days:         v.Days.String(),
		Reason:       v.Reason,
		ApproverName: v.ApproverName,
		Today:        date(v.Today),
	}
	out := make(map[string]string, len(s.Fields))
	for id, key := range s.Fields {
		out[id] = vals[key]
	}
	return out
}
