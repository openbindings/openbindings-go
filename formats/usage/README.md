# usage-go

openbindings.usage binding invoker, interface synthesizer, and usage-spec parser for the [OpenBindings](https://openbindings.com) Go SDK.

This package implements the **openbindings.usage** binding format: a JSON binding-unit document that wraps a pristine [jdx usage-spec](https://usage.jdx.dev/) CLI descriptor and adds the invocation semantics the descriptor cannot express (field delivery channels, stdout decoding, exit classification), so an OpenBindings `(source, ref)` resolves to a complete invocation recipe. It also parses usage-spec KDL and synthesizes OBI documents from bare CLI descriptors (wrapping them with trivial units).

**The format's authority is its companion specification:** [`openbindings.usage`](https://github.com/openbindings/spec/blob/main/formats/usage/openbindings.usage.md) — document and unit shapes, delivery/stdout/exit semantics, the argv value grammar, diagnostics, versioning, and validation all live there. This README covers only the Go API.

## Install

```
go get github.com/openbindings/openbindings-go/formats/usage
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationInvoker

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    usage "github.com/openbindings/openbindings-go/formats/usage"
)

invoker := openbindings.NewOperationInvoker(usage.NewInvoker())
```

The invoker claims `openbindings.usage@^0.1.0`. The synthesizer additionally accepts bare `usage@^2.0.0` artifacts as derivation input, emitting a wrapper source.

### Invoke a binding

```go
invoker := usage.NewInvoker()
call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        Format:   usage.WrapperToken,
        Location: "/abs/path/mycli.usage.json",
    },
    Ref: "#/units/config.set",
})
_ = call.Write(ctx, map[string]any{"key": "theme", "value": "dark"})
_ = call.Close()
out, err := openbindings.Single(ctx, call.Outputs())
```

### Synthesize an interface from a CLI descriptor

```go
synthesizer := usage.NewSynthesizer()
iface, err := synthesizer.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
        Format:   "usage@2.13.1",
        Location: "mycli.usage.kdl",
    }},
})
// iface embeds an openbindings.usage wrapper source (pristine kdl + one
// trivial unit per command) with bindings ref'ing #/units/<name>.
```

### Synthesis fidelity ceiling

Input schemas derived from usage specs inherit the source format's thin value typing (strings plus choices, booleans, counts): synthesis is structurally complete for a CLI's surface but cannot produce richer types than the descriptor carries.

## How it works

### Invocation flow

1. Loads and caches the openbindings.usage wrapper document (JSON-object source content, or an absolute location)
2. Verifies `spec.format` against the accepted artifact range, hash rules, and parses the embedded kdl
3. Resolves the binding ref (`#/units/<name>`) to a unit and validates it against the artifact (per-unit failure granularity)
4. Applies the unit's delivery routing (stdin piping, temp-file materialization), then builds argv from the input (flags by name, positionals in declared order)
5. Executes the binary via `os/exec` with the constructed argv and routed stdin
6. Classifies the exit per the unit's `exit` member, decodes stdout per its `stdout` mode (default text), and emits the value with `x-exit-code`/`x-stderr` trailing metadata

### Credential application

Usage-spec bindings execute local CLI binaries, not network services. Credentials are applied via environment variables through the `environment` key in the `BindingContext`, not HTTP headers.

### Interface creation

Converts a usage-spec KDL document into an OBI by:

- Extracting metadata (name, version, about) from the spec
- Walking all commands depth-first, skipping `subcommand_required` nodes
- Generating JSON Schema input from flags (boolean, string, integer, array) and positional args
- Using dot-separated paths as operation keys and unit names (e.g. `config.set`)
- Emitting an openbindings.usage wrapper source with bindings ref'ing `#/units/<name>`

## Parser SDK

The package also provides a standalone parsing SDK for working with usage-spec documents directly.

### Features

- **Lossless parsing**: Preserves all KDL structure for round-trip fidelity
- **Helper views**: Ergonomic access via `Spec.Meta()`, `Spec.Commands()`, etc.
- **Command traversal**: `Walk()`, `FindCommand()`, `FullPath()` helpers
- **Flag parsing**: Extract short/long names from usage strings
- **Forward compatible**: Unknown nodes preserved in `Unknown` fields

### Quick Start

```go
package main

import (
    "fmt"
    "log"

    "github.com/openbindings/openbindings-go/formats/usage"
)

func main() {
    // Parse a Usage spec file
    spec, err := usage.ParseFile("mycli.usage.kdl")
    if err != nil {
        log.Fatal(err)
    }

    // Access metadata
    meta := spec.Meta()
    fmt.Printf("CLI: %s v%s\n", meta.Name, meta.Version)

    // Walk all commands
    spec.Walk(func(path []string, cmd usage.Command) {
        fmt.Printf("Command: %v\n", path)

        for _, flag := range cmd.Flags {
            parsed := flag.ParseUsage()
            fmt.Printf("  Flag: --%s\n", parsed.Long)
        }

        for _, arg := range cmd.Args {
            fmt.Printf("  Arg: %s (required=%v)\n", arg.CleanName(), arg.IsRequired())
        }
    })

    // Find a specific command
    if cmd := spec.FindCommand([]string{"config", "set"}); cmd != nil {
        fmt.Printf("Found: %s\n", cmd.Help)
    }
}
```

## Core Types

| Type      | Description                                          |
| --------- | ---------------------------------------------------- |
| `Spec`    | Root document with lossless `Nodes` and helper views |
| `Node`    | Lossless KDL node (name, args, props, children)      |
| `Meta`    | Top-level metadata (name, version, about, etc.)      |
| `Command` | CLI command with flags, args, subcommands            |
| `Flag`    | Option definition with parsing helpers               |
| `Arg`     | Positional argument with required/variadic detection |
| `Config`  | Configuration file and defaults                      |

## Helper Methods

```go
// Arg helpers
arg.IsRequired()   // true for <name>, false for [name]
arg.IsVariadic()   // true if var=true or name ends with "..."
arg.CleanName()    // "file" from "<file>..."

// Flag helpers
flag.ParseUsage()  // {Short: ["v"], Long: ["verbose"], ArgName: "level"}
flag.PrimaryName() // "verbose" (prefers long over short)

// Command helpers
cmd.FullPath(ancestors)     // ["config", "set"]
cmd.AllFlags(inheritedGlobals) // merged global + local flags

// Spec helpers
spec.Walk(fn)              // depth-first traversal
spec.FindCommand(path)     // find by path slice
spec.Validate(opts...)     // structural validation

// Value helpers
v.String()                 // string values only
v.Bool()                   // bool or KDL v2-style "#true"/"#false"
v.Int()                    // whole-number, in-range integers only
```

## Validation

Validate specs with configurable options:

```go
// Default validation (lenient)
if err := spec.Validate(); err != nil {
    log.Fatal(err)
}

// Strict validation
err := spec.Validate(
    usage.WithRequireName(),
    usage.WithRequireBin(),
    usage.WithRequireCommandHelp(),
    usage.WithRejectUnknownNodes(),
)
```

| Option                     | Effect                             |
| -------------------------- | ---------------------------------- |
| `WithRequireName()`        | Name field must be present         |
| `WithRequireBin()`         | Bin field must be present          |
| `WithRequireCommandHelp()` | All commands must have help text   |
| `WithRejectUnknownNodes()` | Unknown top-level nodes are errors |

Note: For tooling, prefer calling `Validate()` before traversing the spec to
avoid propagating invalid or empty command names.

## Status

Early development. API may change before v1.0.
