// collection.go — the Collection handle: mutations, reads, TTL,
// indexes, schema, graph, geo.
//
// Collection is safe for concurrent use from multiple goroutines
// (FFI.md §6). Documents cross as Go values (values.go mapping); keys
// are []byte. Errors are always *CorvidError values, never panics.
// Close releases the engine handle (idempotent; finalizer backstop).

package corvid

import (
	"runtime"
	"sync"
)

// Collection is a handle to one collection of a Db. It is safe for
// concurrent use, under the same §6 close caveat as Db: Close only
// after every concurrent operation on it has completed — freeing the
// engine handle while another thread is inside a call on it is
// undefined behavior, and the Db's checkOpen gate is TOCTOU by design.
type Collection struct {
	mu   sync.Mutex
	c    *cColl
	db   *Db
	name string
}

// Close releases the collection handle. Idempotent; a runtime
// finalizer performs it as a backstop for leaked handles. Note that
// live collection handles make Db.Compact answer ErrBusy.
func (coll *Collection) Close() {
	if coll == nil {
		return
	}
	coll.mu.Lock()
	defer coll.mu.Unlock()
	runtime.SetFinalizer(coll, nil)
	cCollFree(coll.c)
}

// Name returns the collection's name (the handle's own record).
func (coll *Collection) Name() string {
	coll.mu.Lock()
	defer coll.mu.Unlock()
	return cCollName(coll.c)
}

// Insert stores doc under key, replacing any previous document.
func (coll *Collection) Insert(key []byte, doc any) error {
	v, err := encodeValue(doc)
	if err != nil {
		return err
	}
	defer cValueFree(v)
	coll.db.ks.remember(doc)
	return cInsert(coll.c, key, v)
}

// PutMany stores key/doc pairs in ONE transaction: either all pairs
// land or the batch rolls back (a schema violation anywhere fails the
// whole call with nothing stored).
func (coll *Collection) PutMany(keys [][]byte, docs []any) error {
	if len(keys) != len(docs) {
		return newErr(ErrArgument, "PutMany: %d keys but %d documents", len(keys), len(docs))
	}
	vals := make([]*cVal, len(docs))
	for i, d := range docs {
		v, err := encodeValue(d)
		if err != nil {
			for _, done := range vals[:i] {
				cValueFree(done)
			}
			return err
		}
		vals[i] = v
	}
	defer func() {
		for _, v := range vals {
			cValueFree(v)
		}
	}()
	for _, d := range docs {
		coll.db.ks.remember(d)
	}
	return cPutMany(coll.c, keys, vals)
}

// InsertAuto stores doc under a fresh engine-generated key (20-digit,
// zero-padded, strictly monotonic per collection) and returns it.
func (coll *Collection) InsertAuto(doc any) ([]byte, error) {
	v, err := encodeValue(doc)
	if err != nil {
		return nil, err
	}
	defer cValueFree(v)
	coll.db.ks.remember(doc)
	return cInsertAuto(coll.c, v)
}

// InsertTTL stores doc under key with an expiry instant (Unix
// seconds). A plain Insert over the key later clears the expiry.
func (coll *Collection) InsertTTL(key []byte, doc any, expiresAt int64) error {
	v, err := encodeValue(doc)
	if err != nil {
		return err
	}
	defer cValueFree(v)
	coll.db.ks.remember(doc)
	return cInsertTTL(coll.c, key, v, expiresAt)
}

// SetTTL sets (or moves) the expiry instant of an existing key.
func (coll *Collection) SetTTL(key []byte, expiresAt int64) error {
	return cSetTTL(coll.c, key, expiresAt)
}

// GetTTL reports a key's expiry instant, if any.
func (coll *Collection) GetTTL(key []byte) (expiresAt int64, has bool, err error) {
	return cGetTTL(coll.c, key)
}

// PurgeExpired removes every key whose expiry instant is ≤ now
// (inclusive boundary) and returns how many went.
func (coll *Collection) PurgeExpired(now int64) (int, error) {
	return cPurgeExpired(coll.c, now)
}

