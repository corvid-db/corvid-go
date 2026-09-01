// db.go — the Db handle: open/close, collections, admin.
//
// Lifetime mapping (docs/PLAN.md): each corvid_db handle becomes a Go
// Db with an explicit Close (idempotent) and a runtime.SetFinalizer
// documented as a BACKSTOP only — Close deliberately, the finalizer is
// there so a leaked Db cannot pin engine memory forever. Db is safe
// for concurrent use from multiple goroutines (FFI.md §6: reads run
// concurrently, writes are serialized by the engine). The API is
// synchronous on purpose: the engine has no async surface, so
// context.Context would be a lie.

package corvid

import (
	"runtime"
	"sync"
)

// errClosedDb is returned by operations on a closed Db.
var errClosedDb = newErr(ErrDatabase, "database is closed")

// Db is an open corvid database (file-backed via Open, in-memory via
// OpenMemory). It is safe for concurrent use. Call Close when done;
// the finalizer is only a backstop.
//
// Close caveat (FFI.md §6): close only after every concurrent
// operation on this Db has completed — freeing the engine handle while
// another thread is inside a call on it is undefined behavior. The
// checkOpen gate that rejects calls on a closed Db is TOCTOU by
// design (a loud use-after-close rejection, not a lock); sequencing
// Close against in-flight calls is the caller's contract.
type Db struct {
	mu     sync.Mutex
	c      *cDB
	ks     *keySet
	closed bool
}

// ffiVersionWanted is the ABI generation this binding speaks (FFI.md
// §4.1); Open verifies the loaded library matches before anything
// else.
const ffiVersionWanted uint32 = 1

// Open opens (or creates) a file-backed database at path.
func Open(path string) (*Db, error) {
	return openDB(func() (*cDB, error) { return cOpen(path) })
}

// OpenMemory opens a private in-memory database.
func OpenMemory() (*Db, error) {
	return openDB(func() (*cDB, error) { return cOpenMemory() })
}

func openDB(open func() (*cDB, error)) (*Db, error) {
	if v := FFIVersion(); v != ffiVersionWanted {
		return nil, newErr(ErrIncompatibleFormat, "corvid: FFI version %d, this binding speaks %d", v, ffiVersionWanted)
	}
	c, err := open()
	if err != nil {
		return nil, err
	}
	db := &Db{c: c, ks: newKeySet()}
	runtime.SetFinalizer(db, (*Db).Close)
	return db, nil
}

// Close closes the database (idempotent). Derived Collection handles
// keep the engine alive through their own reference (FFI.md §2); close
// them too. The runtime finalizer calls Close as a backstop if a Db is
// leaked — treat explicit Close as the only supported path.
func (db *Db) Close() error {
	if db == nil {
		return nil
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	db.closed = true
	runtime.SetFinalizer(db, nil)
	return cClose(db.c)
}

func (db *Db) checkOpen() error {
	db.mu.Lock()
	closed := db.closed
	db.mu.Unlock()
	if closed {
		return errClosedDb
	}
	return nil
}

// Collection acquires a handle to the named collection (created on
// first write). Reserved and invalid names surface at write time, not
// here — exactly like the ABI (FFI.md §4.2). The returned Collection
// is safe for concurrent use; Close it when finished.
func (db *Db) Collection(name string) (*Collection, error) {
	if err := db.checkOpen(); err != nil {
		return nil, err
	}
	c, err := cCollection(db.c, name)
	if err != nil {
		return nil, err
	}
	coll := &Collection{c: c, db: db, name: name}
	runtime.SetFinalizer(coll, (*Collection).Close)
	return coll, nil
}

// Collections lists the database's collection names in engine order.
func (db *Db) Collections() ([]string, error) {
	if err := db.checkOpen(); err != nil {
		return nil, err
	}
	return cCollections(db.c)
}

// Dump writes a portable dump of the whole database to path.
func (db *Db) Dump(path string) error {
	if err := db.checkOpen(); err != nil {
		return err
	}
	return cDump(db.c, path)
}

// Load merges a dump file (as written by Dump) into this database.
func (db *Db) Load(path string) error {
	if err := db.checkOpen(); err != nil {
		return err
	}
	return cLoad(db.c, path)
}

// LoadWithRenames merges a dump file into this database, renaming
// source collections on the fly. A rename to a reserved or invalid
// name fails with ErrReservedCollection / ErrInvalidName before the
// stream is read (FFI.md §4.13).
func (db *Db) LoadWithRenames(path string, renames map[string]string) error {
	if err := db.checkOpen(); err != nil {
		return err
	}
	return cLoadRenames(db.c, path, renames)
}

// Backup copies the database file to path (which must not already
// exist — ErrBackupTargetExists otherwise). Safe while writers are
// active.
func (db *Db) Backup(path string) error {
	if err := db.checkOpen(); err != nil {
		return err
	}
	return cBackup(db.c, path)
}

// Compact reclaims dead data; movedOut reports whether anything moved.
// The engine answers ErrBusy while derived collection handles are
// live: close them first (FFI.md §4.13).
func (db *Db) Compact() (movedOut bool, err error) {
	if err := db.checkOpen(); err != nil {
		return false, err
	}
	return cCompact(db.c)
}
