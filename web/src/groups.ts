import type { Role } from './api'

const ROLES: Role[] = ['monitor', 'viewer', 'operator', 'editor', 'admin']

/** Reads "lab=editor, adt": channel groups, each with an optional role of its own. */
export function parseGroups(text: string): { groups: string[]; roles: Record<string, Role>; bad?: string } {
  const groups: string[] = []
  const roles: Record<string, Role> = {}
  for (const part of text.split(',')) {
    const [name, role] = part.split('=').map((v) => v.trim())
    if (!name) continue
    if (!groups.includes(name)) groups.push(name)
    if (role !== undefined && role !== '') {
      if (!ROLES.includes(role as Role)) return { groups, roles, bad: `${role} is not a role (${ROLES.join(', ')})` }
      roles[name] = role as Role
    }
  }
  return { groups, roles }
}

/** Writes what parseGroups reads. */
export function formatGroups(groups: string[] = [], roles: Record<string, Role> = {}): string {
  return groups.map((g) => (roles[g] ? `${g}=${roles[g]}` : g)).join(', ')
}
