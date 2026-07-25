package apispec

import "testing"

// Guards that the embedded spec actually parsed at init; a broken embed would
// make every routing assertion elsewhere meaningless. It reads the parsed map
// directly rather than through an accessor, so the package exports nothing for
// tests alone.
func TestSpecParsed(t *testing.T) {
	if operationsErr != nil {
		t.Fatalf("parse operations: %v", operationsErr)
	}
	if len(operations) < 100 {
		t.Fatalf("only %d operations parsed; expected the full contract", len(operations))
	}
}
