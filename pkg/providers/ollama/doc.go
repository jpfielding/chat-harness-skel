// Package ollama implements chat.Provider against a local Ollama server.
//
// Status: experimental. Ollama tool-use support depends on the server
// version; the adapter probes /api/version at New() time and version-gates
// Capabilities.Tools. There is no simulated-tools fallback.
package ollama
