<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'

import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppIcon,
  AppInput,
  AppSelect,
  FormField,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  StatusPill,
} from '@/shared/ui'

import { createSite, listRuntimes, listSites, type Runtime, type Site } from '../api'

const router = useRouter()
const runner = useJobRunner()

// ── Data ──────────────────────────────────────────────────────────────────
const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const runtimesQuery = useQuery({ queryKey: ['runtimes'], queryFn: listRuntimes, retry: false })
const sites = computed<Site[]>(() => sitesQuery.data.value ?? [])
const runtimes = computed<Runtime[]>(() => runtimesQuery.data.value ?? [])

// ── Wizard state ─────────────────────────────────────────────────────────
type StepKey = 'template' | 'domain' | 'configuration' | 'done'
const steps: { key: StepKey; label: string }[] = [
  { key: 'template', label: 'Template' },
  { key: 'domain', label: 'Domain' },
  { key: 'configuration', label: 'Configuration' },
  { key: 'done', label: 'Done' },
]
const step = ref(0)

const selectedTemplate = ref('')
const displayName = ref('')
const primaryDomain = ref('')
const phpVersion = ref('')
const nameError = ref('')
const domainError = ref('')
const createdSite = ref<Site>()

// ── Templates ──────────────────────────────────────────────────────────────
interface Template {
  id: string
  label: string
  icon: string
  desc: string
  ready?: boolean
  soon?: boolean
}
const templates: Template[] = [
  { id: 'php', label: 'PHP', icon: 'file-code-2', desc: 'Managed PHP-FPM site behind Nginx', ready: true },
  { id: 'static', label: 'Static site', icon: 'panels-top-left', desc: 'Plain HTML, CSS and JavaScript', soon: true },
  { id: 'wordpress', label: 'WordPress', icon: 'globe', desc: 'WordPress CMS, installed for you', soon: true },
  { id: 'nodejs', label: 'Node.js', icon: 'hexagon', desc: 'Run a Node.js application', soon: true },
  { id: 'reverse-proxy', label: 'Reverse proxy', icon: 'network', desc: 'Proxy requests to an upstream', soon: true },
]

// ── Slug derivation (silent; kept unique against existing sites) ────────────
function slugify(v: string): string {
  return v
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 32)
}
function deriveSlug(name: string, domain: string, taken: Set<string>): string {
  let base = slugify(name) || slugify(domain) || 'site'
  if (!/^[a-z]/.test(base)) base = ('site-' + base).slice(0, 32)
  base = base.replace(/-+$/, '') || 'site'
  if (base.length < 2) base = base + '-1'
  let candidate = base
  let n = 2
  while (taken.has(candidate)) {
    const suffix = '-' + n++
    candidate = (base.slice(0, 32 - suffix.length).replace(/-+$/, '') || 'site') + suffix
  }
  return candidate
}

const taken = computed(() => new Set(sites.value.map((s) => s.slug)))
const derivedSlug = computed(() => deriveSlug(displayName.value, primaryDomain.value, taken.value))

// Backend-derived previews (never sent — the backend derives these from slug).
const unixUser = computed(() => 'nexa_' + derivedSlug.value.replaceAll('-', '_'))
const rootPath = computed(() => '/srv/nexa/sites/' + derivedSlug.value)
const socketPath = computed(() => '/run/php/nexa-' + derivedSlug.value + '.sock')

// ── Runtime handling ─────────────────────────────────────────────────────
const runtimesEmpty = computed(() => runtimesQuery.isSuccess.value && runtimes.value.length === 0)
const createDisabled = computed(() => runtimes.value.length === 0)

// Preselect the first installed runtime once the list arrives.
watch(
  runtimes,
  (list) => {
    const first = list[0]
    if (!phpVersion.value && first) phpVersion.value = first.version
  },
  { immediate: true },
)

// ── Validation ─────────────────────────────────────────────────────────────
// Each label 1–63 chars, alphanumeric boundaries, at least two labels.
const HOSTNAME_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$/

function validate(): boolean {
  let ok = true
  if (!displayName.value.trim()) {
    nameError.value = 'Enter a display name.'
    ok = false
  } else {
    nameError.value = ''
  }
  const domain = primaryDomain.value.trim().toLowerCase()
  if (!domain) {
    domainError.value = 'Enter a primary domain.'
    ok = false
  } else if (domain.length > 253 || !HOSTNAME_RE.test(domain)) {
    domainError.value = 'Enter a valid hostname, for example portal.example.com.'
    ok = false
  } else if (sites.value.some((s) => s.primaryDomain.toLowerCase() === domain)) {
    domainError.value = 'A site with this domain already exists.'
    ok = false
  } else {
    domainError.value = ''
  }
  return ok
}

