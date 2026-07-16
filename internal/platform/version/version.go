package version

import "fmt"

var (
	Version = "0.1.0-dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, BuiltAt)
}
