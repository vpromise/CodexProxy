package managementasset

import (
	"bytes"
	_ "embed"
	"strings"
)

const managementVersionPlaceholder = "__CLI_PROXYAPI_VERSION__"

//go:embed bundled/index.html
var bundledManagementHTML []byte

// HTML returns the scoped management panel embedded in the server binary.
// Release builds replace the placeholder at runtime so standalone archives and
// Docker images report the same version as the backend binary.
func HTML(version string) []byte {
	version = sanitizeVersion(version)
	return bytes.ReplaceAll(
		bundledManagementHTML,
		[]byte(managementVersionPlaceholder),
		[]byte(version),
	)
}

func sanitizeVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "dev"
	}
	var safe strings.Builder
	safe.Grow(len(version))
	for _, char := range version {
		switch {
		case char >= 'a' && char <= 'z':
			safe.WriteRune(char)
		case char >= 'A' && char <= 'Z':
			safe.WriteRune(char)
		case char >= '0' && char <= '9':
			safe.WriteRune(char)
		case char == '.', char == '-', char == '_', char == '+':
			safe.WriteRune(char)
		default:
			safe.WriteByte('-')
		}
	}
	return safe.String()
}
