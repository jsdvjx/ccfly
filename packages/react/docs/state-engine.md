# ccfly Screen-State Engine — Design (v2, post-critique)

> Rebuild of the "read screen → know state → render control → drive" pipeline in `@ccfly/react`.
> The old path (`livestate.ts`, `ctrlstate.go`, `selectKind.ts`, `ControlBar.navTo`) is the **failure oracle**, not a template.
> `ccfly-cloud` is unchanged: `gateway.go` is a transparent `ReverseProxy` that forwards `409`/SSE/WS verbatim — the engine lands here in `@ccfly/react`.
>
> v2 = the v1 design + three adversarial critiques folded in: cut ~30% machinery, fixed the device/client contract, reframed actuation around **command = expectation (`send`+`waitFor`)**, and made the commit **fail-closed**.

---

## 0. Failure oracle (the design's only acceptance test)

Every layer must kill one of these or be cut.

| | Failure (verified in `~/ccfly`) | Kill |
|---|---|---|
| F1 | `stripANSI` before parse + `cur` from caret glyph only → glyphless/reverse-video highlight discards the whole menu | faithful attribute-aware `Frame` + multi-witness `cur` |
| F2 | half-redraw misread; busy↔input flicker; stuck-busy | quiescence debounce + a liveness ceiling |
| F3 | permission stolen by confirm; model read as list; effort misclassified | decisive-signal resolves + `weight` order |
| F4 | wrapped/scrolled/unnumbered options dropped (`opts[0].num==='1'` gate in 4 places) | de-wrap to logical options; `num` optional; `rows: number[]` |
| F5 | blind `Array(|Δ|).fill(dir)` arrow burst from a stale parse → "tap Opus, land Sonnet" | closed-loop `send`+`waitFor`, verified **by value** |
| F6 | TS/Go parser twin ("逐条对齐"); zero tests | **one** parser (`preFrame`) + golden `.ansi` fixtures |
| F7 | hardcoded 5-rung effort ladder, unknown→"medium 居中" | labels verbatim; `curIndex===-1`→`unknown`, no ladder |

---

## 1. The shape (locked with the user)

- **Two inputs + one derived.** `frame` (faithful attribute grid) · `jsonl` (structured conversation data) · `pre` (a **one-time** parse of both, shared by every resolve). `pre` holds **features, never verdicts**.
- **Decentralized registration**, one file per state: `register({ kind, weight, resolve, onMatch })`. The engine is pure logic — **no `View`, never imports React**. `weight` is a **sort key**, not a score.
- **Engine** = a quiescence-debounced clock → stable `frame` → `pre` once → run resolves **by weight, first non-null wins** → call its `onMatch` → return the current `{kind, info} | null`. The UI binds to the current match, so a `kind` change tears down the old view for free.
- **Commands are expectations.** Every command is `precondition → send → waitFor(postcondition)`; a `null` at any step **halts and surfaces**, never proceeds. This is the closed loop.

---

## 2. Constants budget (honest — the v1 "only weight" claim was false)

The **only** runtime numbers are two **clock** constants; everything else is **derived**, and `weight` is the only classifier number.

- `quietMs` (~100) — debounce: classify only after output stops.
- `maxStaleMs` (~400) — **liveness ceiling**: emit the latest frame even if not quiet, so a *perpetually animating spinner* (which mutates `char` and never quiesces) still yields a frame. Kills the F2 "stuck-busy on a live spinner" hole the pure-debounce design reopened.

Derived (no magic numbers): step cap = `options.length`; verify timeout = clock-derived; input-box border = "run of `─` ≥ half the visible width" (relative, not literal `6`). **No confidence floats.**

---

## 3. Interfaces (single reconciled contract)

