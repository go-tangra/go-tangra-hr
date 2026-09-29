package catalog

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestHolidays(t *testing.T) {
	s, m, _ := newSvc()
	h, err := s.CreateHoliday(ctx, admin, HolidayInput{Date: day(2020, 3, 3).Add(5 * time.Hour), Name: "Liberation Day", Recurring: true})
	if err != nil || !h.Date.Equal(day(2020, 3, 3)) {
		t.Fatalf("create: %+v %v", h, err)
	}
	_, err = s.CreateHoliday(ctx, admin, HolidayInput{Date: day(2020, 3, 3), Name: "Dup"})
	is(t, err, apperr.Duplicate, "same date")
	_, err = s.CreateHoliday(ctx, admin, HolidayInput{Date: day(1999, 1, 1), Name: "Old"})
	is(t, err, apperr.Validation, "year range")
	_, err = s.CreateHoliday(ctx, admin, HolidayInput{Date: day(2026, 1, 1), Name: ""})
	is(t, err, apperr.Validation, "name")
	_, err = s.CreateHoliday(ctx, emp, HolidayInput{Date: day(2026, 1, 1), Name: "x"})
	is(t, err, apperr.Forbidden, "employee")
	leap, _ := s.CreateHoliday(ctx, admin, HolidayInput{Date: day(2024, 2, 29), Name: "Leap", Recurring: true})
	once, _ := s.CreateHoliday(ctx, admin, HolidayInput{Date: day(2026, 8, 5), Name: "Test"})
	_, _ = s.CreateHoliday(ctx, admin, HolidayInput{Date: day(2026, 1, 1), Name: "New Year"})

	y26, err := s.ListHolidays(ctx, emp, 2026)
	if err != nil || len(y26) != 3 || !y26[0].Date.Equal(day(2026, 1, 1)) || !y26[1].Date.Equal(day(2026, 3, 3)) {
		t.Fatalf("2026: %+v %v", y26, err)
	}
	y28, _ := s.ListHolidays(ctx, emp, 2028)
	if len(y28) != 2 || !y28[0].Date.Equal(day(2028, 2, 29)) {
		t.Fatalf("2028: %+v", y28)
	}
	if all, _ := s.ListHolidays(ctx, emp, 0); len(all) != 4 {
		t.Fatal("all")
	}
	_, err = s.ListHolidays(ctx, nobody, 0)
	is(t, err, apperr.Forbidden, "no calendar")

	up, err := s.UpdateHoliday(ctx, admin, once.ID, HolidayInput{Date: day(2026, 8, 6), Name: "Moved"})
	if err != nil || up.Name != "Moved" {
		t.Fatal(err)
	}
	_, err = s.UpdateHoliday(ctx, admin, once.ID, HolidayInput{Date: day(2026, 1, 1), Name: "Clash"})
	is(t, err, apperr.Duplicate, "update onto taken date")
	_, err = s.UpdateHoliday(ctx, admin, once.ID, HolidayInput{Date: day(2026, 1, 2)})
	is(t, err, apperr.Validation, "update invalid")
	_, err = s.UpdateHoliday(ctx, admin, "0190f7c2-6a3e-7c1a-9b2e-000000000000", HolidayInput{Date: day(2026, 1, 2), Name: "x"})
	is(t, err, apperr.NotFound, "update missing")
	_, err = s.UpdateHoliday(ctx, emp, once.ID, HolidayInput{Date: day(2026, 1, 2), Name: "x"})
	is(t, err, apperr.Forbidden, "employee update")

	cal, err := s.Calendar(ctx, tn)
	if err != nil || !cal.IsHoliday(day(2031, 3, 3)) || !cal.IsHoliday(day(2026, 8, 6)) || cal.IsHoliday(day(2026, 8, 5)) {
		t.Fatal("calendar")
	}
	is(t, s.DeleteHoliday(ctx, emp, leap.ID), apperr.Forbidden, "employee delete")
	if err := s.DeleteHoliday(ctx, admin, leap.ID); err != nil {
		t.Fatal(err)
	}
	is(t, s.DeleteHoliday(ctx, admin, leap.ID), apperr.NotFound, "delete deleted")
	m.SetErr(errors.New("down"))
	if _, err := s.Calendar(ctx, tn); err == nil {
		t.Fatal("calendar error")
	}
	if _, err := s.ListHolidays(ctx, emp, 2026); err == nil {
		t.Fatal("list error")
	}
}

