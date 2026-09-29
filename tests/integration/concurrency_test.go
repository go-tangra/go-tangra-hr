//go:build integration

package integration

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// T042 / SC-005 through the whole service. The store-level properties (a
// locked AddUsed loop, raw overlapping inserts) are proved in
// internal/repo/repodb; this suite drives the HTTP API → requests.Service →
// repodb path with the real routing, charging and outbox writes.
//
// One allowance (maria's 2026 annual pool, 100 days) is charged by approvals
// of every weekday request of 2026 (261 one-day requests — the most one
// person can hold in one year under the overlap exclusion), each approved by
// four concurrent calls: 1,044 concurrent approvals. Exactly 100 requests
// are approved, each charged once, the allowance is never overdrawn, and
// revoking every approved one restores exactly what was charged.
func TestConcurrentApprovalsNeverOverdraw(t *testing.T) {
	const total = 100
	const perRequest = 4
	e := newEnv(t, envOpt{})
	f := e.setup(t, fixtureOpt{annualDays: total})

	var days []string
	for d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == 2026; d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			days = append(days, d.Format("2006-01-02"))
		}
	}
	ids := make([]string, len(days))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	var mu sync.Mutex
	var createErrs []string
	for i, d := range days {
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := e.json("POST", "/requests", "maria", map[string]any{"absence_type_id": f.annual, "start_date": d, "end_date": d})
			if r.Code != 201 {
				mu.Lock()
				createErrs = append(createErrs, fmt.Sprintf("%s: %d %s", d, r.Code, r.Body))
				mu.Unlock()
				return
			}
			ids[i] = str(r.json(t)["id"])
		}(i, d)
	}
	wg.Wait()
	if len(createErrs) > 0 {
		t.Fatalf("creates failed: %v", createErrs[:min(5, len(createErrs))])
	}

	start := time.Now()
	codes := map[int]int{}
	okByRequest := map[string]int{}
	reasons := map[string]int{}
	gate := make(chan struct{})
	for _, id := range ids {
		for k := 0; k < perRequest; k++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				<-gate
				r := e.json("POST", "/requests/"+id+"/approve", "petar", map[string]any{"notes": "ok"})
				mu.Lock()
				defer mu.Unlock()
				codes[r.Code]++
				if r.Code == 200 {
					okByRequest[id]++
				} else {
					reasons[r.reason()]++
				}
			}(id)
		}
	}
	close(gate)
	wg.Wait()
	t.Logf("%d concurrent approvals of %d requests in %s: codes %v, refusals %v", len(ids)*perRequest, len(ids), time.Since(start), codes, reasons)

	for code := range codes {
		if code != 200 && code != 409 {
			t.Errorf("unexpected status %d (%d times)", code, codes[code])
		}
	}
	for reason := range reasons {
		switch reason {
		case "insufficient_allowance", "conflict", "invalid_transition":
		default:
			t.Errorf("unexpected refusal %q", reason)
		}
	}
	if codes[200] != total || len(okByRequest) != total {
		t.Fatalf("approved %d calls / %d requests, want exactly %d", codes[200], len(okByRequest), total)
	}
	for id, n := range okByRequest {
		if n != 1 {
			t.Errorf("request %s approved %d times", id, n)
		}
	}
	al := e.allowance(t, f.annualAllowance)
	if al["used_days"] != float64(total) || al["remaining"] != float64(0) {
		t.Fatalf("allowance after approvals: %v", al)
	}
	// Exactly the approved requests are charged, once, one day each.
	if n := e.count(t, `SELECT count(*) FROM hr_request_charges WHERE allowance_id = $1`, f.annualAllowance); n != total {
		t.Fatalf("%d charge rows, want %d", n, total)
	}
	if n := e.count(t, `SELECT count(*) FROM hr_request_charges c JOIN hr_requests r ON r.id = c.request_id WHERE r.status <> 'approved'`); n != 0 {
		t.Fatalf("%d charges on requests that are not approved", n)
	}
	if n := e.count(t, `SELECT count(*) FROM hr_requests WHERE status = 'approved'`); n != total {
		t.Fatalf("%d approved requests, want %d", n, total)
	}
	if n := e.count(t, `SELECT count(*) FROM (SELECT request_id FROM hr_request_charges GROUP BY request_id HAVING count(*) > 1) d`); n != 0 {
		t.Fatalf("%d requests charged more than once", n)
	}
	if n := e.count(t, `SELECT count(*) FROM hr_allowances WHERE used_days > total_days + carried_over`); n != 0 {
		t.Fatal("an allowance is overdrawn")
	}
	// One approval mail per approved request, queued with the state change.
	if n := e.count(t, `SELECT count(*) FROM hr_mail_outbox WHERE key = 'hr.request_approved'`); n != total {
		t.Fatalf("%d approval mails queued, want %d", n, total)
	}

	// Every refund restores exactly what was charged (concurrent revokes).
	for id := range okByRequest {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			r := e.json("POST", "/requests/"+id+"/revoke", "petar", map[string]any{"notes": "back"})
			if r.Code != 200 {
				t.Errorf("revoke %s: %d %s", id, r.Code, r.Body)
			}
		}(id)
	}
	wg.Wait()
	al = e.allowance(t, f.annualAllowance)
	if al["used_days"] != float64(0) || al["remaining"] != float64(total) {
		t.Fatalf("allowance after refunds: %v", al)
	}
	if n := e.count(t, `SELECT count(*) FROM hr_request_charges`); n != 0 {
		t.Fatalf("%d charges left after revoking everything", n)
	}
}

// Overlapping requests of one person created concurrently through the API:
// exactly one survives (hr_requests_no_overlap), the others are refused with
// "overlap"; requests of another person on the same days are unaffected.
func TestConcurrentOverlappingRequests(t *testing.T) {
	e := newEnv(t, envOpt{})
	f := e.setup(t, fixtureOpt{})
	const n = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	reasons := map[string]int{}
	gate := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			start := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i%3)
			r := e.json("POST", "/requests", "maria", map[string]any{"absence_type_id": f.unpaid,
				"start_date": start.Format("2006-01-02"), "end_date": start.AddDate(0, 0, 4).Format("2006-01-02")})
			mu.Lock()
			defer mu.Unlock()
			codes[r.Code]++
			if r.Code != 201 {
				reasons[r.reason()]++
			}
		}(i)
	}
	close(gate)
	wg.Wait()
	if codes[201] != 1 || codes[409] != n-1 || reasons["overlap"] != n-1 {
		t.Fatalf("codes %v reasons %v", codes, reasons)
	}
	if c := e.count(t, `SELECT count(*) FROM hr_requests WHERE user_id = 'maria'`); c != 1 {
		t.Fatalf("%d requests stored", c)
	}
	// Petar may take the same days.
	e.request(t, "petar", f.unpaid, "2026-08-04", "2026-08-06", nil)
}
