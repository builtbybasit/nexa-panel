import { apiRequest } from '@/shared/api/request'

import type { SessionRole } from './permissions'

export interface User {
  id: string
  username: string
  role: SessionRole
}

export interface IdentityStatus {
  bootstrapRequired: boolean
  bootstrapTokenRequired: boolean
  authenticated: boolean
  /** Whether the signed-in account has a confirmed second factor. */
  mfaEnabled: boolean
  mfaChallengeRequired: boolean
  /**
   * The account has no second factor and could add one. Enrollment is optional
   * for every role, so this is only a nudge — never a reason to block the SPA.
   */
  mfaEnrollmentRecommended: boolean
  passwordPolicy: PasswordPolicy
  user?: User
}

export interface PasswordPolicy {
  minLength: number
  maxLength: number
  requiredClasses: number
  classExemptLength: number
  denylistApplied: boolean
  rejectsUsername: boolean
}

export type AuthenticationNext = 'mfa_enrollment' | 'mfa_challenge' | 'authenticated'

export interface PasswordResponse {
  user: User
  next: AuthenticationNext
}

export interface MFAEnrollment {
  secret: string
  provisioningUri: string
}

export interface MFAConfirmation {
  user: User
  recoveryCodes: string[]
}

export class IdentityRequestError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message)
  }
}

function request<T>(
  path: string,
  init: RequestInit = {},
  options: { retryAfterMFAStepUp?: boolean; handleUnauthorized?: boolean } = {},
): Promise<T> {
  return apiRequest<T>(path, init, {
    errorPrefix: 'Identity request',
    createError: (message, status, code) =>
      new IdentityRequestError(message === `Identity request failed with status ${status}` ? 'The request could not be completed.' : message, status, code),
    retryAfterMFAStepUp: options.retryAfterMFAStepUp,
    handleUnauthorized: options.handleUnauthorized,
  })
}

export function getIdentityStatus(): Promise<IdentityStatus> {
  return request('/api/v1/auth/status', {}, { retryAfterMFAStepUp: false, handleUnauthorized: false })
}

export function bootstrap(username: string, password: string, bootstrapToken = ''): Promise<PasswordResponse> {
  return request('/api/v1/auth/bootstrap', {
    method: 'POST',
    body: JSON.stringify({ username, password, ...(bootstrapToken ? { bootstrapToken } : {}) }),
  }, { retryAfterMFAStepUp: false, handleUnauthorized: false })
}

export function login(username: string, password: string): Promise<PasswordResponse> {
  return request('/api/v1/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  }, { retryAfterMFAStepUp: false, handleUnauthorized: false })
}

export function enrollMFA(): Promise<MFAEnrollment> {
  return request('/api/v1/auth/mfa/enroll', { method: 'POST' }, { retryAfterMFAStepUp: false, handleUnauthorized: false })
}

export function confirmMFA(code: string): Promise<MFAConfirmation> {
  return request('/api/v1/auth/mfa/confirm', { method: 'POST', body: JSON.stringify({ code }) }, { retryAfterMFAStepUp: false, handleUnauthorized: false })
}

export async function verifyMFA(value: string, recovery: boolean): Promise<User> {
  const response = await request<{ user: User }>('/api/v1/auth/mfa/verify', {
    method: 'POST',
    body: JSON.stringify(recovery ? { recoveryCode: value } : { code: value }),
  }, { retryAfterMFAStepUp: false, handleUnauthorized: false })
  return response.user
}

/** Retires an administrator's lost factor and returns replacement enrollment material. */
export function recoverAdministratorMFA(recoveryCode: string): Promise<MFAEnrollment> {
  return request(
    '/api/v1/auth/mfa/recover',
    { method: 'POST', body: JSON.stringify({ recoveryCode }) },
    { retryAfterMFAStepUp: false, handleUnauthorized: false },
  )
}

export function disableMFA(password: string): Promise<{ user: User; mfaEnabled: false }> {
  return request('/api/v1/auth/mfa/disable', { method: 'POST', body: JSON.stringify({ password }) }, { retryAfterMFAStepUp: false, handleUnauthorized: false })
}

export function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  return request('/api/v1/auth/password', {
    method: 'POST',
    body: JSON.stringify({ currentPassword, newPassword }),
  }, { retryAfterMFAStepUp: false, handleUnauthorized: false })
}

export function logout(): Promise<void> {
  return request('/api/v1/auth/logout', { method: 'POST' }, { retryAfterMFAStepUp: false, handleUnauthorized: false })
}

// --- Admin user management (permission users.manage) ---

export type ManagedRole = SessionRole

export interface ManagedUser {
  id: string
  username: string
  role: ManagedRole
  createdAt: string
  lastLoginAt?: string | null
  mfaConfirmed: boolean
  /** Site grants; only meaningful for the developer role. */
  siteIds?: string[] | null
}

export async function listUsers(): Promise<ManagedUser[]> {
  return (await request<{ items: ManagedUser[] }>('/api/v1/users')).items
}

export function createUser(input: { username: string; password: string; role: ManagedRole }): Promise<ManagedUser> {
  return request('/api/v1/users', { method: 'POST', body: JSON.stringify(input) })
}

export function updateUser(id: string, input: { role?: ManagedRole; password?: string }): Promise<void> {
  return request(`/api/v1/users/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(input) })
}

export function deleteUser(id: string): Promise<void> {
  return request(`/api/v1/users/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function replaceUserSites(id: string, siteIds: string[]): Promise<void> {
  return request(`/api/v1/users/${encodeURIComponent(id)}/sites`, { method: 'PUT', body: JSON.stringify({ siteIds }) })
}
