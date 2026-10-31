package disks

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type slowProvider struct {
	active, maximum atomic.Int32
	reads           atomic.Int32
}

func (*slowProvider) Mock() bool { return true }
func (p *slowProvider) Read(ctx context.Context, d Drive) Sample {
	n := p.active.Add(1)
	defer p.active.Add(-1)
	for old := p.maximum.Load(); n > old; old = p.maximum.Load() {
		if p.maximum.CompareAndSwap(old, n) {
			break
		}
	}
	p.reads.Add(1)
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Millisecond):
	}
	return NewMock().Read(ctx, d)
}

func TestCollectorBoundsConcurrencyAndRefreshesChangedSelection(t *testing.T) {
	p := &slowProvider{}
	var mu sync.Mutex
	cfg := Config{SamplingSeconds: 60}
	for i := 0; i < 6; i++ {
		d := testDrive()
		d.ID = fmt.Sprintf("disk-%d", i)
		cfg.Drives = append(cfg.Drives, d)
	}
	c := NewCollector(p, func() Config { mu.Lock(); defer mu.Unlock(); return cfg })
	c.checkInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("collector did not update")
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait(func() bool { return len(c.Snapshot().Samples) == 6 })
	if p.maximum.Load() != 2 || p.reads.Load() != 6 {
		t.Fatalf("unexpected concurrency=%d reads=%d", p.maximum.Load(), p.reads.Load())
	}
	mu.Lock()
	cfg = Config{SamplingSeconds: 60, Drives: []Drive{testDrive()}}
	mu.Unlock()
	wait(func() bool { return len(c.Snapshot().Samples) == 1 })
	if p.reads.Load() != 7 {
		t.Fatalf("configuration change was not independently sampled: %d", p.reads.Load())
	}
}
