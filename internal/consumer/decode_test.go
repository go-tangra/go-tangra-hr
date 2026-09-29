package consumer

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

const sub = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c77"

func entry(typ, data string) stream.Entry {
	return stream.Entry{ID: "1-0", Fields: map[string]string{"type": typ, "data": data}}
}

func TestDecode(t *testing.T) {
	cases := []struct {
		e       stream.Entry
		outcome string
		ok      bool
		err     error
	}{
		{entry(TypeCompleted, `{"submission_id":"`+sub+`","template_id":"t","final_version":2,"audit_trail":true}`), store.OutcomeCompleted, true, nil},
		{entry(TypeExpired, `{"submission_id":"`+sub+`"}`), store.OutcomeExpired, true, nil},
		{entry(TypeCancelled, `{"submission_id":"`+sub+`","reason_code":"declined"}`), store.OutcomeDeclined, true, nil},
		{entry(TypeCancelled, `{"submission_id":"`+sub+`","reason_code":"cancelled"}`), store.OutcomeCancelled, true, nil},
		{entry(TypeCancelled, `{"submission_id":"`+sub+`"}`), store.OutcomeCancelled, true, nil},
		{entry("hr.request.changed", `{}`), "", false, nil},
		{entry("signing.inbox", `{}`), "", false, nil},
		{entry(TypeCancelled, `{"submission_id":"`+sub+`","reason_code":"weird"}`), "", false, ErrMalformed},
		{entry(TypeCompleted, `{"submission_id":"not-a-uuid"}`), "", false, ErrMalformed},
		{entry(TypeCompleted, `{"submission_id":"`+sub+`"}{"x":1}`), "", false, ErrMalformed},
		{entry(TypeCompleted, `[1]`), "", false, ErrMalformed},
		{entry(TypeCompleted, ``), "", false, ErrMalformed},
		{entry(TypeCompleted, `{"submission_id":"`+sub+`","pad":"`+strings.Repeat("x", MaxData)+`"}`), "", false, ErrMalformed},
	}
	for i, c := range cases {
		outcome, id, ok, err := Decode(c.e)
		if outcome != c.outcome || ok != c.ok || !errors.Is(err, c.err) || (ok && id != sub) {
			t.Errorf("case %d: %q %q %v %v", i, outcome, id, ok, err)
		}
	}
}

// FuzzDecode feeds arbitrary data to the decoder: it never panics and an
// accepted outcome always has a uuid submission id.
func FuzzDecode(f *testing.F) {
	f.Add(TypeCompleted, `{"submission_id":"`+sub+`"}`)
	f.Add(TypeCancelled, `{"submission_id":"`+sub+`","reason_code":"declined"}`)
	f.Add("x", "{")
	f.Fuzz(func(t *testing.T, typ, data string) {
		_, id, ok, _ := Decode(entry(typ, data))
		if ok && !uuidRE.MatchString(id) {
			t.Fatalf("accepted %q", id)
		}
	})
}
