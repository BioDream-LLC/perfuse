# Transformation

Transformations change a message. They are a list, they run in order, and each one sees the result of the one before it.

Perfuse's transformations are declarative: each step names an operation and the field it applies to. There is no scripting language in the ordinary path. This is a deliberate limitation and the reasoning is in [why not a script](#why-not-a-script) below.

## The steps

| Step | What it does |
|---|---|
| `set` | Writes a literal value to a path, creating it if absent. |
| `copy` | Copies one path to another, with an optional default when the source is absent. |
| `clear` | Empties a field, leaving it present. |
| `remove` | Removes a field entirely. |
| `map` | Looks the current value up in a table and replaces it. |
| `replace` | Regular expression substitution within a field. |
| `pad` | Pads a value to a fixed width. |
| `date` | Converts a date or timestamp between formats. |
| `trim` | Strips surrounding whitespace. |
| `case` | Converts to upper or lower case. |

Every step also accepts `description`, which appears in the trace and in the interface, and `when`, which makes the step conditional.

A step is written as the operation name with its own block:

```yaml
transformations:
  - description: Our record number, not theirs
    set:
      path: PID-3.4
      value: RIVERSIDE
  - copy:
      from: PID-19
      to: PID-3.1
      default: UNKNOWN
  - map:
      path: PID-8
      table:
        M: Male
        F: Female
```

> `set` and `value` are not siblings. `set` takes a block containing `path` and `value`. Writing `set: PID-3.4` with `value:` beside it is the commonest mistake in a hand-written channel and produces a load error rather than a silent misconfiguration.

## Clear, remove and the difference

`clear` leaves the field present and empty. `remove` takes it out altogether.

For HL7 this distinction is real and it reaches the receiver. An empty field says "I have this field and it has no value". An absent field says nothing at all. Some receiving systems treat the first as an instruction to blank their stored value and the second as an instruction to leave it alone — which is the difference between deleting a patient's recorded allergy and not mentioning it.

If you do not know which the receiver wants, ask. Guessing has a fifty per cent chance of silently destroying data on the far side.

## Conditions

`when` takes an expression and the step runs only if it is true:

```yaml
  - when: MSH-9.2 == "A08"
    set:
      path: EVN-1
      value: A08
```

