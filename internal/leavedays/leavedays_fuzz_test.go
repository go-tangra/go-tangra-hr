package leavedays

import (
	"testing"
	"time"
)

// FuzzCount checks the day count invariants on arbitrary ranges: never
// negative, never more than one day per calendar day, halves only subtract.
func FuzzCount(f *testing.F) {
	f.Add(int64(0), 4, false, false)
	f.Add(int64(3650), 400, true, true)
	f.Add(int64(-100), 1, true, false)
	f.Fuzz(func(t *testing.T, offset int64, length int, halfStart, halfEnd bool) {
		if length < 0 || length > 800 || offset < -40000 || offset > 40000 {
			return
		}
		c := NewCalendar([]Holiday{{Date: time.Date(2024, 12, 25, 0, 0, 0, 0, time.UTC), Recurring: true}})
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(offset))
		s := Span{Start: start, End: start.AddDate(0, 0, length), HalfStart: halfStart, HalfEnd: halfEnd, MaxDays: 366}
		got, err := c.Count(s)
		if err != nil {
			return
		}
		full, err := c.Count(Span{Start: s.Start, End: s.End, MaxDays: 366})
		if err != nil || got <= 0 || got > full || got < full-2*Half || got > Tenths(length+1)*Day {
			t.Fatalf("count %v full %v err %v for %+v", got, full, err, s)
		}
		if p, err := Parse(got.String()); err != nil || p != got {
			t.Fatalf("round trip %v", got)
		}
	})
}
