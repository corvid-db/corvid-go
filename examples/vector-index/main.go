// vector-index — three vector-index families, ANN vs exact.
//
// A file-backed database (the on-disk index is a disk-resident HNSW
// graph persisted inside the db file) with eight 4-d documents. The
// same embedding is stored under three fields so each index family can
// be demonstrated side by side:
//
//	vMem  — in-memory HNSW             (CreateVectorIndex)
//	vDisk — on-disk HNSW               (CreateVectorIndexOnDisk)
//	vQ    — in-memory binary-quantized  (CreateVectorIndexQuantized)
//
// The exact (streaming-scan) ranking is printed first, then the ANN
// (Approx) ranking served by each index. The unquantized indexes
// answer identically to the scan on this corpus; the binary-quantized
// one genuinely diverges — the recall/footprint trade-off quantization
// makes (binary packs each float32 to one sign bit, ~32x smaller).
// Finally the db is closed and reopened: the on-disk graph reloads and
// serves the same ANN answer without a rebuild.
//
// Scores are RRF ranks (1/(60 + rank)) — the lone vector source's row
// score — so they reflect each lane's own ranking.
//
// Run: go run ./examples/vector-index   (after `make deps`)
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/corvid-db/corvid-go"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

var corpus = []struct {
	key string
	v   []float32
}{
	{"k0", []float32{1.0, 0.0, 0.0, 0.0}}, // nearest
	{"k1", []float32{0.95, 0.05, 0.0, 0.0}},
	{"k2", []float32{0.0, 1.0, 0.0, 0.0}},
	{"k3", []float32{0.0, 0.9, 0.1, 0.0}},
	{"k4", []float32{0.0, 0.0, 1.0, 0.0}},
	{"k5", []float32{0.7, 0.7, 0.0, 0.0}},
	{"k6", []float32{0.0, 0.0, 0.0, 1.0}},
	{"k7", []float32{0.98, 0.02, 0.0, 0.0}},
}

var probe = []float32{1.0, 0.0, 0.0, 0.0}

func runQuery(items *corvid.Collection, field string, approx bool, label string) {
	q := items.Query().Vector(field, probe, 4, corvid.MetricCosine)
	if approx {
		q = q.Approx()
	}
	rows, err := q.Run()
	if err != nil {
		panic(err)
	}
	fmt.Printf("%-38s", label)
	for _, r := range rows {
		fmt.Printf(" %s(%.6f)", r.Key, r.Score)
	}
	fmt.Println()
}

// docs:begin:vector_index
func main() {
	path := filepath.Join(os.TempDir(), "corvid-go-example-vector-index.redb")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		panic(err)
	} // reruns start clean (single-file db)

	db, err := corvid.Open(path)
	if err != nil {
		panic(err)
	}
	items, err := db.Collection("items")
	if err != nil {
		panic(err)
	}
	for _, c := range corpus {
		must(items.Insert([]byte(c.key), map[string]any{
			"v_mem": c.v, "v_disk": c.v, "v_q": c.v,
		}))
	}
	must(items.CreateVectorIndex("v_mem", corvid.MetricCosine))
	must(items.CreateVectorIndexOnDisk("v_disk", corvid.MetricCosine))
	must(items.CreateVectorIndexQuantized("v_q", corvid.MetricCosine, corvid.QuantBinary))

	fmt.Println("top-4 nearest to (1,0,0,0) under cosine:")
	runQuery(items, "v_mem", false, "exact (scan):")
	runQuery(items, "v_mem", true, "ann in-memory HNSW:")
	runQuery(items, "v_disk", true, "ann on-disk HNSW:")
	runQuery(items, "v_q", true, "ann binary-quantized:")
	fmt.Println("(the quantized lane trades recall for a ~32x smaller index)")

	items.Close()
	must(db.Close())

	// Reopen: the on-disk graph reloads (no rebuild) and answers again.
	db, err = corvid.Open(path)
	if err != nil {
		panic(err)
	}
	items, err = db.Collection("items")
	if err != nil {
		panic(err)
	}
	runQuery(items, "v_disk", true, "ann on-disk after reopen:")
	items.Close()
	must(db.Close())

	must(os.Remove(path))
}

// docs:end:vector_index
