package extract

import (
	"cmp"
	"slices"
)

// sortedMapValues returns a map's values ordered by key.
//
// Thirteen extractors deduplicate rows through a map keyed by the row's
// identity — account id, offer id, pool id, config setting id — and then
// range over that map to build their output slice. Go randomizes map
// iteration, so each of them emitted the same rows in a different order on
// every run over the same ledger.
//
// No test caught it, and none could have: the golden suite hashes rows with
// `ORDER BY h`, and the cross-ingester parity harness compares multisets.
// Both deliberately ignore row order, so the randomization was invisible
// while still making Parquet output differ byte-for-byte between two
// extractions of one ledger — which defeats content-addressed storage,
// deduplication, and any byte-level reproducibility claim about a backfill.
//
// Ordering by the dedup key introduces no new decision: that key is already
// the identity the extractor chose for the row.
func sortedMapValues[K cmp.Ordered, V any](m map[K]*V) []V {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	out := make([]V, 0, len(keys))
	for _, k := range keys {
		out = append(out, *m[k])
	}
	return out
}

// sortedSetKeys returns a set's keys in order.
//
// Same reasoning as sortedMapValues, for the sets that become JSON arrays in
// a Bronze column rather than row slices.
func sortedSetKeys[K cmp.Ordered](set map[K]bool) []K {
	keys := make([]K, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
