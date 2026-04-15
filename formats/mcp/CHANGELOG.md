# Changelog

## 0.1.0 — 2026-03-31

Initial public release.

- MCP binding executor (`mcp@2025-11-25`) via Streamable HTTP transport
- Tool discovery and execution (`tools/list`, `tools/call`)
- Resource reading for static resources and URI templates (`resources/read`)
- Prompt retrieval with argument mapping (`prompts/get`)
- Structured content support for tool results (`structuredContent`, `outputSchema`)
- Content type handling (text, image, audio, resource links, embedded resources)
- Credential application (bearer, apiKey) as HTTP headers
- Interface creation from MCP server capabilities with deterministic output
