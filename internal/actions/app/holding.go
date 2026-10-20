package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jchen0426/agent-swarm/internal/holding"
)

// drainRecords is how many record timeouts a drain waits for the calls in
// the gateway's hands: as many as an outcome's tries take.
const drainRecords = recordAttempts

// errDraining answers a call that comes once the gateway drains.
var errDraining = fmt.Errorf("%w: the gateway is draining", ErrUnavailable)

// Drain stops taking calls, which it answers ErrUnavailable, and waits for
// the detached writes, up to 3 × RecordTimeout: until every call in the
// gateway's hands has ended, its writes apart from its context included
// (Plan 7d-1, Decision 6). A call that has not ended by then, or by the end of
// ctx, goes on alone, and Drain fails saying so: its writes may come once the
// holding is over, which refuses them.
func (g *Gateway) Drain(ctx context.Context) error {
	g.mu.Lock()
	g.closing = true
	g.settle()
	drained := g.drained
	g.mu.Unlock()
	bound := drainRecords * g.opts.RecordTimeout
	t := time.NewTimer(bound)
	defer t.Stop()
	select {
	case <-drained:
		return nil
	case <-t.C:
		return fmt.Errorf("app: calls still in the gateway's hands after %s", bound)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// enter counts a call in the gateway's hands, unless the gateway drains.
func (g *Gateway) enter() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return errDraining
	}
	g.busy++
	return nil
}

// leave uncounts a call.
func (g *Gateway) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy--
	g.settle()
}

// settle closes drained once the gateway drains and no call is in its hands.
// The caller holds g.mu.
func (g *Gateway) settle() {
	if !g.closing || g.busy > 0 {
		return
	}
	select {
	case <-g.drained:
	default:
		close(g.drained)
	}
}

// errUnheld stops a call whose request to a server the holding refused.
var errUnheld = fmt.Errorf("%w: the holding of the platform's lease does not hold, so nothing was sent", ErrUnavailable)

// holds asks the holding, before a request to a server, whether the holding
// the request acts for, whose epoch ctx carries, still holds (Plan 7d-1,
// Decision 4). A refusal is logged, never fences, and is returned.
func (g *Gateway) holds(ctx context.Context) error {
	if epoch, err := holding.EpochFrom(ctx); err == nil && g.p.Holding.Valid(epoch) {
		return nil
	}
	return g.holdingRefused(ctx)
}

// holdingRefused logs a holding's refusal of a request to a server, the
// gateway's own or the upstream's, and returns it as errUnheld.
func (g *Gateway) holdingRefused(ctx context.Context) error {
	epoch, _ := holding.EpochFrom(ctx)
	g.log.WarnContext(ctx, "holding refused", "epoch", epoch, "request", "mcp")
	return errUnheld
}

// unheld reports whether err stops a call because the holding it acts for is
// over: the holding refused a request to a server, or a guard refused a write
// (holding.ErrLeaseLost). Such a call records no failure, and is answered
// unavailable (Decision 20).
func unheld(err error) bool {
	return errors.Is(err, errUnheld) || errors.Is(err, holding.ErrLeaseLost)
}
