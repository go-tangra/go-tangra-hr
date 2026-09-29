//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/consumer"
	"github.com/go-tangra/go-tangra-hr/v4/internal/people"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

// T063 / SC-008: a full flow with distinctive reasons, notes, review notes,
// person names, a department name and e-mail addresses. None of them appears
// in hr's logs, audit events, published events (the tenant streams) or the
// backup metadata (headers, archive envelope, backup audit). They are still
// where they belong: the request rows, the queued mail variables and the
// e-mails sent (addresses resolved at send time and never stored).
func TestNoPersonalDataLeaks(t *testing.T) {
	const (
		reason     = "LEAK-REASON-7f3a"
		notes      = "LEAK-NOTES-7f3a"
		edited     = "LEAK-EDIT-7f3a"
		review     = "LEAK-REVIEW-7f3a"
		reject     = "LEAK-REJECT-7f3a"
		revoke     = "LEAK-REVOKE-7f3a"
		signReason = "LEAK-SIGNREASON-7f3a"
		mariaName  = "Maria LEAK-NAME-M-7f3a"
		petarName  = "Petar LEAK-NAME-P-7f3a"
		hanaName   = "Hana LEAK-NAME-H-7f3a"
		mariaMail  = "leak-maria-7f3a@a.example"
		petarMail  = "leak-petar-7f3a@a.example"
		hanaMail   = "leak-hana-7f3a@a.example"
		deptName   = "LEAK-DEPT-7f3a"
	)
	dir := &people.Fake{Users: map[string][]people.Contact{
		tenantA: {
			{UserID: "hana", DisplayName: hanaName, Email: hanaMail}, {UserID: "vera", DisplayName: "Vera"},
			{UserID: "maria", DisplayName: mariaName, Email: mariaMail}, {UserID: "petar", DisplayName: petarName, Email: petarMail},
			{UserID: "cal", DisplayName: "Cal"},
		},
		tenantB: {{UserID: "outsider", DisplayName: "Otto", Email: "otto@b.example"}},
	}}
	e := newEnv(t, envOpt{dir: dir})
	f := e.setup(t, fixtureOpt{departmentName: deptName})

	// Approved, then revoked.
	r1 := e.request(t, "maria", f.annual, "2026-08-03", "2026-08-07", map[string]any{"reason": reason, "notes": notes})
	expect(t, e.json("PUT", "/requests/"+r1, "maria", map[string]any{"reason": edited, "notes": notes}), 200, "edit")
	expect(t, e.json("POST", "/requests/"+r1+"/approve", "maria", map[string]any{"notes": review}), 403, "self approval refused")
	expect(t, e.json("POST", "/requests/"+r1+"/approve", "petar", map[string]any{"notes": review}), 200, "approve")
	expect(t, e.json("POST", "/requests/"+r1+"/revoke", "petar", map[string]any{"notes": revoke}), 200, "revoke")
	// Rejected.
	r2 := e.request(t, "maria", f.annual, "2026-09-07", "2026-09-08", map[string]any{"reason": reason})
	expect(t, e.json("POST", "/requests/"+r2+"/reject", "petar", map[string]any{"notes": reject}), 200, "reject")
	// Cancelled by the employee.
	r3 := e.request(t, "maria", f.unpaid, "2026-09-14", "2026-09-14", map[string]any{"reason": reason})
	expect(t, e.json("POST", "/requests/"+r3+"/cancel", "maria", nil), 200, "cancel")
	// Signed: approval starts signing, the consumer applies the completion.
	r4 := e.request(t, "maria", f.signed, "2026-10-05", "2026-10-06", map[string]any{"reason": signReason})
	ap := e.json("POST", "/requests/"+r4+"/approve", "petar", map[string]any{"notes": review})
	expect(t, ap, 200, "approve signed")
	if ap.json(t)["status"] != "awaiting_signing" {
		t.Fatalf("signed approval: %s", ap.Body)
	}
	submission := e.submission(t, r4)
	key := stream.Key(tenantA)
	if _, err := e.mem.XAdd(ctx, key, map[string]string{"to": "*", "type": consumer.TypeCompleted, "data": `{"submission_id":"` + submission + `"}`,
		"at": time.Now().UTC().Format(time.RFC3339)}, 1000); err != nil {
		t.Fatal(err)
	}
	// A malformed signing event makes the consumer log a warning (so the log
	// check is not vacuous).
	if _, err := e.mem.XAdd(ctx, key, map[string]string{"to": "*", "type": consumer.TypeCompleted, "data": `{"submission_id":"` + reason + `"}`}, 1000); err != nil {
		t.Fatal(err)
	}
	c := &consumer.Consumer{Store: e.a.Repo, Stream: e.stream, Apply: e.a.Requests, Block: 10 * time.Millisecond, Log: e.a.Log, Now: e.a.Now}
	if _, err := c.Step(ctx, tenantA, "0-0"); err != nil {
		t.Fatal(err)
	}
	if st := e.status(t, r4); st["status"] != "approved" {
		t.Fatalf("signed request: %v", st["status"])
	}
	// The team calendar and the list views of other people.
	cal := e.json("GET", "/calendar?from=2026-08-01&to=2026-10-31", "cal", nil)
	expect(t, cal, 200, "calendar")
	for _, s := range []string{reason, edited, notes, review, signReason} {
		if strings.Contains(cal.Body.String(), s) {
			t.Errorf("%q in the calendar view", s)
		}
	}

	// Queued mail carries names and reasons (allowed) but never addresses.
	outbox := e.rows(t, "hr_mail_outbox")
	if !strings.Contains(outbox, mariaName) || !strings.Contains(outbox, revoke) {
		t.Fatalf("queued mail lacks its variables: %s", outbox)
	}
	for _, s := range []string{mariaMail, petarMail, hanaMail} {
		if strings.Contains(outbox, s) {
			t.Errorf("address %q stored in the outbox", s)
		}
	}
	if sent, err := e.a.Outbox.Drain(ctx); err != nil || sent == 0 {
		t.Fatalf("outbox drain: %d %v", sent, err)
	}
	if m := e.mails.dump(); !strings.Contains(m, mariaMail) || !strings.Contains(m, petarMail) || !strings.Contains(m, reason) {
		t.Fatalf("mails sent: %s", m)
	}

	// Backup export and import.
	ex := e.call("POST", prefix+"/backup/export", "hana", nil, "")
	expect(t, ex, 200, "export")
	archive := ex.Body.Bytes()
	expect(t, e.call("POST", prefix+"/backup/import?mode=skip", "hana", bytes.NewReader(archive), "application/gzip"), 200, "import")
	envelope, content := unpack(t, archive)
	if !strings.Contains(content, edited) || !strings.Contains(content, deptName) {
		t.Fatal("the archive lost the request reason or the department (backup would be useless)")
	}
	var headers strings.Builder
	for k, v := range ex.Header() {
		headers.WriteString(k + ": " + strings.Join(v, ",") + "\n")
	}

	e.a.Audit.Flush(ctx)
	audit := e.rows(t, "hr_audit_events")
	backupAudit := ""
	for _, line := range strings.Split(audit, "\n") {
		if strings.Contains(line, `"backup.`) {
			backupAudit += line + "\n"
		}
	}
	var events strings.Builder
	for _, tn := range []string{tenantA, tenantB} {
		entries, err := e.mem.XRange(ctx, stream.Key(tn), "", 100000)
		if err != nil {
			t.Fatal(err)
		}
		for _, en := range entries {
			if en.Fields["type"] == consumer.TypeCompleted {
				continue // the events this test published as the signing module
			}
			for k, v := range en.Fields {
				events.WriteString(k + "=" + v + "\n")
			}
		}
	}

	forbidden := []string{reason, notes, edited, review, reject, revoke, signReason, mariaName, petarName, hanaName,
		"LEAK-NAME", mariaMail, petarMail, hanaMail, deptName}
	places := map[string]string{
		"logs":            e.logText(),
		"audit":           audit,
		"events":          events.String(),
		"backup headers":  headers.String(),
		"backup envelope": envelope,
		"backup audit":    backupAudit,
	}
	for where, text := range places {
		if strings.TrimSpace(text) == "" {
			t.Errorf("%s: nothing captured (the check would be vacuous)", where)
		}
		for _, s := range forbidden {
			if strings.Contains(text, s) {
				t.Errorf("%q found in %s", s, where)
			}
		}
	}
	for _, want := range []string{`"request.create"`, `"request.approve"`, `"request.reject"`, `"request.revoke"`, `"request.signing_outcome"`,
		`"department.create"`, `"backup.export"`, `"backup.import"`, `"refused"`} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %s", want)
		}
	}
	if !strings.Contains(events.String(), "hr.request.changed") || !strings.Contains(events.String(), "hr.calendar.changed") {
		t.Errorf("no hr events captured: %s", events.String())
	}
	if !strings.Contains(e.logText(), "malformed") {
		t.Errorf("expected the consumer warning in the logs")
	}
	// The values are still visible to those who may see them.
	if r := e.json("GET", "/requests/"+r1, "maria", nil); !strings.Contains(r.Body.String(), edited) {
		t.Fatal("maria no longer sees her own reason")
	}
}

// unpack returns the archive's envelope (every top-level field except the
// row arrays) and its full JSON content.
func unpack(t *testing.T, archive []byte) (envelope, content string) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for k, v := range top {
		if len(v) > 0 && v[0] == '[' {
			continue
		}
		b.WriteString(k + "=" + string(v) + "\n")
	}
	return b.String(), string(raw)
}
