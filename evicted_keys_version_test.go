package extract

import (
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Evicted keys live on both LedgerCloseMetaV1 and V2. This extractor read
// V2 only, so an eviction on a V1 ledger was dropped silently — one arm of
// the union ignored, which no consumer could detect because every consumer
// reads this function and they all saw the same empty result.
//
// Ported from stellar-ingest, where it guarded a local extractor that
// production had stopped calling. The behaviour belongs here, with the
// implementation that is actually used.
func TestExtractEvictedKeysReadsV1AndV2(t *testing.T) {
	key, err := xdr.NewLedgerKey(xdr.LedgerEntryTypeTtl, xdr.LedgerKeyTtl{KeyHash: xdr.Hash{1}})
	if err != nil {
		t.Fatalf("create TTL ledger key: %v", err)
	}

	cases := []struct {
		name string
		lcm  xdr.LedgerCloseMeta
		want int
	}{
		{"V1", xdr.LedgerCloseMeta{V: 1, V1: &xdr.LedgerCloseMetaV1{EvictedKeys: []xdr.LedgerKey{key}}}, 1},
		{"V2", xdr.LedgerCloseMeta{V: 2, V2: &xdr.LedgerCloseMetaV2{EvictedKeys: []xdr.LedgerKey{key}}}, 1},
		// V0 predates state archival; there is no field to read.
		{"V0", xdr.LedgerCloseMeta{V: 0, V0: &xdr.LedgerCloseMetaV0{}}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractEvictedKeys(&LedgerInput{
				LCM:      tc.lcm,
				Sequence: 100,
				ClosedAt: time.Unix(0, 0).UTC(),
			})
			if err != nil {
				t.Fatalf("ExtractEvictedKeys: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("extracted %d evicted keys, want %d", len(got), tc.want)
			}
		})
	}
}
