<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { getJob } from '@/modules/jobs/api'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { useToasts } from '@/shared/composables/useToasts'
import { formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppIcon,
  AppInput,
  type Fact,
  FactList,
  FormField,
  JobFailureNotice,
  JobProgress,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'
import CopyField from '@/shared/ui/CopyField.vue'

import { ensureDeployKey, getDeployKey, testDeployKey, type GitHubTestResult } from '../api'

const props = defineProps<{ siteId: string; primaryDomain: string }>()

const identity = useIdentityStore()
const { push } = useToasts()
const canWrite = computed(() => identity.can('deploy.write'))

const keyQuery = useQuery({
  queryKey: ['deploy-key', () => props.siteId],
  queryFn: () => getDeployKey(props.siteId),
  enabled: computed(() => !!props.siteId),
  retry: false,
})
const key = computed(() => keyQuery.data.value)

const repository = ref('')
const repositoryError = ref('')
const busy = ref('')
const actionError = ref('')
const rotateOpen = ref(false)
const testResult = ref<GitHubTestResult>()

// The stored remote seeds the field, so a returning visitor sees what the last
// test was run against rather than an empty box.
watch(key, (current) => {
  if (current && !repository.value) repository.value = current.repository
})

// The same allowlist the operator enforces, restated so a malformed remote is
// refused in the field that typed it. An https:// remote is rejected for the
// reason the node rejects it: it would not exercise the deploy key at all.
const REPOSITORY_RE = /^git@github\.com:[A-Za-z0-9._-]{1,64}\/[A-Za-z0-9._-]{1,100}(\.git)?$/

// Where the public half is registered. Without a repository the panel cannot
// know which repo's key page to open, so it falls back to the account's own.
const registerUrl = computed(() => {
  const match = /^git@github\.com:([^/]+)\/(.+?)(\.git)?$/.exec(key.value?.repository ?? '')
  if (!match) return 'https://github.com/settings/keys'
  return `https://github.com/${match[1]}/${match[2]}/settings/keys/new`
})

const keyFacts = computed<Fact[]>(() => {
  const current = key.value
  if (!current?.present) return []
  const facts: Fact[] = [
    { label: 'Algorithm', value: current.algorithm ?? '' },
    { label: 'Fingerprint', value: current.fingerprint ?? '' },
    { label: 'Version', value: String(current.keyVersion) },
  ]
  if (current.createdAt) facts.push({ label: 'Created', value: formatDateTime(current.createdAt) })
  return facts
})

const lastTest = computed(() => {
  const current = key.value
  if (!current?.lastTestedAt) return ''
  const verdict = current.lastTestOk ? 'succeeded' : 'failed'
  return `Last tested ${formatDateTime(current.lastTestedAt)} — ${verdict}.`
})

function validate(): string {
  const remote = repository.value.trim()
  if (!remote) {
    repositoryError.value = 'Name the repository, for example git@github.com:owner/name.git.'
    return ''
  }
  if (!REPOSITORY_RE.test(remote)) {
    repositoryError.value = 'Use the SSH remote, for example git@github.com:owner/name.git.'
    return ''
  }
  repositoryError.value = ''
  return remote
}

watch(repository, () => {
  if (repositoryError.value) repositoryError.value = ''
})

async function run(action: string, work: () => Promise<unknown>, failure: string) {
  if (!canWrite.value || busy.value) return
  busy.value = action
  actionError.value = ''
  try {
    await work()
    await keyQuery.refetch()
  } catch (caught) {
    actionError.value = caught instanceof Error ? caught.message : failure
  } finally {
    busy.value = ''
  }
}

function generate() {
  const remote = validate()
  if (!remote) return
  return run(
    'generate',
    async () => {
      await ensureDeployKey(props.siteId, remote)
      push({ tone: 'success', title: 'Deploy key ready — register it on GitHub' })
    },
    'The deploy key could not be created.',
  )
}

function rotate() {
  rotateOpen.value = false
  const remote = repository.value.trim()
  return run(
    'rotate',
    async () => {
      testResult.value = undefined
      await ensureDeployKey(props.siteId, REPOSITORY_RE.test(remote) ? remote : '', true)
      push({ tone: 'success', title: 'Deploy key rotated — register the new one on GitHub' })
    },
    'The deploy key could not be rotated.',
  )
}

const testRunner = useJobRunner()

// The verdict lives in the job result, not in the progress stream: the tail is
// captured on the node and only reaches the panel when the run settles.
async function test() {
  const remote = validate()
  if (!remote || testRunner.busy.value) return
  testResult.value = undefined
  actionError.value = ''
  await testRunner.run(async () => (await testDeployKey(props.siteId, remote)).job.id, {
    onSuccess: async (event) => {
      const raw = ((await getJob(event.jobId)).result ?? {}) as Partial<GitHubTestResult>
      testResult.value = {
        authOk: Boolean(raw.authOk),
        account: typeof raw.account === 'string' ? raw.account : '',
        lsRemoteOk: Boolean(raw.lsRemoteOk),
        refCount: Number(raw.refCount ?? 0),
        outputTail: typeof raw.outputTail === 'string' ? raw.outputTail : '',
      }
      await keyQuery.refetch()
    },
    failureMessage: 'The GitHub access test could not be run.',
  })
}
</script>

<template>
  <AppCard eyebrow="Manage" title="GitHub access">
    <template #actions>
      <StatusPill
        v-if="key"
        :tone="key.present ? 'accent' : 'info'"
        :label="key.present ? 'Key ready' : 'No key'"
        :pulse="false"
      />
    </template>

    <div class="space-y-4">
      <p class="text-[13px] leading-relaxed text-ink-secondary">
        A deploy key lets this site pull from one private GitHub repository and nothing else. The pair is generated on
        the node, the private half never leaves it, and only the public half below is ever shown here.
      </p>

      <AppAlert v-if="actionError" tone="danger">{{ actionError }}</AppAlert>

      <div v-if="keyQuery.isPending.value" class="space-y-1" aria-hidden="true">
        <SkeletonRow v-for="n in 2" :key="n" />
      </div>
      <AppAlert v-else-if="keyQuery.isError.value" tone="danger">
        <p>The deploy key could not be loaded.</p>
        <AppButton size="sm" class="mt-2" @click="keyQuery.refetch()">Retry</AppButton>
      </AppAlert>

      <template v-else-if="key">
        <FormField
          label="Repository"
          hint="The SSH remote this site deploys from."
          :error="repositoryError"
        >
          <AppInput v-model="repository" placeholder="git@github.com:owner/name.git" spellcheck="false" />
        </FormField>

        <template v-if="key.present">
          <FactList :facts="keyFacts" />
          <CopyField
            label="Public key"
            :value="key.publicKey ?? ''"
            hint="Add this as a deploy key on the repository. Read-only access is enough to deploy."
          />
          <div class="flex flex-wrap items-center gap-3">
            <a
              :href="registerUrl"
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-1.5 text-[13px] font-medium text-accent-300 underline-offset-2 hover:text-accent-200 hover:underline"
            >
              Add it on GitHub
              <AppIcon name="external-link" :size="14" />
            </a>
            <span v-if="lastTest" class="text-[12px] text-ink-muted">{{ lastTest }}</span>
          </div>
        </template>
        <AppAlert v-else tone="info">
          No deploy key yet. Name the repository and generate one — the public half appears here for you to register on
          GitHub.
        </AppAlert>

        <JobFailureNotice
          v-if="testRunner.error.value"
          :message="testRunner.error.value"
          :job-id="testRunner.jobId.value"
        />
        <JobProgress
          v-if="testRunner.progress.value"
          :event="testRunner.progress.value"
          :messages="testRunner.messages.value"
          :started-at-ms="testRunner.startedAtMs.value"
        />

        <div v-if="testResult" class="space-y-3 rounded-xl border border-outline p-4">
          <div class="flex flex-wrap items-center gap-3">
            <StatusPill
              :tone="testResult.authOk && testResult.lsRemoteOk ? 'accent' : 'danger'"
              :label="testResult.authOk && testResult.lsRemoteOk ? 'Access verified' : 'Access refused'"
              :pulse="false"
            />
            <span class="text-[13px] text-ink-secondary">
              <template v-if="testResult.authOk">
                GitHub greeted <code class="font-mono text-accent-200">{{ testResult.account || 'the key' }}</code
                >.
                <template v-if="testResult.lsRemoteOk">
                  The repository has {{ testResult.refCount }} branch<template v-if="testResult.refCount !== 1"
                    >es</template
                  >.
                </template>
                <template v-else>The key is trusted, but the repository could not be read.</template>
              </template>
              <template v-else>GitHub did not accept the deploy key.</template>
            </span>
          </div>
          <pre
            v-if="testResult.outputTail"
            class="max-h-64 overflow-auto rounded-xl border border-outline bg-canvas/60 p-3 font-mono text-xs leading-5 whitespace-pre-wrap text-ink-secondary"
            >{{ testResult.outputTail }}</pre
          >
          <AppButton size="sm" @click="testResult = undefined">Dismiss</AppButton>
        </div>

        <template v-if="canWrite">
          <div class="flex flex-wrap gap-3 border-t border-outline pt-4">
            <AppButton
              v-if="!key.present"
              variant="primary"
              icon="key-round"
              :disabled="!!busy"
              :loading="busy === 'generate'"
              @click="generate"
            >
              Generate a deploy key
            </AppButton>
            <AppButton
              v-else
              variant="primary"
              icon="plug-zap"
              :disabled="!!busy || testRunner.busy.value"
              :loading="testRunner.busy.value"
              @click="test"
            >
              Test connection
            </AppButton>
            <AppButton
              v-if="key.present"
              variant="ghost"
              icon="rotate-cw"
              :disabled="!!busy || testRunner.busy.value"
              :loading="busy === 'rotate'"
              @click="rotateOpen = true"
            >
              Rotate key
            </AppButton>
          </div>
        </template>
        <AppAlert v-else tone="info">An administrator is required to change this site's deploy key.</AppAlert>
      </template>

      <AppConfirmDialog
        :open="rotateOpen"
        title="Rotate the deploy key"
        confirm-label="Rotate key"
        :type-to-confirm="primaryDomain"
        :busy="busy === 'rotate'"
        @confirm="rotate"
        @close="rotateOpen = false"
      >
        A new pair replaces the current one on the node. The key GitHub already trusts stops working immediately, and
        deployments fail until the new public half is registered on the repository.
      </AppConfirmDialog>
    </div>
  </AppCard>
</template>
