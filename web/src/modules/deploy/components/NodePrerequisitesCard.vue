<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { getJob } from '@/modules/jobs/api'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { AppAlert, AppButton, AppCard, AppIcon, JobFailureNotice, JobProgress, StatusPill } from '@/shared/ui'

import { type DeployTool, type NodePreparation, prepareNode } from '../api'

const props = defineProps<{ siteId: string }>()

const identity = useIdentityStore()
const canWrite = computed(() => identity.can('deploy.write'))

// There is no read endpoint for this: the node's tooling is only observed while
// a prepare runs, so the card starts empty and fills in from the job result.
const preparation = ref<NodePreparation>()

const runner = useJobRunner()

// The checklist lives in the job result rather than the progress stream: what an
// operator wants is the settled table of tools, not the line that installed one.
async function prepare() {
  if (!canWrite.value || runner.busy.value) return
  preparation.value = undefined
  await runner.run(async () => (await prepareNode(props.siteId)).job.id, {
    onSuccess: async (event) => {
      const raw = ((await getJob(event.jobId)).result ?? {}) as Partial<NodePreparation>
      preparation.value = {
        siteId: typeof raw.siteId === 'string' ? raw.siteId : props.siteId,
        ready: Boolean(raw.ready),
        tools: Array.isArray(raw.tools) ? raw.tools : [],
        firewallSsh: {
          checked: Boolean(raw.firewallSsh?.checked),
          installed: Boolean(raw.firewallSsh?.installed),
          active: Boolean(raw.firewallSsh?.active),
          open: Boolean(raw.firewallSsh?.open),
          remediation: raw.firewallSsh?.remediation,
        },
        warnings: Array.isArray(raw.warnings) ? raw.warnings : [],
      }
    },
    failureMessage: 'The node could not be prepared for deployments.',
  })
}

// A tool is either already there, was installed by this run, or could not be
// installed — the operator's three actions, restated for the row.
function toneFor(tool: DeployTool) {
  if (!tool.installed) return 'danger'
  return tool.action === 'installed' ? 'accent' : 'success'
}

function labelFor(tool: DeployTool) {
  if (!tool.installed) return 'Missing'
  return tool.action === 'installed' ? 'Installed now' : 'Present'
}

// Only a firewall that was read and reports 22 blocked earns the amber alert. An
// unchecked firewall is reported through the warning list instead, so a build
// without a firewall client never accuses a node of blocking SSH.
const sshBlocked = computed(() => {
  const firewall = preparation.value?.firewallSsh
  return !!firewall?.checked && !firewall.open
})

// The closed-port warning is also in the warning list, where it would read twice
// under the alert that already states it. Everything else the run reported —
// a tool it could not install, a firewall it could not read — is shown as sent.
const warnings = computed(() => {
  const current = preparation.value
  if (!current) return []
  const remediation = current.firewallSsh.remediation
  if (!sshBlocked.value || !remediation) return current.warnings
  return current.warnings.filter((warning) => !warning.includes(remediation))
})
</script>

<template>
  <AppCard eyebrow="Manage" title="Server prerequisites">
    <template #actions>
      <StatusPill
        v-if="preparation"
        :tone="preparation.ready ? 'success' : 'warning'"
        :label="preparation.ready ? 'Node ready' : 'Tooling missing'"
        :pulse="false"
      />
    </template>

    <div class="space-y-4">
      <p class="text-[13px] leading-relaxed text-ink-secondary">
        A deploy runs Git, Composer and rsync on the node as this site's own account. This check looks for each of them,
        installs whatever is missing, and confirms that an SSH client can still reach the node. It never opens a
        firewall port — if port 22 is closed it tells you, and you allow it yourself.
      </p>

      <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" :job-id="runner.jobId.value" />
      <JobProgress
        v-if="runner.progress.value"
        :event="runner.progress.value"
        :messages="runner.messages.value"
        :started-at-ms="runner.startedAtMs.value"
      />

      <template v-if="preparation">
        <ul class="divide-y divide-outline rounded-xl border border-outline">
          <li
            v-for="tool in preparation.tools"
            :key="tool.name"
            class="flex flex-wrap items-center justify-between gap-3 px-4 py-3"
          >
            <div class="min-w-0">
              <p class="font-mono text-[13px] text-ink">{{ tool.name }}</p>
              <p v-if="tool.version" class="truncate text-[12px] text-ink-muted">{{ tool.version }}</p>
              <p v-else-if="tool.path" class="truncate font-mono text-[12px] text-ink-muted">{{ tool.path }}</p>
            </div>
            <StatusPill :tone="toneFor(tool)" :label="labelFor(tool)" :pulse="false" />
          </li>
        </ul>

        <AppAlert v-if="sshBlocked" tone="warning">
          <p>
            The node's firewall does not allow port 22, so a deployment over SSH cannot reach it.
            <template v-if="preparation.firewallSsh.remediation">
              Allow it below, or run <code class="font-mono">{{ preparation.firewallSsh.remediation }}</code> on the
              node.
            </template>
          </p>
          <RouterLink
            to="/firewall"
            class="mt-2 inline-flex items-center gap-1.5 text-[13px] font-medium text-accent-300 underline-offset-2 hover:text-accent-200 hover:underline"
          >
            Open the firewall
            <AppIcon name="arrow-right" :size="14" />
          </RouterLink>
        </AppAlert>

        <AppAlert v-for="warning in warnings" :key="warning" tone="info">{{ warning }}</AppAlert>
      </template>

      <div v-if="canWrite" class="border-t border-outline pt-4">
        <AppButton
          variant="primary"
          icon="server"
          :disabled="runner.busy.value"
          :loading="runner.busy.value"
          @click="prepare"
        >
          Prepare this node
        </AppButton>
      </div>
      <AppAlert v-else tone="info">An administrator is required to install deployment tooling on this node.</AppAlert>
    </div>
  </AppCard>
</template>
