<script setup lang="ts">
import QrcodeVue from 'qrcode.vue'
import { computed, ref } from 'vue'

import BrandMark from '@/app/components/BrandMark.vue'
import { AppAlert, AppButton, AppIcon, AppInput, FormField } from '@/shared/ui'

import { useIdentityStore } from '../store'

const identity = useIdentityStore()
const username = ref('admin')
const password = ref('')
const confirmPassword = ref('')
const verificationCode = ref('')
const useRecoveryCode = ref(false)
const localError = ref('')
const copied = ref(false)

const isBootstrap = computed(() => identity.phase === 'bootstrap')
const isCredentials = computed(() => identity.phase === 'bootstrap' || identity.phase === 'login')
const headline = computed(() => {
  if (identity.phase === 'bootstrap') return 'Create the first administrator.'
  if (identity.phase === 'enroll') return 'Protect every privileged action.'
  if (identity.phase === 'challenge') return 'Confirm it is really you.'
  if (identity.phase === 'recovery') return 'Keep your recovery path safe.'
  return 'Welcome back.'
})
const errorMessage = computed(() => localError.value || identity.error)

async function submitCredentials() {
  localError.value = ''
  if (isBootstrap.value && password.value !== confirmPassword.value) {
    localError.value = 'The passwords do not match.'
    return
  }
  try {
    if (isBootstrap.value) await identity.bootstrap(username.value, password.value)
    else await identity.login(username.value, password.value)
    password.value = ''
    confirmPassword.value = ''
  } catch {
    // The store exposes the server-safe error below.
  }
}

async function submitMFA() {
  localError.value = ''
  try {
    if (identity.phase === 'enroll') await identity.confirmEnrollment(verificationCode.value)
    else await identity.verify(verificationCode.value, useRecoveryCode.value)
    verificationCode.value = ''
  } catch {
    // The store exposes the server-safe error below.
  }
}

async function copyRecoveryCodes() {
  try {
    await navigator.clipboard.writeText(identity.recoveryCodes.join('\n'))
    copied.value = true
  } catch {
    localError.value = 'Copying was blocked. Save each recovery code manually.'
  }
}
</script>

