// Package hostcmd holds the command-execution helpers shared by the privileged
// operators. It deliberately does NOT unify the operators' Runner types: those
// genuinely differ (file redirection, process-group kill, exit-code capture,
// env replace-vs-append) and each keeps its own runner and injectable seam. What
// this package removes is the identical leaf duplication that sat beside every
// runner — the failed-command error envelope and the bounded output writer.
package hostcmd

import (
	"fmt"
	"strings"
)

// maxErrorOutput bounds how much command output is echoed into an error so a
// noisy tool cannot blow up a log line or leak an unbounded payload.
const maxErrorOutput = 500

// Error formats a failed command into "<action>: <err>: <output>", trimming and
// truncating the output. When the command produced no output the message is just
// "<action>: <err>". It replaces the byte-identical commandError helper that was
// copied into each operator package.
func Error(action string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if len(message) > maxErrorOutput {
		message = message[:maxErrorOutput]
	}
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, message)
}

// Writer is an io.Writer that retains at most Limit bytes while reporting every
// write as fully consumed, so a command's combined stdout/stderr can be captured
// under a hard ceiling without short-writing the process. It replaces the
// identical cappedOutput type that mysql, postgres, and schedules each defined.
type Writer struct {
	Limit int
	data  []byte
}

func (w *Writer) Write(value []byte) (int, error) {
	original := len(value)
	if remaining := w.Limit - len(w.data); remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		w.data = append(w.data, value...)
	}
	return original, nil
}

// Bytes returns the captured output, never exceeding Limit bytes.
func (w *Writer) Bytes() []byte { return w.data }
