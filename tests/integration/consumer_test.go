//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	sdk "github.com/go-tangra/go-tangra-scheduler/sdk/v4/pkg/taskexec"
	"github.com/go-tangra/go-tangra-signing/sdk/v4/pkg/signingclient"

	"github.com/go-tangra/go-tangra-hr/v4/internal/consumer"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

// T053 / SC-002, SC-003: the signing outcome consumer against a real Valkey
// and the real database. A completed submission published to
// platform:events:<tenant> approves and charges its request once; a new
// consumer instance (hr restarted) resumes from the cursor persisted in
// hr_tenants and applies what was published while it was down without
// re-applying anything; a cursor pointing at trimmed entries continues with
// the entries still in the stream and an unknown (empty) cursor starts at the
// tail; the reconcile task repairs the completion and the decline that the
// consumer missed. Exactly one charge per completed request overall.
func TestSigningConsumer(t *testing.T) {
	e := newEnv(t, envOpt{valkey: true})
	f := e.setup(t, fixtureOpt{signedDays: 10})
	key := stream.Key(tenantA)

	// publish appends a signing module event as signing does ({to,type,data,at}).
	publish := func(typ, data string) string {
		t.Helper()
		id, err := e.stream.XAdd(ctx, key, map[string]string{"to": "*", "type": typ, "data": data, "at": time.Now().UTC().Format(time.RFC3339Nano)}, 10000)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	completed := func(sub string) string {
		return publish(consumer.TypeCompleted, `{"submission_id":"`+sub+`","template_id":"t1","final_version":2,"audit_trail":true}`)
	}
	declined := func(sub string) string {
		return publish(consumer.TypeCancelled, `{"submission_id":"`+sub+`","template_id":"t1","reason_code":"declined"}`)
	}
	// awaiting creates a two-day signing-required request and has petar
	// approve it: it awaits signing with a new submission.
	awaiting := func(start, end string) (id, sub string) {
		t.Helper()
		id = e.request(t, "maria", f.signed, start, end, nil)
		r := e.json("POST", "/requests/"+id+"/approve", "petar", map[string]any{"notes": "ok"})
		expect(t, r, 200, "approve signed request")
		m := r.json(t)
		if m["status"] != "awaiting_signing" {
			t.Fatalf("approved into %v", m["status"])
		}
		s := e.submission(t, id)
		return id, s
	}
	cursor := func() string {
		t.Helper()
		ts, err := e.a.Repo.TenantsSystem(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range ts {
			if s.TenantID == tenantA {
				return s.StreamCursor
			}
		}
		return ""
	}
	setCursor := func(c string) {
		t.Helper()
		if err := e.a.Repo.SetCursor(ctx, tenantA, c, e.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// run starts a consumer instance wired as app.buildServices does, with a
	// short block time; stop cancels it and waits.
	run := func() (stop func()) {
		c := &consumer.Consumer{Store: e.a.Repo, Stream: e.stream, Apply: e.a.Requests, Block: 100 * time.Millisecond,
			Batch: 100, Refresh: 100 * time.Millisecond, Retry: 50 * time.Millisecond, Log: e.a.Log, Now: e.a.Now}
		cctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = c.Run(cctx) }()
		return func() { cancel(); wg.Wait() }
	}
	// caughtUp: the persisted cursor is at the newest entry (hr's own hr.*
	// events land on the same stream after each applied outcome).
	caughtUp := func() bool {
		last, err := e.stream.XLast(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		return cursor() == last
	}
	statusOf := func(id string) string { return str(e.status(t, id)["status"]) }
	used := func() float64 {
		v, _ := e.allowance(t, f.signedAllowance)["used_days"].(float64)
		return v
	}

	// 1. A request awaiting signing; the consumer starts at the tail.
	r1, s1 := awaiting("2026-10-05", "2026-10-06")
	if used() != 0 {
		t.Fatal("charged before the document was signed")
	}
	stop := run()
	waitFor(t, "initial cursor", func() bool { return cursor() != "" })
	e1 := completed(s1)
	waitFor(t, "r1 approved", func() bool { return statusOf(r1) == "approved" })
	waitFor(t, "caught up after e1", caughtUp)
	_ = e1
	if used() != 2 {
		t.Fatalf("used %v after r1, want 2", used())
	}
	stop()

	// 2. hr is down: r2's completion and a replay of r1's are published; a
	// new consumer resumes from the persisted cursor.
	r2, s2 := awaiting("2026-10-12", "2026-10-13")
	completed(s1)
	e2 := completed(s2)
	stop = run()
	waitFor(t, "r2 approved after restart", func() bool { return statusOf(r2) == "approved" })
	waitFor(t, "caught up after e2", caughtUp)
	_ = e2
	stop()
	if used() != 4 {
		t.Fatalf("used %v after restart, want 4 (r1 not re-applied)", used())
	}

	// 3. A full replay from the beginning of the stream applies nothing again.
	setCursor("0-0")
	stop = run()
	waitFor(t, "replay consumed", caughtUp)
	stop()
	if used() != 4 {
		t.Fatalf("used %v after a full replay, want 4", used())
	}

	// 4. Trimmed cursor: r3's completion is trimmed from the stream before hr
	// read it and the cursor points at a trimmed entry. The consumer goes on
	// with what is still in the stream (r3 stays awaiting signing).
	r3, s3 := awaiting("2026-10-19", "2026-10-20")
	e3 := completed(s3)
	filler := publish("hr.calendar.changed", `{"from":"2026-10-19","to":"2026-10-20"}`)
	if err := e.stream.XTrimMinID(ctx, key, filler); err != nil {
		t.Fatal(err)
	}
	if left, _ := e.stream.XRange(ctx, key, "", 100); len(left) != 1 || left[0].ID != filler {
		t.Fatalf("trim left %v", left)
	}
	_ = e3
	stop = run()
	waitFor(t, "cursor past the trimmed entries", func() bool { return cursor() == filler })

	// 5. Unknown cursor: r4's decline is published while the cursor is lost;
	// the consumer falls back to the tail and does not see it.
	r4, s4 := awaiting("2026-10-26", "2026-10-27")
	stop()
	e4 := declined(s4)
	setCursor("")
	stop = run()
	waitFor(t, "cursor reset to the tail", func() bool { return cursor() == e4 })
	after := publish("hr.calendar.changed", `{"from":"2026-10-26","to":"2026-10-27"}`)
	waitFor(t, "consumer running after the fallback", func() bool { return cursor() == after })
	stop()
	if statusOf(r3) != "awaiting_signing" || statusOf(r4) != "awaiting_signing" {
		t.Fatalf("missed outcomes applied by the consumer: r3=%s r4=%s", statusOf(r3), statusOf(r4))
	}
	if used() != 4 {
		t.Fatalf("used %v before reconcile, want 4", used())
	}

	// 6. The reconcile task (as the scheduler runs it) repairs both: the
	// signing module reports r3 completed and r4 declined.
	e.signing.set(s3, signingclient.State{Status: "completed", FinalVersion: 2})
	e.signing.set(s4, signingclient.State{Status: "cancelled", CancelReasonCode: "declined"})
	e.clock.Advance(30 * time.Minute)
	res := e.a.Tasks.Reconcile(ctx, sdk.Request{Payload: []byte(`{"older_than_minutes":10}`)})
	if !res.Success {
		t.Fatalf("reconcile: %+v", res)
	}
	if statusOf(r3) != "approved" {
		t.Fatalf("r3 after reconcile: %s", statusOf(r3))
	}
	m4 := e.status(t, r4)
	if m4["status"] != "pending" || m4["signing_note"] != "declined" {
		t.Fatalf("r4 after reconcile: %v", m4)
	}
	if used() != 6 {
		t.Fatalf("used %v after reconcile, want 6", used())
	}
	// A second run and a late event for r3 change nothing.
	if res := e.a.Tasks.Reconcile(ctx, sdk.Request{Payload: []byte(`{}`)}); !res.Success {
		t.Fatalf("second reconcile: %+v", res)
	}
	late := completed(s3)
	stop = run()
	waitFor(t, "late event consumed", func() bool { return !stream.Less(cursor(), late) })
	stop()

	// Exactly one charge per completed request, one outcome per submission.
	if used() != 6 {
		t.Fatalf("used %v at the end, want 6", used())
	}
	for _, id := range []string{r1, r2, r3} {
		if n := e.count(t, `SELECT count(*) FROM hr_request_charges WHERE request_id = $1`, id); n != 1 {
			t.Errorf("request %s has %d charges", id, n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM hr_request_charges`); n != 3 {
		t.Errorf("%d charges overall, want 3", n)
	}
	results := map[string]string{}
	conn := e.admin(t)
	rows, err := conn.Query(ctx, `SELECT submission_id::text, source || ':' || result FROM hr_signing_outcomes`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var sub, res string
		if err := rows.Scan(&sub, &res); err != nil {
			t.Fatal(err)
		}
		results[sub] = res
	}
	rows.Close()
	want := map[string]string{s1: "event:applied", s2: "event:applied", s3: "reconcile:applied", s4: "reconcile:applied"}
	for sub, w := range want {
		if results[sub] != w {
			t.Errorf("outcome of %s: %q, want %q (all %v)", sub, results[sub], w, results)
		}
	}
	if len(results) != len(want) {
		t.Errorf("outcomes %v", results)
	}
	// The employee was mailed the approvals and the failure.
	if n := e.count(t, `SELECT count(*) FROM hr_mail_outbox WHERE key = 'hr.request_approved' AND user_id = 'maria'`); n != 3 {
		t.Errorf("%d approval mails, want 3", n)
	}
	if n := e.count(t, `SELECT count(*) FROM hr_mail_outbox WHERE key = 'hr.signing_failed'`); n != 2 { // maria and petar
		t.Errorf("%d signing failure mails, want 2", n)
	}
	// r4 can be approved again: a second signing attempt starts.
	r := e.json("POST", "/requests/"+r4+"/approve", "petar", nil)
	expect(t, r, 200, "re-approve after decline")
	if r.json(t)["status"] != "awaiting_signing" {
		t.Fatalf("re-approve: %s", r.Body)
	}
}
