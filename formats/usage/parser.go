package usage

import "strings"

func (s *Spec) Meta() Meta {
	return decodeMeta(s.Nodes)
}

func (s *Spec) Commands() []Command {
	return decodeCommands(s.Nodes)
}

func (s *Spec) Flags() []Flag {
	return decodeFlags(s.Nodes)
}

func (s *Spec) Args() []Arg {
	return decodeArgs(s.Nodes)
}

func (s *Spec) Completes() []Complete {
	return decodeCompletes(s.Nodes)
}

func (s *Spec) Config() *Config {
	return decodeConfigFromNodes(s.Nodes)
}

func decodeMeta(nodes []Node) Meta {
	meta := Meta{}
	for _, n := range nodes {
		switch n.Name {
		case "min_usage_version":
			meta.MinUsageVersion = firstString(n)
		case "name":
			meta.Name = firstString(n)
		case "bin":
			meta.Bin = firstString(n)
		case "about":
			meta.About = firstString(n)
		case "usage":
			meta.Usage = firstString(n)
		case "version":
			meta.Version = firstString(n)
		case "author":
			meta.Author = firstString(n)
		case "license":
			meta.License = firstString(n)
		case "before_help":
			meta.BeforeHelp = firstString(n)
		case "after_help":
			meta.AfterHelp = firstString(n)
		case "before_long_help":
			meta.BeforeLongHelp = firstString(n)
		case "long_about":
			meta.LongAbout = firstString(n)
		case "after_long_help":
			meta.AfterLongHelp = firstString(n)
		case "source_code_link_template":
			meta.SourceCodeLinkTemplate = firstString(n)
		case "include":
			// The usage spec uses include file="./path". Fall back to positional arg for compat.
			s := stringProp(n, "file")
			if s == "" {
				s = firstString(n)
			}
			if s != "" {
				meta.Includes = append(meta.Includes, s)
			}
		case "example":
			meta.Examples = append(meta.Examples, decodeExample(n))
		default:
			// Skip known structural nodes — they're accessed via dedicated methods.
			switch n.Name {
			case "cmd", "flag", "arg", "complete", "config", "config_file", "config_alias":
				// structural, not metadata
			default:
				meta.Unknown = append(meta.Unknown, n)
			}
		}
	}
	return meta
}

func decodeCommands(nodes []Node) []Command {
	var out []Command
	for _, n := range nodes {
		if n.Name == "cmd" {
			out = append(out, decodeCommand(n))
		}
	}
	return out
}

func decodeFlags(nodes []Node) []Flag {
	var out []Flag
	for _, n := range nodes {
		if n.Name == "flag" {
			out = append(out, decodeFlag(n))
		}
	}
	return out
}

func decodeArgs(nodes []Node) []Arg {
	var out []Arg
	for _, n := range nodes {
		if n.Name == "arg" {
			out = append(out, decodeArg(n))
		}
	}
	return out
}

func decodeCompletes(nodes []Node) []Complete {
	var out []Complete
	for _, n := range nodes {
		if n.Name == "complete" {
			out = append(out, decodeComplete(n))
		}
	}
	return out
}

func decodeCommand(n Node) Command {
	cmd := Command{
		Node:               n,
		Name:               firstString(n),
		Hide:               boolProp(n, "hide"),
		SubcommandRequired: boolProp(n, "subcommand_required"),
		BeforeHelp:         stringPropOrChild(n, "before_help"),
		Help:               stringPropOrChildOrArg(n, "help", 1), // spec: cmd "name" "help text"
		AfterHelp:          stringPropOrChild(n, "after_help"),
		BeforeLongHelp:     stringPropOrChild(n, "before_long_help"),
		LongHelp:           stringPropOrChild(n, "long_help"),
		AfterLongHelp:      stringPropOrChild(n, "after_long_help"),
	}

	for _, c := range n.Children {
		switch c.Name {
		case "alias":
			cmd.Aliases = append(cmd.Aliases, decodeAlias(c))
		case "example":
			cmd.Examples = append(cmd.Examples, decodeExample(c))
		case "flag":
			cmd.Flags = append(cmd.Flags, decodeFlag(c))
		case "arg":
			cmd.Args = append(cmd.Args, decodeArg(c))
		case "cmd":
			cmd.Commands = append(cmd.Commands, decodeCommand(c))
		case "complete":
			cmd.Completes = append(cmd.Completes, decodeComplete(c))
		case "mount":
			cmd.Mounts = append(cmd.Mounts, decodeMount(c))
		case "before_help", "help", "after_help", "before_long_help", "long_help", "after_long_help":
			// handled via stringPropOrChild
		default:
			cmd.Unknown = append(cmd.Unknown, c)
		}
	}

	return cmd
}

