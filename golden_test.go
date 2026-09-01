// golden_test.go — the golden-suite port, corvid-go's port of the
// engine's reference harness (corvid-db/corvid, crates/corvid-ffi/c/
// smoke.c, MIT) as ported standalone by corvid-c/test/golden.c.
//
// Same job as upstream, different moment of truth: the engine's
// harness links the cdylib cargo just built and reads the golden/
// fixtures committed in the engine repo; this one drives the cdylib
// DOWNLOADED from the pinned GitHub release (fetch.sh / fetch.ps1 put
// it, corvid.h, and the release's golden/ under deps/) through THIS
// BINDING — the Go API wherever it can express the op, the cgo value
// family where the op is inherently raw (VTYPE/VLEN/VAS_*/V*_REF/
// VNEST/VCLONE/VPUSH/VPUT are value-handle exercises and go through
// encodeValue + the read wrappers). If the published .so/.dylib/.dll,
// header, or fixtures disagree with the engine's suite, THIS fails
// where that one stayed green — divergence is a finding for the engine
// repo, never patched around here.
//
// The mechanics are kept deliberately IDENTICAL to the C harness so
// the suites are diffable and their verdicts comparable: the same
// fixture grammar (OP<TAB>args<TAB>expected; value literals with
// bits:/bits32: NaN specials; ~x computed-double tolerance), the same
// dispatch table, the same checks, one line at a time, every line
// dispatched, every expectation checked — no softened asserts. Two
// counting rules carry over verbatim: `lines` comes from an
// INDEPENDENT pre-scan (so a dispatch loop that skips a counted line
// diverges from `executed`), and the first failure names file:line +
// OP + expected-vs-got.
//
// Verdict protocol: stdout (test log) carries one
// "SMOKE <file> lines=<n> executed=<n>" line per fixture.

package corvid

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// -------------------------------------------------------------------
// Scenario state
// -------------------------------------------------------------------

type scenario struct {
	t          *testing.T
	file       string
	line       int
	op         string
	db         *Db
	coll       *Collection
	workdir    string
	dbPath     string
	db2Path    string
	dumpPath   string
	backupPath string
	lastAutoID int64
}

func (S *scenario) fail(format string, args ...any) {
	S.t.Fatalf("FAIL %s:%d OP=%s: "+format, append([]any{S.file, S.line, S.op}, args...)...)
}

func (S *scenario) check(cond bool, format string, args ...any) {
	if !cond {
		S.fail(format, args...)
	}
}

// expectOK mirrors the C harness's expect_ok: CORVID_OK or bust.
func (S *scenario) expectOK(err error) {
	if err != nil {
		S.fail("expected ok, got %v", err)
	}
}

// expectErr mirrors the C harness's expect_err: a failure with exactly
// this code AND a recorded message (driving the error-reporting pair
// through the Go error surface).
func (S *scenario) expectErr(err error, code ErrCode) {
	if err == nil {
		S.fail("expected CORVID_ERR code %d, got success", code)
	}
	var ce *CorvidError
	if !errors.As(err, &ce) {
		S.fail("expected a *CorvidError, got %T: %v", err, err)
	}
	if ce.Code() != code {
		S.fail("expected error code %d, got %d (%s)", code, ce.Code(), ce.Message())
	}
	if ce.Message() == "" {
		S.fail("error code %d recorded but the message is missing", code)
	}
}

func (S *scenario) closeColl() {
	if S.coll != nil {
		S.coll.Close()
		S.coll = nil
	}
}

func (S *scenario) closeDB() {
	S.closeColl()
	if S.db != nil {
		S.expectOK(S.db.Close())
		S.db = nil
	}
}

// docs (re)acquires the primary "docs" collection handle.
func (S *scenario) docs() *Collection {
	if S.coll == nil {
		S.check(S.db != nil, "no database open in this scenario")
		c, err := S.db.Collection("docs")
		S.check(err == nil, "Collection(docs) failed: %v", err)
		S.coll = c
	}
	return S.coll
}

func (S *scenario) openMemory() {
	S.closeDB()
	db, err := OpenMemory()
	S.check(err == nil, "OpenMemory failed: %v", err)
	S.db = db
	S.docs()
}

func (S *scenario) openFile(path string) {
	S.closeDB()
	db, err := Open(path)
	S.check(err == nil, "Open(%s) failed: %v", path, err)
	S.db = db
	S.docs()
}

func (S *scenario) setColl(name string) {
	S.closeColl()
	c, err := S.db.Collection(name)
	S.check(err == nil, "Collection(%s) failed: %v", name, err)
	S.check(c.Name() == name, "collection_name round trip failed")
	S.coll = c
}

// -------------------------------------------------------------------
// Spans and tokenizing (the C harness's split_top, verbatim)
// -------------------------------------------------------------------

// splitTop splits on top-level commas (depth-aware over []{}()),
// trimming trailing spaces; empty input yields no tokens.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i <= len(s); i++ {
		c := byte(',')
		if i < len(s) {
			c = s[i]
		}
		switch c {
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		}
		if c == ',' && depth == 0 {
			end := i
			for end > start && (s[end-1] == ' ' || s[end-1] == '\r') {
				end--
			}
			if end > start {
				out = append(out, s[start:end])
			}
			start = i + 1
		}
	}
	return out
}

func (S *scenario) parseI64(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		S.fail("bad int token %q: %v", s, err)
	}
	return n
}

func (S *scenario) parseInt(s string) int { return int(S.parseI64(s)) }

// parseHex mirrors strtoull(s, NULL, 16): an optional 0x/0X prefix,
// then hex digits.
func (S *scenario) parseHex(s string) uint64 {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	n, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		S.fail("bad hex token %q: %v", s, err)
	}
	return n
}

// parseDouble mirrors the C harness's parse_double: bits:0x… (f64 from
// bits), inf/-inf/nan, else decimal (correctly rounded).
func (S *scenario) parseDouble(s string) float64 {
	if strings.HasPrefix(s, "bits:") {
		return math.Float64frombits(S.parseHex(s[5:]))
	}
	switch s {
	case "inf":
		return math.Inf(1)
	case "-inf":
		return math.Inf(-1)
	case "nan":
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		S.fail("bad float token %q: %v", s, err)
	}
	return f
}

func doubleExact(got, want float64) bool {
	return math.Float64bits(got) == math.Float64bits(want)
}

func doubleNear(got, want float64) bool {
	return math.Abs(got-want) <= 1e-6*(1.0+math.Abs(want))
}

