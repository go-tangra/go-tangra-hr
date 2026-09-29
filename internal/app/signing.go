package app

import (
	"context"
	"sync"

	"github.com/go-tangra/go-tangra-signing/sdk/v4/pkg/signingclient"

	"github.com/go-tangra/go-tangra-hr/v4/internal/config"
	"github.com/go-tangra/go-tangra-hr/v4/internal/signing"
)

// signingModule returns the dialer of the signing module's module API: the
// override (tests), or a client dialled over SPIFFE mTLS on first use so hr
// starts while signing is down.
func (a *App) signingModule(cfg config.Config, override signing.Module) func(ctx context.Context) (signing.Module, error) {
	if override != nil {
		return func(context.Context) (signing.Module, error) { return override, nil }
	}
	var mu sync.Mutex
	var c *signingclient.Client
	return func(ctx context.Context) (signing.Module, error) {
		mu.Lock()
		defer mu.Unlock()
		if c == nil {
			conn, err := a.Freya.Client(ctx, cfg.Signing.Service)
			if err != nil {
				return nil, err
			}
			c = signingclient.New(conn)
		}
		return c, nil
	}
}
