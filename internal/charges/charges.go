// Package charges plans how an approved leave request is charged to
// allowances and how a refund returns it (spec FR-014, FR-015, research D7).
//
// A request of a deducting absence type is charged, for every calendar year
// it touches, to that year's allowance of the type's pool (when the type is in
// a pool) or of the type itself. Every year must have an allowance and, unless
// an overdraw is accepted (a completed signing, FR-035), enough remaining
// days. A refund returns exactly what was charged, never making used days
// negative.
//
// The package is pure; the repository applies a plan inside one transaction
// with the allowances locked in id order.
package charges

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
)

// ErrNoAllowance means a year has no allowance for the type or pool.
var ErrNoAllowance = errors.New("charges: no allowance")

// ErrInsufficient means a year's remaining balance is too low.
var ErrInsufficient = errors.New("charges: insufficient allowance")

// YearError carries the year (and balance) behind an error.
type YearError struct {
	Err       error
	Year      int
	Requested leavedays.Tenths
	Remaining leavedays.Tenths
}

func (e *YearError) Error() string {
	if errors.Is(e.Err, ErrInsufficient) {
		return fmt.Sprintf("%v: %d: %s days requested, %s remaining", e.Err, e.Year, e.Requested, e.Remaining)
	}
	return fmt.Sprintf("%v: %d", e.Err, e.Year)
}

// Unwrap returns the sentinel.
func (e *YearError) Unwrap() error { return e.Err }

// Allowance is the balance state of one allowance row.
type Allowance struct {
	ID      string
	Year    int
	Total   leavedays.Tenths
	Carried leavedays.Tenths
	Used    leavedays.Tenths
}

// Remaining is total + carried − used (negative after an overdraw).
func (a Allowance) Remaining() leavedays.Tenths { return a.Total + a.Carried - a.Used }

// Charge is one allowance debit of a request.
type Charge struct {
	AllowanceID string
	Year        int
	Days        leavedays.Tenths
}

// Plan charges perYear (year → days) against byYear (year → the matching
// allowance). allowOverdraw skips the balance check but still needs an
// allowance per year. The result is ordered by allowance id (lock order).
func Plan(perYear map[int]leavedays.Tenths, byYear map[int]Allowance, allowOverdraw bool) ([]Charge, error) {
	years := make([]int, 0, len(perYear))
	for y := range perYear {
		years = append(years, y)
	}
	sort.Ints(years)
	out := make([]Charge, 0, len(years))
	for _, y := range years {
		days := perYear[y]
		if days <= 0 {
			continue
		}
		a, ok := byYear[y]
		if !ok || a.ID == "" {
			return nil, &YearError{Err: ErrNoAllowance, Year: y, Requested: days}
		}
		if !allowOverdraw && days > a.Remaining() {
			return nil, &YearError{Err: ErrInsufficient, Year: y, Requested: days, Remaining: a.Remaining()}
		}
		out = append(out, Charge{AllowanceID: a.ID, Year: y, Days: days})
	}
	slices.SortFunc(out, func(a, b Charge) int { return cmp.Compare(a.AllowanceID, b.AllowanceID) })
	return out, nil
}

// Refund returns the used-days decrement per allowance for charges, given
// the allowances' current used days: exactly the charged days, floored so used
// never goes below zero. Allowances that no longer exist are skipped.
func Refund(charged []Charge, used map[string]leavedays.Tenths) map[string]leavedays.Tenths {
	out := map[string]leavedays.Tenths{}
	left := map[string]leavedays.Tenths{}
	for id, u := range used {
		left[id] = u
	}
	for _, c := range charged {
		u, ok := left[c.AllowanceID]
		if !ok || c.Days <= 0 {
			continue
		}
		r := min(c.Days, u)
		if r <= 0 {
			continue
		}
		out[c.AllowanceID] += r
		left[c.AllowanceID] = u - r
	}
	return out
}

// Overdrawn reports the allowances a plan leaves negative (to notify HR
// administrators after an accepted overdraw).
func Overdrawn(plan []Charge, byYear map[int]Allowance) []Allowance {
	var out []Allowance
	for _, c := range plan {
		a := byYear[c.Year]
		a.Used += c.Days
		if a.Remaining() < 0 {
			out = append(out, a)
		}
	}
	return out
}
