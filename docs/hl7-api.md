# The public HL7 API — expansion plan

Status: plan, not built. Written 2026-08-21 because `internal/hl7` should be importable, and the moment you
publish a package path you have made promises you cannot take back.

## The counter-intuitive part, first

The instinct when publishing a library is to add the extension points now — options, callbacks, hooks — so you
never have to break anyone later. **That is the wrong move, and Go gives us something better.**

Speculative extension points are permanent maintenance. A callback nobody asked for still has to keep working, keep
being documented, and keep being fired at the right moment for the rest of the package's life. You end up
maintaining guesses.

What we need is not every hook up front. It is the *ability to add any hook later without breaking anyone*. In Go
that is one change:

```go
func Parse(raw []byte, opts ...Option) (*Message, error)
```

`Parse(raw)` still compiles. Every option we invent for the next ten years slots in without a second breaking
change. **This single signature is the whole insurance policy**, and it is the one thing that genuinely must happen
before publication rather than after.

So the plan below is deliberately thin on features and specific about doors.

---

## Tier 0 — one-way doors, must be settled before the first tag

### 0.1 Variadic options on every constructor-shaped function

`Parse`, `ParseString`, and the streaming entry point when it exists. `AckFor` already takes an `AckOptions`
struct, which is a different and also valid pattern — but it should not stay inconsistent with `Parse`, so pick one
and apply it. Recommendation: functional options for `Parse` (the input is one required argument plus a long tail
of rare knobs) and leave `AckFor`'s struct alone (its options are mostly required together).

Doing this after publication technically breaks anyone who took a function value:

```go
var f func([]byte) (*Message, error) = hl7.Parse   // stops compiling
```

Rare, but free to avoid by doing it now.

### 0.2 Decide the copy semantics — the one real hazard

`Parse` does not copy its input, and `Message` holds a pointer into the caller's slice. `Raw()` documents this;
`Parse` does not.

**Inside Perfuse this is correct and is why the parser is fast.** Published as a library it becomes a trap: the
obvious way to read messages is into a reused buffer, and the failure is silent, data-dependent and
non-deterministic. That is the worst class of bug to hand a stranger, and in this domain the corrupted thing is a
patient record.

Three options, and this one is a maintainer's call because it trades the headline benchmark against safety:

| | |
| --- | --- |
| **A. Zero-copy stays default** | Keeps the performance story. Requires the warning to move onto `Parse` itself, plus `WithCopy()` and `Message.Clone()`. |
| **B. Copy by default** | Safe for a newcomer. Costs the three-allocation claim, which is a real selling point. |
| **C. Copy by default, `WithZeroCopy()` to opt in** | Safe by default, fast on request. Perfuse's own engine passes the flag. |

**Recommendation: C.** It matches the house rule that a feature which cannot work yet is refused rather than
silently wrong — the same instinct applied to memory. A library's default should be the one that cannot corrupt
data, and anybody chasing 400 MB/s is reading the documentation anyway. Perfuse itself loses nothing: it opts in on
one line.

### 0.3 The import path

`github.com/biodream-llc/perfuse/hl7`, root level, not `pkg/hl7`. The `pkg/` convention has fallen out of favour and adds
a segment that means nothing. `mllp` should move at the same time and for the same reason — a parser without a
transport is half an answer for anyone reading from a network, and doing both at once means one move rather than
two.

Verified as trivial: `internal/hl7` imports **nothing outside the standard library** and depends on no other
Perfuse package. It can move as-is.

### 0.4 Errors, while they are still cheap to change

Two sentinels exist and are correct: `ErrNotHL7`, `ErrShortHeader`, both `errors.New`, so `errors.Is` works and
must keep working forever.

Add before publishing: a `*ParseError` carrying byte offset and segment name for anything the lenient path
tolerates. Retro-fitting position information into an error type after people are matching on it is unpleasant.

### 0.5 Two small omissions that will be asked for immediately

- `Message.Version()` — MSH-12. Trivial, and its absence is conspicuous next to `Type()` and `ControlID()`.
- `Unescape` is exported but takes `Separators`; there should be a `Message`-scoped convenience so the common case
  does not require plumbing separators by hand.

---

