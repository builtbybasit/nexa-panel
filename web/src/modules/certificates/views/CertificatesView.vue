<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { listSites } from '@/modules/sites/api'
import { daysUntil, formatTime } from '@/shared/formatters'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppInput,
  AppSelect,
  EmptyState,
  FactList,
  FormField,
  JobProgress,
  PageHeader,
  ResourceRow,
  StatusPill,
} from '@/shared/ui'

import {
  applyCertificate,
  createCertificate,
  getCertificatePlan,
  listCertificates,
  prepareCertificate,
  type Certificate,
  type CertificateOperation,
  type CertificatePlan,
} from '../api'

const siteId = ref('')
const email = ref('')

const selected = ref<Certificate>()
const plan = ref<CertificatePlan>()
const planExpiresAt = ref('')

const runner = useJobRunner()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites })
const certificatesQuery = useQuery({ queryKey: ['certificates'], queryFn: () => listCertificates() })
const sites = computed(() => sitesQuery.data.value ?? [])
const certificates = computed(() => certificatesQuery.data.value ?? [])

const planFacts = computed(() =>
  plan.value
    ? [
        { label: 'Operation', value: plan.value.operation },
        { label: 'Subject alternative names', value: plan.value.agentPlan.request.domains.join(', ') },
        { label: 'DNS checked', value: `${Object.keys(plan.value.dns).length} hostnames` },
        { label: 'Certificate path', value: plan.value.agentPlan.certificatePath, mono: true },
      ]
    : [],
)

function expiryLabel(certificate: Certificate): string {
  const days = daysUntil(certificate.expiresAt)
  return days === undefined ? 'Not issued' : `${days} days`
}

async function refreshSelected(certificateId: string) {
  await certificatesQuery.refetch()
  selected.value = certificates.value.find((certificate) => certificate.id === certificateId)
}

async function loadPlan(certificate: Certificate) {
  selected.value = certificate
  plan.value = undefined
  try {
    const response = await getCertificatePlan(certificate.id)
    plan.value = response.plan
    planExpiresAt.value = response.expiresAt
  } catch {
    // A certificate without a prepared plan is a normal state.
  }
}

async function create() {
  await runner.run(
    async () => {
      const result = await createCertificate(siteId.value, email.value)
      selected.value = result.certificate
      return result.job.id
    },
    {
      onSettled: async () => {
        if (selected.value) await refreshSelected(selected.value.id)
      },
      onSuccess: async () => {
        if (selected.value) await loadPlan(selected.value)
      },
      failureMessage: 'The certificate operation failed. Inspect Jobs before retrying.',
    },
  )
}

async function prepare(operation: CertificateOperation) {
  const certificate = selected.value
  if (!certificate) return
  await runner.run(async () => (await prepareCertificate(certificate.id, operation)).id, {
    onSettled: () => refreshSelected(certificate.id),
    onSuccess: async () => {
      if (selected.value) await loadPlan(selected.value)
    },
    failureMessage: 'The certificate plan could not be prepared. Inspect Jobs for details.',
  })
}

async function apply() {
  const certificate = selected.value
  if (!certificate) return
  await runner.run(async () => (await applyCertificate(certificate.id)).id, {
    onSettled: () => refreshSelected(certificate.id),
    failureMessage: 'The certificate operation failed. Previously active TLS remains in place.',
  })
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Let's Encrypt HTTP-01"
      title="TLS certificates"
      description="Issuance uses the port-80 webroot challenge after DNS preflight. Renewal failures never remove a working certificate."
    />

    <div class="grid gap-4 lg:grid-cols-[minmax(0,380px)_1fr]">
      <AppCard
        eyebrow="Certificate resource"
        title="Enable HTTPS"
        description="All active non-redirect hostnames become SANs after DNS preflight."
      >
        <form class="space-y-4" @submit.prevent="create">
          <FormField label="Site">
            <AppSelect v-model="siteId" required>
              <option disabled value="">Select site</option>
              <option v-for="site in sites" :key="site.id" :value="site.id">
                {{ site.displayName }} · {{ site.primaryDomain }}
              </option>
            </AppSelect>
          </FormField>
          <FormField label="ACME contact email">
            <AppInput v-model="email" type="email" placeholder="admin@example.com" required />
          </FormField>

          <AppAlert v-if="runner.error.value" tone="danger">{{ runner.error.value }}</AppAlert>
          <JobProgress v-if="runner.progress.value" :event="runner.progress.value" />

          <AppButton variant="primary" type="submit" :loading="runner.busy.value" class="w-full">
            Prepare issue plan
          </AppButton>
        </form>
      </AppCard>

      <AppCard eyebrow="Expiry monitoring" :title="`${certificates.length} certificates`" flush>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="certificates.length" class="space-y-1">
            <ResourceRow
              v-for="certificate in certificates"
              :key="certificate.id"
              :title="certificate.primaryDomain"
              :subtitle="`${certificate.domains.length} names · ${certificate.email}`"
              icon="shield"
              clickable
              @select="loadPlan(certificate)"
            >
              <template #meta>
                <span class="hidden shrink-0 font-mono text-xs text-ink-muted sm:inline">{{ expiryLabel(certificate) }}</span>
              </template>
              <template #status>
                <StatusPill
                  v-if="certificate.expiringSoon"
                  tone="warning"
                  label="expires soon"
                />
                <StatusPill v-else :status="certificate.status" />
              </template>
            </ResourceRow>
          </div>
          <EmptyState
            v-else
            icon="shield"
            title="No certificates"
            description="Prepare an issue plan to serve a managed site over HTTPS."
            class="m-2"
          />
        </div>
      </AppCard>
    </div>

    <AppCard v-if="selected" eyebrow="Certificate lifecycle" :title="selected.primaryDomain">
      <template #actions>
        <StatusPill v-if="plan && planExpiresAt" tone="warning" :label="`Plan expires ${formatTime(planExpiresAt)}`" :pulse="false" />
      </template>

      <div class="space-y-4">
        <template v-if="plan">
          <FactList :facts="planFacts" />
          <div class="flex flex-wrap gap-2">
            <AppButton variant="primary" :loading="runner.busy.value" class="capitalize" @click="apply">
              Approve {{ plan.operation }}
            </AppButton>
          </div>
        </template>

        <div v-if="selected.status === 'active'" class="flex flex-wrap gap-2">
          <AppButton icon="refresh-cw" :disabled="runner.busy.value" @click="prepare('renew')">Prepare renewal</AppButton>
          <AppButton variant="danger" icon="x" :disabled="runner.busy.value" @click="prepare('revoke')">
            Prepare revocation
          </AppButton>
        </div>
        <div v-else-if="selected.status === 'revoked'" class="flex flex-wrap gap-2">
          <AppButton variant="primary" :disabled="runner.busy.value" @click="prepare('issue')">Prepare reissue</AppButton>
        </div>
      </div>
    </AppCard>
  </section>
</template>
