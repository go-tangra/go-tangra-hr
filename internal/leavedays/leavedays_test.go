package leavedays

import (
	"errors"
	"testing"
	"time"
)

func d(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }

func TestCountWeekdaysAndHolidays(t *testing.T) {
	c := NewCalendar([]Holiday{
		{Date: d(2026, 8, 5)},                   // Wednesday
		{Date: d(2020, 3, 3), Recurring: true},  // every 3 March from 2020
		{Date: d(2030, 5, 24), Recurring: true}, // from 2030 only
		{Date: d(2031, 5, 24), Recurring: true}, // later duplicate keeps the earliest year
		{Date: d(2024, 2, 29), Recurring: true}, // leap years only
	})
	cases := []struct {
		name string
		s    Span
		want Tenths
	}{
		{"mon-fri with holiday", Span{Start: d(2026, 8, 3), End: d(2026, 8, 7)}, 40},
		{"mon-sun with holiday", Span{Start: d(2026, 8, 3), End: d(2026, 8, 9)}, 40},
		{"half last day", Span{Start: d(2026, 8, 3), End: d(2026, 8, 7), HalfEnd: true}, 35},
		{"half both ends", Span{Start: d(2026, 8, 3), End: d(2026, 8, 7), HalfStart: true, HalfEnd: true}, 30},
		{"single half day", Span{Start: d(2026, 8, 4), End: d(2026, 8, 4), HalfStart: true}, 5},
		{"half on weekend end ignored", Span{Start: d(2026, 8, 6), End: d(2026, 8, 8), HalfEnd: true}, 20},
		{"recurring", Span{Start: d(2026, 3, 2), End: d(2026, 3, 4)}, 20},
		{"recurring before first year", Span{Start: d(2019, 3, 4), End: d(2019, 3, 4)}, 10},
		{"recurring from 2030", Span{Start: d(2029, 5, 24), End: d(2029, 5, 24)}, 10},
		{"time of day ignored", Span{Start: time.Date(2026, 8, 3, 23, 0, 0, 0, time.UTC), End: d(2026, 8, 3)}, 10},
	}
	for _, tc := range cases {
		got, err := c.Count(tc.s)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
	if !c.IsHoliday(d(2028, 2, 29)) || c.IsHoliday(d(2026, 2, 28)) || !c.IsHoliday(d(2031, 5, 24)) || !c.IsHoliday(d(2030, 5, 24)) {
		t.Fatal("leap/recurring holidays")
	}
	if c.IsWorkday(d(2026, 8, 8)) || !c.IsWorkday(d(2026, 8, 4)) {
		t.Fatal("IsWorkday")
	}
}

func TestErrorsAndYearSplit(t *testing.T) {
	c := NewCalendar(nil)
	if _, err := c.Count(Span{Start: d(2026, 8, 7), End: d(2026, 8, 3)}); !errors.Is(err, ErrRange) {
		t.Fatalf("range: %v", err)
	}
	if _, err := c.Count(Span{Start: d(2026, 1, 1), End: d(2027, 6, 1), MaxDays: 366}); !errors.Is(err, ErrSpan) {
		t.Fatalf("span: %v", err)
	}
	if _, err := c.Count(Span{Start: d(2026, 8, 4), End: d(2026, 8, 4), HalfEnd: true}); !errors.Is(err, ErrHalf) {
		t.Fatalf("half: %v", err)
	}
	if _, err := c.Count(Span{Start: d(2026, 8, 8), End: d(2026, 8, 9)}); !errors.Is(err, ErrZero) {
		t.Fatalf("zero: %v", err)
	}
	per, err := c.PerYear(Span{Start: d(2026, 12, 28), End: d(2027, 1, 5), HalfEnd: true, MaxDays: 366})
	if err != nil || per[2026] != 40 || per[2027] != 25 || len(per) != 2 {
		t.Fatalf("split: %v %v", per, err)
	}
	if _, err := c.PerYear(Span{Start: d(2026, 8, 7), End: d(2026, 8, 3)}); err == nil {
		t.Fatal("PerYear range")
	}
}

func TestTenths(t *testing.T) {
	for v, s := range map[Tenths]string{40: "4", 35: "3.5", 0: "0", -15: "-1.5", -20: "-2", 5: "0.5"} {
		if v.String() != s {
			t.Errorf("%d → %q", v, v.String())
		}
		p, err := Parse(s)
		if err != nil || p != v {
			t.Errorf("Parse(%q) = %v, %v", s, p, err)
		}
	}
	for _, bad := range []string{"", "x", "3.55", ".5", "3.", "3.x", "99999999999"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
	for f, want := range map[float64]Tenths{20: 200, 2.5: 25, -0.5: -5, 0: 0} {
		got, err := FromFloat(f)
		if err != nil || got != want {
			t.Errorf("FromFloat(%v) = %v, %v", f, got, err)
		}
	}
	if _, err := FromFloat(1.25); err == nil {
		t.Fatal("two decimals accepted")
	}
	if Tenths(35).Float() != 3.5 {
		t.Fatal("Float")
	}
}
