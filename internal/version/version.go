// Package version holds build-time version information.
package version

// Version is overridden at release time via -ldflags "-X github.com/Scarsz/serverpack/internal/version.Version=v1.2.3".
var Version = "dev"

// UserAgent is sent with every outbound HTTP request.
func UserAgent() string {
	return "github.com/Scarsz/serverpack/" + Version
}
