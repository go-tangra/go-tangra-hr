// Package signing adapts the signing module's module API
// (signing.v1.ModuleSubmissions, research D1) to the hr module: it starts the
// leave document of an approval (employee first, then the approver, with the
// mapped leave values prefilled), follows, cancels and deletes it, streams
// the signed PDF, lists the templates for the absence type settings and
// validates those settings. Signing refusals become hr refusals.
package signing

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/go-tangra/go-tangra-signing/sdk/v4/pkg/signingclient"

	"github.com/go-tangra/go-tangra-hr/v4/internal/apperr"
	"github.com/go-tangra/go-tangra-hr/v4/internal/requests"
	"github.com/go-tangra/go-tangra-hr/v4/internal/signingmap"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// Module is the signing module API (signingclient.Client or a test double).
type Module interface {
	ListTemplates(ctx context.Context, tenantID string) ([]signingclient.Template, error)
	CreateAndSend(ctx context.Context, in signingclient.CreateInput) (string, error)
	GetSubmission(ctx context.Context, tenantID, submissionID string) (signingclient.State, error)
	Cancel(ctx context.Context, tenantID, submissionID, reasonCode string) error
	Delete(ctx context.Context, tenantID, submissionID string) error
	FinalDocument(ctx context.Context, tenantID, submissionID string) (io.ReadCloser, string, error)
}

// Adapter implements requests.Signer and catalog.SigningChecker.
type Adapter struct {
	Dial    func(ctx context.Context) (Module, error)
	Now     func() time.Time
	Timeout time.Duration // per call (default 15 s; downloads are not bounded)
}

var (
	_ requests.Signer = (*Adapter)(nil)
)

func (a *Adapter) module(ctx context.Context) (Module, error) {
	m, err := a.Dial(ctx)
	if err != nil {
		return nil, apperr.SigningUnavailable
	}
	return m, nil
}

func (a *Adapter) call(ctx context.Context) (context.Context, context.CancelFunc) {
	t := a.Timeout
	if t <= 0 {
		t = 15 * time.Second
	}
	return context.WithTimeout(ctx, t)
}

// refusal maps a signing module error to an hr refusal.
func refusal(err error) error {
	var e *signingclient.Error
	if !errors.As(err, &e) {
		return apperr.SigningUnavailable
	}
	switch {
	case errors.Is(e, signingclient.ErrInvalid) && e.Reason == "invalid_signer":
		return apperr.SignerInactive
	case errors.Is(e, signingclient.ErrInvalid), errors.Is(e, signingclient.ErrPrecondition), errors.Is(e, signingclient.ErrNotFound):
		return apperr.SigningTemplateInvalid.WithDetail(map[string]any{"reason": e.Reason})
	}
	return apperr.SigningUnavailable
}

func convert(t signingclient.Template) signingmap.Template {
	out := signingmap.Template{ID: t.ID, Name: t.Name}
	for _, p := range t.Parties {
		out.Parties = append(out.Parties, signingmap.Party{ID: p.ID, Name: p.Name})
	}
	for _, f := range t.Fields {
		out.Fields = append(out.Fields, signingmap.Field{ID: f.ID, Name: f.Name, Type: f.Type, Party: f.Party, TextValued: f.TextValued})
	}
	return out
}

