package service

import (
	"context"
	"sync"
	"time"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// fakeClock is a settable Clock for deterministic tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

var _ port.Clock = (*fakeClock)(nil)

// fakeEngineerRepository is a fake port.EngineerRepository.
type fakeEngineerRepository struct {
	mu                    sync.Mutex
	GetByPhoneNumberFn    func(ctx context.Context, phoneNumber string) (domain.Engineer, error)
	getByPhoneNumberCalls []string
}

func (f *fakeEngineerRepository) GetByPhoneNumber(ctx context.Context, phoneNumber string) (domain.Engineer, error) {
	f.mu.Lock()
	f.getByPhoneNumberCalls = append(f.getByPhoneNumberCalls, phoneNumber)
	f.mu.Unlock()
	if f.GetByPhoneNumberFn == nil {
		return domain.Engineer{}, nil
	}
	return f.GetByPhoneNumberFn(ctx, phoneNumber)
}

var _ port.EngineerRepository = (*fakeEngineerRepository)(nil)

// fakeIncidentRepository is a fake port.IncidentRepository.
type fakeIncidentRepository struct {
	mu sync.Mutex

	GetOrCreateFn        func(ctx context.Context, id int64) (domain.Incident, bool, error)
	GetFn                func(ctx context.Context, id int64) (domain.Incident, error)
	SetStatusFn          func(ctx context.Context, id int64, status domain.IncidentStatus) error
	MarkResolvedFn       func(ctx context.Context, id int64, at time.Time) (domain.Incident, error)
	ClaimForGenerationFn func(ctx context.Context, id int64) (bool, error)
	ListByStatusFn       func(ctx context.Context, statuses ...domain.IncidentStatus) ([]domain.Incident, error)

	getOrCreateCalls        []int64
	getCalls                []int64
	setStatusCalls          []setStatusCall
	markResolvedCalls       []markResolvedCall
	claimForGenerationCalls []int64
	listByStatusCalls       [][]domain.IncidentStatus
}

type setStatusCall struct {
	id     int64
	status domain.IncidentStatus
}

type markResolvedCall struct {
	id int64
	at time.Time
}

func (f *fakeIncidentRepository) GetOrCreate(ctx context.Context, id int64) (domain.Incident, bool, error) {
	f.mu.Lock()
	f.getOrCreateCalls = append(f.getOrCreateCalls, id)
	f.mu.Unlock()
	if f.GetOrCreateFn == nil {
		return domain.Incident{}, false, nil
	}
	return f.GetOrCreateFn(ctx, id)
}

func (f *fakeIncidentRepository) Get(ctx context.Context, id int64) (domain.Incident, error) {
	f.mu.Lock()
	f.getCalls = append(f.getCalls, id)
	f.mu.Unlock()
	if f.GetFn == nil {
		return domain.Incident{}, nil
	}
	return f.GetFn(ctx, id)
}

func (f *fakeIncidentRepository) SetStatus(ctx context.Context, id int64, status domain.IncidentStatus) error {
	f.mu.Lock()
	f.setStatusCalls = append(f.setStatusCalls, setStatusCall{id, status})
	f.mu.Unlock()
	if f.SetStatusFn == nil {
		return nil
	}
	return f.SetStatusFn(ctx, id, status)
}

func (f *fakeIncidentRepository) MarkResolved(ctx context.Context, id int64, at time.Time) (domain.Incident, error) {
	f.mu.Lock()
	f.markResolvedCalls = append(f.markResolvedCalls, markResolvedCall{id, at})
	f.mu.Unlock()
	if f.MarkResolvedFn == nil {
		return domain.Incident{}, nil
	}
	return f.MarkResolvedFn(ctx, id, at)
}

func (f *fakeIncidentRepository) ClaimForGeneration(ctx context.Context, id int64) (bool, error) {
	f.mu.Lock()
	f.claimForGenerationCalls = append(f.claimForGenerationCalls, id)
	f.mu.Unlock()
	if f.ClaimForGenerationFn == nil {
		return false, nil
	}
	return f.ClaimForGenerationFn(ctx, id)
}

func (f *fakeIncidentRepository) ListByStatus(ctx context.Context, statuses ...domain.IncidentStatus) ([]domain.Incident, error) {
	f.mu.Lock()
	f.listByStatusCalls = append(f.listByStatusCalls, statuses)
	f.mu.Unlock()
	if f.ListByStatusFn == nil {
		return nil, nil
	}
	return f.ListByStatusFn(ctx, statuses...)
}

var _ port.IncidentRepository = (*fakeIncidentRepository)(nil)

// fakeIncidentEventRepository is a fake port.IncidentEventRepository.
type fakeIncidentEventRepository struct {
	mu sync.Mutex

	AppendFn         func(ctx context.Context, event domain.IncidentEvent) (domain.IncidentEvent, error)
	ListByIncidentFn func(ctx context.Context, incidentID int64) ([]domain.IncidentEvent, error)

	appendCalls         []domain.IncidentEvent
	listByIncidentCalls []int64
}

func (f *fakeIncidentEventRepository) Append(ctx context.Context, event domain.IncidentEvent) (domain.IncidentEvent, error) {
	f.mu.Lock()
	f.appendCalls = append(f.appendCalls, event)
	f.mu.Unlock()
	if f.AppendFn == nil {
		return domain.IncidentEvent{}, nil
	}
	return f.AppendFn(ctx, event)
}

func (f *fakeIncidentEventRepository) ListByIncident(ctx context.Context, incidentID int64) ([]domain.IncidentEvent, error) {
	f.mu.Lock()
	f.listByIncidentCalls = append(f.listByIncidentCalls, incidentID)
	f.mu.Unlock()
	if f.ListByIncidentFn == nil {
		return nil, nil
	}
	return f.ListByIncidentFn(ctx, incidentID)
}

var _ port.IncidentEventRepository = (*fakeIncidentEventRepository)(nil)

// fakeSpeechToText is a fake port.SpeechToText.
type fakeSpeechToText struct {
	mu        sync.Mutex
	OpenFn    func(ctx context.Context) (port.TranscriptionStream, error)
	openCalls int
}

func (f *fakeSpeechToText) Open(ctx context.Context) (port.TranscriptionStream, error) {
	f.mu.Lock()
	f.openCalls++
	f.mu.Unlock()
	if f.OpenFn == nil {
		return nil, nil
	}
	return f.OpenFn(ctx)
}

var _ port.SpeechToText = (*fakeSpeechToText)(nil)

// fakeTranscriptionStream is a fake port.TranscriptionStream. Tests drive it
// with Emit and CloseResults, and can inspect SentAudio for what was written.
type fakeTranscriptionStream struct {
	mu        sync.Mutex
	results   chan domain.TranscriptSegment
	closeOnce sync.Once
	closed    bool
	sentAudio [][]byte

	FinishFn    func(ctx context.Context) error
	CloseFn     func() error
	SendAudioFn func(mulaw []byte) error
}

func newFakeTranscriptionStream() *fakeTranscriptionStream {
	return &fakeTranscriptionStream{results: make(chan domain.TranscriptSegment, 64)}
}

func (f *fakeTranscriptionStream) SendAudio(mulaw []byte) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return domain.ErrStreamClosed
	}
	cp := make([]byte, len(mulaw))
	copy(cp, mulaw)
	f.sentAudio = append(f.sentAudio, cp)
	fn := f.SendAudioFn
	f.mu.Unlock()
	if fn != nil {
		return fn(mulaw)
	}
	return nil
}

