<script setup lang="ts">
import { computed, ref, watch, type WritableComputedRef } from 'vue'

import {
  AppButton,
  AppCard,
  AppInput,
  FormField,
  PasswordField,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  Switch,
} from '@/shared/ui'

import type { SiteSettings } from '../api'

const props = defineProps<{ settings: SiteSettings; canWrite: boolean; busy: boolean }>()
const emit = defineEmits<{ save: [settings: SiteSettings] }>()

// Plain data — a structural clone is enough to detach the editable draft from
// the prop so edits don't mutate the query cache before they're saved.
function clone(settings: SiteSettings): SiteSettings {
  return JSON.parse(JSON.stringify(settings)) as SiteSettings
}

const draft = ref<SiteSettings>(clone(props.settings))

// Re-sync whenever the site is refetched (e.g. after a save applies), unless
// the user is mid-edit — a settled prop should not clobber pending changes.
watch(
  () => props.settings,
  (next) => {
    if (!dirty.value) draft.value = clone(next)
  },
)

const dirty = computed(() => JSON.stringify(draft.value) !== JSON.stringify(props.settings))
const disabled = computed(() => !props.canWrite || props.busy)

// number <input> emits strings; coerce back so the draft stays numeric for the
// dirty comparison and the API body.
function numberField(get: () => number, set: (value: number) => void): WritableComputedRef<number> {
  return computed({
    get,
    set: (raw) => {
      const parsed = typeof raw === 'number' ? raw : Number(raw)
      set(Number.isFinite(parsed) ? parsed : 0)
    },
  })
}

const gzipLevel = numberField(() => draft.value.gzipLevel, (v) => (draft.value.gzipLevel = v))
const staticCacheDays = numberField(() => draft.value.staticCacheDays, (v) => (draft.value.staticCacheDays = v))
const clientMaxBodyMb = numberField(() => draft.value.clientMaxBodyMb, (v) => (draft.value.clientMaxBodyMb = v))
const hstsMaxAge = numberField(() => draft.value.hstsMaxAge, (v) => (draft.value.hstsMaxAge = v))
const pmMaxChildren = numberField(() => draft.value.pmMaxChildren, (v) => (draft.value.pmMaxChildren = v))
const pmMaxRequests = numberField(() => draft.value.pmMaxRequests, (v) => (draft.value.pmMaxRequests = v))
const rateRps = numberField(() => draft.value.rateLimit.requestsPerSecond, (v) => (draft.value.rateLimit.requestsPerSecond = v))
const rateBurst = numberField(() => draft.value.rateLimit.burst, (v) => (draft.value.rateLimit.burst = v))
const logRotateKeep = numberField(() => draft.value.logRotation.keepFiles, (v) => (draft.value.logRotation.keepFiles = v))

// reka-ui forbids an empty-string SelectItem value, so the "no charset
// directive" default is carried through the select as a sentinel and mapped
// back to '' — the only value the API and the renderer ever see.
const CHARSET_DEFAULT = 'default'
const charset = computed<string>({
  get: () => (draft.value.charset === '' ? CHARSET_DEFAULT : draft.value.charset),
  set: (value) => {
    draft.value.charset = (value === CHARSET_DEFAULT ? '' : value) as SiteSettings['charset']
  },
})

// The index list is an ordered array in the API but a single field in the UI;
// split on whitespace or commas and drop blanks so a trailing separator while
// typing does not produce an empty entry the server would reject.
const indexFiles = computed<string>({
  get: () => draft.value.indexFiles.join(' '),
  set: (raw) => {
    draft.value.indexFiles = String(raw).split(/[\s,]+/).filter(Boolean)
  },
})

const tabs = [
  { id: 'static', label: 'Static content' },
  { id: 'https', label: 'HTTPS' },
  { id: 'access', label: 'Access control' },
  { id: 'backend', label: 'PHP-FPM' },
  { id: 'logs', label: 'Log settings' },
] as const
const activeTab = ref<(typeof tabs)[number]['id']>('static')

function save() {
  if (!props.canWrite || props.busy || !dirty.value) return
  emit('save', clone(draft.value))
}
</script>

