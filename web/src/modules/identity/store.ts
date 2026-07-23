import { defineStore } from 'pinia'

import { appQueryClient } from '@/shared/query/client'
import { requestDiscardUnsavedChanges } from '@/shared/navigation/unsavedChanges'

import { hasPermission, type Permission } from './permissions'

import {
  bootstrap as bootstrapRequest,
  confirmMFA,
  enrollMFA,
  getIdentityStatus,
  login as loginRequest,
  logout as logoutRequest,
  recoverAdministratorMFA,
  verifyMFA,
  type AuthenticationNext,
  type MFAEnrollment,
  type PasswordResponse,
  type PasswordPolicy,
  type User,
} from './api'

export type AuthenticationPhase = 'bootstrap' | 'login' | 'enroll' | 'challenge' | 'recovery' | 'authenticated'

export const useIdentityStore = defineStore('identity', {
  state: () => ({
    initialized: false,
    loading: false,
    phase: 'login' as AuthenticationPhase,
    user: undefined as User | undefined,
    /** Whether the signed-in account has a confirmed second factor. */
    mfaEnabled: false,
    /** The account could add a second factor. A nudge only: enrollment is optional. */
    mfaEnrollmentRecommended: false,
    bootstrapTokenRequired: false,
    /** Undefined until the server publishes its policy; forms render nothing before then. */
    passwordPolicy: undefined as PasswordPolicy | undefined,
    enrollment: undefined as MFAEnrollment | undefined,
    recoveryCodes: [] as string[],
    error: '',
  }),
  getters: {
    authenticated: (state) => state.phase === 'authenticated' && state.user !== undefined,
    can: (state) => (permission: Permission) => hasPermission(state.user?.role, permission),
  },
  actions: {
    async initialize() {
      this.loading = true
      this.error = ''
      try {
        const status = await getIdentityStatus()
        this.user = status.user
        this.mfaEnabled = status.mfaEnabled
        this.mfaEnrollmentRecommended = status.mfaEnrollmentRecommended
        this.bootstrapTokenRequired = status.bootstrapTokenRequired
        this.passwordPolicy = status.passwordPolicy
        if (status.bootstrapRequired) this.phase = 'bootstrap'
        else if (status.mfaChallengeRequired) this.phase = 'challenge'
        else if (status.authenticated) this.phase = 'authenticated'
        else this.phase = 'login'
      } catch (error) {
        this.error = error instanceof Error ? error.message : 'The control plane is unavailable.'
      } finally {
        this.initialized = true
        this.loading = false
      }
    },
    async bootstrap(username: string, password: string, bootstrapToken = '') {
      await this.runPassword(() => bootstrapRequest(username, password, bootstrapToken))
    },
    async login(username: string, password: string) {
      await this.runPassword(() => loginRequest(username, password))
    },
    async runPassword(action: () => Promise<PasswordResponse>) {
      this.loading = true
      this.error = ''
      try {
        const response = await action()
        appQueryClient.clear()
        this.user = response.user
        this.setNextPhase(response.next)
        if (this.phase === 'enroll') await this.prepareEnrollment()
      } catch (error) {
        this.error = error instanceof Error ? error.message : 'Authentication failed.'
        throw error
      } finally {
        this.loading = false
      }
    },
    setNextPhase(next: AuthenticationNext) {
      // `mfa_enrollment` is the server offering a second factor, not demanding
      // one: the session behind it is already authenticated, so this phase is
      // always skippable.
      this.mfaEnrollmentRecommended = next === 'mfa_enrollment'
      if (next === 'mfa_enrollment') this.phase = 'enroll'
      else if (next === 'mfa_challenge') this.phase = 'challenge'
      else this.phase = 'authenticated'
    },
    skipEnrollment() {
      this.enrollment = undefined
      this.error = ''
      this.phase = 'authenticated'
    },
    /** Reflect an enable/disable performed from the in-panel account security page. */
    setMfaEnabled(enabled: boolean) {
      this.mfaEnabled = enabled
      this.mfaEnrollmentRecommended = !enabled
    },
    async prepareEnrollment() {
      this.loading = true
      this.error = ''
      try {
        this.enrollment = await enrollMFA()
      } catch (error) {
        this.error = error instanceof Error ? error.message : 'MFA enrollment could not be loaded.'
        throw error
      } finally {
        this.loading = false
      }
    },
    async confirmEnrollment(code: string) {
      await this.runMFA(async () => {
        const response = await confirmMFA(code)
        this.user = response.user
        this.recoveryCodes = response.recoveryCodes
        this.enrollment = undefined
        this.mfaEnabled = true
        this.mfaEnrollmentRecommended = false
        this.phase = 'recovery'
      })
    },
    acknowledgeRecoveryCodes() {
      this.recoveryCodes = []
      this.phase = 'authenticated'
    },
    /**
     * Answer the sign-in challenge. A recovery code signs in every role — it is a
     * single-use factor, not a bypass — so an administrator who lost their phone
     * lands in the panel and can replace or disable the factor from there.
     */
    async verify(code: string, recovery: boolean) {
      await this.runMFA(async () => {
        this.user = await verifyMFA(code, recovery)
        this.mfaEnabled = true
        this.phase = 'authenticated'
      })
    },
    /**
     * Replace a lost administrator factor from the challenge itself. The server
     * spends the recovery code, retires the old secret and hands back fresh
     * enrollment material, so the operator continues straight into pairing.
     */
    async recoverEnrollment(recoveryCode: string) {
      await this.runMFA(async () => {
        this.enrollment = await recoverAdministratorMFA(recoveryCode)
        this.mfaEnabled = false
        this.mfaEnrollmentRecommended = true
        this.phase = 'enroll'
      })
    },
    async runMFA(action: () => Promise<void>) {
      this.loading = true
      this.error = ''
      try {
        await action()
      } catch (error) {
        this.error = error instanceof Error ? error.message : 'Verification failed.'
        throw error
      } finally {
        this.loading = false
      }
    },
    async logout() {
      if (!requestDiscardUnsavedChanges()) return
      this.loading = true
      this.error = ''
      try {
        await logoutRequest()
        this.expireSession()
      } catch (error) {
        this.error = error instanceof Error ? error.message : 'Sign out failed.'
      } finally {
        this.loading = false
      }
    },
    expireSession() {
      appQueryClient.clear()
      this.user = undefined
      this.mfaEnabled = false
      this.mfaEnrollmentRecommended = false
      this.phase = 'login'
      this.enrollment = undefined
      this.recoveryCodes = []
    },
  },
})
