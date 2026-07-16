package identity

import (
	"context"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
)

func (m *Module) recordAudit(ctx context.Context, entry audit.Entry) {
	if err := m.audit.Record(ctx, entry); err != nil {
		m.logger.Error("record identity audit event", "action", entry.Action, "error", err)
	}
}