// Templates lists the tenant's active signing templates.
func (a *Adapter) Templates(ctx context.Context, tenantID string) ([]signingmap.Template, error) {
	m, err := a.module(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.call(ctx)
	defer cancel()
	list, err := m.ListTemplates(ctx, tenantID)
	if err != nil {
		return nil, apperr.SigningUnavailable
	}
	out := make([]signingmap.Template, 0, len(list))
	for _, t := range list {
		out = append(out, convert(t))
	}
	return out, nil
}

// CheckSettings validates an absence type's signing settings against the
// template (catalog.SigningChecker) and fills in the template name.
func (a *Adapter) CheckSettings(ctx context.Context, tenantID string, s store.SigningSettings) (store.SigningSettings, error) {
	list, err := a.Templates(ctx, tenantID)
	if err != nil {
		return s, err
	}
	for _, t := range list {
		if t.ID != s.TemplateID {
			continue
		}
		if err := signingmap.Validate(signingmap.Settings{EmployeeParty: s.EmployeeParty, ApproverParty: s.ApproverParty, Fields: s.Fields}, t); err != nil {
			return s, apperr.SigningTemplateInvalid.WithDetail(map[string]any{"reason": err.Error()})
		}
		s.TemplateName = t.Name
		return s, nil
	}
	return s, apperr.SigningTemplateInvalid.WithDetail(map[string]any{"reason": "template_not_active"})
}

// Start creates and sends the leave document of an approval.
func (a *Adapter) Start(ctx context.Context, in requests.StartInput) (string, error) {
	cfg := in.Type.Signing
	if cfg == nil {
		return "", apperr.SigningTemplateInvalid
	}
	m, err := a.module(ctx)
	if err != nil {
		return "", err
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	r := in.Request
	prefill := signingmap.Render(signingmap.Settings{EmployeeParty: cfg.EmployeeParty, ApproverParty: cfg.ApproverParty, Fields: cfg.Fields},
		signingmap.Values{EmployeeName: in.EmployeeName, Department: in.DepartmentName, AbsenceType: in.Type.Name, Start: r.Start, End: r.End,
			Days: r.Days, Reason: r.Reason, ApproverName: in.ApproverName, Today: now()})
	name := in.Type.Name + " " + r.Start.Format("02.01.2006") + "–" + r.End.Format("02.01.2006")
	if in.EmployeeName != "" {
		name = in.EmployeeName + " — " + name
	}
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	ctx, cancel := a.call(ctx)
	defer cancel()
	id, err := m.CreateAndSend(ctx, signingclient.CreateInput{TenantID: in.TenantID, TemplateID: cfg.TemplateID, SenderUserID: in.ApproverID,
		Name: name, Signers: []signingclient.Signer{{UserID: r.UserID, Party: cfg.EmployeeParty}, {UserID: in.ApproverID, Party: cfg.ApproverParty}},
		Prefill: prefill, SourceRef: r.ID, IdempotencyKey: in.IdempotencyKey})
	if err != nil {
		return "", refusal(err)
	}
	return id, nil
}

// Cancel cancels a running submission.
func (a *Adapter) Cancel(ctx context.Context, tenantID, submissionID, reasonCode string) error {
	m, err := a.module(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := a.call(ctx)
	defer cancel()
	if err := m.Cancel(ctx, tenantID, submissionID, reasonCode); err != nil {
		return refusal(err)
	}
	return nil
}

// Delete deletes a submission and its documents.
func (a *Adapter) Delete(ctx context.Context, tenantID, submissionID string) error {
	m, err := a.module(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := a.call(ctx)
	defer cancel()
	if err := m.Delete(ctx, tenantID, submissionID); err != nil {
		return refusal(err)
	}
	return nil
}

// State returns the terminal outcome of a submission (store.Outcome*), or ""
// while it is in progress. A submission the module no longer knows is
// reported cancelled.
func (a *Adapter) State(ctx context.Context, tenantID, submissionID string) (string, error) {
	m, err := a.module(ctx)
	if err != nil {
		return "", err
	}
	ctx, cancel := a.call(ctx)
	defer cancel()
	st, err := m.GetSubmission(ctx, tenantID, submissionID)
	if errors.Is(err, signingclient.ErrNotFound) {
		return store.OutcomeCancelled, nil
	}
	if err != nil {
		return "", apperr.SigningUnavailable
	}
	switch st.Status {
	case "completed":
		return store.OutcomeCompleted, nil
	case "expired":
		return store.OutcomeExpired, nil
	case "cancelled":
		if st.CancelReasonCode == "declined" {
			return store.OutcomeDeclined, nil
		}
		return store.OutcomeCancelled, nil
	}
	return "", nil
}

// Document streams the signed PDF of a completed submission.
func (a *Adapter) Document(ctx context.Context, tenantID, submissionID string) (io.ReadCloser, string, error) {
	m, err := a.module(ctx)
	if err != nil {
		return nil, "", err
	}
	rc, name, err := m.FinalDocument(ctx, tenantID, submissionID)
	if errors.Is(err, signingclient.ErrNotFound) || errors.Is(err, signingclient.ErrPrecondition) {
		return nil, "", apperr.NotSigned
	}
	if err != nil {
		return nil, "", apperr.SigningUnavailable
	}
	return rc, name, nil
}
