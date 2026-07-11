# PackageMaze CLI

`maze` is the PackageMaze command line interface. This repository contains the
public CLI source, tests, and release workflow that publishes signed release
assets for `packagemaze/setup-maze`.

The first implemented command is:

```sh
maze auth exchange-oidc
maze publish dist/* --feed <organization>/<feed>
```

`maze auth exchange-oidc` exchanges a CI OIDC identity token for a short-lived
PackageMaze Token. `maze publish` sends local artifact facts to PackageMaze,
executes the returned backend plan, uploads bytes, and waits for Publish
Finalization by default. Local and staging development use the normal endpoint
overrides.

## Build And Test

From this repository:

```sh
go mod download
go test ./...
go vet ./...
go build -o bin/maze ./cmd/maze
```

The built binary is written to:

```sh
bin/maze
```

Useful development commands:

```sh
go run ./cmd/maze --help
go run ./cmd/maze version
MAZE_OIDC_TOKEN="$OIDC_TOKEN" go run ./cmd/maze auth exchange-oidc --provider manual --feed <organization>/<feed> --purpose install
MAZE_TOKEN="$PACKAGE_MAZE_TOKEN" go run ./cmd/maze publish dist/* --feed <organization>/<feed>
```

## API Contract

PackageMaze's existing API Domain uses `/v1` routes, so this CLI defaults to:

```text
https://api.packagemaze.com/v1
```

The exchange client posts to:

```text
POST /v1/auth/ci-token
```

Wrapper actions and orbs can correlate the Tokens they request during one
setup invocation without sending generic client metadata:

```sh
maze auth exchange-oidc \
  --feed <organization>/<feed> \
  --purpose install \
  --setup-invocation-id setup-maze_0123456789abcdef0123456789abcdef
```

`--setup-invocation-id` takes precedence over
`MAZE_SETUP_INVOCATION_ID`. The value is optional, non-secret, and limited to
160 letters, numbers, dots, underscores, colons, or hyphens. Wrappers should
generate one stable random id per invocation and prefix it with their own name,
for example `setup-maze_…` or `circleci-maze-orb_…`.

The prefix is caller-supplied provenance, not provider-signed Build evidence.
PackageMaze uses the opaque id only for correlation; neither the CLI nor the
service infers human intent from it. Legacy `--client-context-json` remains
accepted for explicit rolling compatibility, but its contents are
caller-supplied and unverified; it is not the Build evidence or correlation
contract. As of v0.0.4 the CLI no longer collects or sends CI environment
metadata automatically.

PackageMaze returns the server-derived Build handle separately. JSON,
`github-output`, and shell output prefer `build_id` while also emitting the
identical compatibility alias `ci_session_id`. Shell output names them
`MAZE_BUILD_ID` and `MAZE_CI_SESSION_ID`. Token-only output remains exactly the
Token Secret followed by a newline. Use `build_id` with PackageMaze's Build
Report surfaces; do not derive it from `setup_invocation_id`.

The Hosted MCP compatibility capability remains `get_ci_session_report` and
currently names its input `ci_session_id`. Pass the identical server-derived
handle emitted by the CLI; the compatibility name does not turn it into a
different object from the Build.

The response `purpose` remains the requested exchange purpose (`install`,
`publish`, `docker-build`, or `test`). PackageMaze stores the resulting
short-lived credential with Token Purpose `cicd`; that server-side Token
classification does not replace the exchange purpose in CLI output.

Use `--base-url` to change the API Domain base URL or `--api-url` to override
the full API root. `http` URLs are rejected unless they target localhost and
`--allow-insecure-localhost` is set.

## Usage

```sh
maze auth exchange-oidc \
  --feed <organization>/<feed> \
  --purpose install
```

Required flags:

- `--feed <organization>/<feed>`
- `--purpose {install | publish | docker-build | test}`

Publishing also requires:

- `--package <package-name>`

Output formats:

- `--format token`
- `--format json` or `--json`
- `--format shell`
- `--format github-output`

For local API development, point the same command at the local API root and
provide an OIDC token through stdin, a file, or an environment variable:

