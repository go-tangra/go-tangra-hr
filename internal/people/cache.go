package people

import (
	"context"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
)

// Cache keeps each tenant's active member list (with names) and its HR
// administrators (members holding hr:manage) for a short time, so the
// calendar, pickers and routing do not ask auth on every request. A failed
// refresh keeps serving the last list until it is twice as old as the TTL.
type Cache struct {
	Dir      Directory
	Checker  authz.Checker // for Admins
	TTL      time.Duration // members (default 60 s)
	AdminTTL time.Duration // administrators (default 5 min)
	Now      func() time.Time

	mu      sync.Mutex
	members map[string]memberEntry
	admins  map[string]adminEntry
}

type memberEntry struct {
	at   time.Time
	list []Member
	byID map[string]Member
}

type adminEntry struct {
	at  time.Time
	ids []string
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return time.Minute
}

func (c *Cache) adminTTL() time.Duration {
	if c.AdminTTL > 0 {
		return c.AdminTTL
	}
	return 5 * time.Minute
}

func (c *Cache) entry(ctx context.Context, tenant string) (memberEntry, error) {
	c.mu.Lock()
	e, ok := c.members[tenant]
	c.mu.Unlock()
	now := c.now()
	if ok && now.Sub(e.at) < c.ttl() {
		return e, nil
	}
	list, err := c.Dir.Members(ctx, tenant)
	if err != nil {
		if ok && now.Sub(e.at) < 2*c.ttl() {
			return e, nil
		}
		return memberEntry{}, err
	}
	e = memberEntry{at: now, list: list, byID: make(map[string]Member, len(list))}
	for _, m := range list {
		e.byID[m.UserID] = m
	}
	c.mu.Lock()
	if c.members == nil {
		c.members = map[string]memberEntry{}
	}
	c.members[tenant] = e
	c.mu.Unlock()
	return e, nil
}

// Members returns the tenant's active members sorted by name.
func (c *Cache) Members(ctx context.Context, tenant string) ([]Member, error) {
	e, err := c.entry(ctx, tenant)
	return e.list, err
}

// Active reports whether userID is an active member of the tenant.
func (c *Cache) Active(ctx context.Context, tenant, userID string) (bool, error) {
	e, err := c.entry(ctx, tenant)
	if err != nil {
		return false, err
	}
	_, ok := e.byID[userID]
	return ok, nil
}

// Names returns display names of the given users that are active members.
func (c *Cache) Names(ctx context.Context, tenant string, ids []string) (map[string]string, error) {
	e, err := c.entry(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if m, ok := e.byID[id]; ok {
			out[id] = m.DisplayName
		}
	}
	return out, nil
}

// Admins returns the active members holding hr:manage (the routing fallback
// and the overdraw notice recipients), sorted by name.
func (c *Cache) Admins(ctx context.Context, tenant string) ([]string, error) {
	c.mu.Lock()
	a, ok := c.admins[tenant]
	c.mu.Unlock()
	now := c.now()
	if ok && now.Sub(a.at) < c.adminTTL() {
		return a.ids, nil
	}
	list, err := c.Members(ctx, tenant)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range list {
		if c.Checker != nil && c.Checker.Has(ctx, tenant, m.UserID, authz.Manage) {
			ids = append(ids, m.UserID)
		}
	}
	c.mu.Lock()
	if c.admins == nil {
		c.admins = map[string]adminEntry{}
	}
	c.admins[tenant] = adminEntry{at: now, ids: ids}
	c.mu.Unlock()
	return ids, nil
}

// Forget drops a tenant's cached lists (after a member sync).
func (c *Cache) Forget(tenant string) {
	c.mu.Lock()
	delete(c.members, tenant)
	delete(c.admins, tenant)
	c.mu.Unlock()
}
