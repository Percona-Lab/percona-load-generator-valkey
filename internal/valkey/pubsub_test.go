package valkey

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
	"github.com/Percona-Lab/percona-load-generator-valkey/internal/stats"
)

// broker is an in-memory pub/sub: Subscribe registers a handler and blocks;
// Exec of PUBLISH/SPUBLISH delivers to every handler subscribed to the channel.
type broker struct {
	*fakeClient
	mu         sync.Mutex
	subs       map[string][]func(channel, payload string)
	sharded    bool
	subErr     error
	deaf       bool          // subscribers never receive anything (simulates no delivery)
	lossEvery  int           // drop every Nth delivered message
	delay      time.Duration // delivery delay
	n          int
	subCalls   int
	neverReady bool // the subscription is never confirmed
	// confirmAfter delays the confirmation of each subscription.
	confirmAfter         time.Duration
	ready                int
	publishedBeforeReady int
}

func newBroker() *broker {
	return &broker{fakeClient: newFake(), subs: map[string][]func(string, string){}}
}

func (b *broker) Subscribe(ctx context.Context, sharded bool, channels []string, fn func(string, string), onReady func()) error {
	b.mu.Lock()
	b.subCalls++
	b.sharded = sharded
	if b.subErr != nil {
		b.mu.Unlock()
		return b.subErr
	}
	for _, ch := range channels {
		b.subs[ch] = append(b.subs[ch], fn)
	}
	b.mu.Unlock()
	if onReady != nil && !b.neverReady {
		time.Sleep(b.confirmAfter)
		b.mu.Lock()
		b.ready++
		b.mu.Unlock()
		onReady()
	}
	<-ctx.Done()
	return nil
}

func (b *broker) Exec(ctx context.Context, cmds []Cmd) []Result {
	res := b.fakeClient.Exec(ctx, cmds)
	for _, c := range cmds {
		if c[0] != "PUBLISH" && c[0] != "SPUBLISH" {
			continue
		}
		b.mu.Lock()
		if b.ready < b.subCalls {
			b.publishedBeforeReady++
		}
		hs := append([]func(string, string){}, b.subs[c[1]]...)
		deaf, delay := b.deaf, b.delay
		b.mu.Unlock()
		for _, h := range hs {
			b.mu.Lock()
			b.n++
			drop := b.lossEvery > 0 && b.n%b.lossEvery == 0
			b.mu.Unlock()
			if deaf || drop {
				continue
			}
			go func(h func(string, string), ch, p string) {
				if delay > 0 {
					time.Sleep(delay)
				}
				h(ch, p)
			}(h, c[1], c[2])
		}
	}
	return res
}

func psCfg(subs, channels int, sharded bool) config.Config {
	cfg := testCfg()
	cfg.Workload.PubSub = config.PubSub{Subscribers: subs, Channels: channels, Sharded: sharded}
	cfg.Workload.Mix = []config.MixItem{{Op: "PUBLISH", Weight: 1}}
	if sharded {
		cfg.Workload.Mix = []config.MixItem{{Op: "SPUBLISH", Weight: 1}}
	}
	cfg.Run.Duration = 300 * time.Millisecond
	cfg.Run.Concurrency = 4
	return cfg
}

