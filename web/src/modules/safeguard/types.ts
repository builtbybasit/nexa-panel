/**
 * The shape the Firewall and Services pages both poll while a lockout-capable
 * change is counting down. It mirrors internal/modules/safeguard on the server,
 * which owns the timer: the browser only displays it.
 */
export interface LockoutRevert {
  id: string
  subject: string
  summary: string
  reasons: string[]
  jobId?: number
  revertJobId?: number
  armedAt: string
  expiresAt: string
  confirmedAt?: string
  /** armed | confirmed | reverted | revert_failed */
  state: string
  failure?: string
}

/**
 * The server's refusal of a change that can cut the operator off. It is not a
 * validation failure: the identical request succeeds once `confirmLockoutRisk`
 * is set, and is then applied behind a timed rollback.
 */
export class LockoutRiskError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'LockoutRiskError'
  }
}
