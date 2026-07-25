<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'

import { createDatabase, createUser, listDatabases, listServers, listUsers } from '@/modules/databases/api'
import { ENGINES, serverLabel, userLabel } from '@/modules/databases/lib/engines'
import { useIdentityStore } from '@/modules/identity/store'
import { stageSftpCredentials } from '@/modules/sftp/api'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { generatePassword } from '@/shared/lib/password'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppIcon,
  AppInput,
  FormField,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  PasswordField,
  Switch,
} from '@/shared/ui'
import {
  Combobox,
  ComboboxAnchor,
  ComboboxEmpty,
  ComboboxGroup,
  ComboboxInput,
  ComboboxItem,
  ComboboxItemIndicator,
  ComboboxList,
  ComboboxTrigger,
} from '@/shared/ui/combobox'

import { createSite, listRuntimes, listSites, type Runtime, type Site } from '../api'
import ConfigSection from '../create/ConfigSection.vue'
import { DB_NAME_RULE, deriveDbIdentifier, deriveSlug, sitePreviews, validateDomain } from '../create/lib'
import SiteCreateSuccess, { type CreatedDatabase, type StagedSftp } from '../create/SiteCreateSuccess.vue'
import TemplateCard from '../create/TemplateCard.vue'
import { SITE_TEMPLATES, type SiteTemplate } from '../create/templates'
import WizardStepper from '../create/WizardStepper.vue'

const DB_NAME_HINT = 'Lowercase letters, numbers, and underscores; starts with a letter.'
const DB_NAME_ERROR = 'Use lowercase letters, numbers, and underscores, starting with a letter (at least 2 characters).'

const router = useRouter()
const identity = useIdentityStore()
const canCreate = computed(() => identity.can('sites.write'))
const canDatabase = computed(() => identity.can('databases.write'))

// ── Data ──────────────────────────────────────────────────────────────────
const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const runtimesQuery = useQuery({ queryKey: ['runtimes'], queryFn: listRuntimes, retry: false })
// Same keys and fetchers as the databases module, so both share one cache.
const serversQuery = useQuery({ queryKey: ['database-servers'], queryFn: listServers, retry: false, enabled: canDatabase })
const dbUsersQuery = useQuery({ queryKey: ['database-users'], queryFn: listUsers, retry: false, enabled: canDatabase })
const databasesQuery = useQuery({ queryKey: ['managed-databases'], queryFn: listDatabases, retry: false, enabled: canDatabase })

const sites = computed<Site[]>(() => sitesQuery.data.value ?? [])
const runtimes = computed<Runtime[]>(() => runtimesQuery.data.value ?? [])
const activeServers = computed(() =>
  (serversQuery.data.value ?? []).filter((server) => server.status === 'active' || server.status === 'online'),
)

// ── Wizard state ─────────────────────────────────────────────────────────
const steps = ['Template', 'Configure', 'Done']
const step = ref(0)
const template = ref<SiteTemplate>()

function chooseTemplate(candidate: SiteTemplate) {
  if (!candidate.available) return
  template.value = candidate
  step.value = 1
}

// ── Site details ─────────────────────────────────────────────────────────
const displayName = ref('')
const primaryDomain = ref('')
const phpVersion = ref('')
const attempted = ref(false)

const takenSlugs = computed(() => new Set(sites.value.map((site) => site.slug)))
const derivedSlug = computed(() => deriveSlug(displayName.value, primaryDomain.value, takenSlugs.value))
const previews = computed(() => sitePreviews(derivedSlug.value))

const nameError = computed(() => (attempted.value && !displayName.value.trim() ? 'Enter a display name.' : ''))
// Live inline validation: silent until there is input (or a submit attempt).
const domainError = computed(() => {
  if (!attempted.value && !primaryDomain.value.trim()) return ''
  return validateDomain(primaryDomain.value, sites.value.map((site) => site.primaryDomain))
})

// ── Runtime ──────────────────────────────────────────────────────────────
const runtimesEmpty = computed(() => runtimesQuery.isSuccess.value && runtimes.value.length === 0)

// Preselect the first installed runtime once the list arrives.
watch(
  runtimes,
  (list) => {
    const first = list[0]
    if (!phpVersion.value && first) phpVersion.value = first.version
  },
  { immediate: true },
)

