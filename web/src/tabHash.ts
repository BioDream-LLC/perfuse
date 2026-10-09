// Every view's id, for reading one out of the URL. A string from the address bar is only trusted once it is in here.
export const TAB_IDS = [
  'dashboard', 'teams', 'channels', 'messages', 'queue', 'alerts', 'metrics', 'fhir', 'subscriptions', 'documents', 'payer',
  'cms0057', 'shl', 'scripts', 'certificates', 'shadow', 'migrate', 'playground', 'flow', 'contracts', 'fleet', 'tefca', 'tables',
  'mapper', 'users', 'audit', 'settings',
] as const

// viewFromHash reads a view's id out of the address bar (#/<id>), or null when it names no view.
export function viewFromHash(hash: string): (typeof TAB_IDS)[number] | null {
  const id = /^#\/([a-z0-9-]+)$/.exec(hash)?.[1]
  return id && (TAB_IDS as readonly string[]).includes(id) ? (id as (typeof TAB_IDS)[number]) : null
}
