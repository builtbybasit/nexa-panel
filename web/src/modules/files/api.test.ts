import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  archiveEntries,
  copyEntry,
  deleteEntry,
  directorySize,
  downloadUrl,
  extractEntry,
  FilesRequestError,
  listFiles,
  makeDirectory,
  moveEntry,
  readFileContent,
  uploadFile,
  writeFileContent,
} from './api'

const entry = {
  name: 'index.php',
  kind: 'file',
  size: 512,
  mode: 'rw-r-----',
  owner: 'nexa_demo',
  group: 'www-data',
  modifiedAt: '2026-07-16T00:00:00Z',
}

const getInit = { credentials: 'same-origin', headers: { Accept: 'application/json' } }
const uploadChunkSize = 4 * 1024 * 1024

function jsonInit(method: string, body: unknown) {
  return {
    method,
    body: JSON.stringify(body),
    credentials: 'same-origin',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('files API', () => {
  it('lists and reads entries with the path pinned in the query string', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ entries: [entry], truncated: false }))
      .mockResolvedValueOnce(Response.json({ content: '<?php', etag: 'abc', size: 512, truncated: false, binary: false }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listFiles('site_1', 'public/app dir')).resolves.toEqual({ entries: [entry], truncated: false })
    await expect(readFileContent('site_1', 'public/index.php')).resolves.toEqual({
      content: '<?php',
      etag: 'abc',
      size: 512,
      truncated: false,
      binary: false,
    })
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/sites/site_1/files?path=public%2Fapp%20dir', getInit)
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/sites/site_1/files/content?path=public%2Findex.php', getInit)
  })

  it('saves content with the expected etag and surfaces 409 conflicts as typed errors', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ etag: 'def' }))
      .mockResolvedValueOnce(Response.json({ code: 'etag_conflict', message: 'The file changed on the server.' }, { status: 409 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(writeFileContent('site_1', { path: 'public/index.php', content: '<?php', expectedEtag: 'abc' })).resolves.toEqual({
      etag: 'def',
    })
    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/sites/site_1/files/content',
      jsonInit('PUT', { path: 'public/index.php', content: '<?php', expectedEtag: 'abc' }),
    )

    await expect(writeFileContent('site_1', { path: 'public/index.php', content: '<?php', expectedEtag: 'abc' })).rejects.toEqual(
      new FilesRequestError('The file changed on the server.', 409, 'etag_conflict'),
    )
  })

  it('builds a same-origin streaming download URL', () => {
    expect(downloadUrl('site_1', 'backups/site.tar.gz')).toBe('/api/v1/sites/site_1/files/download?path=backups%2Fsite.tar.gz')
  })

  it('creates directories and moves, copies, and deletes entries', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(makeDirectory('site_1', 'public/assets')).resolves.toBeUndefined()
    await expect(moveEntry('site_1', { from: 'public/a.txt', to: 'public/b.txt', overwrite: false })).resolves.toBeUndefined()
    await expect(copyEntry('site_1', { from: 'public/b.txt', to: 'tmp/b.txt' })).resolves.toBeUndefined()
    await expect(deleteEntry('site_1', { path: 'tmp/b.txt', recursive: false })).resolves.toBeUndefined()

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/sites/site_1/files/mkdir', jsonInit('POST', { path: 'public/assets' }))
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/sites/site_1/files/move',
      jsonInit('POST', { from: 'public/a.txt', to: 'public/b.txt', overwrite: false }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/v1/sites/site_1/files/copy',
      jsonInit('POST', { from: 'public/b.txt', to: 'tmp/b.txt' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      '/api/v1/sites/site_1/files/delete',
      jsonInit('POST', { path: 'tmp/b.txt', recursive: false }),
    )
  })

  it('uploads a file in sequential 4 MiB chunks with progress callbacks', async () => {
    const total = uploadChunkSize + 1024
    const file = new File([new Uint8Array(total)], 'big.bin')
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ uploadId: 'up_2' }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(Response.json(entry))
    vi.stubGlobal('fetch', fetchMock)

    const progress: [number, number][] = []
    await expect(uploadFile('site_1', file, 'public/big.bin', false, (sent, size) => progress.push([sent, size]))).resolves.toEqual(entry)

    expect(progress).toEqual([
      [0, total],
      [uploadChunkSize, total],
      [total, total],
    ])
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/sites/site_1/files/uploads/up_2?offset=0')
    expect(fetchMock.mock.calls[2]?.[0]).toBe(`/api/v1/sites/site_1/files/uploads/up_2?offset=${uploadChunkSize}`)
    expect((fetchMock.mock.calls[1]?.[1]?.body as Blob).size).toBe(uploadChunkSize)
    expect((fetchMock.mock.calls[2]?.[1]?.body as Blob).size).toBe(1024)
    expect(fetchMock.mock.calls[3]?.[0]).toBe('/api/v1/sites/site_1/files/uploads/up_2/commit')
  })

  it('aborts the staged upload when a chunk is rejected', async () => {
    const file = new File([new Uint8Array(8)], 'small.bin')
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ uploadId: 'up_3' }))
      .mockResolvedValueOnce(Response.json({ code: 'offset_mismatch', message: 'Chunk offset mismatch.' }, { status: 409 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(uploadFile('site_1', file, 'public/small.bin', false)).rejects.toEqual(
      new FilesRequestError('Chunk offset mismatch.', 409, 'offset_mismatch'),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/v1/sites/site_1/files/uploads/up_3', {
      method: 'DELETE',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  it('queues archive, extract, and size jobs', async () => {
    const job = { id: 9, kind: 'files.archive', state: 'queued' }
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(Response.json({ job }, { status: 202 })))
    vi.stubGlobal('fetch', fetchMock)

    await expect(archiveEntries('site_1', { paths: ['public/app'], target: 'backups/app.tar.gz' })).resolves.toEqual({ job })
    await expect(extractEntry('site_1', { path: 'backups/app.tar.gz', targetDir: 'tmp/app' })).resolves.toEqual({ job })
    await expect(directorySize('site_1', 'public')).resolves.toEqual({ job })

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/sites/site_1/files/archive',
      jsonInit('POST', { paths: ['public/app'], target: 'backups/app.tar.gz' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/sites/site_1/files/extract',
      jsonInit('POST', { path: 'backups/app.tar.gz', targetDir: 'tmp/app' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/v1/sites/site_1/files/size', jsonInit('POST', { path: 'public' }))
  })

  it('surfaces the server-safe error envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(Response.json({ code: 'outside_write_zone', message: 'Mutations are limited to public/, private/, tmp/, and backups/.' }, { status: 422 })),
    )
    await expect(makeDirectory('site_1', 'logs/new')).rejects.toEqual(
      new FilesRequestError('Mutations are limited to public/, private/, tmp/, and backups/.', 422, 'outside_write_zone'),
    )
  })
})
