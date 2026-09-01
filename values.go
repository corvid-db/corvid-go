// values.go — the Go ↔ C value mapping.
//
// Value mapping (docs/PLAN.md):
//
//	C null      ↔ Go nil
//	C bool      ↔ Go bool
//	C int (i64) ↔ Go int64        (encode also accepts the narrower int kinds)
//	C float     ↔ Go float64      (NaN/±inf/-0.0 cross bit-exact; NaN
//	                              payloads are preserved both ways)
//	C text      ↔ Go string
//	C bytes     ↔ Go []byte
//	C vector    ↔ Go []float32    (f32 elements bit-exact, NaN payloads
//	                              included)
//	C array     ↔ Go []any
//	C map       ↔ Go map[string]any
//
// Everything crossing into the engine is built as a fresh C value and
// freed inside the call (the ABI clones what it keeps — FFI.md §5);
// everything borrowed back is copied into Go-owned memory before the
// borrow ends. No C pointer escapes this package.
//
// The map-key boundary (v1): the v0.2.2 ABI has no map-key iterator —
// a Map is readable only by known key. Decoding therefore probes the
// Db's candidate key set (fed by every document that passed through
// this binding's write paths and by declared schemas) and verifies the
// probed count against the map's true entry count: a full match
// decodes, any unknown key fails LOUDLY (ErrMapKeyEnumeration) instead
// of returning a silently-truncated map. GetFields and Query.Select
// never need the oracle (their field list is the key source). The
// proper fix is the anticipated upstream `corvid_value_map_keys`
// append; when it ships, the candidate machinery collapses into a
// plain decode.

package corvid

import (
	"errors"
	"fmt"
	"sync"
)

// ErrMapKeyEnumeration is wrapped by every decode that meets a map
// whose keys are not fully covered by the binding's candidate set (see
// the file comment). On a database opened over pre-existing data, read
// documents with GetFields or Query.Select, or write/declare the keys
// first.
var ErrMapKeyEnumeration = errors.New("corvid: map decode could not enumerate all keys (the v0.2.2 ABI has no map-key iterator; see docs/PLAN.md)")

// keySet is the Db-wide candidate key-name oracle for map decoding.
// It is safe for concurrent use.
type keySet struct {
	mu sync.RWMutex
	m  map[string]struct{}
}

func newKeySet() *keySet { return &keySet{m: make(map[string]struct{})} }

// add records a candidate key name.
func (ks *keySet) add(key string) {
	ks.mu.Lock()
	ks.m[key] = struct{}{}
	ks.mu.Unlock()
}

// addAll records candidate key names.
func (ks *keySet) addAll(keys []string) {
	if len(keys) == 0 {
		return
	}
	ks.mu.Lock()
	for _, k := range keys {
		ks.m[k] = struct{}{}
	}
	ks.mu.Unlock()
}

// snapshot returns the current candidates (a copy); nil-safe.
func (ks *keySet) snapshot() []string {
	if ks == nil {
		return nil
	}
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	out := make([]string, 0, len(ks.m))
	for k := range ks.m {
		out = append(out, k)
	}
	return out
}

// remember walks a Go value and records every map key it contains,
// nested included.
func (ks *keySet) remember(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			ks.add(k)
			ks.remember(item)
		}
	case []any:
		for _, item := range x {
			ks.remember(item)
		}
	}
}