// Patch merges patch (a map) into the document at key, creating it
// when absent.
func (coll *Collection) Patch(key []byte, patch any) error {
	v, err := encodeValue(patch)
	if err != nil {
		return err
	}
	defer cValueFree(v)
	coll.db.ks.remember(patch)
	return cPatch(coll.c, key, v)
}

// Update runs a read-modify-write on key under the engine's
// consistency: fn receives the current document (nil when absent,
// decoded per the values.go mapping) and returns the replacement, or
// nil to delete the key. Returning an error aborts with
// ErrArgument and writes nothing. The callback must not call back
// into the engine (FFI.md §1.6).
func (coll *Collection) Update(key []byte, fn func(current any) (any, error)) error {
	return cUpdate(coll.c, coll.db.ks, key, fn)
}

// CompareAndSet atomically tests-and-sets key: applied reports whether
// expected matched. A nil expected means "key must be absent"; a nil
// replacement means "delete on match". Equality is the engine's
// semantic equality (NaN == NaN regardless of payload, -0.0 == 0.0).
func (coll *Collection) CompareAndSet(key []byte, expected, replacement any) (applied bool, err error) {
	var ex, re *cVal
	if expected != nil {
		v, err := encodeValue(expected)
		if err != nil {
			return false, err
		}
		defer cValueFree(v)
		ex = v
	}
	if replacement != nil {
		v, err := encodeValue(replacement)
		if err != nil {
			return false, err
		}
		defer cValueFree(v)
		re = v
	}
	return cCompareAndSet(coll.c, key, ex, re)
}

// Delete removes key, reporting whether it existed.
func (coll *Collection) Delete(key []byte) (existed bool, err error) {
	return cDelete(coll.c, key)
}

// DeleteWhere removes every document matching pred (which the call
// consumes, even on failure) and returns how many went.
func (coll *Collection) DeleteWhere(pred *Predicate) (removed int, err error) {
	if pred == nil {
		return 0, newErr(ErrArgument, "DeleteWhere: nil predicate")
	}
	defer pred.markConsumed()
	if pred.err != nil {
		return 0, pred.err
	}
	return cDeleteWhere(coll.c, pred.c)
}

// DeleteBatch removes the given keys in one call, returning how many
// existed.
func (coll *Collection) DeleteBatch(keys ...[]byte) (removed int, err error) {
	return cDeleteBatch(coll.c, keys)
}

// Get returns the document at key (nil, nil when absent), decoded per
// the values.go mapping. Map documents decode through the candidate-
// key oracle: on a database with data not written through this
// binding, a map whose keys are not all known fails with an error
// wrapping ErrMapKeyEnumeration — use GetFields for explicit-field
// reads there (see values.go).
func (coll *Collection) Get(key []byte) (doc any, err error) {
	v, err := cGet(coll.c, key)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	defer cValueFree(v)
	return decodeValue(v.h, coll.db.ks)
}

// GetFields returns the named fields (dot paths; all-digit segments
// index arrays) of the document at key, as a map holding exactly the
// fields that are present. Field paths are the key source, so this
// never needs the map-key oracle and works on any database.
func (coll *Collection) GetFields(key []byte, fields ...string) (map[string]any, error) {
	v, err := cGet(coll.c, key)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return map[string]any{}, nil
	}
	defer cValueFree(v)
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		child := walkCHandlePath(v.h, f)
		if child == nil {
			continue
		}
		d, err := decodeValue(child, coll.db.ks)
		if err != nil {
			return nil, err
		}
		out[f] = d
	}
	return out, nil
}

// Len returns the number of live documents.
func (coll *Collection) Len() (int, error) {
	return cLen(coll.c)
}

// Scan streams every key/document pair in key order. fn returning
// false stops the scan early (not an error). Documents decode through
// the candidate-key oracle (see Get).
func (coll *Collection) Scan(fn func(key []byte, doc any) bool) error {
	return cScan(coll.c, coll.db.ks, fn)
}