A step whose condition is false is recorded as **skipped**, and this is reported separately from a step that ran and changed nothing. That distinction is the point of the step-through debugger and it is covered in [debugging](#debugging): a false condition and an absent field look identical from outside and mean opposite things.

## Paths

A path addresses a field: `PID-5` is the fifth field of PID, `PID-5.1` its first component, `PID-3[2]` the second repetition. Without a repetition index a path addresses the first repetition.

Two behaviours worth knowing:

- `set` creates whatever path it addresses, including intervening structure. This is convenient, and it means `set` can never demonstrate that a field was absent — after the step it exists. To test what happens when a field is missing, use `copy` from a path that is not there.
- A path addressing a segment that occurs several times, without an index, addresses the first occurrence. On a message with five OBX segments, `OBX-5` is the first one's fifth field. To reach all of them the path needs an index, and a channel that assumes one OBX will silently ignore the rest.

## Errors

Most steps have nothing to fail at. Setting a value, clearing a field or copying from an absent path with a default are all defined for every input.

`date` is the exception, because a value that is not a date it recognises has no correct conversion. It takes `on_error`:

- `fail` — the default. The message stops and is recorded as a transformation failure.
- `keep` — leave the original value alone.
- `clear` — empty the field.

The default is `fail` because a timestamp that silently did not convert is worse than a message that did not arrive. A date in the wrong format usually still looks like a date, so it passes the receiver's validation and lands in the record as the wrong day.

`keep` is legitimate when the receiver tolerates either format, and when used the trace marks the change as deliberate so it does not read as a step that failed quietly.

## Order matters, and the trace shows it

Each step sees the previous step's output. Two consequences that catch people:

- A `map` after a `case` sees the case-converted value, so its table keys must match the converted form. A table of `M`/`F` after a lower-case step matches nothing, silently, because a value not in the table is left alone.
- A `copy` from a field that a later step removes still works, because it ran first. The reverse does not.

Both are visible in the step-through view, which shows the whole message after each step rather than only the change. See [debugging](#debugging).

## Suggesting mappings, and what to do with a suggestion

The **AI Mapper** proposes mappings between a set of source fields and a set of targets. Its defining behaviour is that it declines: below its confidence threshold it abstains rather than guessing, because a confident wrong mapping in a clinical system is worse than no mapping at all.

### Give it something to be confident about

A name on its own is weak evidence, even when it matches exactly. `PatientMRN` against `PatientMRN` scores 68, which abstains at the default threshold of 70.

The engine knows what an HL7 path means — it will tell you `PID-7 is Date/Time of Birth` and score your field name against that rather than against the string `PID-7`, which shares no characters with anything. A locally defined `Z` segment means whatever your site decided, so nothing is claimed about it.

That gets a mapping most of the way. To finish it, supply both halves:

- **Example values** for a source field, after an equals sign: `ZPI-1 = MRN00412, MRN00998`
- **The kind of value** a target holds, the same way: `PID-3.1 = mrn`. The kinds are `mrn`, `npi`, `ssn`, `phone`, `date` and `email`.

With those, the engine can see that the values agree with the target's kind, and a suggestion rests on more than a name.

### Approval is one mapping at a time

Each suggestion has its own approval box. There is no "accept everything above 80%", deliberately: the engine has already used the confidence to decide whether to offer the mapping, and what remains is a judgement about this field in this feed. A number is not that judgement.

**An abstention cannot be approved at all.** It has no box, not a disabled one, and the screen says the engine does not know. If you know the answer, map it by hand — that is a different act from agreeing with a suggestion.

A suggestion below your threshold is shown without a box too, marked as below it. Worth reading — a near miss is often the mapping you wanted, and hiding it would leave you wondering whether the engine considered it — but not approvable, because your threshold is the line you drew.

### Two things to do with what you approved

**Add them to a channel** turns each mapping into a copy step. This only works where the source is a path in the message, such as `ZPI-1`, because a step copies from one place in a message to another. The steps go into the channel file and take effect when it next loads — nothing changes in the traffic until then. Each step's description records the confidence it was suggested at and that a person approved it, which is what makes it accountable six months later.

**Download as a recipe** is the right answer for the commoner case: a mapping from a column name, a JSON key or a vendor's own field name. Those are real mappings, and a channel cannot read such a field directly, so a step cannot express them. The recipe records them as approved and can be shared.

## Why not a script

Mirth puts a JavaScript transformer at the centre of every channel, and it is the most flexible design available. Perfuse offers scripting — see the `scripts` key in the [channel reference](#channel-reference) — but does not make it the ordinary path, for three reasons that come from what goes wrong in practice.

A declarative step can be inspected. Perfuse can tell you that a step reads `PID-5` and writes `PID-8`, before running it, and can therefore show a field-level trace, warn that a step addresses a path the declared data type cannot contain, and generate a per-step replay. None of that is possible for arbitrary code.

A declarative step can be reasoned about by the interface. The builder can offer the fields that exist in this channel's traffic, and a change to a step can be checked against real historical messages before it is saved.

And a declarative step cannot do something unrelated to the message. A script can call out to the network, read the filesystem, or depend on the time of day. When a channel that worked for two years starts producing wrong output, the cause being outside the message is the hardest case to diagnose, and removing the possibility is worth more than the flexibility it costs.

Where a genuine transformation cannot be expressed declaratively, use a script and accept that the trace will show it as one opaque step.

## What a script is allowed to do

A script is refused anything outside the message unless it is granted, and the grants are on the Scripts section of the builder. This is deliberately unlike Mirth, which gives every script all of them — so there, a transformer can quietly start reading the filesystem after a copy-paste.

There are three.

**Read and write files** requires you to name the directories it may use, and a channel granting file access without them is refused at load. That requirement is the whole difference between file access and unrestricted file access: granting it without directories once reached the entire filesystem, including Perfuse's own database, which holds password hashes, the LDAP service account password and the OIDC client secret. Creating a channel needs only the editor role, so the escalation completed the moment an administrator started the channel.

**Query a database** uses the connections configured on the channel. A script with this can read anything those credentials can.

**Route messages to other channels** lets a script send a message into another channel, which is how a loop between two channels becomes possible.

Granting a capability to a channel with no scripts does nothing, and the generated file omits the whole block in that case.

## Handling a date that does not match its format

A date step converts from one format to another, and the interesting question is what happens when a value does not match the format it claims.

The default is to **stop the message**, and it is the right default: a timestamp that silently did not convert is accepted downstream and then misread, which is worse than a message that failed loudly and is sitting where somebody can see it.

**Leave the value as it is** passes it through in its original format. Downstream then sees two formats in one field, which is the failure the step existed to prevent — but it is the right choice where the field is optional and a partner sends it inconsistently.

**Empty the field** is honest and better than a wrong format, but a receiver that requires the field then rejects the message instead.

## The scripting languages

Three are accepted, and which one to pick depends mostly on whether you are migrating. The value goes in a script's `language` key:

| Value | What it is |
| --- | --- |
| `javascript` | The default. An unmarked script is JavaScript. |
| `lua` | The better choice when nobody is migrating. |
| `wasm` | The bytes of a compiled WebAssembly module, not source. |

**JavaScript** is the default, and an unmarked script is treated as JavaScript. It is here as a compatibility obligation rather than a preference: Mirth's scripts are JavaScript and a site moving across has thousands of lines of it. That includes support for E4X, a long-dead extension to the language that Mirth scripts use for XML and that has to be translated before anything will parse it.

**Lua** (`lua`) is the better choice for anyone not migrating. A smaller runtime, no prototype chain to misuse, and integer-indexed tables that match segment and field addressing more naturally than JavaScript's do. Its standard library is small enough to enumerate, which is what makes its sandbox easier to be confident about.

**WebAssembly** (`wasm`) is not a language but a compiled module, and the field wants the bytes of a `.wasm` file rather than source. Use it when the transformation already exists in a compiled language, or when it is heavy enough that the runtime cost matters.

Its contract is deliberately the simplest one available: **a module reads the message on standard input and writes its result to standard output.** Bytes in, bytes out — which is what every toolchain already does, and what somebody testing a module on their own command line already has. The alternative was an exported function over linear memory, which is faster and requires every module author to agree about allocation, string encoding and who frees what; for something that runs once per message, that trade is not worth it.

Anything a module writes to standard error becomes log lines, so it can explain itself the way a script calling `logger` can.

> A non-zero exit means the module *failed*, which is not the same as a filter deciding to reject a message. A filter module says `true` or `false` on standard output. Conflating the two would make a crashing module indistinguishable from one deliberately dropping traffic.

### What the output means

The rules are the same for all three languages rather than new ones per runtime, so a script's observable behaviour — what it can see, what it may do, how long it may take, what a filter verdict means — is defined once.

For a transformer, preprocessor or writer, **empty output means the message is unchanged.** Falling off the end of a script leaves the message alone; treating no output as "the message is now empty" would discard traffic on a technicality.

WebAssembly modules are compiled when the channel loads, not on the first message. Compiling is not free — a module built from a language that brings its own runtime can take hundreds of milliseconds — and paying that at load time means the first message through a channel is not mysteriously slower than the rest.
