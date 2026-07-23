package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ErrUnauditable marks an action refused because its audit entry could not be
// written. Callers match on it to answer "the audit log is unavailable" instead
// of reporting the action itself as invalid.
var ErrUnauditable = errors.New("action refused: it could not be recorded in the audit log")

// Sink chooses the failure semantics of an audit write per event, so callers
// stop deciding that by dropping the error with "_ =".
//
// A security-sensitive mutation must never happen unauditably: RecordSensitive
// returns the write failure so the caller aborts before touching the node.
// Everything else stays best-effort — a briefly locked audit table must not make
// the panel unusable — but the drop is always logged at ERROR with the action
// and subject, so a lost event is still discoverable.
type Sink struct {
	recorder Recorder
	logger   *slog.Logger
}

func NewSink(recorder Recorder, logger *slog.Logger) Sink {
	if logger == nil {
		logger = slog.Default()
	}
	return Sink{recorder: recorder, logger: logger}
}

// RecordSensitive writes an event fail-closed: the returned error must abort the
// caller's action.
func (s Sink) RecordSensitive(ctx context.Context, entry Entry) error {
	if s.recorder == nil {
		return fmt.Errorf("record %s audit event: %w", entry.Action, ErrUnauditable)
	}
	if err := s.recorder.Record(ctx, entry); err != nil {
		s.logger.Error("refused an unauditable action", "action", entry.Action, "subject", entry.Subject, "error", err)
		return fmt.Errorf("record %s audit event: %v: %w", entry.Action, err, ErrUnauditable)
	}
	return nil
}

// RecordBestEffort writes an event whose loss must not block the caller, and
// logs the loss at ERROR when it happens.
func (s Sink) RecordBestEffort(ctx context.Context, entry Entry) {
	if s.recorder == nil {
		s.logger.Error("dropped an audit event", "action", entry.Action, "subject", entry.Subject, "error", "no audit recorder is configured")
		return
	}
	if err := s.recorder.Record(ctx, entry); err != nil {
		s.logger.Error("dropped an audit event", "action", entry.Action, "subject", entry.Subject, "error", err)
	}
}
