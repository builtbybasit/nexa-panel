import { defineStore } from 'pinia'

import {
  bootstrap as bootstrapRequest,
  confirmMFA,
  enrollMFA,
  getIdentityStatus,
  login as loginRequest,
  logout as logoutRequest,
  verifyMFA,
  type AuthenticationNext,
  type MFAEnrollment,
  type PasswordResponse,
  type User,
} from './api'

export type AuthenticationPhase = 'bootstrap' | 'login' | 'enroll' | 'challenge' | 'recovery' | 'authenticated'

export const useIdentityStore = defineStore('identity', {
  state: () => ({
    initialized: false,
    loading: false,
    phase: 'login' as AuthenticationPhase,
    user: undefined as User | undefined,
    enrollment: undefined as MFAEnrollment | undefined,
    recoveryCodes: [] as string[],
    error: '',
  }),
  getters: {
    authenticated: (state) => state.phase === 'authenticated' && state.user !== undefined,
  },
  actions: {
    async initialize() {
      this.loading = true
      this.error = ''
      try {
        const status = await getIdentityStatus()
        this.user = status.user
        if (status.bootstrapRequired) this.phase = 'bootstrap'
        else if (status.mfaEnrollmentRequired) this.phase = 'enroll'
        else if (status.mfaChallengeRequired) this.phase = 'challenge'
        else if (status.authenticated) this.phase = 'authenticated'
        else this.phase = 'login'
        if (this.phase === 'enroll') await this.prepareEnrollment()
      } catch (error) {
        this.error = error instanceof Error ? error.message : 'The control plane is unavailable.'
      } finally {
        this.initialized = true
        this.loading = false
      }
    },
    async bootstrap(username: string, password: string) {
      await this.runPassword(() => bootstrapRequest(username, password))
    },
    async login(username: string, password: string) {
      await this.runPassword(() => loginRequest(username, password))
    },
    async runPassword(action: () => Promise<PasswordResponse>) {
      this.loading = true
      this.error = ''
      try {
        const response = await action()
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
      this.phase = next === 'mfa_enrollment' ? 'enroll' : 'challenge'
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
        this.phase = 'recovery'
      })
    },
    acknowledgeRecoveryCodes() {
      this.recoveryCodes = []
      this.phase = 'authenticated'
    },
    async verify(code: string, recovery: boolean) {
      await this.runMFA(async () => {
        this.user = await verifyMFA(code, recovery)
        this.phase = 'authenticated'
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
      this.loading = true
      this.error = ''
      try {
        await logoutRequest()
        this.user = undefined
        this.phase = 'login'
        this.enrollment = undefined
        this.recoveryCodes = []
      } catch (error) {
        this.error = error instanceof Error ? error.message : 'Sign out failed.'
      } finally {
        this.loading = false
      }
    },
  },
})
