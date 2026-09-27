# CLAUDE.md — RCA Voice Transcriber

## What this is

A Go service: an on-call SRE phones in, narrates while debugging, and gets an RCA pull request generated from the transcript. Full context is in `IMPLEMENTATION.md` §1. If you are implementing a work package, your WP section in `IMPLEMENTATION.md` §7 is your spec, and its **Purpose** points to the summary of what the system is for.

## Commands

- `docker compose up -d`: start Postgres and apply Flyway migrations
- `make format`: `go fmt ./...`
- `make unittest`: `go test -race -count=1 -cover ./...`
- `make build`: build `bin/rca-transcriber` for this machine's OS and architecture
- `make run`: build, then run (configuration comes from `.env`)

## Architecture rules (hexagonal)

- `internal/core/domain`: entities, value objects, sentinel errors, pure functions. Standard library only.
- `internal/core/port`: interfaces only. Inbound (driving) ports are implemented by services. Outbound (driven) ports are implemented by adapters.
- `internal/core/service`: business logic. Depends only on `domain` and `port`. Never imports an adapter, SQL, HTTP or a vendor SDK.
- `internal/adapter/<name>`: one package per external system. May import `domain`, `port` and its own vendor SDK. **Never imports another adapter.**
- `cmd/server`: the only place that constructs adapters and wires them into services.
- The contracts in `domain` and `port` are frozen. Don't change them. If you think one must change, stop and report back.

## Go conventions

- `context.Context` is the first parameter of anything that does I/O. Respect cancellation.
- Constructor injection (`NewX(deps...)`). No package-level mutable state and no `init()` side effects.
- Wrap errors with context, e.g. `fmt.Errorf("postgres: get incident %d: %w", id, err)`. Compare with `errors.Is` / `errors.As`. Map vendor "not found" errors to `domain.ErrNotFound` at the adapter boundary.
- No panics and no `log.Fatal` outside `main`.
- Logging uses an injected `*slog.Logger`. Never log secrets, full phone numbers (use `domain.MaskPhone`), or transcript text above debug level.
- Every port implementation has a compile-time assertion: `var _ port.X = (*Y)(nil)`.
- Every goroutine has a clear owner and shutdown path (context cancellation or a `sync.WaitGroup`). Guard shared state with a mutex. gorilla/websocket allows only one concurrent writer per connection.
- In `package service`, prefix unexported package-level identifiers with their area (`call…`, `transcription…`, `rca…`), because three work packages share the package.
- Don't add dependencies or run `go mod tidy` unless your work package says to.

## Testing: a key success criterion

Work is not done until it has unit tests covering both happy and sad paths.

- Write table-driven tests with `t.Run` subtests. Use `testify/require` for preconditions and `testify/assert` for checks.
- For **every** exported function and method, test the happy path and each distinct failure: invalid input, not found, dependency error, and context cancellation or timeout where relevant.
- Mock at the port boundary:
  - Core services: the shared fakes in `internal/core/service/fakes_test.go`.
  - PostgreSQL adapter: `pgxmock`.
  - HTTP APIs: `httptest.Server`, or a fake behind a small interface (Gemini).
  - WebSockets (Deepgram, Twilio media): `httptest.Server` with a gorilla upgrader or client.
- Unit tests never touch the real network, a real database or real credentials.
- Tests must be deterministic. Inject clocks and backoff functions. Don't use `time.Sleep` to synchronize. Use channels, or `require.Eventually` with a short timeout when unavoidable.
- All tests pass under `-race`.
- Coverage: at least 80% per adapter package, and at least 90% for each service file. Check the `make unittest` output.

## Definition of done (every work package)

1. Only files your work package owns were changed.
2. `make format`, `make unittest` and `make build` all pass.
3. Coverage targets are met.
4. Final report: files changed, coverage per package, deviations from the spec, open questions.

## Security

- `.env` is gitignored. Never commit or print secrets. `.env.example` holds placeholders only.
- Twilio webhooks and the media WebSocket must pass signature validation (`IMPLEMENTATION.md` §6.1).
- Build TwiML with `encoding/xml`, never with string concatenation.