// doubleMatches matches one expected-double token: `~x` near; `=x`/
// bare/bits:/inf bit-exact (NaN payloads included).
func (S *scenario) doubleMatches(got float64, tok string) bool {
	switch {
	case strings.HasPrefix(tok, "~"):
		return doubleNear(got, S.parseDouble(tok[1:]))
	case strings.HasPrefix(tok, "="):
		return doubleExact(got, S.parseDouble(tok[1:]))
	default:
		return doubleExact(got, S.parseDouble(tok))
	}
}

// errToken parses the err:N expected token.
func (S *scenario) errToken(expected string) ErrCode {
	S.check(strings.HasPrefix(expected, "err:"), "error expectation must be err:N, got %q", expected)
	n, err := strconv.ParseUint(expected[4:], 10, 32)
	if err != nil {
		S.fail("bad err token %q: %v", expected, err)
	}
	return ErrCode(n)
}

// -------------------------------------------------------------------
// Value literals: parse into Go values (then encodeValue builds the
// C side — exercising the binding's value mapping end to end)
// -------------------------------------------------------------------

func (S *scenario) startsWord(s string, i int, word string) bool {
	if !strings.HasPrefix(s[i:], word) {
		return false
	}
	after := i + len(word)
	if after >= len(s) {
		return true
	}
	c := s[after]
	return c == ',' || c == ']' || c == '}' || c == ' ' || c == '\r'
}

// matchParen finds the ')' matching the '(' at open.
func (S *scenario) matchParen(s string, open int) int {
	depth := 0
	for q := open; q < len(s); q++ {
		switch s[q] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return q
			}
		}
	}
	S.fail("unbalanced () in literal")
	return len(s)
}

// matchBracket finds the close bracket for the opener at open.
func (S *scenario) matchBracket(s string, open int) int {
	depth := 0
	for q := open; q < len(s); q++ {
		switch s[q] {
		case s[open]:
			depth++
		case closeOf(s[open]):
			depth--
			if depth == 0 {
				return q
			}
		}
	}
	S.fail("unbalanced %c in literal", s[open])
	return len(s)
}

func closeOf(open byte) byte {
	switch open {
	case '[':
		return ']'
	case '{':
		return '}'
	}
	return ')'
}

func skipWS(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\r') {
		i++
	}
	return i
}

// buildNumber scans one numeric literal (int vs float classified by
// the characters seen, exactly like the C harness).
func (S *scenario) buildNumber(s string, i int) (any, int) {
	start := i
	if S.startsWord(s, i, "inf") {
		return math.Inf(1), i + 3
	}
	if S.startsWord(s, i, "-inf") {
		return math.Inf(-1), i + 4
	}
	if S.startsWord(s, i, "nan") {
		return math.NaN(), i + 3
	}
	isFloat, isBits := false, false
	if strings.HasPrefix(s[i:], "bits:") {
		isFloat, isBits = true, true
		i += 5 // scan the hex payload only
	}
	for i < len(s) {
		c := s[i]
		switch {
		case (c >= '0' && c <= '9') || c == '-' || c == '+':
			i++
		case c == '.' || c == 'e' || c == 'E':
			isFloat = true
			i++
		case isBits && ((c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == 'x' || c == 'X'):
			i++
		default:
			goto scanned
		}
	}
scanned:
	tok := s[start:i]
	if tok == "" {
		S.fail("empty numeric literal")
	}
	if isBits { // re-include the prefix, as the C harness does
		return S.parseDouble(tok), i
	}
	if isFloat {
		f, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			S.fail("bad float literal %q: %v", tok, err)
		}
		return f, i
	}
	return S.parseI64(tok), i
}

// buildLit parses one literal at s[i:], returning its Go value and
// the index just past it.
func (S *scenario) buildLit(s string, i int) (any, int) {
	i = skipWS(s, i)
	if i >= len(s) {
		S.fail("empty literal")
	}
	start, c := i, s[i]

	if c == '-' || (c >= '0' && c <= '9') {
		return S.buildNumber(s, i)
	}
	// bits:/inf/-inf/nan start with letters but are NUMBERS; they must
	// win over the b(...)/t(...) literal heads.
	if strings.HasPrefix(s[i:], "bits:") || S.startsWord(s, i, "inf") ||
		S.startsWord(s, i, "-inf") || S.startsWord(s, i, "nan") {
		return S.buildNumber(s, i)
	}
	if S.startsWord(s, i, "null") {
		return nil, i + 4
	}
	if S.startsWord(s, i, "true") {
		return true, i + 4
	}
	if S.startsWord(s, i, "false") {
		return false, i + 5
	}

	if (c == 't' || c == 'b') && i+1 < len(s) && s[i+1] == '(' {
		close := S.matchParen(s, i+1)
		body := s[i+2 : close]
		i = close + 1
		if c == 't' {
			return body, i
		}
		return []byte(body), i
	}
	if strings.HasPrefix(s[i:], "vec(") {
		close := S.matchParen(s, i+3)
		body := s[i+4 : close]
		i = close + 1
		return S.buildVec(body), i
	}

	if c == '[' {
		close := S.matchBracket(s, i)
		arr := []any{}
		j := i + 1
		for j < close {
			var item any
			item, j = S.buildLit(s, j)
			arr = append(arr, item)
			j = skipWS(s, j)
			if j < close && s[j] == ',' {
				j++
			}
		}
		return arr, close + 1
	}

	if c == '{' {
		close := S.matchBracket(s, i)
		m := map[string]any{}
		j := i + 1
		for j < close {
			j = skipWS(s, j)
			ks := j
			for j < close && s[j] != '=' && s[j] != ',' && s[j] != '}' {
				j++
			}
			if j >= close || s[j] != '=' {
				S.fail("map literal needs k=v pairs")
			}
			key := strings.TrimLeft(s[ks:j], " ")
			j++ // past '='
			var val any
			val, j = S.buildLit(s, j)
			m[key] = val
			j = skipWS(s, j)
			if j < close && s[j] == ',' {
				j++
			}
		}
		return m, close + 1
	}

	snippet := s[start:]
	if len(snippet) > 24 {
		snippet = snippet[:24]
	}
	S.fail("unparseable literal at %q", snippet)
	return nil, i
}

func (S *scenario) buildVec(body string) []float32 {
	toks := splitTop(body)
	vals := make([]float32, 0, len(toks))
	for _, tk := range toks {
		if strings.HasPrefix(tk, "bits32:") {
			vals = append(vals, math.Float32frombits(uint32(S.parseHex(tk[7:]))))
		} else {
			vals = append(vals, float32(S.parseDouble(tk)))
		}
	}
	return vals
}

