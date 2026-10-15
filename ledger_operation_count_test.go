package extract

// tx_set_operation_count counts what the transaction set declared, not what
// produced operation results.
//
// These differ for a transaction rejected before its operations run --
// txBAD_SEQ, txINSUFFICIENT_FEE, txNO_ACCOUNT. Such a transaction is in the
// set and declares operations, but OperationResults() reports none for it,
// so counting results silently drops every operation it declared.
//
// This library counted results until v0.3.4. stellar-ingest counted
// envelopes, and caught the difference when it moved onto ExtractLedgers:
// a ledger whose transactions carry no operation results reported
// tx_set_operation_count = 0 where it had reported 37.

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
)

func envelopeDeclaring(operations int) xdr.TransactionEnvelope {
	return xdr.TransactionEnvelope{
		Type: xdr.EnvelopeTypeEnvelopeTypeTx,
		V1: &xdr.TransactionV1Envelope{
			Tx: xdr.Transaction{Operations: make([]xdr.Operation, operations)},
		},
	}
}

func TestTxSetOperationCountCountsDeclaredOperations(t *testing.T) {
	envelopes := []xdr.TransactionEnvelope{envelopeDeclaring(29), envelopeDeclaring(8)}
	components := []xdr.TxSetComponent{{
		Type:                  xdr.TxSetComponentTypeTxsetCompTxsMaybeDiscountedFee,
		TxsMaybeDiscountedFee: &xdr.TxSetComponentTxsMaybeDiscountedFee{Txs: envelopes},
	}}

	// No TxProcessing entries at all: every transaction in this set was
	// rejected before producing operation results.
	lcm := xdr.LedgerCloseMeta{
		V: 1,
		V1: &xdr.LedgerCloseMetaV1{
			LedgerHeader: xdr.LedgerHeaderHistoryEntry{
				Header: xdr.LedgerHeader{LedgerSeq: xdr.Uint32(3_501_542)},
			},
			TxSet: xdr.GeneralizedTransactionSet{
				V:       1,
				V1TxSet: &xdr.TransactionSetV1{Phases: []xdr.TransactionPhase{{V: 0, V0Components: &components}}},
			},
		},
	}

	rows, err := ExtractLedgers(NewLedgerInput(lcm, "Test SDF Network ; September 2015"))
	if err != nil {
		t.Fatalf("ExtractLedgers: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d ledger rows, want 1", len(rows))
	}
	if got := rows[0].TxSetOperationCount; got != 37 {
		t.Errorf("TxSetOperationCount = %d, want 37 (29 + 8 declared); "+
			"counting operation results instead of envelopes gives 0 here", got)
	}
}
