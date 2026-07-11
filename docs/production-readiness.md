# PackageMaze CLI Production Readiness

This checklist is informed by a shallow reference checkout of GitHub CLI
`cli/cli` at `da68cb8` (`2026-06-08`), especially its command factory pattern,
IO stream abstraction, generated docs/completions, cross-platform build scripts,
and command contract tests.

The current `maze auth exchange-oidc` implementation is an initial production-
intent slice. The CLI should not be considered "best in the world" until the
items below are deliberately addressed.

## Command Experience

- [ ] Commands follow stable noun/verb naming and keep API, CLI, and MCP
  capability names aligned.
- [ ] Every command has high-quality `Short`, `Long`, examples, environment
  annotations, and predictable exit codes.
- [ ] `--help` output is generated into reference docs and checked in CI.
- [ ] Shell completions are generated for bash, zsh, fish, and PowerShell.
- [ ] Machine output is stable, tested, and versioned where needed.
- [ ] Human diagnostics are actionable and never mixed into data stdout.
- [ ] JSON output has tested field contracts and clear compatibility rules.

## Portability

- [ ] The CLI builds and tests on macOS, Linux, and Windows for amd64 and arm64.
- [ ] Paths, file permissions, line endings, and executable suffixes are handled
  per platform.
- [ ] Terminal behavior is abstracted behind testable IO streams with TTY
  detection, width handling, color control, and `NO_COLOR` support.
- [ ] Interactive prompts are disabled or fail clearly in CI and other
  non-interactive contexts.
- [ ] Locales, Unicode, and narrow terminal widths are covered by tests.

## Security

- [ ] Raw OIDC tokens and PackageMaze Token Secrets are never logged, persisted,
  or included in telemetry.
- [ ] Secret-bearing inputs avoid command-line arguments.
- [ ] Token output is limited to explicit output formats.
- [ ] GitHub Actions output masking is tested end-to-end.
- [ ] Local config and cache files use restrictive permissions.
- [ ] TLS verification is on by default, with `http` allowed only for explicit
  localhost development.
- [ ] Threat-model notes exist for OIDC trust, token exchange, logs, config,
  shell completions, and crash/error reports.

## API And Auth

- [ ] HTTP clients set timeouts, user agent/version headers, retry policy,
  proxy behavior, and rate-limit handling intentionally.
- [ ] Backend error responses map to a stable CLI error taxonomy.
- [x] API contract tests cover request/response schemas and redaction.
- [x] Wrapper setup correlation uses the typed, optional
  `setup_invocation_id` request field with tested flag-over-environment
  precedence; the CLI does not collect CI environment metadata automatically,
  and explicit legacy client metadata is not treated as Build evidence.
- [x] Exchange responses accept preferred `build_id` and compatibility
  `ci_session_id`, reject contradictory aliases, and expose the server-derived
  handle in machine output without changing token-only stdout.
- [x] The API Domain exposes `POST /v1/auth/ci-token` with the same names used
  by CLI and future MCP capabilities.
- [ ] CI provider trust rules are testable with GitHub Actions, GitLab CI/CD,
  CircleCI, and manual token input.
- [ ] Multiple PackageMaze environments can be selected without confusing API
  Domain, Web Domain, and Package Client Domain behavior.

## Configuration

- [ ] Environment variable, flag, and config-file precedence is documented and
  tested.
- [ ] Multi-account and multi-environment configuration has explicit storage
  boundaries.
- [ ] Config never stores Token Secrets unless a future design chooses an OS
  credential store and documents that decision.
- [ ] Commands can run in hermetic CI without reading unrelated user config.

## Testing

- [x] Unit tests cover validation, output, API clients, provider detection, and
  command parsing.
- [x] Command tests run through Cobra with injected IO, env, HTTP clients, and
  shell runners.
- [x] End-to-end tests exercise the real command entrypoint against a local
  PackageMaze-compatible API.
- [ ] CI-provider template tests verify GitHub Actions, GitLab, and CircleCI
  snippets.
- [ ] Race tests and fuzz tests cover parsers, URL validation, output escaping,
  and response decoding.
- [ ] Golden tests lock help text, docs, completions, and JSON fields.

## Build, Release, And Supply Chain

- [ ] Builds inject version, commit, date, and build source metadata.
- [ ] Release automation produces signed archives and platform packages.
- [ ] Checksums, SBOMs, provenance attestations, and license reports are
  generated and verified.
- [ ] Homebrew, npm-style wrapper if chosen, apt, rpm, winget, and direct
  archive install paths are documented.
- [ ] Upgrade and rollback paths are documented.
- [ ] Dependency updates are automated and security-scanned.

## Observability And Support

- [ ] `--verbose` and future `--debug` modes provide useful diagnostics without
  secrets.
- [ ] Error messages include remediation steps and support correlation IDs when
  the backend returns them.
- [ ] Optional telemetry, if ever added, has explicit consent, documented data
  fields, and a hard no-secrets guarantee.
- [ ] Logs and traces can distinguish CLI bugs, backend unavailability, trust
  rule failures, and CI provider misconfiguration.

## PackageMaze-Specific Completeness

- [ ] CLI concepts use Feed, Artifact Protocol, Format Adapter, Token,
  Token Secret, Token Scope, API Domain, and Package Client Domain consistently.
- [ ] Auth commands support install, publish, docker-build, and test workflows
  without privileging one Artifact Protocol.
- [ ] Future setup commands can configure npm, PyPI, Maven, NuGet, OCI/Docker,
  Cargo, RubyGems, Composer, Conda, and other Artifact Protocol clients through
  adapter-owned instructions.
- [ ] Published setup workflows have meaningful end-to-end tests before being
  documented as supported.
