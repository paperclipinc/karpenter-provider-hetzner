package instancetype

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// blockingServerTypeClient answers All only once the test releases it, and
// records how many calls are in flight. It is the shape of a slow or hung
// hcloud API: the request is outstanding, not failed.
type blockingServerTypeClient struct {
	types    []*hcloud.ServerType
	err      error
	release  chan struct{}
	entered  chan struct{}
	calls    int
	callsMu  chan struct{} // used as a 1-slot mutex so calls is race-free
	blocking bool
}

func newBlockingClient(types []*hcloud.ServerType) *blockingServerTypeClient {
	c := &blockingServerTypeClient{
		types:   types,
		release: make(chan struct{}),
		entered: make(chan struct{}, 16),
		callsMu: make(chan struct{}, 1),
	}
	c.callsMu <- struct{}{}
	return c
}

func (c *blockingServerTypeClient) All(_ context.Context) ([]*hcloud.ServerType, error) {
	<-c.callsMu
	c.calls++
	blocking := c.blocking
	c.callsMu <- struct{}{}

	if blocking {
		c.entered <- struct{}{}
		<-c.release
	}
	return c.types, c.err
}

func (c *blockingServerTypeClient) callCount() int {
	<-c.callsMu
	n := c.calls
	c.callsMu <- struct{}{}
	return n
}

func testTypes() []*hcloud.ServerType {
	return []*hcloud.ServerType{
		makeServerType("cx22", hcloud.ArchitectureX86, hcloud.CPUTypeShared, 2, 4, 40, testPricings),
	}
}

// warm fetches once so the provider has a catalogue, and pins the clock so the
// test controls expiry rather than the wall clock.
func warm(t *testing.T, p *Provider, now *time.Time) {
	t.Helper()
	p.nowFn = func() time.Time { return *now }
	if _, err := p.List(context.Background(), nil); err != nil {
		t.Fatalf("warming the cache: %v", err)
	}
}

// TestList_WarmCacheIsNotBlockedByAnInFlightRefresh is the regression for the
// lock scope. The catalogue is read on every provisioning decision, and the
// refresh used to happen with the cache's own lock held — so one slow or hung
// hcloud request stalled every caller, including the overwhelming majority
// whose cache was warm and who needed no network at all. From the outside that
// looks like Karpenter having stopped, not like an API problem.
func TestList_WarmCacheIsNotBlockedByAnInFlightRefresh(t *testing.T) {
	now := time.Now()
	client := newBlockingClient(testTypes())
	p := NewProvider(client)
	warm(t, p, &now)

	// A second provider view of the same clock: expire the cache so the next
	// caller refreshes, then hold that refresh open.
	client.blocking = true
	expired := now.Add(cacheTTL + time.Minute)
	p.nowFn = func() time.Time { return expired }

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		_, _ = p.List(context.Background(), nil)
	}()
	<-client.entered // the refresh is now inside the hcloud call

	// Un-expire the clock: this reader's cache is warm and it must be served
	// from memory rather than queueing behind the in-flight request.
	p.nowFn = func() time.Time { return now }

	served := make(chan error, 1)
	go func() {
		_, err := p.List(context.Background(), nil)
		served <- err
	}()

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("warm read failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(client.release)
		<-refreshDone
		t.Fatal("a warm-cache read blocked on an in-flight catalogue refresh: the cache lock is held across the hcloud call")
	}

	close(client.release)
	<-refreshDone
}

// A burst of concurrent misses must still make one API call, not one per
// caller: splitting the lock must not turn a thundering herd loose on hcloud.
func TestList_ConcurrentMissesMakeOneAPICall(t *testing.T) {
	client := newBlockingClient(testTypes())
	client.blocking = true
	p := NewProvider(client)

	const callers = 8
	done := make(chan error, callers)
	for range callers {
		go func() {
			_, err := p.List(context.Background(), nil)
			done <- err
		}()
	}

	<-client.entered // exactly one caller reached the API
	close(client.release)
	for range callers {
		if err := <-done; err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if got := client.callCount(); got != 1 {
		t.Errorf("hcloud was called %d times for %d concurrent misses, want 1", got, callers)
	}
}

// TestList_ServesStaleWhenTheRefreshFails is the regression for the missing
// fallback. Hetzner's server-type catalogue changes on the order of years, so a
// six-hour-old copy is not meaningfully less correct than a fresh one — while
// returning an error fails Create and GetInstanceTypes and stops the cluster
// provisioning at all. A transient 5xx must not be able to do that.
func TestList_ServesStaleWhenTheRefreshFails(t *testing.T) {
	now := time.Now()
	client := newBlockingClient(testTypes())
	p := NewProvider(client)
	warm(t, p, &now)

	client.err = errors.New("503 service unavailable")
	expired := now.Add(cacheTTL + time.Minute)
	p.nowFn = func() time.Time { return expired }

	types, err := p.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected the previous catalogue to be served, got error: %v", err)
	}
	if len(types) != 1 || types[0].Name != "cx22" {
		t.Fatalf("expected the previously fetched catalogue, got %+v", types)
	}
}

// Serving stale must not extend the TTL, or one failed refresh would settle the
// provider into the stale copy for another full cache lifetime.
func TestList_StaleServeDoesNotExtendTheTTL(t *testing.T) {
	now := time.Now()
	client := newBlockingClient(testTypes())
	p := NewProvider(client)
	warm(t, p, &now)

	client.err = errors.New("503 service unavailable")
	expired := now.Add(cacheTTL + time.Minute)
	p.nowFn = func() time.Time { return expired }

	if _, err := p.List(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	before := client.callCount()

	// The API is healthy again. The very next call must retry it, not keep
	// serving from memory.
	client.err = nil
	if _, err := p.List(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := client.callCount(); got != before+1 {
		t.Errorf("hcloud calls %d -> %d: a stale serve extended the TTL and suppressed the retry", before, got)
	}
}

// With nothing ever fetched there is nothing to fall back to, so the error has
// to surface. Returning an empty catalogue instead would report "no instance
// type satisfies requirements", sending operators to their NodePool
// requirements for what is an API outage.
func TestList_ColdCacheReturnsTheError(t *testing.T) {
	client := newBlockingClient(nil)
	client.err = errors.New("401 unauthorized")
	p := NewProvider(client)

	if _, err := p.List(context.Background(), nil); err == nil {
		t.Fatal("expected an error when the catalogue has never been fetched")
	}
}