<template>
  <main class="grid min-h-screen lg:grid-cols-[1.05fr_1fr]">
    <!-- Brand panel -->
    <section class="relative hidden flex-col justify-between overflow-hidden border-r border-outline bg-surface/50 p-10 lg:flex xl:p-14">
      <div
        class="pointer-events-none absolute -top-32 -right-32 size-96 rounded-full bg-accent-500/10 blur-3xl"
        aria-hidden="true"
      />
      <BrandMark subtitle="Modern Server Management Platform" size="lg" />

      <div class="relative max-w-md">
        <p class="text-[11px] font-bold tracking-[0.14em] text-accent-400 uppercase">Secure local control plane</p>
        <h1 class="mt-3 text-3xl leading-tight font-semibold tracking-tight text-ink xl:text-4xl">{{ headline }}</h1>
        <p class="mt-4 text-[15px] leading-relaxed text-ink-secondary">
          Passwords and authenticator verification protect access to server, database, domain, and backup operations.
        </p>
      </div>

      <div class="relative flex flex-wrap gap-2.5" aria-label="Security properties">
        <span
          v-for="assurance in ['Argon2id passwords', 'Encrypted TOTP', 'One-time recovery']"
          :key="assurance"
          class="inline-flex items-center gap-1.5 rounded-full border border-outline-strong bg-white/[0.03] px-3 py-1.5 text-xs font-medium text-ink-secondary"
        >
          <AppIcon name="lock" :size="12" class="text-accent-400" />
          {{ assurance }}
        </span>
      </div>
    </section>

    <!-- Form panel -->
    <section class="flex items-center justify-center p-6 sm:p-10">
      <div class="w-full max-w-sm">
        <div class="mb-8 lg:hidden">
          <BrandMark subtitle="Modern Server Management Platform" />
        </div>

        <form v-if="isCredentials" class="space-y-4" @submit.prevent="submitCredentials">
          <div class="mb-6">
            <p class="text-[11px] font-bold tracking-[0.14em] text-accent-400 uppercase">
              {{ isBootstrap ? 'Initial setup' : 'Administrator access' }}
            </p>
            <h2 class="mt-1.5 text-2xl font-semibold tracking-tight text-ink">
              {{ isBootstrap ? 'Secure your panel' : 'Sign in' }}
            </h2>
            <p class="mt-1.5 text-sm text-ink-secondary">
              {{
                isBootstrap
                  ? 'Use at least 12 characters. Authenticator enrollment follows next.'
                  : 'Enter your administrator password to continue.'
              }}
            </p>
          </div>

          <FormField label="Username">
            <AppInput v-model.trim="username" name="username" autocomplete="username" required minlength="3" maxlength="64" />
          </FormField>
          <FormField label="Password">
            <AppInput
              v-model="password"
              name="password"
              type="password"
              :autocomplete="isBootstrap ? 'new-password' : 'current-password'"
              required
              :minlength="isBootstrap ? 12 : 1"
            />
          </FormField>
          <FormField v-if="isBootstrap" label="Confirm password">
            <AppInput v-model="confirmPassword" name="confirm-password" type="password" autocomplete="new-password" required minlength="12" />
          </FormField>

          <AppAlert v-if="errorMessage" tone="danger">{{ errorMessage }}</AppAlert>
          <AppButton variant="primary" type="submit" :loading="identity.loading" class="w-full">
            {{ isBootstrap ? 'Create administrator' : 'Continue' }}
          </AppButton>
        </form>

        <form v-else-if="identity.phase === 'enroll'" class="space-y-4" @submit.prevent="submitMFA">
          <div class="mb-6">
            <p class="text-[11px] font-bold tracking-[0.14em] text-accent-400 uppercase">Required second factor</p>
            <h2 class="mt-1.5 text-2xl font-semibold tracking-tight text-ink">Connect an authenticator</h2>
            <p class="mt-1.5 text-sm text-ink-secondary">
              Scan this QR code with any TOTP-compatible authenticator, then enter its six-digit code.
            </p>
          </div>

          <div v-if="identity.enrollment" class="space-y-3">
            <div class="grid place-items-center rounded-2xl border border-outline bg-white p-5">
              <QrcodeVue :value="identity.enrollment.provisioningUri" :size="176" level="M" background="#ffffff" foreground="#07111f" />
            </div>
            <div class="rounded-xl border border-outline bg-raised/60 px-4 py-3">
              <span class="block text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Manual setup key</span>
              <code class="mt-1 block font-mono text-[13px] break-all text-accent-200">{{ identity.enrollment.secret }}</code>
            </div>
          </div>
          <AppAlert v-else tone="info">Preparing encrypted authenticator enrollment…</AppAlert>

          <FormField label="Six-digit code">
            <AppInput
              v-model.trim="verificationCode"
              class="text-center font-mono text-lg tracking-[0.4em]"
              name="one-time-code"
              inputmode="numeric"
              autocomplete="one-time-code"
              pattern="[0-9]{6}"
              maxlength="6"
              required
            />
          </FormField>
          <AppAlert v-if="errorMessage" tone="danger">{{ errorMessage }}</AppAlert>
          <AppButton variant="primary" type="submit" :loading="identity.loading" :disabled="!identity.enrollment" class="w-full">
            Verify and enable MFA
          </AppButton>
        </form>

        <form v-else-if="identity.phase === 'challenge'" class="space-y-4" @submit.prevent="submitMFA">
          <div class="mb-6">
            <p class="text-[11px] font-bold tracking-[0.14em] text-accent-400 uppercase">Second-factor verification</p>
            <h2 class="mt-1.5 text-2xl font-semibold tracking-tight text-ink">
              {{ useRecoveryCode ? 'Use a recovery code' : 'Authenticator code' }}
            </h2>
            <p class="mt-1.5 text-sm text-ink-secondary">
              {{ useRecoveryCode ? 'Each recovery code works once.' : 'Enter the current six-digit code from your authenticator.' }}
            </p>
          </div>

          <FormField :label="useRecoveryCode ? 'Recovery code' : 'Six-digit code'">
            <AppInput
              v-model.trim="verificationCode"
              class="text-center font-mono text-lg tracking-[0.3em]"
              name="verification-code"
              :inputmode="useRecoveryCode ? 'text' : 'numeric'"
              autocomplete="one-time-code"
              :pattern="useRecoveryCode ? undefined : '[0-9]{6}'"
              :maxlength="useRecoveryCode ? 19 : 6"
              required
              autofocus
            />
          </FormField>
          <button
            type="button"
            class="text-[13px] font-medium text-accent-300 hover:text-accent-200"
            @click="((useRecoveryCode = !useRecoveryCode), (verificationCode = ''))"
          >
            {{ useRecoveryCode ? 'Use authenticator instead' : 'Use a recovery code' }}
          </button>
          <AppAlert v-if="errorMessage" tone="danger">{{ errorMessage }}</AppAlert>
          <AppButton variant="primary" type="submit" :loading="identity.loading" class="w-full">Verify and sign in</AppButton>
        </form>

        <section v-else-if="identity.phase === 'recovery'" class="space-y-4">
          <div class="mb-6">
            <p class="text-[11px] font-bold tracking-[0.14em] text-amber-400 uppercase">Shown only once</p>
            <h2 class="mt-1.5 text-2xl font-semibold tracking-tight text-ink">Save your recovery codes</h2>
            <p class="mt-1.5 text-sm text-ink-secondary">
              Store these outside this server. Each code can replace your authenticator exactly once.
            </p>
          </div>

          <div class="grid grid-cols-2 gap-2">
            <code
              v-for="code in identity.recoveryCodes"
              :key="code"
              class="rounded-lg border border-outline bg-raised/60 px-3 py-2 text-center font-mono text-[13px] text-accent-200"
            >
              {{ code }}
            </code>
          </div>

          <AppButton :icon="copied ? 'check' : 'copy'" class="w-full" @click="copyRecoveryCodes">
            {{ copied ? 'Copied' : 'Copy all codes' }}
          </AppButton>
          <AppAlert v-if="localError" tone="danger">{{ localError }}</AppAlert>
          <AppButton variant="primary" class="w-full" @click="identity.acknowledgeRecoveryCodes()">I saved these codes</AppButton>
        </section>
      </div>
    </section>
  </main>
</template>
