# dgprobe

A throwaway manual test harness for `internal/adapter/deepgram`. It is not
part of any work package — delete this directory once you're done testing,
don't commit it.

`cmd/server` doesn't wire Deepgram into the HTTP server yet (that's WP9), so
this is the only way to exercise the adapter against the real Deepgram API
before then.

## What it does

Opens a live streaming session with `deepgram.Client.Open`, streams an audio
file to it in real-time-paced 20ms/160-byte frames (matching Twilio's
cadence), prints every `domain.TranscriptSegment` as it arrives, then calls
`Finish` to flush the tail and close the stream.

## Setup

1. Put a real key in `.env` at the repo root:
   ```
   DEEPGRAM_API_KEY=your-real-key
   DEEPGRAM_MODEL=nova-3
   ```
2. Get a raw 8kHz mono mu-law audio file (no container — the adapter hardcodes
   `encoding=mulaw&sample_rate=8000&channels=1`). Convert any speech
   recording:
   ```
   ffmpeg -i input.wav -ar 8000 -ac 1 -f mulaw audio.ulaw
   ```

## Run

```
go run ./cmd/dgprobe audio.ulaw
```

## What to check

- `Open` succeeds and you see `stream open, streaming audio...` — confirms
  the handshake (auth header + query params) is accepted.
- Transcript lines print with plausible `SpokenAt`/`Duration` matching the
  audio content — confirms `readLoop`'s JSON parsing and segment stamping.
- After all audio is sent, `Finish` flushes any trailing sentence that hadn't
  been finalized yet, and `results channel closed` prints — confirms the
  Finalize/CloseStream control flow and clean shutdown.
- To test the error path, temporarily set a garbage `DEEPGRAM_API_KEY` and
  rerun — `Open` should return a wrapped error mentioning a 401 status.

The automated tests in `internal/adapter/deepgram/*_test.go` already cover
the wire protocol against a fake server; this tool is only for confirming
behavior against the real Deepgram service.
