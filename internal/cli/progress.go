package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/dvrkn/khook/internal/engine"
	"github.com/dvrkn/khook/internal/spec"
)

// progress renders one live status line per step (pending → running →
// ok/failed/skipped), redrawn in place on a TTY. Event callbacks arrive from
// parallel step goroutines; every state change goes through mu.
type progress struct {
	mu      sync.Mutex
	out     io.Writer
	width   func() int
	color   bool
	rows    []*progressRow
	index   map[string]*progressRow
	drawn   int // lines currently on screen
	spin    int
	stop    chan struct{}
	stopped sync.WaitGroup
}

type progressRow struct {
	step     *spec.Step
	status   string // pending | running | ok | failed | skipped
	attempt  int
	attempts int // max attempts
	started  time.Time
	result   *engine.Result
}

const (
	ansiDim    = "\x1b[2m"
	ansiGreen  = "\x1b[32m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiReset  = "\x1b[0m"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// stdoutIsTTY reports whether live progress rendering makes sense.
func stdoutIsTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("TERM") != "dumb"
}

func newProgress(out io.Writer, levels [][]*spec.Step) *progress {
	p := &progress{
		out:   out,
		color: os.Getenv("NO_COLOR") == "",
		index: map[string]*progressRow{},
		stop:  make(chan struct{}),
		width: func() int {
			if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
				return w
			}
			return 80
		},
	}
	for _, level := range levels {
		for _, step := range level {
			row := &progressRow{step: step, status: "pending"}
			p.rows = append(p.rows, row)
			p.index[step.Name] = row
		}
	}
	return p
}

// Start draws the initial pending lines and begins the redraw ticker that
// animates spinners and elapsed times.
func (p *progress) Start() {
	p.mu.Lock()
	p.render()
	p.mu.Unlock()

	ticker := time.NewTicker(120 * time.Millisecond)
	p.stopped.Add(1)
	go func() {
		defer p.stopped.Done()
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				p.mu.Lock()
				p.spin++
				p.render()
				p.mu.Unlock()
			}
		}
	}()
}

// Handle is the engine event sink.
func (p *progress) Handle(ev engine.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	row := p.index[ev.Step.Name]
	if row == nil {
		return
	}
	switch ev.Kind {
	case engine.EventRunning:
		row.status = "running"
		row.attempt = ev.Attempt
		row.attempts = ev.MaxAttempts
		if ev.Attempt == 1 {
			row.started = time.Now()
		}
	case engine.EventAttemptFailed:
		// The runner either retries (next EventRunning bumps the attempt) or
		// finishes (EventDone); nothing to show for the attempt itself.
		return
	case engine.EventDone:
		row.status = string(ev.Result.Status)
		row.result = ev.Result
	}
	p.render()
}

// Stop ends the redraw loop, leaves the final state on screen, and prints
// full error details for failed steps (live lines truncate them).
func (p *progress) Stop(results []engine.Result) {
	close(p.stop)
	p.stopped.Wait()
	p.mu.Lock()
	p.render()
	p.mu.Unlock()

	var failed []engine.Result
	for _, res := range results {
		if res.Status == engine.StatusFailed {
			failed = append(failed, res)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintln(p.out)
		for _, res := range failed {
			fmt.Fprintf(p.out, "%s: %v\n", p.paint(ansiRed, "✗ "+res.Step.Name), res.Err)
		}
	}
}

// render redraws every row in place. Caller holds mu.
func (p *progress) render() {
	if p.drawn > 0 {
		fmt.Fprintf(p.out, "\x1b[%dA", p.drawn)
	}
	width := p.width()
	var b strings.Builder
	for _, row := range p.rows {
		b.WriteString("\x1b[2K")
		b.WriteString(truncate(p.line(row), width))
		b.WriteString("\n")
	}
	fmt.Fprint(p.out, b.String())
	p.drawn = len(p.rows)
}

func (p *progress) line(row *progressRow) string {
	name := fmt.Sprintf("%s (%s)", row.step.Name, row.step.Type())
	switch row.status {
	case "running":
		frame := spinnerFrames[p.spin%len(spinnerFrames)]
		detail := time.Since(row.started).Round(time.Second).String()
		if row.attempts > 1 && row.attempt > 1 {
			detail += fmt.Sprintf(", attempt %d/%d", row.attempt, row.attempts)
		}
		return fmt.Sprintf("%s %s  %s", p.paint(ansiYellow, frame), name, p.paint(ansiDim, detail))
	case "ok":
		return fmt.Sprintf("%s %s  %s", p.paint(ansiGreen, "✓"), name, p.paint(ansiDim, row.result.Duration.Round(time.Millisecond).String()))
	case "failed":
		return fmt.Sprintf("%s %s  %v", p.paint(ansiRed, "✗"), name, row.result.Err)
	case "skipped":
		return fmt.Sprintf("%s %s  %s", p.paint(ansiYellow, "-"), name, p.paint(ansiDim, "skipped: "+row.result.SkipReason))
	}
	return p.paint(ansiDim, fmt.Sprintf("· %s", name))
}

func (p *progress) paint(code, s string) string {
	if !p.color {
		return s
	}
	return code + s + ansiReset
}

// truncate limits the visible line to the terminal width so a long error
// never wraps and breaks the in-place redraw. ANSI escapes are zero-width,
// so count only printable runes.
func truncate(s string, width int) string {
	if width <= 1 {
		return s
	}
	visible := 0
	inEscape := false
	for i, r := range s {
		switch {
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		case r == '\x1b':
			inEscape = true
		default:
			visible++
			if visible > width-1 {
				out := s[:i] + "…"
				if strings.Contains(s, "\x1b") {
					out += ansiReset // cut mid-style: make sure it does not bleed
				}
				return out
			}
		}
	}
	return s
}