func (S *scenario) lit(s string) any {
	v, _ := S.buildLit(s, 0)
	return v
}

// encode builds an OWNED C value from a literal token, failing the
// line on a binding encode error.
func (S *scenario) encode(literal string) *cVal {
	v, err := encodeValue(S.lit(literal))
	S.expectOK(err)
	return v
}

// -------------------------------------------------------------------
// Structural comparison of Go-side values (bit-exact floats — the
// decode side of the C harness's read-API comparison)
// -------------------------------------------------------------------

func valuesEqualGo(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case int64:
		y, ok := b.(int64)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && math.Float64bits(x) == math.Float64bits(y)
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []byte:
		y, ok := b.([]byte)
		return ok && bytes.Equal(x, y)
	case []float32:
		y, ok := b.([]float32)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if math.Float32bits(x[i]) != math.Float32bits(y[i]) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !valuesEqualGo(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			yv, present := y[k]
			if !present || !valuesEqualGo(v, yv) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// checkValue compares a decoded Go value against an expected literal
// token (bit-exact; NaN payloads included).
func (S *scenario) checkValue(got any, wantTok string) {
	want := S.lit(wantTok)
	S.check(valuesEqualGo(got, want), "value mismatch: got %#v, want %#v", got, want)
}

// -------------------------------------------------------------------
// Cursor walkers (the C harness's RowWalk, keyed off the Go API's
// returned rows/cursors)
// -------------------------------------------------------------------

func rowKeys(rows []Row) []string {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = string(r.Key)
	}
	return keys
}

func rowScores(rows []Row) []float32 {
	scores := make([]float32, len(rows))
	for i, r := range rows {
		scores[i] = r.Score
	}
	return scores
}

func bytesKeys(keys [][]byte) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = string(k)
	}
	return out
}

// checkKeys matches "k(a,b,c)" — key order exact.
func (S *scenario) checkKeys(keys []string, expected string) {
	S.check(len(expected) >= 3 && expected[0] == 'k' && expected[1] == '(' && expected[len(expected)-1] == ')',
		"key expectation must be k(...), got %q", expected)
	body := expected[2 : len(expected)-1]
	var want []string
	if body != "" {
		want = splitTop(body)
	}
	S.check(len(keys) == len(want), "row count %d, expected %d (%v)", len(keys), len(want), keys)
	for i := range want {
		S.check(keys[i] == want[i], "row %d key %q, expected %q", i, keys[i], want[i])
	}
}

// checkScores matches a "|~s1,~s2" suffix — one double token per row.
func (S *scenario) checkScores(scores []float32, suffix string) {
	if suffix == "" {
		return
	}
	S.check(suffix[0] == '|', "score suffix must start with |, got %q", suffix)
	body := suffix[1:]
	if body == "" {
		return
	}
	toks := splitTop(body)
	S.check(len(scores) == len(toks), "score count %d, expected %d", len(scores), len(toks))
	for i := range toks {
		got := float64(scores[i])
		S.check(S.doubleMatches(got, toks[i]), "row %d score %.9g does not match %q", i, got, toks[i])
	}
}

func keyPart(expected string) string {
	if i := strings.IndexByte(expected, '|'); i >= 0 {
		return expected[:i]
	}
	return expected
}

func suffixPart(expected string) string {
	if i := strings.IndexByte(expected, '|'); i >= 0 {
		return expected[i:]
	}
	return ""
}

// textBody extracts the t(...) literal body.
func (S *scenario) textBody(tok string) string {
	S.check(len(tok) >= 3 && tok[0] == 't' && tok[1] == '(' && tok[len(tok)-1] == ')',
		"expected a t(...) literal, got %q", tok)
	return tok[2 : len(tok)-1]
}

// listBody extracts the k(...) list body.
func (S *scenario) listBody(tok string) string {
	S.check(len(tok) >= 3 && tok[0] == 'k' && tok[1] == '(' && tok[len(tok)-1] == ')',
		"expected a k(...) list, got %q", tok)
	return tok[2 : len(tok)-1]
}

// -------------------------------------------------------------------
// Predicate / enum parse helpers
// -------------------------------------------------------------------

func (S *scenario) fieldCmp(path, op string, v any) *Predicate {
	f := Field(path)
	switch op {
	case "eq":
		return f.Eq(v)
	case "ne":
		return f.Ne(v)
	case "lt":
		return f.Lt(v)
	case "le":
		return f.Le(v)
	case "gt":
		return f.Gt(v)
	case "ge":
		return f.Ge(v)
	}
	S.fail("bad cmp op %q", op)
	return nil
}

func (S *scenario) parseMetric(s string) Metric {
	switch s {
	case "cosine":
		return MetricCosine
	case "dot":
		return MetricDot
	case "l2":
		return MetricL2
	}
	S.fail("bad metric %q", s)
	return MetricCosine
}

func (S *scenario) parseQuant(s string) Quant {
	switch s {
	case "none":
		return QuantNone
	case "binary":
		return QuantBinary
	case "scalar":
		return QuantScalar
	}
	S.fail("bad quant %q", s)
	return QuantNone
}

func (S *scenario) parseFieldType(s string) FieldType {
	switch s {
	case "any":
		return FieldAny
	case "bool":
		return FieldBool
	case "int":
		return FieldInt
	case "float":
		return FieldFloat
	case "text":
		return FieldText
	case "bytes":
		return FieldBytes
	case "vector":
		return FieldVector
	case "array":
		return FieldArray
	case "map":
		return FieldMap
	}
	S.fail("bad field type %q", s)
	return FieldAny
}

// filteredCount is the (filter) → count workhorse: builds, filters,
// counts — all consumed by the terminal.
func (S *scenario) filteredCount(p *Predicate) int64 {
	n, err := S.docs().Query().Filter(p).Count()
	S.expectOK(err)
	return int64(n)
}

func (S *scenario) expectNum(expected string, got int64) {
	S.check(S.parseI64(expected) == got, "expected %d, want %q", got, expected)
}

// -------------------------------------------------------------------
// OP implementations (the C harness's run_line, op for op)
// -------------------------------------------------------------------

