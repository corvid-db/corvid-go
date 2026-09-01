// cgo.go — THE cgo layer of the corvid-go binding.
//
// Every line of C interop in this package lives in this one file: the
// #include of the published corvid.h, the two FFI.md §1.6 callback
// bridges, Go-safe wrappers over every needed ABI symbol, and the
// C-derived constants (frozen enums, FFI.md §1.3/§1.4/§8). C pointers
// never leave this file: the wrappers take and return Go types (or the
// opaque cXxx structs declared here), and the rest of the package — and
// the entire public API — is pure Go.
//
// Linking: #cgo points at deps/current, the normalized output of
// `make deps` (fetch.sh / fetch.ps1 download the pinned release archive,
// sha256-verify it against the release's checksums.txt, and copy corvid.h
// + the platform cdylib there). The system-install alternative is
// documented in the README (CGO_CFLAGS/CGO_LDFLAGS from pkg-config).
//
// Pointer discipline (docs/PLAN.md):
//   - No Go pointer is EVER stored in C memory. The POD arrays the ABI
//     takes (corvid_kv, parallel key/name arrays, corvid_field_def) are
//     built in C.malloc/C.CBytes memory holding C copies of Go bytes, and
//     freed before the wrapper returns.
//   - Strings/bytes/float slices passed directly as call arguments are
//     passed zero-copy into Go memory: legal under the cgo rules (the
//     memory is pinned for the call's duration and C never retains it;
//     the ABI clones what it keeps). cStr/cBytes/cFloats provide that
//     with a non-NULL sentinel for the empty case (FFI.md §1.5: empty is
//     a non-NULL pointer with length 0).
//   - Everything borrowed back from C (rows/geohits keys and docs, _ref
//     views, callback arguments) is COPIED into Go-owned memory inside
//     the wrapper call — the "copies at the boundary" rule (FFI.md §5).
//   - The scan/update callbacks cross as integer registry ids stored in
//     a C.malloc'd cell; the exported Go trampolines look the closure up
//     by id, so no Go pointer reaches C-allocated memory.

package corvid

/*
#cgo CFLAGS: -I${SRCDIR}/deps/current
#cgo !windows LDFLAGS: -L${SRCDIR}/deps/current -lcorvid -Wl,-rpath,${SRCDIR}/deps/current
#cgo windows LDFLAGS: -L${SRCDIR}/deps/current -lcorvid

#include <stdlib.h>
#include <stdint.h>
#include "corvid.h"

// §1.6 callback bridges. ctx points at a C-allocated cell holding a
// registry id (a plain integer — C never dereferences it as memory).
// The extern prototypes are non-const to match the prototypes cgo
// generates for the //export trampolines; the bridges cast away const
// explicitly, and the Go side only ever performs read-API calls on
// those values (never writes through them).
extern int           corvidgoScanCB(uintptr_t id, uint8_t *key, size_t key_len, corvid_value *doc);
extern corvid_status corvidgoUpdateCB(uintptr_t id, corvid_value *current, corvid_value **out);

static int corvidgo_scan_bridge(void *ctx, const uint8_t *key, size_t key_len,
                                const corvid_value *doc) {
	return corvidgoScanCB(*(const uintptr_t *)ctx, (uint8_t *)key, key_len,
	                      (corvid_value *)doc);
}

static corvid_status corvidgo_update_bridge(void *ctx, const corvid_value *current,
                                            corvid_value **out) {
	return corvidgoUpdateCB(*(const uintptr_t *)ctx, (corvid_value *)current, out);
}

// The engine calls must be issued from C: a preamble-local function
// pointer is not expressible on the Go side of the boundary.
static corvid_status corvidgo_scan_call(corvid_coll *c, void *ctx) {
	return corvid_scan(c, corvidgo_scan_bridge, ctx);
}

static corvid_status corvidgo_update_call(corvid_coll *c, const uint8_t *key,
                                          size_t key_len, void *ctx) {
	return corvid_update(c, key, key_len, corvidgo_update_bridge, ctx);
}
*/
import "C"

import (
	"sync"
	"unsafe"
)

// ---------------------------------------------------------------------------
// C-derived constants (frozen — FFI.md §1.3/§1.4/§8; never renumbered)
// ---------------------------------------------------------------------------

// ErrCode is a detailed corvid error code (FFI.md §1.3). Codes 1–18 map
// 1:1 onto the engine's error variants; 19 (ErrBusy) is FFI-only.
type ErrCode uint32

const (
	ErrNone               ErrCode = C.CORVID_E_OK
	ErrDatabase           ErrCode = C.CORVID_E_DATABASE
	ErrTransaction        ErrCode = C.CORVID_E_TRANSACTION
	ErrTable              ErrCode = C.CORVID_E_TABLE
	ErrStorage            ErrCode = C.CORVID_E_STORAGE
	ErrCommit             ErrCode = C.CORVID_E_COMMIT
	ErrSetDurability      ErrCode = C.CORVID_E_SET_DURABILITY
	ErrCompaction         ErrCode = C.CORVID_E_COMPACTION
	ErrDecode             ErrCode = C.CORVID_E_DECODE
	ErrCorruptIndex       ErrCode = C.CORVID_E_CORRUPT_INDEX
	ErrReservedCollection ErrCode = C.CORVID_E_RESERVED_COLLECTION
	ErrInvalidName        ErrCode = C.CORVID_E_INVALID_NAME
	ErrArgument           ErrCode = C.CORVID_E_ARGUMENT
	ErrIncompatibleFormat ErrCode = C.CORVID_E_INCOMPATIBLE_FORMAT
	ErrEmptyIndexTraining ErrCode = C.CORVID_E_EMPTY_INDEX_TRAINING
	ErrSchemaViolation    ErrCode = C.CORVID_E_SCHEMA_VIOLATION
	ErrInvalidDump        ErrCode = C.CORVID_E_INVALID_DUMP
	ErrBackupTargetExists ErrCode = C.CORVID_E_BACKUP_TARGET_EXISTS
	ErrIO                 ErrCode = C.CORVID_E_IO
	ErrBusy               ErrCode = C.CORVID_E_BUSY
)

// Metric is the vector distance metric (FFI.md §1.4).
type Metric uint32

const (
	MetricCosine Metric = C.CORVID_METRIC_COSINE
	MetricDot    Metric = C.CORVID_METRIC_DOT
	MetricL2     Metric = C.CORVID_METRIC_L2
)

// Quant is the stored-vector quantization mode (FFI.md §1.4).
type Quant uint32

const (
	QuantNone   Quant = C.CORVID_QUANT_NONE
	QuantBinary Quant = C.CORVID_QUANT_BINARY
	QuantScalar Quant = C.CORVID_QUANT_SCALAR
)

// FieldType is the declared type of a schema field (FFI.md §1.4).
type FieldType uint32

const (
	FieldAny    FieldType = C.CORVID_FIELD_ANY
	FieldBool   FieldType = C.CORVID_FIELD_BOOL
	FieldInt    FieldType = C.CORVID_FIELD_INT
	FieldFloat  FieldType = C.CORVID_FIELD_FLOAT
	FieldText   FieldType = C.CORVID_FIELD_TEXT
	FieldBytes  FieldType = C.CORVID_FIELD_BYTES
	FieldVector FieldType = C.CORVID_FIELD_VECTOR
	FieldArray  FieldType = C.CORVID_FIELD_ARRAY
	FieldMap    FieldType = C.CORVID_FIELD_MAP
)

