// oracle_test.go — the map-key oracle's loudness across a reopen.
//
// The candidate key set is per-Db and fed only by this binding's write
// paths and declared schemas (values.go). The loud half of that
// contract — a fresh Db over a file this binding itself wrote still
// cannot decode unknown map keys — is what this test pins: Get must
// fail with an error wrapping ErrMapKeyEnumeration (errors.Is), never
// return a silently-truncated map; GetFields (explicit field list, no
// oracle) keeps working; and a write through the new Db re-learns the
// keys, after which Get decodes.

package corvid

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestMapKeyOracleFailsLoudAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oracle.redb")
	doc := map[string]any{"alpha": 1, "beta": "two"}

	// Db A writes the document (its key set learns alpha/beta).
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
	if _, err := collA.Get([]byte("k")); err != nil { // same-Db read: candidates known
		t.Fatalf("Get through the writing Db: %v", err)
	}
	collA.Close()
	if err := dbA.Close(); err != nil {
		t.Fatal(err)
	}

	// Same file, FRESH Db → empty candidate set: Get must fail loudly.
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

	_, err = collB.Get([]byte("k"))
	if !errors.Is(err, ErrMapKeyEnumeration) {
		t.Fatalf("Get on a fresh Db over an existing file: got %v, want an error wrapping ErrMapKeyEnumeration", err)
	}

	// The escape: explicit fields never need the oracle.
	fields, err := collB.GetFields([]byte("k"), "alpha", "beta")
	if err != nil {
		t.Fatalf("GetFields: %v", err)
	}
	if len(fields) != 2 || fields["alpha"] != int64(1) || fields["beta"] != "two" {
		t.Fatalf("GetFields = %#v, want both fields intact", fields)
	}

	// A write through Db B re-learns the keys; Get decodes afterwards.
	if err := collB.Patch([]byte("k"), doc); err != nil {
		t.Fatal(err)
	}
	got, err := collB.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get after a write through this Db: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok || len(m) != 2 || m["alpha"] != int64(1) || m["beta"] != "two" {
		t.Fatalf("decoded document = %#v, want both keys back", got)
	}
}
