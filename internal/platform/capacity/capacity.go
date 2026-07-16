package capacity

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type Profile string

const (
	ProfileUnsupported Profile = "unsupported"
	ProfileCompact     Profile = "compact"
	ProfileStandard    Profile = "standard"
	ProfilePro         Profile = "pro"
)

type Snapshot struct {
	Supported      bool    `json:"supported"`
	TotalBytes     uint64  `json:"totalBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	SwapTotalBytes uint64  `json:"swapTotalBytes"`
	SwapFreeBytes  uint64  `json:"swapFreeBytes"`
	UsedPercent    float64 `json:"usedPercent"`
	Profile        Profile `json:"profile"`
}

type Reader interface {
	Read(ctx context.Context) (Snapshot, error)
}

type ProcReader struct {
	path string
}

func NewProcReader() ProcReader {
	return ProcReader{path: "/proc/meminfo"}
}

func (r ProcReader) Read(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	file, err := os.Open(r.path)
	if err != nil {
		return Snapshot{Supported: false, Profile: ProfileUnsupported}, fmt.Errorf("read %s: %w", r.path, err)
	}
	defer file.Close()

	return ParseMemInfo(file)
}

func ParseMemInfo(source io.Reader) (Snapshot, error) {
	values := make(map[string]uint64)
	scanner := bufio.NewScanner(source)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ":")
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return Snapshot{}, fmt.Errorf("parse %s: %w", name, err)
		}
		if len(fields) >= 3 && strings.EqualFold(fields[2], "kB") {
			value *= 1024
		}
		values[name] = value
	}
	if err := scanner.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("scan memory information: %w", err)
	}

	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 {
		return Snapshot{}, errors.New("MemTotal is missing or zero")
	}
	if available > total {
		available = total
	}

	usedPercent := float64(total-available) / float64(total) * 100
	return Snapshot{
		Supported:      true,
		TotalBytes:     total,
		AvailableBytes: available,
		SwapTotalBytes: values["SwapTotal"],
		SwapFreeBytes:  values["SwapFree"],
		UsedPercent:    usedPercent,
		Profile:        Classify(total),
	}, nil
}

func Classify(totalBytes uint64) Profile {
	const gib = uint64(1024 * 1024 * 1024)
	switch {
	case totalBytes < 2*gib:
		return ProfileUnsupported
	case totalBytes < 4*gib:
		return ProfileCompact
	case totalBytes < 8*gib:
		return ProfileStandard
	default:
		return ProfilePro
	}
}

func FormatBytes(bytes uint64) string {
	const (
		kib = uint64(1024)
		mib = kib * 1024
		gib = mib * 1024
	)
	switch {
	case bytes >= gib:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(gib))
	case bytes >= mib:
		return fmt.Sprintf("%.0f MiB", float64(bytes)/float64(mib))
	case bytes >= kib:
		return fmt.Sprintf("%.0f KiB", float64(bytes)/float64(kib))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
