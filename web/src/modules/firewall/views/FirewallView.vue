<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, reactive, ref } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppInput,
  EmptyState,
  FormField,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/shared/ui/select'
import { useJobRunner } from '@/shared/composables/useJobRunner'

import LockoutRevertPanel from '@/modules/safeguard/LockoutRevertPanel.vue'

import {
  confirmFirewallRevert,
  firewallAction,
  getFirewallReverts,
  getFirewallStatus,
  LockoutRiskError,
  type FirewallRule,
} from '../api'

const identity = useIdentityStore()
const canWrite = computed(() => identity.can('firewall.write'))

// A short refetch keeps status and the rule table reflecting reality after an
// action without a manual refresh.
const statusQuery = useQuery({
  queryKey: ['firewall-status'],
  queryFn: getFirewallStatus,
  refetchInterval: 15_000,
  retry: false,
})
const status = computed(() => statusQuery.data.value)
const installed = computed(() => status.value?.installed ?? false)
const active = computed(() => status.value?.active ?? false)
const rules = computed(() => status.value?.rules ?? [])

const runner = useJobRunner()

// The new-rule form. Protocol "" means both; action is allow or deny.
const form = reactive({ port: '', protocol: 'both', action: 'allow', from: '', comment: '' })
const formError = ref('')

const protocolOptions = [
  { value: 'both', label: 'TCP + UDP' },
  { value: 'tcp', label: 'TCP' },
  { value: 'udp', label: 'UDP' },
]
const actionOptions = [
  { value: 'allow', label: 'Allow' },
  { value: 'deny', label: 'Deny' },
]

// The armed reverts are the server's, not this page's: it polls them so a
// reload, a second browser, or a colleague's session all see the same countdown
// and the same confirm button.
const revertsQuery = useQuery({
  queryKey: ['firewall-reverts'],
  queryFn: getFirewallReverts,
  refetchInterval: 5_000,
  retry: false,
})
const reverts = computed(() => revertsQuery.data.value?.items ?? [])
const confirmingRevert = ref('')

const pending = ref('')
const confirmDisable = ref(false)
const ruleToRemove = ref<FirewallRule>()
// Set when the server refuses a change as lockout-capable: it holds the rule to
// retry with the acknowledgement, plus the server's own explanation of why.
const lockoutRisk = ref<{ rule: FirewallRule; message: string }>()

function ruleKey(rule: FirewallRule): string {
  return `${rule.action}:${rule.port}/${rule.protocol}:${rule.from}:${rule.v6 ? 'v6' : 'v4'}`
}

function describeTarget(rule: FirewallRule): string {
  const proto = rule.protocol ? `/${rule.protocol}` : ''
  const port = rule.port || 'any'
  const from = rule.from ? ` from ${rule.from}` : ''
  return `${port}${proto}${from}`
}

async function submit() {
  if (!canWrite.value || runner.busy.value) return
  formError.value = ''
  if (!/^[0-9]{1,5}(:[0-9]{1,5})?$/.test(form.port.trim())) {
    formError.value = 'Enter a port (1-65535) or a range like 8000:8100.'
    return
  }
  if (form.from.trim() && form.protocol === 'both') {
    formError.value = 'A rule limited to a source address must specify TCP or UDP.'
    return
  }
  pending.value = 'add'
  await runner.run(
    async () =>
      (
        await firewallAction(form.action as 'allow' | 'deny', {
          port: form.port.trim(),
          protocol: form.protocol === 'both' ? '' : form.protocol,
          from: form.from.trim(),
          comment: form.comment.trim() || undefined,
        })
      ).job.id,
    {
      onSettled: async () => {
        await statusQuery.refetch()
      },
      successToast: `Rule added for ${form.port.trim()}`,
      failureMessage: 'Could not add the firewall rule',
    },
  )
  pending.value = ''
  form.port = ''
  form.from = ''
  form.comment = ''
}

async function remove(rule: FirewallRule, acknowledgeLockout = false) {
  if (!canWrite.value || runner.busy.value) return
  pending.value = ruleKey(rule)
  await runner.run(
    async () => {
      try {
        const submission = await firewallAction('delete', rule, acknowledgeLockout)
        // An accepted risky delete comes back with an armed revert; showing the
        // countdown immediately matters more here than anywhere else on the page.
        await revertsQuery.refetch()
        return submission.job.id
      } catch (caught) {
        // The server refused because this rule carries access. That is not a
        // failure to report: it is a second, better-informed question to ask.
        if (caught instanceof LockoutRiskError) {
          lockoutRisk.value = { rule, message: caught.message }
          return undefined
        }
        throw caught
      }
    },
    {
      onSettled: async () => {
        await Promise.all([statusQuery.refetch(), revertsQuery.refetch()])
      },
      successToast: `Rule removed for ${describeTarget(rule)}`,
      failureMessage: 'Could not remove the firewall rule',
    },
  )
  pending.value = ''
}

