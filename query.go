// query.go — the fluent Query builder, predicates, and result types.
//
// A Query is a single-goroutine builder chain (FFI.md §6): build,
// call exactly one terminal (Run or an aggregation), done. Errors
// anywhere in the chain are held and surfaced by the terminal — Go
// errors, never panics. Predicates are consumed by Filter /
// DeleteWhere / the combinators exactly as the ABI consumes them
// (FFI.md §8: consumption happens even when the combining call
// fails); Close frees an abandoned builder or a never-consumed
// predicate, with finalizers as backstops.
//
// Documents in Run rows materialize through Select: a projected row
// decodes from exactly the selected fields (which never needs the
// map-key oracle of values.go). Without Select, Row.Doc is nil — the
// v0.2.1 ABI has no map-key iterator, so this binding refuses to
// guess; pair with Get/GetFields for point reads (docs/PLAN.md).

package corvid

import "runtime"

// Row is one query result: the key, the projected document (nil
// unless Select was called), and the relevance score (0 for
// non-scoring queries).
type Row struct {
	Key   []byte
	Doc   any
	Score float32
}

// Group is one group-aggregate result, in engine order.
type Group struct {
	Key   string
	Value float64
}

// Weighted is one weighted graph edge.
type Weighted struct {
	Key    []byte
	Weight float64
}

// GeoHit is one geo query hit; Doc decodes through the candidate-key
// oracle (nil for weighted-neighbor cursors, which carry no document).
type GeoHit struct {
	Key        []byte
	DistanceKm float64
	Doc        any
}

// FieldDef declares one schema field.
type FieldDef struct {
	Name     string
	Type     FieldType
	Required bool
	Unique   bool
}

// ---------------------------------------------------------------------------
// Predicates
// ---------------------------------------------------------------------------

// Predicate is a filter tree node. Build one from a Field expression
// (Field("n").Eq(5), Field("body").StartsWith("rust"), ...), combine
// with And/Or/Not. A Predicate is consumed by the Query.Filter,
// Collection.DeleteWhere, And/Or/Not — exactly once; Close frees a
// never-consumed root. Single-goroutine use.
type Predicate struct {
	c        *cPred
	err      error
	consumed bool
}

// Field starts a predicate over a document field (dot path).
func Field(path string) FieldExpr { return FieldExpr{path: path} }

// FieldExpr is the fluent entry for field predicates.
type FieldExpr struct {
	path string
}

func predOrErr(p *cPred, err error) *Predicate {
	if err != nil {
		return &Predicate{err: err}
	}
	pred := &Predicate{c: p}
	runtime.SetFinalizer(pred, func(p *Predicate) { p.Close() }) // backstop for never-consumed roots
	return pred
}

// Exists matches documents that carry the field.
func (f FieldExpr) Exists() *Predicate {
	return predOrErr(cPredExists(f.path))
}

// Eq matches field == v (the engine's semantic equality).
func (f FieldExpr) Eq(v any) *Predicate { return f.compare(cmpEq, v) }

// Ne matches field != v.
func (f FieldExpr) Ne(v any) *Predicate { return f.compare(cmpNe, v) }

// Lt matches field < v.
func (f FieldExpr) Lt(v any) *Predicate { return f.compare(cmpLt, v) }

// Le matches field <= v.
func (f FieldExpr) Le(v any) *Predicate { return f.compare(cmpLe, v) }

// Gt matches field > v.
func (f FieldExpr) Gt(v any) *Predicate { return f.compare(cmpGt, v) }

// Ge matches field >= v.
func (f FieldExpr) Ge(v any) *Predicate { return f.compare(cmpGe, v) }

func (f FieldExpr) compare(op uint32, v any) *Predicate {
	cv, err := encodeValue(v)
	if err != nil {
		return &Predicate{err: err}
	}
	defer cValueFree(cv) // cloned into the tree (FFI.md §5)
	return predOrErr(cPredCompare(f.path, op, cv))
}

// Between matches lo <= field <= hi.
func (f FieldExpr) Between(lo, hi any) *Predicate {
	l, err := encodeValue(lo)
	if err != nil {
		return &Predicate{err: err}
	}
	defer cValueFree(l)
	h, err := encodeValue(hi)
	if err != nil {
		return &Predicate{err: err}
	}
	defer cValueFree(h)
	return predOrErr(cPredBetween(f.path, l, h))
}

// StartsWith matches a text field's prefix.
func (f FieldExpr) StartsWith(prefix string) *Predicate {
	return predOrErr(cPredStartsWith(f.path, prefix))
}

// Contains matches a text field's substring.
func (f FieldExpr) Contains(substr string) *Predicate {
	return predOrErr(cPredContains(f.path, substr))
}

// In matches field ∈ vals.
func (f FieldExpr) In(vals ...any) *Predicate {
	cvs := make([]*cVal, len(vals))
	for i, v := range vals {
		cv, err := encodeValue(v)
		if err != nil {
			for _, done := range cvs[:i] {
				cValueFree(done)
			}
			return &Predicate{err: err}
		}
		cvs[i] = cv
	}
	defer func() {
		for _, cv := range cvs {
			cValueFree(cv)
		}
	}()
	return predOrErr(cPredIn(f.path, cvs))
}