// Value type tags (FFI.md §1.4) — internal: decode dispatch only.
const (
	tagNull   = uint32(C.CORVID_TYPE_NULL)
	tagBool   = uint32(C.CORVID_TYPE_BOOL)
	tagInt    = uint32(C.CORVID_TYPE_INT)
	tagFloat  = uint32(C.CORVID_TYPE_FLOAT)
	tagText   = uint32(C.CORVID_TYPE_TEXT)
	tagBytes  = uint32(C.CORVID_TYPE_BYTES)
	tagArray  = uint32(C.CORVID_TYPE_ARRAY)
	tagMap    = uint32(C.CORVID_TYPE_MAP)
	tagVector = uint32(C.CORVID_TYPE_VECTOR)
)

// Comparison operators (FFI.md §1.4) — internal: Field(...).Eq(...) etc.
const (
	cmpEq = uint32(C.CORVID_CMP_EQ)
	cmpNe = uint32(C.CORVID_CMP_NE)
	cmpLt = uint32(C.CORVID_CMP_LT)
	cmpLe = uint32(C.CORVID_CMP_LE)
	cmpGt = uint32(C.CORVID_CMP_GT)
	cmpGe = uint32(C.CORVID_CMP_GE)
)

// ---------------------------------------------------------------------------
// Opaque handle carriers — C pointers stay behind these structs
// ---------------------------------------------------------------------------

type cDB struct{ h *C.corvid_db }
type cColl struct{ h *C.corvid_coll }
type cVal struct{ h *C.corvid_value } // an OWNED value handle
type cPred struct{ h *C.corvid_pred }
type cQuery struct{ h *C.corvid_query }

// Package-visible aliases for the borrowed cursor/handle types the Go
// layer walks (cgo types cannot be named outside this file's import "C").
type (
	cValueHandle = C.corvid_value
	cGeoHits     = C.corvid_geohits
	cGroupIter   = C.corvid_groupiter
)

// ---------------------------------------------------------------------------
// Zero-copy argument pointers (non-NULL sentinel for empty, FFI.md §1.5)
// ---------------------------------------------------------------------------

var (
	zeroByte byte
	zeroF32  float32
)

func cBytes(b []byte) (*C.uint8_t, C.size_t) {
	if len(b) == 0 {
		return (*C.uint8_t)(unsafe.Pointer(&zeroByte)), 0
	}
	return (*C.uint8_t)(unsafe.Pointer(&b[0])), C.size_t(len(b))
}

func cStr(s string) (*C.char, C.size_t) {
	if len(s) == 0 {
		return (*C.char)(unsafe.Pointer(&zeroByte)), 0
	}
	return (*C.char)(unsafe.Pointer(unsafe.StringData(s))), C.size_t(len(s))
}

func cFloats(v []float32) (*C.float, C.size_t) {
	if len(v) == 0 {
		return (*C.float)(unsafe.Pointer(&zeroF32)), 0
	}
	return (*C.float)(unsafe.Pointer(&v[0])), C.size_t(len(v))
}

// copyBytes copies a borrowed C byte range into Go-owned memory.
func copyBytes(p *C.uint8_t, n C.size_t) []byte {
	if n == 0 {
		return []byte{}
	}
	return C.GoBytes(unsafe.Pointer(p), C.int(n))
}

// cStrTable lays out the ABI's parallel-string shape ((const char **,
// size_t *) over n entries) for the given Go strings WITHOUT storing a
// Go pointer in C memory: every string's bytes are first copied into
// ONE C.malloc'd buffer, and the pointer/length arrays — C.malloc'd
// too — aim at those C copies (the same discipline as cPutMany /
// cSetSchema). Each pointer is non-NULL even for an empty string (the
// §1.5 sentinel; the buffer carries one spare byte so &copy[0] always
// exists). The caller defers C.free over all three allocations when
// its engine call returns; len(strs) must be > 0.
func cStrTable(strs []string) (blob, ptrs, lens unsafe.Pointer) {
	total := 1 // +1 keeps &buf[off] in-bounds for trailing empty strings
	for _, s := range strs {
		total += len(s)
	}
	blob = C.malloc(C.size_t(total))
	ptrs = C.malloc(C.size_t(len(strs)) * C.size_t(unsafe.Sizeof((*C.char)(nil))))
	lens = C.malloc(C.size_t(len(strs)) * C.size_t(unsafe.Sizeof(C.size_t(0))))
	buf := (*[1 << 30]byte)(blob)
	ph := (*[1 << 28]*C.char)(ptrs)[:len(strs):len(strs)]
	lh := (*[1 << 28]C.size_t)(lens)[:len(strs):len(strs)]
	off := 0
	for i, s := range strs {
		copy(buf[off:off+len(s)], s) // the C copy of this string's bytes
		ph[i] = (*C.char)(unsafe.Pointer(&buf[off]))
		lh[i] = C.size_t(len(s))
		off += len(s)
	}
	return blob, ptrs, lens
}

// ---------------------------------------------------------------------------
// Error plumbing (FFI.md §3): read the thread-local slot immediately
// after the failing call, on this goroutine's thread.
// ---------------------------------------------------------------------------

func cLastError() *CorvidError {
	code := ErrCode(C.corvid_last_error_code())
	if code == ErrNone {
		// Should not happen (every failure records a fresh error) — do
		// not manufacture a zero-code error silently.
		return newErr(ErrNone, "failure with no recorded error")
	}
	var n C.size_t
	msg := C.corvid_last_error_message(&n)
	m := ""
	if msg != nil {
		m = C.GoStringN(msg, C.int(n))
	}
	return newErr(code, "%s", m)
}

func cStatusErr(st C.corvid_status) error {
	if st == C.CORVID_OK {
		return nil
	}
	return cLastError()
}

// ---------------------------------------------------------------------------
// Lifecycle & errors (FFI.md §4.1)
// ---------------------------------------------------------------------------

func cFFIVersion() uint32 { return uint32(C.corvid_ffi_version()) }

func cOpen(path string) (*cDB, error) {
	p, n := cStr(path)
	h := C.corvid_open(p, n)
	if h == nil {
		return nil, cLastError()
	}
	return &cDB{h: h}, nil
}

func cOpenMemory() (*cDB, error) {
	h := C.corvid_open_memory()
	if h == nil {
		return nil, cLastError()
	}
	return &cDB{h: h}, nil
}

func cClose(db *cDB) error {
	if db == nil || db.h == nil {
		return nil
	}
	return cStatusErr(C.corvid_close(db.h))
}

