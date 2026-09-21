//go:build linux

package verification

import (
	"errors"
	"math"
	"syscall"
)

func backendDiskAvailable(path string) (uint64, error) {
	var state syscall.Statfs_t
	if err := syscall.Statfs(path, &state); err != nil || state.Bsize <= 0 || state.Bavail > math.MaxUint64/uint64(state.Bsize) {
		return 0, errors.New("docker storage capacity unavailable")
	}
	// Bavail excludes blocks reserved from ordinary writers; Bfree overstates
	// capacity available to the local daemon/user on reserved-block filesystems.
	return state.Bavail * uint64(state.Bsize), nil
}