// GeoWithin matches documents whose geo field lies within radiusKm of
// (lat, lon).
func (f FieldExpr) GeoWithin(lat, lon, radiusKm float64) *Predicate {
	return predOrErr(cPredGeoWithin(f.path, lat, lon, radiusKm))
}

// Not negates p (consuming it).
func (p *Predicate) Not() *Predicate {
	defer p.markConsumed()
	if p.err != nil {
		return &Predicate{err: p.err}
	}
	return predOrErr(cPredNot(p.c))
}

// And combines p and q (consuming both).
func (p *Predicate) And(q *Predicate) *Predicate {
	return combinePred(p, q, cPredAnd)
}

// Or combines p and q disjunctively (consuming both).
func (p *Predicate) Or(q *Predicate) *Predicate {
	return combinePred(p, q, cPredOr)
}

func combinePred(p, q *Predicate, combine func(a, b *cPred) (*cPred, error)) *Predicate {
	defer p.markConsumed()
	defer q.markConsumed()
	if p.err != nil {
		return &Predicate{err: p.err}
	}
	if q.err != nil {
		return &Predicate{err: q.err}
	}
	return predOrErr(combine(p.c, q.c))
}

// markConsumed records that the C side owns (or owned) the handle —
// the ABI consumes predicates even when the combining call fails
// (FFI.md §8), so neither Close nor the finalizer may free it again.
func (p *Predicate) markConsumed() { p.consumed = true }

// Close frees a never-consumed predicate (idempotent; the runtime
// finalizer is a backstop for abandoned builders).
func (p *Predicate) Close() {
	if p == nil {
		return
	}
	runtime.SetFinalizer(p, nil)
	if !p.consumed {
		cPredFree(p.c)
	}
	p.consumed = true
}

// ---------------------------------------------------------------------------
// Query builder
// ---------------------------------------------------------------------------

// Query is a single-shot query builder over one Collection. Chain the
// shaping methods, then call exactly one terminal (Run or an
// aggregation) — the terminal consumes the query. Single-goroutine
// use (FFI.md §6); builders are cheap.
type Query struct {
	c        *cQuery
	db       *Db
	err      error
	consumed bool
	selected bool
}

func newQuery(coll *Collection) *Query {
	q, err := cQueryNew(coll.c)
	if err != nil {
		return &Query{db: coll.db, err: err}
	}
	qy := &Query{c: q, db: coll.db}
	runtime.SetFinalizer(qy, func(q *Query) { q.release() }) // backstop for abandoned builders
	return qy
}

// Query starts a query over this collection.
func (coll *Collection) Query() *Query { return newQuery(coll) }

// step records a build-step error (first one wins; the terminal
// surfaces it).
func (q *Query) step(err error) {
	if q.err == nil {
		q.err = err
	}
}

// Filter constrains the query with pred (consuming it).
func (q *Query) Filter(pred *Predicate) *Query {
	if pred == nil {
		q.step(newErr(ErrArgument, "Filter: nil predicate"))
		return q
	}
	defer pred.markConsumed()
	if pred.err != nil {
		q.step(pred.err)
		return q
	}
	q.step(cQueryFilter(q.c, pred.c))
	return q
}

// Vector adds an ANN vector source over field (top-k, metric).
func (q *Query) Vector(field string, query []float32, k int, metric Metric) *Query {
	q.step(cQueryVector(q.c, field, query, k, metric))
	return q
}

// Text adds a BM25 text source over field (top-k).
func (q *Query) Text(field, text string, k int) *Query {
	q.step(cQueryText(q.c, field, text, k))
	return q
}

// FuseRRF fuses multiple sources with reciprocal-rank fusion (k is
// the RRF constant, e.g. 60).
func (q *Query) FuseRRF(k float32) *Query {
	q.step(cQueryFuseRRF(q.c, k))
	return q
}

// RerankMMR reranks fused sources with maximal-marginal-relevance
// (lambda trades relevance against diversity).
func (q *Query) RerankMMR(lambda float32) *Query {
	q.step(cQueryRerankMMR(q.c, lambda))
	return q
}

// Approx relaxes vector execution to approximate scanning (same
// answers for small corpora).
func (q *Query) Approx() *Query {
	q.step(cQueryApprox(q.c))
	return q
}

// Limit caps the number of rows.
func (q *Query) Limit(n int) *Query {
	q.step(cQueryLimit(q.c, n))
	return q
}

// Offset skips the first n rows.
func (q *Query) Offset(n int) *Query {
	q.step(cQueryOffset(q.c, n))
	return q
}

// OrderBy sorts by field: numbers first in value order, rows missing
// the field last, ties by key; descending reverses within class only.
func (q *Query) OrderBy(field string, descending bool) *Query {
	q.step(cQueryOrderBy(q.c, field, descending))
	return q
}

