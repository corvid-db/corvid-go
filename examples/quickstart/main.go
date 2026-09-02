// quickstart — the README tour as a runnable file.
//
// Open an in-memory database, create a collection, insert three small
// documents carrying 2-d embeddings, run a kNN vector query under
// cosine, and print the ranked rows. Close what you opened.
//
// Run: go run ./examples/quickstart   (after `make deps`)
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

// docs:begin:quickstart
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

	must(docs.Insert([]byte("p1"), map[string]any{
		"title": "rust embedded database", "kind": "doc",
		"v": []float32{1.0, 0.0},
	}))
	must(docs.Insert([]byte("p2"), map[string]any{
		"title": "python web frameworks", "kind": "doc",
		"v": []float32{0.0, 1.0},
	}))
	must(docs.Insert([]byte("p3"), map[string]any{
		"title": "rust again database", "kind": "doc",
		"v": []float32{0.9, 0.1},
	}))

	// kNN: the 3 nearest documents to (1, 0) under cosine. Row.Doc is
	// materialized only under Select — retrieval rows carry keys and
	// scores, so select the field the printout needs.
	rows, err := docs.Query().
		Vector("v", []float32{1.0, 0.0}, 3, corvid.MetricCosine).
		Select("title").
		Run()
	if err != nil {
		panic(err)
	}
	for rank, r := range rows {
		fmt.Printf("%d. %s score=%.6f %v\n", rank+1, r.Key, r.Score, r.Doc)
	}
}

// docs:end:quickstart
