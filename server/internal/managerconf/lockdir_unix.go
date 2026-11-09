//go:build !windows

package managerconf

import (
	"os"
	"syscall"
)

// LockDir takes an exclusive lock on the directory at path until the returned func is called.
func LockDir(path string) (func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
