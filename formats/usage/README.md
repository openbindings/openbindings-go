# formats/usage

Binding invoker, interface synthesizer, and usage-spec parser for bare [jdx usage](https://usage.jdx.dev/) CLI descriptors, for the [OpenBindings](https://openbindings.com) Go SDK.

The artifact IS the source: an OBI binding points `{source: usage@2.x/3.x, ref: "<space-separated command path>"}` at a pristine usage-spec KDL document, with no wrapper and no OB-authored companion document. Where the descriptor cannot answer a wire question (usage describes a CLI's human surface — flags, args, help — not stdout decoding, exit-code meaning, or a field's stdin routing), the gap is made up in **consumer configuration**: documented content-independent assumptions, overridable through the SDK's generic hook seam. *Specification + configuration = complete invocation.* See the shared [formats/README completeness-spectrum note](https://github.com/openbindings/spec/blob/main/formats/README.md) for the recommended defaults, the consultation matrix, and the decline chain.

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

The invoker and synthesizer claim `usage@^2.0.0` and `usage@^3.0.0` (usage version numbers track the jdx tool, which is what an artifact's `min_usage_version` pins; the KDL vocabulary is unchanged across the 2.x → 3.x line).

### Invoke a binding

```go
invoker := usage.NewInvoker()
call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        Format:   "usage@2.13.1",
        Location: "/abs/path/mycli.usage.kdl", // or exec:mycli, or inline Content
    },
    Ref: "config set", // the format's own grammar: a command path; empty = root
})
_ = call.Write(ctx, map[string]any{"key": "theme", "value": "dark"})
_ = call.Close()
out, err := openbindings.Single(ctx, call.Outputs())
```

### The assumptions (and the hooks that override them)

The built-in defaults are content-independent — decided by the artifact and these documented rules, never by sniffing payload bytes:

| Wire question | Built-in assumption | Override |
| --- | --- | --- |
| Where does an input field ride? | its argv slot (strings verbatim, other values compact JSON) | `FieldRouter` returning `usage.RouteStdinDash` (bytes to stdin, `-` in the slot), `usage.RouteStdin` (slotless pure channel), or `usage.RouteFile` (temp-file path in the slot) |
| How does stdout decode? | text, trailing newlines stripped (command-substitution semantics) | `OutputDecoder` (e.g. strict JSON for a machine lane) |
| Which exits are success? | exit 0 | `ResultClassifier` (the diff(1) class: `{0, 1}`) |

Hooks attach at invoker level (`inv.OutputDecoder = ...`) or per invocation (`openbindings.WithOutputDecoder(...)`), decline-chaining per axis. Channel tokens are validated loudly at argv assembly — an unknown token, a second stdin field, or a slot-incompatible route (a boolean flag, a choices-constrained slot without `-`) refuses before the process spawns; a typo can never silently change behavior.

`HookTable` is the data-shaped form: per-CLI knowledge (`DecodeJSON` op list, `OKExits`, `Routes`) compiled into guarded hooks. Key rows by canonical operation keys — prefer codegen'd signature constants over string literals (a stale literal after a rename silently reverts that op to the floor; constants follow the rename).

```go
table := usage.HookTable{
    DecodeJSON: []string{OperationSignatures.Report.Key},
    OKExits:    map[string][]int{OperationSignatures.Compare.Key: {0, 1}},
    Routes:     map[string]map[string]string{OperationSignatures.Format.Key: {"source": usage.RouteStdinDash}},
}
table.Install(inv)
```

### Triage

| Symptom | Remedy |
| --- | --- |
| a string where you expected an object | set an `OutputDecoder` (the machine lane is consumer knowledge) |
| an exit-code error but the output in details looks right | set a `ResultClassifier` (diff-class exits) |
| "no such file", and the filename is your document | set a `FieldRouter` (the CLI wants content, not a path — or vice versa) |
| "argument list too long" | route the field off argv (`stdin-dash` or `file`) |
| you set a decoder and output validation STARTED failing | the derived contract still declares the floor's string — elect the real output schema (`ob operation output-schema`) |
| secrets showing up in `ps` output | credentials ride environment variables via context (the `environment` field), never argv and never a `FieldRouter` |
| the command hangs | the CLI is prompting; usage cannot express interactivity — pass its non-interactive flag as an input field and set a ctx deadline |

### What usage cannot express

The descriptor declares commands, flags, args, choices, and help — a CLI's *surface*. It cannot declare: stdout structure or encoding, exit-code semantics beyond "the process exited", stdin acceptance, per-field delivery channels, streaming/interactivity, or output schemas. Every one of those is answered by the assumptions above or by your hooks; the configuration burden is honest information about the descriptor format's completeness, and pressure to reduce it belongs upstream on the usage spec itself.

### Synthesize an interface from a CLI descriptor

```go
synthesizer := usage.NewSynthesizer()
iface, err := synthesizer.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
        Format:   "usage@2.13.1",
        Location: "mycli.usage.kdl",
    }},
})
// iface carries the pristine artifact as its source, one operation per
// bindable command (dot-joined keys, e.g. config.set), bindings ref'ing
// command paths, and FLOOR-TRUE output schemas: {"type":"string"} with an
// in-schema x-ob floor-stamp (the text assumption always yields a string,
// so the derived contract never lies; the stamp keys the diagnostics and
// clears when a real schema is elected).
```

### Synthesis fidelity ceiling

Input schemas derived from usage specs inherit the source format's thin value typing (strings plus choices, booleans, counts): synthesis is structurally complete for a CLI's surface but cannot produce richer types than the descriptor carries.

## How it works

### Invocation flow

1. Loads and caches the bare usage artifact (inline content, an ABSOLUTE file location, or an `exec:` locator running the binary's own spec emission), checking `min_usage_version` against the supported range
2. Resolves the binding ref — a space-separated command path — against the command tree (empty ref = the root command)
3. Consults the `FieldRouter` chain per input field and applies the channel mechanics (stdin piping, `-` operands, temp-file materialization) with loud slot-compatibility refusals, then builds argv from the remaining fields (flags by name, positionals in declared order)
4. Executes the binary via `os/exec` with the constructed argv and routed stdin
5. Classifies the exit through the seam (assumption: exit 0), decodes stdout through the seam (assumption: text), and emits the value with `x-exit-code`/`x-stderr` and the §4.5.2 provenance stamps (`x-ob-decode`, `x-ob-classify`, per-field `x-ob-route`) as trailing metadata

### Credential application

Usage-spec bindings execute local CLI binaries, not network services. Credentials are applied via environment variables through the `environment` key in the `BindingContext`, not HTTP headers.

### Interface synthesis

Converts a usage-spec KDL document into an OBI by:

- Extracting metadata (name, version, about) from the spec
- Walking all commands depth-first, skipping `subcommand_required` nodes
- Generating JSON Schema input from flags (boolean, string, integer, array) and positional args
- Using dot-separated paths as operation keys and space-separated paths as refs (e.g. `config.set` / `config set`)
- Carrying the pristine artifact verbatim as the source (location-referenced or embedded)

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
