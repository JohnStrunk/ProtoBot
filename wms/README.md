# WMS Implementations

Each WMS adapter implementation belongs in its own native component under
this directory, for example:

```text
wms/github/
wms/jira/
```

Implementations may use Go, Python, TypeScript, Rust, or another appropriate
language and own their build and test configuration.

The Go module in this directory contains two backend-neutral components:

- `validation/` — the pure `validation-rules/v1` lifecycle evaluator;
- `memory/` — an atomic in-memory WMS adapter for lifecycle and Drafting
  Table conformance tests.

Run the Go checks from this directory with `go test ./...` and `go vet ./...`.