func (f *fakeTranscriptionStream) Results() <-chan domain.TranscriptSegment {
	return f.results
}

func (f *fakeTranscriptionStream) Finish(ctx context.Context) error {
	f.mu.Lock()
	f.closed = true
	fn := f.FinishFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	f.CloseResults()
	return nil
}

func (f *fakeTranscriptionStream) Close() error {
	f.mu.Lock()
	f.closed = true
	fn := f.CloseFn
	f.mu.Unlock()
	f.CloseResults()
	if fn != nil {
		return fn()
	}
	return nil
}

// Emit delivers a segment to Results. Safe to call from a test goroutine.
func (f *fakeTranscriptionStream) Emit(seg domain.TranscriptSegment) {
	f.results <- seg
}

// CloseResults closes the Results channel. Idempotent.
func (f *fakeTranscriptionStream) CloseResults() {
	f.closeOnce.Do(func() { close(f.results) })
}

func (f *fakeTranscriptionStream) SentAudio() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.sentAudio))
	copy(out, f.sentAudio)
	return out
}

var _ port.TranscriptionStream = (*fakeTranscriptionStream)(nil)

// fakeRCAGenerator is a fake port.RCAGenerator.
type fakeRCAGenerator struct {
	mu sync.Mutex

	GenerateFn    func(ctx context.Context, incident domain.Incident, events []domain.IncidentEvent) (domain.RCAReport, error)
	generateCalls int
}

func (f *fakeRCAGenerator) Generate(ctx context.Context, incident domain.Incident, events []domain.IncidentEvent) (domain.RCAReport, error) {
	f.mu.Lock()
	f.generateCalls++
	f.mu.Unlock()
	if f.GenerateFn == nil {
		return domain.RCAReport{}, nil
	}
	return f.GenerateFn(ctx, incident, events)
}

func (f *fakeRCAGenerator) GenerateCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generateCalls
}

var _ port.RCAGenerator = (*fakeRCAGenerator)(nil)

// fakeRCAPublisher is a fake port.RCAPublisher.
type fakeRCAPublisher struct {
	mu sync.Mutex

	PublishFn    func(ctx context.Context, doc domain.RCADocument) (string, error)
	publishCalls []domain.RCADocument
}

func (f *fakeRCAPublisher) Publish(ctx context.Context, doc domain.RCADocument) (string, error) {
	f.mu.Lock()
	f.publishCalls = append(f.publishCalls, doc)
	f.mu.Unlock()
	if f.PublishFn == nil {
		return "", nil
	}
	return f.PublishFn(ctx, doc)
}

var _ port.RCAPublisher = (*fakeRCAPublisher)(nil)

// fakeRCATrigger is a fake port.RCATrigger. It records every enqueued incident ID.
type fakeRCATrigger struct {
	mu        sync.Mutex
	EnqueueFn func(incidentID int64)
	enqueued  []int64
}

func (f *fakeRCATrigger) Enqueue(incidentID int64) {
	f.mu.Lock()
	f.enqueued = append(f.enqueued, incidentID)
	f.mu.Unlock()
	if f.EnqueueFn != nil {
		f.EnqueueFn(incidentID)
	}
}

func (f *fakeRCATrigger) Enqueued() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.enqueued))
	copy(out, f.enqueued)
	return out
}

var _ port.RCATrigger = (*fakeRCATrigger)(nil)
