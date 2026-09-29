package signing

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-signing/sdk/v4/pkg/signingclient"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/requests"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

var ctx = context.Background()

type fake struct {
	err      error
	created  signingclient.CreateInput
	state    signingclient.State
	stateErr error
	docErr   error
	calls    []string
}

func (f *fake) ListTemplates(context.Context, string) ([]signingclient.Template, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []signingclient.Template{{ID: "t1", Name: "Leave form",
		Parties: []signingclient.Party{{ID: "p1", Name: "Employee"}, {ID: "p2", Name: "Approver"}},
		Fields:  []signingclient.Field{{ID: "name", Type: "text", Party: "p1", TextValued: true}, {ID: "days", Type: "text", Party: "p1", TextValued: true}}}}, nil
}

func (f *fake) CreateAndSend(_ context.Context, in signingclient.CreateInput) (string, error) {
	f.created = in
	return "sub-1", f.err
}

func (f *fake) GetSubmission(context.Context, string, string) (signingclient.State, error) {
	return f.state, f.stateErr
}

func (f *fake) Cancel(_ context.Context, _, id, reason string) error {
	f.calls = append(f.calls, "cancel "+id+" "+reason)
	return f.err
}

func (f *fake) Delete(_ context.Context, _, id string) error {
	f.calls = append(f.calls, "delete "+id)
	return f.err
}

func (f *fake) FinalDocument(context.Context, string, string) (io.ReadCloser, string, error) {
	if f.docErr != nil {
		return nil, "", f.docErr
	}
	return io.NopCloser(strings.NewReader("%PDF")), "leave.pdf", nil
}

func adapter(f *fake) *Adapter {
	return &Adapter{Dial: func(context.Context) (Module, error) { return f, nil },
		Now: func() time.Time { return time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC) }}
}

func sigErr(kind error, reason string) error { return &signingclient.Error{Kind: kind, Reason: reason} }

func TestTemplatesAndSettings(t *testing.T) {
	f := &fake{}
	a := adapter(f)
	ts, err := a.Templates(ctx, tn)
	if err != nil || len(ts) != 1 || ts[0].Parties[1].Name != "Approver" || !ts[0].Fields[0].TextValued {
		t.Fatalf("templates: %+v %v", ts, err)
	}
	s, err := a.CheckSettings(ctx, tn, store.SigningSettings{TemplateID: "t1", EmployeeParty: "p1", ApproverParty: "p2",
		Fields: map[string]string{"name": "employee_name"}})
	if err != nil || s.TemplateName != "Leave form" {
		t.Fatalf("settings: %+v %v", s, err)
	}
	if _, err := a.CheckSettings(ctx, tn, store.SigningSettings{TemplateID: "t1", EmployeeParty: "p1", ApproverParty: "p1"}); !errors.Is(err, apperr.SigningTemplateInvalid) {
		t.Fatal("invalid parties")
	}
	if _, err := a.CheckSettings(ctx, tn, store.SigningSettings{TemplateID: "gone"}); !errors.Is(err, apperr.SigningTemplateInvalid) {
		t.Fatal("unknown template")
	}
	f.err = sigErr(signingclient.ErrUnavailable, "down")
	if _, err := a.CheckSettings(ctx, tn, store.SigningSettings{TemplateID: "t1"}); !errors.Is(err, apperr.SigningUnavailable) {
		t.Fatal("module down")
	}
	down := &Adapter{Dial: func(context.Context) (Module, error) { return nil, errors.New("no route") }}
	if _, err := down.Templates(ctx, tn); !errors.Is(err, apperr.SigningUnavailable) {
		t.Fatal("dial error")
	}
	for _, fn := range []func() error{
		func() error {
			_, err := down.Start(ctx, requests.StartInput{Type: store.AbsenceType{Signing: &store.SigningSettings{}}})
			return err
		},
		func() error { return down.Cancel(ctx, tn, "s", "x") },
		func() error { return down.Delete(ctx, tn, "s") },
		func() error { _, err := down.State(ctx, tn, "s"); return err },
		func() error { _, _, err := down.Document(ctx, tn, "s"); return err },
	} {
		if !errors.Is(fn(), apperr.SigningUnavailable) {
			t.Fatal("dial error on call")
		}
	}
}

