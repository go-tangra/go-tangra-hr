package people

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
)

type countingDir struct {
	Fake
	calls int
}

func (c *countingDir) Members(ctx context.Context, tenant string) ([]Member, error) {
	c.calls++
	return c.Fake.Members(ctx, tenant)
}

func TestCache(t *testing.T) {
	dir := &countingDir{Fake: Fake{Users: map[string][]Contact{tenant: {{UserID: "hana", DisplayName: "Hana"}, {UserID: "maria", DisplayName: "Maria"}}}}}
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	c := &Cache{Dir: dir, Checker: authz.Static{"hana": {authz.Manage}}, Now: func() time.Time { return now }}
	ctx := context.Background()
	ms, err := c.Members(ctx, tenant)
	if err != nil || len(ms) != 2 {
		t.Fatal(err)
	}
	if ok, _ := c.Active(ctx, tenant, "maria"); !ok {
		t.Fatal("active")
	}
	if ok, _ := c.Active(ctx, tenant, "gone"); ok {
		t.Fatal("inactive")
	}
	names, _ := c.Names(ctx, tenant, []string{"maria", "gone"})
	if names["maria"] != "Maria" || len(names) != 1 {
		t.Fatal("names")
	}
	if dir.calls != 1 {
		t.Fatalf("cached calls = %d", dir.calls)
	}
	admins, err := c.Admins(ctx, tenant)
	if err != nil || len(admins) != 1 || admins[0] != "hana" {
		t.Fatalf("admins: %v %v", admins, err)
	}
	_, _ = c.Admins(ctx, tenant) // cached

	// Expired: refresh; failed refresh within 2×TTL serves stale data.
	now = now.Add(90 * time.Second)
	dir.Err = errors.New("down")
	if ms, err := c.Members(ctx, tenant); err != nil || len(ms) != 2 {
		t.Fatal("stale serve")
	}
	now = now.Add(time.Hour)
	if _, err := c.Members(ctx, tenant); err == nil {
		t.Fatal("too stale served")
	}
	if _, err := c.Active(ctx, tenant, "x"); err == nil {
		t.Fatal("active error")
	}
	if _, err := c.Names(ctx, tenant, nil); err == nil {
		t.Fatal("names error")
	}
	if _, err := c.Admins(ctx, tenant); err == nil {
		t.Fatal("admins error")
	}
	dir.Err = nil
	c.Forget(tenant)
	if ms, _ := c.Members(ctx, tenant); len(ms) != 2 {
		t.Fatal("after forget")
	}
	d := &Cache{}
	if d.ttl() != time.Minute || d.adminTTL() != 5*time.Minute || d.now().IsZero() {
		t.Fatal("defaults")
	}
}