function runtimeLabel(version: string): string {
  const runtime = runtimes.value.find((candidate) => candidate.version === version)
  if (!runtime) return ''
  return `PHP ${runtime.version}${runtime.supportStatus === 'end_of_life_allowed' ? ' — no longer supported' : ''}`
}

// ── Database (optional, orchestrated after the site) ─────────────────────
/** Sentinel option in the owner picker, FastPanel-style. */
const CREATE_NEW_DB_USER = '__new__'

const dbEnabled = ref(false)
const dbOpen = ref(false)
const dbServerId = ref('')
const dbOwner = ref(CREATE_NEW_DB_USER)
const dbName = ref('')
const dbUserName = ref('')
const dbHost = ref('localhost')
// Pre-generated so the default path is zero-click; confirmation starts in
// agreement and only diverges once someone types a password by hand.
const dbPassword = ref(generatePassword(20))
const dbPasswordConfirm = ref(dbPassword.value)
// One-shot: default the section on the first time servers become known, then
// leave the choice alone — including across refetches.
const dbDefaulted = ref(false)
const dbNameTouched = ref(false)
const dbUserTouched = ref(false)

watch(
  activeServers,
  (list) => {
    const first = list[0]
    if (first && !list.some((server) => server.id === dbServerId.value)) dbServerId.value = first.id
    if (!dbDefaulted.value && first) {
      dbDefaulted.value = true
      dbEnabled.value = true
    }
  },
  { immediate: true },
)

function setDbEnabled(enabled: boolean) {
  dbEnabled.value = enabled
  dbDefaulted.value = true
  dbOpen.value = enabled
}

const selectedServer = computed(() => activeServers.value.find((server) => server.id === dbServerId.value))

/** Existing active users on the chosen server, offered as ready-made owners. */
const dbOwnerOptions = computed(() =>
  (dbUsersQuery.data.value ?? []).filter((user) => user.status === 'active' && user.serverId === dbServerId.value),
)
const creatingDbUser = computed(() => dbOwner.value === CREATE_NEW_DB_USER)
const selectedDbOwner = computed(() => dbOwnerOptions.value.find((user) => user.id === dbOwner.value))

// The chosen server decides which users are valid, so changing it resets an
// owner that no longer fits.
watch(dbServerId, () => {
  if (!creatingDbUser.value && !dbOwnerOptions.value.some((user) => user.id === dbOwner.value)) {
    dbOwner.value = CREATE_NEW_DB_USER
  }
})

function dbOwnerLabel(id: string): string {
  if (id === CREATE_NEW_DB_USER) return 'Create a new user'
  const user = dbOwnerOptions.value.find((candidate) => candidate.id === id)
  return user ? userLabel(user) : ''
}

const dbHostVisible = computed(
  () => creatingDbUser.value && !!selectedServer.value && ENGINES[selectedServer.value.engine].userHostScopes,
)

// Auto-derived identifiers follow the site name until the user edits them;
// clearing a field hands it back to the derivation.
const takenDbNames = computed(() => new Set((databasesQuery.data.value ?? []).map((database) => database.name)))
const takenDbUsers = computed(() => new Set((dbUsersQuery.data.value ?? []).map((user) => user.name)))
const autoDbName = computed(() => deriveDbIdentifier(derivedSlug.value, takenDbNames.value))
const autoDbUser = computed(() => deriveDbIdentifier(derivedSlug.value, takenDbUsers.value))
watch(autoDbName, (value) => { if (!dbNameTouched.value) dbName.value = value }, { immediate: true })
watch(autoDbUser, (value) => { if (!dbUserTouched.value) dbUserName.value = value }, { immediate: true })

function setDbName(value: string | number | undefined) {
  dbName.value = String(value ?? '')
  dbNameTouched.value = dbName.value.trim() !== ''
  if (!dbNameTouched.value) dbName.value = autoDbName.value
}

function setDbUserName(value: string | number | undefined) {
  dbUserName.value = String(value ?? '')
  dbUserTouched.value = dbUserName.value.trim() !== ''
  if (!dbUserTouched.value) dbUserName.value = autoDbUser.value
}

