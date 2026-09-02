# corvid-go — the binding's plan

corvid-go is the **Go binding** for the `corvid` embedded database. Like its
sibling `corvid-c`, it exists to prove, continuously and outside the engine
repo, that corvid's **published FFI artifacts** — the platform cdylib,
`corvid.h`, and the golden fixtures shipped in each release archive — drive
a real consumer to the same verdicts the engine's own suite produces; on top
of that proof it carries the idiomatic Go API.

Engine repo: `corvid-db/corvid` (read-only upstream; never a submodule, never
vendored). Canonical docs: the corvid docs site's FFI section (the
`docs/FFI.md` contract — 124 symbols, frozen enums, §8 idiom gate).

## The architecture ruling: cgo over the typed C ABI, release artifacts only

**Deliberately different from corvid-node/corvid-python** (Rust-source
bindings): corvid-go links the **published cdylib via cgo**, the corvid-c
pattern. Why:

- Go users expect either a system/shared library or a bundled download —
  not a Rust toolchain. A binding whose install invokes `cargo` (or pins a
  nightly rustc) is a non-starter for the Go ecosystem's zero-build-chain
  ethos; cgo + a fetched, checksummed artifact keeps the requirement at
  "a C compiler", which Go already needs for cgo itself.
- The C ABI is the engine's *locked*, stability-governed surface (FFI.md
  §8): enum values frozen, symbols append-only, breaks are loud version
  bumps. Binding to it binds to the contract, not to Rust crate internals
  that are `#[non_exhaustive]` and pre-1.0.
- Consuming the release artifacts keeps this repo an independent verifier:
  if a published dylib/header/fixture set disagrees with the spec, the
  golden suite here catches it (that is exactly how corvid-c found the
  v0.2.0 install-name defect, finding F1).

Consequences, all locked:

- **No Rust toolchain, ever.** `make deps` (fetch.sh / fetch.ps1) downloads
  the pinned release archive for the host platform, sha256-verifies it
  against the release's `checksums.txt`, extracts into gitignored `deps/`,
  and normalizes `corvid.h` + the cdylib into `deps/current/` (stable name,
  so the `#cgo` flags stay platform-independent; on Windows the MSVC import
  library is additionally copied under the `libcorvid.dll.a` name so
  mingw-w64's `ld` finds it via plain `-lcorvid`).
- **Pin EXACT engine tags.** One engine version at a time; today `v0.3.1`.
  The pin lives in exactly one variable per fetch script
  (`CORVID_VERSION` / `$CorvidVersion`) and is stamped into
  `deps/version.txt`.
- **No vendored binaries in git.** `deps/` is gitignored.
- **No network at build time.** The build consumes `deps/` only.
- **Published-artifact defects are findings, not patches.** Divergence is
  reported upstream (`corvid-db/corvid`), never worked around locally. The
  fetch scripts also byte-compare the release's `golden/*.txt` against the
  fixtures vendored in this repo — a mismatch is a hard fetch failure.

## The locked rule: golden port BEFORE ergonomic sugar

Inherited from the bindings program's master plan and non-negotiable:

> **A binding opens with the golden-suite port.** The engine's golden
> fixtures (267 executable lines across 8 files) are the contract; a
> binding that wraps the ABI before it can replay the contract is building
> on unverified ground.

corvid-go's first substantive deliverable is `golden_test.go` — a port of
the engine's harness (`c/smoke.c`, as ported standalone by
`corvid-c/test/golden.c`) — replaying every fixture line **through this
binding** (the Go API wherever it can express the op, the cgo layer's
value-family wrappers where the op is inherently raw — see below). The
fixtures are vendored byte-identical under `golden/`. No softened asserts:
the same expectation checks, the same `executed == counted` dispatch rule,
first failure naming file:line + OP + expected-vs-got.

Only with the port green does the ergonomic surface count.

## C-handle lifetime mapping (FFI.md §2 → Go)

Each C handle becomes a Go struct holding the pointer, with **explicit
`Close()` and `runtime.SetFinalizer` documented as a backstop only** — Go
errors, never panics, for every failure path. No cgo type appears in any
exported signature.

