package charges

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-hr/v4/internal/leavedays"
)

func TestPlan(t *testing.T) {
	by := map[int]Allowance{
		2026: {ID: "b-2026", Year: 2026, Total: 200, Carried: 30, Used: 190}, // 4 left
		2027: {ID: "a-2027", Year: 2027, Total: 200},
	}
	plan, err := Plan(map[int]leavedays.Tenths{2026: 40, 2027: 25, 2028: 0}, by, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || plan[0].AllowanceID != "a-2027" || plan[1].AllowanceID != "b-2026" || plan[1].Days != 40 || plan[0].Days != 25 {
		t.Fatalf("plan (lock order by id): %+v", plan)
	}
	_, err = Plan(map[int]leavedays.Tenths{2026: 45}, by, false)
	var ye *YearError
	if !errors.As(err, &ye) || !errors.Is(err, ErrInsufficient) || ye.Year != 2026 || ye.Remaining != 40 || ye.Requested != 45 ||
		!strings.Contains(err.Error(), "4.5 days requested, 4 remaining") {
		t.Fatalf("insufficient: %v", err)
	}
	if _, err := Plan(map[int]leavedays.Tenths{2026: 45}, by, true); err != nil {
		t.Fatalf("overdraw refused: %v", err)
	}
	_, err = Plan(map[int]leavedays.Tenths{2029: 10}, by, true)
	if !errors.Is(err, ErrNoAllowance) || !strings.Contains(err.Error(), "2029") {
		t.Fatalf("no allowance: %v", err)
	}
	if _, err := Plan(map[int]leavedays.Tenths{2026: 10}, map[int]Allowance{2026: {}}, false); !errors.Is(err, ErrNoAllowance) {
		t.Fatal("empty allowance id accepted")
	}
	// Equal ids sort stably (same allowance twice cannot happen, but the comparator handles it).
	p, _ := Plan(map[int]leavedays.Tenths{2026: 10, 2027: 10}, map[int]Allowance{2026: {ID: "x", Total: 100}, 2027: {ID: "x", Total: 100}}, false)
	if len(p) != 2 {
		t.Fatal("equal ids")
	}
}

func TestRefundAndOverdrawn(t *testing.T) {
	charged := []Charge{{AllowanceID: "a", Days: 40}, {AllowanceID: "b", Days: 25}, {AllowanceID: "gone", Days: 10}, {AllowanceID: "a", Days: 0}}
	r := Refund(charged, map[string]leavedays.Tenths{"a": 100, "b": 10})
	if r["a"] != 40 || r["b"] != 10 || len(r) != 2 {
		t.Fatalf("refund: %v", r)
	}
	if r := Refund([]Charge{{AllowanceID: "a", Days: 5}, {AllowanceID: "a", Days: 5}}, map[string]leavedays.Tenths{"a": 5}); r["a"] != 5 {
		t.Fatalf("floor across charges: %v", r)
	}
	if r := Refund([]Charge{{AllowanceID: "a", Days: 5}}, map[string]leavedays.Tenths{"a": 0}); len(r) != 0 {
		t.Fatalf("zero used: %v", r)
	}
	by := map[int]Allowance{2026: {ID: "a", Year: 2026, Total: 100, Used: 90}, 2027: {ID: "b", Year: 2027, Total: 100}}
	od := Overdrawn([]Charge{{AllowanceID: "a", Year: 2026, Days: 20}, {AllowanceID: "b", Year: 2027, Days: 20}}, by)
	if len(od) != 1 || od[0].ID != "a" || od[0].Remaining() != -10 {
		t.Fatalf("overdrawn: %+v", od)
	}
}
