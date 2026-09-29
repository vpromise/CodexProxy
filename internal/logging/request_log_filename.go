package logging

import (
	"path/filepath"
	"regexp"
	"strings"
)

var requestLogFilenamePattern = regexp.MustCompile(`^(.*-\d{4}-\d{2}-\d{2}T\d{6}(?:_\d+)?)-(.+)$`)

// SplitRequestLogFilename separates the timestamp/sequence prefix from the full
// request ID. IDs may contain hyphens; older filenames without a timestamp retain
// the trailing-component convention. The extension is excluded from both results.
func SplitRequestLogFilename(filename string) (prefix, requestID string) {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	if parts := requestLogFilenamePattern.FindStringSubmatch(base); parts != nil {
		return parts[1], parts[2]
	}
	if index := strings.LastIndex(base, "-"); index > 0 {
		return base[:index], base[index+1:]
	}
	return base, ""
}
