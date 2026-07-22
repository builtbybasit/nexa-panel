import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  addSSHKey,
  disableSSHAccess,
  enableSSHAccess,
  ensureDeployKey,
  generateSSHKey,
  getDeployKey,
  getSharedEnv,
  getSSHAccess,
  prepareNode,
  removeSSHKey,
  setDeploymentMode,
  testDeployKey,
  updateSharedEnv,
} from './api'

const ACCESS = {
  siteId: 'site_1',
  enabled: true,
  username: 'nexa_demo',
  host: 'node.example.com',
  port: 22,
  shell: '/bin/bash',
  authorizedKeysPath: '/etc/nexa-panel/generated/deploy/demo/authorized_keys',
  keys: [],
}

afterEach(() => vi.unstubAllGlobals())

describe('deploy API', () => {
  it('reads per-site SSH access', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json(ACCESS))
    vi.stubGlobal('fetch', fetchMock)
    await expect(getSSHAccess('site_1')).resolves.toEqual(ACCESS)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/ssh', {
      credentials: 'same-origin', headers: { Accept: 'application/json' },
    })
  })

  it('enables and disables access', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(Response.json(ACCESS)))
    vi.stubGlobal('fetch', fetchMock)
    await enableSSHAccess('site_1')
    await disableSSHAccess('site_1')
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/sites/site_1/ssh/enable', {
      method: 'POST', credentials: 'same-origin', headers: { Accept: 'application/json' },
    })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/sites/site_1/ssh/disable', {
      method: 'POST', credentials: 'same-origin', headers: { Accept: 'application/json' },
    })
  })

  it('adds and removes an authorized key, escaping every id', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(Response.json(ACCESS)))
    vi.stubGlobal('fetch', fetchMock)
    await addSSHKey('site_1', 'Laptop', 'ssh-ed25519 AAAAC3Nz laptop')
    await removeSSHKey('site_1', 'sshkey_a/b')
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/sites/site_1/ssh/keys', {
      method: 'POST',
      body: JSON.stringify({ label: 'Laptop', publicKey: 'ssh-ed25519 AAAAC3Nz laptop' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/sites/site_1/ssh/keys/sshkey_a%2Fb', {
      method: 'DELETE', credentials: 'same-origin', headers: { Accept: 'application/json' },
    })
  })

  it('generates a key pair and returns the one-time private half', async () => {
    const generated = { key: { id: 'sshkey_1', label: 'Deploy', algorithm: 'ssh-ed25519', fingerprint: 'SHA256:abc', createdAt: '2026-07-22T10:00:00Z' }, privateKey: 'PRIVATE', access: ACCESS }
    const fetchMock = vi.fn().mockResolvedValue(Response.json(generated))
    vi.stubGlobal('fetch', fetchMock)
    await expect(generateSSHKey('site_1', 'Deploy')).resolves.toEqual(generated)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/ssh/keys/generate', {
      method: 'POST',
      body: JSON.stringify({ label: 'Deploy' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('reads, ensures and rotates the deploy key', async () => {
    const key = { siteId: 'site_1', present: true, algorithm: 'ssh-ed25519', publicKey: 'ssh-ed25519 AAAAC3Nz nexa-demo-deploy', fingerprint: 'SHA256:abc', keyVersion: 2, repository: 'git@github.com:owner/name.git' }
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(Response.json(key)))
    vi.stubGlobal('fetch', fetchMock)
    await expect(getDeployKey('site_1')).resolves.toEqual(key)
    await ensureDeployKey('site_1', 'git@github.com:owner/name.git')
    await ensureDeployKey('site_1', '', true)
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/sites/site_1/deploy-key', {
      credentials: 'same-origin', headers: { Accept: 'application/json' },
    })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/sites/site_1/deploy-key', {
      method: 'POST',
      body: JSON.stringify({ repository: 'git@github.com:owner/name.git', rotate: false }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/v1/sites/site_1/deploy-key', {
      method: 'POST',
      body: JSON.stringify({ repository: '', rotate: true }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('queues the GitHub access test and returns its job', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ job: { id: 7, kind: 'deploy.github_test' } }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(testDeployKey('site_1', 'git@github.com:owner/name.git')).resolves.toEqual({
      job: { id: 7, kind: 'deploy.github_test' },
    })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/deploy-key/test', {
      method: 'POST',
      body: JSON.stringify({ repository: 'git@github.com:owner/name.git' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('switches the deployment mode and returns the re-apply job', async () => {
    const change = { siteId: 'site_1', mode: 'deployer', job: { id: 9, kind: 'site.settings' } }
    const fetchMock = vi.fn().mockResolvedValue(Response.json(change))
    vi.stubGlobal('fetch', fetchMock)
    await expect(setDeploymentMode('site_1', 'deployer')).resolves.toEqual(change)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/deployment-mode', {
      method: 'PATCH',
      body: JSON.stringify({ mode: 'deployer' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('reads and writes the shared environment file', async () => {
    const shared = { siteId: 'site_1', path: '/srv/nexa/sites/demo/app/shared/.env', present: true, content: 'APP_ENV=production\n', bytes: 20, sha256: 'a'.repeat(64) }
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(Response.json(shared)))
    vi.stubGlobal('fetch', fetchMock)
    await expect(getSharedEnv('site_1')).resolves.toEqual(shared)
    await updateSharedEnv('site_1', 'APP_ENV=production\n')
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/sites/site_1/deployment/env', {
      credentials: 'same-origin', headers: { Accept: 'application/json' },
    })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/sites/site_1/deployment/env', {
      method: 'PUT',
      body: JSON.stringify({ content: 'APP_ENV=production\n' }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('queues the node preparation without a request body', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ job: { id: 11, kind: 'deploy.prepare' } }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(prepareNode('site_1')).resolves.toEqual({ job: { id: 11, kind: 'deploy.prepare' } })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/deployment/prepare', {
      method: 'POST', credentials: 'same-origin', headers: { Accept: 'application/json' },
    })
  })

  it('surfaces the server refusal message', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ code: 'sftp_access_enabled', message: 'Disable SFTP for this site first.' }, { status: 409 })))
    await expect(enableSSHAccess('site_1')).rejects.toThrow('Disable SFTP for this site first.')
  })
})
