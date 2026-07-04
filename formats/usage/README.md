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
document is written in); these conventions carry their own version. In this
section, MUST/SHOULD/MAY carry their RFC 2119 meanings and bind any
implementation of the format, not just this SDK.

**Conventions version: 2.** Version 2 adds binding transport members
(`x-usage`: field delivery routing, declared stdout modes, exit
classification), fixes the argv value grammar to canonical JSON, and moves
stderr out of the output value (breaking; see CHANGELOG). This package
exports its level as `usage.ConventionsVersion`.

**Version skew.** Three rules make conventions levels detectable without a
per-binding version stamp:

1. **Fail closed.** A tool implementing these conventions MUST treat an
   `x-usage` member containing member names or mode values it does not
   recognize as an invocation error, never ignore them. Presence of
   `x-usage` is the declaration; unknown vocabulary inside it means the
   binding was authored against a later conventions level.
2. **Unactionable, not ignorable.** A tool that handles the usage format
   but does not implement `x-usage` at all MUST treat a binding carrying it
   as one it cannot act on (excluded from binding selection per OBI-T-09),
   rather than invoking with the member ignored. (Tools predating these
   conventions cannot be reached by this rule; no third-party
   implementation of the usage binding format predates conventions 2, so
   that exposure window is empty.)
3. **No reinterpretation.** A future conventions level MUST NOT change the
   meaning of documents valid under an earlier level; new behavior is keyed
   to the presence of new members or mode values, so rule 1 catches it.

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

A binding entry MAY carry an `x-usage` extension member declaring the
binding author's per-command elections: how each transport-input field
physically reaches the process, what the command's stdout is, and which
exit codes are success. Shape adaptation (field renames, format forcing)
stays in the spec-level `inputTransform`; `x-usage` members are written
against the post-transform object, because the transform's output *is* the
transport's input.

```json
"compare.usage": {
  "operation": "compare",
  "source": "cli",
  "ref": "diff",
  "x-usage": {
    "delivery": { "baseline": "stdin", "comparison": "file" },
    "stdout": "json",
    "exit": { "ok": [0, 1] }
  }
}
```

**`delivery`** maps a transport-input field name to `"stdin"` or `"file"`;
unlisted fields ride argv.

- The field keeps its normal flag/arg mapping; delivery substitutes the
  token in its slot — `-` for stdin, an absolute temp-file path for file —
  with the real bytes carried out of band. A stdin-routed field that maps
  to **no** flag or arg of the command is consumed by delivery itself:
  bytes to stdin, nothing emitted on argv (the no-operand filter class:
  `pbcopy`, `sort`). A file-routed field MUST map to a flag or arg; its
  path has to land somewhere.
- A binding MUST NOT declare more than one field with `"stdin"` delivery;
  tools MUST reject such a binding at invocation regardless of which fields
  the input carries (the rule is static).