func cCollections(db *cDB) ([]string, error) {
	s := C.corvid_collections(db.h)
	if s == nil {
		return nil, cLastError()
	}
	defer C.corvid_strs_free(s)
	var out []string
	for {
		var str *C.char
		var n C.size_t
		if C.corvid_strs_next(s, &str, &n) != 1 {
			break
		}
		out = append(out, C.GoStringN(str, C.int(n))) // borrowed until next → copy now
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Collection handles (FFI.md §4.2)
// ---------------------------------------------------------------------------

func cCollection(db *cDB, name string) (*cColl, error) {
	p, n := cStr(name)
	h := C.corvid_collection(db.h, p, n)
	if h == nil {
		return nil, cLastError()
	}
	return &cColl{h: h}, nil
}

func cCollFree(c *cColl) {
	if c == nil || c.h == nil {
		return
	}
	C.corvid_collection_free(c.h)
	c.h = nil
}

func cCollName(c *cColl) string {
	if c == nil || c.h == nil {
		return ""
	}
	var n C.size_t
	p := C.corvid_collection_name(c.h, &n)
	if p == nil {
		return ""
	}
	return C.GoStringN(p, C.int(n)) // borrowed from the handle → copy now
}

// ---------------------------------------------------------------------------
// Value construction (FFI.md §4.3)
// ---------------------------------------------------------------------------

func cValueNull() *cVal { return &cVal{h: C.corvid_value_null()} }
func cValueBool(b bool) *cVal {
	v := C.int(0)
	if b {
		v = 1
	}
	return &cVal{h: C.corvid_value_bool(v)}
}
func cValueInt(i int64) *cVal     { return &cVal{h: C.corvid_value_int(C.int64_t(i))} }
func cValueFloat(f float64) *cVal { return &cVal{h: C.corvid_value_float(C.double(f))} }

func cValueText(s string) (*cVal, error) {
	p, n := cStr(s)
	h := C.corvid_value_text(p, n)
	if h == nil {
		return nil, cLastError()
	}
	return &cVal{h: h}, nil
}

func cValueBytes(b []byte) (*cVal, error) {
	p, n := cBytes(b)
	h := C.corvid_value_bytes(p, n)
	if h == nil {
		return nil, cLastError()
	}
	return &cVal{h: h}, nil
}

func cValueVector(v []float32) (*cVal, error) {
	p, n := cFloats(v)
	h := C.corvid_value_vector(p, n)
	if h == nil {
		return nil, cLastError()
	}
	return &cVal{h: h}, nil
}

func cArrayNew() *cVal { return &cVal{h: C.corvid_value_array_new()} }

// cArrayPush appends item, CONSUMING it unconditionally (FFI.md §8) —
// the caller must not free item whatever the status.
func cArrayPush(arr, item *cVal) error {
	return cStatusErr(C.corvid_value_array_push(arr.h, item.h))
}

func cMapNew() *cVal { return &cVal{h: C.corvid_value_map_new()} }

// cMapPut inserts val under key, CONSUMING val unconditionally — the
// caller must not free val whatever the status.
func cMapPut(m *cVal, key string, val *cVal) error {
	k, kl := cStr(key)
	return cStatusErr(C.corvid_value_map_put(m.h, k, kl, val.h))
}

// ---------------------------------------------------------------------------
// Value reads (FFI.md §4.4) — over borrowed or owned handles; all
// returned data is copied into Go memory (the _ref views are borrowed
// only until the parent's next mutation or free).
// ---------------------------------------------------------------------------

func cVType(h *C.corvid_value) uint32 { return uint32(C.corvid_value_type(h)) }
func cVLen(h *C.corvid_value) int     { return int(C.corvid_value_len(h)) }

func cVAsBool(h *C.corvid_value) (bool, bool) {
	var ok C.int
	v := C.corvid_value_as_bool(h, &ok)
	return v != 0, ok != 0
}

func cVAsInt(h *C.corvid_value) (int64, bool) {
	var ok C.int
	v := C.corvid_value_as_int(h, &ok)
	return int64(v), ok != 0
}

func cVAsFloat(h *C.corvid_value) (float64, bool) {
	var ok C.int
	v := C.corvid_value_as_float(h, &ok)
	return float64(v), ok != 0
}

func cVTextRef(h *C.corvid_value) (string, bool) {
	var n C.size_t
	p := C.corvid_value_text_ref(h, &n)
	if p == nil {
		return "", false
	}
	return C.GoStringN(p, C.int(n)), true
}

func cVBytesRef(h *C.corvid_value) ([]byte, bool) {
	var n C.size_t
	p := C.corvid_value_bytes_ref(h, &n)
	if p == nil {
		return nil, false
	}
	return copyBytes(p, n), true
}

func cVVectorRef(h *C.corvid_value) ([]float32, bool) {
	var dim C.size_t
	p := C.corvid_value_vector_ref(h, &dim)
	if p == nil {
		return nil, false
	}
	out := make([]float32, int(dim))
	if dim > 0 {
		copy(out, unsafe.Slice((*float32)(unsafe.Pointer(p)), int(dim)))
	}
	return out, true
}

func cVArrayGet(h *C.corvid_value, i int) *C.corvid_value {
	return C.corvid_value_array_get(h, C.size_t(i))
}

func cVMapGet(h *C.corvid_value, key string) *C.corvid_value {
	k, kl := cStr(key)
	return C.corvid_value_map_get(h, k, kl)
}

func cVClone(h *C.corvid_value) (*cVal, error) {
	c := C.corvid_value_clone(h)
	if c == nil {
		return nil, cLastError()
	}
	return &cVal{h: c}, nil
}

func cValueFree(v *cVal) {
	if v == nil || v.h == nil {
		return
	}
	C.corvid_value_free(v.h)
	v.h = nil
}

// ---------------------------------------------------------------------------
// Predicates (FFI.md §4.5)
// ---------------------------------------------------------------------------

func cPredExists(path string) (*cPred, error) {
	p, n := cStr(path)
	h := C.corvid_pred_exists(p, n)
	if h == nil {
		return nil, cLastError()
	}
	return &cPred{h: h}, nil
}

// cPredCompare clones v into the tree — the caller keeps (and frees) v.
func cPredCompare(path string, op uint32, v *cVal) (*cPred, error) {
	p, n := cStr(path)
	h := C.corvid_pred_compare(p, n, C.corvid_cmp(op), v.h)
	if h == nil {
		return nil, cLastError()
	}
	return &cPred{h: h}, nil
}

// cPredIn clones every value — the caller keeps (and frees) them all.
func cPredIn(path string, vals []*cVal) (*cPred, error) {
	p, n := cStr(path)
	if len(vals) == 0 {
		h := C.corvid_pred_in(p, n, nil, 0)
		if h == nil {
			return nil, cLastError()
		}
		return &cPred{h: h}, nil
	}
	arr := C.malloc(C.size_t(len(vals)) * C.size_t(unsafe.Sizeof((*C.corvid_value)(nil))))
	defer C.free(arr)
	hdrs := (*[1 << 28]*C.corvid_value)(arr)[:len(vals):len(vals)]
	for i, v := range vals {
		hdrs[i] = v.h
	}
	h := C.corvid_pred_in(p, n, (**C.corvid_value)(arr), C.size_t(len(vals)))
	if h == nil {
		return nil, cLastError()
	}
	return &cPred{h: h}, nil
}

// cPredBetween clones both bounds — the caller keeps (and frees) them.
func cPredBetween(path string, lo, hi *cVal) (*cPred, error) {
	p, n := cStr(path)
	h := C.corvid_pred_between(p, n, lo.h, hi.h)
	if h == nil {
		return nil, cLastError()
	}
	return &cPred{h: h}, nil
}

func cPredStartsWith(path, prefix string) (*cPred, error) {
	p, n := cStr(path)
	q, m := cStr(prefix)
	h := C.corvid_pred_starts_with(p, n, q, m)
	if h == nil {
		return nil, cLastError()
	}
	return &cPred{h: h}, nil
}

func cPredContains(path, substr string) (*cPred, error) {
	p, n := cStr(path)
	q, m := cStr(substr)
	h := C.corvid_pred_contains(p, n, q, m)
	if h == nil {
		return nil, cLastError()
	}
	return &cPred{h: h}, nil
}

func cPredGeoWithin(path string, lat, lon, radiusKm float64) (*cPred, error) {
	p, n := cStr(path)
	h := C.corvid_pred_geo_within(p, n, C.double(lat), C.double(lon), C.double(radiusKm))
	if h == nil {
		return nil, cLastError()
	}
	return &cPred{h: h}, nil
}

// cPredAnd/or/not CONSUME their children unconditionally (FFI.md §8):
// even on failure the caller-side "consumed" marking must happen.
func cPredAnd(a, b *cPred) (*cPred, error) {
	h := C.corvid_pred_and(a.h, b.h)
	if h == nil {
		return nil, cLastError()
	}
	return &cPred{h: h}, nil
}

func cPredOr(a, b *cPred) (*cPred, error) {
	h := C.corvid_pred_or(a.h, b.h)
	if h == nil {
		return nil, cLastError()
	}
	return &cPred{h: h}, nil
}

func cPredNot(a *cPred) (*cPred, error) {
	h := C.corvid_pred_not(a.h)
	if h == nil {
		return nil, cLastError()
	}
	return &cPred{h: h}, nil
}

func cPredFree(p *cPred) {
	if p == nil || p.h == nil {
		return
	}
	C.corvid_pred_free(p.h)
	p.h = nil
}

// ---------------------------------------------------------------------------
// Query builder & rows (FFI.md §4.6)
// ---------------------------------------------------------------------------

func cQueryNew(c *cColl) (*cQuery, error) {
	h := C.corvid_query_new(c.h)
	if h == nil {
		return nil, cLastError()
	}
	return &cQuery{h: h}, nil
}

// cQueryFilter consumes pred unconditionally.
func cQueryFilter(q *cQuery, p *cPred) error {
	return cStatusErr(C.corvid_query_filter(q.h, p.h))
}

func cQueryVector(q *cQuery, field string, query []float32, k int, m Metric) error {
	p, n := cStr(field)
	v, vn := cFloats(query)
	return cStatusErr(C.corvid_query_vector(q.h, p, n, v, vn, C.size_t(k), C.corvid_metric(m)))
}

func cQueryText(q *cQuery, field, s string, k int) error {
	p, n := cStr(field)
	t, tn := cStr(s)
	return cStatusErr(C.corvid_query_text(q.h, p, n, t, tn, C.size_t(k)))
}

func cQueryFuseRRF(q *cQuery, k float32) error {
	return cStatusErr(C.corvid_query_fuse_rrf(q.h, C.float(k)))
}

func cQueryRerankMMR(q *cQuery, lambda float32) error {
	return cStatusErr(C.corvid_query_rerank_mmr(q.h, C.float(lambda)))
}

func cQueryApprox(q *cQuery) error { return cStatusErr(C.corvid_query_approx(q.h)) }

func cQueryLimit(q *cQuery, n int) error { return cStatusErr(C.corvid_query_limit(q.h, C.size_t(n))) }

func cQueryOffset(q *cQuery, n int) error { return cStatusErr(C.corvid_query_offset(q.h, C.size_t(n))) }

func cQueryOrderBy(q *cQuery, field string, descending bool) error {
	p, n := cStr(field)
	d := C.int(0)
	if descending {
		d = 1
	}
	return cStatusErr(C.corvid_query_order_by(q.h, p, n, d))
}

func cQuerySelect(q *cQuery, fields []string) error {
	if len(fields) == 0 {
		return cStatusErr(C.corvid_query_select(q.h, nil, nil, 0))
	}
	blob, ptrs, lens := cStrTable(fields)
	defer C.free(blob)
	defer C.free(ptrs)
	defer C.free(lens)
	return cStatusErr(C.corvid_query_select(q.h, (**C.char)(ptrs), (*C.size_t)(lens), C.size_t(len(fields))))
}

// cQueryRun consumes q (even on failure).
func cQueryRun(q *cQuery) (*C.corvid_rows, error) {
	h := C.corvid_query_run(q.h)
	if h == nil {
		return nil, cLastError()
	}
	return h, nil
}

func cQueryFree(q *cQuery) {
	if q == nil || q.h == nil {
		return
	}
	C.corvid_query_free(q.h)
	q.h = nil
}

// cRowsNext returns the next row; key is copied immediately (borrowed only
// until the next call), doc stays borrowed and must be consumed (decoded
// or cloned) before the next call on this cursor.
func cRowsNext(rows *C.corvid_rows) (key []byte, doc *C.corvid_value, score float32, ok bool) {
	var kp *C.uint8_t
	var kl C.size_t
	var dv *C.corvid_value
	var sc C.float
	if C.corvid_rows_next(rows, &kp, &kl, &dv, &sc) != 1 {
		return nil, nil, 0, false
	}
	return copyBytes(kp, kl), dv, float32(sc), true
}

func cRowsFree(rows *C.corvid_rows) {
	if rows != nil {
		C.corvid_rows_free(rows)
	}
}

// ---------------------------------------------------------------------------
// Aggregations (FFI.md §4.7) — every one consumes the query.
// ---------------------------------------------------------------------------

func cQueryCount(q *cQuery) (int, error) {
	var n C.size_t
	if err := cStatusErr(C.corvid_query_count(q.h, &n)); err != nil {
		return 0, err
	}
	return int(n), nil
}

func cQueryCountDistinct(q *cQuery, field string) (int, error) {
	p, n := cStr(field)
	var out C.size_t
	if err := cStatusErr(C.corvid_query_count_distinct(q.h, p, n, &out)); err != nil {
		return 0, err
	}
	return int(out), nil
}

func cQuerySum(q *cQuery, field string) (float64, error) {
	p, n := cStr(field)
	var out C.double
	if err := cStatusErr(C.corvid_query_sum(q.h, p, n, &out)); err != nil {
		return 0, err
	}
	return float64(out), nil
}

func cQueryAvg(q *cQuery, field string) (float64, bool, error) {
	p, n := cStr(field)
	var out C.double
	var has C.int
	if err := cStatusErr(C.corvid_query_avg(q.h, p, n, &out, &has)); err != nil {
		return 0, false, err
	}
	return float64(out), has != 0, nil
}

func cQueryMin(q *cQuery, field string) (*cVal, error) {
	p, n := cStr(field)
	var out *C.corvid_value
	if err := cStatusErr(C.corvid_query_min(q.h, p, n, &out)); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil // absence is a success (FFI.md §3)
	}
	return &cVal{h: out}, nil
}

func cQueryMax(q *cQuery, field string) (*cVal, error) {
	p, n := cStr(field)
	var out *C.corvid_value
	if err := cStatusErr(C.corvid_query_max(q.h, p, n, &out)); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return &cVal{h: out}, nil
}

func cQueryGroupCount(q *cQuery, field string) (*C.corvid_groupiter, error) {
	p, n := cStr(field)
	h := C.corvid_query_group_count(q.h, p, n)
	if h == nil {
		return nil, cLastError()
	}
	return h, nil
}

func cQueryGroupSum(q *cQuery, groupField, valueField string) (*C.corvid_groupiter, error) {
	g, gn := cStr(groupField)
	v, vn := cStr(valueField)
	h := C.corvid_query_group_sum(q.h, g, gn, v, vn)
	if h == nil {
		return nil, cLastError()
	}
	return h, nil
}

func cQueryGroupAvg(q *cQuery, groupField, valueField string) (*C.corvid_groupiter, error) {
	g, gn := cStr(groupField)
	v, vn := cStr(valueField)
	h := C.corvid_query_group_avg(q.h, g, gn, v, vn)
	if h == nil {
		return nil, cLastError()
	}
	return h, nil
}

func cGroupNext(it *C.corvid_groupiter) (key string, val float64, ok bool) {
	var kp *C.char
	var kl C.size_t
	var v C.double
	if C.corvid_groupiter_next(it, &kp, &kl, &v) != 1 {
		return "", 0, false
	}
	return C.GoStringN(kp, C.int(kl)), float64(v), true // key borrowed → copy now
}

func cGroupFree(it *C.corvid_groupiter) {
	if it != nil {
		C.corvid_groupiter_free(it)
	}
}

// ---------------------------------------------------------------------------
// Mutations (FFI.md §4.8)
// ---------------------------------------------------------------------------

func cInsert(c *cColl, key []byte, doc *cVal) error {
	k, n := cBytes(key)
	return cStatusErr(C.corvid_insert(c.h, k, n, doc.h))
}

// cPutMany builds the corvid_kv array in C memory (C copies of the keys;
// the value handles are already C-side). The engine clones every value;
// the caller keeps ownership of the vals.
func cPutMany(c *cColl, keys [][]byte, vals []*cVal) error {
	if len(keys) != len(vals) {
		return newErr(ErrArgument, "put_many: keys and values must pair up")
	}
	if len(keys) == 0 {
		return cStatusErr(C.corvid_put_many(c.h, nil, 0))
	}
	arr := C.malloc(C.size_t(len(keys)) * C.sizeof_struct_corvid_kv)
	defer C.free(arr)
	mem := make([]unsafe.Pointer, len(keys))
	for i := range mem {
		if len(keys[i]) == 0 {
			mem[i] = unsafe.Pointer(&zeroByte) // static Go byte: pinned, call-scoped, no Go pointers inside
		} else {
			mem[i] = C.CBytes(keys[i])
			defer C.free(mem[i])
		}
	}
	hdrs := (*[1 << 28]C.struct_corvid_kv)(arr)[:len(keys):len(keys)]
	for i := range keys {
		hdrs[i].key = (*C.uint8_t)(mem[i])
		hdrs[i].key_len = C.size_t(len(keys[i]))
		hdrs[i].val = vals[i].h
	}
	return cStatusErr(C.corvid_put_many(c.h, (*C.struct_corvid_kv)(arr), C.size_t(len(keys))))
}

// cInsertAuto returns the fresh key copied into Go memory; the C buffer
// is corvid_free'd here.
func cInsertAuto(c *cColl, doc *cVal) ([]byte, error) {
	var n C.size_t
	k := C.corvid_insert_auto(c.h, doc.h, &n)
	if k == nil {
		return nil, cLastError()
	}
	out := copyBytes(k, n)
	C.corvid_free(unsafe.Pointer(k))
	return out, nil
}

func cPatch(c *cColl, key []byte, patch *cVal) error {
	k, n := cBytes(key)
	return cStatusErr(C.corvid_patch(c.h, k, n, patch.h))
}

// cCompareAndSet: nil expected/replacement carry the ABI's semantic NULL
// ("must be absent" / "delete on match").
func cCompareAndSet(c *cColl, key []byte, expected, replacement *cVal) (bool, error) {
	k, n := cBytes(key)
	var ex, re *C.corvid_value
	if expected != nil {
		ex = expected.h
	}
	if replacement != nil {
		re = replacement.h
	}
	var applied C.int32_t
	if err := cStatusErr(C.corvid_compare_and_set(c.h, k, n, ex, re, &applied)); err != nil {
		return false, err
	}
	return applied != 0, nil
}

func cDelete(c *cColl, key []byte) (bool, error) {
	k, n := cBytes(key)
	var existed C.int32_t
	if err := cStatusErr(C.corvid_delete(c.h, k, n, &existed)); err != nil {
		return false, err
	}
	return existed != 0, nil
}

// cDeleteWhere consumes pred unconditionally.
func cDeleteWhere(c *cColl, p *cPred) (int, error) {
	var removed C.size_t
	if err := cStatusErr(C.corvid_delete_where(c.h, p.h, &removed)); err != nil {
		return 0, err
	}
	return int(removed), nil
}

func cDeleteBatch(c *cColl, keys [][]byte) (int, error) {
	if len(keys) == 0 {
		var removed C.size_t
		if err := cStatusErr(C.corvid_delete_batch(c.h, nil, nil, 0, &removed)); err != nil {
			return 0, err
		}
		return int(removed), nil
	}
	kp := C.malloc(C.size_t(len(keys)) * C.size_t(unsafe.Sizeof((*C.uint8_t)(nil))))
	kl := C.malloc(C.size_t(len(keys)) * C.size_t(unsafe.Sizeof(C.size_t(0))))
	defer C.free(kp)
	defer C.free(kl)
	mem := make([]unsafe.Pointer, len(keys))
	for i := range mem {
		if len(keys[i]) == 0 {
			mem[i] = unsafe.Pointer(&zeroByte)
		} else {
			mem[i] = C.CBytes(keys[i])
			defer C.free(mem[i])
		}
	}
	phdrs := (*[1 << 28]*C.uint8_t)(kp)[:len(keys):len(keys)]
	lhdrs := (*[1 << 28]C.size_t)(kl)[:len(keys):len(keys)]
	for i := range keys {
		phdrs[i] = (*C.uint8_t)(mem[i])
		lhdrs[i] = C.size_t(len(keys[i]))
	}
	var removed C.size_t
	if err := cStatusErr(C.corvid_delete_batch(c.h, (**C.uint8_t)(kp), (*C.size_t)(kl), C.size_t(len(keys)), &removed)); err != nil {
		return 0, err
	}
	return int(removed), nil
}

func cInsertTTL(c *cColl, key []byte, doc *cVal, expiresAt int64) error {
	k, n := cBytes(key)
	return cStatusErr(C.corvid_insert_with_ttl(c.h, k, n, doc.h, C.int64_t(expiresAt)))
}

func cSetTTL(c *cColl, key []byte, expiresAt int64) error {
	k, n := cBytes(key)
	return cStatusErr(C.corvid_set_ttl(c.h, k, n, C.int64_t(expiresAt)))
}

func cGetTTL(c *cColl, key []byte) (int64, bool, error) {
	k, n := cBytes(key)
	var at C.int64_t
	var has C.int32_t
	if err := cStatusErr(C.corvid_get_ttl(c.h, k, n, &at, &has)); err != nil {
		return 0, false, err
	}
	return int64(at), has != 0, nil
}

func cPurgeExpired(c *cColl, now int64) (int, error) {
	var purged C.size_t
	if err := cStatusErr(C.corvid_purge_expired(c.h, C.int64_t(now), &purged)); err != nil {
		return 0, err
	}
	return int(purged), nil
}

// ---------------------------------------------------------------------------
// Reads (FFI.md §4.9)
// ---------------------------------------------------------------------------

// cGet returns an OWNED value, or (nil, nil) when the key holds no
// document (absence is a success).
func cGet(c *cColl, key []byte) (*cVal, error) {
	k, n := cBytes(key)
	var out *C.corvid_value
	if err := cStatusErr(C.corvid_get(c.h, k, n, &out)); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return &cVal{h: out}, nil
}

func cScan(c *cColl, ks *keySet, fn func(key []byte, doc any) bool) error {
	job := &scanJob{fn: fn, ks: ks}
	id := cbPut(job)
	cell := C.malloc(C.size_t(unsafe.Sizeof(uintptr(0))))
	defer C.free(cell)
	*(*uintptr)(cell) = id
	st := C.corvidgo_scan_call(c.h, cell)
	cbDel(id)
	if job.panicVal != nil {
		panic(job.panicVal) // the closure panicked: surface it here, not through C frames
	}
	if err := cStatusErr(st); err != nil {
		return err
	}
	if job.err != nil {
		return job.err // a decode failure stopped the scan
	}
	return nil
}

// cPage returns the rows cursor (borrowed docs — decode before the next
// use of this file's helpers on it) plus the resume cursor copied into Go
// memory; the C buffer is corvid_free'd here.
func cPage(c *cColl, after []byte, limit int) (*C.corvid_rows, []byte, error) {
	a, n := cBytes(after)
	var rows *C.corvid_rows
	var next *C.uint8_t
	var nlen C.size_t
	if err := cStatusErr(C.corvid_page(c.h, a, n, C.size_t(limit), &rows, &next, &nlen)); err != nil {
		return nil, nil, err
	}
	var nb []byte
	if next != nil {
		nb = copyBytes(next, nlen)
		C.corvid_free(unsafe.Pointer(next))
	}
	return rows, nb, nil
}

func cLen(c *cColl) (int, error) {
	var n C.size_t
	if err := cStatusErr(C.corvid_len(c.h, &n)); err != nil {
		return 0, err
	}
	return int(n), nil
}

// ---------------------------------------------------------------------------
// Indexes & schema (FFI.md §4.10)
// ---------------------------------------------------------------------------

func cCreateScalarIndex(c *cColl, field string) error {
	p, n := cStr(field)
	return cStatusErr(C.corvid_create_scalar_index(c.h, p, n))
}

func cCreateCompoundIndex(c *cColl, fields []string) error {
	if len(fields) == 0 {
		return cStatusErr(C.corvid_create_compound_index(c.h, nil, nil, 0))
	}
	blob, ptrs, lens := cStrTable(fields)
	defer C.free(blob)
	defer C.free(ptrs)
	defer C.free(lens)
	return cStatusErr(C.corvid_create_compound_index(c.h, (**C.char)(ptrs), (*C.size_t)(lens), C.size_t(len(fields))))
}

func cCreateTextIndex(c *cColl, field string) error {
	p, n := cStr(field)
	return cStatusErr(C.corvid_create_text_index(c.h, p, n))
}

func cCreateTextIndexOnDisk(c *cColl, field string) error {
	p, n := cStr(field)
	return cStatusErr(C.corvid_create_text_index_ondisk(c.h, p, n))
}

func cCreateGeoIndex(c *cColl, field string) error {
	p, n := cStr(field)
	return cStatusErr(C.corvid_create_geo_index(c.h, p, n))
}

func cCreateVectorIndex(c *cColl, field string, m Metric) error {
	p, n := cStr(field)
	return cStatusErr(C.corvid_create_vector_index(c.h, p, n, C.corvid_metric(m)))
}

func cCreateVectorIndexQuantized(c *cColl, field string, m Metric, q Quant) error {
	p, n := cStr(field)
	return cStatusErr(C.corvid_create_vector_index_quantized(c.h, p, n, C.corvid_metric(m), C.corvid_quant(q)))
}

func cCreateVectorIndexOnDisk(c *cColl, field string, m Metric) error {
	p, n := cStr(field)
	return cStatusErr(C.corvid_create_vector_index_ondisk(c.h, p, n, C.corvid_metric(m)))
}

func cCreateVectorIndexOnDiskQuantized(c *cColl, field string, m Metric, q Quant) error {
	p, n := cStr(field)
	return cStatusErr(C.corvid_create_vector_index_ondisk_quantized(c.h, p, n, C.corvid_metric(m), C.corvid_quant(q)))
}

func cCreateVectorIndexPQ(c *cColl, field string, m Metric, subspaces, centroids int) error {
	p, n := cStr(field)
	return cStatusErr(C.corvid_create_vector_index_pq(c.h, p, n, C.corvid_metric(m), C.size_t(subspaces), C.size_t(centroids)))
}

func cCreateVectorIndexOnDiskPQ(c *cColl, field string, m Metric, subspaces, centroids int) error {
	p, n := cStr(field)
	return cStatusErr(C.corvid_create_vector_index_ondisk_pq(c.h, p, n, C.corvid_metric(m), C.size_t(subspaces), C.size_t(centroids)))
}

// cSetSchema builds the corvid_field_def array in C memory (C copies of
// the names). Empty names use the static sentinel (legal: pinned,
// call-scoped, holds no Go pointers).
func cSetSchema(c *cColl, defs []FieldDef) error {
	if len(defs) == 0 {
		return cStatusErr(C.corvid_set_schema(c.h, nil, 0))
	}
	arr := C.malloc(C.size_t(len(defs)) * C.sizeof_struct_corvid_field_def)
	defer C.free(arr)
	mem := make([]unsafe.Pointer, len(defs))
	for i := range mem {
		if len(defs[i].Name) == 0 {
			mem[i] = unsafe.Pointer(&zeroByte)
		} else {
			mem[i] = C.CBytes([]byte(defs[i].Name))
			defer C.free(mem[i])
		}
	}
	hdrs := (*[1 << 28]C.struct_corvid_field_def)(arr)[:len(defs):len(defs)]
	for i, d := range defs {
		hdrs[i].name = (*C.char)(mem[i])
		hdrs[i].name_len = C.size_t(len(d.Name))
		hdrs[i]._type = C.corvid_field_type(d.Type)
		rq := C.int(0)
		if d.Required {
			rq = 1
		}
		hdrs[i].required = rq
		uq := C.int(0)
		if d.Unique {
			uq = 1
		}
		hdrs[i].unique = uq
	}
	return cStatusErr(C.corvid_set_schema(c.h, (*C.struct_corvid_field_def)(arr), C.size_t(len(defs))))
}

// cSchema returns nil iter for "no schema declared" (a success).
func cSchema(c *cColl) (*C.corvid_schemaiter, error) {
	var it *C.corvid_schemaiter
	if err := cStatusErr(C.corvid_schema(c.h, &it)); err != nil {
		return nil, err
	}
	return it, nil
}

func cSchemaIterNext(it *C.corvid_schemaiter) (FieldDef, bool) {
	var fd C.struct_corvid_field_def
	if C.corvid_schemaiter_next(it, &fd) != 1 {
		return FieldDef{}, false
	}
	return FieldDef{
		Name:     C.GoStringN(fd.name, C.int(fd.name_len)), // borrowed → copy now
		Type:     FieldType(fd._type),
		Required: fd.required != 0,
		Unique:   fd.unique != 0,
	}, true
}

func cSchemaIterFree(it *C.corvid_schemaiter) {
	if it != nil {
		C.corvid_schemaiter_free(it)
	}
}

// ---------------------------------------------------------------------------
// Graph (FFI.md §4.11)
// ---------------------------------------------------------------------------

func cLink(c *cColl, from []byte, relation string, to []byte) error {
	f, fn := cBytes(from)
	r, rn := cStr(relation)
	t, tn := cBytes(to)
	return cStatusErr(C.corvid_link(c.h, f, fn, r, rn, t, tn))
}

func cLinkWeighted(c *cColl, from []byte, relation string, to []byte, weight float64) error {
	f, fn := cBytes(from)
	r, rn := cStr(relation)
	t, tn := cBytes(to)
	return cStatusErr(C.corvid_link_weighted(c.h, f, fn, r, rn, t, tn, C.double(weight)))
}

func cUnlink(c *cColl, from []byte, relation string, to []byte) (bool, error) {
	f, fn := cBytes(from)
	r, rn := cStr(relation)
	t, tn := cBytes(to)
	var removed C.int32_t
	if err := cStatusErr(C.corvid_unlink(c.h, f, fn, r, rn, t, tn, &removed)); err != nil {
		return false, err
	}
	return removed != 0, nil
}

// cNeighbors walks a strs cursor to completion; the borrowed bytes are
// copied per step.
func cNeighbors(c *cColl, from []byte, relation string) ([][]byte, error) {
	f, fn := cBytes(from)
	r, rn := cStr(relation)
	s := C.corvid_neighbors(c.h, f, fn, r, rn)
	if s == nil {
		return nil, cLastError()
	}
	defer C.corvid_strs_free(s)
	return walkStrs(s), nil
}

func cInNeighbors(c *cColl, to []byte, relation string) ([][]byte, error) {
	t, tn := cBytes(to)
	r, rn := cStr(relation)
	s := C.corvid_in_neighbors(c.h, t, tn, r, rn)
	if s == nil {
		return nil, cLastError()
	}
	defer C.corvid_strs_free(s)
	return walkStrs(s), nil
}

type cWeighted struct {
	Key    []byte
	Weight float64
}

func cNeighborsWeighted(c *cColl, from []byte, relation string) ([]cWeighted, error) {
	f, fn := cBytes(from)
	r, rn := cStr(relation)
	h := C.corvid_neighbors_weighted(c.h, f, fn, r, rn)
	if h == nil {
		return nil, cLastError()
	}
	defer C.corvid_geohits_free(h)
	var out []cWeighted
	for {
		hit, _, ok := cGeoNext(h)
		if !ok {
			break
		}
		// The weighted-neighbor cursor carries the edge weight in the
		// geohit distance slot (§4.11/§4.12 share the cursor type).
		out = append(out, cWeighted{Key: hit.Key, Weight: hit.Dist})
	}
	return out, nil
}

func cTraverse(c *cColl, start []byte, relation string, hops int) ([][]byte, error) {
	s0, sn := cBytes(start)
	r, rn := cStr(relation)
	s := C.corvid_traverse(c.h, s0, sn, r, rn, C.size_t(hops))
	if s == nil {
		return nil, cLastError()
	}
	defer C.corvid_strs_free(s)
	return walkStrs(s), nil
}

func walkStrs(s *C.corvid_strs) [][]byte {
	var out [][]byte
	for {
		var str *C.char
		var n C.size_t
		if C.corvid_strs_next(s, &str, &n) != 1 {
			break
		}
		out = append(out, copyBytes((*C.uint8_t)(unsafe.Pointer(str)), n)) // borrowed → copy now
	}
	return out
}

// ---------------------------------------------------------------------------
// Geo (FFI.md §4.12)
// ---------------------------------------------------------------------------

type cGeoHit struct {
	Key    []byte
	Dist   float64
	Doc    *C.corvid_value // borrowed until the next cGeoNext on this cursor
	HasDoc bool            // false for neighbors_weighted cursors
}

func cGeoNext(h *C.corvid_geohits) (cGeoHit, float64, bool) {
	var hit C.struct_corvid_geohit
	var doc *C.corvid_value
	if C.corvid_geohits_next(h, &hit, &doc) != 1 {
		return cGeoHit{}, 0, false
	}
	return cGeoHit{
		Key:    copyBytes(hit.key, hit.key_len),
		Dist:   float64(hit.distance_km),
		Doc:    doc,
		HasDoc: doc != nil,
	}, float64(hit.distance_km), true
}

func cGeoWithinRadius(c *cColl, field string, lat, lon, radiusKm float64) (*C.corvid_geohits, error) {
	p, n := cStr(field)
	h := C.corvid_geo_within_radius(c.h, p, n, C.double(lat), C.double(lon), C.double(radiusKm))
	if h == nil {
		return nil, cLastError()
	}
	return h, nil
}

func cGeoWithinBBox(c *cColl, field string, minLat, minLon, maxLat, maxLon float64) (*C.corvid_geohits, error) {
	p, n := cStr(field)
	h := C.corvid_geo_within_bbox(c.h, p, n, C.double(minLat), C.double(minLon), C.double(maxLat), C.double(maxLon))
	if h == nil {
		return nil, cLastError()
	}
	return h, nil
}

func cGeoNearest(c *cColl, field string, lat, lon float64, k int) (*C.corvid_geohits, error) {
	p, n := cStr(field)
	h := C.corvid_geo_nearest(c.h, p, n, C.double(lat), C.double(lon), C.size_t(k))
	if h == nil {
		return nil, cLastError()
	}
	return h, nil
}

func cGeoFree(h *C.corvid_geohits) {
	if h != nil {
		C.corvid_geohits_free(h)
	}
}

// ---------------------------------------------------------------------------
// Admin (FFI.md §4.13)
// ---------------------------------------------------------------------------

func cDump(db *cDB, path string) error {
	p, n := cStr(path)
	return cStatusErr(C.corvid_dump_to_path(db.h, p, n))
}

func cLoad(db *cDB, path string) error {
	p, n := cStr(path)
	return cStatusErr(C.corvid_load_from_path(db.h, p, n))
}

func cLoadRenames(db *cDB, path string, renames map[string]string) error {
	p, pn := cStr(path)
	count := len(renames)
	if count == 0 {
		return cStatusErr(C.corvid_load_from_path_with_renames(db.h, p, pn, nil, nil, nil, nil, 0))
	}
	froms := make([]string, 0, count)
	tos := make([]string, 0, count)
	for from, to := range renames {
		froms = append(froms, from)
		tos = append(tos, to)
	}
	fblob, fptrs, flens := cStrTable(froms)
	defer C.free(fblob)
	defer C.free(fptrs)
	defer C.free(flens)
	nblob, nptrs, nlens := cStrTable(tos)
	defer C.free(nblob)
	defer C.free(nptrs)
	defer C.free(nlens)
	return cStatusErr(C.corvid_load_from_path_with_renames(db.h, p, pn,
		(**C.char)(fptrs), (**C.char)(nptrs), (*C.size_t)(flens), (*C.size_t)(nlens), C.size_t(count)))
}

func cBackup(db *cDB, path string) error {
	p, n := cStr(path)
	return cStatusErr(C.corvid_backup(db.h, p, n))
}

func cCompact(db *cDB) (bool, error) {
	var moved C.int
	if err := cStatusErr(C.corvid_compact(db.h, &moved)); err != nil {
		return false, err
	}
	return moved != 0, nil
}

// cCompactBusy exercises the §4.13 quiescence gate: compacting with
// the moved_out reporter omitted answers CORVID_E_BUSY — the golden
// suite's COMPACT_BUSY line.
func cCompactBusy(db *cDB) error {
	return cStatusErr(C.corvid_compact(db.h, nil))
}

// cNullFrees exercises the §7 inert rule: every _free(NULL) shape is
// a documented no-op.
func cNullFrees() {
	C.corvid_value_free(nil)
	C.corvid_pred_free(nil)
	C.corvid_query_free(nil)
	C.corvid_rows_free(nil)
	C.corvid_strs_free(nil)
	C.corvid_geohits_free(nil)
	C.corvid_groupiter_free(nil)
	C.corvid_schemaiter_free(nil)
	C.corvid_collection_free(nil)
	C.corvid_free(nil)
}

// cGroupIterNilNextOK exercises the §7 inert rule for cursors: next on
// a NULL handle answers 0 (exhausted), never an error.
func cGroupIterNilNextOK() bool {
	return C.corvid_groupiter_next(nil, nil, nil, nil) == 0
}

// ---------------------------------------------------------------------------
// §1.6 callback trampolines + the integer-id registry
// ---------------------------------------------------------------------------

type scanJob struct {
	fn       func(key []byte, doc any) bool
	ks       *keySet
	err      error // a decode failure stops the scan; surfaced by cScan
	panicVal any   // a panic in fn; recovered in the trampoline, re-panicked by cScan
}

type updateJob struct {
	fn       func(current any) (any, error)
	ks       *keySet
	err      error // the user callback's failure; surfaced by cUpdate
	panicVal any   // a panic in fn; recovered in the trampoline, re-panicked by cUpdate
}

var (
	cbMu   sync.Mutex
	cbNext uintptr
	cbRegs = map[uintptr]any{}
)

func cbPut(v any) uintptr {
	cbMu.Lock()
	defer cbMu.Unlock()
	cbNext++
	cbRegs[cbNext] = v
	return cbNext
}

func cbGet(id uintptr) any {
	cbMu.Lock()
	defer cbMu.Unlock()
	return cbRegs[id]
}

func cbDel(id uintptr) {
	cbMu.Lock()
	defer cbMu.Unlock()
	delete(cbRegs, id)
}

// The scan callback runs on the calling goroutine's thread between engine
// operations (FFI.md §1.6). Decoding the borrowed doc uses only read-side
// value calls on that same borrowed handle — no engine calls, no writes —
// and copies everything it touches; the reentrancy contract's "no
// reentrant corvid calls" is about engine/transaction state, which reads
// on the callback's own argument cannot disturb (the same class as the
// sanctioned corvid_value_clone escape).

//export corvidgoScanCB
func corvidgoScanCB(id C.uintptr_t, key *C.uint8_t, keyLen C.size_t, doc *C.corvid_value) (ret C.int) {
	job := cbGet(uintptr(id)).(*scanJob)
	// A panic in the user closure must never unwind through the C
	// frames (the Go runtime cannot): recover it here, stash the value,
	// stop the scan — cScan re-panics once the engine call has returned.
	defer func() {
		if p := recover(); p != nil {
			job.panicVal = p
			ret = 0
		}
	}()
	k := copyBytes(key, keyLen)
	d, err := decodeValue(doc, job.ks)
	if err != nil {
		job.err = err
		return 0 // stop the scan; not an error at the C level
	}
	if job.fn(k, d) {
		return 1
	}
	return 0
}

//export corvidgoUpdateCB
func corvidgoUpdateCB(id C.uintptr_t, current *C.corvid_value, out **C.corvid_value) (ret C.corvid_status) {
	job := cbGet(uintptr(id)).(*updateJob)
	// Same recover discipline as corvidgoScanCB: a panic in the user
	// closure aborts the update at the ABI level (CORVID_ERR) and is
	// re-panicked by cUpdate once the engine call has returned.
	defer func() {
		if p := recover(); p != nil {
			job.panicVal = p
			ret = C.CORVID_ERR
		}
	}()
	var cur any
	if current != nil {
		d, err := decodeValue(current, job.ks)
		if err != nil {
			job.err = err
			return C.CORVID_ERR
		}
		cur = d
	}
	v, err := job.fn(cur)
	if err != nil {
		job.err = err
		return C.CORVID_ERR // the aborting-callback contract (§1.6)
	}
	if v == nil {
		*out = nil // delete the key
		return C.CORVID_OK
	}
	cv, encErr := encodeValue(v)
	if encErr != nil {
		job.err = encErr
		return C.CORVID_ERR
	}
	*out = cv.h // owned; consumed by the call
	return C.CORVID_OK
}

// cUpdate drives the read-modify-write callback. An aborting callback
// fails with CORVID_E_ARGUMENT at the ABI level; this wrapper prefers the
// Go callback's own error, wrapped in a CorvidError with that code.
func cUpdate(c *cColl, ks *keySet, key []byte, fn func(current any) (any, error)) error {
	job := &updateJob{fn: fn, ks: ks}
	id := cbPut(job)
	cell := C.malloc(C.size_t(unsafe.Sizeof(uintptr(0))))
	defer C.free(cell)
	*(*uintptr)(cell) = id
	k, n := cBytes(key)
	st := C.corvidgo_update_call(c.h, k, n, cell)
	cbDel(id)
	if job.panicVal != nil {
		panic(job.panicVal) // the closure panicked: surface it here, not through C frames
	}
	if st == C.CORVID_OK {
		return nil
	}
	if job.err != nil {
		return newErr(ErrArgument, "update callback aborted: %s", job.err.Error())
	}
	return cLastError()
}

// FFIVersion returns the ABI version of the loaded library (bindings
// verify it equals 1 before anything else — FFI.md §4.1).
func FFIVersion() uint32 { return cFFIVersion() }
