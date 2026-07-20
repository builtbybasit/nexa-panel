package identity

import "github.com/nexa-panel/nexa-panel/internal/platform/secureid"

func randomID(bytes int) string { return secureid.Hex(bytes) }
