// Package usage provides a Go SDK for the Usage CLI specification.
//
// The SDK offers lossless parsing of Usage (.usage.kdl) documents with
// ergonomic helper views for accessing commands, flags, args, and metadata.
//
// Basic usage:
//
//	spec, err := usage.ParseFile("mycli.usage.kdl")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	meta := spec.Meta()
//	fmt.Println(meta.Name, meta.Version)
//
//	spec.Walk(func(path []string, cmd usage.Command) {
//	    fmt.Println(path, cmd.Help)
//	})
package usage

import (
	"fmt"
	"math"
)

// Spec is the root Usage document.
// The Usage spec refers to the document as a "spec", so we mirror that term here.
type Spec struct {
	Nodes []Node
}

// Node is a lossless KDL node representation.
// It preserves all names, args, properties, and children.
type Node struct {
	Name     string
	Args     []Value
	Props    map[string]Value
	Children []Node
}

// Value preserves a raw KDL value.
type Value struct {
	Raw any
}

func (v Value) String() string {
	switch t := v.Raw.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}

func (v Value) Bool() (bool, bool) {
	if b, ok := v.Raw.(bool); ok {
		return b, true
	}
	// KDL v2 uses #true/#false which some parsers return as strings
	if s, ok := v.Raw.(string); ok {
		switch s {
		case "#true", "true":
			return true, true
		case "#false", "false":
			return false, true
		}
	}
	return false, false
}

// Int returns a strict integer conversion: only whole-number and in-range values succeed.
func (v Value) Int() (int, bool) {
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1

	switch t := v.Raw.(type) {
	case int:
		return t, true
	case int64:
		if t > int64(maxInt) || t < int64(minInt) {
			return 0, false
		}
		return int(t), true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		if math.Trunc(t) != t {
			return 0, false
		}
		if t > float64(maxInt) || t < float64(minInt) {
			return 0, false
		}
		return int(t), true
	default:
		return 0, false
	}
}

// Meta contains top-level metadata from a Usage spec.
type Meta struct {
	MinUsageVersion        string
	Name                   string
	Bin                    string
	About                  string
	Usage                  string // e.g., "Usage: mycli [OPTIONS] <command>"
	Version                string
	Author                 string
	License                string
	BeforeHelp             string
	AfterHelp              string
	BeforeLongHelp         string
	LongAbout              string
	AfterLongHelp          string
	SourceCodeLinkTemplate string
	Includes               []string
	Examples               []Example
	Unknown                []Node
}

type Command struct {
	Node               Node
	Name               string
	Hide               bool
	SubcommandRequired bool
	BeforeHelp         string
	Help               string
	AfterHelp          string
	BeforeLongHelp     string
	LongHelp           string
	AfterLongHelp      string
	Examples           []Example
	Aliases            []Alias
	Flags              []Flag
	Args               []Arg
	Commands           []Command
	Completes          []Complete
	Mounts             []Mount
	Unknown            []Node
}

type Flag struct {
	Node           Node
	Usage          string
	Aliases        []Alias
	Args           []Arg
	Choices        []string
	Hide           bool
	Global         bool
	Count          bool
	Required       bool
	Var            bool
	VarMin         *int
	VarMax         *int
	Default        any
	Negate         string
	Env            string
	ConfigKey      string
	RequiredIf     string
	RequiredUnless string
	Overrides      string
	Help           string
	LongHelp       string
	Unknown        []Node
}

type Arg struct {
	Node       Node
	Name       string
	Default    any
	Env        string
	Parse      string
	Var        bool
	VarMin     *int
	VarMax     *int
	Choices    []string
	Help       string
	LongHelp   string
	DoubleDash string
	Hide       bool
	Unknown    []Node
}

type Example struct {
	Node   Node
	Code   string
	Header string
	Help   string
	Lang   string
	Args   []string
	Unknown []Node
}

type Alias struct {
	Node  Node
	Names []string
	Hide  bool
}

type Mount struct {
	Node Node
	Run  string
}

type Complete struct {
	Node        Node
	Target      string
	Run         string
	Descriptions bool
}

type Config struct {
	Node     *Node
	Files    []ConfigFile
	Defaults []ConfigDefault
	Aliases  []ConfigAlias
	Unknown  []Node
}

type ConfigFile struct {
	Node   Node
	Path   string
	FindUp bool
	Format string
}

type ConfigDefault struct {
	Node  Node
	Key   string
	Value any
}

type ConfigAlias struct {
	Node Node
	From string
	To   []string
}
