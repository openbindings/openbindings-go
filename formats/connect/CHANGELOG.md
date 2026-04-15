# Changelog

## 0.1.0

Initial public release.

- Connect binding executor: execute unary RPCs via HTTP POST with JSON
- Interface creator: parse .proto files and inline protobuf definitions
- Same ref convention as gRPC (package.Service/Method)
- Proto-aware input marshaling when descriptors are available
- Standard auth retry flow (401 -> resolve credentials -> retry once)
- Context store integration with scheme-agnostic key normalization