```ts
// ── frame.ts — PURE DATA (zero xterm/React imports; JSON-constructible for tests) ──
export type ColorMode = 'default' | 'palette' | 'rgb'
export interface Color { mode: ColorMode; value: number }          // mode-tagged: a glyphless highlight is a non-default bg
export interface Cell { char: string; fg: Color; bg: Color; inverse: boolean; dim: boolean; bold: boolean; width: number }
export interface Cursor { row: number; col: number; visible: boolean }
export interface Frame {
  rows: number; cols: number
  cells: Cell[][]                                                   // row 0 = visible-screen top (baseY-anchored)
  cursor: Cursor
  text(row: number): string
  readonly hash: string                                            // FULL frame (char + every attribute + cursor). No selective exclusion.
}

// ── pre.ts — the ONE parser. Split so the driver can run it on a bare Frame (no jsonl). ──
export interface PreOption {
  num: number | null                                               // F4: numbering optional
  label: string                                                    // de-wrapped logical label
  rows: number[]                                                   // F4/F5: a wrapped option spans rows; verify per-row
  cur: boolean                                                     // multi-witness (see §4)
  checked?: boolean                                                // tri-state; undefined = single-select
}
export interface FramePre {                                        // SCREEN-only features
  options: PreOption[]
  footer: string | null                                            // raw bottom hint text (NOT a modal verdict)
  isBusy: boolean
  inputBox: boolean
}
export interface JsonlFacts {                                      // STRUCTURED, never scraped
  model: string; turns: number; tokens: number; title: string
  lastEntry: { role: string; kind: string }
  toolInput?: unknown                                              // permission target / Edit being confirmed come from HERE
}
export type Pre = FramePre & JsonlFacts
export function preFrame(frame: Frame): FramePre                   // driver uses THIS — no jsonl needed
export function projectJsonl(items: JsonlItem[]): JsonlFacts
export interface Ctx { frame: Frame; jsonl: JsonlFacts; pre: Pre }

// ── registry.ts — plain hand-written union; no StateInfoMap meta-machinery ──
export type StateKind =
  | 'busy' | 'input' | 'offline'
  | 'modelSelect' | 'permission' | 'effort' | 'confirm' | 'multi' | 'list'
export type Info = BusyInfo | InputInfo | ModelSelectInfo | PermissionInfo
  | EffortInfo | ConfirmInfo | MultiInfo | ListInfo
export interface Match { kind: StateKind; info: Info }
export interface StateDef { kind: StateKind; weight: number; resolve(ctx: Ctx): Info | null; onMatch(info: Info): void }
export function register(def: StateDef): void

// ── clock.ts — ONE timer. No FrameSource, no separate Actuator.nextStable. ──
export interface FrameClock {
  onStableFrame(cb: (f: Frame) => void): () => void
  waitFor(kind: StateKind, pred?: (info: Info) => boolean, timeoutMs?: number): Promise<Info | null>
  current(): Frame
  dispose(): void
}

// ── send: ALL actuation goes through the device /sendkeys HTTP rail, never the WS typing rail ──
export type DeviceKind = 'input' | 'select' | 'busy' | 'offline'  // the device's COARSE vocabulary (distinct from StateKind)
export type SendResult =
  | { ok: true }
  | { ok: false; reason: 'floor'; deviceKind: DeviceKind }        // device floor 409 (control.go) — has a coarse kind
  | { ok: false; reason: 'offline' }                              // gateway-minted 409 (gateway.go:36) — NO kind
  | { ok: false; reason: 'network' }
export function send(keys: string[], navExpect?: NavExpect): Promise<SendResult>
```

**Why the corrections (each fixes a critic finding):**
- **`DeviceKind` ≠ `StateKind`.** The device floor only ever returns coarse `{input,select,busy,offline}` (`control.go:178`→`detectState().Kind`); the client union is granular. v1 compared `r.kind !== info.kind` (granular vs coarse) → **always-true spurious bail**. Compare coarse-to-coarse only ("is it still a select at all").
- **`preFrame` split.** `drive()` has no `jsonl`; v1 called `preParse(frame, jsonl)`. The screen half needs no jsonl — split it out.
- **`rows: number[]`** everywhere on the drive path; a wrapped option spans rows, and a singular `row` mis-verifies it (false divergence bail).
- **Full-frame `hash`.** The selective hash bought a stuck-state bug; the debounce already swallows spinner churn, and `maxStaleMs` covers perpetual animation.

