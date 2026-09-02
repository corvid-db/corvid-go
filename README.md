# corvid-go

The Go binding for [corvid](https://github.com/corvid-db/corvid) — an
embedded database with a typed C ABI. It links the engine's **published
FFI artifacts** (the platform cdylib and `corvid.h`) over **cgo** and
carries an idiomatic Go API on top — and it proves, continuously and
outside the engine repo, that the published artifacts drive a real Go
consumer to the same verdicts the engine's own suite produces: the
golden-suite port in `golden_test.go` replays the engine's 267-line
fixture suite through this binding.

**Documentation:** the [corvid docs site](https://corvid-db.github.io/docs/)
is canonical — the [C ABI section](https://corvid-db.github.io/docs/ffi/)
documents every symbol this binding links (handles, ownership, errors,
threading), and [docs/PLAN.md](docs/PLAN.md) records this binding's
architecture ruling and lifetime mapping.

## The architecture ruling: cgo over the C ABI, release artifacts only

Deliberately different from the node/python bindings (Rust-source
builds): Go users expect a system/shared library or a bundled download,
not a Rust toolchain. `make deps` fetches the pinned engine release
archive for the host platform, sha256-verifies it against the release's
`checksums.txt`, byte-compares the release's golden fixtures against
the ones vendored here, and normalizes `corvid.h` + the cdylib into
gitignored `deps/current/` (on Windows the MSVC import library is also
copied under the `libcorvid.dll.a` name so mingw-w64's `ld` finds it).
Requirements stop at "a C compiler" — which cgo already needs.

- **No Rust toolchain, ever.**
- **One exact engine pin** — `v0.3.1`, living in one variable per fetch
  script (`CORVID_VERSION` in `fetch.sh`, `$CorvidVersion` in
  `fetch.ps1`), stamped into `deps/version.txt`.
- **No vendored binaries in git** (`deps/` is gitignored) and **no
  network at build time.**
- **Published-artifact defects are findings**, never local patches.

## Quick start

Requirements: Go ≥ 1.26 (CI exercises 1.27.x and 1.26.x), a C compiler
(CGO enabled — the default when one is present), `curl` +
`shasum`/`sha256sum` (macOS/Linux) or PowerShell 5+ (Windows).

```sh
make deps          # fetch + verify corvid v0.3.1 into deps/current
go test ./...      # the golden suite (267 executable lines, 8 fixtures)
```

On Windows (PowerShell), `make deps` is `./fetch.ps1`; there is no
rpath there, so put the cdylib on the DLL search path before building
or testing:

```powershell
./fetch.ps1
$env:PATH = "$(Get-Location)\deps\current;$env:PATH"
go test ./...
```

A taste of the API:

```go
package main

import (
	"fmt"

	"github.com/corvid-db/corvid-go"
)

func main() {
	db, err := corvid.OpenMemory()
	if err != nil {
		panic(err)
	}
	defer db.Close()

	docs, err := db.Collection("docs")
	if err != nil {
		panic(err)
	}
	defer docs.Close()

	// map[string]any / []any / []float32 / []byte / string / int64 /
	// float64 / bool / nil — NaN and ±inf cross bit-exactly.
	err = docs.Insert([]byte("p1"), map[string]any{
		"name": "ada",
		"v":    []float32{1, 0, 0},
	})

	if err := docs.CreateVectorIndex("v", corvid.MetricCosine); err != nil {
		panic(err)
	}

	// hybrid: filter + vector + text, RRF-fused, MMR-reranked
	rows, err := docs.Query().
		Filter(corvid.Field("name").Eq("ada")).
		Vector("v", []float32{1, 0, 0}, 3, corvid.MetricCosine).
		Select("name").
		Run()
	if err != nil {
		panic(err)
	}
	for _, r := range rows {
		fmt.Println(string(r.Key), r.Doc, r.Score)
	}

	n, _ := docs.Query().Filter(corvid.Field("name").StartsWith("a")).Count()
	fmt.Println("matched:", n)
}
```

Errors are `*corvid.CorvidError` (implements `error` + `Code()`) — Go
errors, never panics. `Db` and `Collection` are safe for concurrent
use; `Query`/`Predicate` builders are single-goroutine, build-once,
consumed-by-the-terminal. `Close` deliberately on every handle; the
runtime finalizers are backstops only. Concurrent use carries the FFI
§6 close caveat: **close a `Db`/`Collection` only after every
concurrent operation on it has completed** — freeing engine memory
while another thread is inside a call on it is undefined behavior, and
the binding's closed-handle gate (`checkOpen`) is TOCTOU by design, a
loud rejection of use-after-close, not a lock.

## Documents and maps

Engine v0.3.0 added the map-key iterator (`corvid_value_map_keys`, the
§4.4 erratum): every decode in this binding enumerates map keys
through it — `Get`/`Scan`/`Page`/query rows decode documents
COMPLETE on any database, whatever wrote the data, unknown and UTF-8
keys included (`mapkeys_test.go` pins the across-a-reopen shape that
the v0.2.2-era candidate-key oracle could not do; the decision-log row
in [docs/PLAN.md](docs/PLAN.md) records the collapse). Retrieval
queries still return `Row.Doc == nil` without `Query.Select(...)`
(retrieval carries keys and scores; read the document explicitly), and
`(*Collection).PhraseSearch` rows always carry documents.

## Installing the engine system-wide (alternative to deps/)

If `corvid` is installed as a system library (e.g. from a source
build), point cgo at it instead of running `make deps`:

```sh
export CGO_CFLAGS="-I/usr/local/include"
export CGO_LDFLAGS="-L/usr/local/lib -lcorvid"
# macOS may additionally need: export DYLD_LIBRARY_PATH=/usr/local/lib
go build ./...
```

## What's inside

| Path | What it is |
| --- | --- |
| `fetch.sh` / `fetch.ps1` | Download the pinned release archive, verify (sha256) against `checksums.txt`, byte-check the golden fixtures, normalize into `deps/current/` |
| `cgo.go` | The cgo layer — every line of C interop: the corvid.h include, the scan/update callback bridges, Go-safe wrappers over the ABI |
| `errors.go` / `values.go` / `db.go` / `collection.go` / `query.go` | The idiomatic Go API (Db/Collection/Query/Predicate, the value mapping, the error type) |
| `golden_test.go` | The golden-suite port — 267 fixture lines through the binding, no softened asserts |
| `golden/` | The engine's golden fixtures, vendored byte-identical (verified against each release) |
| `examples/{quickstart,hybrid,vector-index,text-search,graph,geo}/` | The examples tour — one runnable `main.go` per concept, `go run` on every CI leg: the README quickstart, hybrid RRF+MMR, the three vector-index families vs exact, BM25 incl. CJK, graph + delete cascade, geo radius/bbox/nearest |
| `docs/PLAN.md` | The binding's plan: architecture ruling, lifetime mapping, pointer discipline, phase scope |

## CI

A linux/macos/windows × Go {1.27.x, 1.26.x} matrix
(`.github/workflows/ci.yml`): fetch + verify the pinned artifacts,
`go vet`, and `go test ./...` (the golden suite) on every leg;
golangci-lint on Linux.

## Surface manifest (docs/SURFACE.tsv)

Every construct of the engine's public surface (the radar-enforced list the
engine publishes as `scripts/bindings/surface.tsv` at each release tag) is
resolved in `docs/SURFACE.tsv`: the Go API exposing it plus the test that
proves it (golden fixture line references), or `N/A` + reason where the v1
binding deliberately does not expose it. `scripts/surface-gate.sh` fails CI
when a line is unresolved, a cell is empty, or the N/A count drifts from the
committed baseline — so an engine pin bump that changes the surface lands in
this gate, not in a user's bug report.

## Versioning

The engine pin lives in one variable in the fetch scripts
(`CORVID_VERSION=v0.3.1`). Artifacts always come from that exact tag's
GitHub release and are sha256-verified; `deps/` is never committed.

## License

MIT — see [LICENSE](LICENSE).
