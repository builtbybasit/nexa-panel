package backups

import (
	"context"
	"time"
)

// Start runs schedule reconciliation only after the full application graph is
// valid. Error/pending schedules retry frequently, while a slower full pass
// repairs externally removed or edited unit files.
func (m *Module) Start(parent context.Context) {
	m.reconcileStartOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		m.reconcileCancel = cancel
		go func() {
			if err := m.reconcileSchedules(ctx, true); err != nil && ctx.Err() == nil {
				m.logger.Warn("initial backup schedule reconciliation failed", "error", err)
			}
			m.reconcileScheduleLoop(ctx)
		}()
	})
}

func (m *Module) Close() {
	m.reconcileCloseOnce.Do(func() {
		if m.reconcileCancel == nil {
			close(m.reconcileDone)
			return
		}
		m.reconcileCancel()
		<-m.reconcileDone
	})
}

func (m *Module) reconcileScheduleLoop(ctx context.Context) {
	defer close(m.reconcileDone)
	retryTicker := time.NewTicker(m.scheduleRetryInterval)
	fullTicker := time.NewTicker(m.scheduleFullInterval)
	defer retryTicker.Stop()
	defer fullTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-retryTicker.C:
			if err := m.reconcileSchedules(ctx, false); err != nil && ctx.Err() == nil {
				m.logger.Warn("retry backup schedule reconciliation", "error", err)
			}
		case <-fullTicker.C:
			if err := m.reconcileSchedules(ctx, true); err != nil && ctx.Err() == nil {
				m.logger.Warn("full backup schedule reconciliation", "error", err)
			}
		}
	}
}
