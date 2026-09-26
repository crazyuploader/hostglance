package server

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/crazyuploader/hostglance/internal/config"
	"github.com/crazyuploader/hostglance/internal/fetcher"
)

const (
	// pollerPrimeDelay is the delay before the second startup fetch. It gives
	// counter-based rates (CPU, network) a second sample soon after start
	// instead of waiting a whole refresh interval.
	pollerPrimeDelay = 30 * time.Second
)

// runPoller fetches all hosts at the configured refresh interval and
// broadcasts a reload to SSE clients after each fresh fetch. This makes the
// server the single source of refresh signals — request handlers never
// broadcast, so external API consumers cannot force browsers to reload.
//
// The interval is re-read from cfgPtr every cycle, so a SIGHUP config reload
// takes effect on the next tick without ticker juggling.
func runPoller(ctx context.Context, f *fetcher.Fetcher, hub *Hub, cfgPtr *atomic.Pointer[config.Config]) {
	refresh := func() {
		// Each HTTP request has its own timeout. A fixed whole-fleet deadline
		// would starve later hosts when discovery runs in bounded batches.
		f.Refresh(ctx)
		hub.broadcast()
		slog.Debug("poller refreshed metrics")
	}

	refresh()

	// Prime fetch: second sample for counter-based rates. Never wait longer
	// than the refresh interval itself.
	primeDelay := min(pollerPrimeDelay, cfgPtr.Load().Refresh)
	select {
	case <-ctx.Done():
		return
	case <-time.After(primeDelay):
		refresh()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfgPtr.Load().Refresh):
			refresh()
		}
	}
}
