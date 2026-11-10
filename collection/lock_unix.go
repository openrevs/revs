//go:build !windows

package collection

import (
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive lock on f without blocking, reporting
// whether it got it.
func tryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == unix.EWOULDBLOCK {
		return false, nil
	}
	return err == nil, err
}

func unlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
