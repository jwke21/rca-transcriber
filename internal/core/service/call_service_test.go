package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

const (
	callTestPhone      = "+15555550123"
	callTestIncidentID = int64(4821)
)

var (
	callTestNow      = time.Date(2026, 9, 27, 14, 30, 0, 0, time.UTC)
	callTestEngineer = domain.Engineer{ID: 7, PhoneNumber: callTestPhone}
	callTestErrDB    = errors.New("db exploded")
)

// callTestDeps bundles the fakes behind one CallService under test.
type callTestDeps struct {
	engineers *fakeEngineerRepository
	incidents *fakeIncidentRepository
	trigger   *fakeRCATrigger
	clock     *fakeClock
	logs      *bytes.Buffer
	svc       *CallService
}

// callTestNew builds a CallService whose engineer repository authorizes
// callTestPhone only.
func callTestNew() *callTestDeps {
	d := &callTestDeps{
		engineers: &fakeEngineerRepository{
			GetByPhoneNumberFn: func(_ context.Context, phone string) (domain.Engineer, error) {
				if phone == callTestPhone {
					return callTestEngineer, nil
				}
				return domain.Engineer{}, domain.ErrNotFound
			},
		},
		incidents: &fakeIncidentRepository{},
		trigger:   &fakeRCATrigger{},
		clock:     newFakeClock(callTestNow),
		logs:      &bytes.Buffer{},
	}
	logger := slog.New(slog.NewTextHandler(d.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	d.svc = NewCallService(d.engineers, d.incidents, d.trigger, d.clock, logger)
	return d
}

func callTestIncident(status domain.IncidentStatus) domain.Incident {
	return domain.Incident{ID: callTestIncidentID, Status: status, CreatedAt: callTestNow.Add(-time.Hour)}
}

// callTestAssertNoIncidentCalls checks the incident repository was not touched.
func callTestAssertNoIncidentCalls(t *testing.T, repo *fakeIncidentRepository) {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.getOrCreateCalls)
	assert.Empty(t, repo.getCalls)
	assert.Empty(t, repo.setStatusCalls)
	assert.Empty(t, repo.markResolvedCalls)
}

func TestNewCallService_NilLogger(t *testing.T) {
	svc := NewCallService(&fakeEngineerRepository{}, &fakeIncidentRepository{}, &fakeRCATrigger{}, newFakeClock(callTestNow), nil)
	require.NotNil(t, svc)
	_, err := svc.AuthorizeCaller(context.Background(), callTestPhone)
	assert.NoError(t, err)
}

func TestCallService_AuthorizeCaller(t *testing.T) {
	tests := []struct {
		name        string
		phone       string
		lookupErr   error
		wantErr     error
		wantWrapped bool
		wantLookup  string
	}{
		{name: "allowlisted", phone: callTestPhone, wantLookup: callTestPhone},
		{name: "trimmed", phone: "  " + callTestPhone + "\n", wantLookup: callTestPhone},
		{name: "not allowlisted", phone: "+15555559999", wantErr: domain.ErrUnauthorizedCaller, wantLookup: "+15555559999"},
		{name: "repository error", phone: callTestPhone, lookupErr: callTestErrDB, wantErr: callTestErrDB, wantWrapped: true, wantLookup: callTestPhone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := callTestNew()
			if tc.lookupErr != nil {
				d.engineers.GetByPhoneNumberFn = func(context.Context, string) (domain.Engineer, error) {
					return domain.Engineer{}, tc.lookupErr
				}
			}

			got, err := d.svc.AuthorizeCaller(context.Background(), tc.phone)

			assert.Equal(t, []string{tc.wantLookup}, d.engineers.getByPhoneNumberCalls)
			assert.NotContains(t, d.logs.String(), tc.wantLookup, "full phone number must never be logged")
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.NotErrorIs(t, err, domain.ErrNotFound)
				if tc.wantWrapped {
					assert.NotEqual(t, tc.wantErr, err, "error should be wrapped with context")
					assert.NotErrorIs(t, err, domain.ErrUnauthorizedCaller)
					assert.NotContains(t, err.Error(), tc.wantLookup, "error must not carry the full number")
				}
				assert.Equal(t, domain.Engineer{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, callTestEngineer, got)
			assert.Contains(t, d.logs.String(), domain.MaskPhone(callTestPhone))
		})
	}
}

// TestCallService_AuthorizationFailures covers every method with an
// unauthorized caller and with an engineer-repository error. None of them may
// touch the incident repository or the RCA trigger.
func TestCallService_AuthorizationFailures(t *testing.T) {
	methods := []struct {
		name string
		call func(svc *CallService, phone string) error
	}{
		{name: "AuthorizeCaller", call: func(svc *CallService, phone string) error {
			_, err := svc.AuthorizeCaller(context.Background(), phone)
			return err
		}},
		{name: "BeginRecording", call: func(svc *CallService, phone string) error {
			_, err := svc.BeginRecording(context.Background(), phone, "4821")
			return err
		}},
		{name: "ResumeRecording", call: func(svc *CallService, phone string) error {
			_, err := svc.ResumeRecording(context.Background(), phone, callTestIncidentID)
			return err
		}},
		{name: "ConfirmResolution", call: func(svc *CallService, phone string) error {
			confirmed, err := svc.ConfirmResolution(context.Background(), phone, callTestIncidentID, "1")
			if confirmed {
				return errors.New("confirmed unexpectedly")
			}
			return err
		}},
	}
	failures := []struct {
		name      string
		phone     string
		lookupErr error
		wantErr   error
	}{
		{name: "unauthorized caller", phone: "+15555559999", wantErr: domain.ErrUnauthorizedCaller},
		{name: "engineer repository error", phone: callTestPhone, lookupErr: callTestErrDB, wantErr: callTestErrDB},
	}
	for _, m := range methods {
		for _, f := range failures {
			t.Run(m.name+"/"+f.name, func(t *testing.T) {
				d := callTestNew()
				if f.lookupErr != nil {
					d.engineers.GetByPhoneNumberFn = func(context.Context, string) (domain.Engineer, error) {
						return domain.Engineer{}, f.lookupErr
					}
				}

				err := m.call(d.svc, f.phone)

				require.Error(t, err)
				assert.ErrorIs(t, err, f.wantErr)
				callTestAssertNoIncidentCalls(t, d.incidents)
				assert.Empty(t, d.trigger.Enqueued())
			})
		}
	}
}

func TestCallService_BeginRecording(t *testing.T) {
	resolvedAt := callTestNow.Add(-24 * time.Hour)
	tests := []struct {
		name          string
		existing      domain.Incident
		created       bool
		getOrCreateEr error
		setStatusErr  error
		wantErr       error
		wantWrapped   bool
		wantResumed   bool
		wantStatus    domain.IncidentStatus
		wantSetStatus []setStatusCall
	}{
		{
			name:        "new incident",
			existing:    callTestIncident(domain.IncidentStatusOpen),
			created:     true,
			wantResumed: false,
			wantStatus:  domain.IncidentStatusOpen,
		},
		{
			name:        "existing open incident",
			existing:    callTestIncident(domain.IncidentStatusOpen),
			wantResumed: true,
			wantStatus:  domain.IncidentStatusOpen,
		},
		{
			name:          "rca_complete is reopened",
			existing:      callTestIncident(domain.IncidentStatusRCAComplete),
			wantResumed:   true,
			wantStatus:    domain.IncidentStatusOpen,
			wantSetStatus: []setStatusCall{{callTestIncidentID, domain.IncidentStatusOpen}},
		},
		{
			name:          "rca_failed is reopened",
			existing:      callTestIncident(domain.IncidentStatusRCAFailed),
			wantResumed:   true,
			wantStatus:    domain.IncidentStatusOpen,
			wantSetStatus: []setStatusCall{{callTestIncidentID, domain.IncidentStatusOpen}},
		},
		{
			name:     "rca_pending is busy",
			existing: callTestIncident(domain.IncidentStatusRCAPending),
			wantErr:  domain.ErrIncidentBusy,
		},
		{
			name:     "rca_generating is busy",
			existing: callTestIncident(domain.IncidentStatusRCAGenerating),
			wantErr:  domain.ErrIncidentBusy,
		},
		{
			name:     "unknown status",
			existing: callTestIncident(domain.IncidentStatus("bogus")),
			wantErr:  domain.ErrInvalidTransition,
		},
		{
			name:          "GetOrCreate error is wrapped",
			getOrCreateEr: callTestErrDB,
			wantErr:       callTestErrDB,
			wantWrapped:   true,
		},
		{
			name:          "SetStatus error is wrapped",
			existing:      callTestIncident(domain.IncidentStatusRCAFailed),
			setStatusErr:  callTestErrDB,
			wantErr:       callTestErrDB,
			wantWrapped:   true,
			wantSetStatus: []setStatusCall{{callTestIncidentID, domain.IncidentStatusOpen}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := callTestNew()
			existing := tc.existing
			existing.ResolutionTime = &resolvedAt
			d.incidents.GetOrCreateFn = func(context.Context, int64) (domain.Incident, bool, error) {
				if tc.getOrCreateEr != nil {
					return domain.Incident{}, false, tc.getOrCreateEr
				}
				return existing, tc.created, nil
			}
			d.incidents.SetStatusFn = func(context.Context, int64, domain.IncidentStatus) error {
				return tc.setStatusErr
			}

			got, err := d.svc.BeginRecording(context.Background(), callTestPhone, " 4821 ")

			assert.Equal(t, []int64{callTestIncidentID}, d.incidents.getOrCreateCalls)
			assert.Equal(t, tc.wantSetStatus, d.incidents.setStatusCalls)
			assert.Empty(t, d.trigger.Enqueued())
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				if tc.wantWrapped {
					assert.NotEqual(t, tc.wantErr, err, "error should be wrapped with context")
				}
				assert.Equal(t, port.RecordingStart{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, callTestEngineer, got.Engineer)
			assert.Equal(t, tc.wantResumed, got.Resumed)
			assert.Equal(t, callTestIncidentID, got.Incident.ID)
			assert.Equal(t, tc.wantStatus, got.Incident.Status)
			require.NotNil(t, got.Incident.ResolutionTime, "ResolutionTime must be kept")
			assert.Equal(t, resolvedAt, *got.Incident.ResolutionTime)
		})
	}
}

func TestCallService_BeginRecording_InvalidDigits(t *testing.T) {
	for _, digits := range []string{"", "0", "0000", "abc", "12a", "-5", "12#", "1234567890"} {
		t.Run("digits="+digits, func(t *testing.T) {
			d := callTestNew()

			got, err := d.svc.BeginRecording(context.Background(), callTestPhone, digits)

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidIncidentNumber)
			assert.Equal(t, port.RecordingStart{}, got)
			callTestAssertNoIncidentCalls(t, d.incidents)
			assert.Empty(t, d.trigger.Enqueued())
		})
	}
}

func TestCallService_ResumeRecording(t *testing.T) {
	tests := []struct {
		name          string
		getIncident   domain.Incident
		getErr        error
		setStatusErr  error
		wantErr       error
		wantStatus    domain.IncidentStatus
		wantSetStatus []setStatusCall
	}{
		{
			name:        "found open",
			getIncident: callTestIncident(domain.IncidentStatusOpen),
			wantStatus:  domain.IncidentStatusOpen,
		},
		{
			name:    "not found",
			getErr:  domain.ErrNotFound,
			wantErr: domain.ErrNotFound,
		},
		{
			name:    "get error",
			getErr:  callTestErrDB,
			wantErr: callTestErrDB,
		},
		{
			name:        "busy pending",
			getIncident: callTestIncident(domain.IncidentStatusRCAPending),
			wantErr:     domain.ErrIncidentBusy,
		},
		{
			name:        "busy generating",
			getIncident: callTestIncident(domain.IncidentStatusRCAGenerating),
			wantErr:     domain.ErrIncidentBusy,
		},
		{
			name:          "reopen complete",
			getIncident:   callTestIncident(domain.IncidentStatusRCAComplete),
			wantStatus:    domain.IncidentStatusOpen,
			wantSetStatus: []setStatusCall{{callTestIncidentID, domain.IncidentStatusOpen}},
		},
		{
			name:          "reopen failed",
			getIncident:   callTestIncident(domain.IncidentStatusRCAFailed),
			wantStatus:    domain.IncidentStatusOpen,
			wantSetStatus: []setStatusCall{{callTestIncidentID, domain.IncidentStatusOpen}},
		},
		{
			name:          "reopen SetStatus error",
			getIncident:   callTestIncident(domain.IncidentStatusRCAFailed),
			setStatusErr:  callTestErrDB,
			wantErr:       callTestErrDB,
			wantSetStatus: []setStatusCall{{callTestIncidentID, domain.IncidentStatusOpen}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := callTestNew()
			d.incidents.GetFn = func(context.Context, int64) (domain.Incident, error) {
				return tc.getIncident, tc.getErr
			}
			d.incidents.SetStatusFn = func(context.Context, int64, domain.IncidentStatus) error {
				return tc.setStatusErr
			}

			got, err := d.svc.ResumeRecording(context.Background(), callTestPhone, callTestIncidentID)

			assert.Equal(t, []int64{callTestIncidentID}, d.incidents.getCalls)
			assert.Empty(t, d.incidents.getOrCreateCalls)
			assert.Equal(t, tc.wantSetStatus, d.incidents.setStatusCalls)
			assert.Empty(t, d.trigger.Enqueued())
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Equal(t, port.RecordingStart{}, got)
				return
			}
			require.NoError(t, err)
			assert.True(t, got.Resumed)
			assert.Equal(t, callTestEngineer, got.Engineer)
			assert.Equal(t, callTestIncidentID, got.Incident.ID)
			assert.Equal(t, tc.wantStatus, got.Incident.Status)
		})
	}
}

