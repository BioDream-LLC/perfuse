# Debugging

The question is almost always the same: this message came in, something wrong went out, where did it change? Perfuse answers it by replaying a recorded message through the current configuration and showing every step.

Nothing is sent while tracing. The channel keeps running untouched, and no redeploy is needed.

## Tracing a message

Select any recorded message and trace it. The result shows, in order:

- **What the filter read.** Which fields it looked at and what values it found. A filter rejecting everything is usually reading a path that does not exist in this feed, and this names it.
- **Each transformation.** What it changed, from what to what.
- **What each destination decided.** Whether it would accept the message, and why not if it would not.
- **The output.** The message as each destination would send it.

## Step by step

The step-through view runs each transformation on its own and snapshots the whole message after it. A numbered rail of steps, coloured by what happened, and the message as it stood at each point.

The whole message rather than only the change, because the change is often not where the problem is — a step that correctly modified the field it was aimed at, on a message where an earlier step had already put the wrong thing there, looks perfectly correct in isolation.

## Four outcomes, and the distinction that matters

| Outcome | Meaning |
|---|---|
| `changed` | The step ran and modified the message. |
| `no-effect` | The step ran, and the message is unchanged. |
| `skipped` | The step's `when` condition was false, so it did not run. |
| `failed` | The step errored. |

**`no-effect` and `skipped` are reported separately, and this is the point of the whole feature.**

From outside they are identical: the message is the same afterwards either way. They mean opposite things. `skipped` means the condition worked as written and this message was not one it applied to — usually correct. `no-effect` means the step ran and found nothing to do, which usually means it is aimed at a field that is not there.

An engine that reports both as "nothing happened" leaves you unable to tell a working conditional from a broken path, and that is the most common transformation bug there is.

> A step reporting `no-effect` on every message is nearly always a wrong path. The step-through header counts how many steps found nothing, which is the fastest way to spot it: a channel with three steps and three `no-effect` results is not transforming anything at all.

## Testing a change before saving it

The builder can run a candidate configuration against recorded traffic and report what would differ. This is the safest way to change a live channel: make the edit, see what it does to the last few hundred real messages, then save.

It is also how to find out that a change which looks obviously correct affects messages you had not thought about — a step conditioned on one trigger event, on a feed that turns out to carry four.

## What tracing cannot tell you

It runs the current configuration. If the channel has been edited since the message arrived, the trace is a hypothetical rather than a reconstruction, and Perfuse marks it as such rather than letting the distinction pass silently.

It also does not exercise the transport. A trace shows what would be sent, not whether the destination would accept it — a message that is correct and a destination that is refusing connections produce a clean trace and a failed delivery. Those are visible in the delivery record instead.

## Scripts

A script is one opaque step in the trace. Perfuse can show what went in and what came out, and cannot show what happened in between or which fields were read.

This is the concrete cost of using a script rather than declarative steps, and it is worth weighing when choosing. See [transformation](#transformation).