```sh
printf '%s' "$OIDC_TOKEN" | maze auth exchange-oidc \
  --base-url http://127.0.0.1:8787 \
  --allow-insecure-localhost \
  --provider manual \
  --oidc-token-stdin \
  --feed <organization>/<feed> \
  --purpose install \
  --format json
```

## Publish

```sh
maze publish dist/* --feed <organization>/<feed>
maze publish ./package-1.0.0.tgz --feed <organization>/<feed> --json
```

`maze publish` is a generic executor for PackageMaze Publish Sessions. It
computes filename, byte size, SHA-256, and content type for each path, asks the
Feed for a versioned publish plan, uploads through the instructed direct R2
multipart target, reports completion, and waits for the backend status contract.
PackageMaze owns npm and PyPI package/version decisions.

Authentication uses a PackageMaze Token with publish scope:

- `MAZE_TOKEN`
- `--token-file <path>`
- `--token-stdin`

Useful flags:

- `--package-client-url <url>` for local or staging Package Client Domain tests
- `--package <name>` and `--version <version>` as optional backend hints
- `--wait=false` to return after upload completion
- `--json` or `--format json` for CI-safe structured output

## GitHub Actions

```yaml
permissions:
  contents: read
  id-token: write

steps:
  - uses: actions/checkout@v4
  - id: packagemaze
    uses: packagemaze/setup-maze@v0.0.3
    with:
      feed: <organization>/<feed>
      purpose: install
  - run: npm ci
    env:
      NODE_AUTH_TOKEN: ${{ steps.packagemaze.outputs.token }}
```

Use `maze auth exchange-oidc` directly when building wrapper actions or
troubleshooting token exchange. For workflow outputs:

```sh
maze auth exchange-oidc \
  --feed <organization>/<feed> \
  --purpose install \
  --format github-output \
  --output-name package_maze_token
```

The `github-output` format writes the requested Token output plus
`artifact_protocol`, `feed_base_url`, `build_id`, and the compatibility
`ci_session_id` so wrapper actions can choose protocol-specific setup, use
canonical registry URLs, and link users or Agents to the exact Build without
asking workflows to duplicate Feed metadata.

The `shell` format writes `MAZE_TOKEN`, `MAZE_TOKEN_EXPIRES_AT`, `MAZE_FEED`,
`MAZE_FEED_BASE_URL`, `MAZE_PURPOSE`, `MAZE_ARTIFACT_PROTOCOL`,
`MAZE_BUILD_ID`, and compatibility `MAZE_CI_SESSION_ID` exports for wrapper
actions that need to consume exchange metadata without using GitHub step
outputs. Build exports are omitted when an older PackageMaze deployment does
not return a Build handle during a rolling upgrade.

## GitLab CI/CD

The CLI can acquire an explicitly configured GitLab OIDC token, but PackageMaze
does not yet accept GitLab CI/CD identities in production. The command will
surface the Worker's structured `unsupported_provider` diagnostic until a
first-class GitLab integration ships.

```yaml
id_tokens:
  MAZE_OIDC_TOKEN:
    aud: https://api.packagemaze.com

script:
  - maze auth exchange-oidc --feed <organization>/<feed> --purpose install
```

## CircleCI

Provide `MAZE_OIDC_TOKEN` or install the CircleCI CLI in the job so the command
can run:

```sh
circleci run oidc get --claims '{"aud":"https://api.packagemaze.com"}'
```

Then run:

```sh
maze auth exchange-oidc --feed <organization>/<feed> --purpose install
```

## Manual Token Input

Manual mode avoids a plain `--oidc-token` flag so token values do not leak into
shell history or process listings.

```sh
printf '%s' "$OIDC_TOKEN" | maze auth exchange-oidc \
  --provider manual \
  --oidc-token-stdin \
  --feed <organization>/<feed> \
  --purpose install
```

## Security Notes

- The raw OIDC token is never printed.
- The PackageMaze Token is printed only through the requested output format.
- `--verbose` writes non-secret diagnostics to stderr.
- `github-output` writes to `$GITHUB_OUTPUT` and emits an `add-mask` workflow
  command for the PackageMaze Token.
- Tokens are not written to project files or persistent config.

## Production Readiness

The initial command is a usable first slice. The broader bar for a robust,
portable, best-in-class CLI is tracked in
[`docs/production-readiness.md`](docs/production-readiness.md).
