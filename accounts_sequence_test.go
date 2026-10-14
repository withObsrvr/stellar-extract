package extract

import (
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// accountWithSeqExtension builds an account carrying AccountEntryExtensionV3,
// which is where CAP-21 puts the sequence bump ledger and time. The extension
// is reachable only through the V1 -> V2 -> V3 chain, so a test that sets V3
// without the intermediate arms silently reads back zero.
func accountWithSeqExtension(t *testing.T, seqLedger xdr.Uint32, seqTime xdr.TimePoint) *xdr.AccountEntry {
	t.Helper()
	return &xdr.AccountEntry{
		AccountId: xdr.MustAddress("GBCW77HZVZAXWTFWY2BM2DPVDARDJUD3JRXAA2JF6WRY3OH3IDFOF5GR"),
		Balance:   1000,
		SeqNum:    xdr.SequenceNumber(42),
		Ext: xdr.AccountEntryExt{
			V: 1,
			V1: &xdr.AccountEntryExtensionV1{
				Liabilities: xdr.Liabilities{},
				Ext: xdr.AccountEntryExtensionV1Ext{
					V: 2,
					V2: &xdr.AccountEntryExtensionV2{
						Ext: xdr.AccountEntryExtensionV2Ext{
							V: 3,
							V3: &xdr.AccountEntryExtensionV3{
								SeqLedger: seqLedger,
								SeqTime:   seqTime,
							},
						},
					},
				},
			},
		},
	}
}

// The live ingester persists sequence_ledger and sequence_time into columns
// that exist in its schema, and wrote 0 into both because this library had no
// fields to map. Zero is worse than NULL there: it reads as data.
func TestExtractAccountDataCarriesSequenceLedgerAndTime(t *testing.T) {
	entry := accountWithSeqExtension(t, 60596414, 1767436964)

	got := extractAccountData(entry, 60596420, time.Unix(100, 0).UTC(), 60590000, nil)

	if got.SequenceLedger != 60596414 {
		t.Errorf("SequenceLedger = %d, want 60596414", got.SequenceLedger)
	}
	if got.SequenceTime != 1767436964 {
		t.Errorf("SequenceTime = %d, want 1767436964", got.SequenceTime)
	}
	// The pre-existing sequence number must not be disturbed by the addition.
	if got.SequenceNumber != 42 {
		t.Errorf("SequenceNumber = %d, want 42", got.SequenceNumber)
	}
}

// An account that has not been bumped since the extension was introduced
// carries no V3 arm at all. Both fields read zero, which is the protocol's
// own answer rather than a missing mapping.
func TestExtractAccountDataWithoutSequenceExtension(t *testing.T) {
	entry := &xdr.AccountEntry{
		AccountId: xdr.MustAddress("GBCW77HZVZAXWTFWY2BM2DPVDARDJUD3JRXAA2JF6WRY3OH3IDFOF5GR"),
		Balance:   1000,
		SeqNum:    xdr.SequenceNumber(7),
	}

	got := extractAccountData(entry, 100, time.Unix(100, 0).UTC(), 0, nil)

	if got.SequenceLedger != 0 || got.SequenceTime != 0 {
		t.Fatalf("expected zero sequence bump fields on a pre-CAP-21 account, got %d/%d",
			got.SequenceLedger, got.SequenceTime)
	}
}