func decodeFlag(n Node) Flag {
	flag := Flag{
		Node:           n,
		Usage:          firstString(n),
		Hide:           boolProp(n, "hide"),
		Global:         boolProp(n, "global"),
		Count:          boolProp(n, "count"),
		Required:       boolProp(n, "required"),
		Var:            boolProp(n, "var"),
		VarMin:         intProp(n, "var_min"),
		VarMax:         intProp(n, "var_max"),
		Default:        propValue(n, "default"),
		Negate:         stringProp(n, "negate"),
		Env:            stringProp(n, "env"),
		ConfigKey:      stringProp(n, "config"),
		RequiredIf:     stringProp(n, "required_if"),
		RequiredUnless: stringProp(n, "required_unless"),
		Overrides:      stringProp(n, "overrides"),
		Help:           stringPropOrChildOrArg(n, "help", 1), // spec: flag "usage" "help text"
		LongHelp:       stringPropOrChild(n, "long_help"),
	}

	// Detect variadic flag shorthand: flag "--include..." implies var=#true.
	// The spec allows trailing "..." on the flag name itself as shorthand for var=#true.
	if !flag.Var && strings.Contains(flag.Usage, "...") {
		// Check if the "..." is on the flag name (before any arg), not on an arg like "<pattern>..."
		parts := strings.Fields(flag.Usage)
		for _, p := range parts {
			if (strings.HasPrefix(p, "-") || strings.HasPrefix(p, "--")) && strings.HasSuffix(p, "...") {
				flag.Var = true
				break
			}
		}
	}

	for _, c := range n.Children {
		switch c.Name {
		case "alias":
			flag.Aliases = append(flag.Aliases, decodeAlias(c))
		case "arg":
			flag.Args = append(flag.Args, decodeArg(c))
		case "choices":
			flag.Choices = append(flag.Choices, stringsFromArgs(c)...)
		case "help", "long_help":
			// handled via stringPropOrChild
		default:
			flag.Unknown = append(flag.Unknown, c)
		}
	}

	return flag
}

func decodeArg(n Node) Arg {
	arg := Arg{
		Node:       n,
		Name:       firstString(n),
		Default:    propValue(n, "default"),
		Env:        stringProp(n, "env"),
		Parse:      stringProp(n, "parse"),
		Var:        boolProp(n, "var"),
		VarMin:     intProp(n, "var_min"),
		VarMax:     intProp(n, "var_max"),
		Help:       stringPropOrChild(n, "help"),
		LongHelp:   stringPropOrChild(n, "long_help"),
		DoubleDash: stringProp(n, "double_dash"),
		Hide:       boolProp(n, "hide"),
	}

	for _, c := range n.Children {
		switch c.Name {
		case "choices":
			arg.Choices = append(arg.Choices, stringsFromArgs(c)...)
		case "help", "long_help":
			// handled via stringPropOrChild
		default:
			arg.Unknown = append(arg.Unknown, c)
		}
	}

	return arg
}

func decodeExample(n Node) Example {
	ex := Example{
		Node:   n,
		Args:   stringsFromArgs(n),
		Header: stringProp(n, "header"),
		Help:   stringProp(n, "help"),
		Lang:   stringProp(n, "lang"),
	}
	if len(ex.Args) >= 2 && ex.Header == "" {
		ex.Header = ex.Args[0]
		ex.Code = ex.Args[1]
	} else if len(ex.Args) == 1 {
		ex.Code = ex.Args[0]
	}
	for _, c := range n.Children {
		ex.Unknown = append(ex.Unknown, c)
	}
	return ex
}

func decodeAlias(n Node) Alias {
	return Alias{
		Node:  n,
		Names: stringsFromArgs(n),
		Hide:  boolProp(n, "hide"),
	}
}

