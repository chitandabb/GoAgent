package conversationmemory

import "github.com/eino-contrib/jsonschema"

const ResponseSchemaName = "conversation_memory_v1"

// PayloadJSONSchema returns the provider-facing structural contract. Trusted
// source identity, incremental immutability, and lineage invariants remain
// enforced by ValidatePayload after model generation.
func PayloadJSONSchema() *jsonschema.Schema {
	reflector := jsonschema.Reflector{Anonymous: true, DoNotReference: true}
	return reflector.Reflect(Payload{})
}
