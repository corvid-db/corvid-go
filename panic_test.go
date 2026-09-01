// panic_test.go — the §1.6 callback-panic contract.
//
// The scan/update trampolines recover a panic in the user closure (the
// runtime cannot unwind through the C frames), stash the value on the
// job, and stop the engine call at the ABI level; the Scan/Update call
// sites re-panic once the engine call has returned. These tests pin
// both halves: the panic value surfaces at the CALL SITE (not a
// runtime fatal, not a Go error), and the engine is left in a
// consistent, usable state afterwards.

package corvid

import "testing"

func openTestColl(t *testing.T) (*Db, *Collection) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	coll, err := db.Collection("docs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(coll.Close)
	return db, coll
}

func TestScanPanicSurfacesAtCallSite(t *testing.T) {
	_, coll := openTestColl(t)
	doc := map[string]any{"k": 1}
	for _, k := range []string{"a", "b", "c"} {
		if err := coll.Insert([]byte(k), doc); err != nil {
			t.Fatal(err)
		}
	}
	visited := 0
	caught, panicked := func() (p any, panicked bool) {
		defer func() {
			p, panicked = recover(), true
		}()
		_ = coll.Scan(func(key []byte, doc any) bool { // panics inside; the blank assign appeases errcheck
			visited++
			panic("scan-closure-boom")
		})
		return nil, false
	}()
	if !panicked {
		t.Fatal("a panicking scan closure did not surface at Scan")
	}
	if caught != "scan-closure-boom" {
		t.Fatalf("panic value %v, want scan-closure-boom", caught)
	}
	if visited != 1 {
		t.Fatalf("visited %d documents after the panic, want the first only", visited)
	}
	// The engine saw a clean early-stop, not a broken call: it must
	// still answer normally.
	n, err := coll.Len()
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("Len() = %d after the panicked scan, want 3", n)
	}
	if _, err := coll.Get([]byte("b")); err != nil {
		t.Fatalf("Get after the panicked scan: %v", err)
	}
}

func TestUpdatePanicSurfacesAtCallSite(t *testing.T) {
	_, coll := openTestColl(t)
	orig := map[string]any{"n": 1}
	if err := coll.Insert([]byte("k"), orig); err != nil {
		t.Fatal(err)
	}
	caught, panicked := func() (p any, panicked bool) {
		defer func() {
			p, panicked = recover(), true
		}()
		_ = coll.Update([]byte("k"), func(current any) (any, error) { // panics inside; the blank assign appeases errcheck
			panic("update-closure-boom")
		})
		return nil, false
	}()
	if !panicked {
		t.Fatal("a panicking update closure did not surface at Update")
	}
	if caught != "update-closure-boom" {
		t.Fatalf("panic value %v, want update-closure-boom", caught)
	}
	// The engine saw the aborting-callback status (nothing written);
	// the key must still hold its original document and answer reads.
	got, err := coll.Get([]byte("k"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(map[string]any)
	if !ok || len(m) != 1 || m["n"] != int64(1) {
		t.Fatalf("document after the panicked update = %#v, want the original {n:1}", got)
	}
}
