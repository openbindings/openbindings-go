package usage

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	openbindings "github.com/openbindings/openbindings-go"
)

// FailureEvidence is the completed native process evidence retained by a
// rejected exit, signal termination, decode failure, or bounded-output failure.
type FailureEvidence struct {
	ExitCode int
	Signal   string
	Stdout   ProcessBytes
	Stderr   ProcessBytes
}

type ProcessBytes struct {
	Bytes     []byte
	Truncated bool
}

// FailureEvidenceFrom validates and extracts Usage process evidence after
// either in-process use or an invoker-frame JSON round trip.
func FailureEvidenceFrom(err error) (FailureEvidence, bool) {
	var invocationError *openbindings.InvocationError
	if !errors.As(err, &invocationError) || invocationError == nil || invocationError.Details == nil {
		return FailureEvidence{}, false
	}
	raw, marshalErr := json.Marshal(invocationError.Details)
	if marshalErr != nil {
		return FailureEvidence{}, false
	}
	var wire struct {
		Usage *struct {
			Process *struct {
				ExitCode int                 `json:"exitCode"`
				Signal   string              `json:"signal"`
				Stdout   capturedProcessWire `json:"stdout"`
				Stderr   capturedProcessWire `json:"stderr"`
			} `json:"process"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Usage == nil || wire.Usage.Process == nil {
		return FailureEvidence{}, false
	}
	stdout, ok := decodeProcessBytes(wire.Usage.Process.Stdout)
	if !ok {
		return FailureEvidence{}, false
	}
	stderr, ok := decodeProcessBytes(wire.Usage.Process.Stderr)
	if !ok {
		return FailureEvidence{}, false
	}
	return FailureEvidence{
		ExitCode: wire.Usage.Process.ExitCode, Signal: wire.Usage.Process.Signal,
		Stdout: stdout, Stderr: stderr,
	}, true
}

type capturedProcessWire struct {
	Base64     string `json:"base64"`
	ByteLength int    `json:"byteLength"`
	Truncated  bool   `json:"truncated"`
}

func decodeProcessBytes(wire capturedProcessWire) (ProcessBytes, bool) {
	if wire.ByteLength < 0 {
		return ProcessBytes{}, false
	}
	value, err := base64.StdEncoding.DecodeString(wire.Base64)
	return ProcessBytes{Bytes: value, Truncated: wire.Truncated}, err == nil && len(value) == wire.ByteLength
}