func (S *scenario) runLine(op, args, expected string) {
	a := splitTop(args)

	// ---- pure value ops (no db) ----
	switch op {
	case "VERSION":
		S.check(FFIVersion() == 1, "FFI_VERSION must be 1, got %d", FFIVersion())
		return

	case "VTYPE":
		names := []string{"null", "bool", "int", "float", "text", "bytes", "array", "map", "vector"}
		v := S.encode(a[0])
		defer cValueFree(v)
		t := cVType(v.h)
		S.check(t <= 8, "type tag %d out of range", t)
		S.check(expected == names[t], "type %s, want %q", names[t], expected)
		return

	case "VLEN":
		v := S.encode(a[0])
		defer cValueFree(v)
		S.expectNum(expected, int64(cVLen(v.h)))
		return

	case "VAS_INT", "VAS_FLOAT", "VAS_BOOL":
		v := S.encode(a[0])
		defer cValueFree(v)
		switch op {
		case "VAS_INT":
			got, ok := cVAsInt(v.h)
			if expected == "fail" {
				S.check(!ok, "as_int unexpectedly ok (%d)", got)
			} else {
				S.check(ok, "as_int failed")
				S.check(expected == "ok:"+strconv.FormatInt(got, 10), "as_int ok:%d, want %q", got, expected)
			}
		case "VAS_FLOAT":
			got, ok := cVAsFloat(v.h)
			if expected == "fail" {
				S.check(!ok, "as_float unexpectedly ok")
			} else {
				S.check(ok, "as_float failed")
				S.check(strings.HasPrefix(expected, "ok:"), "as_float expectation must be ok:<double>, got %q", expected)
				S.check(S.doubleMatches(got, expected[3:]),
					"as_float 0x%016x (%g) does not match %q", math.Float64bits(got), got, expected[3:])
			}
		default:
			got, ok := cVAsBool(v.h)
			if expected == "fail" {
				S.check(!ok, "as_bool unexpectedly ok")
			} else {
				S.check(ok, "as_bool failed")
				want := "ok:0"
				if got {
					want = "ok:1"
				}
				S.check(expected == want, "as_bool %s, want %q", want, expected)
			}
		}
		return

	case "VTEXT_REF", "VBYTES_REF", "VVECTOR_REF":
		v := S.encode(a[0])
		defer cValueFree(v)
		switch op {
		case "VTEXT_REF":
			got, ok := cVTextRef(v.h)
			S.check(ok, "text_ref returned NULL for a text value")
			body := S.textBody(expected)
			S.check(got == body, "text bytes differ: got %q, want %q", got, body)
		case "VBYTES_REF":
			got, ok := cVBytesRef(v.h)
			S.check(ok, "bytes_ref returned NULL for a bytes value")
			S.check(len(expected) >= 3 && expected[0] == 'b' && expected[1] == '(', "bytes expectation must be b(...), got %q", expected)
			body := expected[2 : len(expected)-1]
			S.check(string(got) == body, "bytes differ: got %q, want %q", got, body)
		default:
			got, ok := cVVectorRef(v.h)
			S.check(ok, "vector_ref returned NULL for a vector value")
			want := S.lit(a[0]).([]float32)
			S.check(len(got) == len(want), "ref dim %d, rebuilt dim %d", len(got), len(want))
			for i := range want {
				S.check(math.Float32bits(got[i]) == math.Float32bits(want[i]), "vector elem %d differs bit-exactly", i)
			}
		}
		return

	case "VNEST", "VCLONE":
		root := S.encode(a[0])
		defer cValueFree(root)
		holder := root
		if op == "VCLONE" {
			cl, err := cVClone(root.h)
			S.expectOK(err)
			defer cValueFree(cl)
			holder = cl
		}
		child := walkCHandlePath(holder.h, a[1])
		if expected == "absent" {
			S.check(child == nil, "path unexpectedly present")
		} else {
			S.check(child != nil, "path unexpectedly absent, want %q", expected)
			got, err := decodeValue(child, nil)
			S.expectOK(err)
			S.checkValue(got, expected)
		}
		return

	case "VPUSH":
		arr := S.encode(a[0])
		defer cValueFree(arr)
		item := S.encode(a[1])
		S.expectOK(cArrayPush(arr, item)) // consumes item
		S.expectNum(expected, int64(cVLen(arr.h)))
		return

	case "VPUT":
		m := S.encode(a[0])
		defer cValueFree(m)
		val := S.encode(a[2])
		S.expectOK(cMapPut(m, a[1], val)) // consumes val
		S.expectNum(expected, int64(cVLen(m.h)))
		return

	case "NULLFREES":
		cNullFrees() // every _free(NULL) shape is a documented no-op (§7)
		return
	}

	// ---- db-required ops from here on ----
	switch op {
	case "COLL":
		S.setColl(a[0])
		return

	case "INSERT", "INSERT_ERR":
		err := S.docs().Insert([]byte(a[0]), S.lit(a[1]))
		if op == "INSERT_ERR" {
			S.expectErr(err, S.errToken(expected))
		} else {
			S.expectOK(err)
		}
		return

	case "LEN":
		n, err := S.docs().Len()
		S.expectOK(err)
		S.expectNum(expected, int64(n))
		return

	case "GET", "GETFIELD":
		if op == "GETFIELD" {
			m, err := S.docs().GetFields([]byte(a[0]), a[1])
			S.expectOK(err)
			got, present := m[a[1]]
			if expected == "absent" {
				S.check(!present, "field unexpectedly present")
			} else {
				S.check(present, "field unexpectedly absent, want %q", expected)
				S.checkValue(got, expected)
			}
			return
		}
		if expected == "absent" {
			doc, err := S.docs().Get([]byte(a[0]))
			S.expectOK(err)
			S.check(doc == nil, "expected absence, got a document: %#v", doc)
			return
		}
		want := S.lit(expected)
		S.db.ks.remember(want) // the expectation is the key source, same as the C harness's maps_equal
		doc, err := S.docs().Get([]byte(a[0]))
		S.expectOK(err)
		S.check(doc != nil, "expected a document, got absence")
		S.checkValue(doc, expected)
		return

	case "PUTMANY", "PUTMANY_ROLLBACK":
		S.check(len(a)%2 == 0, "PUTMANY wants key/literal pairs")
		count := len(a) / 2
		keys := make([][]byte, count)
		docs := make([]any, count)
		for i := 0; i < count; i++ {
			keys[i] = []byte(a[2*i])
			docs[i] = S.lit(a[2*i+1])
		}
		err := S.docs().PutMany(keys, docs)
		if op == "PUTMANY_ROLLBACK" {
			S.expectErr(err, S.errToken(expected))
		} else {
			S.expectOK(err)
		}
		return

	case "INSERT_AUTO":
		key, err := S.docs().InsertAuto(S.lit(a[0]))
		S.expectOK(err)
		S.check(len(key) == 20, "auto key length %d, want 20", len(key))
		var id int64
		for _, b := range key {
			S.check(b >= '0' && b <= '9', "auto key not zero-padded digits: %q", key)
			id = id*10 + int64(b-'0')
		}
		S.check(S.lastAutoID == 0 || id > S.lastAutoID, "auto id %d not monotonic (previous %d)", id, S.lastAutoID)
		S.lastAutoID = id
		return

	case "UPDATE":
		S.expectOK(S.docs().Update([]byte(a[0]), func(current any) (any, error) {
			var n int64
			if current != nil {
				m, ok := current.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("update_bump: current doc is not a map: %T", current)
				}
				f, ok := m["n"]
				if !ok {
					return nil, errors.New("update_bump: current doc lacks field n")
				}
				n, ok = f.(int64)
				if !ok {
					return nil, fmt.Errorf("update_bump: field n is not an int: %T", f)
				}
			}
			return map[string]any{"n": n + 1}, nil
		}))
		return

	case "UPDATE_ABORT":
		err := S.docs().Update([]byte(a[0]), func(current any) (any, error) {
			return nil, errors.New("update_abort: aborting per the fixture")
		})
		S.expectErr(err, ErrArgument)
		return

	case "PATCH":
		S.expectOK(S.docs().Patch([]byte(a[0]), S.lit(a[1])))
		return

	case "CAS":
		var ex, re any
		if a[1] != "absent" {
			ex = S.lit(a[1])
		}
		if a[2] != "absent" {
			re = S.lit(a[2])
		}
		applied, err := S.docs().CompareAndSet([]byte(a[0]), ex, re)
		S.expectOK(err)
		want := "applied:0"
		if applied {
			want = "applied:1"
		}
		S.check(expected == want, "CAS applied=%t, want %q (expected %q)", applied, want, expected)
		return

	case "DELETE":
		existed, err := S.docs().Delete([]byte(a[0]))
		S.expectOK(err)
		want := "existed:0"
		if existed {
			want = "existed:1"
		}
		S.check(expected == want, "delete existed=%t, want %q", existed, want)
		return

	case "DELETE_WHERE":
		removed, err := S.docs().DeleteWhere(S.fieldCmp(a[0], a[1], S.lit(a[2])))
		S.expectOK(err)
		S.check(expected == fmt.Sprintf("removed:%d", removed), "removed %d, want %q", removed, expected)
		return

	case "DELETE_IN":
		vals := make([]any, len(a)-1)
		for i := range vals {
			vals[i] = S.lit(a[i+1])
		}
		removed, err := S.docs().DeleteWhere(Field(a[0]).In(vals...))
		S.expectOK(err)
		S.check(expected == fmt.Sprintf("removed:%d", removed), "removed %d, want %q", removed, expected)
		return

	case "DELETE_BATCH":
		keys := make([][]byte, len(a))
		for i := range a {
			keys[i] = []byte(a[i])
		}
		removed, err := S.docs().DeleteBatch(keys...)
		S.expectOK(err)
		S.check(expected == fmt.Sprintf("removed:%d", removed), "removed %d, want %q", removed, expected)
		return

	case "INSERT_TTL":
		S.expectOK(S.docs().InsertTTL([]byte(a[0]), S.lit(a[1]), S.parseI64(a[2])))
		return

	case "GET_TTL":
		at, has, err := S.docs().GetTTL([]byte(a[0]))
		S.expectOK(err)
		var got string
		if has {
			got = fmt.Sprintf("ttl:%d", at)
		} else {
			got = "nottl"
		}
		S.check(expected == got, "ttl %s, want %q", got, expected)
		return

	case "SET_TTL":
		S.expectOK(S.docs().SetTTL([]byte(a[0]), S.parseI64(a[1])))
		return

	case "PURGE":
		purged, err := S.docs().PurgeExpired(S.parseI64(a[0]))
		S.expectOK(err)
		S.check(expected == fmt.Sprintf("purged:%d", purged), "purged %d, want %q", purged, expected)
		return

	case "SCAN", "SCAN_STOP":
		stop := 0
		if op == "SCAN_STOP" {
			stop = S.parseInt(a[0])
		}
		count := 0
		err := S.docs().Scan(func(key []byte, doc any) bool {
			count++
			return stop <= 0 || count < stop
		})
		S.expectOK(err)
		S.expectNum(expected, int64(count))
		return

	case "PAGE":
		var after []byte
		if a[0] != "-" {
			after = []byte(a[0])
		}
		rows, next, err := S.docs().Page(after, S.parseInt(a[1]))
		S.expectOK(err)
		S.checkKeys(rowKeys(rows), keyPart(expected))
		sp := suffixPart(expected)
		atEnd := next == nil
		want := "|more"
		if atEnd {
			want = "|end"
		}
		S.check(sp == want, "page cursor %s, want %q", want, sp)
		return
	}

	// ---- predicates + queries ----
	switch op {
	case "QF_COUNT":
		S.expectNum(expected, S.filteredCount(S.fieldCmp(a[0], a[1], S.lit(a[2]))))
		return

	case "QF_EXISTS":
		S.expectNum(expected, S.filteredCount(Field(a[0]).Exists()))
		return

	case "QF_BETWEEN":
		S.expectNum(expected, S.filteredCount(Field(a[0]).Between(S.lit(a[1]), S.lit(a[2]))))
		return

	case "QF_STARTS", "QF_CONTAINS":
		body := S.textBody(a[1])
		var p *Predicate
		if op == "QF_STARTS" {
			p = Field(a[0]).StartsWith(body)
		} else {
			p = Field(a[0]).Contains(body)
		}
		S.expectNum(expected, S.filteredCount(p))
		return

	case "QF_GEO":
		S.expectNum(expected, S.filteredCount(Field(a[0]).GeoWithin(S.parseDouble(a[1]), S.parseDouble(a[2]), S.parseDouble(a[3]))))
		return

	case "QF_AND", "QF_OR":
		l := S.fieldCmp(a[0], a[1], S.lit(a[2]))
		r := S.fieldCmp(a[3], a[4], S.lit(a[5]))
		var p *Predicate
		if op == "QF_AND" {
			p = l.And(r)
		} else {
			p = l.Or(r)
		}
		S.expectNum(expected, S.filteredCount(p))
		return

	case "QF_NOT":
		S.expectNum(expected, S.filteredCount(S.fieldCmp(a[0], a[1], S.lit(a[2])).Not()))
		return

	case "PRED_FREE":
		S.fieldCmp(a[0], a[1], S.lit(a[2])).Close() // the never-consumed-root free path
		return

	case "Q_ABANDON":
		S.docs().Query().Close() // the abandoned-builder free path
		return

	case "QVEC", "APPROX":
		q := S.docs().Query()
		if op == "APPROX" {
			q.Approx()
		}
		q.Vector(a[0], S.lit(a[1]).([]float32), S.parseInt(a[2]), MetricCosine)
		rows, err := q.Run()
		S.expectOK(err)
		S.checkKeys(rowKeys(rows), keyPart(expected))
		S.checkScores(rowScores(rows), suffixPart(expected))
		return

	case "QTEXT":
		rows, err := S.docs().Query().Text(a[0], S.textBody(a[1]), S.parseInt(a[2])).Run()
		S.expectOK(err)
		S.checkKeys(rowKeys(rows), expected)
		return

	case "HYBRID", "HYBRID_F":
		// args: vfield vec k tfield t(query) tk [tagvalue] limit — the
		// tagvalue (HYBRID_F) slides the limit to the LAST slot.
		// (HYBRID adds a kind=doc filter; HYBRID_F a tag=<arg6> filter)
		tagged := op == "HYBRID_F"
		vk := S.parseInt(a[2])
		tk := S.parseInt(a[5])
		limitIdx := 6
		var filter *Predicate
		if tagged {
			filter = Field("tag").Eq(S.lit(a[6]))
			limitIdx = 7
		} else {
			filter = Field("kind").Eq("doc")
		}
		rows, err := S.docs().Query().
			Filter(filter).
			Vector(a[0], S.lit(a[1]).([]float32), vk, MetricCosine).
			Text(a[3], S.textBody(a[4]), tk).
			FuseRRF(60.0).
			RerankMMR(1.0).
			Limit(S.parseInt(a[limitIdx])).
			Run()
		S.expectOK(err)
		S.checkKeys(rowKeys(rows), keyPart(expected))
		S.checkScores(rowScores(rows), suffixPart(expected))
		return

	case "ORDER_BY":
		rows, err := S.docs().Query().
			OrderBy(a[0], S.parseInt(a[1]) != 0).
			Offset(S.parseInt(a[2])).
			Limit(S.parseInt(a[3])).
			Run()
		S.expectOK(err)
		S.checkKeys(rowKeys(rows), expected)
		return

	case "SELECT":
		// args: (field,field,...) k(row-key); expected: that row's
		// projected document.
		S.check(len(a[0]) >= 2 && a[0][0] == '(' && a[0][len(a[0])-1] == ')',
			"SELECT's first arg must be a (field,...) group, got %q", a[0])
		fields := splitTop(a[0][1 : len(a[0])-1])
		rows, err := S.docs().Query().Select(fields...).Run()
		S.expectOK(err)
		wantKey := S.listBody(a[1])
		var doc any
		found := false
		for _, r := range rows {
			if string(r.Key) == wantKey {
				doc = r.Doc
				found = true
			}
		}
		S.check(found, "row %q not in the result", wantKey)
		S.checkValue(doc, expected)
		return

	case "AGG_COUNT":
		n, err := S.docs().Query().Count()
		S.expectOK(err)
		S.expectNum(expected, int64(n))
		return

	case "AGG_DISTINCT":
		n, err := S.docs().Query().CountDistinct(a[0])
		S.expectOK(err)
		S.expectNum(expected, int64(n))
		return

	case "AGG_SUM":
		sum, err := S.docs().Query().Sum(a[0])
		S.expectOK(err)
		S.check(S.doubleMatches(sum, expected), "sum %.17g vs %q", sum, expected)
		return

	case "AGG_AVG":
		avg, has, err := S.docs().Query().Avg(a[0])
		S.expectOK(err)
		if expected == "none" {
			S.check(!has, "avg has=%t, want none", has)
		} else {
			S.check(has, "avg has=false, want %q", expected)
			S.check(S.doubleMatches(avg, expected), "avg %.17g vs %q", avg, expected)
		}
		return

	case "AGG_MIN", "AGG_MAX":
		q := S.docs().Query()
		var out any
		var err error
		if op == "AGG_MIN" {
			out, err = q.Min(a[0])
		} else {
			out, err = q.Max(a[0])
		}
		S.expectOK(err)
		if expected == "absent" {
			S.check(out == nil, "expected absence")
		} else {
			S.check(out != nil, "expected a value, got absence")
			S.checkValue(out, expected)
		}
		return

	case "AGG_GCOUNT", "AGG_GSUM", "AGG_GAVG":
		q := S.docs().Query()
		var groups []Group
		var err error
		switch op {
		case "AGG_GCOUNT":
			groups, err = q.GroupCount(a[0])
		case "AGG_GSUM":
			groups, err = q.GroupSum(a[0], a[1])
		default:
			groups, err = q.GroupAvg(a[0], a[1])
		}
		S.expectOK(err)
		// §7 inert rule exercised once with a NULL handle.
		S.check(cGroupIterNilNextOK(), "NULL-handle groupiter_next must answer 0")
		S.check(len(expected) >= 3 && expected[0] == 'g' && expected[1] == '(' && expected[len(expected)-1] == ')',
			"group expectation must be g(...), got %q", expected)
		body := expected[2 : len(expected)-1]
		var pairs []string
		if body != "" {
			pairs = splitTop(body)
		}
		S.check(len(groups) == len(pairs), "group count %d, expected %d", len(groups), len(pairs))
		for i, pair := range pairs {
			eq := strings.LastIndexByte(pair, '=')
			S.check(eq > 0, "group pair needs key=val, got %q", pair)
			key, vtok := pair[:eq], pair[eq+1:]
			S.check(groups[i].Key == key, "group key %q, want %q", groups[i].Key, key)
			S.check(S.doubleMatches(groups[i].Value, vtok), "group %q value %.17g vs %q", key, groups[i].Value, vtok)
		}
		return
	}

	// ---- graph ----
	switch op {
	case "LINK":
		S.expectOK(S.docs().Link([]byte(a[0]), a[1], []byte(a[2])))
		return

	case "LINK_W":
		S.expectOK(S.docs().LinkWeighted([]byte(a[0]), a[1], []byte(a[2]), S.parseDouble(a[3])))
		return

	case "UNLINK":
		removed, err := S.docs().Unlink([]byte(a[0]), a[1], []byte(a[2]))
		S.expectOK(err)
		want := "removed:0"
		if removed {
			want = "removed:1"
		}
		S.check(expected == want, "unlink removed=%t, want %q", removed, want)
		return

	case "NEIGHBORS", "IN_NEIGHBORS":
		var keys [][]byte
		var err error
		if op == "NEIGHBORS" {
			keys, err = S.docs().Neighbors([]byte(a[0]), a[1])
		} else {
			keys, err = S.docs().InNeighbors([]byte(a[0]), a[1])
		}
		S.expectOK(err)
		S.checkKeys(bytesKeys(keys), expected)
		return

	case "NEIGHBORS_W":
		weighted, err := S.docs().NeighborsWeighted([]byte(a[0]), a[1])
		S.expectOK(err)
		S.check(len(expected) >= 3 && expected[0] == 'g' && expected[1] == '(' && expected[len(expected)-1] == ')',
			"weighted expectation must be g(...), got %q", expected)
		body := expected[2 : len(expected)-1]
		var pairs []string
		if body != "" {
			pairs = splitTop(body)
		}
		S.check(len(weighted) == len(pairs), "weighted hits %d, expected %d", len(weighted), len(pairs))
		for i, pair := range pairs {
			eq := strings.LastIndexByte(pair, '=')
			S.check(eq > 0, "weighted pair needs key=val, got %q", pair)
			key, vtok := pair[:eq], pair[eq+1:]
			S.check(string(weighted[i].Key) == key, "weighted key %q, want %q", weighted[i].Key, key)
			S.check(S.doubleMatches(weighted[i].Weight, vtok), "weight of %q %.17g vs %q", key, weighted[i].Weight, vtok)
		}
		return

	case "TRAVERSE":
		keys, err := S.docs().Traverse([]byte(a[0]), a[1], S.parseInt(a[2]))
		S.expectOK(err)
		S.checkKeys(bytesKeys(keys), expected)
		return
	}

	// ---- geo ----
	switch op {
	case "GINSERT", "GINSERT_M":
		var loc any
		if op == "GINSERT_M" { // {lat, lon} map form
			loc = map[string]any{"lat": S.parseDouble(a[1]), "lon": S.parseDouble(a[2])}
		} else {
			loc = []any{S.parseDouble(a[1]), S.parseDouble(a[2])}
		}
		S.expectOK(S.docs().Insert([]byte(a[0]), map[string]any{"loc": loc}))
		return

	case "RADIUS", "NEAREST", "BBOX":
		var hits []GeoHit
		var err error
		switch op {
		case "RADIUS":
			hits, err = S.docs().GeoWithinRadius(a[0], S.parseDouble(a[1]), S.parseDouble(a[2]), S.parseDouble(a[3]))
		case "NEAREST":
			hits, err = S.docs().GeoNearest(a[0], S.parseDouble(a[1]), S.parseDouble(a[2]), S.parseInt(a[3]))
		default:
			hits, err = S.docs().GeoWithinBBox(a[0], S.parseDouble(a[1]), S.parseDouble(a[2]), S.parseDouble(a[3]), S.parseDouble(a[4]))
		}
		S.expectOK(err)
		keys := make([]string, len(hits))
		dists := make([]float64, len(hits))
		for i, h := range hits {
			keys[i] = string(h.Key)
			dists[i] = h.DistanceKm
		}
		S.checkKeys(keys, keyPart(expected))
		if sp := suffixPart(expected); sp != "" {
			S.check(sp[0] == '|', "geo suffix must start with |, got %q", sp)
			var toks []string
			if body := sp[1:]; body != "" {
				toks = splitTop(body)
			}
			S.check(len(dists) == len(toks), "distance count %d, expected %d", len(dists), len(toks))
			for i := range toks {
				S.check(S.doubleMatches(dists[i], toks[i]), "hit %d distance %.9g vs %q", i, dists[i], toks[i])
			}
		}
		return

	case "BBOX_ERR":
		_, err := S.docs().GeoWithinBBox(a[0], S.parseDouble(a[1]), S.parseDouble(a[2]), S.parseDouble(a[3]), S.parseDouble(a[4]))
		S.expectErr(err, S.errToken(expected))
		return
	}

	// ---- schema & indexes ----
	switch op {
	case "SET_SCHEMA":
		specs := splitTop(args)
		defs := make([]FieldDef, len(specs))
		for i, spec := range specs {
			// field specs split on '#' (no nesting inside a spec)
			part := strings.Split(spec, "#")
			S.check(len(part) == 4, "field spec needs name#type#required#unique, got %q", spec)
			defs[i] = FieldDef{
				Name:     part[0],
				Type:     S.parseFieldType(part[1]),
				Required: part[2] == "1",
				Unique:   part[3] == "1",
			}
		}
		S.expectOK(S.docs().SetSchema(defs...))
		return

	case "SCHEMA":
		tn := map[FieldType]string{
			FieldAny: "any", FieldBool: "bool", FieldInt: "int", FieldFloat: "float",
			FieldText: "text", FieldBytes: "bytes", FieldVector: "vector",
			FieldArray: "array", FieldMap: "map",
		}
		defs, err := S.docs().Schema()
		S.expectOK(err)
		S.check(defs != nil, "a schema must be declared first")
		parts := make([]string, 0, len(defs))
		for _, f := range defs {
			r, u := 0, 0
			if f.Required {
				r = 1
			}
			if f.Unique {
				u = 1
			}
			parts = append(parts, fmt.Sprintf("%s/%s/%d/%d", f.Name, tn[f.Type], r, u))
		}
		got := strings.Join(parts, ",")
		S.check(expected == got, "schema %s, want %q", got, expected)
		return

	case "SCHEMA9":
		names := []string{"f_any", "f_bool", "f_int", "f_float", "f_text", "f_bytes", "f_vector", "f_array", "f_map"}
		types := []FieldType{FieldAny, FieldBool, FieldInt, FieldFloat, FieldText, FieldBytes, FieldVector, FieldArray, FieldMap}
		defs := make([]FieldDef, 9)
		for i := range defs {
			defs[i] = FieldDef{Name: names[i], Type: types[i], Required: i == 1, Unique: i == 8}
		}
		S.expectOK(S.docs().SetSchema(defs...))
		got, err := S.docs().Schema()
		S.expectOK(err)
		S.check(got != nil, "the 9-field schema must be declared")
		tags := make([]string, 0, len(got))
		for i, f := range got {
			S.check(i < 9 && f.Type == types[i] && f.Name == names[i], "field %d did not round-trip", i)
			tags = append(tags, strconv.FormatUint(uint64(f.Type), 10))
		}
		S.check(len(got) == 9, "expected exactly 9 fields, saw %d", len(got))
		joined := strings.Join(tags, ",")
		S.check(expected == joined, "schema9 %s, want %q", joined, expected)
		return

	case "SCHEMA_ERR":
		err := S.docs().Insert([]byte(a[0]), S.lit(a[1]))
		S.expectErr(err, S.errToken(expected))
		return

	case "IDX_SCALAR":
		S.expectOK(S.docs().CreateScalarIndex(a[0]))
		return

	case "IDX_COMPOUND":
		S.expectOK(S.docs().CreateCompoundIndex(splitTop(args)...))
		return

	case "IDX_TEXT":
		S.expectOK(S.docs().CreateTextIndex(a[0]))
		return

	case "IDX_TEXT_DISK":
		S.expectOK(S.docs().CreateTextIndexOnDisk(a[0]))
		return

	case "IDX_GEO":
		S.expectOK(S.docs().CreateGeoIndex(a[0]))
		return

	case "IDX_VEC":
		S.expectOK(S.docs().CreateVectorIndex(a[0], S.parseMetric(a[1])))
		return

	case "IDX_VEC_Q":
		S.expectOK(S.docs().CreateVectorIndexQuantized(a[0], S.parseMetric(a[1]), S.parseQuant(a[2])))
		return

	case "IDX_VEC_DISK":
		S.expectOK(S.docs().CreateVectorIndexOnDisk(a[0], S.parseMetric(a[1])))
		return

	case "IDX_VEC_DISK_Q":
		S.expectOK(S.docs().CreateVectorIndexOnDiskQuantized(a[0], S.parseMetric(a[1]), S.parseQuant(a[2])))
		return

	case "IDX_PQ", "IDX_PQ_DISK", "IDX_PQ_ERR":
		var err error
		if op == "IDX_PQ_DISK" {
			err = S.docs().CreateVectorIndexOnDiskPQ(a[0], S.parseMetric(a[1]), S.parseInt(a[2]), S.parseInt(a[3]))
		} else {
			err = S.docs().CreateVectorIndexPQ(a[0], S.parseMetric(a[1]), S.parseInt(a[2]), S.parseInt(a[3]))
		}
		if op == "IDX_PQ_ERR" {
			S.expectErr(err, S.errToken(expected))
		} else {
			S.expectOK(err)
		}
		return
	}

	// ---- admin & persistence ----
	switch op {
	case "FILEDB":
		S.openFile(S.dbPath)
		return

	case "FILEDB2":
		S.openFile(S.db2Path)
		return

	case "DUMP":
		S.expectOK(S.db.Dump(S.dumpPath))
		return

	case "LOAD":
		S.expectOK(S.db.Load(S.dumpPath))
		return

	case "LOAD_RENAMES":
		err := S.db.LoadWithRenames(S.dumpPath, map[string]string{a[0]: a[1]})
		if strings.HasPrefix(expected, "err:") {
			S.expectErr(err, S.errToken(expected))
		} else {
			S.expectOK(err)
		}
		return

	case "COLLECTIONS":
		names, err := S.db.Collections()
		S.expectOK(err)
		S.checkKeys(names, expected)
		return

	case "BACKUP":
		S.expectOK(S.db.Backup(S.backupPath))
		return

	case "BACKUP_DUP":
		err := S.db.Backup(S.backupPath)
		S.expectErr(err, ErrBackupTargetExists)
		return

	case "COMPACT_BUSY":
		S.expectErr(cCompactBusy(S.db.c), ErrBusy)
		return

	case "COMPACT":
		S.closeColl() // quiesce: the derived-handle gate (§4.13)
		moved, err := S.db.Compact()
		S.expectOK(err)
		_ = moved // the ABI's int32 boolean is already folded into Go bool
		S.docs()  // re-acquire for subsequent lines
		return

	case "REOPEN":
		path := S.dbPath
		S.closeDB()
		db, err := Open(path)
		S.check(err == nil, "reopen of %s failed: %v", path, err)
		S.db = db
		S.docs()
		return
	}

	S.fail("unknown OP %q", op)
}

