//go:build !windows

package safefile

import "os"

// Replace atomically publishes src at dst on the same filesystem.
func Replace(src, dst string) error {
	return os.Rename(src, dst)
}
