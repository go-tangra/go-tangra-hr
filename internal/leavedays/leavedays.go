// Package leavedays counts the working days of a leave request (spec FR-011,
// FR-015, research D6): Monday–Friday, minus the tenant's public holidays
// (single dates and yearly recurring ones), minus half a day for a half first
// or last day, split by calendar year for charging.
//
// Days are fixed-point tenths (Tenths) so balances never use floating point
// (SC-005). Dates are calendar dates: any time.Time is reduced to its UTC
// year, month and day.
package leavedays

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Tenths is a number of days in tenths (35 = 3.5 days).
type Tenths int64

// Day is one working day.
const Day Tenths = 10

// Half is half a working day.
const Half Tenths = 5

// Errors of the day count.
var (
	ErrRange = errors.New("leavedays: end date before start date")
	ErrSpan  = errors.New("leavedays: date range too long")
	ErrHalf  = errors.New("leavedays: invalid half day")
	ErrZero  = errors.New("leavedays: no working days in range")
)

// Date returns t as a calendar date (UTC midnight of its UTC day).
func Date(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Holiday is a public holiday: one date, or the same month and day every year
// from Date's year on (a recurring 29 February applies in leap years only).
type Holiday struct {
	Date      time.Time
	Recurring bool
}

// Calendar answers working-day questions for one tenant.
type Calendar struct {
	single    map[time.Time]bool
	recurring map[[2]int]int // month, day → first year
}

// NewCalendar builds a calendar over holidays.
func NewCalendar(holidays []Holiday) Calendar {
	c := Calendar{single: map[time.Time]bool{}, recurring: map[[2]int]int{}}
	for _, h := range holidays {
		d := Date(h.Date)
		if !h.Recurring {
			c.single[d] = true
			continue
		}
		k := [2]int{int(d.Month()), d.Day()}
		if first, ok := c.recurring[k]; !ok || d.Year() < first {
			c.recurring[k] = d.Year()
		}
	}
	return c
}

// IsHoliday reports whether d is a public holiday.
func (c Calendar) IsHoliday(d time.Time) bool {
	d = Date(d)
	if c.single[d] {
		return true
	}
	first, ok := c.recurring[[2]int{int(d.Month()), d.Day()}]
	return ok && d.Year() >= first
}

// IsWorkday reports whether d is a working day (Monday–Friday, not a holiday).
func (c Calendar) IsWorkday(d time.Time) bool {
	switch Date(d).Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	return !c.IsHoliday(d)
}

// Span is the requested range with its half days.
type Span struct {
	Start, End         time.Time
	HalfStart, HalfEnd bool
	MaxDays            int // longest allowed range in calendar days (inclusive)
}

func (s Span) check() (start, end time.Time, err error) {
	start, end = Date(s.Start), Date(s.End)
	if end.Before(start) {
		return start, end, ErrRange
	}
	if s.MaxDays > 0 && int(end.Sub(start).Hours()/24)+1 > s.MaxDays {
		return start, end, ErrSpan
	}
	if start.Equal(end) && s.HalfEnd {
		return start, end, ErrHalf // a one-day request marks its half with HalfStart
	}
	return start, end, nil
}

// PerYear counts the working days of s by calendar year.
func (c Calendar) PerYear(s Span) (map[int]Tenths, error) {
	start, end, err := s.check()
	if err != nil {
		return nil, err
	}
	out := map[int]Tenths{}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if !c.IsWorkday(d) {
			continue
		}
		v := Day
		if (s.HalfStart && d.Equal(start)) || (s.HalfEnd && d.Equal(end)) {
			v = Half
		}
		out[d.Year()] += v
	}
	if len(out) == 0 {
		return nil, ErrZero
	}
	return out, nil
}

// Count is the total working days of s.
func (c Calendar) Count(s Span) (Tenths, error) {
	per, err := c.PerYear(s)
	if err != nil {
		return 0, err
	}
	var t Tenths
	for _, v := range per {
		t += v
	}
	return t, nil
}

// String renders t with one decimal only when needed ("4", "3.5", "-1.5").
func (t Tenths) String() string {
	sign := ""
	v := int64(t)
	if v < 0 {
		sign, v = "-", -v
	}
	if v%10 == 0 {
		return sign + strconv.FormatInt(v/10, 10)
	}
	return fmt.Sprintf("%s%d.%d", sign, v/10, v%10)
}

// Parse reads a decimal number of days with at most one decimal ("3", "3.5",
// "-0.5"); it is the inverse of String and of the database numeric(6,1).
func Parse(s string) (Tenths, error) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" || (hasFrac && len(frac) != 1) {
		return 0, fmt.Errorf("leavedays: bad number %q", s)
	}
	w, err := strconv.ParseInt(whole, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("leavedays: bad number %q", s)
	}
	v := w * 10
	if hasFrac {
		f := int64(frac[0] - '0')
		if f < 0 || f > 9 {
			return 0, fmt.Errorf("leavedays: bad number %q", s)
		}
		v += f
	}
	if neg {
		v = -v
	}
	return Tenths(v), nil
}

// FromFloat converts JSON input (e.g. 20 or 2.5) to tenths, refusing more
// than one decimal.
func FromFloat(f float64) (Tenths, error) {
	v := f * 10
	r := Tenths(int64(v + sign(v)*0.5))
	if d := float64(r) - v; d > 1e-6 || d < -1e-6 {
		return 0, fmt.Errorf("leavedays: %v has more than one decimal", f)
	}
	return r, nil
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// Float is t as a JSON number.
func (t Tenths) Float() float64 { return float64(t) / 10 }