// Select projects rows to the named top-level fields — the only shape
// in which Run materializes documents (Row.Doc decodes from exactly
// these fields).
func (q *Query) Select(fields ...string) *Query {
	if len(fields) > 0 {
		q.selected = true
		q.db.ks.addAll(fields)
	}
	q.step(cQuerySelect(q.c, fields))
	return q
}

// release frees an unconsumed builder (the abandoned-builder path);
// call sites set consumed=true first when the C call already consumed
// the query.
func (q *Query) release() {
	runtime.SetFinalizer(q, nil)
	if !q.consumed {
		q.consumed = true
		cQueryFree(q.c)
	}
}

// Close frees an abandoned builder without running it (idempotent;
// finalizer backstop).
func (q *Query) Close() { q.release() }

// Run executes the query and returns its rows (consuming the query).
// Row.Doc is non-nil only under Select — see the file comment.
func (q *Query) Run() ([]Row, error) {
	defer q.release()
	if q.err != nil {
		return nil, q.err
	}
	rowsH, err := cQueryRun(q.c)
	q.consumed = true // run consumes the query even on failure
	if err != nil {
		return nil, err
	}
	defer cRowsFree(rowsH)
	var rows []Row
	for {
		key, docH, score, ok := cRowsNext(rowsH)
		if !ok {
			break
		}
		var doc any
		if q.selected {
			doc, err = decodeValue(docH, q.db.ks) // borrowed: decode before the next step
			if err != nil {
				return nil, err
			}
		}
		rows = append(rows, Row{Key: key, Doc: doc, Score: score})
	}
	return rows, nil
}

// Count returns the number of matching documents (terminal).
func (q *Query) Count() (int, error) {
	defer q.release()
	if q.err != nil {
		return 0, q.err
	}
	n, err := cQueryCount(q.c)
	q.consumed = true
	return n, err
}

// CountDistinct returns the number of distinct values of field
// (terminal).
func (q *Query) CountDistinct(field string) (int, error) {
	defer q.release()
	if q.err != nil {
		return 0, q.err
	}
	n, err := cQueryCountDistinct(q.c, field)
	q.consumed = true
	return n, err
}

// Sum returns the sum of field's numeric values (terminal).
func (q *Query) Sum(field string) (float64, error) {
	defer q.release()
	if q.err != nil {
		return 0, q.err
	}
	s, err := cQuerySum(q.c, field)
	q.consumed = true
	return s, err
}

// Avg returns the mean of field's numeric values, or ok=false when no
// document carries a numeric value there (terminal).
func (q *Query) Avg(field string) (avg float64, ok bool, err error) {
	defer q.release()
	if q.err != nil {
		return 0, false, q.err
	}
	avg, ok, err = cQueryAvg(q.c, field)
	q.consumed = true
	return avg, ok, err
}

// Min returns field's minimum value (nil, nil when absent) (terminal).
func (q *Query) Min(field string) (any, error) {
	defer q.release()
	if q.err != nil {
		return nil, q.err
	}
	v, err := cQueryMin(q.c, field)
	q.consumed = true
	if err != nil {
		return nil, err
	}
	defer cValueFree(v)
	if v == nil {
		return nil, nil
	}
	return decodeValue(v.h, q.db.ks)
}

// Max returns field's maximum value (nil, nil when absent) (terminal).
func (q *Query) Max(field string) (any, error) {
	defer q.release()
	if q.err != nil {
		return nil, q.err
	}
	v, err := cQueryMax(q.c, field)
	q.consumed = true
	if err != nil {
		return nil, err
	}
	defer cValueFree(v)
	if v == nil {
		return nil, nil
	}
	return decodeValue(v.h, q.db.ks)
}

// GroupCount counts per distinct value of field (terminal).
func (q *Query) GroupCount(field string) ([]Group, error) {
	defer q.release()
	if q.err != nil {
		return nil, q.err
	}
	it, err := cQueryGroupCount(q.c, field)
	q.consumed = true
	if err != nil {
		return nil, err
	}
	defer cGroupFree(it)
	return walkGroups(it), nil
}

// GroupSum sums valueField per distinct value of groupField (terminal).
func (q *Query) GroupSum(groupField, valueField string) ([]Group, error) {
	defer q.release()
	if q.err != nil {
		return nil, q.err
	}
	it, err := cQueryGroupSum(q.c, groupField, valueField)
	q.consumed = true
	if err != nil {
		return nil, err
	}
	defer cGroupFree(it)
	return walkGroups(it), nil
}

// GroupAvg averages valueField per distinct value of groupField
// (terminal).
func (q *Query) GroupAvg(groupField, valueField string) ([]Group, error) {
	defer q.release()
	if q.err != nil {
		return nil, q.err
	}
	it, err := cQueryGroupAvg(q.c, groupField, valueField)
	q.consumed = true
	if err != nil {
		return nil, err
	}
	defer cGroupFree(it)
	return walkGroups(it), nil
}

func walkGroups(it *cGroupIter) []Group {
	var out []Group
	for {
		key, val, ok := cGroupNext(it)
		if !ok {
			break
		}
		out = append(out, Group{Key: key, Value: val})
	}
	return out
}