- Byte encoding: a string value is written raw (it already is the document
  text); any other value is written as its compact JSON serialization. A
  listed field that is absent from the input, or present with JSON `null`,
  is a no-op (matching the argv grammar's null rule).
- Temp files: the path passed MUST be absolute; files MUST live in a
  private per-invocation directory that is removed once the process exits
  (note: removal cannot be guaranteed if the invoking process itself dies
  first — see Security); a JSON-encoded value's file name MUST end in
  `.json` and a raw string's MUST NOT (children sniff by extension); the
  base name is otherwise implementation-chosen (SHOULD derive from the
  field name). A routed value larger than 10 MiB is refused before spawn.

**`stdout`** declares what zero-exit stdout *is*:

- `"json"`: one strict JSON value, numbers included. Surrounding whitespace
  is stripped before parsing; empty or whitespace-only stdout decodes as
  `null`; anything else that fails to parse is a terminal
  `ERR_EXECUTION_FAILED`, never a silent wrap.
- `"text"`: the output value is stdout with trailing newline characters
  (`\n`, `\r`) stripped — command-substitution semantics: the final newline
  is line-termination framing, not payload. Interior newlines are
  preserved.
- Absent: the heuristic below.

**`exit`** classifies exit codes: `{"ok": [0, 1]}` lists the codes that are
successful completions (stdout decodes normally; the actual code rides the
`x-exit-code` trailer). Absent means `[0]`. This exists for the diff(1)
class, where exit 1 means "differences found" — a result, not a failure.
Any exit code outside the ok-set is a terminal `ERR_EXECUTION_FAILED`
carrying `{exitCode, output: {stdout, stderr}}` in details.

### Value grammar

Input fields render onto argv as: strings verbatim; every other value as its
compact canonical JSON (`1000000`, `{"k":"v"}`). Booleans on flags use
presence semantics (`true` emits the flag; `false` emits the flag's `negate`
form when declared, else nothing; `count` flags repeat by value). Arrays
repeat a flag per item or spread across a variadic arg, items per the same
rules. `null` fields are omitted.

Argv assembly: flags are emitted first, in lexicographic field-name order
(long names get `--`, single-character names get `-`); positional args
follow in the command's declared arg order, with `--` inserted before the
first arg that declares `double_dash`. Field names MUST match the command's
flag or arg names exactly (an arg matches by its clean name — `file` for
`<file>...`); an input field matching neither is an error, except a
slotless stdin-routed field as above.

Limits note: argv tokens are subject to OS ceilings (per-argument
~128 KiB on Linux, ~1 MiB total on macOS). Document-valued fields SHOULD
declare `delivery` rather than ride argv as JSON tokens.

The zero-exit stdout **heuristic** (no declared `stdout` mode) is this
algorithm:

1. Trim surrounding whitespace from the captured stdout.
2. The trimmed text *triggers* bare parsing when it starts with `{` and
   ends with `}`, starts with `[` and ends with `]`, equals `null`, `true`,
   or `false` exactly, or starts with a double quote.
3. On a trigger, strict-JSON-parse the trimmed text; success yields the
   parsed value.
4. No trigger, or a triggered parse that fails, yields
   `{"stdout": <the original, untrimmed text>}`. The fallback is silent by
   design on the undeclared lane — in contrast to `"json"` mode, where a
   parse failure is an error.

Bare numbers therefore wrap under the heuristic; bindings that mean JSON
declare it.

### Diagnostics

stderr and the exit code are diagnostics, never part of the output value.
On the success path (a declared-ok exit) they ride trailing metadata:

- `x-exit-code`: always present; the exit code as a base-10 decimal string,
  a single entry.
- `x-stderr`: the captured stderr bytes as a single entry, omitted when
  stderr is empty; truncated to 64 KiB.
- `x-stderr-truncated`: present with the single entry `"true"` when
  `x-stderr` was truncated (by the 64 KiB trailer bound or the capture cap
  below).

Capture bounds: stdout and stderr are each captured up to 10 MiB. stdout
overflow is a terminal error (a truncated document cannot be decoded);
stderr overflow is NOT — diagnostics volume never fails a successful
invocation; the capture is truncated and marked. Any transport that
carries these trailers across a header-framed protocol (HTTP, gRPC) MUST
encode them byte-safely (JSON string or base64); raw child bytes are not
header-safe.

A non-ok exit is a terminal `ERR_EXECUTION_FAILED` carrying
`{exitCode, output: {stdout, stderr}}` in details, with the full captured
streams (up to the capture caps).

### Security

- **Trust model.** A binding author is a code author: `x-usage` directs
  where caller data is written (disk, stdin) and how output is
  interpreted. Treat bindings from untrusted documents accordingly. When a
  binding-invocation lane carries binding extension members from a remote
  caller, they are caller-asserted, not verified against any interface.
- **Sensitive values.** Fields whose values may be sensitive (credentials,
  context payloads) SHOULD declare `"stdin"`, not `"file"`: stdin never
  touches persistent storage, while a temp file leaves residue if the
  invoking process dies before cleanup (and may land in filesystem
  backups or snapshots).
- **Reserved tokens.** The grammar emits `-` and file paths into argv
  slots. String values that are exactly `-` or begin with `-` in operand
  position can collide with CLI option parsing; content-carrying string
  fields SHOULD be delivery-routed rather than passed as argv tokens.

### Non-portable tool features

Direct-binary dispatch (a `binary` hint in context metadata that skips the
usage spec and renders every input field as a flag on a shlex-split ref) is
a feature of this SDK, not part of the format conventions; implementations
are not required to provide it.

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
