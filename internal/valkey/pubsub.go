package valkey

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
)

// PubSubResult summarizes message delivery.
type PubSubResult struct {
	Subscribers int
	Channels    int
	Sharded     bool
	Published   uint64 // PUBLISH/SPUBLISH calls that succeeded
	Expected    uint64 // Published x Subscribers: every subscriber listens to every channel
	Delivered   uint64
}

// subscribeTimeout is how long the subscribers get to be confirmed by the server.
var subscribeTimeout = 10 * time.Second

// pubSub runs the subscribers of a run.
type pubSub struct {
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	delivered atomic.Uint64
	cfg       config.PubSub
	err       atomic.Value // first subscription error
}

// startPubSub connects the subscribers and waits until the server has confirmed
// every subscription. Nothing is published meanwhile: a subscriber that is still
// subscribing must not be flooded, because subscribers share the client's
// connection and a full subscriber buffer would stall the whole connection.
func startPubSub(ctx context.Context, c Client, cfg config.Config, col *stats.Collector) (*pubSub, error) {
	ps := cfg.Workload.PubSub
	pctx, cancel := context.WithCancel(ctx)
	p := &pubSub{cancel: cancel, cfg: ps}
	prefix := cfg.Dataset.KeyPrefix
	var ready atomic.Int64
	handler := func(_, payload string) {
		if i := strings.IndexByte(payload, '|'); i > 0 {
			if ts, err := strconv.ParseInt(payload[:i], 10, 64); err == nil {
				if d := time.Since(time.Unix(0, ts)); d >= 0 {
					col.RecordDelivery(d)
				}
			}
		}
		p.delivered.Add(1)
	}
	want := int64(0) // one confirmation per subscription call
	run := func(channels []string) {
		want++
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			if err := c.Subscribe(pctx, ps.Sharded, channels, handler, func() { ready.Add(1) }); err != nil && pctx.Err() == nil {
				p.err.CompareAndSwap(nil, err)
			}
		}()
	}
	for s := 0; s < ps.Subscribers; s++ {
		if ps.Sharded {
			// Shard channels of different slots cannot share one subscription.
			for i := 0; i < ps.Channels; i++ {
				run([]string{ChannelName(prefix, i, true)})
			}
		} else {
			chs := make([]string, ps.Channels)
			for i := range chs {
				chs[i] = ChannelName(prefix, i, false)
			}
			run(chs)
		}
	}
	deadline := time.Now().Add(subscribeTimeout)
	for ready.Load() < want {
		if e, _ := p.err.Load().(error); e != nil {
			cancel()
			p.wg.Wait()
			return nil, fmt.Errorf("pub/sub subscribers could not subscribe: %w", e)
		}
		if time.Now().After(deadline) {
			cancel()
			p.wg.Wait()
			return nil, fmt.Errorf("pub/sub subscribers did not start receiving within %v", subscribeTimeout)
		}
		select {
		case <-ctx.Done():
			cancel()
			p.wg.Wait()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return p, nil
}

// finish waits for in-flight messages (until the count reaches expected or
// stops growing), then stops the subscribers.
func (p *pubSub) finish(expected uint64) PubSubResult {
	last, still := p.delivered.Load(), 0
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		n := p.delivered.Load()
		if n >= expected {
			break
		}
		if n == last {
			if still++; still >= 6 { // 300ms with nothing new
				break
			}
		} else {
			last, still = n, 0
		}
	}
	p.cancel()
	p.wg.Wait()
	return PubSubResult{Subscribers: p.cfg.Subscribers, Channels: p.cfg.Channels, Sharded: p.cfg.Sharded, Delivered: p.delivered.Load()}
}