| C handle | Go owner | Explicit release | Backstop finalizer |
|---|---|---|---|
| `corvid_db` | `Db` | `(*Db).Close()` (idempotent) | frees via same path |
| `corvid_coll` | `Collection` | `(*Collection).Close()` (idempotent) | frees via same path |
| `corvid_value` (owned) | transient inside a call, or decoded-then-freed | freed deterministically at end of the wrapper call | not needed |
| `corvid_pred` | `Predicate` | consumed by `And/Or/Not/Filter/DeleteWhere`; `Close()` frees a never-consumed root | frees if never consumed |
| `corvid_query` | `Query` | consumed by `Run`/every aggregate; `Close()` frees an abandoned builder | frees if never run |
| cursors (`rows`, `strs`, `geohits`, `groupiter`, `schemaiter`) | walked to exhaustion inside the single wrapper call | freed in `defer` | not needed |
| buffers (`insert_auto` key, `page` next-after) | copied to Go memory | `corvid_free`'d in the wrapper | not needed |

The ABI's unconditional-consumption rule (§8: `and/or/not`, `filter`,
`run`, aggregates consume **even when they fail**) is honored by marking the
Go side consumed regardless of the C result — a failed combine still took
the children, and the Go layer never double-frees.

## Borrowed-doc rules mapped to Go: copies at the boundary

FFI.md §5: `rows_next`/`geohits_next`/callback keys and docs are **BORROWED
until the next `next`/`free`**; `_ref` views are borrowed until the parent
mutates or dies; writing through or freeing them is UB. The node binding's
answer was copies at the boundary; Go's is the same, mechanically:

- Every key/doc/`_ref` buffer is **copied into Go-owned memory inside the
  wrapper call** (`C.GoBytes` / slice copy), before the next `next` call or
  the parent's free. Nothing borrowed is ever retained past the call.
- Values crossing **into** the engine are built as fresh C values (the ABI
  clones them into the engine) and freed immediately after the call; the
  caller's Go data is never handed over as anything but a borrowed,
  read-only, call-scoped pointer.
- Decoding a map is read-only over the borrowed handle and copies
  everything it touches; the decode result is fully Go-owned.

### cgo pointer discipline

- **No Go pointer is ever stored in C memory.** The POD arrays the ABI
  takes (`corvid_kv`, parallel key/name arrays, `corvid_field_def`) are
  built in `C.malloc`/`C.CBytes` memory with C copies of the Go bytes, and
  freed in `defer`. (This is why keys/names are copied even though cgo
  pins directly-passed argument pointers for the call duration.)
- Strings/bytes/float slices passed **directly as call arguments** are
  passed as pointers into Go memory without copying — legal under the cgo
  rules (pinned for the call, C never retains them; the ABI clones), and
  the hot path stays zero-copy on the Go side of the boundary.
- Empty (pointer, 0) is expressed with a non-NULL sentinel (§1.5): a
  package-level zero byte / zero float stands in for empty slices.
- The §1.6 callbacks (`scan`, `update`) cross as **integer registry ids**
  stored in a `C.malloc`'d cell — no Go pointer reaches C-allocated
  memory, and the exported Go trampolines look the closure up by id.

## Goroutine-safety mapping (FFI.md §6)

The engine contract, restated as Go:

- `Db` and `Collection` are **safe for concurrent use from multiple
  goroutines** (backed by `Arc<Db>`; reads concurrent, writes serialized by
  the engine). No extra locking is added in the binding.
- `Query`, `Predicate`, and every result cursor are **single-goroutine**
  (the ABI calls concurrent use of one such handle UB). The binding does
  not police this — same as the engine, it is documented, not detected —
  but nothing in the API encourages sharing them (builders are cheap,
  chainable, consumed by the terminal call).
