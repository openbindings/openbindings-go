package openbindings

import (
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema"
)

func exactCountCompiler() *jsonschema.Compiler { return schemacompiler.New() }
