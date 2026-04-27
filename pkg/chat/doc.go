// Package chat defines the normalized chat, tool, and streaming schema that
// all provider adapters convert to and from.
//
// Status: experimental. The public API may change in breaking ways across
// minor versions until the normalized contract has survived integration of
// several materially different providers.
//
// The schema is not a superset of any single provider. It is the harness's
// own model; adapters in pkg/providers translate to/from it. Provider-specific
// fields that do not portably round-trip travel in ContentBlock.ProviderMetadata
// with no portability guarantee.
package chat
