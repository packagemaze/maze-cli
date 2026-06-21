package version

import "fmt"

var (
	Version = "0.1.0"
	Commit  = "unknown"
	Date    = "unknown"
)

func Info() string {
	return fmt.Sprintf("maze %s (%s, %s)", Version, Commit, Date)
}
