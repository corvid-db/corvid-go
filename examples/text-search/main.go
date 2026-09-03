// text-search — BM25 ranking, English and CJK.
//
// Six notes (three English, three CJK) searched through a text index
// with the query builder's BM25 source. Row scores are RRF ranks
// (1/(60 + rank)); the *order* is the BM25 ranking.
//
// The CJK strings exercise the engine's dictionary-free CJK
// segmentation: maximal runs of CJK characters are tokenized as
// sliding BIGRAMS (「东京」… → "东京", …), so an unsegmented CJK query
// matches by its bigrams — "城市" (city) matches both city notes,
// "数据库" (database) matches the ML note.
//
// Phrase matching: engine v0.3.0 added the DIRECT positional
// `corvid_phrase_search` to the ABI (consecutive in-order analyzed
// tokens, stop words collapsing out of adjacency), surfaced here as
// (*Collection).PhraseSearch — Row.Score is the BM25 phrase sum, not
// the builder's fused RRF scale.
//
// Run: go run ./examples/text-search   (after `make deps`)
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

var corpus = []struct {
	key  string
	body string
}{
	{"n1", "the quick brown fox jumps over the lazy dog"},
	{"n2", "a quick red fox leaps over a sleeping dog"},
	{"n3", "slow green turtle crosses the road"},
	{"n4", "东京是一座巨大的城市"},  // Tokyo is a huge city
	{"n5", "大阪是关西最大的城市"},  // Osaka is Kansai's biggest city
	{"n6", "机器学习正在改变数据库"}, // ML is changing databases
}

func search(notes *corvid.Collection, query, label string) {
	rows, err := notes.Query().Text("body", query, 3).Run()
	if err != nil {
		panic(err)
	}
	fmt.Printf("%-28s ->", label)
	for _, r := range rows {
		fmt.Printf(" %s(%.6f)", r.Key, r.Score)
	}
	fmt.Println()
}

func phrase(notes *corvid.Collection, query, label string) {
	rows, err := notes.PhraseSearch("body", query, 3)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%-28s ->", label)
	for _, r := range rows {
		fmt.Printf(" %s(%.6f)", r.Key, r.Score)
	}
	fmt.Println()
}

// docs:begin:text_search
func main() {
	db, err := corvid.OpenMemory()
	if err != nil {
		panic(err)
	}
	defer func() { must(db.Close()) }()

	notes, err := db.Collection("notes")
	if err != nil {
		panic(err)
	}
	defer notes.Close()

	for _, n := range corpus {
		must(notes.Insert([]byte(n.key), map[string]any{"body": n.body}))
	}
	must(notes.CreateTextIndex("body"))

	search(notes, "quick fox", `bm25 "quick fox":`)
	search(notes, "quick dog", `bm25 "quick dog":`)
	search(notes, "城市", "bm25 CJK 城市 (city):")
	search(notes, "数据库", "bm25 CJK 数据库 (database):")

	phrase(notes, "fox jumps over", `phrase "fox jumps over":`)
	phrase(notes, "over jumps fox", `phrase "over jumps fox" (reversed — no match):`)
	phrase(notes, "leaps over a sleeping", `phrase with stop words collapsed:`)
}

// docs:end:text_search