<template>
  <AppCard eyebrow="Configuration" title="Nginx & PHP-FPM settings">
    <template #actions>
      <AppButton
        size="sm"
        variant="primary"
        icon="check"
        :disabled="disabled || !dirty"
        :loading="busy"
        @click="save"
      >
        Save changes
      </AppButton>
    </template>

    <div class="space-y-5">
      <p class="text-[13px] leading-relaxed text-ink-secondary">
        Tune how this site is served. Changes are re-applied to the live Nginx and PHP-FPM configuration when you save.
      </p>

      <!-- Tabs -->
      <div class="flex flex-wrap gap-1 rounded-xl border border-outline bg-canvas/40 p-1" role="tablist">
        <button
          v-for="tab in tabs"
          :key="tab.id"
          type="button"
          role="tab"
          :aria-selected="activeTab === tab.id"
          class="rounded-lg px-3 py-1.5 text-[13px] font-medium transition-colors"
          :class="
            activeTab === tab.id
              ? 'bg-white/[0.06] text-ink'
              : 'text-ink-muted hover:text-ink-secondary'
          "
          @click="activeTab = tab.id"
        >
          {{ tab.label }}
        </button>
      </div>

      <!-- Static content -->
      <div v-show="activeTab === 'static'" class="space-y-4">
        <FormField
          label="Subdirectory"
          hint="Folder inside the site's public/ directory to serve, e.g. dist or app/public. Leave empty to serve public/ itself. (For PHP's working directory, see Working subdirectory on the PHP-FPM tab.)"
        >
          <AppInput
            v-model="draft.subdirectory"
            placeholder="public/"
            spellcheck="false"
            autocapitalize="none"
            autocorrect="off"
            :disabled="disabled"
          />
        </FormField>
        <FormField
          label="Character set"
          hint="Adds a charset directive to this site's Nginx configuration. The default emits no directive, leaving the character set to whatever your application sends."
        >
          <Select v-model="charset" :disabled="disabled">
            <SelectTrigger />
            <SelectContent>
              <SelectItem value="default">Default — no charset directive</SelectItem>
              <SelectItem value="off">Off (charset off)</SelectItem>
              <SelectItem value="utf-8">UTF-8</SelectItem>
              <SelectItem value="iso-8859-1">ISO-8859-1 (Latin-1)</SelectItem>
              <SelectItem value="iso-8859-2">ISO-8859-2 (Latin-2)</SelectItem>
              <SelectItem value="iso-8859-15">ISO-8859-15 (Latin-9)</SelectItem>
              <SelectItem value="windows-1251">Windows-1251 (Cyrillic)</SelectItem>
              <SelectItem value="windows-1252">Windows-1252 (Western)</SelectItem>
              <SelectItem value="koi8-r">KOI8-R (Russian)</SelectItem>
              <SelectItem value="koi8-u">KOI8-U (Ukrainian)</SelectItem>
              <SelectItem value="big5">Big5 (Traditional Chinese)</SelectItem>
              <SelectItem value="euc-jp">EUC-JP (Japanese)</SelectItem>
              <SelectItem value="euc-kr">EUC-KR (Korean)</SelectItem>
              <SelectItem value="gb2312">GB2312 (Simplified Chinese)</SelectItem>
              <SelectItem value="shift_jis">Shift_JIS (Japanese)</SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <div class="flex items-center justify-between gap-4 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
          <div class="min-w-0">
            <p class="text-[13px] font-medium text-ink">Gzip compression</p>
            <p class="mt-0.5 text-xs text-ink-muted">Compress text responses before sending them to visitors.</p>
          </div>
          <Switch v-model="draft.gzip" :disabled="disabled" aria-label="Gzip compression" />
        </div>
        <div class="grid gap-4 sm:grid-cols-3">
          <FormField label="Compression level" hint="1 (fastest) – 9 (smallest)">
            <AppInput v-model="gzipLevel" type="number" min="1" max="9" :disabled="disabled || !draft.gzip" />
          </FormField>
          <FormField label="Static cache (days)" hint="0 sends no expires header">
            <AppInput v-model="staticCacheDays" type="number" min="0" max="3650" :disabled="disabled" />
          </FormField>
          <FormField label="Max upload size (MB)" hint="Request body limit (1 – 2048)">
            <AppInput v-model="clientMaxBodyMb" type="number" min="1" max="2048" :disabled="disabled" />
          </FormField>
        </div>
      </div>

      <!-- HTTPS -->
      <div v-show="activeTab === 'https'" class="space-y-4">
        <div class="flex items-center justify-between gap-4 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
          <div class="min-w-0">
            <p class="text-[13px] font-medium text-ink">Redirect to HTTPS</p>
            <p class="mt-0.5 text-xs text-ink-muted">
              Send visitors who arrive over plain HTTP to the secure address with a 301. Takes effect once
              the site has a certificate; certificate renewal keeps working either way.
            </p>
          </div>
          <Switch v-model="draft.httpsRedirect" :disabled="disabled" aria-label="Redirect to HTTPS" />
        </div>
        <div class="flex items-center justify-between gap-4 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
          <div class="min-w-0">
            <p class="text-[13px] font-medium text-ink">HSTS</p>
            <p class="mt-0.5 text-xs text-ink-muted">
              Tell browsers to only reach this site over HTTPS. Browsers that have already seen this header
              will keep forcing HTTPS even if the redirect above is turned off.
            </p>
          </div>
          <Switch v-model="draft.hsts" :disabled="disabled" aria-label="HSTS" />
        </div>
        <FormField label="HSTS max-age (seconds)" hint="How long browsers remember the HTTPS-only rule.">
          <AppInput v-model="hstsMaxAge" type="number" min="0" :disabled="disabled || !draft.hsts" />
        </FormField>
        <div class="flex items-center justify-between gap-4 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
          <div class="min-w-0">
            <p class="text-[13px] font-medium text-ink">HTTP/2</p>
            <p class="mt-0.5 text-xs text-ink-muted">Multiplexed connections for faster page loads.</p>
          </div>
          <Switch v-model="draft.http2" :disabled="disabled" aria-label="HTTP/2" />
        </div>
        <div class="flex items-center justify-between gap-4 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
          <div class="min-w-0">
            <p class="text-[13px] font-medium text-ink">HTTP/3</p>
            <p class="mt-0.5 text-xs text-ink-muted">QUIC-based transport for lower latency.</p>
          </div>
          <Switch v-model="draft.http3" :disabled="disabled" aria-label="HTTP/3" />
        </div>
      </div>

      <!-- Access control -->
      <div v-show="activeTab === 'access'" class="space-y-4">
        <div class="flex items-center justify-between gap-4 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
          <div class="min-w-0">
            <p class="text-[13px] font-medium text-ink">Basic authentication</p>
            <p class="mt-0.5 text-xs text-ink-muted">Require a username and password before the site loads.</p>
          </div>
          <Switch v-model="draft.basicAuth.enabled" :disabled="disabled" aria-label="Basic authentication" />
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField label="Realm">
            <AppInput v-model="draft.basicAuth.realm" :disabled="disabled || !draft.basicAuth.enabled" />
          </FormField>
          <FormField label="Username" hint="Lowercase letters, digits, - and _ (max 32).">
            <AppInput
              v-model="draft.basicAuth.username"
              pattern="[a-z0-9_-]{1,32}"
              maxlength="32"
              :disabled="disabled || !draft.basicAuth.enabled"
            />
          </FormField>
        </div>
        <PasswordField
          v-model="draft.basicAuth.password"
          label="Password"
          :minimum-length="8"
          hint="Leave blank to keep the current password for this username."
          autocomplete="new-password"
        />

        <div class="flex items-center justify-between gap-4 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
          <div class="min-w-0">
            <p class="text-[13px] font-medium text-ink">Rate limiting</p>
            <p class="mt-0.5 text-xs text-ink-muted">Throttle requests per client IP to blunt abuse.</p>
          </div>
          <Switch v-model="draft.rateLimit.enabled" :disabled="disabled" aria-label="Rate limiting" />
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField label="Requests per second" hint="1 – 10000">
            <AppInput
              v-model="rateRps"
              type="number"
              min="1"
              max="10000"
              :disabled="disabled || !draft.rateLimit.enabled"
            />
          </FormField>
          <FormField label="Burst" hint="Extra requests allowed above the rate (0 – 10000)">
            <AppInput
              v-model="rateBurst"
              type="number"
              min="0"
              max="10000"
              :disabled="disabled || !draft.rateLimit.enabled"
            />
          </FormField>
        </div>
      </div>

      <!-- Backend -->
      <div v-show="activeTab === 'backend'" class="space-y-4">
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField
            label="Application file"
            hint="Files Nginx looks for when a folder is requested, in order — separate with spaces. Default: index.php index.html. The first .php file here is also the front controller that pretty URLs fall back to."
          >
            <AppInput
              v-model="indexFiles"
              placeholder="index.php index.html"
              spellcheck="false"
              autocapitalize="none"
              autocorrect="off"
              :disabled="disabled"
            />
          </FormField>
          <FormField
            label="Working subdirectory"
            hint="PHP's working directory, relative to the site root (e.g. public or app). Leave empty to use the site root. This is not the Subdirectory field on the Static content tab — that one is relative to public/ and changes which folder visitors are served; this one only changes where PHP scripts start from and publishes nothing."
          >
            <AppInput
              v-model="draft.workingDirectory"
              placeholder="(site root)"
              spellcheck="false"
              autocapitalize="none"
              autocorrect="off"
              :disabled="disabled"
            />
          </FormField>
        </div>
        <div class="grid gap-4 sm:grid-cols-3">
          <FormField label="Process manager" hint="How PHP-FPM spawns worker processes.">
            <Select v-model="draft.pmMode" :disabled="disabled">
              <SelectTrigger />
              <SelectContent>
                <SelectItem value="ondemand">On demand</SelectItem>
                <SelectItem value="dynamic">Dynamic</SelectItem>
                <SelectItem value="static">Static</SelectItem>
              </SelectContent>
            </Select>
          </FormField>
          <FormField label="Max children" hint="Worker process ceiling (1 – 1024)">
            <AppInput v-model="pmMaxChildren" type="number" min="1" max="1024" :disabled="disabled" />
          </FormField>
          <FormField label="Max requests" hint="Recycle a worker after N requests (0 = unlimited)">
            <AppInput v-model="pmMaxRequests" type="number" min="0" max="100000" :disabled="disabled" />
          </FormField>
        </div>
      </div>

      <!-- Log settings -->
      <div v-show="activeTab === 'logs'" class="space-y-4">
        <div class="flex items-center justify-between gap-4 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
          <div class="min-w-0">
            <p class="text-[13px] font-medium text-ink">Access log</p>
            <p class="mt-0.5 text-xs text-ink-muted">Record every request Nginx serves for this site.</p>
          </div>
          <Switch v-model="draft.accessLog" :disabled="disabled" aria-label="Access log" />
        </div>
        <div class="flex items-center justify-between gap-4 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
          <div class="min-w-0">
            <p class="text-[13px] font-medium text-ink">Error log</p>
            <p class="mt-0.5 text-xs text-ink-muted">Capture Nginx errors for this site.</p>
          </div>
          <Switch v-model="draft.errorLog" :disabled="disabled" aria-label="Error log" />
        </div>
        <div class="flex items-center justify-between gap-4 rounded-xl border border-outline bg-canvas/40 px-4 py-3">
          <div class="min-w-0">
            <p class="text-[13px] font-medium text-ink">Log rotation</p>
            <p class="mt-0.5 text-xs text-ink-muted">
              Trim this site's access, error, and PHP error logs on a schedule instead of letting them grow forever.
            </p>
          </div>
          <Switch v-model="draft.logRotation.enabled" :disabled="disabled" aria-label="Log rotation" />
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField label="Rotate" hint="Hourly only takes effect if the node runs logrotate hourly.">
            <Select v-model="draft.logRotation.frequency" :disabled="disabled || !draft.logRotation.enabled">
              <SelectTrigger />
              <SelectContent>
                <SelectItem value="hourly">Hourly</SelectItem>
                <SelectItem value="daily">Daily</SelectItem>
                <SelectItem value="weekly">Weekly</SelectItem>
              </SelectContent>
            </Select>
          </FormField>
          <FormField label="Stored files" hint="Rotated copies to keep before the oldest is deleted (1 – 365)">
            <AppInput
              v-model="logRotateKeep"
              type="number"
              min="1"
              max="365"
              :disabled="disabled || !draft.logRotation.enabled"
            />
          </FormField>
        </div>
      </div>
    </div>
  </AppCard>
</template>
