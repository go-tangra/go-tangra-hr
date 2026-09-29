package app

import (
	"context"
	"sync"

	"github.com/go-tangra/go-tangra-notification/sdk/v4/pkg/notifyclient"
)

// lazyNotify is the notification module client, dialled over SPIFFE mTLS on
// first use so hr starts while notification is down. A dial failure is
// reported as retryable (the outbox retries the mail).
type lazyNotify struct {
	app     *App
	service string
	mu      sync.Mutex
	c       *notifyclient.Client
}

func (l *lazyNotify) client(ctx context.Context) (*notifyclient.Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.c == nil {
		conn, err := l.app.Freya.Client(ctx, l.service)
		if err != nil {
			return nil, err
		}
		l.c = notifyclient.New(conn)
	}
	return l.c, nil
}

// SendKey sends one keyed notification through the notification module.
func (l *lazyNotify) SendKey(ctx context.Context, tenantID, key, recipient string, vars map[string]string, correlationID string) (notifyclient.Result, error) {
	c, err := l.client(ctx)
	if err != nil {
		l.app.Log.WarnContext(ctx, "notification client", "err", err)
		return notifyclient.Result{Retryable: true, Reason: "notification unreachable"}, nil
	}
	return c.SendKey(ctx, tenantID, key, recipient, vars, correlationID)
}
