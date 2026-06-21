# PackageMaze CLI Agent Notes

- Keep the executable name `maze`.
- Keep this repository limited to CLI source, tests, docs, and release
  automation.
- Use `go test ./...`, `go vet ./...`, `gofmt`, and `go build ./cmd/maze`.
- Keep command constructors dependency-injected so tests never need real
  network calls, shell commands, or developer machine credentials.
- Preserve PackageMaze product terminology.
- Treat PackageMaze as artifact-protocol-first. Do not describe CLI behavior as
  language-first.
- Align CLI command names, API paths, and future MCP capability names when a
  workflow crosses surfaces.
- Do not add a plain `--oidc-token` flag. Prefer stdin, files, or environment
  variables so tokens do not leak through shell history or process listings.
- Do not log raw OIDC tokens or PackageMaze Token Secrets.
- Keep stdout for requested machine-readable data and stderr for human
  diagnostics.
- Test local and staging workflows with endpoint overrides or dependency
  injection rather than alternate product-visible command modes.