func TestParseImport(t *testing.T) {
	data := "\ufeff# Bulgarian holidays\n2026-01-01,New Year\n\n2026-03-03, Liberation Day ,yearly\n2026-05-01,Labour,once\n" +
		"bad line\n2026-13-01,Bad date\n2026-06-01,\n2026-07-01,x,maybe\n2026-01-01,Dup\n1999-01-01,Old\n"
	items, errs := ParseImport([]byte(data), 100)
	if len(items) != 3 || !items[1].Recurring || items[1].Name != "Liberation Day" || items[2].Recurring {
		t.Fatalf("items: %+v", items)
	}
	want := []LineError{{6, "format"}, {7, "date"}, {8, "name"}, {9, "format"}, {10, "duplicate_in_file"}, {11, "date"}}
	if len(errs) != len(want) {
		t.Fatalf("errors: %+v", errs)
	}
	for i, w := range want {
		if errs[i] != w {
			t.Errorf("error %d: %+v want %+v", i, errs[i], w)
		}
	}
	_, errs = ParseImport([]byte("2026-01-01,a\n2026-01-02,b\n2026-01-03,c\n"), 2)
	if len(errs) != 1 || errs[0].Reason != "too_many_lines" {
		t.Fatalf("line limit: %+v", errs)
	}
	_, errs = ParseImport([]byte(strings.Repeat("x", 5000)), 10)
	if len(errs) != 1 || errs[0].Reason != "format" {
		t.Fatalf("long line: %+v", errs)
	}
	_, errs = ParseImport([]byte("2026-01-01,\xff\xfe\n"), 10)
	if len(errs) != 1 || errs[0].Reason != "format" {
		t.Fatalf("invalid utf8: %+v", errs)
	}
}

func TestImportHolidays(t *testing.T) {
	s, m, _ := newSvc()
	_, _ = s.CreateHoliday(ctx, admin, HolidayInput{Date: day(2026, 1, 1), Name: "New Year"})
	file := []byte("2026-01-01,New Year\n2026-03-03,Liberation,yearly\n2026-05-24,Culture\n")
	res, err := s.ImportHolidays(ctx, admin, file, true)
	if err != nil || res.Created != 2 || res.Skipped != 1 || !res.DryRun {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	if hs, _ := m.ListHolidays(ctx, tn); len(hs) != 1 {
		t.Fatal("dry run wrote")
	}
	res, err = s.ImportHolidays(ctx, admin, file, false)
	if err != nil || res.Created != 2 {
		t.Fatalf("import: %+v %v", res, err)
	}
	if hs, _ := m.ListHolidays(ctx, tn); len(hs) != 3 {
		t.Fatal("not imported")
	}
	res, err = s.ImportHolidays(ctx, admin, []byte("2026-12-24,Eve\nbad\n"), false)
	is(t, err, apperr.Validation, "errors refuse the import")
	if res.Created != 0 || len(res.Errors) != 1 {
		t.Fatalf("refused result: %+v", res)
	}
	res, err = s.ImportHolidays(ctx, admin, []byte("2026-12-24,Eve\nbad\n"), true)
	if err != nil || len(res.Errors) != 1 || res.Created != 0 {
		t.Fatal("dry run with errors")
	}
	if res, err := s.ImportHolidays(ctx, admin, []byte("2026-01-01,New Year\n"), false); err != nil || res.Created != 0 || res.Skipped != 1 {
		t.Fatal("nothing to do")
	}
	_, err = s.ImportHolidays(ctx, emp, file, true)
	is(t, err, apperr.Forbidden, "employee import")
	_, err = s.ImportHolidays(ctx, admin, make([]byte, 70<<10), true)
	is(t, err, apperr.PayloadTooLarge, "too large")
	m.Fail("CreateHoliday", errors.New("down"))
	if res, err := s.ImportHolidays(ctx, admin, []byte("2026-12-25,Christmas\n"), false); err == nil || res.Created != 0 {
		t.Fatal("write failure")
	}
	m.Fail("ListHolidays", errors.New("down"))
	if _, err := s.ImportHolidays(ctx, admin, []byte("2026-12-25,Christmas\n"), false); err == nil {
		t.Fatal("list failure")
	}
	if got := InYear([]store.Holiday{{Date: day(2027, 1, 1), Recurring: true}}, 2026); len(got) != 0 {
		t.Fatal("recurring before first year")
	}
}

func TestNewDefaults(t *testing.T) {
	s := New(Deps{})
	if s.d.Limits.MaxImportBytes != 64<<10 || s.d.Limits.MaxImportLines != 500 || s.d.Now == nil {
		t.Fatal("defaults")
	}
	if reasonOf(errors.New("x")) != "error" {
		t.Fatal("reasonOf")
	}
}