const dbActive = computed(() => dbEnabled.value && canDatabase.value && activeServers.value.length > 0)
const dbServerError = computed(() => (attempted.value && dbActive.value && !dbServerId.value ? 'Select a server.' : ''))
const dbNameError = computed(() =>
  dbActive.value && (attempted.value || dbNameTouched.value) && !DB_NAME_RULE.test(dbName.value) ? DB_NAME_ERROR : '',
)
const dbUserError = computed(() =>
  dbActive.value && creatingDbUser.value && (attempted.value || dbUserTouched.value) && !DB_NAME_RULE.test(dbUserName.value)
    ? DB_NAME_ERROR
    : '',
)
const dbHostError = computed(() =>
  attempted.value && dbActive.value && dbHostVisible.value && !dbHost.value.trim()
    ? 'Enter a host, or % to allow any host.'
    : '',
)
const dbPasswordError = computed(() =>
  dbActive.value && creatingDbUser.value && attempted.value && dbPassword.value.length < 8
    ? 'Use at least 8 characters — or generate one.'
    : '',
)
// Live mismatch feedback lives inside PasswordField; this only catches an
// empty confirmation at submit time.
const dbConfirmError = computed(() =>
  dbActive.value && creatingDbUser.value && attempted.value && !dbPasswordConfirm.value
    ? 'Re-enter the password to confirm it.'
    : '',
)
const dbPasswordsAgree = computed(() => dbPassword.value === dbPasswordConfirm.value)

const dbSummary = computed(() => {
  if (!canDatabase.value) return 'Your account cannot create databases.'
  if (activeServers.value.length === 0) return 'No database server is available on this node yet.'
  if (!dbEnabled.value) return 'Skipped — you can add one from the site later.'
  const server = selectedServer.value
  const where = server ? `${dbName.value} on ${serverLabel(server)}` : dbName.value
  const owner = selectedDbOwner.value
  return owner ? `${where} · owned by ${userLabel(owner)}` : where
})

// ── SFTP credentials (staged; applied when the site activates) ───────────
const sftpEnabled = ref(false)
const sftpOpen = ref(false)
const sftpPassword = ref('')
const sftpPasswordConfirm = ref('')

function setSftpEnabled(enabled: boolean) {
  sftpEnabled.value = enabled
  sftpOpen.value = enabled
  if (enabled && !sftpPassword.value) {
    sftpPassword.value = generatePassword(20)
    sftpPasswordConfirm.value = sftpPassword.value
  }
}

const sftpPasswordError = computed(() =>
  sftpEnabled.value && attempted.value && (sftpPassword.value.length < 12 || sftpPassword.value.length > 72)
    ? 'Use 12-72 characters — or generate one.'
    : '',
)
const sftpConfirmError = computed(() =>
  sftpEnabled.value && attempted.value && !sftpPasswordConfirm.value ? 'Re-enter the password to confirm it.' : '',
)
const sftpPasswordsAgree = computed(() => sftpPassword.value === sftpPasswordConfirm.value)

const sftpSummary = computed(() =>
  sftpEnabled.value
    ? `${previews.value.unixUser}@${primaryDomain.value.trim() || 'your domain'} — live once the site is activated`
    : 'Off — you can enable it from the site page any time.',
)

// ── Provisioning orchestration ───────────────────────────────────────────
// Three jobs at most (site plan, database user, database) plus one synchronous
// staging call for SFTP, run back-to-back and narrated as one checklist.
const siteRunner = useJobRunner()
const dbUserRunner = useJobRunner()
const dbRunner = useJobRunner()

type StageKey = 'site' | 'db-user' | 'database' | 'sftp'
const STAGE_ORDER: StageKey[] = ['site', 'db-user', 'database', 'sftp']
const stageRunners: Partial<Record<StageKey, ReturnType<typeof useJobRunner>>> = {
  site: siteRunner,
  'db-user': dbUserRunner,
  database: dbRunner,
}

const submitted = ref(false)
const stage = ref<StageKey>()
const createdSite = ref<Site>()
const createdDatabase = ref<CreatedDatabase>()
const databaseFailure = ref('')
const stagedSftp = ref<StagedSftp>()
const sftpFailure = ref('')

const stageList = computed<{ key: StageKey; label: string }[]>(() => {
  const items: { key: StageKey; label: string }[] = [
    { key: 'site', label: `Create ${primaryDomain.value.trim() || 'the site'} and plan its configuration` },
  ]
  if (dbActive.value) {
    if (creatingDbUser.value) items.push({ key: 'db-user', label: `Create database user ${dbUserName.value}` })
    items.push({ key: 'database', label: `Create database ${dbName.value}` })
  }
  if (sftpEnabled.value) items.push({ key: 'sftp', label: 'Stage SFTP credentials for activation' })
  return items
})

