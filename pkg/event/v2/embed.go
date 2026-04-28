package eventv2

import (
	_ "embed"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed v2.0.schema.json
var embeddedSchema []byte

// EmbeddedSchema returns the bytes of the v2.0 JSON Schema that ships with
// this package. The canonical copy lives at docs/schema/v2.0.json; the file
// next to this one is a build-time mirror so the runtime binary doesn't need
// the docs tree at startup. A drift test asserts the two stay byte-identical.
func EmbeddedSchema() []byte {
	return embeddedSchema
}

// CompileEmbeddedSchema compiles the embedded v2.0 schema once. Callers should
// hold the returned *jsonschema.Schema for the process lifetime and reuse it
// across requests via ValidateAny.
func CompileEmbeddedSchema() (*jsonschema.Schema, error) {
	return compileFromBytes(embeddedSchema)
}
