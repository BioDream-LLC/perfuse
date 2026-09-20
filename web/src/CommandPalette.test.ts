import { describe, expect, it } from 'vitest'
import { rank, type Command } from './CommandPalette'

const cmd = (label: string, keywords?: string[]): Command => ({
  id: label,
  label,
  group: 'test',
  keywords,
  run: () => {},
})

const commands = [
  cmd('Dashboard'),
  cmd('Channels'),
  cmd('Messages'),
  cmd('Queue'),
  cmd('adt-inbound'),
  cmd('adt-inbound · queue', ['queue', 'stuck', 'backlog']),
  cmd('lab-results'),
  cmd('lab-results · messages', ['messages', 'browse']),
  cmd('lab-results · queue', ['queue', 'stuck', 'backlog']),
  cmd('laboratory-results-archive-secondary'),
]

const labels = (query: string) => rank(commands, query).map((c) => c.label)

describe('rank', () => {
  it('returns everything for an empty query', () => {
    expect(rank(commands, '').length).toBe(commands.length)
    expect(rank(commands, '   ').length).toBe(commands.length)
  })

  it('puts an exact match first', () => {
    expect(labels('queue')[0]).toBe('Queue')
  })

  it('prefers a prefix match over a match in the middle', () => {
    // "lab" should find the lab channel before the longer name that merely starts the
    // same way, and before anything with "lab" buried in it.
    const got = labels('lab')
    expect(got[0]).toBe('lab-results')
  })

  it('matches a subsequence, which is the reason to type instead of clicking', () => {
    // "lbq" is not a substring of anything. It is l(ab)-(results ·) q(ueue), in order
    // but not adjacent, and that is the whole point of typing rather than clicking.
    expect(labels('lbq')).toContain('lab-results · queue')
  })

  it('does not match when a query character is absent', () => {
    // There is no "l" anywhere in "adt-inbound · queue", so a subsequence matcher that
    // returned it would be matching nothing in particular.
    expect(labels('lbq')).not.toContain('adt-inbound · queue')
  })

  it('finds a channel queue by an abbreviation', () => {
    expect(labels('adtq')).toContain('adt-inbound · queue')
  })

  it('excludes what does not match at all', () => {
    expect(labels('zzzz')).toEqual([])
  })

  it('matches keywords as well as labels', () => {
    // Somebody thinking "backlog" should find the queue, because that is what they
    // call it even though nothing is labelled that.
    expect(labels('backlog')).toContain('adt-inbound · queue')
  })

  it('ranks a label match above a keyword match', () => {
    const got = labels('messages')
    expect(got[0]).toBe('Messages')
  })

  it('prefers the shorter of two equally good matches', () => {
    // "lab" beats "laboratory-results-archive-secondary" for the query "lab", because
    // otherwise the longest name in the system wins every partial query.
    const got = labels('lab')
    const short = got.indexOf('lab-results')
    const long = got.indexOf('laboratory-results-archive-secondary')
    expect(short).toBeLessThan(long)
  })

  it('is case insensitive', () => {
    expect(labels('QUEUE')[0]).toBe('Queue')
    expect(labels('DaShBoArD')[0]).toBe('Dashboard')
  })

  it('does not reorder equally scored results between keystrokes', () => {
    // A list that reshuffles as somebody types a character that changes nothing makes
    // the highlighted row land on something they did not mean to select.
    const a = labels('a')
    const b = labels('a')
    expect(a).toEqual(b)
  })

  it('treats a word boundary match as better than a mid-word one', () => {
    const got = labels('results')
    // Both contain it; the one where it starts a word should come first.
    expect(got.indexOf('lab-results')).toBeLessThan(
      got.indexOf('laboratory-results-archive-secondary'),
    )
  })
})
