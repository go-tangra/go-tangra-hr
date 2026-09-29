package apperr

import (
	"errors"
	"fmt"
	"testing"
)

func TestError(t *testing.T) {
	e := Validation.WithField("name")
	if e.Error() != "validation: name" || Overlap.Error() != "overlap" || e.Status != 422 {
		t.Fatalf("error text: %q", e.Error())
	}
	if !errors.Is(fmt.Errorf("wrap: %w", e), Validation) || errors.Is(e, Overlap) || errors.Is(e, errors.New("validation")) {
		t.Fatal("Is")
	}
	d := InsufficientAllowance.WithDetail(map[string]any{"year": 2026})
	if d.Detail["year"] != 2026 || InsufficientAllowance.Detail != nil {
		t.Fatal("WithDetail must copy")
	}
	got, ok := As(fmt.Errorf("x: %w", d))
	if !ok || got.Reason != "insufficient_allowance" {
		t.Fatal("As")
	}
	if _, ok := As(errors.New("plain")); ok {
		t.Fatal("As plain")
	}
}
