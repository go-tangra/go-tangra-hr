package consumer

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

// Signing event types consumed (contracts/cross-module.md).
const (
	TypeCompleted = "signing.submission.completed"
	TypeCancelled = "signing.submission.cancelled"
	TypeExpired   = "signing.submission.expired"
)

// MaxData bounds an event's data field.
const MaxData = 4 << 10

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ErrMalformed marks a signing event that could not be decoded (skipped).
var ErrMalformed = errors.New("consumer: malformed event")

type payload struct {
	SubmissionID string `json:"submission_id"`
	ReasonCode   string `json:"reason_code"`
}

// Decode turns a stream entry into a signing outcome. ok is false for events
// of other types (not an error); a signing event that is malformed returns
// ErrMalformed.
func Decode(e stream.Entry) (outcome, submissionID string, ok bool, err error) {
	switch e.Fields["type"] {
	case TypeCompleted, TypeCancelled, TypeExpired:
	default:
		return "", "", false, nil
	}
	data := e.Fields["data"]
	if len(data) == 0 || len(data) > MaxData {
		return "", "", false, ErrMalformed
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(data)))
	var p payload
	if err := dec.Decode(&p); err != nil || dec.More() {
		return "", "", false, ErrMalformed
	}
	if !uuidRE.MatchString(p.SubmissionID) {
		return "", "", false, ErrMalformed
	}
	switch e.Fields["type"] {
	case TypeCompleted:
		return store.OutcomeCompleted, p.SubmissionID, true, nil
	case TypeExpired:
		return store.OutcomeExpired, p.SubmissionID, true, nil
	}
	switch p.ReasonCode {
	case "declined":
		return store.OutcomeDeclined, p.SubmissionID, true, nil
	case "cancelled", "":
		return store.OutcomeCancelled, p.SubmissionID, true, nil
	}
	return "", "", false, ErrMalformed
}
