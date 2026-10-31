package disks

import (
	"context"
	"reflect"
	"sync"
	"time"
)

// Collector owns a separate worker. Slow disk/controller IO never blocks GPU
// control or IPMI restoration. A complete scan uses at most two subprocesses.
type Collector struct {
	provider      Provider
	configuration func() Config
	mu            sync.RWMutex
	snapshot      Snapshot
	previous      map[Drive]Sample
	checkInterval time.Duration
}

func NewCollector(p Provider, configuration func() Config) *Collector {
	return &Collector{provider: p, configuration: configuration, snapshot: Snapshot{Enabled: true, Mock: p.Mock(), Samples: []Sample{}}, previous: map[Drive]Sample{}, checkInterval: time.Second}
}

func cloneSample(s Sample) Sample {
	if s.TemperatureC != nil {
		t := *s.TemperatureC
		s.TemperatureC = &t
	}
	return s
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := c.snapshot
	out.Samples = make([]Sample, len(c.snapshot.Samples))
	for i, s := range c.snapshot.Samples {
		out.Samples[i] = cloneSample(s)
	}
	return out
}

func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.checkInterval)
	defer ticker.Stop()
	var prior Config
	var next time.Time
	for {
		if ctx.Err() != nil {
			return
		}
		cfg := c.configuration()
		cfg.Drives = append([]Drive(nil), cfg.Drives...)
		if next.IsZero() || !reflect.DeepEqual(cfg, prior) || !time.Now().Before(next) {
			c.collect(ctx, cfg)
			prior = cfg
			seconds := cfg.SamplingSeconds
			if seconds < 1 {
				seconds = 60
			}
			next = time.Now().Add(time.Duration(seconds) * time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Collector) collect(ctx context.Context, cfg Config) {
	// Callers validate configuration, but cap work here as a defense in depth.
	drives := cfg.Drives
	if len(drives) > 32 {
		drives = drives[:32]
	}
	type result struct {
		drive  Drive
		sample Sample
	}
	jobs := make(chan Drive)
	results := make(chan result, len(drives))
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for d := range jobs {
				results <- result{d, c.provider.Read(ctx, d)}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, d := range drives {
			select {
			case jobs <- d:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	next := map[Drive]Sample{}
	for r := range results {
		s := r.sample
		if s.Status != "ok" {
			if old, ok := c.previous[r.drive]; ok {
				s.LastValidAt = old.LastValidAt
				s.TemperatureC = cloneSample(old).TemperatureC
			}
		}
		next[r.drive] = s
	}
	c.previous = next
	samples := make([]Sample, 0, len(drives))
	for _, d := range drives {
		if s, ok := next[d]; ok {
			samples = append(samples, cloneSample(s))
		}
	}
	c.mu.Lock()
	c.snapshot.Samples, c.snapshot.UpdatedAt = samples, time.Now().UTC()
	c.mu.Unlock()
}
