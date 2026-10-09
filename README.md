# PackageMaze CLI

[PackageMaze](https://www.packagemaze.com/) is a private package registry for
npm and PyPI. It hosts the packages you publish, serves public packages from
npmjs.com and PyPI under the rules you set, and keeps a record of every version
it delivers.

`maze` is the PackageMaze command line tool. This repository contains the
public CLI source, tests, and the release pipeline that publishes the builds
[`packagemaze/setup-maze`](https://github.com/packagemaze/setup-maze) installs,
each with a checksum and a verifiable record of the build that produced it.

If a coding agent is setting up PackageMaze for you, point it at the
[Agent quickstart](https://www.packagemaze.com/docs/agent-quickstart/).
[Connect your agent](https://www.packagemaze.com/docs/connect-your-agent/)
explains how to give an agent direct access to PackageMaze.

The implemented commands are:

```sh
maze doctor
maze auth exchange-oidc
maze publish dist/* --feed <organization>/<feed>
```

`maze doctor` diagnoses a repository's PackageMaze setup without changing it.
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

Linux, macOS, and Windows are officially supported targets: the same commands
work on each platform (with a `bin\maze.exe` build output on Windows), and
`go test ./...` must pass on all three.

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
POST /v1/auth/workload-token
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

PackageMaze returns the server-derived human-facing Build reference separately.
JSON, `github-output`, and shell output emit `build_number` and canonical
`build_url`; shell output names them `MAZE_BUILD_NUMBER` and `MAZE_BUILD_URL`.
Token-only output remains exactly the Token Secret followed by a newline. Use
the URL with PackageMaze's Build Report surfaces; do not derive either value
from `setup_invocation_id`.

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
Feed for a versioned Publish Plan, transfers each Artifact as instructed,
reports completion, and waits for PackageMaze status.
PackageMaze owns npm and PyPI package/version decisions.

Each invocation sends one opaque publication request identity. PackageMaze
returns a prepared Artifact transfer Plan, either new or resumed; `maze` follows
that Plan without exposing its storage implementation. The CLI does not carry a
legacy client-created transfer path. If Plan creation has a transient or
ambiguous failure, `maze` retries it once with the exact same request identity
and bytes so PackageMaze can safely return the already-created Plan.
If the transfer completion acknowledgement is lost, `maze` asks PackageMaze to
confirm the immutable Artifact and continues only when PackageMaze can do so.
If an invocation stops before the Publish Session becomes terminal, rerunning
the same ordered Artifact submission resumes the same PackageMaze Plan. The CLI
keeps only a private, expiring local recovery identity; it neither prints nor
persists temporary transfer authorization. A resumed transfer currently sends
only the Artifact parts that PackageMaze's transfer destination has not already
accepted. Independent missing parts transfer under a fixed concurrency bound;
independent Artifacts also transfer under a separate fixed bound. Credential
renewal remains future work.

Every request from `maze` to PackageMaze carries
`X-PackageMaze-Client-Version: maze/<version>`. PackageMaze can use that explicit
version for diagnostics and support policy without inferring it from output or
transfer details.

Authentication uses a PackageMaze Token with publish scope:

- `MAZE_TOKEN`
- `--token-file <path>`
- `--token-stdin`

Useful flags:

- `--package-client-url <url>` for local or staging Package Client Domain tests
- `--package <name>` and `--version <version>` as optional backend hints
- `--wait=false` to return after upload completion
- `--json` or `--format json` for CI-safe structured output

## Doctor

```sh
maze doctor
maze doctor --format json
maze doctor --dir packages/app --feed <organization>/<feed>
```

Run `maze doctor` in a repository when an install fails, or before setting
PackageMaze up. It is for people and for coding agents: the default output is
Markdown, `--format json` (or `--json`) is the same report as data, and the
exit code is non-zero only when a check failed.

It reads the committed package-client configuration in the directory and its
parents up to the repository root: `.npmrc`, `.yarnrc.yml`, `bunfig.toml`,
`pnpm-workspace.yaml`, `package.json` (`publishConfig`), `requirements*.txt`,
`pip.conf`, `pyproject.toml` (uv, Poetry, PDM), `uv.toml`, `Pipfile`, and
`.github/workflows/*.yml` steps that use `packagemaze/setup-maze`. It also reads
index variables such as `PIP_INDEX_URL` and `NPM_CONFIG_REGISTRY`. From those
it says which Feed the repository points at, or that none does.

Every check has a stable `code`, a `status` (`pass`, `warning`, `fail`), a
`summary` of what was observed, and a `remediation` naming the next step. The
checks are Feed Doctor's vocabulary applied locally:

- the URL shape each client needs: the Feed Base URL with its trailing slash
  for npm clients, the `/simple/` URL for pip, uv, Poetry, and PDM, the
  `/legacy/` URL for publishing;
- PackageMaze as the only index, with no `--extra-index-url`, supplemental
  Poetry source, non-default uv index, or scope routed elsewhere;
- committed credentials, including the pnpm case where a committed
  `${MAZE_TOKEN}` placeholder is ignored;
- the pnpm registry description, `permissions.id-token: write` next to
  setup-maze, and workflows minting Tokens for a different Feed;
- whether `MAZE_TOKEN` (or the variable named with `--token-env`) is set, and
  exactly what to set and where to get one when it is not.

With a Token set, the command makes one read-only request per configured Feed,
exactly as that Feed's package client would: `GET <Feed Base URL>-/whoami` with
the Token as a Bearer credential for npm Feeds, `GET <Feed Base URL>simple/`
with `__token__` Basic credentials for PyPI Feeds. The answer proves the Token
is accepted for reads (`read_token_ready`), rejected
(`read_token_rejected`), or that no Feed answers at that address
(`feed_not_found`). PackageMaze deliberately does not tell package clients why
a Token was rejected, and the report says so. `--offline` skips the request.

Security notes specific to `maze doctor`:

- It never writes a file and never prints a Token value. URLs are printed
  without credentials, and every output passes through redaction.
- It contacts only the Package Client Domain (`https://pkg.packagemaze.com`,
  or `--package-client-url` / `MAZE_PACKAGE_CLIENT_URL` for local and staging
  stacks). A URL on any other host is reported as not PackageMaze and is never
  requested.
- It does not read user-level client configuration (`~/.npmrc`, pip or uv
  credentials, Poetry's auth store); the report lists that under
  `not_checked`.

## GitHub Actions

```yaml
permissions:
  contents: read
  id-token: write

steps:
  - uses: actions/checkout@v4
  - id: packagemaze
    uses: packagemaze/setup-maze@v0.0.5
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
`artifact_protocol`, `feed_base_url`, `build_number`, and `build_url` so wrapper
actions can choose protocol-specific setup, use
canonical registry URLs, and link users or Agents to the exact Build without
asking workflows to duplicate Feed metadata.

The `shell` format writes `MAZE_TOKEN`, `MAZE_TOKEN_EXPIRES_AT`, `MAZE_FEED`,
`MAZE_FEED_BASE_URL`, `MAZE_PURPOSE`, `MAZE_ARTIFACT_PROTOCOL`,
`MAZE_BUILD_NUMBER`, and `MAZE_BUILD_URL` exports for wrapper
actions that need to consume exchange metadata without using GitHub step
outputs. Build exports are omitted when an older PackageMaze deployment does
not return a Build reference during a rolling upgrade.

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

The command also sends a fixed, non-secret allowlist of CircleCI's built-in job
environment for Build reporting: job number and URL, job name, parallel node
position, pull request and repository URLs, and commit SHA. PackageMaze validates
this context against the signed CircleCI identity where possible and labels it
as client reported; it never affects CI access rules or Token scopes.

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

## Repository CI And Releases

All builds for this repository run on GitHub Actions using Blacksmith runners.

[`.github/workflows/test.yml`](.github/workflows/test.yml) runs on every pull
request and push to `main` with a native matrix over the officially supported
platforms:

- Linux (`blacksmith-4vcpu-ubuntu-2404`): `gofmt`, `go vet`, `go test ./...`,
  and a build plus `maze version` smoke test.
- macOS (`blacksmith-6vcpu-macos-15`, Apple Silicon): `go vet`,
  `go test ./...`, build, and smoke test.
- Windows (`blacksmith-4vcpu-windows-2025`): `go vet`, `go test ./...`, build,
  and smoke test. Tests that drive the Linux-only bash release pipeline skip
  themselves on Windows.

[`.github/workflows/release.yml`](.github/workflows/release.yml) reuses that
test matrix as a gate, then `scripts/build-release-assets.sh` cross-compiles
every official target and writes SHA-256 checksums. The official release
assets are:

- `maze_linux_amd64.tar.gz`
- `maze_linux_arm64.tar.gz`
- `maze_darwin_arm64.tar.gz`
- `maze_windows_amd64.zip` (contains `maze.exe`)
- `maze_checksums.txt`

Pushing a `v*` tag runs the same test and build jobs, attaches GitHub build
provenance attestations for the checksummed assets, and then runs
`scripts/publish-release.sh` with the workflow's own `GITHUB_TOKEN` to create
the GitHub release for the tag, resume a draft, or verify an immutable rerun
without mutating assets.

A release can also be cut without pushing a tag: run the Release workflow
manually from `main` with a `v*` `release_tag` input. The dispatched run fails
if the tag already exists on a different commit; otherwise publishing the
GitHub release creates the tag on the dispatched `main` commit.

The only repository-side CI requirement is the Blacksmith GitHub app; the
release workflow stores no long-lived credentials.

## Production Readiness

The initial command is a usable first slice. The broader bar for a robust,
portable, best-in-class CLI is tracked in
[`docs/production-readiness.md`](docs/production-readiness.md).
