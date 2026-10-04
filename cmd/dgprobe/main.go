// Command dgprobe is a throwaway manual test harness for the Deepgram
// adapter (internal/adapter/deepgram). It is NOT part of any work package
// and should be deleted once you're done testing — do not commit it.
//
// Usage:
//
//	DEEPGRAM_API_KEY=... DEEPGRAM_MODEL=nova-3 go run ./cmd/dgprobe path/to/audio.ulaw
//
// The audio file must be raw 8kHz mono mu-law (no container), matching what
// Twilio sends. Convert a wav/mp3 with:
//
//	ffmpeg -i input.wav -ar 8000 -ac 1 -f mulaw audio.ulaw
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/jwke21/rca-transcriber/internal/adapter/deepgram"
)

// frameSize is 20ms of 8kHz mu-law audio (8000 bytes/sec / 50), matching the
// cadence Twilio uses so Deepgram sees realistic pacing.
const frameSize = 160

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: dgprobe path/to/audio.ulaw")
	}
	audioPath := os.Args[1]

	_ = godotenv.Load(".env") // best-effort; real env vars still win

	apiKey := os.Getenv("DEEPGRAM_API_KEY")
	model := os.Getenv("DEEPGRAM_MODEL")
	if apiKey == "" || model == "" {
		return fmt.Errorf("DEEPGRAM_API_KEY and DEEPGRAM_MODEL must be set")
	}

	audio, err := os.ReadFile(audioPath)
	if err != nil {
		return fmt.Errorf("read audio file: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := deepgram.New(deepgram.Config{APIKey: apiKey, Model: model}, logger)

	fmt.Println("opening stream...")
	stream, err := client.Open(ctx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	fmt.Println("stream open, streaming audio...")

	printDone := make(chan struct{})
	go func() {
		defer close(printDone)
		for seg := range stream.Results() {
			fmt.Printf("[%s +%s] %q\n", seg.SpokenAt.Format(time.RFC3339), seg.Duration, seg.Text)
		}
		fmt.Println("results channel closed")
	}()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

sendLoop:
	for offset := 0; offset < len(audio); offset += frameSize {
		end := offset + frameSize
		if end > len(audio) {
			end = len(audio)
		}
		if err := stream.SendAudio(audio[offset:end]); err != nil {
			return fmt.Errorf("send audio at offset %d: %w", offset, err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			break sendLoop
		}
	}

	fmt.Println("done sending audio, finishing stream...")
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := stream.Finish(finishCtx); err != nil {
		fmt.Fprintf(os.Stderr, "finish: %v\n", err)
	}

	<-printDone
	return nil
}
