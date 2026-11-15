package console

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// ANSI styles of the message kinds.
const (
	styleInfo    = "\x1b[32m"
	styleComment = "\x1b[33m"
	styleWarning = "\x1b[33m"
	styleError   = "\x1b[37;41m"
	styleReset   = "\x1b[0m"
)

// Output writes a command's messages. Colours are only emitted on a
// terminal, so logs and test buffers get plain text.
type Output struct {
	mu        sync.Mutex
	w         io.Writer
	decorated bool
	// keepErrors collects the error lines into errorLines, for a silenced
	// command whose failure must still say why.
	keepErrors bool
	errorLines []string
}

// NewOutput writes to w, decorating when w is a terminal.
func NewOutput(w io.Writer) *Output {
	return &Output{w: w, decorated: isTerminal(w)}
}

// silenced prints nothing but keeps the error lines.
func silenced() *Output {
	return &Output{w: io.Discard, keepErrors: true}
}

// keptErrors is what a silenced output's error lines said, joined.
func (o *Output) keptErrors() string {
	o.mu.Lock()
	defer o.mu.Unlock()

	return strings.Join(o.errorLines, "; ")
}

// isTerminal reports whether w is a character device.
func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}

	info, err := file.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// write prints one styled line.
func (o *Output) write(style, message string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.keepErrors && style == styleError {
		o.errorLines = append(o.errorLines, message)
	}

	if o.decorated && style != "" {
		message = style + message + styleReset
	}

	_, _ = fmt.Fprintln(o.w, message)
}

// Line prints an unstyled line.
func (o *Output) Line(message string) { o.write("", message) }

// Info prints a green line.
func (o *Output) Info(message string) { o.write(styleInfo, message) }

// Comment prints a yellow line.
func (o *Output) Comment(message string) { o.write(styleComment, message) }

// Warn prints a yellow warning line.
func (o *Output) Warn(message string) { o.write(styleWarning, message) }

// Error prints a white-on-red line.
func (o *Output) Error(message string) { o.write(styleError, message) }

// Linef prints an unstyled formatted line.
func (o *Output) Linef(format string, args ...any) { o.Line(fmt.Sprintf(format, args...)) }

// Infof prints a green formatted line.
func (o *Output) Infof(format string, args ...any) { o.Info(fmt.Sprintf(format, args...)) }

// Commentf prints a yellow formatted line.
func (o *Output) Commentf(format string, args ...any) { o.Comment(fmt.Sprintf(format, args...)) }

// Warnf prints a yellow formatted warning line.
func (o *Output) Warnf(format string, args ...any) { o.Warn(fmt.Sprintf(format, args...)) }

// Errorf prints a white-on-red formatted line.
func (o *Output) Errorf(format string, args ...any) { o.Error(fmt.Sprintf(format, args...)) }

// NewLine prints an empty line.
func (o *Output) NewLine() { o.write("", "") }

// Table prints a boxed table (`+---+` borders, `|` separators).
func (o *Output) Table(headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len([]rune(header))
	}

	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				widths[i] = max(widths[i], len([]rune(cell)))
			}
		}
	}

	var b strings.Builder
	b.WriteString("+")
	for _, width := range widths {
		b.WriteString(strings.Repeat("-", width+2) + "+")
	}

	border := b.String()

	line := func(cells []string) string {
		var b strings.Builder
		b.WriteString("|")
		for i, width := range widths {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}

			b.WriteString(" " + cell + strings.Repeat(" ", width-len([]rune(cell))) + " |")
		}

		return b.String()
	}

	o.Line(border)
	o.Line(line(headers))
	o.Line(border)

	for _, row := range rows {
		o.Line(line(row))
	}

	o.Line(border)
}
