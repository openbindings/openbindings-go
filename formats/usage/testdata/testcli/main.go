// testcli is a minimal CLI binary used by integration tests.
// It exercises JSON output, exit codes, stderr, and flag/arg handling.
package main

import (
	"encoding/json"
	"fmt"
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

	case "root":
		// Root invocation mode: echo all remaining args.
		// Used to test root command (empty ref) execution.
		fmt.Println(strings.Join(rest, " "))

	default:
		// Treat as root invocation: echo all args (including cmd).
		fmt.Println(strings.Join(args, " "))
	}
}
