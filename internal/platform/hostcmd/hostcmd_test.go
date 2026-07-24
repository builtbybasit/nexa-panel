package hostcmd

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorFormats(t *testing.T) {
	base := errors.New("exit status 1")

	withOutput := Error("start unit", []byte("  boom  "), base)
	if withOutput.Error() != "start unit: exit status 1: boom" {
		t.Fatalf("got %q", withOutput.Error())
	}
	if !errors.Is(withOutput, base) {
		t.Fatal("wrapped error must stay unwrappable")
	}

	empty := Error("start unit", []byte("   "), base)
	if empty.Error() != "start unit: exit status 1" {
		t.Fatalf("got %q", empty.Error())
	}
}

func TestErrorTruncatesOutput(t *testing.T) {
	long := strings.Repeat("x", 900)
	got := Error("run", []byte(long), errors.New("e"))
	// "run: e: " prefix + 500 bytes of output
	if len(got.Error()) != len("run: e: ")+maxErrorOutput {
		t.Fatalf("expected truncation to %d bytes, got len %d", maxErrorOutput, len(got.Error()))
	}
}

func TestWriterCapsButReportsFullWrite(t *testing.T) {
	w := &Writer{Limit: 4}
	n, err := w.Write([]byte("abcdef"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 6 {
		t.Fatalf("Write must report the full input length (6), got %d", n)
	}
	if string(w.Bytes()) != "abcd" {
		t.Fatalf("retained %q, want abcd", w.Bytes())
	}
	// A second write past the limit is dropped but still reported as consumed.
	n, _ = w.Write([]byte("gh"))
	if n != 2 || string(w.Bytes()) != "abcd" {
		t.Fatalf("n=%d data=%q", n, w.Bytes())
	}
}
