package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// callConfirmDigit is the keypad digit that confirms an incident is resolved.
const callConfirmDigit = "1"

// CallService holds the phone-call control logic: caller authorization,
// incident-number validation, finding, creating or reopening incidents, and
// turning the SRE's confirmation into a resolved incident with RCA generation
// queued. It returns domain results and errors; the Twilio adapter renders them.
type CallService struct {
	engineers port.EngineerRepository
	incidents port.IncidentRepository
	trigger   port.RCATrigger
	clock     port.Clock
	logger    *slog.Logger
}

var _ port.CallService = (*CallService)(nil)

// NewCallService returns a CallService. A nil logger discards log output.
func NewCallService(
	engineers port.EngineerRepository,
	incidents port.IncidentRepository,
	trigger port.RCATrigger,
	clock port.Clock,
	logger *slog.Logger,
) *CallService {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &CallService{
		engineers: engineers,
		incidents: incidents,
		trigger:   trigger,
		clock:     clock,
		logger:    logger,
	}
}

// AuthorizeCaller returns the engineer that owns phoneNumber, or
// domain.ErrUnauthorizedCaller if the number isn't allowlisted.
func (s *CallService) AuthorizeCaller(ctx context.Context, phoneNumber string) (domain.Engineer, error) {
	phone := strings.TrimSpace(phoneNumber)
	masked := domain.MaskPhone(phone)

	engineer, err := s.engineers.GetByPhoneNumber(ctx, phone)
	if errors.Is(err, domain.ErrNotFound) {
		s.logger.WarnContext(ctx, "call: unauthorized caller", "phone", masked)
		return domain.Engineer{}, domain.ErrUnauthorizedCaller
	}
	if err != nil {
		return domain.Engineer{}, fmt.Errorf("call: authorize caller %s: %w", masked, err)
	}

	s.logger.DebugContext(ctx, "call: caller authorized", "phone", masked, "engineer_id", engineer.ID)
	return engineer, nil
}

// BeginRecording authorizes the caller, validates the keypad digits, and
// finds, creates or reopens the incident.
func (s *CallService) BeginRecording(ctx context.Context, phoneNumber, digits string) (port.RecordingStart, error) {
	engineer, err := s.AuthorizeCaller(ctx, phoneNumber)
	if err != nil {
		return port.RecordingStart{}, err
	}

	id, err := domain.ParseIncidentNumber(digits)
	if err != nil {
		return port.RecordingStart{}, err
	}

	incident, created, err := s.incidents.GetOrCreate(ctx, id)
	if err != nil {
		return port.RecordingStart{}, fmt.Errorf("call: get or create incident %d: %w", id, err)
	}

	incident, err = s.callEnsureRecordable(ctx, incident)
	if err != nil {
		return port.RecordingStart{}, err
	}

	s.logger.InfoContext(ctx, "call: recording started",
		"incident_id", incident.ID, "engineer_id", engineer.ID, "created", created)
	return port.RecordingStart{Engineer: engineer, Incident: incident, Resumed: !created}, nil
}

// ResumeRecording re-checks an existing incident after the SRE declines the
// confirmation prompt, reopening it if needed.
func (s *CallService) ResumeRecording(ctx context.Context, phoneNumber string, incidentID int64) (port.RecordingStart, error) {
	engineer, err := s.AuthorizeCaller(ctx, phoneNumber)
	if err != nil {
		return port.RecordingStart{}, err
	}

	incident, err := s.incidents.Get(ctx, incidentID)
	if err != nil {
		return port.RecordingStart{}, fmt.Errorf("call: get incident %d: %w", incidentID, err)
	}

	incident, err = s.callEnsureRecordable(ctx, incident)
	if err != nil {
		return port.RecordingStart{}, err
	}

	s.logger.InfoContext(ctx, "call: recording resumed", "incident_id", incident.ID, "engineer_id", engineer.ID)
	return port.RecordingStart{Engineer: engineer, Incident: incident, Resumed: true}, nil
}

// ConfirmResolution marks the incident resolved and enqueues RCA generation
// when digits is "1". Any other input returns false, nil.
func (s *CallService) ConfirmResolution(ctx context.Context, phoneNumber string, incidentID int64, digits string) (bool, error) {
	if _, err := s.AuthorizeCaller(ctx, phoneNumber); err != nil {
		return false, err
	}

	if strings.TrimSpace(digits) != callConfirmDigit {
		return false, nil
	}

	_, err := s.incidents.MarkResolved(ctx, incidentID, s.clock.Now().UTC())
	if errors.Is(err, domain.ErrInvalidTransition) {
		return s.callCheckAlreadyResolved(ctx, incidentID, err)
	}
	if err != nil {
		return false, fmt.Errorf("call: mark incident %d resolved: %w", incidentID, err)
	}

	s.trigger.Enqueue(incidentID)
	s.logger.InfoContext(ctx, "call: incident resolved, RCA enqueued", "incident_id", incidentID)
	return true, nil
}

// callCheckAlreadyResolved handles ErrInvalidTransition from MarkResolved. If
// the incident is already pending or generating (for example, Twilio retried
// the webhook), the confirmation is treated as done without enqueueing again.
func (s *CallService) callCheckAlreadyResolved(ctx context.Context, incidentID int64, transitionErr error) (bool, error) {
	incident, err := s.incidents.Get(ctx, incidentID)
	if err != nil {
		return false, fmt.Errorf("call: get incident %d after invalid transition: %w", incidentID, err)
	}
	switch incident.Status {
	case domain.IncidentStatusRCAPending, domain.IncidentStatusRCAGenerating:
		s.logger.InfoContext(ctx, "call: incident already resolved, not enqueueing again",
			"incident_id", incidentID, "status", incident.Status)
		return true, nil
	default:
		return false, fmt.Errorf("call: mark incident %d resolved (status %s): %w", incidentID, incident.Status, transitionErr)
	}
}

// callEnsureRecordable applies the status rules for starting or resuming a
// recording: open continues, rca_complete and rca_failed are reopened (keeping
// ResolutionTime), and rca_pending and rca_generating are busy.
func (s *CallService) callEnsureRecordable(ctx context.Context, incident domain.Incident) (domain.Incident, error) {
	switch incident.Status {
	case domain.IncidentStatusOpen:
		return incident, nil
	case domain.IncidentStatusRCAComplete, domain.IncidentStatusRCAFailed:
		if err := s.incidents.SetStatus(ctx, incident.ID, domain.IncidentStatusOpen); err != nil {
			return domain.Incident{}, fmt.Errorf("call: reopen incident %d: %w", incident.ID, err)
		}
		s.logger.InfoContext(ctx, "call: incident reopened", "incident_id", incident.ID, "previous_status", incident.Status)
		incident.Status = domain.IncidentStatusOpen
		return incident, nil
	case domain.IncidentStatusRCAPending, domain.IncidentStatusRCAGenerating:
		return domain.Incident{}, domain.ErrIncidentBusy
	default:
		return domain.Incident{}, fmt.Errorf("call: incident %d has unknown status %q: %w",
			incident.ID, incident.Status, domain.ErrInvalidTransition)
	}
}