// Reset field errors as the user edits, like the old dialog did.
watch(displayName, () => {
  if (nameError.value) nameError.value = ''
})
watch(primaryDomain, () => {
  if (domainError.value) domainError.value = ''
})

// ── Runner spreads (optional props bound conditionally) ─────────────────────
const failureLink = computed(() => (runner.jobId.value !== undefined ? { jobId: runner.jobId.value } : {}))
const progressExtras = computed(() => ({
  messages: runner.messages.value,
  ...(runner.startedAtMs.value !== undefined ? { startedAtMs: runner.startedAtMs.value } : {}),
}))

// ── Navigation ──────────────────────────────────────────────────────────────
function cancel() {
  void router.push('/sites')
}
function selectTemplate(t: Template) {
  if (!t.ready) return
  selectedTemplate.value = t.id
  step.value = 1
}
function continueFromDomain() {
  if (validate()) step.value = 2
}

async function submit() {
  if (runner.busy.value || !validate()) return
  const slug = deriveSlug(displayName.value, primaryDomain.value, taken.value)
  await runner.run(
    async () => {
      const r = await createSite({
        displayName: displayName.value.trim(),
        slug,
        primaryDomain: primaryDomain.value.trim(),
        phpVersion: phpVersion.value,
      })
      createdSite.value = r.site
      await sitesQuery.refetch()
      return r.job.id
    },
    {
      failureMessage: 'Site planning failed',
      onSuccess: async () => {
        // The planning job has finished — refresh so the success screen reflects
        // the site's final status (plan_ready) rather than the captured 'planning'.
        await sitesQuery.refetch()
        const updated = sitesQuery.data.value?.find((s) => s.id === createdSite.value?.id)
        if (updated) createdSite.value = updated
        step.value = 3
      },
    },
  )
}

function createAnother() {
  displayName.value = ''
  primaryDomain.value = ''
  phpVersion.value = runtimes.value[0]?.version ?? ''
  nameError.value = ''
  domainError.value = ''
  selectedTemplate.value = ''
  createdSite.value = undefined
  runner.reset()
  step.value = 0
}

