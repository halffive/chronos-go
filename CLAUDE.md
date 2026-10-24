# chronos-go

Go SDK for the Chronos API, module `chronos.sh/go`, package `chronos`. Public, MIT, standard library only.

- One flat package. Resources hang off `Client` as `*XService` types sharing one `service` struct; transport lives in `request.go` and `retry.go`, unexported.
- The REST clients match the Node.js SDK (`@chronos.sh/sdk`) on retries (429 for any method; network, timeout and 5xx for GET only), error codes, iteration and webhook checks. Change both together. There is no pull worker here yet.
- JSON field names match the API's snake_case. Optional request fields are pointers; update fields the API can clear use `Nullable[T]` with `omitzero`.
- Every network method takes `context.Context` first. Never read environment variables or log inside the library.
- The API key lives only in the `setHeaders` closure, so printing a `Client` never shows it.
- Tests run against `httptest` servers, table-driven where cases share a shape. No mocking libraries; inject through unexported fields (`Client.sleep`, `Client.jitter`, `Client.jobWaitTimeout`, `Webhook.now`).
- `make check` (gofmt, vet, staticcheck, `go test -race`, govulncheck) must pass before handing work back.
- `go.mod` stays at the oldest supported Go release.
- Releases: bump `Version` in `version.go` and merge, then run the `release` workflow on main. It creates the `v<Version>` tag and GitHub release, so the two cannot differ. Stay on `v0.x` until the API is stable.
- Commits and PR titles are lowercase conventional commits under 60 characters; branches are `{ft|bg|ch}-{issue-key}-{max-3-words}`.