// -------------------------------------------------------------------
// Fixture-file driver
// -------------------------------------------------------------------

// values.txt runs against no db; every other file starts in-memory
// (admin/persist switch to file dbs via their OPs).
func startsWithDB(path string) bool {
	return filepath.Base(path) != "values.txt"
}

func runFixture(t *testing.T, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot open fixture %s: %v", path, err)
	}
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, ".txt")
	dir := t.TempDir()

	S := &scenario{
		t:          t,
		file:       path,
		workdir:    dir,
		dbPath:     filepath.Join(dir, stem+".redb"),
		db2Path:    filepath.Join(dir, stem+"-2.redb"),
		dumpPath:   filepath.Join(dir, stem+".dump"),
		backupPath: filepath.Join(dir, stem+".backup.redb"),
	}
	defer S.closeDB()
	if startsWithDB(path) {
		S.openMemory()
	}

	lines := strings.Split(string(data), "\n")

	// `lines` is counted in an INDEPENDENT pre-scan (the same rule the
	// Rust/C drivers apply), so a dispatch loop that skips a counted
	// line — a stray continue, a swallowed branch — diverges from
	// `executed` below, instead of the two fields silently reading one
	// counter.
	counted := 0
	for _, raw := range lines {
		first := 0
		for first < len(raw) && (raw[first] == ' ' || raw[first] == '\r') {
			first++
		}
		if first < len(raw) && raw[first] != '#' {
			counted++
		}
	}

	executed := 0
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		S.line = executed + 1
		S.op = line // refined below; kept whole for the unknown-OP message

		// OP \t ARGS \t EXPECTED
		parts := strings.SplitN(line, "\t", 3)
		op := parts[0]
		args, expected := "", ""
		if len(parts) >= 2 {
			args = parts[1]
		}
		if len(parts) >= 3 {
			expected = parts[2]
		}
		S.op = op
		S.runLine(op, args, expected)
		executed++
	}

	if executed != counted {
		S.fail("dispatched %d of %d counted executable lines", executed, counted)
	}
	t.Logf("SMOKE %s lines=%d executed=%d", path, counted, executed)
}

// TestGolden replays the engine's golden fixture suite (256 executable
// lines across 8 files) through this binding. Vendored byte-identical
// under golden/; fetch.sh byte-compares them against the pinned
// release's copies on every `make deps`.
func TestGolden(t *testing.T) {
	if v := FFIVersion(); v != 1 {
		t.Fatalf("FAIL wrong FFI_VERSION %d", v)
	}
	for _, name := range []string{"values", "mutations", "queries", "schema", "geo", "graph", "admin", "persist"} {
		name := name
		t.Run(name, func(t *testing.T) {
			runFixture(t, filepath.Join("golden", name+".txt"))
		})
	}
}
