package synthesize

import "errors"

var (
	// ErrNoSynthesizer is returned when no synthesizer matches the requested format.
	ErrNoSynthesizer = errors.New("openbindings: no synthesizer for format")

	// ErrNoSources is returned when an operation requires sources but none were provided.
	ErrNoSources = errors.New("openbindings: no sources provided")

	// ErrMultipleSources is returned by single-source synthesizers handed a
	// multi-source input. Multi-source composition is implementation-defined
	// (the format packages compose one artifact per call); answering for a
	// subset silently is never legitimate — compose above the format
	// (per-source synthesis + merge, or a service-level synthesizer).
	ErrMultipleSources = errors.New("openbindings: this synthesizer composes one source per call; synthesize per source and merge, or use a multi-source synthesizer")

	// ErrSourceInspectionUnsupported is returned when a source inspector cannot inspect a format.
	ErrSourceInspectionUnsupported = errors.New("openbindings: source inspection unsupported for format")

	// ErrSynthesisCoverageUnsupported is returned when a selected synthesizer
	// does not implement the optional durable coverage capability.
	ErrSynthesisCoverageUnsupported = errors.New("openbindings: synthesis coverage unsupported for format")
)