---

## 4. `pre` — the single parser

- **Option extraction gates on "looks like an option" *before* the highlight witness** (footer-anchored menu region + a label token), so a `/compact` progress bar or the effort slider's colored rung is **never** mis-claimed as a selected option.
- **`cur` is multi-witness with a DEFINED precedence and a commit-time agreement rule:** `cursor-on-row` > `inverse / non-default-bg run` > `caret glyph`. For navigation, any witness suffices. **For a commit, the witnesses must AGREE**; if they disagree (glyph says A, inverse says B), `pre` flags it and the driver bails (see §6). v1 listed four OR'd witnesses with *no* conflict rule — the actual hard case.
- De-wrap to logical options; `num` may be `null`. Track a scroll indicator if present.
- `isBusy` / `inputBox` are features; `footer` is **raw text**, not a "this is a real modal" verdict.
- **JSONL supplies what JSONL has:** `model/turns/tokens/title` and `toolInput` (a permission's target path, the Edit a confirm is about) come from `projectJsonl`, **not** the screen. Only genuinely screen-only data (the `cur` highlight, effort labels, spinner verb) is scraped.

---

## 5. resolves (state catalog)

`resolve` returns `Info | null`; non-null **only** on a decisive structural signal, so resolves are near-mutually-exclusive and `weight` is just the tiebreak.

| kind | weight | decisive signal (→ non-null) | Info payload (screen \| jsonl) |
|---|---|---|---|
| `modelSelect` | 20 | title-model **and** ≥2 options with a family/anchor | rows: screen · `currentModel`: **jsonl** |
| `permission` | 20 | an option like *don't-ask-again / always / allow* exists | options: screen · `target/path`: **jsonl `toolInput`** |
| `effort` | 30 | effort labels present (verbatim, **no ladder**) | labels: screen · `curIndex===-1`→`unknown` |
| `confirm` | 40 | bare yes/no; gate **excludes** govern/choose rows | `question/Edit`: **jsonl** |
| `multi` | 50 | a checkbox tri-state glyph/inverse | options+checked: screen |
| `busy` | 70 | `pre.isBusy` (frame discriminator) | verb: screen · `model/tokens`: **jsonl** |
| `input` | 80 | `pre.inputBox` | suggest ghost: screen |
| `list` | 90 | fallback: ≥2 options, none more specific | rows: screen |
| `offline` | — | **upstream session gate**, not a frame resolve | — |

---

## 6. Actuation = command = expectation (`send` + `waitFor`), **fail-closed**

The user's framing is the spine, and the safety critique proves it's the right one. **Split into two atoms**; a command composes them:

- `send(keys)` — fire keystrokes. **HTTP `/sendkeys` rail only, never the WS typing rail** (so a digit fast-path can't slip past the device unguarded).
- `waitFor(kind, pred?, timeout)` — await the next stable frame whose `kind.resolve` matches `pred`. Reuses detection's `resolve`; it's a transient consumer of the same clock. **Event-driven** (resolves the instant the state appears); `timeout` is only the failure ceiling.

```ts
async function selectModel(name: string): Promise<boolean> {
  if (!current.is('input')) return false                          // precondition (safety: don't inject /model mid-turn)
  await send(['/model', 'Enter'])
  let sel = await waitFor('modelSelect') as ModelSelectInfo | null
  if (!sel) return false                                          // picker never opened → HALT, surface
  // closed-loop nav: prefer typing the NUMBER; else one arrow per step, verified by VALUE
  while (!onTarget(sel, name)) {
    const before = sel
    await send(['Down'])
    sel = await waitFor('modelSelect', s => curMovedFrom(s, before)) as ModelSelectInfo | null
    if (!sel) return false                                        // didn't move → HALT, never blind-send the next step
  }
  // COMMIT (the dangerous key): only if cur is on target BY VALUE and witnesses agree
  if (!commitSafe(sel, name)) return false                        // fail-closed
  await send(['Enter'])
  return !!await waitFor('input', s => (s as InputInfo).model.includes(name))  // final proof via jsonl.model
}
```

**Commit safety (the catastrophe is a wrong `Enter` on a permission menu) — fail-closed, client-verified:**
1. **Verify by VALUE, not distance.** Before committing, require `cur` on the target by **label fingerprint** (`num` + bounded label), not "distance decreased". Value survives a concurrent viewer walking the cursor; distance does not.
2. **Witness agreement.** Commit only if `pre`'s `cur` witnesses agree (§4). A systematically misread `cur` that the per-step distance check can't catch is caught here.
3. **Concurrency guard.** **Any** unexpected `cur` jump (moved without us sending, or farther than our one step) → divergence → bail. (`kind`-change alone misses the case where `kind` stays `select` but another viewer moved the cursor.)
4. **HTTP rail only**, so the device sees every keystroke; the device keeps its text-submit floor and adds a **coarse** "still a select / is input" guard for menu-`Enter` — **no rich device parser** (so F6 isn't reopened).
5. **Fail-closed on doubt.** On degraded (WS down → ≥1.8 s stale) or any unmet check, **refuse** permission/destructive commits — *"live connection required to answer prompts."* Never ship a weaker commit path for the highest-stakes interaction.

**Drop the "structurally impossible" claim.** Honest invariant: **on any doubt, refuse and surface — never wrong-commit.** (A *device*-authoritative guarantee would need a minimal attribute-aware cur-on-anchor reader on the device — a small golden-corpus-shared parser. Deferred; not built unless we decide the strong guarantee is worth that parser. See "Open decision".)

**409 decode:** gateway-minted offline 409 (no kind, `{error}`) → terminal `offline`, abort immediately; device-floor 409 (`DeviceKind`) → re-bind/bail. v1 conflated them → a dying device surfaced as a confusing `timeout`.

---

## 7. Tests (F6)

Capture real attribute-bearing frames (`capture-pane -e` / xterm-serialize) per prompt-type into `.ansi` fixtures; table-test `preFrame` + each `resolve`. **Must include** a glyphless-highlight fixture and a **witness-disagreement** fixture (glyph vs inverse on different rows) asserting the documented tiebreak + commit bail. No test runner exists today — add one.

---

## 8. Cuts applied (critics, unanimous)

- **Deleted** the degraded `FrameSource` / `parseAnsiToFrame` / `source` axis — no failure class needs it and it reopened F6 (a second ANSI parser). WS-down = don't classify (raw-terminal holds last-known) + refuse menu commits.
- **Collapsed** `FrameClock` + `FrameSource` + `Actuator.nextStable` → one `FrameClock`; actuation is a bare `send`.
- **Dropped** `StateInfoMap` declaration-merging → an ~8-line hand-written union.
- **Full-frame hash** (+ `maxStaleMs` liveness) instead of a hand-curated subset.
- **`Action` 5 → 3** verified variants (`select`/`toggle`/`submit`); `effortStep` folds into a step, `Escape` is a bare `send`.
- **Constants derived** (`cap=options.length`, timeout clock-derived, border ≥half-width); honest 2-constant budget.
- **Cut** speculative `pre` fields (`curBy` off the shipped struct → recompute in tests) and screen-scraped fields JSONL already has.

---

## 9. Migration (cloud unchanged)

Shadow the new engine alongside the old (`tick` vs `detectState`, compare), then cut over. The shadow comparator is a **temporary scaffold with a hard-delete gate** (not "keep importable"). `ccfly-cloud` needs no change — except the client driver must decode the gateway's own offline-409.

---

## Open decision (needs a call)

**Commit authority.** v2 ships **fail-closed client-verified** commit (refuse on any doubt) — simple, no device parser, safe-by-refusal. The alternative is **device-authoritative** commit (a minimal attribute-aware cur-on-anchor reader on the device that refuses `Enter` unless its *own* cursor is on the anchor by value) — strictly stronger, but a real (small, golden-corpus-shared) second parser. Recommendation: ship fail-closed now; add device-authoritative later only if the strong guarantee is needed.
