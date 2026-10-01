//go:build ignore

// Command appliedtext prints the git ref of the specification text this SDK
// applies, read from version.go by parsing it, never by matching text:
// appliedRevision, a full 40-hex commit, while appliedRelease names a working
// draft; once the release is out (appliedRevision ""), the release tag
// v<appliedRelease>. It refuses, with exit status 1, a constant that is
// missing, declared twice, or not a string literal, a release that is not
// SemVer 2.0.0, and a revision that is neither empty nor a full 40-hex
// commit.
//
// Usage: go run .github/ci/appliedtext.go MODULE-ROOT (it reads MODULE-ROOT/version.go)
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var (
	semver       = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)
	fullRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: appliedtext MODULE-ROOT")
		os.Exit(2)
	}
	ref, err := appliedRef(filepath.Join(os.Args[1], "version.go"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "appliedtext:", err)
		os.Exit(1)
	}
	fmt.Println(ref)
}

func appliedRef(path string) (string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	found := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if name.Name != "appliedRelease" && name.Name != "appliedRevision" {
					continue
				}
				if _, twice := found[name.Name]; twice {
					return "", fmt.Errorf("%s is declared twice", name.Name)
				}
				var lit *ast.BasicLit
				if i < len(vs.Values) {
					lit, _ = vs.Values[i].(*ast.BasicLit)
				}
				if lit == nil || lit.Kind != token.STRING {
					return "", fmt.Errorf("%s is not declared with a string literal", name.Name)
				}
				if found[name.Name], err = strconv.Unquote(lit.Value); err != nil {
					return "", err
				}
			}
		}
	}
	release, hasRelease := found["appliedRelease"]
	revision, hasRevision := found["appliedRevision"]
	switch {
	case !hasRelease || !hasRevision:
		return "", fmt.Errorf("%s declares no appliedRelease or no appliedRevision", path)
	case !semver.MatchString(release):
		return "", fmt.Errorf("appliedRelease %q is not a SemVer 2.0.0 version", release)
	case revision == "":
		return "v" + release, nil
	case !fullRevision.MatchString(revision):
		return "", fmt.Errorf("appliedRevision %q is not a full 40-hex commit", revision)
	}
	return revision, nil
}
