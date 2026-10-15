package extract

// A fee-bumped transaction keeps its preconditions and its Soroban
// resource declaration on the *inner* transaction. xdr.TransactionEnvelope
// exposes helpers that resolve through the wrapper -- Preconditions(),
// and ingest's GetSorobanData() -- but reaching into Envelope.V1 directly
// returns nothing for a FEE_BUMP envelope and reads as "this transaction
// declared none".
//
// That is not hypothetical. Reading Envelope.V1 directly found Soroban
// resources on 42 of 14,590 transactions across the committed fixtures;
// resolving through the fee bump finds 1,706. Forty times more, and the
// difference is silent -- a NULL column, not an error.

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func innerWithPreconditionsAndSoroban(t *testing.T) *xdr.TransactionV1Envelope {
	t.Helper()
	source := xdr.MustMuxedAddress("GA243VFHTL7K5TSB4PMWZPXBL6VXMC7XII6I6CA76G3AEHA26FPJJTDF")
	minSeq := xdr.SequenceNumber(12345)
	return &xdr.TransactionV1Envelope{
		Tx: xdr.Transaction{
			SourceAccount: source,
			Fee:           100,
			Cond: xdr.Preconditions{
				Type: xdr.PreconditionTypePrecondV2,
				V2: &xdr.PreconditionsV2{
					LedgerBounds: &xdr.LedgerBounds{MinLedger: 10, MaxLedger: 20},
					MinSeqNum:    &minSeq,
					MinSeqAge:    xdr.Duration(99),
				},
			},
			Ext: xdr.TransactionExt{
				V: 1,
				SorobanData: &xdr.SorobanTransactionData{
					Resources:   xdr.SorobanResources{Instructions: 7, DiskReadBytes: 8, WriteBytes: 9},
					ResourceFee: 4242,
				},
			},
		},
	}
}

func TestFeeBumpInnerPreconditionsAndSorobanAreRead(t *testing.T) {
	inner := innerWithPreconditionsAndSoroban(t)
	feeSource := xdr.MustMuxedAddress("GCFX7B74KOFPZ3ZXF4B2TYJIYOELME4RAOKNVRNZWLYTOACTNOWAGTQM")

	// Meta V3 on purpose: every committed fixture is V4, so the V3 arm --
	// where contract events live on SorobanMeta rather than per-operation
	// -- is otherwise never exercised.
	tx := ingest.LedgerTransaction{
		UnsafeMeta: xdr.TransactionMeta{
			V: 3,
			V3: &xdr.TransactionMetaV3{
				SorobanMeta: &xdr.SorobanTransactionMeta{
					Events: []xdr.ContractEvent{{Type: xdr.ContractEventTypeContract},
						{Type: xdr.ContractEventTypeContract}},
				},
			},
		},
		Envelope: xdr.TransactionEnvelope{
			Type: xdr.EnvelopeTypeEnvelopeTypeTxFeeBump,
			FeeBump: &xdr.FeeBumpTransactionEnvelope{
				Tx: xdr.FeeBumpTransaction{
					FeeSource: feeSource,
					Fee:       2000,
					InnerTx: xdr.FeeBumpTransactionInnerTx{
						Type: xdr.EnvelopeTypeEnvelopeTypeTx,
						V1:   inner,
					},
				},
			},
		},
	}

	var data TransactionData
	if err := populateFeeBumpFields(tx, "Test SDF Network ; September 2015", &data); err != nil {
		t.Fatalf("populateFeeBumpFields: %v", err)
	}
	populatePreconditionFields(tx, &data)
	if err := populateSorobanDeclaredFields(tx, &data); err != nil {
		t.Fatalf("populateSorobanDeclaredFields: %v", err)
	}

	if data.FeeBumpFee == nil || *data.FeeBumpFee != 2000 {
		t.Errorf("FeeBumpFee = %v, want 2000", deref(data.FeeBumpFee))
	}
	if data.InnerSourceAccount == nil ||
		*data.InnerSourceAccount != "GA243VFHTL7K5TSB4PMWZPXBL6VXMC7XII6I6CA76G3AEHA26FPJJTDF" {
		t.Errorf("InnerSourceAccount = %v", data.InnerSourceAccount)
	}
	if data.InnerTransactionHash == nil || len(*data.InnerTransactionHash) != 64 {
		t.Errorf("InnerTransactionHash = %v, want a 64-char hex hash", data.InnerTransactionHash)
	}

	// The three that silently returned nil before.
	if data.LedgerboundsMin == nil || *data.LedgerboundsMin != 10 {
		t.Errorf("LedgerboundsMin = %v, want 10 — preconditions not resolved through the fee bump",
			deref(data.LedgerboundsMin))
	}
	if data.MinSequenceNumber == nil || *data.MinSequenceNumber != 12345 {
		t.Errorf("MinSequenceNumber = %v, want 12345", deref(data.MinSequenceNumber))
	}
	if data.MinSequenceAge == nil || *data.MinSequenceAge != 99 {
		t.Errorf("MinSequenceAge = %v, want 99", deref(data.MinSequenceAge))
	}
	if data.SorobanDataResources == nil {
		t.Error("SorobanDataResources is nil — Soroban data not resolved through the fee bump")
	}
	if data.SorobanDataSizeBytes == nil || *data.SorobanDataSizeBytes == 0 {
		t.Errorf("SorobanDataSizeBytes = %v, want the marshalled length",
			deref32(data.SorobanDataSizeBytes))
	}

	// The V3 arm: GetTransactionEvents puts SorobanMeta.Events at
	// OperationEvents[0], so the count must equal len(SorobanMeta.Events).
	if data.SorobanContractEventsCount == nil || *data.SorobanContractEventsCount != 2 {
		t.Errorf("SorobanContractEventsCount = %v, want 2 from V3 SorobanMeta.Events",
			deref32(data.SorobanContractEventsCount))
	}
}

// A plain V1 transaction with no preconditions leaves them nil rather than
// zero: "no minimum sequence number" and "minimum sequence number zero" are
// different facts.
func TestAbsentPreconditionsStayNil(t *testing.T) {
	tx := ingest.LedgerTransaction{
		Envelope: xdr.TransactionEnvelope{
			Type: xdr.EnvelopeTypeEnvelopeTypeTx,
			V1: &xdr.TransactionV1Envelope{
				Tx: xdr.Transaction{
					SourceAccount: xdr.MustMuxedAddress("GA243VFHTL7K5TSB4PMWZPXBL6VXMC7XII6I6CA76G3AEHA26FPJJTDF"),
					Cond:          xdr.Preconditions{Type: xdr.PreconditionTypePrecondNone},
				},
			},
		},
	}
	var data TransactionData
	populatePreconditionFields(tx, &data)
	if data.LedgerboundsMin != nil || data.MinSequenceNumber != nil ||
		data.MinSequenceAge != nil || data.ExtraSigners != nil {
		t.Errorf("undeclared preconditions should be nil, got %+v", data)
	}
	if data.FeeBumpFee != nil {
		t.Error("a non-fee-bump transaction should leave FeeBumpFee nil")
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func deref32(p *int32) any {
	if p == nil {
		return nil
	}
	return *p
}
