import { describe, expect, it } from 'vitest'

import type { FileEntry } from './api'
import {
  crumbsOf,
  entryTypeLabel,
  freeName,
  isWritablePath,
  joinPath,
  parentOf,
  symbolicToOctal,
} from './lib'

function entry(overrides: Partial<FileEntry> = {}): FileEntry {
  return {
    name: 'index.html',
    kind: 'file',
    size: 21,
    mode: 'rw-r--r--',
    owner: 'site_usr',
    group: 'site_usr',
    modifiedAt: '2026-01-28T15:06:00Z',
    ...overrides,
  }
}

describe('paths', () => {
  it('joins against the site root without a leading dot', () => {
    expect(joinPath('.', 'public')).toBe('public')
    expect(joinPath('public', 'index.html')).toBe('public/index.html')
  })

  it('walks up to the root and stops there', () => {
    expect(parentOf('public/assets/img')).toBe('public/assets')
    expect(parentOf('public')).toBe('.')
    expect(parentOf('.')).toBe('.')
  })

  it('builds cumulative crumbs', () => {
    expect(crumbsOf('public/assets')).toEqual([
      { name: 'public', path: 'public' },
      { name: 'assets', path: 'public/assets' },
    ])
    expect(crumbsOf('.')).toEqual([])
  })

  it('only treats the four managed zones as writable', () => {
    expect(isWritablePath('public/assets')).toBe(true)
    expect(isWritablePath('backups')).toBe(true)
    expect(isWritablePath('logs/nginx')).toBe(false)
    expect(isWritablePath('.')).toBe(false)
    // A directory that merely starts with a zone name is not that zone.
    expect(isWritablePath('publicity')).toBe(false)
  })
})

describe('free names', () => {
  it('returns the name unchanged when nothing claims it', () => {
    expect(freeName('report.txt', new Set(['other.txt']))).toBe('report.txt')
  })

  it('numbers copies past every taken variant', () => {
    expect(freeName('report.txt', new Set(['report.txt']))).toBe('report (copy).txt')
    expect(freeName('report.txt', new Set(['report.txt', 'report (copy).txt']))).toBe('report (copy 2).txt')
    expect(freeName('assets', new Set(['assets', 'assets (copy)', 'assets (copy 2)']))).toBe('assets (copy 3)')
  })

  it('suffixes before the extension so the copy stays the same file type', () => {
    // Multi-part extensions stay together and a dotfile keeps its leading dot.
    expect(freeName('site.tar.gz', new Set(['site.tar.gz']))).toBe('site (copy).tar.gz')
    expect(freeName('.htaccess', new Set(['.htaccess']))).toBe('.htaccess (copy)')
  })
})

describe('entry presentation', () => {
  it('converts symbolic modes to octal and rejects malformed ones', () => {
    expect(symbolicToOctal('rwxr-x---')).toBe('750')
    expect(symbolicToOctal('rw-r--r--')).toBe('644')
    expect(symbolicToOctal('nonsense')).toBe('')
  })

  it('labels archives by shape rather than by generic extension', () => {
    expect(entryTypeLabel(entry({ name: 'site.tar.gz' }))).toBe('Archive')
    expect(entryTypeLabel(entry({ name: 'photo.png' }))).toBe('Image')
    expect(entryTypeLabel(entry({ name: 'index.html' }))).toBe('HTML')
    expect(entryTypeLabel(entry({ name: 'public', kind: 'dir' }))).toBe('Folder')
    expect(entryTypeLabel(entry({ name: 'notes.markdown' }))).toBe('File')
  })
})
