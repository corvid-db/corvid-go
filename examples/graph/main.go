// graph — directed edges over a small corpus, and delete cascade.
//
// Three documents (ga, gb, gc) linked by a `parent_of` relation, plus
// one edge pointing at `gd` which never exists as a document (dangling
// edges are allowed), and a weighted `route` relation. Demonstrates
// neighbors (key order), in-neighbors, weighted neighbors, BFS
// traverse at 1 and 2 hops (cycle-safe), and the delete cascade:
// deleting a key removes its edges in the same transaction — deleting
// the never-a-document `gd` still drops the `gb -> gd` edge (spec
// §4.8/§4.11).
//
// Run: go run ./examples/graph   (after `make deps`)
package main

import (
	"fmt"
	"strings"

	"github.com/corvid-db/corvid-go"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func keysToStrs(keys [][]byte) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = string(k)
	}
	return out
}

func show(label string, keys [][]byte) {
	fmt.Printf("%-36s [%s]\n", label, strings.Join(keysToStrs(keys), " "))
}

func main() {
	db, err := corvid.OpenMemory()
	if err != nil {
		panic(err)
	}
	defer func() { must(db.Close()) }()

	nodes, err := db.Collection("nodes")
	if err != nil {
		panic(err)
	}
	defer nodes.Close()

	for _, key := range []string{"ga", "gb", "gc"} {
		must(nodes.Insert([]byte(key), map[string]any{"n": key}))
	}

	must(nodes.Link([]byte("ga"), "parent_of", []byte("gb")))
	must(nodes.Link([]byte("ga"), "parent_of", []byte("gc")))
	must(nodes.Link([]byte("gb"), "parent_of", []byte("gd"))) // gd never exists as a document
	must(nodes.LinkWeighted([]byte("ga"), "route", []byte("gb"), 2.5))
	must(nodes.LinkWeighted([]byte("ga"), "route", []byte("gd"), 0.75))

	ga, gb := []byte("ga"), []byte("gb")

	if nb, err := nodes.Neighbors(ga, "parent_of"); err != nil {
		panic(err)
	} else {
		show("neighbors(ga)", nb)
	}
	if in, err := nodes.InNeighbors(gb, "parent_of"); err != nil {
		panic(err)
	} else {
		show("in_neighbors(gb)", in)
	}
	if routes, err := nodes.NeighborsWeighted(ga, "route"); err != nil {
		panic(err)
	} else {
		parts := make([]string, len(routes))
		for i, r := range routes {
			parts[i] = fmt.Sprintf("%s=%.2f", r.Key, r.Weight)
		}
		fmt.Printf("%-36s [%s]\n", "routes from ga (weighted):", strings.Join(parts, " "))
	}
	if tr, err := nodes.Traverse(ga, "parent_of", 1); err != nil {
		panic(err)
	} else {
		show("traverse(ga, 1 hop)", tr)
	}
	if tr, err := nodes.Traverse(ga, "parent_of", 2); err != nil {
		panic(err)
	} else {
		show("traverse(ga, 2 hops)", tr)
	}

	// Delete cascade: remove gc (a document) and gd (never a document).
	if existed, err := nodes.Delete([]byte("gc")); err != nil {
		panic(err)
	} else {
		fmt.Println("delete gc: existed =", existed)
	}
	if existed, err := nodes.Delete([]byte("gd")); err != nil {
		panic(err)
	} else {
		fmt.Println("delete gd: existed =", existed,
			"(never a document; its edges still cascade)")
	}

	if nb, err := nodes.Neighbors(ga, "parent_of"); err != nil {
		panic(err)
	} else {
		show("neighbors(ga) after deletes", nb)
	}
	if nb, err := nodes.Neighbors(gb, "parent_of"); err != nil {
		panic(err)
	} else {
		show("neighbors(gb) after deletes", nb)
	}
	if tr, err := nodes.Traverse(ga, "parent_of", 2); err != nil {
		panic(err)
	} else {
		show("traverse(ga, 2 hops) after", tr)
	}
}
