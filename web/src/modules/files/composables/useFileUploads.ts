import { computed, markRaw, ref, type Ref } from 'vue'

import { uploadFile } from '../api'
import { isWritablePath, joinPath } from '../lib'

export interface UploadItem {
  file: File
  targetPath: string
  sent: number
  total: number
  status: 'uploading' | 'done' | 'failed'
  error: string
}

interface Options {
  siteId: Ref<string>
  path: Ref<string>
  canWriteFiles: Ref<boolean>
  /** False in read-only trees and while the editor owns the surface. */
  canDrop: Ref<boolean>
  onUploaded: () => Promise<void> | void
}

/**
 * The upload tray: a sequential queue of chunked transfers plus the drag-and-drop
 * state of the listing they land in. Uploads are deliberately serial so a folder
 * dropped onto the panel cannot open dozens of parallel chunk streams.
 */
export function useFileUploads({ siteId, path, canWriteFiles, canDrop, onUploaded }: Options) {
  const uploads = ref<UploadItem[]>([])

  async function start(item: UploadItem, overwrite: boolean) {
    if (!canWriteFiles.value || !isWritablePath(item.targetPath)) {
      item.status = 'failed'
      item.error = 'Your account cannot upload to this path.'
      return
    }
    item.status = 'uploading'
    item.error = ''
    item.sent = 0
    try {
      await uploadFile(siteId.value, item.file, item.targetPath, overwrite, (sent, total) => {
        item.sent = sent
        item.total = total
      })
      item.status = 'done'
      await onUploaded()
    } catch (caught) {
      item.status = 'failed'
      item.error = caught instanceof Error ? caught.message : 'The upload failed.'
    }
  }

  async function queue(files: File[]) {
    if (!canDrop.value) return
    // Pinned to the directory the drop happened in, so navigating away mid-queue
    // does not redirect the remaining files.
    const directory = path.value
    const queued: UploadItem[] = []
    for (const file of files) {
      uploads.value.push({
        file: markRaw(file),
        targetPath: joinPath(directory, file.name),
        sent: 0,
        total: file.size,
        status: 'uploading',
        error: '',
      })
      const item = uploads.value[uploads.value.length - 1]
      if (item) queued.push(item)
    }
    for (const item of queued) await start(item, false)
  }

  const retry = (item: UploadItem) => start(item, true)

  function percent(item: UploadItem): number {
    if (item.status === 'done') return 100
    return Math.round((item.sent / Math.max(item.total, 1)) * 100)
  }

  /** Removes everything that is no longer transferring, failed rows included. */
  function clearFinished() {
    uploads.value = uploads.value.filter((item) => item.status === 'uploading')
  }

  function dismiss(item: UploadItem) {
    uploads.value = uploads.value.filter((candidate) => candidate !== item)
  }

  // --- Drag-and-drop onto the listing ---

  const dragDepth = ref(0)
  const dragActive = computed(() => dragDepth.value > 0 && canDrop.value)

  function hasFiles(event: DragEvent): boolean {
    return Array.from(event.dataTransfer?.types ?? []).includes('Files')
  }

  /** Keyed for `v-on="dragHandlers"`, which expects bare event names. */
  const dragHandlers = {
    dragenter(event: DragEvent) {
      if (!hasFiles(event)) return
      event.preventDefault()
      dragDepth.value += 1
    },
    dragover(event: DragEvent) {
      if (!hasFiles(event)) return
      event.preventDefault()
      if (event.dataTransfer) event.dataTransfer.dropEffect = canDrop.value ? 'copy' : 'none'
    },
    dragleave(event: DragEvent) {
      if (!hasFiles(event)) return
      dragDepth.value = Math.max(0, dragDepth.value - 1)
    },
    drop(event: DragEvent) {
      if (!hasFiles(event)) return
      event.preventDefault()
      dragDepth.value = 0
      const files = Array.from(event.dataTransfer?.files ?? [])
      if (files.length) void queue(files)
    },
  }

  return { uploads, queue, retry, dismiss, clearFinished, percent, dragActive, dragHandlers }
}
