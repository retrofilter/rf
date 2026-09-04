package eval

import (
	"context"
	"time"
)

const interruptPoll = 50 * time.Millisecond

// InterruptContext derives a context that cancels when a pending interrupt is
// observed (polled every interruptPoll), so context-taking blocking calls
// abort on ^C. The returned stop releases the poller.
func InterruptContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(interruptPoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if Interrupted() {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}
