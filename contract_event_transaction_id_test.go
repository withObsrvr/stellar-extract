package extract

// ContractEventData.TransactionID must name the same transaction that
// TransactionData.TransactionID names.
//
// The two are computed independently -- transactions.go counts a 1-indexed
// txIndex over its own loop, soroban.go counts one over its own -- so they
// agree only by construction, and nothing but a test keeps them agreeing. An
// identity derived twice is how trade amounts ended up off by 10^7 and how a
// hot-buffer key that omitted OperationIndex discarded 86% of
// contract_events on insert.
//
// The field exists so Bronze can stop storing transaction_hash on every
// event: 700 GB on that table, plus 800 GB of event_id which embeds it,
// against ~4 bytes per row for an int64. That saving is only safe if this
// invariant holds, because the hash is recovered by joining on it.
//
// Gated on a fixture because Soroban events are impractical to synthesise:
//
//	STELLAR_EXTRACT_LEDGER_FIXTURE=/path/to/one-ledger.xdr go test -run TransactionID

import (
	"os"
	"testing"

	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/toid"
)

func TestContractEventTransactionIDMatchesItsTransaction(t *testing.T) {
	fixturePath := os.Getenv("STELLAR_EXTRACT_LEDGER_FIXTURE")
	if fixturePath == "" {
		t.Skip("set STELLAR_EXTRACT_LEDGER_FIXTURE to run")
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	input, err := NewLedgerInputFromXDR(raw, network.PublicNetworkPassphrase)
	if err != nil {
		t.Fatalf("NewLedgerInputFromXDR: %v", err)
	}

	txs, err := ExtractTransactions(input)
	if err != nil {
		t.Fatalf("ExtractTransactions: %v", err)
	}
	events, err := ExtractContractEvents(input)
	if err != nil {
		t.Fatalf("ExtractContractEvents: %v", err)
	}
	if len(events) == 0 {
		t.Skip("fixture has no contract events; use a Soroban-era ledger")
	}

	// The join the storage contract relies on: hash -> id, from the parent.
	wantByHash := make(map[string]int64, len(txs))
	for _, tx := range txs {
		wantByHash[tx.TransactionHash] = tx.TransactionID
	}

	var checked, zero int
	for _, ev := range events {
		want, ok := wantByHash[ev.TransactionHash]
		if !ok {
			t.Errorf("event on transaction %s has no matching transaction row; the join that "+
				"replaces transaction_hash would drop this event", ev.TransactionHash)
			continue
		}
		if ev.TransactionID == 0 {
			zero++
			continue
		}
		if ev.TransactionID != want {
			t.Errorf("event on %s: TransactionID = %d, transaction row says %d -- the two "+
				"derivations have diverged",
				ev.TransactionHash, ev.TransactionID, want)
		}
		checked++
	}

	// A column of zeros is the failure mode this project has hit before: the
	// account-sequence columns existed for months receiving nothing (F5).
	if zero > 0 {
		t.Errorf("%d of %d events have TransactionID = 0; the field is declared but not populated",
			zero, len(events))
	}
	if checked == 0 {
		t.Fatal("verified nothing; the test is broken, not the code")
	}
	t.Logf("verified %d events against %d transactions", checked, len(txs))
}

// The convention itself: 1-indexed, matching transactions.go. Off by one here
// would give every event the previous transaction's id -- a wrong answer that
// looks entirely plausible, since the ids would all be real transactions in
// the same ledger.
func TestContractEventTransactionIDIsOneIndexed(t *testing.T) {
	fixturePath := os.Getenv("STELLAR_EXTRACT_LEDGER_FIXTURE")
	if fixturePath == "" {
		t.Skip("set STELLAR_EXTRACT_LEDGER_FIXTURE to run")
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	input, err := NewLedgerInputFromXDR(raw, network.PublicNetworkPassphrase)
	if err != nil {
		t.Fatalf("NewLedgerInputFromXDR: %v", err)
	}
	txs, err := ExtractTransactions(input)
	if err != nil {
		t.Fatalf("ExtractTransactions: %v", err)
	}
	if len(txs) == 0 {
		t.Skip("fixture has no transactions")
	}

	// The first transaction in application order must be index 1, not 0.
	first := toid.New(int32(input.Sequence), 1, 0).ToInt64()
	if txs[0].TransactionID != first {
		t.Errorf("first transaction id = %d, want %d (toid index 1); the TOID convention is "+
			"1-based and soroban.go copies this counting",
			txs[0].TransactionID, first)
	}
}
