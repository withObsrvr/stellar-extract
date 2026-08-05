package extract

import (
	"os"
	"testing"

	"github.com/stellar/go-stellar-sdk/network"
)

var benchmarkContractEvents []ContractEventData
var benchmarkLedgerData *LedgerData

func BenchmarkContractEventsFromXDR(b *testing.B) {
	fixturePath := os.Getenv("STELLAR_EXTRACT_LEDGER_FIXTURE")
	if fixturePath == "" {
		b.Skip("set STELLAR_EXTRACT_LEDGER_FIXTURE to benchmark a real ledger")
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(raw)))

	b.Run("parsed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			input, err := NewLedgerInputFromXDR(raw, network.PublicNetworkPassphrase)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkContractEvents, err = ExtractContractEvents(input)
			if err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(len(benchmarkContractEvents)), "events/op")
	})

	b.Run("view", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			input, err := NewLedgerViewInput(raw, network.PublicNetworkPassphrase)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkContractEvents, err = ExtractContractEventsView(input)
			if err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(len(benchmarkContractEvents)), "events/op")
	})
}

func BenchmarkExtractAllFromXDR(b *testing.B) {
	fixturePath := os.Getenv("STELLAR_EXTRACT_LEDGER_FIXTURE")
	if fixturePath == "" {
		b.Skip("set STELLAR_EXTRACT_LEDGER_FIXTURE to benchmark a real ledger")
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(raw)))

	b.Run("parsed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			input, err := NewLedgerInputFromXDR(raw, network.PublicNetworkPassphrase)
			if err != nil {
				b.Fatal(err)
			}
			var errs []error
			benchmarkLedgerData, errs = ExtractAll(input)
			if len(errs) != 1 || errs[0].Error() != "token_transfers: failed to extract token transfer events: error reading from unified events stream, expected version 4 got 3" {
				b.Fatalf("unexpected extraction errors: %v", errs)
			}
		}
	})

	b.Run("direct", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var errs []error
			benchmarkLedgerData, errs = ExtractAllFromXDR(raw, network.PublicNetworkPassphrase)
			if len(errs) != 1 || errs[0].Error() != "token_transfers: failed to extract token transfer events: error reading from unified events stream, expected version 4 got 3" {
				b.Fatalf("unexpected extraction errors: %v", errs)
			}
		}
	})
}
