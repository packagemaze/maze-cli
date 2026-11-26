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
maze auth login --org <organization>
maze auth exchange-oidc
maze auth exec -- <command>
maze auth show-identity
maze publish dist/* --feed <organization>/<feed>
```

`maze doctor` diagnoses a repository's PackageMaze setup without changing it.
`maze auth login` gets a person's machine a Token: it opens PackageMaze in the
browser, waits while you approve the request there, saves the Token to a
profile in your user config directory (`packagemaze/credentials.json`, mode
0600) and verifies it. It prints the Organization and profile, never the Token.
`maze publish` and `maze doctor` use that saved Token when `MAZE_TOKEN` is unset.
Each Feed gets the profile saved for its own Organization; `--profile <name>`
picks the profile for its Organization's Feeds only. A profile never serves
another Organization's Feeds. In CI a profile is read only when `--profile`
names it. The profile also records the Package Client Domain
(`--package-client-url` or `MAZE_PACKAGE_CLIENT_URL` at login): `maze publish`
refuses a profile whose Token expired or that was saved for another Package
Client Domain, and also refuses a set but empty `MAZE_TOKEN`; `maze doctor`
warns about all three, and about several profiles for one Organization.
`maze auth exchange-oidc` exchanges a job's workload identity token for a
short-lived PackageMaze Token. `maze auth exec` does the same exchange and
runs a command with its package clients signed in to the Feeds.
`maze auth show-identity` prints the claims in the job's identity token
without signing in. `maze publish` sends local artifact facts
to PackageMaze, executes the returned backend plan, uploads bytes, and waits for Publish
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
# --provider manual is for local development against a local API only.
MAZE_OIDC_TOKEN="$OIDC_TOKEN" go run ./cmd/maze auth exchange-oidc --base-url http://127.0.0.1:8787 --allow-insecure-localhost --provider manual --feed <organization>/<feed> --purpose install
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
contract. The CLI collects no CI environment metadata automatically, with one
exception: on CircleCI it sends the fixed allowlist described under
[CircleCI](#circleci).

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

Where the identity token comes from decides what the request names:

- **Built-in platforms.** On GitHub Actions and CircleCI, `maze` requests the
  job's identity token itself (the Actions token request URL, or
  `circleci run oidc get`) and names that platform as the request's
  `provider`.
- **Any other platform** PackageMaze has an access rule for, such as GitLab
  CI/CD or a sandbox, is a custom platform. Request the platform's identity
  token for the audience `https://api.packagemaze.com` and pass it in:

  ```sh
  <your platform's token command> | maze auth exchange-oidc \
    --identity-token-stdin \
    --feed <organization>/<feed> \
    --purpose install
  ```

  A supplied token names no `provider`: PackageMaze selects the verifier from
  the token's issuer. On GitLab CI/CD, for example, request the token with
  `id_tokens` into `MAZE_OIDC_TOKEN`:

  ```yaml
  id_tokens:
    MAZE_OIDC_TOKEN:
      aud: https://api.packagemaze.com
  script:
    - maze auth exchange-oidc --feed <organization>/<feed> --purpose install
  ```

- **Injected identity.** When the platform's egress proxy adds the identity
  token to the request itself, pass `--identity-token-injected`; see
  [Injected Identity](#injected-identity).

A supplied token is read from, in order: `--identity-token-file <path>`,
`--identity-token-stdin`, then the environment variable `--identity-token-env`
names (default `MAZE_OIDC_TOKEN`), then `MAZE_IDENTITY_TOKEN`. On GitHub
Actions a file or stdin token replaces the job's own; an environment token
does not. On CircleCI an environment token replaces `circleci run oidc get`.
The token never appears in arguments or output. PackageMaze accepts an
identity token once, so request a fresh one for each exchange.

Each exchange carries a `retry_secret`: 32 random bytes `maze` generates for
it and keeps only in memory. When the answer is lost (the request fails in
transit, except on a certificate failure, or a 2xx answer cannot be read) or
PackageMaze answers 429, 502, 503 or 504, `maze` resends the identical
request with the same identity token and secret, and PackageMaze revokes the
Token the first attempt minted and returns a new one. Any other answer, such
as `oidc_assertion_replayed`, ends the exchange with PackageMaze's error.
`maze` waits 1, 2, 4, 8, then 15 seconds between attempts, or longer when
`Retry-After` asks, and prints one line per retry on stderr. It stops
retrying at the identity token's `exp` or 2 minutes after the first attempt,
whichever is sooner; 2 minutes when it cannot read `exp` or the platform
injects the token. `maze auth exec` retries the same way. `retry_secret` needs a PackageMaze server that accepts
it.

`--oidc-token-file`, `--oidc-token-stdin`, and `--oidc-token-env` are the
earlier names of these flags. They keep working, and giving both names of one
flag is an error.

`--provider` names a provider explicitly, and an explicit name is always sent:
`github` or `circleci` for a built-in platform, `oidc` for PackageMaze's
generic verifier, or `manual` for local development against a local API
(never chosen automatically). `--provider auto`, the default, names one only
when `maze` requested the token from a built-in platform itself. `--verbose`
prints which provider was named, or that none was.

PackageMaze decides what the identity may do: a publish Token needs a publish
access rule on the Feed.

Some platforms do not sign the job's git ref or commit. When the platform's
setup in PackageMaze asks for them, pass the job's own values with
`--ref <value>` and `--commit <value>` (each trimmed, at most 256 characters).
The request then carries `"reported": {"ref": ..., "commit": ...}` with only
the values given. PackageMaze shows them as Reported: they are not verified and
cannot be used in an access rule. These flags need a PackageMaze server that
accepts them; without them the request is unchanged.

To install from several Feeds of one Organization, repeat `--feed` (up to 20
times) and sign in once:

```sh
maze auth exchange-oidc \
  --feed <organization>/<feed> \
  --feed <organization>/<another-feed> \
  --purpose install
```

One Token covers all of them. `--format json` carries PackageMaze's `feeds`
list (`feed`, `feed_base_url`, and `artifact_protocol` for each Feed) in place
of the single-Feed fields. `--format shell` exports `MAZE_FEEDS` (the Feed
references, space-separated) in place of `MAZE_FEED`, `MAZE_FEED_BASE_URL`,
and `MAZE_ARTIFACT_PROTOCOL`. `--format github-output` adds the output
`<output-name>-feeds` (for example `token-feeds`) holding the Feed references
as a JSON array, in place of `artifact_protocol` and `feed_base_url`.
`--purpose publish` takes one Feed. Several Feeds need a PackageMaze server
that accepts them.

Required flags:

- `--feed <organization>/<feed>`, repeatable
- `--purpose {install | publish | docker-build | test}`

`--package <package-name>` is optional with `--purpose publish` and has no
effect on what the Token may publish.

Output formats:

- `--format token`
- `--format json` or `--json`
- `--format shell`
- `--format github-output`

For local API development only, point the same command at the local API root
and provide a token through stdin, a file, or an environment variable with
`--provider manual`:

```sh
printf '%s' "$OIDC_TOKEN" | maze auth exchange-oidc \
  --base-url http://127.0.0.1:8787 \
  --allow-insecure-localhost \
  --provider manual \
  --identity-token-stdin \
  --feed <organization>/<feed> \
  --purpose install \
  --format json
```

## Run A Command With Feed Access

`maze auth exec` takes every `maze auth exchange-oidc` flag, including
repeated `--feed`, then runs the command after `--` with temporary
package-client configuration for each Feed the Token covers:

```sh
maze auth exec --feed <organization>/<feed> --purpose install -- npm ci
maze auth exec \
  --feed <organization>/<npm-feed> --feed <organization>/<pypi-feed> \
  --purpose install -- make install
```

For npm Feeds it writes a private npm user config, in a new directory only
you can open (mode `0700`, file `0600`), and points `NPM_CONFIG_USERCONFIG`
at it, which npm and pnpm read:

```ini
registry=https://pkg.packagemaze.com/<organization>/<npm-feed>/
replace-registry-host=npmjs
//pkg.packagemaze.com/<organization>/<npm-feed>/:_authToken=${MAZE_TOKEN}
```

The first npm Feed is the default registry; every npm Feed gets its own
Feed-URL-scoped credential line. It also sets `COREPACK_NPM_REGISTRY` and
`COREPACK_NPM_TOKEN` for Corepack, for the first npm Feed only, because
Corepack reads one registry. It sets nothing for Yarn Berry, whose only
environment token is not scoped to a URL: name the Feed under `npmRegistries`
in the project's `.yarnrc.yml` with `npmAuthToken: "${MAZE_TOKEN}"`, which
`maze auth exec` exports. The command does not read your own npm user config,
and an inherited `NPM_CONFIG_REGISTRY` is removed because it would outrank
every npmrc file.

For PyPI Feeds it sets `PIP_INDEX_URL` and `UV_DEFAULT_INDEX` to the first
Feed's Simple URL, `https://__token__:<Token>@pkg.packagemaze.com/<organization>/<feed>/simple/`,
and, for more PyPI Feeds, `PIP_EXTRA_INDEX_URL` and `UV_INDEX` to the others.
pip merges every index; uv searches the extra ones, in `--feed` order, before
the default one. Inherited `PIP_EXTRA_INDEX_URL` and `UV_INDEX` are removed
when there is one PyPI Feed, and uv's deprecated `UV_INDEX_URL` and
`UV_EXTRA_INDEX_URL` always are, so the named Feeds are the only indexes.

The command sees the Token as `MAZE_TOKEN` too. The Token is never printed,
never put in an argument, and never written to a file: the npm user config
references `${MAZE_TOKEN}`. `maze auth exec` never sets `cafile` or `cert` and
never changes your own configuration files. `--verbose` names the Feeds and
the variables set or removed, never a value.

`maze` exits with the command's exit status. It removes the directory when the
command ends, fails, or stops on a signal: `SIGINT`, `SIGTERM`, and `SIGHUP`
are passed to the command, and a command a signal stopped exits with 128 plus
the signal number. A `SIGKILL` leaves the directory behind, without a Token in
it. A Feed of any other Artifact Protocol, a `--format`, or a missing command
fails before anything runs. Bun, Poetry, PDM, and PyPI publishing tools are
not configured.

## Injected Identity

Some sandbox platforms keep the identity token outside the workload: their
egress proxy adds it to outbound requests for hosts you choose, as
`Authorization: Bearer <token>`. PackageMaze reads the identity token from
that header when the request body carries none. Pass
`--identity-token-injected` and `maze` sends no `oidc_token`, no
`Authorization` header of its own, and no `provider`, so the token's issuer
selects the verifier:

```sh
# Inside a sandbox whose platform injects its identity token into requests
# for api.packagemaze.com (E2B works this way).
maze auth exchange-oidc \
  --identity-token-injected \
  --feed <organization>/<feed> \
  --purpose install
```

The request still carries `audience` (default `https://api.packagemaze.com`,
or `--audience`); it must match the audience the platform requested the
token for. `--identity-token-injected` fails when combined with
`--identity-token-file`, `--identity-token-stdin`, an identity token in the
environment, or `--provider`. `maze auth show-identity` cannot read an
injected token.

## Show Identity

When you set up a CI or sandbox platform in PackageMaze, run
`maze auth show-identity` in a job to see which claims its identity token
carries, before any sign-in fails:

```sh
<your platform's token command> | maze auth show-identity \
  --identity-token-stdin
```

The command obtains the token exactly as `maze auth exchange-oidc` does (same
`--provider`, `--identity-token-stdin`, `--identity-token-file`,
`--identity-token-env`, `--audience`, and `--base-url`), decodes it locally,
and prints the issuer,
audience, expiry, signing algorithm and key id, then every other claim sorted
by name. `--format json` prints `{"verified": false, "header": {...},
"claims": {...}}`. The token is not verified and nothing is sent to
PackageMaze.

Every claim value is printed to the job log. The token itself and its
signature are never printed, and control characters in claim names and values
are escaped.

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

On a Feed with publish approval on, a publish holds its new versions until an
Organization Admin approves the Run that published them. `maze publish` still
succeeds and prints, on stderr, `<name>@<version> is waiting for an
Organization Admin's approval: <link>` for each held version. If an
Organization Admin rejected the Run, `maze publish` fails with a message saying
so. JSON output carries `publish_approval` on the session and on each Artifact.

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
- whether `MAZE_TOKEN` (or the variable named with `--token-env`) is set, or
  which profile `maze auth login` saved each Organization uses (in JSON,
  `token.organizations` lists `{"organization", "source": {"kind": "env" |
  "profile", "name"}}`), and
  exactly what to set and where to get one when neither is.

With a Token set, the command makes one read-only request per configured Feed,
exactly as that Feed's package client would: `GET <Feed Base URL>-/whoami` with
the Token as a Bearer credential for npm Feeds, `GET <Feed Base URL>simple/`
with `__token__` Basic credentials for PyPI Feeds. The answer proves the Token
is accepted for reads (`read_token_ready`), rejected
(`read_token_rejected`), or that no Feed answers at that address
(`feed_not_found`). PackageMaze deliberately does not tell package clients why
a Token was rejected, and the report says so. `--offline` skips the request
and reads no saved profile unless `--profile` names one.

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
    uses: packagemaze/setup-maze@v0.0.7
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
asking workflows to duplicate Feed metadata. For several Feeds it writes
`<output-name>-feeds` in place of `artifact_protocol` and `feed_base_url`.

The `shell` format writes `MAZE_TOKEN`, `MAZE_TOKEN_EXPIRES_AT`, `MAZE_FEED`,
`MAZE_FEED_BASE_URL`, `MAZE_PURPOSE`, `MAZE_ARTIFACT_PROTOCOL`,
`MAZE_BUILD_NUMBER`, and `MAZE_BUILD_URL` exports for wrapper
actions that need to consume exchange metadata without using GitHub step
outputs; for several Feeds, `MAZE_FEEDS` replaces the three single-Feed
exports. Build exports are omitted when an older PackageMaze deployment does
not return a Build reference during a rolling upgrade.

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

When `maze` runs `circleci run oidc get` itself, the request names the
`circleci` provider; a token supplied through `MAZE_OIDC_TOKEN` names none and
its issuer selects the verifier.

The command also sends a fixed, non-secret allowlist of CircleCI's built-in job
environment for Build reporting: job number and URL, job name, parallel node
position, pull request and repository URLs, and commit SHA. PackageMaze validates
this context against the signed CircleCI identity where possible and labels it
as client reported; it never affects CI access rules or Token scopes.

## Manual Token Input

`--provider manual` is for local development against a local PackageMaze API
only; `maze` never chooses it. Like every token input, it avoids a plain
`--identity-token` flag so token values do not leak into shell history or
process listings.

```sh
printf '%s' "$OIDC_TOKEN" | maze auth exchange-oidc \
  --base-url http://127.0.0.1:8787 \
  --allow-insecure-localhost \
  --provider manual \
  --identity-token-stdin \
  --feed <organization>/<feed> \
  --purpose install
```

## Security Notes

- The raw identity token is never printed.
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