func decodeMount(n Node) Mount {
	return Mount{
		Node: n,
		Run:  stringProp(n, "run"),
	}
}

func decodeComplete(n Node) Complete {
	return Complete{
		Node:         n,
		Target:       firstString(n),
		Run:          stringProp(n, "run"),
		Descriptions: boolProp(n, "descriptions"),
	}
}

func decodeConfigFromNodes(nodes []Node) *Config {
	var cfg Config
	var hasConfig bool
	for _, n := range nodes {
		switch n.Name {
		case "config":
			decoded := decodeConfig(n)
			cfg = decoded
			hasConfig = true
		case "config_file":
			if !hasConfig {
				cfg = Config{}
			}
			cfg.Files = append(cfg.Files, decodeConfigFile(n))
			hasConfig = true
		case "config_alias":
			if !hasConfig {
				cfg = Config{}
			}
			cfg.Aliases = append(cfg.Aliases, decodeConfigAlias(n))
			hasConfig = true
		}
	}
	if !hasConfig {
		return nil
	}
	return &cfg
}

func decodeConfig(n Node) Config {
	cfg := Config{Node: &n}
	for _, c := range n.Children {
		switch c.Name {
		case "file":
			cfg.Files = append(cfg.Files, decodeConfigFile(c))
		case "default":
			cfg.Defaults = append(cfg.Defaults, decodeConfigDefault(c))
		case "alias":
			cfg.Aliases = append(cfg.Aliases, decodeConfigAlias(c))
		default:
			cfg.Unknown = append(cfg.Unknown, c)
		}
	}
	return cfg
}

func decodeConfigFile(n Node) ConfigFile {
	return ConfigFile{
		Node:   n,
		Path:   firstString(n),
		FindUp: boolProp(n, "findup"),
		Format: stringProp(n, "format"),
	}
}

func decodeConfigDefault(n Node) ConfigDefault {
	if len(n.Args) < 2 {
		return ConfigDefault{Node: n}
	}
	return ConfigDefault{
		Node:  n,
		Key:   n.Args[0].String(),
		Value: n.Args[1].Raw,
	}
}

func decodeConfigAlias(n Node) ConfigAlias {
	args := stringsFromArgs(n)
	return ConfigAlias{
		Node: n,
		From: firstOrEmpty(args),
		To:   tail(args),
	}
}

func firstString(n Node) string {
	if len(n.Args) == 0 {
		return ""
	}
	return n.Args[0].String()
}

func stringsFromArgs(n Node) []string {
	if len(n.Args) == 0 {
		return nil
	}
	out := make([]string, 0, len(n.Args))
	for _, a := range n.Args {
		if s := a.String(); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func stringProp(n Node, key string) string {
	if v, ok := n.Props[key]; ok {
		return v.String()
	}
	return ""
}

func stringPropOrChild(n Node, key string) string {
	if s := stringProp(n, key); s != "" {
		return s
	}
	for _, c := range n.Children {
		if c.Name == key {
			return firstString(c)
		}
	}
	return ""
}

func boolProp(n Node, key string) bool {
	if v, ok := n.Props[key]; ok {
		if b, ok := v.Bool(); ok {
			return b
		}
	}
	return false
}

func intProp(n Node, key string) *int {
	if v, ok := n.Props[key]; ok {
		if i, ok := v.Int(); ok {
			return &i
		}
	}
	return nil
}

func propValue(n Node, key string) any {
	if v, ok := n.Props[key]; ok {
		return v.Raw
	}
	return nil
}

// stringPropOrChildOrArg checks (in order): property, child node, then positional arg at argIdx.
// This supports the Usage spec pattern where help text can be provided as a property,
// a child node, or a positional argument (e.g., cmd "name" "help text").
func stringPropOrChildOrArg(n Node, key string, argIdx int) string {
	if s := stringPropOrChild(n, key); s != "" {
		return s
	}
	if argIdx >= 0 && argIdx < len(n.Args) {
		return n.Args[argIdx].String()
	}
	return ""
}

func firstOrEmpty(in []string) string {
	if len(in) == 0 {
		return ""
	}
	return in[0]
}

func tail(in []string) []string {
	if len(in) <= 1 {
		return nil
	}
	return in[1:]
}
