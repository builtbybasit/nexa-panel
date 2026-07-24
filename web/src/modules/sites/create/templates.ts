/**
 * The site templates the wizard offers. Exactly one is provisionable today —
 * the PHP stack the backend plans and activates. The rest are real ambitions
 * with no backend yet, shown disabled so the catalog communicates direction
 * without promising what the panel cannot do. Adding a stack later means
 * adding its backend and flipping `available` here.
 */
export interface SiteTemplate {
  id: 'php' | 'wordpress' | 'nodejs' | 'static' | 'proxy'
  name: string
  tagline: string
  /** Badge icon from the shared registry. */
  icon: string
  available: boolean
}

export const SITE_TEMPLATES: SiteTemplate[] = [
  {
    id: 'php',
    name: 'PHP',
    tagline: 'PHP-FPM behind Nginx — fits Laravel, Symfony, and most classic apps.',
    icon: 'file-code-2',
    available: true,
  },
  {
    id: 'wordpress',
    name: 'WordPress',
    tagline: 'A CMS site in a few clicks with WordPress preinstalled.',
    icon: 'blocks',
    available: false,
  },
  {
    id: 'nodejs',
    name: 'Node.js',
    tagline: 'Run a modern JavaScript app behind an Nginx front.',
    icon: 'hexagon',
    available: false,
  },
  {
    id: 'static',
    name: 'Static',
    tagline: 'Plain HTML, CSS, and JS served straight from disk.',
    icon: 'app-window',
    available: false,
  },
  {
    id: 'proxy',
    name: 'Reverse proxy',
    tagline: 'Forward requests to a service running somewhere else.',
    icon: 'arrow-left-right',
    available: false,
  },
]
