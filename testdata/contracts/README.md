# PackageMaze API contract fixtures

`workload-token-exchange.json` mirrors the Worker-owned `POST /v1/auth/workload-token`
boundary in the PackageMaze repository:

- request parsing in `runtime-worker/src/workload-auth/route.ts` and
  `runtime-worker/src/workload-auth/exchange-request-fields.ts`, provider
  selection by issuer in `runtime-worker/src/workload-auth/sign-in-provider.ts`,
  and response construction in
  `runtime-worker/src/workload-auth/sign-in-feeds.ts`; and
- the public error shape in `contracts/src/api.ts`.

`provider` and `oidc_token` are optional: without `provider` the identity
token's issuer selects the verifier (a `null` provider is refused, so the CLI
omits the key), and without `oidc_token` PackageMaze reads the token from the
`Authorization: Bearer` header an egress-injecting platform adds. The fixture's
request names `manual`, as `--provider manual` does for local development.

`retry_secret` is 32–256 characters from `[A-Za-z0-9_-]`. A repeat of the
same identity token, secret and request revokes the Token the first exchange
minted and returns a new one; any other repeat is refused as
`oidc_assertion_replayed`. A server without it refuses the field with 422
`extra_forbidden`.

The API client and command end-to-end tests consume this same fixture. Update
it alongside intentional PackageMaze contract changes so field-name, structured
error, and exchange-purpose drift fails in one of those layers.

Successful responses carry the Organization-scoped `build_number` and canonical
server-derived `build_url`.

`setup_invocation_id` is caller-supplied, non-secret correlation metadata. Its
provenance prefix is useful to humans but is not provider-signed Build evidence
and must not be interpreted as user intent.
