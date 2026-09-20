package directory

import (
	"context"
	"net"

	"github.com/Ohgwen/on-netreg/internal/config"
)

// dialerFor bounds connection establishment by the configured timeout and
// the request context's deadline.
func dialerFor(ctx context.Context, cfg config.LDAPConfig) *net.Dialer {
	d := &net.Dialer{Timeout: cfg.Timeout}
	if dl, ok := ctx.Deadline(); ok {
		d.Deadline = dl
	}
	return d
}