// encodeValue builds an OWNED C value from a Go value. The caller
// frees it (cValueFree) once the engine call that consumed/borrowed it
// has returned.
func encodeValue(v any) (*cVal, error) {
	switch x := v.(type) {
	case nil:
		return cValueNull(), nil
	case bool:
		return cValueBool(x), nil
	case string:
		return cValueText(x)
	case []byte:
		return cValueBytes(x)
	case []float32:
		return cValueVector(x)
	case int:
		return cValueInt(int64(x)), nil
	case int8:
		return cValueInt(int64(x)), nil
	case int16:
		return cValueInt(int64(x)), nil
	case int32:
		return cValueInt(int64(x)), nil
	case int64:
		return cValueInt(x), nil
	case uint:
		return cValueInt(int64(x)), nil
	case uint8:
		return cValueInt(int64(x)), nil
	case uint16:
		return cValueInt(int64(x)), nil
	case uint32:
		return cValueInt(int64(x)), nil
	case uint64:
		return cValueInt(int64(x)), nil
	case float32:
		return cValueFloat(float64(x)), nil
	case float64:
		return cValueFloat(x), nil
	case []any:
		arr := cArrayNew()
		for _, item := range x {
			cv, err := encodeValue(item)
			if err != nil {
				cValueFree(arr)
				return nil, err
			}
			if err := cArrayPush(arr, cv); err != nil { // cv consumed, even on failure
				cValueFree(arr)
				return nil, err
			}
		}
		return arr, nil
	case map[string]any:
		m := cMapNew()
		for k, item := range x {
			cv, err := encodeValue(item)
			if err != nil {
				cValueFree(m)
				return nil, err
			}
			if err := cMapPut(m, k, cv); err != nil { // cv consumed, even on failure
				cValueFree(m)
				return nil, err
			}
		}
		return m, nil
	default:
		return nil, newErr(ErrArgument, "unsupported Go type %T for a corvid value", v)
	}
}

// decodeValue copies a C value (owned or borrowed) into a fully
// Go-owned value. Non-map kinds always decode completely; maps decode
// via the candidate-key oracle (see the file comment) and fail loudly
// on incomplete coverage.
func decodeValue(h *cValueHandle, ks *keySet) (any, error) {
	switch cVType(h) {
	case tagNull:
		return nil, nil
	case tagBool:
		v, _ := cVAsBool(h)
		return v, nil
	case tagInt:
		v, _ := cVAsInt(h)
		return v, nil
	case tagFloat:
		v, _ := cVAsFloat(h)
		return v, nil
	case tagText:
		v, _ := cVTextRef(h)
		return v, nil
	case tagBytes:
		v, _ := cVBytesRef(h)
		return v, nil
	case tagVector:
		v, _ := cVVectorRef(h)
		return v, nil
	case tagArray:
		n := cVLen(h)
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			item, err := decodeValue(cVArrayGet(h, i), ks)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	case tagMap:
		n := cVLen(h)
		if n == 0 {
			return map[string]any{}, nil
		}
		out := make(map[string]any, n)
		for _, k := range ks.snapshot() { // nil-safe: snapshot of a nil keySet is empty
			child := cVMapGet(h, k)
			if child == nil {
				continue
			}
			item, err := decodeValue(child, ks)
			if err != nil {
				return nil, err
			}
			out[k] = item
		}
		if len(out) != n {
			return nil, fmt.Errorf("%w: %d of %d entries matched known keys", ErrMapKeyEnumeration, len(out), n)
		}
		return out, nil
	default:
		return nil, newErr(ErrDecode, "unknown value type tag %d", cVType(h))
	}
}

// walkCHandlePath walks a child path like "a.b.0.c" over a C value
// handle, mirroring the golden harness's walk: dot-separated segments,
// all-digit segments index arrays, anything else keys maps. Returns
// nil when the path is absent. The visited children are borrowed
// views; the caller must not free them (FFI.md §5).
func walkCHandlePath(root *cValueHandle, path string) *cValueHandle {
	cur := root
	i := 0
	for i < len(path) && cur != nil {
		if path[i] == '.' {
			i++
		}
		j := i
		for j < len(path) && path[j] != '.' {
			j++
		}
		seg := path[i:j]
		if seg == "" {
			break
		}
		if allDigits(seg) {
			idx := 0
			for _, c := range seg {
				idx = idx*10 + int(c-'0')
			}
			cur = cVArrayGet(cur, idx)
		} else {
			cur = cVMapGet(cur, seg)
		}
		i = j
	}
	return cur
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