function stageState(key: StageKey): 'pending' | 'running' | 'done' | 'failed' {
  if (stageRunners[key]?.error.value) return 'failed'
  if (key === 'sftp' && sftpFailure.value) return 'failed'
  if (stage.value === key) return 'running'
  const current = stage.value ? STAGE_ORDER.indexOf(stage.value) : createdSite.value ? STAGE_ORDER.length : -1
  return STAGE_ORDER.indexOf(key) < current ? 'done' : 'pending'
}

const activeRunner = computed(() => (stage.value ? stageRunners[stage.value] : undefined))
const activeProgress = computed(() => activeRunner.value?.progress.value)
// The progress props come from whichever stage runner is currently active.
const progressExtras = computed(() => activeRunner.value?.progressProps.value ?? {})

/** Resolves once the runner is idle again — terminal job state, queue failure, or stream loss. */
function untilIdle(runner: ReturnType<typeof useJobRunner>): Promise<void> {
  if (!runner.busy.value) return Promise.resolve()
  return new Promise((resolve) => {
    const stopWatching = watch(runner.busy, (busy) => {
      if (!busy) {
        stopWatching()
        resolve()
      }
    })
  })
}

async function runStage(
  runner: ReturnType<typeof useJobRunner>,
  action: () => Promise<number | undefined>,
  failureMessage: string,
): Promise<boolean> {
  await runner.run(action, { failureMessage })
  await untilIdle(runner)
  return !runner.error.value
}

const createDisabled = computed(() => !canCreate.value || runtimes.value.length === 0)

function formValid(): boolean {
  if (nameError.value || domainError.value) return false
  const dbInvalid =
    dbServerError.value ||
    dbNameError.value ||
    dbUserError.value ||
    dbHostError.value ||
    dbPasswordError.value ||
    dbConfirmError.value ||
    (creatingDbUser.value && !dbPasswordsAgree.value)
  if (dbActive.value && dbInvalid) {
    dbOpen.value = true
    return false
  }
  if (sftpEnabled.value && (sftpPasswordError.value || sftpConfirmError.value || !sftpPasswordsAgree.value)) {
    sftpOpen.value = true
    return false
  }
  return true
}

async function submit() {
  if (!canCreate.value || submitted.value || createDisabled.value) return
  attempted.value = true
  if (!formValid()) return
  submitted.value = true

  stage.value = 'site'
  const ok = await runStage(
    siteRunner,
    async () => {
      const result = await createSite({
        displayName: displayName.value.trim(),
        slug: derivedSlug.value,
        primaryDomain: primaryDomain.value.trim().toLowerCase(),
        phpVersion: phpVersion.value,
      })
      createdSite.value = result.site
      return result.job.id
    },
    'Site planning failed',
  )
  if (!ok) {
    stage.value = undefined
    submitted.value = false
    return
  }
  // The planning job has finished — refresh so the success screen reflects
  // the site's final status (plan_ready) rather than the captured 'planning'.
  await sitesQuery.refetch()
  const updated = sites.value.find((site) => site.id === createdSite.value?.id)
  if (updated) createdSite.value = updated

  const server = selectedServer.value
  if (dbActive.value && server) {
    let ownerUserId = creatingDbUser.value ? '' : dbOwner.value
    let dbOk = true
    if (creatingDbUser.value) {
      stage.value = 'db-user'
      dbOk = await runStage(
        dbUserRunner,
        async () => {
          const result = await createUser({
            serverId: server.id,
            name: dbUserName.value,
            password: dbPassword.value,
            ...(ENGINES[server.engine].userHostScopes ? { host: dbHost.value } : {}),
          })
          ownerUserId = result.user.id
          return result.job.id
        },
        'Creating the database user failed',
      )
    }
    if (dbOk && ownerUserId) {
      stage.value = 'database'
      let databaseId = ''
      dbOk = await runStage(
        dbRunner,
        async () => {
          const result = await createDatabase({
            serverId: server.id,
            name: dbName.value,
            ownerUserId,
            ...(createdSite.value ? { siteId: createdSite.value.id } : {}),
          })
          databaseId = result.database.id
          return result.job.id
        },
        'Creating the database failed',
      )
      if (dbOk) {
        const owner = selectedDbOwner.value
        createdDatabase.value = {
          id: databaseId,
          name: dbName.value,
          username: owner ? owner.name : dbUserName.value,
          ...(creatingDbUser.value ? { password: dbPassword.value } : {}),
          serverName: serverLabel(server),
        }
      }
    }
    if (!dbOk) {
      databaseFailure.value =
        'The site was created, but its database could not be provisioned. The details are below — you can retry from the Databases page.'
    }
    await Promise.all([dbUsersQuery.refetch(), databasesQuery.refetch()])
  }

  // Staging is synchronous — no job to follow. Only a bcrypt hash of the
  // password leaves the browser; activation installs it and enables the jail.
  if (sftpEnabled.value && createdSite.value) {
    stage.value = 'sftp'
    try {
      const staged = await stageSftpCredentials(createdSite.value.id, sftpPassword.value)
      stagedSftp.value = {
        username: staged.username,
        host: staged.host,
        port: staged.port,
        password: sftpPassword.value,
      }
    } catch (caught) {
      sftpFailure.value =
        (caught instanceof Error ? caught.message + ' — ' : '') +
        'The site was created, but the SFTP credentials could not be staged. You can enable SFTP from the site page once it is active.'
    }
  }

  stage.value = undefined
  step.value = 2
}

