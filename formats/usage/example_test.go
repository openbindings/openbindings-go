package usage_test

import (
	"fmt"

	"github.com/openbindings/openbindings-go/formats/usage"
)

func ExampleSpec_Meta() {
	// Create a spec from nodes (normally via ParseKDL)
	spec := &usage.Spec{
		Nodes: []usage.Node{
			{Name: "name", Args: []usage.Value{{Raw: "My CLI"}}},
			{Name: "bin", Args: []usage.Value{{Raw: "mycli"}}},
			{Name: "version", Args: []usage.Value{{Raw: "1.0.0"}}},
		},
	}

	meta := spec.Meta()
	fmt.Printf("%s v%s (bin: %s)\n", meta.Name, meta.Version, meta.Bin)
	// Output: My CLI v1.0.0 (bin: mycli)
}

func ExampleSpec_Walk() {
	spec := &usage.Spec{
		Nodes: []usage.Node{
			{
				Name: "cmd",
				Args: []usage.Value{{Raw: "config"}},
				Children: []usage.Node{
					{Name: "cmd", Args: []usage.Value{{Raw: "set"}}},
					{Name: "cmd", Args: []usage.Value{{Raw: "get"}}},
				},
			},
			{Name: "cmd", Args: []usage.Value{{Raw: "version"}}},
		},
	}

	spec.Walk(func(path []string, cmd usage.Command) {
		fmt.Println(path)
	})
	// Output:
	// [config]
	// [config set]
	// [config get]
	// [version]
}

func ExampleSpec_FindCommand() {
	spec := &usage.Spec{
		Nodes: []usage.Node{
			{
				Name: "cmd",
				Args: []usage.Value{{Raw: "config"}},
				Props: map[string]usage.Value{"help": {Raw: "Manage config"}},
				Children: []usage.Node{
					{
						Name:  "cmd",
						Args:  []usage.Value{{Raw: "set"}},
						Props: map[string]usage.Value{"help": {Raw: "Set a value"}},
					},
				},
			},
		},
	}

	cmd := spec.FindCommand([]string{"config", "set"})
	if cmd != nil {
		fmt.Println(cmd.Help)
	}
	// Output: Set a value
}

func ExampleArg_IsRequired() {
	required := usage.Arg{Name: "<file>"}
	optional := usage.Arg{Name: "[file]"}

	fmt.Printf("<file> required: %v\n", required.IsRequired())
	fmt.Printf("[file] required: %v\n", optional.IsRequired())
	// Output:
	// <file> required: true
	// [file] required: false
}

func ExampleArg_CleanName() {
	arg := usage.Arg{Name: "<files>..."}
	fmt.Println(arg.CleanName())
	// Output: files
}

func ExampleFlag_ParseUsage() {
	flag := usage.Flag{Usage: "-u --user <username>"}
	parsed := flag.ParseUsage()

	fmt.Printf("Short: %v\n", parsed.Short)
	fmt.Printf("Long: %v\n", parsed.Long)
	fmt.Printf("Arg: %s\n", parsed.ArgName)
	// Output:
	// Short: [u]
	// Long: [user]
	// Arg: username
}

func ExampleFlag_PrimaryName() {
	f1 := usage.Flag{Usage: "-v --verbose"}
	f2 := usage.Flag{Usage: "-f"}

	fmt.Println(f1.PrimaryName()) // prefers long
	fmt.Println(f2.PrimaryName()) // falls back to short
	// Output:
	// verbose
	// f
}

func ExampleCommand_AllFlags() {
	globalVerbose := usage.Flag{Usage: "-v --verbose", Global: true}
	localForce := usage.Flag{Usage: "-f --force"}

	cmd := usage.Command{
		Name:  "deploy",
		Flags: []usage.Flag{localForce},
	}

	// Merge inherited global flags
	allFlags := cmd.AllFlags([]usage.Flag{globalVerbose})
	for _, f := range allFlags {
		fmt.Println(f.PrimaryName())
	}
	// Output:
	// verbose
	// force
}

func ExampleSpec_Validate() {
	spec := &usage.Spec{
		Nodes: []usage.Node{
			{Name: "name", Args: []usage.Value{{Raw: "My CLI"}}},
			{Name: "bin", Args: []usage.Value{{Raw: "mycli"}}},
			{
				Name:  "cmd",
				Args:  []usage.Value{{Raw: "run"}},
				Props: map[string]usage.Value{"help": {Raw: "Run a task"}},
			},
		},
	}

	if err := spec.Validate(); err != nil {
		fmt.Println("invalid:", err)
	} else {
		fmt.Println("valid")
	}
	// Output: valid
}

func ExampleSpec_Validate_strict() {
	spec := &usage.Spec{
		Nodes: []usage.Node{
			{Name: "name", Args: []usage.Value{{Raw: "My CLI"}}},
			{Name: "unknownNode", Args: []usage.Value{{Raw: "value"}}},
		},
	}

	// Default: unknown nodes are allowed (forward-compat)
	err := spec.Validate()
	fmt.Println("default:", err == nil)

	// Strict: unknown nodes are rejected
	err = spec.Validate(usage.WithRejectUnknownNodes())
	fmt.Println("strict:", err != nil)
	// Output:
	// default: true
	// strict: true
}
