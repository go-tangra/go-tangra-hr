// Package consumer applies the signing module's submission outcomes to leave
// requests (spec FR-033, research D2). For every tenant known to hr it reads
// platform:events:<tenant> from a cursor persisted in hr_tenants, so events
// published while hr was down are replayed after a restart. Each outcome is
// applied and the cursor advanced in one transaction; outcomes are recorded
// once per submission, so a replay never applies twice. Events trimmed from
// the stream before hr read them are repaired by the reconcile task.
package consumer

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/repo"
	"github.com/go-tangra/go-tangra-hr/v4/internal/requests"
	"github.com/go-tangra/go-tangra-hr/v4/internal/store"
	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

// Applier applies an outcome inside a transaction (requests.Service).
type Applier interface {
	ApplyOutcome(ctx context.Context, tx repo.Store, o requests.Outcome) (*store.Request, error)
	Applied(ctx context.Context, r *store.Request)
}

// Consumer follows every tenant's stream.
type Consumer struct {
	Store   repo.Store
	Stream  stream.Client
	Apply   Applier
	Block   time.Duration // one XREAD wait (default 2 s)
	Batch   int           // entries per read (default 100)
	Refresh time.Duration // tenant registry re-read (default 30 s)
	Retry   time.Duration // wait after an error (default 1 s)
	Log     *slog.Logger
	Now     func() time.Time

	mu      sync.Mutex
	running map[string]bool
}

func (c *Consumer) defaults() {
	if c.Block <= 0 {
		c.Block = 2 * time.Second
	}
	if c.Batch <= 0 {
		c.Batch = 100
	}
	if c.Refresh <= 0 {
		c.Refresh = 30 * time.Second
	}
	if c.Retry <= 0 {
		c.Retry = time.Second
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Run follows the tenants until ctx ends (a worker of the app).
func (c *Consumer) Run(ctx context.Context) error {
	c.defaults()
	var wg sync.WaitGroup
	defer wg.Wait()
	t := time.NewTicker(c.Refresh)
	defer t.Stop()
	for {
		c.startTenants(ctx, &wg)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (c *Consumer) startTenants(ctx context.Context, wg *sync.WaitGroup) {
	tenants, err := c.Store.TenantsSystem(ctx)
	if err != nil {
		c.Log.Warn("consumer: tenant registry", "err", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running == nil {
		c.running = map[string]bool{}
	}
	for _, ts := range tenants {
		if c.running[ts.TenantID] {
			continue
		}
		c.running[ts.TenantID] = true
		wg.Add(1)
		go func(ts store.TenantState) {
			defer wg.Done()
			c.follow(ctx, ts)
		}(ts)
	}
}

func (c *Consumer) sleep(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(c.Retry):
		return true
	}
}

// follow reads one tenant's stream from its persisted cursor.
func (c *Consumer) follow(ctx context.Context, ts store.TenantState) {
	key := stream.Key(ts.TenantID)
	cursor := ts.StreamCursor
	for cursor == "" && ctx.Err() == nil {
		// First start for this tenant: begin at the tail (older outcomes are
		// the reconcile task's job) and persist it.
		last, err := c.Stream.XLast(ctx, key)
		if err == nil {
			if last == "" {
				last = "0-0"
			}
			if err = c.Store.SetCursor(ctx, ts.TenantID, last, c.Now()); err == nil {
				cursor = last
				break
			}
		}
		c.Log.Warn("consumer: initial cursor", "tenant", ts.TenantID, "err", err)
		if !c.sleep(ctx) {
			return
		}
	}
	for ctx.Err() == nil {
		next, err := c.Step(ctx, ts.TenantID, cursor)
		if err != nil {
			if ctx.Err() == nil {
				c.Log.Warn("consumer: step", "tenant", ts.TenantID, "err", err)
			}
			if !c.sleep(ctx) {
				return
			}
			continue
		}
		cursor = next
	}
}

// Step reads one batch after cursor and applies it; it returns the new
// cursor (unchanged when nothing arrived).
func (c *Consumer) Step(ctx context.Context, tenant, cursor string) (string, error) {
	c.defaults()
	entries, err := c.Stream.XRead(ctx, stream.Key(tenant), cursor, c.Block, int64(c.Batch))
	if err != nil {
		return cursor, err
	}
	skipped := ""
	for _, e := range entries {
		outcome, sub, ok, derr := Decode(e)
		if errors.Is(derr, ErrMalformed) {
			c.Log.Warn("consumer: malformed signing event skipped", "tenant", tenant, "id", e.ID)
		}
		if !ok {
			skipped = e.ID
			continue
		}
		var changed *store.Request
		err := c.Store.Tx(ctx, tenant, func(tx repo.Store) error {
			var err error
			changed, err = c.Apply.ApplyOutcome(ctx, tx, requests.Outcome{TenantID: tenant, SubmissionID: sub, Outcome: outcome,
				Source: store.SourceEvent, EventID: e.ID})
			if err != nil {
				return err
			}
			return tx.SetCursor(ctx, tenant, e.ID, c.Now())
		})
		if err != nil {
			if skipped != "" {
				_ = c.Store.SetCursor(ctx, tenant, skipped, c.Now())
				return skipped, err
			}
			return cursor, err
		}
		c.Apply.Applied(ctx, changed)
		cursor, skipped = e.ID, ""
	}
	if skipped != "" {
		if err := c.Store.SetCursor(ctx, tenant, skipped, c.Now()); err != nil {
			return cursor, err
		}
		cursor = skipped
	}
	return cursor, nil
}