function requestRemove(rule: FirewallRule) {
  if (rule.action.toLowerCase() === 'allow') {
    ruleToRemove.value = rule
    return
  }
  void remove(rule)
}

function confirmRemoveRule() {
  const rule = ruleToRemove.value
  ruleToRemove.value = undefined
  if (rule) void remove(rule)
}

// The operator has read the server's reasons and still wants the change. It goes
// through with the acknowledgement set, which is what arms the timed rollback.
function acceptLockoutRisk() {
  const risk = lockoutRisk.value
  lockoutRisk.value = undefined
  if (risk) void remove(risk.rule, true)
}

async function confirmRevert(id: string) {
  confirmingRevert.value = id
  try {
    await confirmFirewallRevert(id)
  } finally {
    confirmingRevert.value = ''
    await revertsQuery.refetch()
  }
}

async function toggleFirewall() {
  if (!canWrite.value || runner.busy.value) return
  if (active.value) {
    confirmDisable.value = true
    return
  }
  pending.value = 'toggle'
  await runner.run(async () => (await firewallAction('enable')).job.id, {
    onSettled: async () => {
      await statusQuery.refetch()
    },
    successToast: 'Firewall enabled',
    failureMessage: 'Could not enable the firewall',
  })
  pending.value = ''
}

async function confirmDisableFirewall() {
  confirmDisable.value = false
  pending.value = 'toggle'
  await runner.run(async () => (await firewallAction('disable')).job.id, {
    onSettled: async () => {
      await statusQuery.refetch()
    },
    successToast: 'Firewall disabled',
    failureMessage: 'Could not disable the firewall',
  })
  pending.value = ''
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Server"
      title="Firewall"
      description="Open and close ports on this node's UFW firewall. Enabling the firewall automatically keeps SSH and the panel's HTTP/HTTPS ports open so you are never locked out."
    >
      <StatusPill
        v-if="!statusQuery.isPending.value"
        :tone="!installed ? 'neutral' : active ? 'success' : 'warning'"
        :label="!installed ? 'Not installed' : active ? 'Active' : 'Inactive'"
        :pulse="false"
      />
    </PageHeader>

    <LockoutRevertPanel
      :reverts="reverts"
      :confirming="confirmingRevert"
      :disabled="!canWrite"
      @confirm="confirmRevert"
    />

    <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" :job-id="runner.jobId.value" />
    <JobProgress
      v-if="runner.progress.value"
      :event="runner.progress.value"
      :messages="runner.messages.value"
      :started-at-ms="runner.startedAtMs.value"
    />

    <AppAlert v-if="statusQuery.isError.value" tone="danger">
      The firewall status couldn't be loaded. The node agent may be unreachable.
    </AppAlert>

    <EmptyState
      v-else-if="!statusQuery.isPending.value && !installed"
      icon="shield"
      title="UFW is not installed"
      description="Install ufw on the node (it ships with the panel installer). Once present, you can enable the firewall and manage ports here."
    />

    <template v-else>
      <AppCard class="space-y-4">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <p class="text-sm font-medium text-ink">Firewall is {{ active ? 'active' : 'inactive' }}</p>
            <p class="text-xs text-ink-muted">
              {{
                active
                  ? 'Incoming traffic is filtered against the rules below.'
                  : 'All incoming traffic is currently allowed. Enable the firewall to enforce the rules below.'
              }}
            </p>
          </div>
          <AppButton
            :variant="active ? 'danger' : 'primary'"
            :icon="active ? 'power' : 'shield'"
            :disabled="!canWrite || runner.busy.value"
            :loading="pending === 'toggle'"
            @click="toggleFirewall"
          >
            {{ active ? 'Disable firewall' : 'Enable firewall' }}
          </AppButton>
        </div>
        <AppAlert v-if="!active" tone="info">
          When you enable the firewall, SSH (port 22 plus any custom sshd ports) and the panel's ports 80 and 443 are
          allowed first, so the connection you are using now stays open.
        </AppAlert>
      </AppCard>

      <AppCard v-if="canWrite" class="space-y-4">
        <p class="text-sm font-medium text-ink">Add a rule</p>
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-5">
          <FormField label="Port or range">
            <AppInput v-model="form.port" placeholder="e.g. 8888 or 8000:8100" />
          </FormField>
          <FormField label="Protocol">
            <Select v-model="form.protocol">
              <SelectTrigger />
              <SelectContent>
                <SelectItem v-for="option in protocolOptions" :key="option.value" :value="option.value">{{
                  option.label
                }}</SelectItem>
              </SelectContent>
            </Select>
          </FormField>
          <FormField label="Action">
            <Select v-model="form.action">
              <SelectTrigger />
              <SelectContent>
                <SelectItem v-for="option in actionOptions" :key="option.value" :value="option.value">{{
                  option.label
                }}</SelectItem>
              </SelectContent>
            </Select>
          </FormField>
          <FormField label="Source (optional)" hint="IP or CIDR; leave blank for anywhere">
            <AppInput v-model="form.from" placeholder="e.g. 203.0.113.0/24" />
          </FormField>
          <FormField label="Comment (optional)">
            <AppInput v-model="form.comment" placeholder="e.g. office VPN" />
          </FormField>
        </div>
        <AppAlert v-if="formError" tone="danger">{{ formError }}</AppAlert>
        <div class="flex justify-end">
          <AppButton
            variant="primary"
            icon="plus"
            :disabled="runner.busy.value"
            :loading="pending === 'add'"
            @click="submit"
          >
            Add rule
          </AppButton>
        </div>
      </AppCard>

      <div class="overflow-hidden rounded-xl border border-outline">
        <table class="w-full text-sm">
          <thead class="bg-raised/50 text-left text-[11px] font-semibold tracking-[0.1em] text-ink-muted uppercase">
            <tr>
              <th class="px-4 py-3">Port</th>
              <th class="px-4 py-3">Protocol</th>
              <th class="px-4 py-3">Action</th>
              <th class="px-4 py-3">Source</th>
              <th class="px-4 py-3">Family</th>
              <th class="w-20 px-4 py-3 text-right">Remove</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-outline">
            <template v-if="statusQuery.isPending.value">
              <tr v-for="n in 5" :key="n">
                <td colspan="6" class="px-4 py-2"><SkeletonRow /></td>
              </tr>
            </template>
            <tr v-else-if="!rules.length">
              <td colspan="6" class="px-4 py-10 text-center text-ink-muted">
                No firewall rules yet. Add one above to open a port.
              </td>
            </tr>
            <tr v-for="rule in rules" v-else :key="ruleKey(rule)" class="hover:bg-raised/30">
              <td class="px-4 py-3 font-mono text-ink">{{ rule.port || 'Any' }}</td>
              <td class="px-4 py-3 text-ink-secondary uppercase">{{ rule.protocol || 'tcp+udp' }}</td>
              <td class="px-4 py-3">
                <StatusPill
                  :tone="rule.action === 'allow' ? 'success' : 'danger'"
                  :label="rule.action"
                  :pulse="false"
                />
              </td>
              <td class="px-4 py-3 font-mono text-ink-secondary">{{ rule.from || 'Anywhere' }}</td>
              <td class="px-4 py-3 text-ink-muted">{{ rule.v6 ? 'IPv6' : 'IPv4' }}</td>
              <td class="px-4 py-3 text-right">
                <AppButton
                  size="sm"
                  variant="ghost"
                  icon="trash"
                  title="Remove rule"
                  :disabled="!canWrite || runner.busy.value"
                  :loading="pending === ruleKey(rule)"
                  @click="requestRemove(rule)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <AppConfirmDialog
      :open="Boolean(ruleToRemove)"
      :title="`Remove access rule for ${ruleToRemove ? describeTarget(ruleToRemove) : ''}?`"
      confirm-label="Remove access rule"
      :type-to-confirm="ruleToRemove ? describeTarget(ruleToRemove) : undefined"
      @confirm="confirmRemoveRule"
      @close="ruleToRemove = undefined"
    >
      Removing an allow rule can close the panel or SSH path you are using. Keep an independent server session open and
      type the port/protocol target to confirm.
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="Boolean(lockoutRisk)"
      :title="`This will cut off access to ${lockoutRisk ? describeTarget(lockoutRisk.rule) : ''}`"
      confirm-label="Remove it anyway, with a rollback"
      :type-to-confirm="lockoutRisk ? describeTarget(lockoutRisk.rule) : undefined"
      @confirm="acceptLockoutRisk"
      @close="lockoutRisk = undefined"
    >
      {{ lockoutRisk?.message }} If you continue, the rule is removed and then restored automatically unless you
      confirm from a working session within the rollback window shown at the top of this page.
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="confirmDisable"
      title="Disable the firewall?"
      confirm-label="Disable firewall"
      @confirm="confirmDisableFirewall"
      @close="confirmDisable = false"
    >
      Disabling the firewall stops filtering all incoming traffic — every port on this node becomes reachable. Your rules
      are kept and re-applied when you enable it again.
    </AppConfirmDialog>
  </section>
</template>
