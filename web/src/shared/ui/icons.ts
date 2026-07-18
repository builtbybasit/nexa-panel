/**
 * Icons are Lucide, served through Iconify's offline collection (registered in
 * `main.ts`). Feature modules reference icons by their Lucide kebab-case name
 * (e.g. `file-text`, `hard-drive`) so no SVG markup is ever embedded in views.
 *
 * `iconAliases` maps a handful of legacy names this codebase already used onto
 * the current Lucide name, so existing `<AppIcon name="…" />` call sites keep
 * working without a sweep.
 */
export const iconAliases: Record<string, string> = {
  stop: 'square',
}

/** Prefix an icon name with the Lucide collection, resolving legacy aliases. */
export function lucideName(name: string): string {
  return `lucide:${iconAliases[name] ?? name}`
}

/** Icon names are free-form Lucide identifiers. */
export type IconName = string