func TestPubSubDeliversEveryMessageToEverySubscriber(t *testing.T) {
	b := newBroker()
	b.delay = 20 * time.Millisecond
	cfg := psCfg(3, 4, false)
	res, err := Run(context.Background(), b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ps := res.PubSub
	if ps == nil || ps.Subscribers != 3 || ps.Channels != 4 || ps.Sharded {
		t.Fatalf("%+v", ps)
	}
	if ps.Published == 0 || ps.Published != res.Ops-res.Errors {
		t.Fatalf("published %d of %d ops", ps.Published, res.Ops)
	}
	if ps.Expected != ps.Published*3 || ps.Delivered != ps.Expected {
		t.Fatalf("every subscriber listens to every channel: published %d, expected %d, delivered %d", ps.Published, ps.Expected, ps.Delivered)
	}
	if res.DeliveryHist.Count() != ps.Delivered {
		t.Fatalf("one latency per delivery: %d vs %d", res.DeliveryHist.Count(), ps.Delivered)
	}
	if p := res.DeliveryHist.Quantile(0.5); p < 20*time.Millisecond || p > 200*time.Millisecond {
		t.Fatalf("delivery latency %v should include the 20ms delay", p)
	}
	var delivered uint64
	for _, s := range res.Series {
		delivered += s.Delivered
	}
	if delivered == 0 {
		t.Fatal("the series carries deliveries per slice")
	}
	if b.subCalls != 3 {
		t.Fatalf("one subscription per subscriber, got %d", b.subCalls)
	}
}

// Regression: readiness used to be probed with published messages. Subscribers share
// the client's connection, so probes sent while another subscriber was still
// subscribing filled its buffer, stalled the connection and hung the run. Nothing
// may be published before every subscription is confirmed.
func TestPubSubPublishesNothingBeforeEverySubscriberIsConfirmed(t *testing.T) {
	b := newBroker()
	b.confirmAfter = 150 * time.Millisecond
	cfg := psCfg(3, 2, false)
	cfg.Run.Duration = 100 * time.Millisecond
	res, err := Run(context.Background(), b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if b.publishedBeforeReady != 0 {
		t.Fatalf("%d messages were published before the subscribers were confirmed", b.publishedBeforeReady)
	}
	if res.PubSub.Delivered != res.PubSub.Published*3 {
		t.Fatalf("%+v", res.PubSub)
	}
}

func TestPubSubLossIsVisible(t *testing.T) {
	b := newBroker()
	b.lossEvery = 4 // a quarter of the deliveries vanish
	res, err := Run(context.Background(), b, psCfg(2, 2, false))
	if err != nil {
		t.Fatal(err)
	}
	ps := res.PubSub
	if ps.Delivered >= ps.Expected || float64(ps.Delivered) < 0.6*float64(ps.Expected) || float64(ps.Delivered) > 0.9*float64(ps.Expected) {
		t.Fatalf("expected about 75%% delivered: %+v", ps)
	}
}

func TestShardedPubSubSubscribesPerChannel(t *testing.T) {
	b := newBroker()
	res, err := Run(context.Background(), b, psCfg(2, 3, true))
	if err != nil {
		t.Fatal(err)
	}
	if !b.sharded || b.subCalls != 2*3 {
		t.Fatalf("sharded: %v, subscriptions %d (shard channels of different slots cannot share a subscription)", b.sharded, b.subCalls)
	}
	if res.PubSub.Delivered != res.PubSub.Published*2 {
		t.Fatalf("%+v", res.PubSub)
	}
	for ch := range b.subs {
		if !strings.Contains(ch, "{") {
			t.Fatalf("shard channel %q needs a hash tag", ch)
		}
	}
}

func TestPubSubSubscribersThatNeverReceiveFailTheRun(t *testing.T) {
	defer func(d time.Duration) { subscribeTimeout = d }(subscribeTimeout)
	subscribeTimeout = 300 * time.Millisecond
	b := newBroker()
	b.neverReady = true
	start := time.Now()
	_, err := Run(context.Background(), b, psCfg(2, 2, false))
	if err == nil || !strings.Contains(err.Error(), "did not start receiving") {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatal("must give up at the subscribe timeout")
	}
}

func TestPubSubSubscribeErrorIsReported(t *testing.T) {
	b := newBroker()
	b.subErr = errors.New("NOPERM this user has no permissions to run the 'subscribe' command")
	_, err := Run(context.Background(), b, psCfg(1, 1, false))
	if err == nil || !strings.Contains(err.Error(), "NOPERM") || !strings.Contains(err.Error(), "could not subscribe") {
		t.Fatalf("%v", err)
	}
}

func TestPubSubOffByDefault(t *testing.T) {
	b := newBroker()
	cfg := psCfg(0, 4, false)
	res, err := Run(context.Background(), b, cfg)
	if err != nil || res.PubSub != nil || b.subCalls != 0 {
		t.Fatalf("publishing with no subscribers starts none: %v %+v %d", err, res.PubSub, b.subCalls)
	}
	if res.Ops == 0 {
		t.Fatal("the publishes still run")
	}
}

func TestPubSubStopsWithTheRun(t *testing.T) {
	b := newBroker()
	cfg := psCfg(2, 2, false)
	cfg.Run.Duration = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	done := make(chan struct{})
	go func() { _, _ = Run(ctx, b, cfg); close(done) }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("a canceled run must stop its subscribers")
	}
}

func TestDeliveryStatsInTheCollector(t *testing.T) {
	col := stats.NewCollector([]string{"PUBLISH"}, 1, time.Now())
	for i := 1; i <= 100; i++ {
		col.RecordDelivery(time.Duration(i) * time.Millisecond)
	}
	s := col.Snapshot(time.Now().Add(time.Second))
	if s.Delivered != 100 || s.DeliveryP50 < 45*time.Millisecond || s.DeliveryP50 > 56*time.Millisecond || s.DeliveryP99 < 95*time.Millisecond {
		t.Fatalf("%+v", s)
	}
	col.RecordDelivery(5 * time.Millisecond) // after the last slice closed
	if tot := col.Totals(); tot.DeliveryHist.Count() != 101 {
		t.Fatalf("totals include late deliveries: %d", tot.DeliveryHist.Count())
	}
}