- Errors are per-thread in C and per-goroutine in effect: every wrapper
  reads `corvid_last_error_code/message` **immediately** after the failing
  call, on the thread the failing call ran on (cgo calls run on the
  calling goroutine's thread). That shrinks the exposure to the
  theoretical minimum, but it is not an absolute guarantee: the runtime
  may migrate the goroutine between the failing call and the read, and
  the read then lands on another thread's slot and **misses** the
  message. A miss is loud, never silent — a failure whose slot reads
  empty surfaces as a zero-code `CorvidError` ("failure with no
  recorded error", cgo.go), never as another call's error silently
  misattributed.
- v1 is **context-free and synchronous**, per the master plan (the engine
  is sync; `context.Context` would be a lie over a synchronous ABI).

## Map-key enumeration: the v0.2.2 oracle and its v0.3.0 collapse

**History (v0.2.2 bootstrap).** The C ABI had **no map-key iterator** —
`Value::Map` was readable only by known key (`corvid_value_map_get`; the
engine's own FFI suite walked maps by expected keys with the length
pinned — task-3 report, note 2). A Go API that returns `map[string]any`
from `Get` needs a key source, so each `Db` kept a candidate key-name
set, populated by every document passing through the binding's write
paths and by declared schemas; decoding probed the candidates and
**verified the probed count against the map's true entry count**
(`corvid_value_len`) — a full match decoded, any unknown key failed
loudly (`ErrMapKeyEnumeration`) instead of returning a
silently-truncated map. `GetFields` and `Query.Select` never needed the
oracle (their field list is the key source). The gap was logged as
docs/FFI.md §4.4 errata.

**The v0.3.0 collapse.** Engine v0.3.0 shipped the anticipated
`corvid_value_map_keys` (§4.4's additive erratum: an OWNED strs cursor
in ascending key-BYTE order; non-maps an EMPTY cursor, inert). The
candidate machinery — the key set, `ErrMapKeyEnumeration`, the write
path `remember` hooks, the schema `addAll` — is DELETED, not
maintained alongside: `decodeValue` enumerates keys through the real
iterator and every read path (`Get`/`Scan`/`Page`/query rows/geo
docs/`Update` callbacks) decodes COMPLETE on any database, whatever
wrote the data (`mapkeys_test.go` pins the across-a-reopen shape the
oracle existed for; the VMAP_KEYS/GET_KEYS golden lines pin the
iterator's order and inert shapes op by op). Retrieval queries keep
`Row.Doc == nil` without `Select` — a projection decision that stands
on its own now, no longer ABI-forced; `PhraseSearch` rows (v0.3.0's
other addition) always carry documents.

## Phase GO1 (this bootstrap) — scope

1. **Plan doc** (this file) — ruling, lifetime mapping, pointer discipline,
   goroutine mapping, the map-key boundary.
2. **Repo scaffold** — go.mod (`github.com/corvid-db/corvid-go`), MIT
   LICENSE (engine's copyright line), `.gitignore` (`deps/`, test
   artifacts), README (usage + requirements: artifacts NOT vendored).
3. **Fetch + verify** — `fetch.sh` / `fetch.ps1` (the corvid-c pattern) +
   `make deps`; vendored `golden/` byte-verified against the release.
4. **The cgo layer** — ONE file (`cgo.go`) holding every line of C
   interop: the `#include "corvid.h"` preamble, the two §1.6 callback
   bridges, and Go-safe wrappers for every needed symbol (value
   construction/reads, predicates, query builder, mutations, reads, index
   creates, TTL, graph, geo, aggregations, schema, admin). C pointers
   never leave the file; the rest of the package sees only Go types.
5. **The Go API** — `corvid` package: `Open`/`OpenMemory`,
   `(*Db).Collection`/`Collections`/dump/load(+renames)/backup/compact,
   `(*Collection).Insert/Get/GetFields/Delete/DeleteWhere/DeleteBatch/
   PutMany/InsertAuto/InsertTTL/SetTTL/GetTTL/PurgeExpired/Patch/Update/
   CompareAndSet/Scan/Page/Len`, the fluent `Query` builder
   (`Filter`+`Field(path)` predicate helpers, `Vector`/`Text`/
   `FuseRRF`/`RerankMMR`/`Approx`/`Limit`/`Offset`/`OrderBy`/`Select`,
   terminal `Run → []Row{Key []byte; Doc any; Score float32}` and the
   aggregation terminals), index creates (all variants), schema, TTL,
   graph, geo. Errors are `*CorvidError` (implements `error` + `Code()`).
   Value mapping: `map[string]any`, `[]byte` (Bytes), `[]float32` (Vector),
   `int64`/`float64`/`string`/`bool`/`nil`; NaN/±inf/-0.0 cross bit-exact
   (documented).
6. **The golden port** — `golden_test.go`: 267 executable fixture lines
   through the binding, first failure named per file:line, dispatch count
   enforced.
7. **CI** — a linux/macos/windows matrix: fetch+verify, `go vet`,
   golangci-lint (one leg), `go test ./...` (the golden suite); under 5
   minutes.

Out of scope for GO1: contexts/async wrappers, batch iterators beyond the
ABI's, any second binding surface (pure-Go? not plausible — the engine has
no wire protocol).

## Verdict protocol

Same as corvid-c's: the golden suite logs one
`SMOKE <file> lines=<n> executed=<n>` line per fixture; green means every
expectation of every executable line passed and the dispatch count matches
the pre-scan count. Divergence from the engine-side suite's verdicts is a
defect here; divergence of the artifacts from the engine repo is a finding
for the engine repo.