## Tier 1 — real capability, all safely additive once 0.1 lands

Ordered by how often someone will actually want it.

### 1.1 Streaming a batch file — the "callback" that genuinely matters

Multi-gigabyte HL7 files are ordinary. Today the only entry point takes a `[]byte`, so the whole file must be in
memory.

Go 1.23 range-over-func makes this idiomatic rather than a callback:

```go
for msg, err := range hl7.All(r) {   // iter.Seq2[*Message, error]
    ...
}
```

An `io.Reader` entry point is the single biggest practical gain and it composes with everything — files, network,
gzip, S3 — without the package knowing about any of them.

### 1.2 Walk — one callback that pays for itself

```go
func (m *Message) Walk(fn func(Path, Value) error) error
```

De-identification, profiling, mapping and diffing all want "visit every populated field".

**Checked rather than assumed, and the case is weaker than it first looks.** `profile` and `deident` do traverse
messages, but via `SegmentCount` / `SegmentAt` / `ValueAt` — index loops, not a shared walker — and nothing outside
the parser calls `Segments()` except `internal/api/runtime.go`. So this is not extracting an existing pattern that
appears three times; it is proposing a better one to replace two hand-rolled loops.

That is still worth doing, and the index loops are evidence the need is real, but it is Tier 1 on merit rather than
a free refactor. Worth building for Perfuse's own use first and publishing once it has earned its shape.

### 1.3 Mutation and building — the biggest gap, and the biggest design job

The package is **read-only**. No `Set`, no `NewMessage`, no builder. A library user hits this within an hour, and
"parse but never produce" is a strange shape for an HL7 library.

This needs its own design pass, not a bullet point. The hard questions:

- Does `Set` re-index eagerly, lazily, or return a new `Message`?
- Copy-on-write, given 0.2's zero-copy decision?
- Does building share a type with parsing, or is `Builder` separate?
- Escaping on write is where correctness bugs live — `Set` must escape, and must not double-escape.

Deliberately **after** the first tag. It is additive, so publishing without it costs nothing, and getting it wrong
in public costs a lot.

### 1.4 Options worth having, none urgent

All addable at any time thanks to 0.1, which is the point of 0.1:

- `WithSeparators` — a malformed MSH that a site insists on sending anyway.
- `WithSegmentTerminator` — for the senders that use something exotic.
- `WithMaxSize`, `WithMaxSegments` — **anything network-facing needs these.** A parser reachable from a socket
  without a size bound is a denial-of-service waiting to happen, and library users will point it at sockets.
- `WithStrict` — reject what is currently tolerated, for people validating rather than routing.
- `WithOnWarning(func(Warning))` — collect tolerated oddities instead of discarding them. This is the honest
  version of "callbacks": it exists because lenient parsing currently throws information away.

---

## Tier 2 — only on request, with a reason

- **Message structure awareness** (segment groups for `ADT_A01` and friends). Needs the structure tables, which is
  a large body of data and a versioning problem. Do not start it speculatively.
- **Batch headers** (FHS/BHS).
- **Code table lookups** — belongs in `codeset`, not the parser. Naming it here so it stays out.

---

## Rules the package lives by, to be enforced mechanically

House style is drift guards, and a published API is exactly where one belongs. Tests that must exist before the
first tag:

1. **Zero dependencies.** A test failing if the package imports anything outside the standard library. Currently
   true, and it is a headline claim, so it should be impossible to break by accident.
2. **No internal imports.** A public package that reaches into `internal/` drags private types into a public
   promise. Fail the build.
3. **No exported function returns an unexported or internal type.**
4. **Sentinel errors stay comparable.** A test asserting `errors.Is` against each one, so nobody "improves" them
   into wrapped types.
5. **The benchmarks are part of the contract in spirit.** Not a pass/fail gate — benchmark thresholds in CI are
   flaky — but a recorded number in the package documentation, so a regression is visible rather than discovered.

## What this plan deliberately does not do

It does not add a single callback, hook or option that no caller has asked for. Tier 0 buys the right to add all of
them later; spending that right in advance, on guesses, is how a small clean package becomes a large apologetic
one.

The order is: settle the doors, move the package, publish, then let real users choose Tier 1.
