import { useEffect, useRef, useState } from 'react'
import { viewIcons } from './Icons'
import type { Tab } from './App'

/**
 * The header navigation, grouped.
 *
 * There are twenty-two views. They used to be shown as the first seven followed by a menu called
 * "More" holding the other fifteen in declaration order, which meant the answer to "where do I look
 * for the thing I need" was to open More and read all of it. The order carried no meaning, so nothing
 * could be learnt and the list had to be re-read every time.
 *
 * Grouped by the question being asked instead: whether the feed is healthy, how to change what it
 * does, which standards it speaks, and who may operate it. Two views stay directly in the bar because
 * they are where work starts and putting them behind a menu costs a click on every visit.
 *
 * Groups also mean a view can be found without knowing its name. Somebody looking for message replay
 * does not know it is called Messages, but they do know they are trying to find out what happened.
 */

/** NavGroup is one dropdown in the header. */
export interface NavGroup {
  /** name is the button label, and is what the group teaches. Kept to one word so the bar stays a bar. */
  name: string
  /** about explains the group itself, shown as the menu's own heading rather than only as a tooltip. */
  about: string
  /**
   * accent is a Tailwind colour family.
   *
   * Written out as whole class names rather than assembled from the family, because Tailwind scans the
   * source for literals and a constructed class name is not in the stylesheet at all - which fails as
   * an unstyled menu rather than as a build error.
   */
  accent: {
    /** text is the group name when its view is selected, and the menu heading. */
    text: string
    /** activeBg tints the trigger when the current view lives in this group. */
    activeBg: string
    /** ring is the menu's top border, so an open menu is visibly tied to the button that opened it. */
    ring: string
    /** dot marks the selected item inside the menu. */
    dot: string
  }
  /** members are view ids, in the order they should be read. */
  members: Tab[]
}

/**
 * NAV_GROUPS defines the bar.
 *
 * Every view must appear here or directly in the bar, and a test asserts it: a view added to the tab
 * list and forgotten here would not be missing from the interface, which would be noticed, but would
 * silently fall to the end of the last group, which would not.
 */
export const NAV_GROUPS: NavGroup[] = [
  {
    name: 'Monitor',
    about: 'Whether it is working, and what happened',
    accent: {
      text: 'text-sky-300',
      activeBg: 'bg-sky-500/15 text-sky-200',
      ring: 'border-t-sky-500/60',
      dot: 'bg-sky-400',
    },
    // Messages before Queue before Alerts: the order somebody follows when a feed is reported broken.
    members: ['messages', 'queue', 'alerts', 'metrics', 'flow'],
  },
  {
    name: 'Build',
    about: 'Change what a channel does, and try it before it is live',
    accent: {
      text: 'text-violet-300',
      activeBg: 'bg-violet-500/15 text-violet-200',
      ring: 'border-t-violet-500/60',
      dot: 'bg-violet-400',
    },
    members: ['scripts', 'contracts', 'tables', 'mapper', 'playground', 'shadow'],
  },
  {
    name: 'Exchange',
    about: 'The standards this server speaks to other organisations',
    accent: {
      text: 'text-emerald-300',
      activeBg: 'bg-emerald-500/15 text-emerald-200',
      ring: 'border-t-emerald-500/60',
      dot: 'bg-emerald-400',
    },
    members: ['fhir', 'documents', 'tefca'],
  },
  {
    name: 'Administer',
    about: 'Who may use this, what it trusts, and how it is set up',
    accent: {
      text: 'text-amber-300',
      activeBg: 'bg-amber-500/15 text-amber-200',
      ring: 'border-t-amber-500/60',
      dot: 'bg-amber-400',
    },
    members: ['users', 'certificates', 'audit', 'fleet', 'migrate', 'settings'],
  },
]

/**
 * DIRECT_TABS stay in the bar rather than in a group.
 *
 * Two, not five. Every one added is a group name pushed off the visible bar on a narrow window, and
 * the grouping is the thing being added here.
 */
export const DIRECT_TABS: Tab[] = ['dashboard', 'channels']

function TabIcon({ label }: { label: string }) {
  const Icon = viewIcons[label]
  if (!Icon) return null
  return <Icon size={15} />
}

export interface NavItem {
  id: Tab
  label: string
  about: string
}

/**
 * GroupMenu is one grouped dropdown.
 *
 * A menu button rather than more tabs, because ARIA allows a tablist to contain only tabs - a trigger
 * inside one is invalid and assistive technology may skip it or report it as a tab that cannot be
 * selected. So the groups sit beside the tablist and the selected view is mirrored into it as a
 * visually hidden tab, which is what keeps the tablist from reporting no selection at all.
 */
