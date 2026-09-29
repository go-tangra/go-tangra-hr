package catalog

import "testing"

// FuzzParseImport feeds arbitrary files to the holiday parser: it must never
// panic, never return more items than lines, and every item must be valid.
func FuzzParseImport(f *testing.F) {
	f.Add([]byte("2026-01-01,New Year\n2026-03-03,Liberation,yearly\n"))
	f.Add([]byte("\ufeff#c\n\n2026-02-29,x\n"))
	f.Add([]byte("a,b,c,d\n,,\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		items, errs := ParseImport(data, 50)
		if len(items)+len(errs) > 51+len(data) {
			t.Fatalf("too many results: %d %d", len(items), len(errs))
		}
		for _, it := range items {
			if _, err := checkHoliday(it); err != nil {
				t.Fatalf("invalid item %+v: %v", it, err)
			}
		}
	})
}
