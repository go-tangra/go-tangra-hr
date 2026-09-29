// Package apperr is the hr module's domain refusal: a stable reason from the
// closed vocabulary of the API (contracts/hr-api.md "Error codes"), its HTTP
// status, and optionally the offending field and a content-safe detail.
// Services return *Error for every expected refusal; the HTTP layer writes it
// verbatim. Anything else is an internal failure (503, logged).
package apperr

import (
	"errors"
	"net/http"
)

// Error is a domain refusal.
type Error struct {
	Status int
	Reason string
	Field  string         // offending field name or id (never its value)
	Detail map[string]any // content-safe detail (days, years, counts)
}

func (e *Error) Error() string {
	if e.Field != "" {
		return e.Reason + ": " + e.Field
	}
	return e.Reason
}

// Is matches errors of the same reason (errors.Is(err, apperr.Overlap)).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Reason == e.Reason
}

// WithField returns a copy naming the offending field.
func (e *Error) WithField(f string) *Error { c := *e; c.Field = f; return &c }

// WithDetail returns a copy with detail.
func (e *Error) WithDetail(d map[string]any) *Error { c := *e; c.Detail = d; return &c }

func r(status int, reason string) *Error { return &Error{Status: status, Reason: reason} }

// Refusals (the API's closed vocabulary).
var (
	NotFound               = r(http.StatusNotFound, "not_found")
	Forbidden              = r(http.StatusForbidden, "forbidden")
	Validation             = r(http.StatusUnprocessableEntity, "validation")
	Overlap                = r(http.StatusConflict, "overlap")
	InsufficientAllowance  = r(http.StatusConflict, "insufficient_allowance")
	NoAllowance            = r(http.StatusConflict, "no_allowance")
	ZeroDays               = r(http.StatusUnprocessableEntity, "zero_days")
	InvalidTransition      = r(http.StatusConflict, "invalid_transition")
	SelfReview             = r(http.StatusForbidden, "self_review")
	NotRouted              = r(http.StatusForbidden, "not_routed")
	InUse                  = r(http.StatusConflict, "in_use")
	Duplicate              = r(http.StatusConflict, "duplicate")
	NotEmpty               = r(http.StatusConflict, "not_empty")
	Cycle                  = r(http.StatusConflict, "cycle")
	SigningUnavailable     = r(http.StatusServiceUnavailable, "signing_unavailable")
	SigningTemplateInvalid = r(http.StatusUnprocessableEntity, "signing_template_invalid")
	SignerInactive         = r(http.StatusUnprocessableEntity, "signer_inactive")
	NotSigned              = r(http.StatusConflict, "not_signed")
	Conflict               = r(http.StatusConflict, "conflict")
	PayloadTooLarge        = r(http.StatusRequestEntityTooLarge, "payload_too_large")
	InvalidBackup          = r(http.StatusUnprocessableEntity, "invalid_backup")
	TemporarilyUnavailable = r(http.StatusServiceUnavailable, "temporarily_unavailable")
)

// As returns the *Error in err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
