# RCA Voice Transcriber

An on-call SRE phones a Twilio number, enters an incident number and narrates while debugging; the service transcribes the call live with Deepgram and saves every line to PostgreSQL with the time it was spoken. When the SRE presses `#` and then `1`, the service has Google Gemini write a short, factual RCA from the transcript and opens a GitHub pull request adding it to `incidents/` for a human to review and merge.

The full background, requirements and design are in [`IMPLEMENTATION.md` §1](IMPLEMENTATION.md#1-project-purpose).

## Prerequisites

- Go (the version in `go.mod`)
- Docker with Docker Compose
- [ngrok](https://ngrok.com/)
- A Twilio account and a Twilio phone number with voice enabled
- A Deepgram API key
- A Google Gemini API key
- A GitHub fine-grained personal access token scoped to **this repository only**, with **Contents: read and write** and **Pull requests: read and write**

## Setup

1. Create your config and fill it in:

   ```sh
   cp .env.example .env
   ```

   Generate `STREAM_SIGNING_SECRET` with `openssl rand -hex 32`. `.env` is gitignored; real environment variables override it.

2. Start Postgres and apply the migrations:

   ```sh
   docker compose up -d
   docker compose ps -a    # the flyway container should show "Exited (0)"
   ```

3. Allow your phone number to call in (E.164 format):

   ```sh
   docker compose exec postgres psql -U rca -d rca -c "INSERT INTO engineers (phone_number) VALUES ('+15555550123');"
   ```

4. Expose port 8080 publicly:

   ```sh
   ngrok http 8080
   # or, with a static domain:
   ngrok http --url=your-subdomain.ngrok-free.app 8080
   ```

   Set `PUBLIC_BASE_URL` in `.env` to the `https://` forwarding URL, with no trailing slash.

5. In the Twilio Console, open your phone number and set **A call comes in** to **Webhook**, `HTTP POST`, URL `{PUBLIC_BASE_URL}/twilio/voice`. The server logs this exact URL at startup (`webhook_url=…`).

6. Run the server:

   ```sh
   make run
   ```

   It logs the effective config with secrets masked, and `GET /healthz` returns `200 ok` once the database is reachable (`503` otherwise).

7. Call the number.

## The call script

1. You hear: *"Enter the incident number, then press pound."* Type the incident number (e.g. `4821`) and press `#`.
2. You hear *"New incident 4 8 2 1. Start narrating. Press pound when the incident is resolved."* If the incident number already exists, it says *"Resuming incident…"* instead, and new lines are added to the same transcript. While an RCA for that incident is still being generated, you hear *"An R C A is already being generated…"* and the call ends.
3. Narrate what you see and try. Each finished sentence is saved as you speak. If the call drops, call back and enter the same number to continue.
4. When the incident is resolved, press `#`. You hear: *"Press 1 to confirm the incident is resolved and generate the R C A. Press any other key to keep recording."*
   - Press `1`: you hear *"Generating the R C A for incident 4 8 2 1. A pull request will be opened shortly. Goodbye."* and the call ends.
   - Press any other key, or nothing: recording resumes ("Resuming incident…").
5. Within a couple of minutes, a pull request titled `docs: incident 4821` appears in the repository, from branch `incident-4821`. It adds `incidents/{YYYY-MM-DD}-4821.md`, dated with the UTC resolution date, containing Title, Date, Author (`RCA Transcriber`) and Summary. Review it, then merge it.

Calling in again later with the same number and confirming again updates the same branch, file and PR rather than opening a new one.

To view a transcript:

```sh
docker compose exec postgres psql -U rca -d rca -c \
  "SELECT id, created_at, transcription FROM incident_events WHERE incident_id = 4821 ORDER BY created_at, id;"
```

## Troubleshooting

- **Every webhook returns 403.** `PUBLIC_BASE_URL` must match the ngrok URL exactly (scheme, host, no trailing slash). Twilio signs the URL it calls, and the server validates against `PUBLIC_BASE_URL`. `TWILIO_AUTH_TOKEN` and `TWILIO_ACCOUNT_SID` must belong to the account that owns the number.
- **"This phone number is not authorized."** Seed the `engineers` table with your number in E.164 format (`+` and country code, no spaces), as in setup step 3.
- **Twilio trial accounts** play a trial message before your webhook runs, and only accept calls from verified caller IDs.
- **No PR appears.** Look in the server logs for `rca: background generation failed`, and check the incident's status:

  ```sh
  docker compose exec postgres psql -U rca -d rca -c "SELECT id, status, resolution_time FROM incidents;"
  ```

  A status of `rca_failed` means Gemini or GitHub failed after retries. Check the GitHub token's repository access and scopes (Contents and Pull requests, read and write), and the Gemini key and model name.
- **Retrying an `rca_failed` incident.** An RCA that fails generation or publishing, or that was still running when a shutdown timed out, ends as `rca_failed`; nothing is published. To retry, call in with the same incident number and press `#`, then `1` again. The transcript is kept, and new narration is added to it.
- **The confirmation prompt plays straight away.** If the media stream can't start (for example Deepgram is down or the key is wrong), the server closes the stream and you hear the confirmation prompt without anything having been recorded. Pressing `1` then ends as `rca_failed` because there is no transcript. Check the logs for `twilio: media start session failed`.
- **Startup fails with `password authentication failed` or `connection refused`.** Make sure `localhost:5432` reaches the Docker container and not another Postgres installed on the host.

## Makefile targets

| Target | What it does |
|---|---|
| `make format` | `go fmt ./...` |
| `make unittest` | `go test -race -count=1 -cover ./...` |
| `make build` | Builds `bin/rca-transcriber` for this machine (override with `GOOS=… GOARCH=…`) |
| `make run` | Builds, then runs the server. Configuration comes from `.env`. |

## Operational notes

- **Shutdown.** On `SIGINT`/`SIGTERM` the server stops accepting requests (up to 10 s), waits for in-flight RCA generations (up to 60 s), then closes the database pool.
- **Recovery.** At startup, incidents left in `rca_pending` or `rca_generating` (for example after a crash) are generated again. Run only one instance.

## GitHub identity (PoC)

`GITHUB_OWNER` is the account that owns the repository, and `GITHUB_TOKEN` is that account's fine-grained PAT, so PRs are opened as that person. This is acceptable for a proof of concept. A production version would authenticate as a service account (a GitHub App or a machine user) instead of a human account.