// ── Stepper cosmetics ────────────────────────────────────────────────────
function circleClass(index: number): string {
  if (index < step.value) return 'border-accent-400/40 bg-accent-500/15 text-accent-200'
  if (index === step.value) return 'border-accent-400 bg-accent-500 text-accent-950'
  return 'border-outline bg-white/[0.02] text-ink-muted'
}
function labelClass(index: number): string {
  if (index === step.value) return 'text-ink'
  if (index < step.value) return 'text-ink-secondary'
  return 'text-ink-muted'
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Web hosting"
      title="Create site"
      :breadcrumbs="[{ label: 'Sites', to: '/sites' }, { label: 'New site' }]"
    >
      <AppButton variant="ghost" icon="arrow-left" @click="cancel">Cancel</AppButton>
    </PageHeader>

    <!-- Stepper -->
    <AppCard>
      <ol class="flex items-center justify-between gap-1 sm:gap-3">
        <li v-for="(s, i) in steps" :key="s.key" class="flex flex-1 items-center gap-2 last:flex-none sm:gap-3">
          <div class="flex items-center gap-2.5">
            <span
              class="grid size-8 shrink-0 place-items-center rounded-full border text-[13px] font-semibold transition-colors"
              :class="circleClass(i)"
            >
              <AppIcon v-if="i < step" name="check" :size="16" />
              <span v-else>{{ i + 1 }}</span>
            </span>
            <span class="hidden text-[13px] font-medium sm:inline" :class="labelClass(i)">{{ s.label }}</span>
          </div>
          <span
            v-if="i < steps.length - 1"
            class="h-px flex-1 rounded-full transition-colors sm:min-w-6"
            :class="i < step ? 'bg-accent-400/50' : 'bg-outline'"
            aria-hidden="true"
          />
        </li>
      </ol>
    </AppCard>

    <!-- STEP 0 — Template -->
    <div v-if="step === 0" class="mx-auto max-w-4xl space-y-4">
      <div>
        <p class="text-[11px] font-bold tracking-[0.12em] text-ink-muted uppercase">Choose template</p>
        <h2 class="mt-1 text-[15px] font-semibold text-ink">What kind of site are you creating?</h2>
        <p class="mt-1 text-[13px] text-ink-secondary">Pick a starting point. More templates are on the way.</p>
      </div>
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <component
          :is="t.ready ? 'button' : 'div'"
          v-for="t in templates"
          :key="t.id"
          :type="t.ready ? 'button' : undefined"
          :aria-disabled="t.ready ? undefined : 'true'"
          :tabindex="t.ready ? undefined : -1"
          class="group relative flex flex-col gap-3 rounded-2xl border border-outline bg-surface/80 p-5 text-left transition-colors"
          :class="
            t.ready
              ? 'cursor-pointer hover:border-accent-400/40 hover:bg-white/[0.04] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-400'
              : 'cursor-default opacity-55'
          "
          @click="selectTemplate(t)"
        >
          <span
            v-if="t.soon"
            class="absolute right-3 top-3 rounded-full border border-outline bg-white/[0.04] px-1.5 py-0.5 text-[9px] font-bold tracking-[0.08em] text-ink-muted uppercase"
          >
            Soon
          </span>
          <span v-else class="absolute right-3 top-3">
            <StatusPill label="Ready" tone="success" />
          </span>
          <span
            class="grid size-11 place-items-center rounded-xl border border-outline bg-white/[0.03] text-accent-300 transition-colors group-hover:border-accent-400/30 group-hover:text-accent-200"
          >
            <AppIcon :name="t.icon" :size="22" />
          </span>
          <span class="min-w-0">
            <span class="block text-[15px] font-semibold text-ink">{{ t.label }}</span>
            <span class="mt-1 block text-[13px] text-ink-secondary">{{ t.desc }}</span>
          </span>
        </component>
      </div>
    </div>

    <!-- STEP 1 — Domain -->
    <div v-else-if="step === 1" class="mx-auto max-w-3xl">
      <AppCard
        eyebrow="Domain"
        title="Name your site"
        description="This is how the site appears in the panel and which hostname it answers on."
      >
        <div class="space-y-4">
          <FormField label="Display name" :error="nameError">
            <AppInput
              v-model="displayName"
              :maxlength="80"
              placeholder="Customer portal"
              :invalid="!!nameError"
              required
            />
          </FormField>
          <FormField label="Primary domain" :error="domainError" hint="The main hostname visitors will use.">
            <AppInput
              v-model="primaryDomain"
              :maxlength="253"
              autocomplete="url"
              :spellcheck="false"
              placeholder="portal.example.com"
              :invalid="!!domainError"
              required
            />
          </FormField>
        </div>

        <div class="mt-6 flex items-center justify-between gap-3">
          <AppButton variant="secondary" icon="arrow-left" @click="step = 0">Previous step</AppButton>
          <AppButton variant="primary" icon="arrow-right" @click="continueFromDomain">Continue</AppButton>
        </div>
      </AppCard>
    </div>

    <!-- STEP 2 — Configuration -->
    <div v-else-if="step === 2" class="mx-auto max-w-3xl space-y-4">
      <div>
        <p class="text-[11px] font-bold tracking-[0.12em] text-ink-muted uppercase">Configuration</p>
        <h2 class="mt-1 text-[15px] font-semibold text-ink">Default settings have been set — they suit most cases.</h2>
        <p class="mt-1 text-[13px] text-ink-secondary">
          Review the defaults for <span class="font-medium text-ink">{{ primaryDomain }}</span
          >. You can fine-tune everything after the site is created.
        </p>
      </div>

      <AppAlert v-if="runtimesEmpty" tone="warning">
        No installed PHP runtime was found under <span class="font-mono text-xs">/etc/php</span>. Install a PHP version on
        this node before creating a site.
      </AppAlert>
      <AppAlert v-else-if="runtimesQuery.isError.value" tone="danger">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span>PHP runtimes could not be loaded.</span>
          <AppButton size="sm" @click="runtimesQuery.refetch()">Retry</AppButton>
        </div>
      </AppAlert>

      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <!-- User -->
        <div class="rounded-2xl border border-outline bg-surface/80 p-4">
          <div class="flex items-start justify-between gap-3">
            <div class="flex items-center gap-2.5">
              <span class="grid size-9 place-items-center rounded-xl border border-outline bg-white/[0.03] text-accent-300">
                <AppIcon name="user" :size="18" />
              </span>
              <span class="text-[13px] font-semibold text-ink">User</span>
            </div>
            <StatusPill label="Auto" tone="neutral" />
          </div>
          <dl class="mt-3 space-y-1.5 text-[12px]">
            <div class="flex items-center justify-between gap-2">
              <dt class="text-ink-muted">SSH</dt>
              <dd class="text-ink-secondary">Not granted</dd>
            </div>
            <div class="flex items-center justify-between gap-2">
              <dt class="text-ink-muted">User</dt>
              <dd class="truncate font-mono text-accent-200" :title="unixUser">{{ unixUser }}</dd>
            </div>
          </dl>
        </div>

        <!-- Backend (interactive) -->
        <div class="rounded-2xl border border-outline bg-surface/80 p-4 sm:col-span-2 lg:col-span-1">
          <div class="flex items-start justify-between gap-3">
            <div class="flex items-center gap-2.5">
              <span class="grid size-9 place-items-center rounded-xl border border-outline bg-white/[0.03] text-accent-300">
                <AppIcon name="file-code-2" :size="18" />
              </span>
              <span class="text-[13px] font-semibold text-ink">Backend</span>
            </div>
            <StatusPill label="PHP" tone="accent" />
          </div>
          <div class="mt-3">
            <FormField label="PHP version">
              <AppSelect v-model="phpVersion" empty-message="No PHP runtimes installed" required>
                <option v-for="runtime in runtimes" :key="runtime.version" :value="runtime.version">
                  PHP {{ runtime.version
                  }}{{ runtime.supportStatus === 'end_of_life_allowed' ? ' — no longer supported' : '' }}
                </option>
              </AppSelect>
            </FormField>
            <p class="mt-2 flex items-center justify-between gap-2 text-[12px]">
              <span class="text-ink-muted">Handler</span>
              <span class="text-ink-secondary">PHP-FPM + Nginx</span>
            </p>
          </div>
        </div>

        <!-- Root directory -->
        <div class="rounded-2xl border border-outline bg-surface/80 p-4">
          <div class="flex items-start justify-between gap-3">
            <div class="flex items-center gap-2.5">
              <span class="grid size-9 place-items-center rounded-xl border border-outline bg-white/[0.03] text-accent-300">
                <AppIcon name="folder" :size="18" />
              </span>
              <span class="text-[13px] font-semibold text-ink">Root directory</span>
            </div>
            <StatusPill label="Auto" tone="neutral" />
          </div>
          <dl class="mt-3 space-y-1.5 text-[12px]">
            <div class="flex items-center justify-between gap-2">
              <dt class="text-ink-muted">Path</dt>
              <dd class="truncate font-mono text-accent-200" :title="rootPath">{{ rootPath }}</dd>
            </div>
            <div class="flex items-center justify-between gap-2">
              <dt class="text-ink-muted">Socket</dt>
              <dd class="truncate font-mono text-accent-200" :title="socketPath">{{ socketPath }}</dd>
            </div>
          </dl>
        </div>

        <!-- Database -->
        <div class="rounded-2xl border border-outline bg-surface/80 p-4">
          <div class="flex items-start justify-between gap-3">
            <div class="flex items-center gap-2.5">
              <span class="grid size-9 place-items-center rounded-xl border border-outline bg-white/[0.03] text-accent-300">
                <AppIcon name="database" :size="18" />
              </span>
              <span class="text-[13px] font-semibold text-ink">Database</span>
            </div>
            <StatusPill label="Not configured" tone="neutral" />
          </div>
          <p class="mt-3 text-[12px] text-ink-muted">Add a database from the site later if your app needs one.</p>
        </div>

        <!-- Backup copies -->
        <div class="rounded-2xl border border-outline bg-surface/80 p-4">
          <div class="flex items-start justify-between gap-3">
            <div class="flex items-center gap-2.5">
              <span class="grid size-9 place-items-center rounded-xl border border-outline bg-white/[0.03] text-accent-300">
                <AppIcon name="archive" :size="18" />
              </span>
              <span class="text-[13px] font-semibold text-ink">Backup copies</span>
            </div>
            <StatusPill label="Soon" tone="neutral" />
          </div>
          <p class="mt-3 text-[12px] text-ink-muted">Scheduled backups arrive in a future release.</p>
        </div>

        <!-- FTP / SFTP -->
        <div class="rounded-2xl border border-outline bg-surface/80 p-4">
          <div class="flex items-start justify-between gap-3">
            <div class="flex items-center gap-2.5">
              <span class="grid size-9 place-items-center rounded-xl border border-outline bg-white/[0.03] text-accent-300">
                <AppIcon name="key-round" :size="18" />
              </span>
              <span class="text-[13px] font-semibold text-ink">FTP / SFTP</span>
            </div>
            <StatusPill label="Soon" tone="neutral" />
          </div>
          <p class="mt-3 text-[12px] text-ink-muted">Managed file-transfer credentials are on the roadmap.</p>
        </div>
      </div>

      <div class="flex items-center justify-between gap-3">
        <AppButton variant="secondary" icon="arrow-left" :disabled="runner.busy.value" @click="step = 1">
          Previous step
        </AppButton>
        <AppButton variant="primary" icon="check" :loading="runner.busy.value" :disabled="createDisabled" @click="submit">
          Create site
        </AppButton>
      </div>

      <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" v-bind="failureLink" />
      <JobProgress v-if="runner.progress.value" :event="runner.progress.value" v-bind="progressExtras" />
    </div>

    <!-- STEP 3 — Done -->
    <div v-else-if="step === 3 && createdSite" class="mx-auto max-w-4xl">
      <AppCard>
        <div class="grid gap-8 lg:grid-cols-[1.1fr_1fr] lg:items-center">
          <div>
            <span class="grid size-12 place-items-center rounded-2xl border border-emerald-400/25 bg-emerald-400/10 text-emerald-300">
              <AppIcon name="check" :size="24" />
            </span>
            <h2 class="mt-4 text-xl font-semibold tracking-tight text-ink sm:text-2xl">
              {{ createdSite.primaryDomain }} created!
            </h2>
            <p class="mt-2 max-w-md text-[13px] leading-relaxed text-ink-secondary">
              Your site is configured and a plan is ready. Review it and activate to go live — nothing changes on the
              server until you approve.
            </p>
            <div class="mt-3">
              <StatusPill :status="createdSite.status" />
            </div>

            <div class="mt-6 flex flex-wrap gap-2">
              <AppButton variant="primary" icon="arrow-right" @click="router.push('/sites/' + createdSite.id)">
                Review &amp; activate
              </AppButton>
              <AppButton variant="ghost" @click="createAnother">Create another site</AppButton>
            </div>

            <div class="mt-6 border-t border-outline pt-4">
              <p class="text-[11px] font-bold tracking-[0.12em] text-ink-muted uppercase">Shortcuts</p>
              <div class="mt-2 flex flex-wrap gap-2">
                <AppButton variant="secondary" size="sm" icon="folder" @click="router.push(`/files?site=${createdSite.id}`)">
                  Files
                </AppButton>
                <AppButton
                  variant="secondary"
                  size="sm"
                  icon="globe"
                  @click="router.push(`/domains?site=${createdSite.id}&create=1`)"
                >
                  Add domain
                </AppButton>
                <AppButton
                  variant="secondary"
                  size="sm"
                  icon="lock"
                  @click="router.push(`/certificates?site=${createdSite.id}&create=1`)"
                >
                  Enable HTTPS
                </AppButton>
              </div>
            </div>
          </div>

          <!-- Decorative browser mock -->
          <div class="hidden overflow-hidden rounded-2xl border border-outline bg-canvas/60 lg:block" aria-hidden="true">
            <div class="flex items-center gap-2 border-b border-outline bg-white/[0.02] px-3 py-2.5">
              <span class="size-2.5 rounded-full bg-white/10" />
              <span class="size-2.5 rounded-full bg-white/10" />
              <span class="size-2.5 rounded-full bg-white/10" />
              <div
                class="ml-2 flex flex-1 items-center gap-1.5 truncate rounded-md border border-outline bg-canvas/80 px-2.5 py-1 font-mono text-[11px] text-accent-200"
              >
                <AppIcon name="lock" :size="11" class="shrink-0 text-ink-muted" />
                {{ createdSite.primaryDomain }}
              </div>
            </div>
            <div class="space-y-3 p-5">
              <div class="h-2.5 w-2/3 rounded-full bg-white/[0.07]" />
              <div class="h-2 w-full rounded-full bg-white/[0.04]" />
              <div class="h-2 w-5/6 rounded-full bg-white/[0.04]" />
              <div class="mt-4 grid grid-cols-3 gap-2">
                <div class="h-12 rounded-lg border border-outline bg-white/[0.02]" />
                <div class="h-12 rounded-lg border border-outline bg-white/[0.02]" />
                <div class="h-12 rounded-lg border border-outline bg-white/[0.02]" />
              </div>
              <div class="mt-2 h-8 w-28 rounded-lg bg-accent-500/20" />
            </div>
          </div>
        </div>
      </AppCard>
    </div>
  </section>
</template>
