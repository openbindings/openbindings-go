//go:build !(linux || darwin)

package openbindings

import "errors"

// fileSentinel is unavailable where the platform has no FIFOs.
type fileSentinel struct{ path string }

func startFileSentinel() (*fileSentinel, error) {
	return nil, errors.New("the file channel cannot be observed on this platform")
}

func (*fileSentinel) stop() int64 { return 0 }
