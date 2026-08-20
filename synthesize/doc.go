// Package synthesize is the OpenBindings authoring layer: the realization
// of the published interface-synthesizer and source-inspector interfaces.
//
// An InterfaceSynthesizer builds OpenBindings interfaces from sources
// governed by its supported binding specifications; a SourceInspector
// enumerates a source's bindable targets. CombineSynthesizers routes both
// capabilities across formats by exact binding-specification identifier.
// FetchInterface resolves an OBI from a base URL (well-known discovery,
// with synthesis from a raw spec as the fallback).
package synthesize
