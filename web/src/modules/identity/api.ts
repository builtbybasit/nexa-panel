import { apiRequest } from '@/shared/api/request'

import type { SessionRole } from './permissions'

export interface User {
  id: string
  username: string
  role: SessionRole
}

export interface IdentityStatus {
  bootstrapRequired: boolean
  authenticated: boolean
  /** Whether the signed-in account has a confirmed second factor. */
  mfaEnabled: boolean
  mfaChallengeRequired: boolean
  /** An administrator must enroll before the session can enter the panel. */
  mfaEnrollmentRequired: boolean
  user?: User
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

function request<T>(path: string, init: RequestInit = {}, retryAfterMFAStepUp = true): Promise<T> {
  return apiRequest<T>(path, init, {
    errorPrefix: 'Identity request',
    createError: (message, status, code) =>
      new IdentityRequestError(message === `Identity request failed with status ${status}` ? 'The request could not be completed.' : message, status, code),
    retryAfterMFAStepUp,
  })
}

export function getIdentityStatus(): Promise<IdentityStatus> {
  return request('/api/v1/auth/status', {}, false)
}

export function bootstrap(username: string, password: string): Promise<PasswordResponse> {
  return request('/api/v1/auth/bootstrap', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  }, false)
}

export function login(username: string, password: string): Promise<PasswordResponse> {
  return request('/api/v1/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  }, false)
}

export function enrollMFA(): Promise<MFAEnrollment> {
  return request('/api/v1/auth/mfa/enroll', { method: 'POST' }, false)
}

export function confirmMFA(code: string): Promise<MFAConfirmation> {
  return request('/api/v1/auth/mfa/confirm', { method: 'POST', body: JSON.stringify({ code }) }, false)
}

export async function verifyMFA(value: string, recovery: boolean): Promise<User> {
  const response = await request<{ user: User }>('/api/v1/auth/mfa/verify', {
    method: 'POST',
    body: JSON.stringify(recovery ? { recoveryCode: value } : { code: value }),
  }, false)
  return response.user
}

export function disableMFA(password: string): Promise<{ user: User; mfaEnabled: false }> {
  return request('/api/v1/auth/mfa/disable', { method: 'POST', body: JSON.stringify({ password }) }, false)
}

export function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  return request('/api/v1/auth/password', {
    method: 'POST',
    body: JSON.stringify({ currentPassword, newPassword }),
  }, false)
}

export function logout(): Promise<void> {
  return request('/api/v1/auth/logout', { method: 'POST' }, false)
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
