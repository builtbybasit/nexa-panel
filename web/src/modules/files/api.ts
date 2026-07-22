import type { Job } from '../jobs/api'
import { apiRequest } from '@/shared/api/request'

export type EntryKind = 'file' | 'dir' | 'symlink' | 'other'

export interface FileEntry {
  name: string
  kind: EntryKind
  size: number
  /** Symbolic permission string such as `rwxr-x---`. */
  mode: string
  owner: string
  group: string
  modifiedAt: string
}

export interface FileListing {
  entries: FileEntry[]
  /** True when the server capped the listing (5000 entries). */
  truncated: boolean
}

export interface FileContent {
  /** Present only when the file is neither binary nor truncated. */
  content: string
  /** sha256 of the full file content; used for optimistic-concurrency saves. */
  etag: string
  size: number
  truncated: boolean
  binary: boolean
}

/** Result payload of a succeeded `files.size` job. */
export interface DirectorySizeResult {
  bytes: number
  files: number
  dirs: number
  truncated: boolean
}

export class FilesRequestError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message)
  }
}

function base(siteId: string): string {
  return `/api/v1/sites/${encodeURIComponent(siteId)}/files`
}

function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  return apiRequest<T>(path, init, {
    errorPrefix: 'Files request',
    createError: (message, status, code) => new FilesRequestError(message, status, code),
  })
}

export function listFiles(siteId: string, path: string): Promise<FileListing> {
  return request(`${base(siteId)}?path=${encodeURIComponent(path)}`)
}

export function readFileContent(siteId: string, path: string): Promise<FileContent> {
  return request(`${base(siteId)}/content?path=${encodeURIComponent(path)}`)
}

/** Save file content; `expectedEtag: ''` requires the file to not exist yet. Rejects 409 on conflict. */
export function writeFileContent(siteId: string, input: { path: string; content: string; expectedEtag: string }): Promise<{ etag: string }> {
  return request(`${base(siteId)}/content`, { method: 'PUT', body: JSON.stringify(input) })
}

/** Same-origin streaming download URL for a plain `<a href>` (server sets Content-Disposition). */
export function downloadUrl(siteId: string, path: string): string {
  return `${base(siteId)}/download?path=${encodeURIComponent(path)}`
}

export function makeDirectory(siteId: string, path: string): Promise<void> {
  return request(`${base(siteId)}/mkdir`, { method: 'POST', body: JSON.stringify({ path }) })
}

export function moveEntry(siteId: string, input: { from: string; to: string; overwrite: boolean }): Promise<void> {
  return request(`${base(siteId)}/move`, { method: 'POST', body: JSON.stringify(input) })
}

export function copyEntry(siteId: string, input: { from: string; to: string }): Promise<void> {
  return request(`${base(siteId)}/copy`, { method: 'POST', body: JSON.stringify(input) })
}

/** Change permissions; `mode` is three octal digits such as `"755"`. Returns the updated entry. */
export function changeMode(siteId: string, input: { path: string; mode: string }): Promise<FileEntry> {
  return request(`${base(siteId)}/chmod`, { method: 'POST', body: JSON.stringify(input) })
}

export function deleteEntry(siteId: string, input: { path: string; recursive: boolean }): Promise<void> {
  return request(`${base(siteId)}/delete`, { method: 'POST', body: JSON.stringify(input) })
}

function beginUpload(siteId: string, input: { path: string; size: number; overwrite: boolean }): Promise<{ uploadId: string }> {
  return request(`${base(siteId)}/uploads`, { method: 'POST', body: JSON.stringify(input) })
}

/** Append one raw chunk at `offset`; the server verifies the offset matches the staged size. */
async function uploadChunk(siteId: string, uploadId: string, offset: number, chunk: Blob): Promise<void> {
  const response = await fetch(`${base(siteId)}/uploads/${encodeURIComponent(uploadId)}?offset=${offset}`, {
    method: 'PUT',
    credentials: 'same-origin',
    headers: { Accept: 'application/json', 'Content-Type': 'application/octet-stream' },
    body: chunk,
  })
  if (!response.ok) {
    const error = (await response.json().catch(() => null)) as { code?: string; message?: string } | null
    throw new FilesRequestError(error?.message ?? `Files upload failed with status ${response.status}`, response.status, error?.code)
  }
}

function commitUpload(siteId: string, uploadId: string): Promise<FileEntry> {
  return request(`${base(siteId)}/uploads/${encodeURIComponent(uploadId)}/commit`, { method: 'POST' })
}

function abortUpload(siteId: string, uploadId: string): Promise<void> {
  return request(`${base(siteId)}/uploads/${encodeURIComponent(uploadId)}`, { method: 'DELETE' })
}

/** 4 MiB chunks keep each PUT well under the server's per-chunk cap. */
const UPLOAD_CHUNK_SIZE = 4 * 1024 * 1024

/**
 * Chunked upload session: begin, PUT sequential slices, commit. The staging
 * upload is aborted (best effort) when any step fails.
 */
export async function uploadFile(
  siteId: string,
  file: File,
  targetPath: string,
  overwrite: boolean,
  onProgress?: (sentBytes: number, totalBytes: number) => void,
): Promise<FileEntry> {
  const { uploadId } = await beginUpload(siteId, { path: targetPath, size: file.size, overwrite })
  try {
    onProgress?.(0, file.size)
    let offset = 0
    while (offset < file.size) {
      const chunk = file.slice(offset, Math.min(offset + UPLOAD_CHUNK_SIZE, file.size))
      await uploadChunk(siteId, uploadId, offset, chunk)
      offset += chunk.size
      onProgress?.(offset, file.size)
    }
    return await commitUpload(siteId, uploadId)
  } catch (caught) {
    await abortUpload(siteId, uploadId).catch(() => undefined)
    throw caught
  }
}

export function archiveEntries(siteId: string, input: { paths: string[]; target: string }): Promise<{ job: Job }> {
  return request(`${base(siteId)}/archive`, { method: 'POST', body: JSON.stringify(input) })
}

export function extractEntry(siteId: string, input: { path: string; targetDir: string }): Promise<{ job: Job }> {
  return request(`${base(siteId)}/extract`, { method: 'POST', body: JSON.stringify(input) })
}

export function directorySize(siteId: string, path: string): Promise<{ job: Job }> {
  return request(`${base(siteId)}/size`, { method: 'POST', body: JSON.stringify({ path }) })
}
