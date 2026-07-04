// testcli is a minimal CLI binary used by integration tests.
// It exercises JSON output, exit codes, stderr, and flag/arg handling.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "no command")
		os.Exit(1)
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "json":
		// Output structured JSON from key=value flag pairs.
		result := map[string]any{}
		for _, arg := range rest {
			if k, v, ok := strings.Cut(arg, "="); ok {
				result[k] = v
			}
		}
		data, _ := json.Marshal(result)
		fmt.Println(string(data))

	case "fail":
		// Exit with a non-zero code.
		code := 1
		if len(rest) > 0 {
			fmt.Fprintln(os.Stderr, strings.Join(rest, " "))
		}
		os.Exit(code)

	case "mixed":
		// Write to both stdout and stderr.
		fmt.Println("stdout line")
		fmt.Fprintln(os.Stderr, "stderr line")

	case "echo":
		// Simple echo of remaining args.
		fmt.Println(strings.Join(rest, " "))

	case "slurp":
		// Read all of stdin and echo it back with the remaining args, as
		// JSON. Exercises stdin delivery: the routed field's bytes arrive
		// here, and its argv slot ("-") shows up in args.
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read stdin:", err)
			os.Exit(1)
		}
		out, _ := json.Marshal(map[string]any{"stdin": string(data), "args": rest})
		fmt.Println(string(out))

	case "readfile":
		// Read the file named by the first arg and emit its path and
		// content as JSON. Exercises temp-file materialization.
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, "readfile: missing path")
			os.Exit(1)
		}
		data, err := os.ReadFile(rest[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "readfile:", err)
			os.Exit(1)
		}
		out, _ := json.Marshal(map[string]any{"path": rest[0], "content": string(data)})
		fmt.Println(string(out))

	case "num":
		// A bare number on stdout: wraps under the heuristic, parses under
		// a declared JSON lane.
		fmt.Println("42")

	case "prose":
		// Plain human text on stdout (with a stderr aside): invalid on a
		// declared JSON lane, the raw value on a declared text lane.
		fmt.Println("all good")
		fmt.Fprintln(os.Stderr, "checked 3 things")

	case "root":
		// Root invocation mode: echo all remaining args.
		// Used to test root command (empty ref) execution.
		fmt.Println(strings.Join(rest, " "))

	default:
		// Treat as root invocation: echo all args (including cmd).
		fmt.Println(strings.Join(args, " "))
	}
}
