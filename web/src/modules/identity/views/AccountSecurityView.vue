<script setup lang="ts">
import QrcodeVue from 'qrcode.vue'
import { computed, ref } from 'vue'

import {
  AppAlert,
  AppButton,
  AppCard,
  AppDialog,
  AppIcon,
  AppInput,
  FormField,
  PageHeader,
  PasswordField,
  StatusPill,
} from '@/shared/ui'
import { useToasts } from '@/shared/composables/useToasts'

import { changePassword, confirmMFA, disableMFA, enrollMFA, IdentityRequestError, type MFAEnrollment } from '../api'
import { useIdentityStore } from '../store'

/** Focus the field the moment its step renders; autofocus is unreliable inside a dialog. */
const vFocus = { mounted: (el: HTMLElement) => el.focus() }

const identity = useIdentityStore()
const toasts = useToasts()

const dialog = ref<'password' | 'enable' | 'disable'>()
const busy = ref(false)
const dialogError = ref('')

// Enable flow
const enableStep = ref<'enroll' | 'recovery'>('enroll')
const enrollment = ref<MFAEnrollment>()
const code = ref('')
const recoveryCodes = ref<string[]>([])
const codesSaved = ref(false)
const copied = ref(false)

// Disable flow
const password = ref('')

/** Only the rules the server says it applies; nothing is assumed before it answers. */
const passwordHint = computed(() => {
  const policy = identity.passwordPolicy
  if (!policy) return undefined
  const rules = [
    ...(policy.denylistApplied ? ['common passwords'] : []),
    ...(policy.rejectsUsername ? ['passwords containing your username'] : []),
  ]
  return rules.length ? `The server rejects ${rules.join(' and ')}.` : undefined
})

// Change-password flow
const currentPassword = ref('')
const newPassword = ref('')
const confirmNewPassword = ref('')

function messageFor(error: unknown, fallback: string) {
  if (error instanceof IdentityRequestError) return error.message
  return error instanceof Error ? error.message : fallback
}

function closeDialog() {
  if (busy.value) return
  dialog.value = undefined
}

function openPassword() {
  dialog.value = 'password'
  currentPassword.value = ''
  newPassword.value = ''
  confirmNewPassword.value = ''
  dialogError.value = ''
}

async function submitPassword() {
  dialogError.value = ''
  if (newPassword.value !== confirmNewPassword.value) {
    dialogError.value = 'The new passwords do not match.'
    return
  }
  busy.value = true
  try {
    await changePassword(currentPassword.value, newPassword.value)
    dialog.value = undefined
    toasts.push({
      title: 'Password changed',
      body: 'Your other sessions were signed out. Use your new password next time you sign in.',
      tone: 'success',
    })
  } catch (error) {
    dialogError.value = messageFor(error, 'The password could not be changed.')
  } finally {
    busy.value = false
  }
}

async function openEnable() {
  dialog.value = 'enable'
  enableStep.value = 'enroll'
  enrollment.value = undefined
  code.value = ''
  recoveryCodes.value = []
  codesSaved.value = false
  copied.value = false
  dialogError.value = ''
  await loadEnrollment()
}

async function loadEnrollment() {
  busy.value = true
  dialogError.value = ''
  try {
    enrollment.value = await enrollMFA()
  } catch (error) {
    dialogError.value = messageFor(error, 'Two-factor setup could not be started.')
  } finally {
    busy.value = false
  }
}

async function submitEnable() {
  if (!enrollment.value) return
  busy.value = true
  dialogError.value = ''
  try {
    const response = await confirmMFA(code.value)
    recoveryCodes.value = response.recoveryCodes
    // The server confirmed the factor and marked this session verified.
    identity.setMfaEnabled(true)
    enableStep.value = 'recovery'
  } catch (error) {
    dialogError.value = messageFor(error, 'That code was not accepted. Codes rotate every 30 seconds — try the newest one.')
  } finally {
    busy.value = false
  }
}

function openDisable() {
  dialog.value = 'disable'
  password.value = ''
  dialogError.value = ''
}

async function submitDisable() {
  busy.value = true
  dialogError.value = ''
  try {
    await disableMFA(password.value)
    identity.setMfaEnabled(false)
    dialog.value = undefined
  } catch (error) {
    dialogError.value = messageFor(error, 'Two-factor authentication could not be disabled.')
  } finally {
    busy.value = false
  }
}

