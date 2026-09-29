package requests

import (
	"context"
	"net/url"

	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
)

// Notification system template keys (contracts/cross-module.md).
const (
	KeySubmitted = "hr.request_submitted"
	KeyApproved  = "hr.request_approved"
	KeyRejected  = "hr.request_rejected"
	KeyRevoked   = "hr.request_revoked"
	KeyFailed    = "hr.signing_failed"
	KeyOverdrawn = "hr.allowance_overdrawn"
)

// Keys lists every template key the module sends.
var Keys = []string{KeySubmitted, KeyApproved, KeyRejected, KeyRevoked, KeyFailed, KeyOverdrawn}

func date(t interface{ Format(string) string }) string { return t.Format("02.01.2006") }

func (s *Service) link(path string) string {
	u, err := url.JoinPath(s.d.Links.PortalBaseURL, path)
	if err != nil {
		return path
	}
	return u
}

// enqueue queues one e-mail to recipient inside tx (research D8). Variables
// are plain text: the notification templates escape them.
func (s *Service) enqueue(ctx context.Context, tx repo.Store, r store.Request, t store.AbsenceType, key, recipient string,
	names map[string]string, extra map[string]string) error {
	if recipient == "" {
		return nil
	}
	vars := map[string]string{
		"EmployeeName": names[r.UserID],
		"AbsenceType":  t.Name,
		"StartDate":    date(r.Start),
		"EndDate":      date(r.End),
		"Days":         r.Days.String(),
		"RequestURL":   s.link("/hr/requests/" + r.ID),
	}
	switch key {
	case KeySubmitted:
		vars["ApproverName"] = names[recipient]
		vars["Reason"] = r.Reason
		vars["ReviewURL"] = s.link("/hr/review")
	case KeyApproved, KeyRejected, KeyRevoked:
		vars["ReviewerName"] = names[r.ReviewedBy]
		vars["ReviewNotes"] = r.ReviewNotes
		vars["Reason"] = r.ReviewNotes
	case KeyFailed:
		vars["RecipientName"] = names[recipient]
	}
	for k, v := range extra {
		vars[k] = v
	}
	now := s.d.Now()
	return tx.EnqueueMail(ctx, store.Mail{ID: store.NewID(), TenantID: r.TenantID, Key: key, UserID: recipient, Vars: vars,
		NextAt: now, CreatedAt: now})
}
