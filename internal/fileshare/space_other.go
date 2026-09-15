//go:build !darwin && !linux && !windows

package fileshare

import "errors"

func availableBytes(string) (int64, error) {
	return 0, errors.New("disk availability probe unsupported")
}