func TestStart(t *testing.T) {
	f := &fake{}
	a := adapter(f)
	in := requests.StartInput{TenantID: tn, ApproverID: "petar", EmployeeName: "Maria", ApproverName: "Petar", DepartmentName: "Platform",
		IdempotencyKey: "r1:1",
		Request: store.Request{ID: "r1", UserID: "maria", Start: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
			End: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), Days: 35, Reason: "Sea"},
		Type: store.AbsenceType{Name: "Annual leave", Signing: &store.SigningSettings{TemplateID: "t1", EmployeeParty: "p1", ApproverParty: "p2",
			Fields: map[string]string{"name": "employee_name", "days": "days"}}}}
	id, err := a.Start(ctx, in)
	c := f.created
	if err != nil || id != "sub-1" || c.SenderUserID != "petar" || c.Signers[0].UserID != "maria" || c.Signers[0].Party != "p1" ||
		c.Signers[1].UserID != "petar" || c.Prefill["name"] != "Maria" || c.Prefill["days"] != "3.5" || c.SourceRef != "r1" ||
		c.IdempotencyKey != "r1:1" || c.Name != "Maria — Annual leave 03.08.2026–07.08.2026" {
		t.Fatalf("start: %+v %v", c, err)
	}
	in.EmployeeName, in.Type.Name = "", strings.Repeat("x", 300)
	if _, err := a.Start(ctx, in); err != nil || len([]rune(f.created.Name)) != 200 {
		t.Fatal("long name trimmed")
	}
	for kind, want := range map[error]error{
		sigErr(signingclient.ErrInvalid, "invalid_signer"):           apperr.SignerInactive,
		sigErr(signingclient.ErrInvalid, "invalid_prefill"):          apperr.SigningTemplateInvalid,
		sigErr(signingclient.ErrPrecondition, "template_not_active"): apperr.SigningTemplateInvalid,
		sigErr(signingclient.ErrNotFound, "not_found"):               apperr.SigningTemplateInvalid,
		sigErr(signingclient.ErrDenied, "caller"):                    apperr.SigningUnavailable,
		errors.New("plain"): apperr.SigningUnavailable,
	} {
		f.err = kind
		if _, err := a.Start(ctx, in); !errors.Is(err, want) {
			t.Errorf("%v → %v", kind, err)
		}
	}
	in.Type.Signing = nil
	if _, err := a.Start(ctx, in); !errors.Is(err, apperr.SigningTemplateInvalid) {
		t.Fatal("no settings")
	}
	b := &Adapter{Dial: a.Dial}
	in.Type.Signing = &store.SigningSettings{}
	f.err = nil
	if _, err := b.Start(ctx, in); err != nil {
		t.Fatal("default clock")
	}
}

func TestStateCancelDeleteDocument(t *testing.T) {
	f := &fake{}
	a := adapter(f)
	for st, want := range map[signingclient.State]string{
		{Status: "in_progress"}:                              "",
		{Status: "completed"}:                                store.OutcomeCompleted,
		{Status: "expired"}:                                  store.OutcomeExpired,
		{Status: "cancelled", CancelReasonCode: "declined"}:  store.OutcomeDeclined,
		{Status: "cancelled", CancelReasonCode: "cancelled"}: store.OutcomeCancelled,
	} {
		f.state = st
		if got, err := a.State(ctx, tn, "s"); err != nil || got != want {
			t.Errorf("%+v → %q %v", st, got, err)
		}
	}
	f.stateErr = sigErr(signingclient.ErrNotFound, "not_found")
	if got, _ := a.State(ctx, tn, "s"); got != store.OutcomeCancelled {
		t.Fatal("vanished submission")
	}
	f.stateErr = sigErr(signingclient.ErrUnavailable, "down")
	if _, err := a.State(ctx, tn, "s"); !errors.Is(err, apperr.SigningUnavailable) {
		t.Fatal("state error")
	}
	if err := a.Cancel(ctx, tn, "s", "hr_cancelled"); err != nil || f.calls[0] != "cancel s hr_cancelled" {
		t.Fatal("cancel")
	}
	if err := a.Delete(ctx, tn, "s"); err != nil || f.calls[1] != "delete s" {
		t.Fatal("delete")
	}
	f.err = sigErr(signingclient.ErrNotFound, "not_found")
	if err := a.Cancel(ctx, tn, "s", "x"); err == nil {
		t.Fatal("cancel error")
	}
	if err := a.Delete(ctx, tn, "s"); err == nil {
		t.Fatal("delete error")
	}
	rc, name, err := a.Document(ctx, tn, "s")
	if err != nil || name != "leave.pdf" {
		t.Fatal(err)
	}
	_ = rc.Close()
	f.docErr = sigErr(signingclient.ErrPrecondition, "submission_not_open")
	if _, _, err := a.Document(ctx, tn, "s"); !errors.Is(err, apperr.NotSigned) {
		t.Fatal("not completed")
	}
	f.docErr = sigErr(signingclient.ErrUnavailable, "down")
	if _, _, err := a.Document(ctx, tn, "s"); !errors.Is(err, apperr.SigningUnavailable) {
		t.Fatal("download error")
	}
	if (&Adapter{Timeout: time.Second}).Timeout != time.Second {
		t.Fatal("timeout")
	}
	c, cancel := (&Adapter{Timeout: time.Second}).call(ctx)
	defer cancel()
	if _, ok := c.Deadline(); !ok {
		t.Fatal("deadline")
	}
}
