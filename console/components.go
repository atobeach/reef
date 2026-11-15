package console

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// terminalWidth is the width the components fill: COLUMNS when set, 80
// otherwise, capped at 150.
func terminalWidth() int {
	width := 80
	if columns, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && columns > 0 {
		width = columns
	}

	return min(width, 150)
}

// Write prints the message with no trailing newline (progress dots).
func (o *Output) Write(message string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	_, _ = fmt.Fprint(o.w, message)
}

// TwoColumnDetail prints "  first ........ second  " across the terminal
// width.
func (o *Output) TwoColumnDetail(first, second string) {
	dots := max(terminalWidth()-utf8.RuneCountInString(first)-utf8.RuneCountInString(second)-6, 0)
	o.Line("  " + first + " " + strings.Repeat(".", dots) + " " + second + "  ")
}

// Task runs task and prints "  description ....... 12.34ms DONE" (FAIL when
// it errors). The error is returned for the caller to handle.
func (o *Output) Task(description string, task func() error) error {
	o.Write("  " + description + " ")

	started := time.Now()
	err := task()
	runTime := " " + runTimeForHumans(time.Since(started))

	dots := max(terminalWidth()-utf8.RuneCountInString(description)-utf8.RuneCountInString(runTime)-10, 0)
	o.Write(strings.Repeat(".", dots) + runTime)

	if err != nil {
		o.Line(" FAIL")

		return err
	}

	o.Line(" DONE")

	return nil
}

// runTimeForHumans is Task's elapsed time: milliseconds to two places up to
// a second, then a short interval ("2s", "1m 5s").
func runTimeForHumans(elapsed time.Duration) string {
	ms := float64(elapsed.Microseconds()) / 1000
	if ms <= 1000 {
		return strconv.FormatFloat(ms, 'f', 2, 64) + "ms"
	}

	elapsed = elapsed.Truncate(time.Second)
	minutes, seconds := int(elapsed.Minutes()), int(elapsed.Seconds())%60
	if minutes == 0 {
		return fmt.Sprintf("%ds", seconds)
	}

	if seconds == 0 {
		return fmt.Sprintf("%dm", minutes)
	}

	return fmt.Sprintf("%dm %ds", minutes, seconds)
}

// ProgressBar is a block-character bar (" 3/3 [▓▓▓…] 100%") that redraws
// nothing: it prints a frame at start and on finish, each on its own line,
// so it reads the same in logs as on a terminal.
type ProgressBar struct {
	out     *Output
	max     int
	current int
	drawn   bool
}

// CreateProgressBar builds a bar of total steps.
func (o *Output) CreateProgressBar(total int) *ProgressBar {
	return &ProgressBar{out: o, max: total}
}

// Start draws the empty bar.
func (b *ProgressBar) Start() { b.draw() }

// Advance moves the bar one step.
func (b *ProgressBar) Advance() { b.current++ }

// Finish fills the bar and draws it.
func (b *ProgressBar) Finish() {
	b.current = max(b.current, b.max)
	b.draw()
}

// draw writes the current frame, on a new line after the first.
func (b *ProgressBar) draw() {
	const width = 28

	filled := width
	percent := 100
	if b.max > 0 {
		filled = width * b.current / b.max
		percent = 100 * b.current / b.max
	}

	frame := fmt.Sprintf(" %*d/%d [%s%s] %3d%%", len(strconv.Itoa(b.max)), b.current, b.max,
		strings.Repeat("\u2593", filled), strings.Repeat("\u2591", width-filled), percent)

	if b.drawn {
		frame = "\n" + frame
	}

	b.drawn = true
	b.out.Write(frame)
}
