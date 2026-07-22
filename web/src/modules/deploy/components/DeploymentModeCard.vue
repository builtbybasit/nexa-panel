<script setup lang="ts">
import { computed, ref } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import type { SiteDeploymentMode } from '@/modules/sites/api'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  type Fact,
  FactList,
  JobFailureNotice,
  JobProgress,
  StatusPill,
} from '@/shared/ui'

import { setDeploymentMode } from '../api'

const props = defineProps<{ siteId: string; primaryDomain: string; rootPath: string; mode: SiteDeploymentMode }>()
const emit = defineEmits<{ changed: [] }>()

const identity = useIdentityStore()
const canWrite = computed(() => identity.can('deploy.write'))

const deployer = computed(() => props.mode === 'deployer')
const target = computed<SiteDeploymentMode>(() => (deployer.value ? 'standard' : 'deployer'))
const confirmOpen = ref(false)

// The paths the two modes serve. They are the renderer's, restated so the page
// says exactly what the switch will do to the vhost.
const facts = computed<Fact[]>(() => [
  { label: 'Document root', value: deployer.value ? `${props.rootPath}/app/current/public` : `${props.rootPath}/public` },
  { label: 'Deploy path', value: deployer.value ? `${props.rootPath}/app` : '—' },
  { label: 'Shared path', value: deployer.value ? `${props.rootPath}/app/shared` : '—' },
])

const runner = useJobRunner()

// The switch re-renders and re-applies the whole virtual host through the
// site's own settings job, so a node that refuses it rolls back on its own and
// the site keeps serving what it served before.
async function confirmSwitch() {
  confirmOpen.value = false
  if (!canWrite.value || runner.busy.value) return
  await runner.run(
    async () => {
      const change = await setDeploymentMode(props.siteId, target.value)
      emit('changed')
      return change.job?.id
    },
    {
      failureMessage: 'The deployment mode could not be applied.',
      successToast: 'Deployment mode applied',
      onSuccess: () => emit('changed'),
    },
  )
}
</script>

<template>
  <AppCard eyebrow="Manage" title="Deployment mode">
    <template #actions>
      <StatusPill :tone="deployer ? 'accent' : 'info'" :label="deployer ? 'Deployer' : 'Standard'" :pulse="false" />
    </template>

    <div class="space-y-4">
      <p class="text-[13px] leading-relaxed text-ink-secondary">
        In standard mode the panel owns the document root and you upload into <code class="font-mono">public/</code>. In
        deployer mode a deploy tool writes numbered releases under
        <code class="font-mono">app/releases/</code> and flips <code class="font-mono">app/current</code> at the end of a
        deploy — the panel points Nginx and PHP-FPM at that link and never touches a release itself.
      </p>

      <FactList :facts="facts" />

      <AppAlert v-if="deployer" tone="info">
        The panel seeds an empty first release so the site answers before the first deploy. Files you already had in
        <code class="font-mono">public/</code> are left where they are — deployer mode stops serving them, it does not
        delete them.
      </AppAlert>

      <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" :job-id="runner.jobId.value" />
      <JobProgress
        v-if="runner.progress.value"
        :event="runner.progress.value"
        :messages="runner.messages.value"
        :started-at-ms="runner.startedAtMs.value"
      />

      <div v-if="canWrite" class="border-t border-outline pt-4">
        <AppButton
          variant="primary"
          icon="rocket"
          :disabled="runner.busy.value"
          :loading="runner.busy.value"
          @click="confirmOpen = true"
        >
          Switch to {{ target === 'deployer' ? 'deployer' : 'standard' }} mode
        </AppButton>
      </div>
      <AppAlert v-else tone="info">An administrator is required to change this site's deployment mode.</AppAlert>

      <AppConfirmDialog
        :open="confirmOpen"
        :title="`Switch to ${target} mode`"
        :confirm-label="`Switch to ${target}`"
        :type-to-confirm="primaryDomain"
        :busy="runner.busy.value"
        @confirm="confirmSwitch"
        @close="confirmOpen = false"
      >
        <template v-if="target === 'deployer'">
          The virtual host is re-rendered to serve <code class="font-mono">app/current/public</code> and re-applied
          immediately. Until your first deploy lands, the site serves the placeholder release the panel seeds.
        </template>
        <template v-else>
          The virtual host goes back to serving <code class="font-mono">public/</code>. The release tree stays on disk,
          but nothing under it is served and the next deploy will not change what visitors see.
        </template>
      </AppConfirmDialog>
    </div>
  </AppCard>
</template>
