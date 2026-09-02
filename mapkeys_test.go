// mapkeys_test.go — the real key iterator across a reopen.
//
// corvid_value_map_keys (engine v0.3.0) retired the v0.2.2-era
// candidate-key oracle this binding shipped at bootstrap (a per-Db
// remembered key set; unknown keys failed Get loudly with
// ErrMapKeyEnumeration). This is the scenario that oracle could NOT
// do and the one it existed for: a FRESH Db over a file this process
// never wrote decodes a map whose keys were never declared anywhere —
// completely, nested keys included, no write needed first. The
// VMAP_KEYS/GET_KEYS golden lines (golden/values.txt,
// golden/mutations.txt) pin the iterator's order and inert non-map
// shapes op by op; this test pins the collapse end to end.

package corvid

import (
	"path/filepath"
	"testing"
)

func TestMapKeysDecodeAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapkeys.redb")
	doc := map[string]any{
		"alpha": 1,
		"beta":  "two",
		"nested": map[string]any{
			"inner": []any{map[string]any{"deep": true}},
			"键":     float64(7), // a UTF-8 key no oracle ever saw
		},
	}

	// Db A writes the document and closes.
	dbA, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	collA, err := dbA.Collection("docs")
	if err != nil {
		t.Fatal(err)
	}
	if err := collA.Insert([]byte("k"), doc); err != nil {
		t.Fatal(err)
	}
	collA.Close()
	if err := dbA.Close(); err != nil {
		t.Fatal(err)
	}

	// Same file, FRESH Db, no write first: Get decodes every key.
	dbB, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dbB.Close() }()
	collB, err := dbB.Collection("docs")
	if err != nil {
		t.Fatal(err)
	}
	defer collB.Close()

	got, err := collB.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get on a fresh Db over an existing file: %v (the oracle's old loud failure must be gone)", err)
	}
	m, ok := got.(map[string]any)
	if !ok || len(m) != 3 || m["alpha"] != int64(1) || m["beta"] != "two" {
		t.Fatalf("decoded document = %#v, want all three top-level keys back", got)
	}
	nested, ok := m["nested"].(map[string]any)
	if !ok || len(nested) != 2 {
		t.Fatalf("nested map = %#v, want both keys back", m["nested"])
	}
	if nested["键"] != float64(7) {
		t.Fatalf("nested UTF-8 key = %#v, want 7", nested["键"])
	}
	inner, ok := nested["inner"].([]any)
	if !ok || len(inner) != 1 {
		t.Fatalf("nested array = %#v, want the deep map back", nested["inner"])
	}
	if dm, ok := inner[0].(map[string]any); !ok || dm["deep"] != true {
		t.Fatalf("deep map = %#v, want {deep:true}", inner[0])
	}
}
