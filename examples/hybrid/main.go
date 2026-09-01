// hybrid — the flagship: filter + vector + BM25, RRF fusion, MMR
// rerank, limit.
//
// Hybrid retrieval over a 4-document corpus: a pre-ranking `kind`
// filter, a vector (ANN) source and a BM25 text source, both
// contributing top-2 candidate lists, fused with Reciprocal Rank
// Fusion (k = 60) and reranked for diversity with MMR (lambda = 1.0),
// capped at 2 rows. The printed scores are RRF rank sums: s1 is rank 1
// of both sources (1/61 + 1/61 = 2/61), s3 rank 2 of both (2/62).
//
// Run: go run ./examples/hybrid   (after `make deps`)
package main

import (
	"fmt"

	"github.com/corvid-db/corvid-go"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// docs:begin:hybrid
func main() {
	db, err := corvid.OpenMemory()
	if err != nil {
		panic(err)
	}
	defer func() { must(db.Close()) }()

	docs, err := db.Collection("docs")
	if err != nil {
		panic(err)
	}
	defer docs.Close()

	must(docs.Insert([]byte("s1"), map[string]any{
		"kind": "doc", "body": "rust embedded database",
		"v": []float32{1.0, 0.0},
	}))
	must(docs.Insert([]byte("s2"), map[string]any{
		"kind": "doc", "body": "python web frameworks",
		"v": []float32{0.0, 1.0},
	}))
	must(docs.Insert([]byte("s3"), map[string]any{
		"kind": "doc", "body": "rust again database",
		"v": []float32{0.9, 0.1},
	}))
	must(docs.Insert([]byte("m1"), map[string]any{"kind": "meta"})) // filtered out below

	// The flagship query: filter + vector + text, RRF + MMR + limit.
	rows, err := docs.Query().
		Filter(corvid.Field("kind").Eq("doc")).
		Vector("v", []float32{1.0, 0.0}, 2, corvid.MetricCosine).
		Text("body", "rust database", 2).
		FuseRRF(60).
		RerankMMR(1.0).
		Limit(2).
		Select("body").
		Run()
	if err != nil {
		panic(err)
	}
	for rank, r := range rows {
		fmt.Printf("%d. %s score=%.6f %v\n", rank+1, r.Key, r.Score, r.Doc)
	}
}

// docs:end:hybrid