async function copyRecoveryCodes() {
  try {
    await navigator.clipboard.writeText(recoveryCodes.value.join('\n'))
    copied.value = true
  } catch {
    dialogError.value = 'Copying was blocked by the browser. Download the codes or write them down instead.'
  }
}

function downloadRecoveryCodes() {
  const blob = new Blob([recoveryCodes.value.join('\n') + '\n'], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = 'nexa-panel-recovery-codes.txt'
  anchor.click()
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      eyebrow="Your account"
      title="Account security"
      description="Manage how your Nexa Panel account is protected."
    />

    <AppCard>
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="flex min-w-0 items-start gap-3">
          <span class="grid size-10 shrink-0 place-items-center rounded-xl border border-outline bg-raised/60 text-accent-300">
            <AppIcon name="key" :size="18" />
          </span>
          <div class="min-w-0">
            <h3 class="text-[15px] font-semibold text-ink">Password</h3>
            <p class="mt-1.5 max-w-xl text-[13px] leading-relaxed text-ink-secondary">
              The password you use to sign in. Changing it signs out your other sessions.
            </p>
          </div>
        </div>
        <div class="shrink-0">
          <AppButton icon="key" @click="openPassword">Change password</AppButton>
        </div>
      </div>
    </AppCard>

    <AppCard>
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="flex min-w-0 items-start gap-3">
          <span class="grid size-10 shrink-0 place-items-center rounded-xl border border-outline bg-raised/60 text-accent-300">
            <AppIcon name="shield" :size="18" />
          </span>
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <h3 class="text-[15px] font-semibold text-ink">Two-factor authentication</h3>
              <StatusPill
                :tone="identity.mfaEnabled ? 'success' : 'neutral'"
                :label="identity.mfaEnabled ? 'Enabled' : 'Disabled'"
                :description="
                  identity.mfaEnabled
                    ? 'A one-time code from your authenticator is required at sign-in.'
                    : 'Your account is protected by a password only.'
                "
              />
            </div>
            <p class="mt-1.5 max-w-xl text-[13px] leading-relaxed text-ink-secondary">
              {{
                identity.mfaEnabled
                  ? 'An authenticator code is required whenever you sign in. A stolen password alone cannot reach your servers.'
                  : 'Add a time-based one-time code from an authenticator app for a second layer of protection at sign-in.'
              }}
            </p>
          </div>
        </div>
        <div class="shrink-0">
          <AppButton v-if="!identity.mfaEnabled" variant="primary" icon="lock" @click="openEnable">
            Enable
          </AppButton>
          <AppButton v-else variant="danger" icon="lock" @click="openDisable">Disable</AppButton>
        </div>
      </div>
    </AppCard>

    <!-- Enable: scan + confirm, then one-time recovery codes -->
    <AppDialog
      :open="dialog === 'enable'"
      :title="enableStep === 'recovery' ? 'Save your recovery codes' : 'Enable two-factor authentication'"
      @close="closeDialog"
    >
      <form v-if="enableStep === 'enroll'" id="enable-mfa-form" class="space-y-4" @submit.prevent="submitEnable">
        <p class="text-[13px] leading-relaxed text-ink-secondary">
          Scan the QR code with any authenticator app, then enter the six-digit code it shows.
        </p>
        <div v-if="enrollment" class="space-y-3">
          <div class="grid place-items-center rounded-2xl border border-outline bg-white p-5">
            <QrcodeVue :value="enrollment.provisioningUri" :size="176" level="M" background="#ffffff" foreground="#07111f" />
          </div>
          <div class="rounded-xl border border-outline bg-raised/60 px-4 py-3">
            <span class="block text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Manual setup key</span>
            <code class="mt-1 block font-mono text-[13px] break-all text-accent-200">{{ enrollment.secret }}</code>
          </div>
        </div>
        <AppAlert v-else-if="busy" tone="info">Preparing authenticator enrollment…</AppAlert>

        <FormField label="Six-digit code">
          <AppInput
            v-model.trim="code"
            v-focus
            class="text-center font-mono text-lg tracking-[0.4em]"
            name="one-time-code"
            inputmode="numeric"
            autocomplete="one-time-code"
            pattern="[0-9]{6}"
            maxlength="6"
            required
          />
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
      </form>

      <section v-else class="space-y-4">
        <AppAlert tone="warning">
          These codes are shown only once. Store them outside this server — each replaces your authenticator exactly once.
        </AppAlert>
        <div class="grid grid-cols-2 gap-2">
          <code
            v-for="recovery in recoveryCodes"
            :key="recovery"
            class="rounded-lg border border-outline bg-raised/60 px-3 py-2 text-center font-mono text-[13px] text-accent-200"
          >
            {{ recovery }}
          </code>
        </div>
        <div class="grid grid-cols-2 gap-2">
          <AppButton icon="download" @click="downloadRecoveryCodes">Download codes (.txt)</AppButton>
          <AppButton :icon="copied ? 'check' : 'copy'" aria-live="polite" @click="copyRecoveryCodes">
            {{ copied ? 'Copied' : 'Copy all codes' }}
          </AppButton>
        </div>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <label
          class="flex cursor-pointer items-start gap-2.5 rounded-xl border border-outline bg-raised/40 px-4 py-3 text-[13px] leading-relaxed text-ink-secondary"
        >
          <input v-model="codesSaved" type="checkbox" class="mt-0.5 accent-accent-500" />
          I saved these codes somewhere outside this server.
        </label>
      </section>

      <template #footer>
        <AppButton v-if="enableStep === 'enroll'" :disabled="busy" @click="closeDialog">Cancel</AppButton>
        <AppButton
          v-if="enableStep === 'enroll'"
          variant="primary"
          type="submit"
          form="enable-mfa-form"
          :loading="busy"
          :disabled="!enrollment"
        >
          Verify and enable
        </AppButton>
        <AppButton v-else variant="primary" :disabled="!codesSaved" @click="closeDialog">Done</AppButton>
      </template>
    </AppDialog>

    <!-- Disable: re-confirm the account password -->
    <AppDialog :open="dialog === 'disable'" title="Disable two-factor authentication" size="sm" @close="closeDialog">
      <form id="disable-mfa-form" class="space-y-4" @submit.prevent="submitDisable">
        <p class="text-[13px] leading-relaxed text-ink-secondary">
          Your account will be protected by a password only, and this change is recorded in the audit log. Enter your
          current password to confirm.
        </p>
        <FormField label="Current password">
          <AppInput
            v-model="password"
            v-focus
            name="current-password"
            type="password"
            autocomplete="current-password"
            required
          />
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
      </form>
      <template #footer>
        <AppButton :disabled="busy" @click="closeDialog">Cancel</AppButton>
        <AppButton variant="danger" type="submit" form="disable-mfa-form" :loading="busy" :disabled="!password">
          Disable
        </AppButton>
      </template>
    </AppDialog>

    <!-- Change password -->
    <AppDialog :open="dialog === 'password'" title="Change password" @close="closeDialog">
      <form id="change-password-form" class="space-y-4" @submit.prevent="submitPassword">
        <FormField label="Current password">
          <AppInput
            v-model="currentPassword"
            v-focus
            name="current-password"
            type="password"
            autocomplete="current-password"
            required
          />
        </FormField>
        <PasswordField
          v-model="newPassword"
          label="New password"
          :minimum-length="identity.passwordPolicy?.minLength"
          :maximum-length="identity.passwordPolicy?.maxLength"
          :required-classes="identity.passwordPolicy?.requiredClasses"
          :class-exempt-length="identity.passwordPolicy?.classExemptLength"
          autocomplete="new-password"
          :hint="passwordHint"
          required
        />
        <FormField label="Confirm new password">
          <AppInput
            v-model="confirmNewPassword"
            name="confirm-password"
            type="password"
            autocomplete="new-password"
            :minlength="identity.passwordPolicy?.minLength"
            :maxlength="identity.passwordPolicy?.maxLength"
            required
          />
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
      </form>
      <template #footer>
        <AppButton :disabled="busy" @click="closeDialog">Cancel</AppButton>
        <AppButton
          variant="primary"
          type="submit"
          form="change-password-form"
          :loading="busy"
          :disabled="!currentPassword || !newPassword || !confirmNewPassword"
        >
          Change password
        </AppButton>
      </template>
    </AppDialog>
  </div>
</template>