function createAnother() {
  if (!canCreate.value) return
  displayName.value = ''
  primaryDomain.value = ''
  phpVersion.value = runtimes.value[0]?.version ?? ''
  attempted.value = false
  dbNameTouched.value = false
  dbUserTouched.value = false
  dbOwner.value = CREATE_NEW_DB_USER
  dbHost.value = 'localhost'
  dbPassword.value = generatePassword(20)
  dbPasswordConfirm.value = dbPassword.value
  dbOpen.value = false
  sftpEnabled.value = false
  sftpOpen.value = false
  sftpPassword.value = ''
  sftpPasswordConfirm.value = ''
  stagedSftp.value = undefined
  sftpFailure.value = ''
  createdSite.value = undefined
  createdDatabase.value = undefined
  databaseFailure.value = ''
  submitted.value = false
  siteRunner.reset()
  dbUserRunner.reset()
  dbRunner.reset()
  template.value = undefined
  step.value = 0
}

function cancel() {
  void router.push('/sites')
}

// exactOptionalPropertyTypes: only bind optional props when a value exists.
const successProps = computed(() => ({
  ...(createdDatabase.value ? { database: createdDatabase.value } : {}),
  ...(databaseFailure.value ? { databaseError: databaseFailure.value } : {}),
  ...(stagedSftp.value ? { sftp: stagedSftp.value } : {}),
  ...(sftpFailure.value ? { sftpError: sftpFailure.value } : {}),
}))
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

    <AppAlert v-if="!canCreate" tone="warning">
      Your account can view sites but does not have permission to create them.
    </AppAlert>

    <template v-else>
      <AppCard>
        <WizardStepper :steps="steps" :current="step" />
      </AppCard>

      <!-- STEP 0 — Template -->
      <div v-if="step === 0" class="mx-auto max-w-4xl space-y-5">
        <div class="text-center">
          <h2 class="text-lg font-semibold tracking-tight text-ink">Choose a template</h2>
          <p class="mx-auto mt-1 max-w-md text-[13px] leading-relaxed text-ink-secondary">
            Pick the stack your site runs on. Everything else comes with sensible defaults you can adjust before
            creating.
          </p>
        </div>
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <TemplateCard
            v-for="candidate in SITE_TEMPLATES"
            :key="candidate.id"
            :template="candidate"
            :selected="template?.id === candidate.id"
            @select="chooseTemplate(candidate)"
          />
        </div>
      </div>

      <!-- STEP 1 — Configure -->
      <div v-else-if="step === 1 && template" class="mx-auto max-w-3xl space-y-4">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2.5">
            <span class="grid size-9 place-items-center rounded-xl border border-outline bg-white/[0.03] text-accent-300">
              <AppIcon :name="template.icon" :size="17" />
            </span>
            <div>
              <p class="text-[13px] font-semibold text-ink">{{ template.name }} site</p>
              <p class="text-[11px] text-ink-muted">PHP-FPM + Nginx</p>
            </div>
          </div>
          <AppButton variant="ghost" size="sm" :disabled="submitted" @click="step = 0">Change template</AppButton>
        </div>

        <AppCard
          eyebrow="Site details"
          title="Name your site"
          description="How the site appears in the panel and which hostname it answers on."
        >
          <div class="space-y-4">
            <FormField label="Display name" :error="nameError">
              <AppInput
                v-model="displayName"
                :maxlength="80"
                placeholder="Customer portal"
                :invalid="!!nameError"
                :disabled="submitted"
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
                :disabled="submitted"
                required
              />
            </FormField>
          </div>
        </AppCard>

        <AppAlert v-if="runtimesEmpty" tone="warning">
          No installed PHP runtime was found under <span class="font-mono text-xs">/etc/php</span>. Install a PHP
          version on this node before creating a site.
        </AppAlert>
        <AppAlert v-else-if="runtimesQuery.isError.value" tone="danger">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span>PHP runtimes could not be loaded.</span>
            <AppButton size="sm" @click="runtimesQuery.refetch()">Retry</AppButton>
          </div>
        </AppAlert>

        <div>
          <p class="text-[11px] font-bold tracking-[0.12em] text-ink-muted uppercase">Configuration</p>
          <h2 class="mt-1 text-[15px] font-semibold text-ink">Defaults are set — they suit most cases.</h2>
          <p class="mt-1 text-[13px] text-ink-secondary">Open a section to fine-tune it. Everything stays editable after creation.</p>
        </div>

        <div class="space-y-3">
          <ConfigSection
            icon="user"
            title="System user"
            pill-label="Automatic"
            :summary="previews.unixUser"
          >
            <dl class="space-y-2 text-[12px]">
              <div class="flex items-center justify-between gap-3">
                <dt class="text-ink-muted">Unix user</dt>
                <dd class="truncate font-mono text-accent-200">{{ previews.unixUser }}</dd>
              </div>
              <div class="flex items-center justify-between gap-3">
                <dt class="text-ink-muted">Root directory</dt>
                <dd class="truncate font-mono text-accent-200">{{ previews.rootPath }}</dd>
              </div>
              <div class="flex items-center justify-between gap-3">
                <dt class="text-ink-muted">FPM socket</dt>
                <dd class="truncate font-mono text-accent-200">{{ previews.socketPath }}</dd>
              </div>
            </dl>
            <p class="mt-3 text-[12px] leading-relaxed text-ink-muted">
              A dedicated system user isolates this site from every other one on the node. It is derived from the site
              name and created during activation — reusing an existing user is not supported, because one user per
              site is what keeps sites isolated from each other.
            </p>
          </ConfigSection>

          <ConfigSection
            icon="file-code-2"
            title="Runtime"
            pill-label="PHP"
            pill-tone="accent"
            :summary="phpVersion ? `PHP ${phpVersion} · PHP-FPM + Nginx` : 'Select a PHP version'"
          >
            <FormField label="PHP version">
              <Combobox v-model="phpVersion" :disabled="submitted">
                <ComboboxAnchor as-child>
                  <ComboboxTrigger aria-label="PHP version" placeholder="Select a PHP version" :label="runtimeLabel(phpVersion)" />
                </ComboboxAnchor>
                <ComboboxList>
                  <ComboboxInput placeholder="Search versions…" />
                  <ComboboxEmpty>No PHP runtimes installed</ComboboxEmpty>
                  <ComboboxGroup>
                    <ComboboxItem
                      v-for="runtime in runtimes"
                      :key="runtime.version"
                      :value="runtime.version"
                      :text-value="`PHP ${runtime.version}`"
                    >
                      {{ runtimeLabel(runtime.version) }}
                      <ComboboxItemIndicator />
                    </ComboboxItem>
                  </ComboboxGroup>
                </ComboboxList>
              </Combobox>
            </FormField>
            <p class="mt-3 flex items-center justify-between gap-2 text-[12px]">
              <span class="text-ink-muted">Handler</span>
              <span class="text-ink-secondary">PHP-FPM + Nginx</span>
            </p>
          </ConfigSection>

          <ConfigSection
            v-model:open="dbOpen"
            icon="database"
            title="Database"
            :pill-label="dbActive ? 'Will be created' : canDatabase && activeServers.length > 0 ? 'Skipped' : 'Unavailable'"
            :pill-tone="dbActive ? 'success' : 'neutral'"
            :summary="dbSummary"
            :static="!canDatabase || activeServers.length === 0"
          >
            <template v-if="canDatabase && activeServers.length > 0" #control>
              <Switch :model-value="dbEnabled" :disabled="submitted" aria-label="Create a database" @update:model-value="setDbEnabled" />
            </template>
            <div v-if="dbEnabled" class="space-y-4">
              <FormField label="Server" hint="The engine is decided by the server you pick." :error="dbServerError">
                <Combobox v-model="dbServerId" :disabled="submitted">
                  <ComboboxAnchor as-child>
                    <ComboboxTrigger
                      :invalid="!!dbServerError"
                      placeholder="Select server"
                      :label="selectedServer ? serverLabel(selectedServer) : ''"
                    />
                  </ComboboxAnchor>
                  <ComboboxList>
                    <ComboboxInput placeholder="Search servers…" />
                    <ComboboxEmpty>No active servers</ComboboxEmpty>
                    <ComboboxGroup>
                      <ComboboxItem
                        v-for="server in activeServers"
                        :key="server.id"
                        :value="server.id"
                        :text-value="serverLabel(server)"
                      >
                        {{ serverLabel(server) }}<ComboboxItemIndicator />
                      </ComboboxItem>
                    </ComboboxGroup>
                  </ComboboxList>
                </Combobox>
              </FormField>
              <FormField label="Database name" :hint="DB_NAME_HINT" :error="dbNameError">
                <AppInput
                  :model-value="dbName"
                  autocomplete="off"
                  :spellcheck="false"
                  :invalid="!!dbNameError"
                  :disabled="submitted"
                  @update:model-value="setDbName"
                />
              </FormField>
              <FormField label="User" hint="The login that owns the new database.">
                <Combobox v-model="dbOwner" :disabled="submitted">
                  <ComboboxAnchor as-child>
                    <ComboboxTrigger placeholder="Select user" :label="dbOwnerLabel(dbOwner)" />
                  </ComboboxAnchor>
                  <ComboboxList>
                    <ComboboxInput placeholder="Search users…" />
                    <ComboboxEmpty>No users on this server yet</ComboboxEmpty>
                    <ComboboxGroup>
                      <ComboboxItem :value="CREATE_NEW_DB_USER" text-value="Create a new user">
                        Create a new user<ComboboxItemIndicator />
                      </ComboboxItem>
                      <ComboboxItem
                        v-for="user in dbOwnerOptions"
                        :key="user.id"
                        :value="user.id"
                        :text-value="userLabel(user)"
                      >
                        {{ userLabel(user) }}<ComboboxItemIndicator />
                      </ComboboxItem>
                    </ComboboxGroup>
                  </ComboboxList>
                </Combobox>
              </FormField>
              <template v-if="creatingDbUser">
                <FormField label="Login" hint="Cleared fields fall back to the suggested value." :error="dbUserError">
                  <AppInput
                    :model-value="dbUserName"
                    autocomplete="off"
                    :spellcheck="false"
                    :invalid="!!dbUserError"
                    :disabled="submitted"
                    @update:model-value="setDbUserName"
                  />
                </FormField>
                <FormField
                  v-if="dbHostVisible"
                  label="Host"
                  hint="Where this user may connect from: localhost, %, or a specific address."
                  :error="dbHostError"
                >
                  <AppInput
                    v-model="dbHost"
                    autocomplete="off"
                    :spellcheck="false"
                    :invalid="!!dbHostError"
                    :disabled="submitted"
                  />
                </FormField>
                <PasswordField
                  v-model="dbPassword"
                  v-model:confirmation="dbPasswordConfirm"
                  label="Password"
                  with-confirmation
                  :confirm-error="dbConfirmError"
                  :minimum-length="8"
                  :maximum-length="128"
                  :error="dbPasswordError"
                  hint="Generated for you — it is shown once more on the final screen so you can copy it."
                />
              </template>
            </div>
            <p v-else class="text-[13px] leading-relaxed text-ink-secondary">
              No database will be created. If your app needs one later, add it from the Databases page and link it to
              this site.
            </p>
          </ConfigSection>

          <ConfigSection
            v-model:open="sftpOpen"
            icon="server"
            title="SFTP access"
            :pill-label="sftpEnabled ? 'Will be enabled' : 'Optional'"
            :pill-tone="sftpEnabled ? 'success' : 'neutral'"
            :summary="sftpSummary"
          >
            <template #control>
              <Switch
                :model-value="sftpEnabled"
                :disabled="submitted"
                aria-label="Enable SFTP access"
                @update:model-value="setSftpEnabled"
              />
            </template>
            <div v-if="sftpEnabled" class="space-y-4">
              <dl class="space-y-2 text-[12px]">
                <div class="flex items-center justify-between gap-3">
                  <dt class="text-ink-muted">Username</dt>
                  <dd class="truncate font-mono text-accent-200">{{ previews.unixUser }}</dd>
                </div>
                <div class="flex items-center justify-between gap-3">
                  <dt class="text-ink-muted">Host · Port</dt>
                  <dd class="truncate font-mono text-accent-200">{{ primaryDomain.trim() || 'your domain' }} · 22</dd>
                </div>
              </dl>
              <PasswordField
                v-model="sftpPassword"
                v-model:confirmation="sftpPasswordConfirm"
                label="Password"
                with-confirmation
                :confirm-error="sftpConfirmError"
                :minimum-length="12"
                :maximum-length="72"
                :error="sftpPasswordError"
                hint="Only a hash is stored. Logins start the moment the site is activated — jailed to this site's own directory."
              />
            </div>
            <p v-else class="text-[13px] leading-relaxed text-ink-secondary">
              SFTP stays off. It uses the site's own system user and can be enabled from the site page once the site is
              live.
            </p>
          </ConfigSection>

          <ConfigSection
            icon="archive"
            title="Backup copies"
            pill-label="Optional"
            summary="Add this site to a backup plan any time from the Backups page."
            static
          />
        </div>

        <!-- Provisioning progress -->
        <AppCard v-if="submitted" eyebrow="Provisioning" :title="`Creating ${primaryDomain.trim()}`">
          <ol class="space-y-1.5">
            <li
              v-for="item in stageList"
              :key="item.key"
              class="flex items-center gap-3 rounded-lg border border-outline bg-canvas/40 px-3 py-2 text-[13px]"
            >
              <span
                class="grid size-6 shrink-0 place-items-center rounded-full border"
                :class="{
                  'border-emerald-400/40 bg-emerald-400/10 text-emerald-300': stageState(item.key) === 'done',
                  'border-accent-400/40 bg-accent-500/10 text-accent-300': stageState(item.key) === 'running',
                  'border-rose-400/40 bg-rose-400/10 text-rose-300': stageState(item.key) === 'failed',
                  'border-outline bg-white/[0.02] text-ink-muted': stageState(item.key) === 'pending',
                }"
              >
                <AppIcon v-if="stageState(item.key) === 'done'" name="check" :size="13" />
                <span
                  v-else-if="stageState(item.key) === 'running'"
                  class="size-3 animate-spin rounded-full border-2 border-current border-t-transparent"
                  aria-hidden="true"
                />
                <AppIcon v-else-if="stageState(item.key) === 'failed'" name="x" :size="13" />
                <span v-else class="size-1.5 rounded-full bg-current" aria-hidden="true" />
              </span>
              <span :class="stageState(item.key) === 'pending' ? 'text-ink-muted' : 'text-ink-secondary'">
                {{ item.label }}
              </span>
            </li>
          </ol>
          <div class="mt-3 space-y-2">
            <JobProgress v-if="activeProgress" :event="activeProgress" v-bind="progressExtras" />
          </div>
        </AppCard>

        <JobFailureNotice v-if="siteRunner.error.value" v-bind="siteRunner.failureProps.value" />
        <JobFailureNotice v-if="dbUserRunner.error.value" v-bind="dbUserRunner.failureProps.value" />
        <JobFailureNotice v-if="dbRunner.error.value" v-bind="dbRunner.failureProps.value" />

        <div class="flex items-center justify-between gap-3">
          <AppButton variant="secondary" icon="arrow-left" :disabled="submitted" @click="step = 0">Back</AppButton>
          <AppButton variant="primary" icon="check" :loading="submitted" :disabled="createDisabled" @click="submit">
            Create site
          </AppButton>
        </div>
      </div>

      <!-- STEP 2 — Done -->
      <SiteCreateSuccess
        v-else-if="step === 2 && createdSite"
        :site="createdSite"
        v-bind="successProps"
        @create-another="createAnother"
      />
    </template>
  </section>
</template>
