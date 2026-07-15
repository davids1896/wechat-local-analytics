//go:build windows

package config

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const lockfileExclusiveLock = 0x00000002

var (
	kernel32DLL      = syscall.NewLazyDLL("kernel32.dll")
	lockFileExProc   = kernel32DLL.NewProc("LockFileEx")
	unlockFileExProc = kernel32DLL.NewProc("UnlockFileEx")
)

func lockConfigFile(file *os.File) (func() error, error) {
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileExProc.Call(
		file.Fd(),
		lockfileExclusiveLock,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		return nil, windowsCallError(callErr)
	}
	return func() error {
		result, _, callErr := unlockFileExProc.Call(
			file.Fd(),
			0,
			1,
			0,
			uintptr(unsafe.Pointer(&overlapped)),
		)
		if result == 0 {
			return windowsCallError(callErr)
		}
		return nil
	}, nil
}

func windowsCallError(err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return syscall.EINVAL
	}
	return err
}
