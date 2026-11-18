// Package resource distinguishes native filesystem paths from Android grants.
package resource

import (
	"net/url"
	"path/filepath"
	"strings"
)

func IsURI(path string) bool {
	u, err := url.Parse(path)
	return err == nil && u.Scheme == "content" && u.Host != "" && u.Path != ""
}
func Valid(path string) bool { return filepath.IsAbs(path) || IsURI(path) }
func Clean(path string) string {
	if IsURI(path) {
		return strings.TrimSpace(path)
	}
	return filepath.Clean(path)
}