// Page returns up to limit rows ordered by key, starting after the
// after cursor (nil to start at the beginning), plus the next cursor
// (nil means the end was reached). Pass the returned cursor back as
// after to resume.
func (coll *Collection) Page(after []byte, limit int) (rows []Row, next []byte, err error) {
	rowsH, next, err := cPage(coll.c, after, limit)
	if err != nil {
		return nil, nil, err
	}
	defer cRowsFree(rowsH)
	for {
		key, docH, score, ok := cRowsNext(rowsH)
		if !ok {
			break
		}
		doc, err := decodeValue(docH, coll.db.ks)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, Row{Key: key, Doc: doc, Score: score})
	}
	return rows, next, nil
}

// ---------------------------------------------------------------------------
// Indexes (FFI.md §4.10). Existing documents train a new index
// immediately; PQ variants fail with ErrEmptyIndexTraining when the
// field has no vectors or dim % subspaces != 0.
// ---------------------------------------------------------------------------

// CreateScalarIndex indexes a scalar field for comparisons.
func (coll *Collection) CreateScalarIndex(field string) error {
	return cCreateScalarIndex(coll.c, field)
}

// CreateCompoundIndex indexes the concatenation of fields.
func (coll *Collection) CreateCompoundIndex(fields ...string) error {
	return cCreateCompoundIndex(coll.c, fields)
}

// CreateTextIndex indexes a text field for BM25 queries (in-memory).
func (coll *Collection) CreateTextIndex(field string) error {
	return cCreateTextIndex(coll.c, field)
}

// CreateTextIndexOnDisk is CreateTextIndex with an on-disk index.
func (coll *Collection) CreateTextIndexOnDisk(field string) error {
	return cCreateTextIndexOnDisk(coll.c, field)
}

// CreateGeoIndex indexes a geo field (lat/lon array or map) for
// radius/bbox/nearest queries.
func (coll *Collection) CreateGeoIndex(field string) error {
	return cCreateGeoIndex(coll.c, field)
}

// CreateVectorIndex indexes a vector field under metric.
func (coll *Collection) CreateVectorIndex(field string, metric Metric) error {
	return cCreateVectorIndex(coll.c, field, metric)
}

// CreateVectorIndexQuantized indexes a vector field with storage
// quantization.
func (coll *Collection) CreateVectorIndexQuantized(field string, metric Metric, quant Quant) error {
	return cCreateVectorIndexQuantized(coll.c, field, metric, quant)
}

// CreateVectorIndexOnDisk is CreateVectorIndex with an on-disk index.
func (coll *Collection) CreateVectorIndexOnDisk(field string, metric Metric) error {
	return cCreateVectorIndexOnDisk(coll.c, field, metric)
}

// CreateVectorIndexOnDiskQuantized combines on-disk and quantization.
func (coll *Collection) CreateVectorIndexOnDiskQuantized(field string, metric Metric, quant Quant) error {
	return cCreateVectorIndexOnDiskQuantized(coll.c, field, metric, quant)
}

// CreateVectorIndexPQ trains a product-quantization index (subspaces
// × centroids codebooks).
func (coll *Collection) CreateVectorIndexPQ(field string, metric Metric, subspaces, centroids int) error {
	return cCreateVectorIndexPQ(coll.c, field, metric, subspaces, centroids)
}

// CreateVectorIndexOnDiskPQ is CreateVectorIndexPQ with an on-disk
// index.
func (coll *Collection) CreateVectorIndexOnDiskPQ(field string, metric Metric, subspaces, centroids int) error {
	return cCreateVectorIndexOnDiskPQ(coll.c, field, metric, subspaces, centroids)
}

// ---------------------------------------------------------------------------
// Schema (FFI.md §4.10)
// ---------------------------------------------------------------------------

// SetSchema declares (or replaces) the collection's schema. Its field
// names also join the Db's candidate key set for map decoding.
func (coll *Collection) SetSchema(defs ...FieldDef) error {
	coll.db.ks.addAll(fieldDefNames(defs))
	return cSetSchema(coll.c, defs)
}

// Schema returns the declared schema (nil when none is declared).
func (coll *Collection) Schema() ([]FieldDef, error) {
	it, err := cSchema(coll.c)
	if err != nil {
		return nil, err
	}
	if it == nil {
		return nil, nil
	}
	defer cSchemaIterFree(it)
	var defs []FieldDef
	for {
		fd, ok := cSchemaIterNext(it)
		if !ok {
			break
		}
		defs = append(defs, fd)
	}
	return defs, nil
}

