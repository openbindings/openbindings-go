//go:build !(linux || darwin)

package openbindings

import "errors"

type sentinels struct{}

func startSentinels([]string) (*sentinels, error) {
	return nil, errors.New("retrieval cannot be observed on this platform")
}

func (*sentinels) substitute(doc []byte) []byte { return doc }

func (*sentinels) stop() string { return "" }
