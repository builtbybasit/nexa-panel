import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'
import type { SiteDeploymentMode } from '../sites/api'

// One authorized key as the panel lists it. The key body is deliberately absent
// from the read shape: the fingerprint is what a human compares against
// `ssh-keygen -lf`, so the blob never needs to reach the page.
export interface SshKey {
  id: string
  label: string
  algorithm: string
  fingerprint: string
  comment?: string
  createdAt: string
}

export interface SshAccess {
  siteId: string
  enabled: boolean
  username: string
  host: string
  port: number
  shell: string
  authorizedKeysPath: string
  enabledAt?: string
  keys: SshKey[]
}

// Returned only by the generate endpoint: the private half appears once, in the
// response, and is never stored or retrievable again.
export interface GeneratedSshKey {
  key: SshKey
  privateKey: string
  access: SshAccess
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Deploy request')
}

function sshPath(siteId: string, suffix = ''): string {
  return `/api/v1/sites/${encodeURIComponent(siteId)}/ssh${suffix}`
}

export function getSSHAccess(siteId: string): Promise<SshAccess> {
  return request(sshPath(siteId))
}

export function enableSSHAccess(siteId: string): Promise<SshAccess> {
  return request(sshPath(siteId, '/enable'), { method: 'POST' })
}

export function disableSSHAccess(siteId: string): Promise<SshAccess> {
  return request(sshPath(siteId, '/disable'), { method: 'POST' })
}

export function addSSHKey(siteId: string, label: string, publicKey: string): Promise<SshAccess> {
  return request(sshPath(siteId, '/keys'), { method: 'POST', body: JSON.stringify({ label, publicKey }) })
}

export function removeSSHKey(siteId: string, keyId: string): Promise<SshAccess> {
  return request(sshPath(siteId, `/keys/${encodeURIComponent(keyId)}`), { method: 'DELETE' })
}

export function generateSSHKey(siteId: string, label: string): Promise<GeneratedSshKey> {
  return request(sshPath(siteId, '/keys/generate'), { method: 'POST', body: JSON.stringify({ label }) })
}

// The site's GitHub deploy key. Only public material crosses the wire: the pair
// is minted on the node and the private half never leaves it, so `present`
// rather than a non-empty publicKey is what says a key exists.
export interface DeployKey {
  siteId: string
  present: boolean
  algorithm?: string
  publicKey?: string
  fingerprint?: string
  keyVersion: number
  repository: string
  lastTestedAt?: string
  lastTestOk?: boolean
  createdAt?: string
}

// The verdict of the two probes the test job runs on the node. A probe that ran
// and failed is a result, not a job failure — the tail is what an operator asked
// the test for.
export interface GitHubTestResult {
  authOk: boolean
  account: string
  lsRemoteOk: boolean
  refCount: number
  outputTail: string
}

function deployKeyPath(siteId: string, suffix = ''): string {
  return `/api/v1/sites/${encodeURIComponent(siteId)}/deploy-key${suffix}`
}

export function getDeployKey(siteId: string): Promise<DeployKey> {
  return request(deployKeyPath(siteId))
}

// An empty repository keeps whatever the panel already stored; rotate replaces
// the pair itself, which invalidates the key GitHub already trusts.
export function ensureDeployKey(siteId: string, repository: string, rotate = false): Promise<DeployKey> {
  return request(deployKeyPath(siteId), { method: 'POST', body: JSON.stringify({ repository, rotate }) })
}

export function testDeployKey(siteId: string, repository: string) {
  return request<{ job: Job }>(deployKeyPath(siteId, '/test'), {
    method: 'POST',
    body: JSON.stringify({ repository }),
  })
}

// The result of a mode switch. A job is present whenever the site was live
// enough to need re-applying; without one the mode is stored and the site's
// next plan picks it up.
export interface DeploymentModeChange {
  siteId: string
  mode: SiteDeploymentMode
  job?: Job
}

// The deployer layout's shared environment file. `content` is a secret in both
// directions — it is never logged, never audited, and never put in a job — so
// it is only ever read into the editor and written straight back.
export interface SharedEnv {
  siteId: string
  path: string
  present: boolean
  content: string
  bytes: number
  sha256: string
  modifiedAt?: string
}

/** The cap the control plane and the node both enforce, restated for the editor. */
export const SHARED_ENV_MAX_BYTES = 64 * 1024

function deploymentPath(siteId: string, suffix = ''): string {
  return `/api/v1/sites/${encodeURIComponent(siteId)}/deployment${suffix}`
}

export function setDeploymentMode(siteId: string, mode: SiteDeploymentMode): Promise<DeploymentModeChange> {
  return request(`${deploymentPath(siteId)}-mode`, { method: 'PATCH', body: JSON.stringify({ mode }) })
}

export function getSharedEnv(siteId: string): Promise<SharedEnv> {
  return request(deploymentPath(siteId, '/env'))
}

export function updateSharedEnv(siteId: string, content: string): Promise<SharedEnv> {
  return request(deploymentPath(siteId, '/env'), { method: 'PUT', body: JSON.stringify({ content }) })
}

// One prerequisite as the prepare run found — or left — it on the node. A tool
// the run could not install comes back with `installed: false` and
// `action: 'failed'`; that is a result to render, not a failed job.
export interface DeployTool {
  name: string
  path?: string
  version?: string
  installed: boolean
  action: 'present' | 'installed' | 'failed'
}

// The port-22 verdict. `checked: false` says the panel could not ask, which is a
// fact about this build and never means the port is closed — the page must not
// render it as one.
interface FirewallSshCheck {
  checked: boolean
  installed: boolean
  active: boolean
  open: boolean
  remediation?: string
}

// The prepare job's result: what the node has, what it now has, and whether an
// SSH client can still reach it. `ready` covers the tooling only; the firewall
// is a warning surface, not something this job clears.
export interface NodePreparation {
  siteId: string
  ready: boolean
  tools: DeployTool[]
  firewallSsh: FirewallSshCheck
  warnings: string[]
}

// No request body: the branch the node is prepared for is the one the site
// already serves, and a caller who could name a version could name a package.
export function prepareNode(siteId: string) {
  return request<{ job: Job }>(deploymentPath(siteId, '/prepare'), { method: 'POST' })
}