func fieldDefNames(defs []FieldDef) []string {
	if len(defs) == 0 {
		return nil
	}
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}

// ---------------------------------------------------------------------------
// Graph (FFI.md §4.11)
// ---------------------------------------------------------------------------

// Link adds a directed edge from→to under relation.
func (coll *Collection) Link(from []byte, relation string, to []byte) error {
	return cLink(coll.c, from, relation, to)
}

// LinkWeighted adds a directed weighted edge.
func (coll *Collection) LinkWeighted(from []byte, relation string, to []byte, weight float64) error {
	return cLinkWeighted(coll.c, from, relation, to, weight)
}

// Unlink removes one edge, reporting whether it existed. Deleting a
// key always cascades its edges.
func (coll *Collection) Unlink(from []byte, relation string, to []byte) (removed bool, err error) {
	return cUnlink(coll.c, from, relation, to)
}

// Neighbors lists the outgoing targets of from under relation, in
// engine (key) order.
func (coll *Collection) Neighbors(from []byte, relation string) ([][]byte, error) {
	return cNeighbors(coll.c, from, relation)
}

// InNeighbors lists the incoming sources of to under relation.
func (coll *Collection) InNeighbors(to []byte, relation string) ([][]byte, error) {
	return cInNeighbors(coll.c, to, relation)
}

// NeighborsWeighted lists the outgoing weighted edges of from.
func (coll *Collection) NeighborsWeighted(from []byte, relation string) ([]Weighted, error) {
	hits, err := cNeighborsWeighted(coll.c, from, relation)
	if err != nil {
		return nil, err
	}
	out := make([]Weighted, len(hits))
	for i, h := range hits {
		out[i] = Weighted(h) // identical layout: Key, Weight
	}
	return out, nil
}

// Traverse walks the relation graph breadth-first up to hops levels,
// de-duplicated, cycle-safe.
func (coll *Collection) Traverse(start []byte, relation string, hops int) ([][]byte, error) {
	return cTraverse(coll.c, start, relation, hops)
}

// ---------------------------------------------------------------------------
// Geo (FFI.md §4.12). Hits come back nearest-first (radius/nearest) or
// in key order (bbox), with haversine kilometres.
// ---------------------------------------------------------------------------

// GeoWithinRadius returns the documents within radiusKm of
// (lat, lon), nearest first, ties by key, boundary inclusive.
func (coll *Collection) GeoWithinRadius(field string, lat, lon, radiusKm float64) ([]GeoHit, error) {
	h, err := cGeoWithinRadius(coll.c, field, lat, lon, radiusKm)
	if err != nil {
		return nil, err
	}
	defer cGeoFree(h)
	return coll.walkGeoHits(h)
}

// GeoWithinBBox returns the documents inside the axis-aligned box
// (inclusive bounds). Inverted boxes fail with ErrArgument.
func (coll *Collection) GeoWithinBBox(field string, minLat, minLon, maxLat, maxLon float64) ([]GeoHit, error) {
	h, err := cGeoWithinBBox(coll.c, field, minLat, minLon, maxLat, maxLon)
	if err != nil {
		return nil, err
	}
	defer cGeoFree(h)
	return coll.walkGeoHits(h)
}

// GeoNearest returns the k documents nearest to (lat, lon).
func (coll *Collection) GeoNearest(field string, lat, lon float64, k int) ([]GeoHit, error) {
	h, err := cGeoNearest(coll.c, field, lat, lon, k)
	if err != nil {
		return nil, err
	}
	defer cGeoFree(h)
	return coll.walkGeoHits(h)
}

func (coll *Collection) walkGeoHits(h *cGeoHits) ([]GeoHit, error) {
	var out []GeoHit
	for {
		hit, _, ok := cGeoNext(h)
		if !ok {
			break
		}
		g := GeoHit{Key: hit.Key, DistanceKm: hit.Dist}
		if hit.HasDoc {
			doc, err := decodeValue(hit.Doc, coll.db.ks) // borrowed: decode before the next step
			if err != nil {
				return nil, err
			}
			g.Doc = doc
		}
		out = append(out, g)
	}
	return out, nil
}
