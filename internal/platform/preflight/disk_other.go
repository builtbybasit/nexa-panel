//go:build !linux && !darwin

package preflight

import "errors"

func availableBytes(string) (uint64, error) {
	return 0, errors.New("free space is not measurable on this operating system")
}
