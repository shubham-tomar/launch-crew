package cards

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"sync"
	"sync/atomic"
	"testing"
)

// Copies decode whole images; on a 512 MB server two at once can be too
// many, so they take turns.
func TestBlueskyCopyRunsOneAtATime(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	var running, peak atomic.Int32
	var ready sync.WaitGroup
	ready.Add(1)
	testCopyHook = func() {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		ready.Wait() // hold the slot until every goroutine has started
		running.Add(-1)
	}
	t.Cleanup(func() { testCopyHook = nil })

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := BlueskyCopy(context.Background(), buf.Bytes()); err != nil {
				t.Error(err)
			}
		}()
	}
	ready.Done()
	wg.Wait()
	if peak.Load() != 1 {
		t.Errorf("peak concurrent copies = %d, want 1", peak.Load())
	}
}

// A caller that gives up while waiting for the slot gets its context error.
func TestBlueskyCopyWaitHonoursContext(t *testing.T) {
	copySlot <- struct{}{}
	defer func() { <-copySlot }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := BlueskyCopy(ctx, []byte("x")); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
