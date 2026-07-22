<script setup lang="ts">
import { computed, ref } from 'vue'

import {
  DEFAULT_LOCAL_PORTS,
  cliCommand,
  connectionUri,
  jdbcUrl,
  sshTunnelCommand,
  tunneledTarget,
  type ConnectionTarget,
  type EngineKind,
} from '@/shared/lib/connectionStrings'

import AppAlert from './AppAlert.vue'
import AppButton from './AppButton.vue'
import AppCard from './AppCard.vue'
import AppInput from './AppInput.vue'
import CopyField from './CopyField.vue'
import FormField from './FormField.vue'
import { Select, SelectContent, SelectItem, SelectTrigger } from './select'

const props = defineProps<{
  engine: EngineKind
  /** Human name of the server, e.g. "PostgreSQL 17 · main". */
  engineLabel: string
  port: number
  database: string
  /** Accounts that can reach this database; the first one is preselected. */
  users: { value: string; label: string }[]
  /** Shown when the engine is reachable over a Unix socket as well. */
  socketPath?: string
}>()

/**
 * The node's address as the browser already knows it — the panel and the
 * database engine live on the same host, so whatever reached this page reaches
 * SSH too. Editable because the panel is often opened on an internal name.
 */
const host = ref(window.location.hostname)
const sshUser = ref('root')
const localPort = ref(DEFAULT_LOCAL_PORTS[props.engine])
const username = ref(props.users[0]?.value ?? '')
const overSsh = ref(true)

const jdbcDriverHint = computed(() =>
  props.engine === 'postgres' ? 'PostgreSQL driver' : `${props.engine === 'mariadb' ? 'MariaDB' : 'MySQL'} driver`,
)

const directTarget = computed<ConnectionTarget>(() => ({
  engine: props.engine,
  host: host.value.trim() || window.location.hostname,
  port: props.port,
  database: props.database,
  username: username.value,
}))

const tunnel = computed(() => ({
  sshUser: sshUser.value.trim() || 'root',
  sshHost: directTarget.value.host,
  localPort: Number(localPort.value) || DEFAULT_LOCAL_PORTS[props.engine],
}))

/** What the client is actually pointed at, once the tunnel choice is applied. */
const target = computed(() =>
  overSsh.value ? tunneledTarget(directTarget.value, tunnel.value) : directTarget.value,
)

const tunnelCommand = computed(() => sshTunnelCommand(directTarget.value, tunnel.value))
</script>

<template>
  <AppCard eyebrow="Connect from your computer" title="Remote connection">
    <template #actions>
      <div class="flex items-center gap-1 rounded-lg border border-outline p-1">
        <AppButton size="sm" :variant="overSsh ? 'primary' : 'ghost'" @click="overSsh = true">SSH tunnel</AppButton>
        <AppButton size="sm" :variant="overSsh ? 'ghost' : 'primary'" @click="overSsh = false">Direct</AppButton>
      </div>
    </template>

    <AppAlert v-if="overSsh" tone="info">
      {{ engineLabel }} listens on the node's loopback address only. Open the tunnel below — or paste the same details
      into your client's own SSH-tunnel tab (pgAdmin, Beekeeper Studio, DBeaver and TablePlus all have one) — then
      connect to 127.0.0.1.
    </AppAlert>
    <AppAlert v-else tone="warning">
      A direct connection only works from the node itself, or once you have exposed port {{ port }} and opened it in the
      firewall. From a laptop, prefer the SSH tunnel.
    </AppAlert>

    <div class="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <FormField label="Node address">
        <AppInput v-model="host" autocomplete="off" spellcheck="false" />
      </FormField>
      <FormField v-if="users.length > 1" label="User">
        <Select v-model="username">
          <SelectTrigger placeholder="Select user" />
          <SelectContent>
            <SelectItem v-for="user in users" :key="user.value" :value="user.value">{{ user.label }}</SelectItem>
          </SelectContent>
        </Select>
      </FormField>
      <template v-if="overSsh">
        <FormField label="SSH user">
          <AppInput v-model="sshUser" autocomplete="off" spellcheck="false" />
        </FormField>
        <FormField label="Local port" hint="Port opened on your laptop.">
          <AppInput v-model="localPort" type="number" min="1" max="65535" />
        </FormField>
      </template>
    </div>

    <CopyField
      v-if="overSsh"
      class="mt-4"
      label="SSH tunnel command"
      :value="tunnelCommand"
      hint="Run this in a terminal and leave it open while you are connected."
    />

    <div class="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <CopyField label="Host" :value="target.host" />
      <CopyField label="Port" :value="String(target.port)" />
      <CopyField label="Database" :value="target.database" />
      <CopyField label="User" :value="target.username" />
    </div>

    <div class="mt-4 space-y-4">
      <CopyField
        label="Connection URI"
        :value="connectionUri(target)"
        hint="No password: reveal or rotate the user's credential below, then add it in your client."
      />
      <CopyField label="JDBC URL" :value="jdbcUrl(target)" :hint="jdbcDriverHint" />
      <CopyField label="Command line" :value="cliCommand(target)" />
      <CopyField v-if="socketPath" label="Unix socket (on the node)" :value="socketPath" />
    </div>
  </AppCard>
</template>
