// Package demo provides synthetic Source and Destination implementations that
// drive the real transfer pipeline with fake data at a simulated rate. It backs
// `timberlake --demo`, which fills the UI with realistic, fully interactive
// state without touching the network — useful for working on the interface and
// for reproducing states (catch-up, slow transfers, failures) on demand.
package demo

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"sync"
	"time"

	"timberlake/transfer"
)

// Config tunes the synthetic workload.
type Config struct {
	Files        int           // how many items to invent
	AlreadyThere float64       // fraction that the destination already has (drives catch-up)
	BytesPerSec  int64         // simulated per-transfer throughput
	FailEvery    int           // fail every Nth transfer (0 = never)
	MinSize      int64         // smallest file
	MaxSize      int64         // largest file
	StatDelay    time.Duration // simulated round-trip for a destination check
}

// DefaultConfig mimics a large photogrammetry sync that has been resumed: most
// files are already uploaded, the rest are big and slow.
func DefaultConfig() Config {
	return Config{
		Files:        4000,
		AlreadyThere: 0.35,
		BytesPerSec:  3 << 20,
		FailEvery:    97,
		MinSize:      180 << 20,
		MaxSize:      260 << 20,
		StatDelay:    12 * time.Millisecond,
	}
}

// Source invents a tree of plausible-looking files.
type Source struct {
	cfg   Config
	items []transfer.Item
}

// NewSource builds the synthetic item list.
func NewSource(cfg Config) *Source {
	rng := rand.New(rand.NewSource(20260729)) //nolint:gosec // deterministic demo data
	dirs := []string{
		"1_The_Kings_Chamber/Photogrammetry",
		"2_West_Passage/Photogrammetry",
		"3_Antechamber/RTI",
		"4_The_Revenge_of_the_Queen/Photogrammetry",
	}
	items := make([]transfer.Item, 0, cfg.Files)
	for i := 0; i < cfg.Files; i++ {
		size := cfg.MinSize
		if cfg.MaxSize > cfg.MinSize {
			size += rng.Int63n(cfg.MaxSize - cfg.MinSize)
		}
		items = append(items, transfer.Item{
			RelativePath: fmt.Sprintf("%s/DSC%05d.tif", dirs[i%len(dirs)], 2000+i),
			Size:         size,
		})
	}
	return &Source{cfg: cfg, items: items}
}

func (s *Source) Describe() string { return "demo://synthetic-source" }
func (s *Source) Close() error     { return nil }

func (s *Source) Scan(_ context.Context, progress transfer.ScanProgress) ([]transfer.Item, error) {
	// Pretend the scan takes a moment so the scanning screen is visible.
	time.Sleep(600 * time.Millisecond)
	if progress != nil {
		var bytes int64
		for _, it := range s.items {
			bytes += it.Size
		}
		progress(int64(len(s.items)), bytes)
	}
	return s.items, nil
}

func (s *Source) Open(_ context.Context, item transfer.Item) (transfer.OpenFunc, error) {
	return func(offset int64) (io.ReadCloser, error) {
		remaining := item.Size - offset
		if remaining < 0 {
			remaining = 0
		}
		return io.NopCloser(io.LimitReader(zeroes{}, remaining)), nil
	}, nil
}

// zeroes is an endless reader; the demo never inspects the bytes.
type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) { return len(p), nil }

// Dest simulates a remote destination with latency and finite throughput.
type Dest struct {
	cfg Config

	mu   sync.Mutex
	have map[string]int64 // items the "previous run" already uploaded
	nth  int
}

// NewDest builds the synthetic destination.
func NewDest(cfg Config, items []transfer.Item) *Dest {
	have := make(map[string]int64)
	upTo := int(float64(len(items)) * cfg.AlreadyThere)
	for i := 0; i < upTo && i < len(items); i++ {
		have[items[i].RelativePath] = items[i].Size
	}
	return &Dest{cfg: cfg, have: have}
}

func (d *Dest) Describe() string { return "demo://synthetic-destination" }
func (d *Dest) Close() error     { return nil }

func (d *Dest) Stat(_ context.Context, item transfer.Item) (bool, int64, error) {
	time.Sleep(d.cfg.StatDelay)
	d.mu.Lock()
	defer d.mu.Unlock()
	if size, ok := d.have[item.RelativePath]; ok {
		return true, size, nil
	}
	return false, 0, nil
}

func (d *Dest) Put(ctx context.Context, item transfer.Item, _ transfer.OpenFunc, size int64, progress transfer.Progress) error {
	d.mu.Lock()
	d.nth++
	shouldFail := d.cfg.FailEvery > 0 && d.nth%d.cfg.FailEvery == 0
	d.mu.Unlock()

	// Buffered bytes race ahead of the wire, as they do for real.
	if progress != nil {
		progress(0, 0, min64(size, 16<<20))
	}

	const tick = 150 * time.Millisecond
	perTick := int64(float64(d.cfg.BytesPerSec) * tick.Seconds())
	if perTick <= 0 {
		perTick = 1 << 20
	}

	var committed int64
	for committed < size {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(tick):
		}
		committed = min64(size, committed+perTick)
		if shouldFail && committed > size/2 {
			return fmt.Errorf("simulated upload failure for %s", item.RelativePath)
		}
		if progress != nil {
			wire := min64(size, committed+perTick)
			progress(committed, wire, min64(size, wire+(16<<20)))
		}
	}
	if progress != nil {
		progress(size, size, size)
	}

	d.mu.Lock()
	d.have[item.RelativePath] = size
	d.mu.Unlock()
	return nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// Pair builds a matched synthetic source and destination.
func Pair(cfg Config) (*Source, *Dest) {
	src := NewSource(cfg)
	return src, NewDest(cfg, src.items)
}

// ParseSpec allows `--demo` to be tuned as "files=500,slow=1", mostly so the
// interface can be exercised at different scales.
func ParseSpec(spec string) Config {
	cfg := DefaultConfig()
	for _, part := range strings.Split(spec, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			continue
		}
		switch k {
		case "files":
			cfg.Files = n
		case "mbps":
			cfg.BytesPerSec = int64(n) << 20
		case "fail":
			cfg.FailEvery = n
		}
	}
	return cfg
}
