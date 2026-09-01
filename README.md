# corvid-go

The Go binding for [corvid](https://github.com/corvid-db/corvid) — an
embedded database with a typed C ABI. It links the engine's **published
FFI artifacts** (the platform cdylib and `corvid.h`) over **cgo** and
carries an idiomatic Go API on top — and it proves, continuously and
outside the engine repo, that the published artifacts drive a real Go
consumer to the same verdicts the engine's own suite produces: the
golden-suite port in `golden_test.go` replays the engine's 256-line
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
- **One exact engine pin** — `v0.2.1`, living in one variable per fetch
  script (`CORVID_VERSION` in `fetch.sh`, `$CorvidVersion` in
  `fetch.ps1`), stamped into `deps/version.txt`.
- **No vendored binaries in git** (`deps/` is gitignored) and **no
  network at build time.**
- **Published-artifact defects are findings**, never local patches.

## Quick start

Requirements: Go ≥ 1.22, a C compiler (CGO enabled — the default when
one is present), `curl` + `shasum`/`sha256sum` (macOS/Linux) or
PowerShell 5+ (Windows).

```sh
make deps          # fetch + verify corvid v0.2.1 into deps/current
go test ./...      # the golden suite (256 executable lines, 8 fixtures)
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
runtime finalizers are backstops only.

## Documents, maps, and the v0.2.1 ABI boundary

The v0.2.1 C ABI has **no map-key iterator** — a stored map is readable
only by known key. Consequences in this binding (either-correct-or-
loud, never silently truncated — details in
[docs/PLAN.md](docs/PLAN.md#the-v1-boundary-map-key-enumeration)):

- `Get`/`Scan`/`Page` decode map documents through a candidate-key set
  fed by everything written through this binding (plus declared
  schemas). On a database with pre-existing data, a map with unknown
  keys fails with an error wrapping `corvid.ErrMapKeyEnumeration`.
- `GetFields(key, fields...)` (explicit-field read) and
  `Query.Select(fields...)` (projection — the only shape in which
  `Run` materializes `Row.Doc`) never need the oracle and work on any
  database. Without `Select`, `Row.Doc` is nil: carry the key, read
  the document explicitly.
- Non-map values (arrays, vectors, bytes, scalars) always decode
  completely.

The upstream `corvid_value_map_keys` ABI append will collapse this
machinery into a plain decode.

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
| `golden_test.go` | The golden-suite port — 256 fixture lines through the binding, no softened asserts |
| `golden/` | The engine's golden fixtures, vendored byte-identical (verified against each release) |
| `docs/PLAN.md` | The binding's plan: architecture ruling, lifetime mapping, pointer discipline, phase scope |

## CI

A linux/macos/windows matrix (`.github/workflows/ci.yml`): fetch +
verify the pinned artifacts, `go vet`, and `go test ./...` (the golden
suite) on every leg; golangci-lint on Linux.

## Versioning

The engine pin lives in one variable in the fetch scripts
(`CORVID_VERSION=v0.2.1`). Artifacts always come from that exact tag's
GitHub release and are sha256-verified; `deps/` is never committed.

## License

MIT — see [LICENSE](LICENSE).
