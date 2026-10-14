package extract

import (
	"reflect"
	"testing"
)

// Extractors that deduplicate through a map must emit rows in a stable
// order. Go randomizes map iteration, so ranging over the dedup map
// directly — which thirteen extractors used to do — produced the same rows
// in a different sequence on every run over one ledger.
//
// Nothing downstream noticed: the golden suite hashes rows with ORDER BY,
// and the cross-ingester parity harness compares multisets. Both ignore row
// order by design, which is exactly why the randomization could persist
// while still making Parquet output differ byte-for-byte between two
// extractions of the same ledger.
func TestSortedMapValuesIsOrderedByKey(t *testing.T) {
	type row struct{ ID string }
	m := map[string]*row{
		"zeta":  {ID: "zeta"},
		"alpha": {ID: "alpha"},
		"mid":   {ID: "mid"},
		"beta":  {ID: "beta"},
	}

	want := []row{{ID: "alpha"}, {ID: "beta"}, {ID: "mid"}, {ID: "zeta"}}
	for i := 0; i < 100; i++ {
		if got := sortedMapValues(m); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: got %v, want %v", i, got, want)
		}
	}
}

// Numeric keys must sort numerically, not as text. offers and
// config_settings are keyed by int64 and int32 respectively, where a
// lexicographic order would put 10 before 9.
func TestSortedMapValuesSortsNumericKeysNumerically(t *testing.T) {
	type row struct{ N int }
	m := map[int64]*row{9: {N: 9}, 10: {N: 10}, 1: {N: 1}}

	got := sortedMapValues(m)
	want := []row{{N: 1}, {N: 9}, {N: 10}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSortedMapValuesOnEmptyMap(t *testing.T) {
	type row struct{ ID string }
	if got := sortedMapValues(map[string]*row{}); len(got) != 0 {
		t.Fatalf("expected no rows, got %v", got)
	}
}

// Every dedup extractor must go through the helper rather than ranging over
// its map. This is the guard that keeps the fix from being undone one
// extractor at a time: a new extractor that copies the old pattern fails
// here instead of silently reintroducing randomized output.
func TestNoExtractorRangesOverItsDedupMap(t *testing.T) {
	offenders := findMapRangeAppends(t)
	for _, o := range offenders {
		t.Errorf("%s ranges over dedup map %q and appends: row order will vary between runs. "+
			"Use sortedMapValues(%s) instead.", o.where, o.mapName, o.mapName)
	}
}
