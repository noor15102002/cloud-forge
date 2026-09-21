//go:build !linux

package verification

import "errors"

func backendDiskAvailable(string) (uint64, error) {
	return 0, errors.New("backend capacity requires Linux")
}
