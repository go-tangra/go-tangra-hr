package signingmap

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func tpl() Template {
	return Template{ID: "t1", Name: "Leave form",
		Parties: []Party{{ID: "p1", Name: "Employee"}, {ID: "p2", Name: "Approver"}},
		Fields: []Field{{ID: "name", Type: "text", Party: "p1", TextValued: true}, {ID: "days", Type: "number", Party: "p1", TextValued: true},
			{ID: "sig", Type: "signature", Party: "p2"}}}
}

func TestValidate(t *testing.T) {
	ok := Settings{EmployeeParty: "p1", ApproverParty: "p2", Fields: map[string]string{"name": EmployeeName, "days": Days}}
	if err := Validate(ok, tpl()); err != nil {
		t.Fatal(err)
	}
	big := map[string]string{}
	for i := 0; i <= MaxFields; i++ {
		big[fmt.Sprint(i)] = Days
	}
	cases := map[string]struct {
		s    Settings
		want error
	}{
		"employee party": {Settings{EmployeeParty: "x", ApproverParty: "p2"}, ErrParty},
		"approver party": {Settings{EmployeeParty: "p1", ApproverParty: "x"}, ErrParty},
		"same party":     {Settings{EmployeeParty: "p1", ApproverParty: "p1"}, ErrParty},
		"too many":       {Settings{EmployeeParty: "p1", ApproverParty: "p2", Fields: big}, ErrField},
		"missing field":  {Settings{EmployeeParty: "p1", ApproverParty: "p2", Fields: map[string]string{"x": Days}}, ErrField},
		"signature":      {Settings{EmployeeParty: "p1", ApproverParty: "p2", Fields: map[string]string{"sig": Days}}, ErrField},
		"unknown value":  {Settings{EmployeeParty: "p1", ApproverParty: "p2", Fields: map[string]string{"name": "salary"}}, ErrValue},
	}
	for name, c := range cases {
		if err := Validate(c.s, tpl()); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRender(t *testing.T) {
	s := Settings{Fields: map[string]string{"a": EmployeeName, "b": Department, "c": AbsenceType, "d": StartDate, "e": EndDate,
		"f": Days, "g": Reason, "h": ApproverName, "i": Today}}
	got := Render(s, Values{EmployeeName: "Мария", Department: "Platform", AbsenceType: "Annual leave",
		Start: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 7, 23, 0, 0, 0, time.UTC), Days: 35,
		Reason: "Holiday", ApproverName: "Petar", Today: time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC)})
	want := map[string]string{"a": "Мария", "b": "Platform", "c": "Annual leave", "d": "03.08.2026", "e": "07.08.2026", "f": "3.5",
		"g": "Holiday", "h": "Petar", "i": "01.07.2026"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Fatal("extra values")
	}
}
