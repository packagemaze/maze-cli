# PackageMaze CLI

`maze` is the PackageMaze command line interface. This repository contains the
public CLI source, tests, and release workflow that publishes signed release
assets for `packagemaze/setup-maze`.

The first implemented command is:

```sh
maze auth exchange-oidc
```

It exchanges a CI OIDC identity token for a short-lived PackageMaze Token. The
command always runs the same exchange flow; local and staging development use
the normal `--base-url` or `--api-url` endpoint overrides.

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

## GitHub Actions

```yaml
permissions:
  contents: read
  id-token: write

steps:
  - uses: actions/checkout@v4
  - uses: packagemaze/setup-maze@v0.0.1
  - id: packagemaze
    run: maze auth exchange-oidc --feed <organization>/<feed> --purpose install --format github-output
  - run: npm ci
    env:
      NODE_AUTH_TOKEN: ${{ steps.packagemaze.outputs.token }}
```

For workflow outputs:

```sh
maze auth exchange-oidc \
  --feed <organization>/<feed> \
  --purpose install \
  --format github-output \
  --output-name package_maze_token
```

## GitLab CI/CD

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
  command.
- Tokens are not written to project files or persistent config.

## Production Readiness

The initial command is a usable first slice. The broader bar for a robust,
portable, best-in-class CLI is tracked in
[`docs/production-readiness.md`](docs/production-readiness.md).
