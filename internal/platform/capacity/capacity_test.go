package capacity

import (
	"strings"
	"testing"
)

func TestParseMemInfoCompactProfile(t *testing.T) {
	input := `MemTotal:        2097152 kB
MemAvailable:   1048576 kB
SwapTotal:      1048576 kB
SwapFree:        524288 kB
`

	snapshot, err := ParseMemInfo(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseMemInfo returned an error: %v", err)
	}
	if snapshot.Profile != ProfileCompact {
		t.Fatalf("profile = %q, want compact", snapshot.Profile)
	}
	if snapshot.UsedPercent != 50 {
		t.Fatalf("used percent = %.1f, want 50", snapshot.UsedPercent)
	}
}

func TestClassify(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	tests := []struct {
		memory uint64
		want   Profile
	}{
		{memory: gib, want: ProfileUnsupported},
		{memory: 2 * gib, want: ProfileCompact},
		{memory: 4 * gib, want: ProfileStandard},
		{memory: 8 * gib, want: ProfilePro},
	}
	for _, test := range tests {
		if got := Classify(test.memory); got != test.want {
			t.Errorf("Classify(%d) = %q, want %q", test.memory, got, test.want)
		}
	}
}
