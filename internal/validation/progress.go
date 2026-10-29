package validation

import (
	"fmt"
	"math"
	"os"
	"sync"
	"time"
)

// Progress renders a simple text progress bar with ETA.
type Progress struct {
	mu       sync.Mutex
	phase    string
	total    int
	done     int
	start    time.Time
	last     time.Time
	width    int
}

// NewProgress creates a progress tracker for a phase.
func NewProgress(phase string, total int) *Progress {
	if total <= 0 {
		total = 1
	}
	return &Progress{
		phase:  phase,
		total:  total,
		start:  time.Now(),
		last:   time.Now(),
		width:  30,
	}
}

// Inc advances the progress by one item.
func (p *Progress) Inc(item string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done++
	if p.done > p.total {
		p.done = p.total
	}
	p.render(item)
}

// Done finalizes the progress line.
func (p *Progress) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(os.Stderr, "\r\033[K%s complete (%d/%d)\n", p.phase, p.total, p.total)
}

func (p *Progress) render(item string) {
	now := time.Now()
	elapsed := now.Sub(p.start).Seconds()
	rate := float64(p.done) / elapsed
	remaining := 0.0
	if rate > 0 {
		remaining = float64(p.total-p.done) / rate
	}
	filled := int(math.Round(float64(p.done) / float64(p.total) * float64(p.width)))
	if filled > p.width {
		filled = p.width
	}
	bar := ""
	for i := 0; i < p.width; i++ {
		if i < filled {
			bar += "#"
		} else {
			bar += "-"
		}
	}
	pct := int(float64(p.done) / float64(p.total) * 100)
	eta := time.Duration(remaining) * time.Second
	fmt.Fprintf(os.Stderr, "\r\033[K%s [%s] %d/%d (%d%%) %s ETA %s", p.phase, bar, p.done, p.total, pct, shorten(item, 30), eta.Round(time.Second))
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