func TestCallService_ConfirmResolution(t *testing.T) {
	// The fake clock returns a non-UTC time to prove the service normalizes it.
	nonUTC := callTestNow.In(time.FixedZone("UTC-7", -7*3600))

	tests := []struct {
		name          string
		digits        string
		markErr       error
		getIncident   domain.Incident
		getErr        error
		wantConfirmed bool
		wantErr       error
		wantMarkCalls int
		wantGetCalls  int
		wantEnqueued  []int64
	}{
		{name: "1 confirms", digits: "1", wantConfirmed: true, wantMarkCalls: 1, wantEnqueued: []int64{callTestIncidentID}},
		{name: "padded 1 is trimmed and confirms", digits: " 1 ", wantConfirmed: true, wantMarkCalls: 1, wantEnqueued: []int64{callTestIncidentID}},
		{name: "2 does not confirm", digits: "2"},
		{name: "empty does not confirm", digits: ""},
		{name: "pound does not confirm", digits: "#"},
		{name: "11 does not confirm", digits: "11"},
		{
			name:          "MarkResolved error, no enqueue",
			digits:        "1",
			markErr:       callTestErrDB,
			wantErr:       callTestErrDB,
			wantMarkCalls: 1,
		},
		{
			name:          "MarkResolved not found, no enqueue",
			digits:        "1",
			markErr:       domain.ErrNotFound,
			wantErr:       domain.ErrNotFound,
			wantMarkCalls: 1,
		},
		{
			name:          "retry while already pending is idempotent",
			digits:        "1",
			markErr:       domain.ErrInvalidTransition,
			getIncident:   callTestIncident(domain.IncidentStatusRCAPending),
			wantConfirmed: true,
			wantMarkCalls: 1,
			wantGetCalls:  1,
		},
		{
			name:          "retry while already generating is idempotent",
			digits:        "1",
			markErr:       domain.ErrInvalidTransition,
			getIncident:   callTestIncident(domain.IncidentStatusRCAGenerating),
			wantConfirmed: true,
			wantMarkCalls: 1,
			wantGetCalls:  1,
		},
		{
			name:          "invalid transition on open incident returns the error",
			digits:        "1",
			markErr:       domain.ErrInvalidTransition,
			getIncident:   callTestIncident(domain.IncidentStatusOpen),
			wantErr:       domain.ErrInvalidTransition,
			wantMarkCalls: 1,
			wantGetCalls:  1,
		},
		{
			name:          "invalid transition on complete incident returns the error",
			digits:        "1",
			markErr:       domain.ErrInvalidTransition,
			getIncident:   callTestIncident(domain.IncidentStatusRCAComplete),
			wantErr:       domain.ErrInvalidTransition,
			wantMarkCalls: 1,
			wantGetCalls:  1,
		},
		{
			name:          "invalid transition then Get error",
			digits:        "1",
			markErr:       domain.ErrInvalidTransition,
			getErr:        callTestErrDB,
			wantErr:       callTestErrDB,
			wantMarkCalls: 1,
			wantGetCalls:  1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := callTestNew()
			d.clock.Set(nonUTC)
			d.incidents.MarkResolvedFn = func(_ context.Context, id int64, at time.Time) (domain.Incident, error) {
				if tc.markErr != nil {
					return domain.Incident{}, tc.markErr
				}
				inc := callTestIncident(domain.IncidentStatusRCAPending)
				inc.ResolutionTime = &at
				return inc, nil
			}
			d.incidents.GetFn = func(context.Context, int64) (domain.Incident, error) {
				return tc.getIncident, tc.getErr
			}

			confirmed, err := d.svc.ConfirmResolution(context.Background(), callTestPhone, callTestIncidentID, tc.digits)

			assert.Equal(t, tc.wantConfirmed, confirmed)
			require.Len(t, d.incidents.markResolvedCalls, tc.wantMarkCalls)
			for _, c := range d.incidents.markResolvedCalls {
				assert.Equal(t, callTestIncidentID, c.id)
				assert.True(t, c.at.Equal(callTestNow), "resolved at the fake clock's time")
				assert.Equal(t, time.UTC, c.at.Location(), "resolution time must be UTC")
			}
			assert.Len(t, d.incidents.getCalls, tc.wantGetCalls)
			assert.Empty(t, d.incidents.setStatusCalls)
			assert.Empty(t, d.incidents.getOrCreateCalls)
			if tc.wantEnqueued == nil {
				assert.Empty(t, d.trigger.Enqueued())
			} else {
				assert.Equal(t, tc.wantEnqueued, d.trigger.Enqueued())
			}
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.NotEqual(t, tc.wantErr, err, "error should be wrapped with context")
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestCallService_ConfirmResolution_RetryDoesNotEnqueueTwice simulates Twilio
// retrying the confirm webhook: the first call enqueues, the retry sees the
// incident already pending and does not enqueue again.
func TestCallService_ConfirmResolution_RetryDoesNotEnqueueTwice(t *testing.T) {
	d := callTestNew()
	status := domain.IncidentStatusOpen
	d.incidents.MarkResolvedFn = func(_ context.Context, id int64, at time.Time) (domain.Incident, error) {
		if status != domain.IncidentStatusOpen {
			return domain.Incident{}, domain.ErrInvalidTransition
		}
		status = domain.IncidentStatusRCAPending
		return domain.Incident{ID: id, Status: status, ResolutionTime: &at}, nil
	}
	d.incidents.GetFn = func(_ context.Context, id int64) (domain.Incident, error) {
		return domain.Incident{ID: id, Status: status}, nil
	}

	for i := range 2 {
		confirmed, err := d.svc.ConfirmResolution(context.Background(), callTestPhone, callTestIncidentID, "1")
		require.NoError(t, err, "attempt %d", i+1)
		assert.True(t, confirmed, "attempt %d", i+1)
	}

	assert.Equal(t, []int64{callTestIncidentID}, d.trigger.Enqueued())
	assert.Len(t, d.incidents.markResolvedCalls, 2)
}
