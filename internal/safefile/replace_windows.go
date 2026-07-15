//go:build windows

package safefile

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

// Replace atomically publishes src at dst on the same filesystem.
func Replace(src, dst string) error {
	from, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	ok, _, callErr := moveFileExW.Call(
		uintptr(unsafePointer(from)),
		uintptr(unsafePointer(to)),
		uintptr(moveFileReplaceExisting|moveFileWriteThrough),
	)
	if ok == 0 {
		return fmt.Errorf("MoveFileExW %q -> %q: %w", src, dst, callErr)
	}
	return nil
}

// Keep unsafe scoped to this tiny Windows boundary.
func unsafePointer[T any](p *T) unsafe.Pointer { return unsafe.Pointer(p) }
