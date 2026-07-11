# PackageMaze API contract fixtures

`ci-token-exchange.json` mirrors the Worker-owned `POST /v1/auth/ci-token`
boundary in the PackageMaze repository:

- request parsing and response construction in
  `runtime-worker/src/ci-auth/route.ts`;
- the public error shape in `contracts/src/api.ts`; and
- Build correlation semantics in ADR 0115.

The API client and command end-to-end tests consume this same fixture. Update
it alongside intentional PackageMaze contract changes so field-name, structured
error, and exchange-purpose drift fails in one of those layers.

Successful responses carry preferred `build_id` and compatibility
`ci_session_id` with the same server-derived value. The client accepts either
during a rolling upgrade and rejects a response where both identify different
Builds.

`setup_invocation_id` is caller-supplied, non-secret correlation metadata. Its
provenance prefix is useful to humans but is not provider-signed Build evidence
and must not be interpreted as user intent.
