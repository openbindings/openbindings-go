# usage-go

Usage-spec binding invoker, interface creator, and parser for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to execute CLI tools described by [Usage spec](https://usage.jdx.dev/) KDL documents and synthesize OBI documents from them. It parses usage-spec files, builds CLI arguments from OBI input, executes the binary, and returns the result as a stream event.

See the [spec](https://github.com/openbindings/spec) and [pattern documentation](https://github.com/openbindings/spec/tree/main/patterns) for how invokers and creators fit into the OpenBindings architecture.

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

exec := openbindings.NewOperationInvoker(usage.NewInvoker())
```

The invoker declares `usage@^2.0.0` -- it handles any Usage spec version 2.x.

### Invoke a binding

```go
invoker := usage.NewInvoker()
ch, err := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationInput{
    Source: openbindings.BindingInvocationSource{
        Format:   "usage@2.0",
        Location: "mycli.usage.kdl",
    },
    Ref:   "config set",
    Input: map[string]any{"key": "theme", "value": "dark"},
})
for ev := range ch {
    if ev.Error != nil {
        log.Fatal(ev.Error.Message)
    }
    fmt.Println(ev.Output)
}
```

### Create an interface from a usage spec

```go
creator := usage.NewCreator()
iface, err := creator.CreateInterface(ctx, &openbindings.CreateInput{
    Sources: []openbindings.CreateSource{{
        Format:   "usage@2.0",
        Location: "mycli.usage.kdl",
    }},
})
// iface is a fully-formed OBInterface with operations, bindings, and sources
```

## Conventions

These are the conventions of the `usage` binding format — the rules a tool
needs beyond the [Usage spec](https://usage.jdx.dev/) itself to synthesize
interfaces from, and invoke bindings against, usage-described CLIs. The
format token versions the *artifact* (the Usage spec version a source
document is written in); these conventions carry their own version.

**Conventions version: 2.** Version 2 adds binding transport members
(`x-usage`: field delivery routing, declared stdout modes), fixes the argv
value grammar to canonical JSON, and moves stderr out of the output value
(breaking; see CHANGELOG).

### Format token

`usage@^2.0.0` (caret range). Matches any Usage spec version 2.x.

### Ref format

Space-separated command path:

- `config set` - the `set` subcommand under `config`
- `deploy` - a top-level command
- `db migrate run` - a deeply nested subcommand

The ref mirrors how the command would be invoked on the command line (without the binary name).

### Source expectations

- **`location`**: Path to the usage-spec KDL file (e.g., `mycli.usage.kdl`). Also supports `exec:<binary>` to extract the usage spec from the binary at runtime.
- **`content`**: Inline usage-spec KDL content (string).

### Binding members (`x-usage`)

A binding entry MAY carry an `x-usage` extension member declaring how the
transport-input fields physically reach the process, and what the command's
stdout is. Shape adaptation (field renames, format forcing) stays in the
spec-level `inputTransform`; `x-usage` members are written against the
post-transform object, because the transform's output *is* the transport's
input.

```json
"compare.usage": {
  "operation": "compare",
  "source": "cli",
  "ref": "diff",
  "x-usage": {
    "delivery": { "baseline": "stdin", "comparison": "file" },
    "stdout": "json"
  }
}
```

- **`delivery`** maps a transport-input field name to `"stdin"` or `"file"`;
  unlisted fields ride argv. The field keeps its normal flag/arg mapping —
  delivery only substitutes the token in its slot: `-` for stdin, an
  absolute temp-file path for file, with the real bytes carried out of band.
  At most one field may ride stdin. A string value is written raw (it
  already is the document text); any other value is written as compact JSON.
  Temp files are named `<field>.json` for JSON-encoded values and `<field>`
  for raw strings, live in a private directory, and are removed once the
  process exits. A listed field absent from the input is a no-op.
- **`stdout`** declares zero-exit stdout: `"json"` (one strict JSON value,
  numbers included; empty stdout is `null`; a parse failure is a terminal
  `ERR_EXECUTION_FAILED`, never a silent wrap) or `"text"` (the raw string
  is the output value). Absent means the heuristic below.

### Value grammar

Input fields render onto argv as: strings verbatim; every other value as its
compact canonical JSON (`1000000`, `{"k":"v"}`). Booleans on flags use
presence semantics (`true` emits the flag; `false` emits the flag's `negate`
form when declared, else nothing; `count` flags repeat by value). Arrays
repeat a flag per item or spread across a variadic arg, items per the same
rules. `null` fields are omitted.

Zero-exit stdout without a declared `stdout` mode decodes heuristically:
objects and arrays by shape, plus JSON strings and the literals
`null`/`true`/`false`, bare-parse; anything else (bare numbers, prose) wraps
as `{"stdout": <text>}`. Bindings that mean JSON declare it.

### Diagnostics

stderr and the exit code are diagnostics, never part of the output value:
on the zero-exit path they ride trailing metadata as `x-stderr` and
`x-exit-code`. A non-zero exit is a terminal `ERR_EXECUTION_FAILED` carrying
`{exitCode, output: {stdout, stderr}}` in details.

### Credential application

Usage-spec bindings execute local CLI binaries, not network services. There are no HTTP headers. Credentials and configuration are supplied through the `environment` key in the `BindingContext` and surfaced to the child process as environment variables.

### Interface creation

- Commands walked depth-first, skipping `subcommand_required` nodes
- Operation keys use dot-separated paths (e.g., `config.set`)
- Binding refs use space-separated paths (e.g., `config set`)
- Input schemas built from flags (boolean, string, integer, array) and positional args
- No security metadata (local invocation)

## How it works

### Invocation flow

1. Loads and caches the usage-spec KDL document (from file, inline content, or `exec:` artifact)
2. Finds the command matching the ref (space-separated path, e.g. `config set`)
3. Applies the binding's `x-usage` delivery routing (stdin piping, temp-file materialization), then builds CLI arguments from the input: flags are mapped by name, positional args by order
4. Executes the binary via `os/exec` with the constructed arguments (and routed stdin)
5. Decodes stdout per the binding's declared `stdout` mode (strict JSON, raw text, or the heuristic)
6. Emits the value as one output, with the exit code and captured stderr as `x-exit-code`/`x-stderr` trailing metadata

### Credential application

Usage-spec bindings execute local CLI binaries, not network services. Credentials are applied via environment variables through the `environment` key in the `BindingContext`, not HTTP headers.

### Interface creation

Converts a usage-spec KDL document into an OBI by:

- Extracting metadata (name, version, about) from the spec
- Walking all commands depth-first, skipping `subcommand_required` nodes
- Generating JSON Schema input from flags (boolean, string, integer, array) and positional args
- Using dot-separated paths as operation keys (e.g. `config.set`)
- Using space-separated paths as binding refs (e.g. `config set`)

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