export function GroupMenu({
  group,
  items,
  current,
  onSelect,
}: {
  group: NavGroup
  items: NavItem[]
  current: Tab
  onSelect: (id: Tab) => void
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const itemRefs = useRef<(HTMLButtonElement | null)[]>([])

  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  // Focus the first item on open, or a keyboard user opens the menu and their focus is still behind it.
  // The selected item is focused instead when there is one, so returning to a group you are already in
  // starts where you are rather than at the top.
  useEffect(() => {
    if (!open) return
    const selected = items.findIndex((t) => t.id === current)
    itemRefs.current[selected >= 0 ? selected : 0]?.focus()
  }, [open, items, current])

  /** close returns focus to the trigger, so dismissing the menu does not strand the user. */
  const close = () => {
    setOpen(false)
    triggerRef.current?.focus()
  }

  const selected = items.find((t) => t.id === current)

  const onItemKeyDown = (e: React.KeyboardEvent, i: number) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      close()
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const next = e.key === 'ArrowDown' ? i + 1 : i - 1
      itemRefs.current[(next + items.length) % items.length]?.focus()
      return
    }
    if (e.key === 'Home') {
      e.preventDefault()
      itemRefs.current[0]?.focus()
      return
    }
    if (e.key === 'End') {
      e.preventDefault()
      itemRefs.current[items.length - 1]?.focus()
    }
  }

  // Nothing to show rather than an empty menu. A viewer sees no Administer group at all, instead of one
  // that opens onto nothing and reads as broken.
  if (items.length === 0) return null

  return (
    <div className="relative" ref={ref}>
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen((o) => !o)}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && open) {
            e.preventDefault()
            close()
          }
          if (e.key === 'ArrowDown' && !open) {
            e.preventDefault()
            setOpen(true)
          }
        }}
        className={`inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium transition ${
          selected
            ? group.accent.activeBg
            : 'text-slate-400 hover:bg-slate-900 hover:text-slate-200'
        }`}
        aria-expanded={open}
        aria-haspopup="menu"
        // The group name and the selected view, both. The old control replaced "More" with the view
        // name, which named the selection but lost the group - so the bar stopped saying where you
        // were. Naming both is what makes the grouping learnable by using it.
        aria-label={selected ? `${group.name} views, ${selected.label} selected` : `${group.name} views`}
      >
        {group.name}
        {selected && (
          <>
            <span aria-hidden="true" className="text-slate-500">
              ·
            </span>
            <span aria-hidden="true">{selected.label}</span>
          </>
        )}
        <span className="text-[10px] opacity-70" aria-hidden="true">
          ▾
        </span>
      </button>

      {open && (
        <div
          role="menu"
          aria-label={`${group.name} views`}
          className={`absolute right-0 top-full z-50 mt-1 w-72 rounded-xl border border-slate-800 border-t-2 ${group.accent.ring} bg-slate-950/95 p-1.5 shadow-xl shadow-black/40 backdrop-blur-xl`}
        >
          {/*
            The group explains itself here rather than in a tooltip on the trigger. A tooltip is not
            reachable by keyboard, never appears on a touch screen, and is the wrong place for the one
            sentence that says what the whole group is for.
          */}
          <p
            className={`px-3 pb-1.5 pt-1 text-[11px] font-semibold uppercase tracking-wider ${group.accent.text}`}
          >
            {group.name}
          </p>
          <p className="border-b border-slate-800/80 px-3 pb-2 text-xs leading-snug text-slate-400">
            {group.about}
          </p>

          <div className="pt-1.5">
            {items.map((t, i) => (
              <button
                key={t.id}
                ref={(el) => {
                  itemRefs.current[i] = el
                }}
                type="button"
                role="menuitem"
                aria-current={current === t.id ? 'true' : undefined}
                onKeyDown={(e) => onItemKeyDown(e, i)}
                onClick={() => {
                  onSelect(t.id)
                  setOpen(false)
                }}
                className={`flex w-full items-start gap-2.5 rounded-lg px-3 py-2 text-left transition ${
                  current === t.id
                    ? 'bg-slate-800 text-slate-100'
                    : 'text-slate-300 hover:bg-slate-800/60 hover:text-slate-100'
                }`}
              >
                <span className="mt-0.5 shrink-0">
                  <TabIcon label={t.label} />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-1.5 text-sm font-medium">
                    {t.label}
                    {current === t.id && (
                      <span
                        aria-hidden="true"
                        className={`h-1.5 w-1.5 shrink-0 rounded-full ${group.accent.dot}`}
                      />
                    )}
                  </span>
                  {/*
                    Each view says what it is for, in the menu, where the choice is being made. This
                    text already existed as a title attribute on every tab and was therefore invisible
                    to most of the people who needed it.
                  */}
                  <span className="mt-0.5 block text-xs leading-snug text-slate-500">{t.about}</span>
                </span>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
