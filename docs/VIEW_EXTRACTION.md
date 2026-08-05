# View-backed extraction

`go-stellar-sdk v0.7.1` introduces experimental zero-copy XDR views. This
library adopts them behind an explicit `LedgerViewInput` boundary so a future
SDK signature change is localized and parsed callers remain source-compatible.

## Borrowed input contract

`NewLedgerViewInput` borrows its XDR byte slice. The input, its cached
transaction views, and every SDK view derived from it are valid only until the
upstream ledger stream advances or reuses the buffer.

Complete extraction before advancing:

```go
for ledgerXDR, err := range stream.RawLedgers(ctx, ledgerRange) {
    if err != nil {
        return err
    }
    input, err := extract.NewLedgerViewInput(ledgerXDR, networkPassphrase)
    if err != nil {
        return err
    }
    events, err := extract.ExtractContractEventsView(input)
    if err != nil {
        return err
    }
    if err := writer.AppendContractEvents(events); err != nil {
        return err
    }
    // ledgerXDR may be reused after all rows have been consumed.
}
```

Do not retain or copy `LedgerViewInput` after first use. Its synchronized cache
ensures view-backed table extractors share one SDK transaction-view walk.

Typed output rows do not retain the borrowed XDR bytes.

## Full typed surface

`ExtractAllFromXDR` is the direct raw-XDR-to-`LedgerData` entry point for the
backfill writer. Today it decodes the ledger exactly once and calls the stable
parsed extractors. This removes any need for JSON or `LedgerBatch` protobuf in
the file-backfill path without claiming the full extraction surface is already
zero-copy.

`ExtractAllView` exists for callers that need view metadata, a custom
`LedgerRange`, or `EraID` before requesting the complete typed surface. It also
decodes exactly one compatibility input.

The full surface intentionally does not run parsed and view implementations of
the same table together. A real-ledger benchmark showed that migrating only
contract events while retaining the parsed graph for every other table paid
for both representations and made total extraction about 13% slower. The
default was kept on the non-regressing one-decode path.

## Current migration status

| Table family | Input path | Notes |
| --- | --- | --- |
| Contract events | View-native | Cached `LedgerTransactionViewRange`; diagnostic, operation, and transaction-level events retain parsed-path semantics |
| All other typed rows | One parsed ledger | Existing stable extractors; migrated incrementally behind `LedgerViewInput` |

Contract events were selected first because they dominate row volume on recent
Soroban ledgers and the SDK exposes all three event shapes through transaction
views.

`ExtractLedgerTxParts` is not the contract-event boundary yet because
`EventsFromTxParts` intentionally omits diagnostic events and V3 event gating
depends on the paired envelope. `LedgerTransactionViewRange` provides
diagnostic, operation, and transaction-level events with that pairing in one
cached API. The parts API remains a candidate when transaction and fee-derived
tables migrate together.

## Differential gate

The repository does not commit another megabyte-scale ledger fixture. Point
the parity test at a real `LedgerCloseMeta` binary instead:

```bash
STELLAR_EXTRACT_LEDGER_FIXTURE=/path/to/ledger.bin \
go test -run TestExtractAllViewRealLedgerParity -v -count=1
```

The gate compares:

- errors from every extractor;
- row counts for every `LedgerData` table family; and
- every contract-event field after normalizing the intentionally nondeterministic `CreatedAt` timestamp.

The 2026-08-05 gate used SDK fixture ledger 58,752,000, a 1,278,080-byte
mainnet ledger with 249 transactions and 2,229 extracted contract-event rows.
It passed.

## Benchmark

```bash
STELLAR_EXTRACT_LEDGER_FIXTURE=/path/to/ledger.bin \
go test -run '^$' \
  -bench 'Benchmark(ContractEvents|ExtractAll)FromXDR' \
  -benchmem -count=5
```

On ledger 58,752,000 and an AMD Ryzen 9 7940HS:

| Contract events from raw XDR | Time | Allocated bytes | Allocations |
| --- | ---: | ---: | ---: |
| Parsed | 11.40–11.78 ms | 14.90 MB | ~197.7k |
| View-backed | 8.22–8.42 ms | 8.98 MB | ~129.7k |

The view path reduced time by roughly 28%, allocated bytes by 40%, and
allocations by 34%. `ExtractAllFromXDR` remains within benchmark noise of the
manual `NewLedgerInputFromXDR` plus `ExtractAll` sequence because it is a
convenience boundary over that same one-decode implementation.

## Next table migrations

Migrate a table only when its differential gate is exact and the full-surface
benchmark improves. The next candidates are transactions/operations, followed
by change-reader-backed state tables. Once enough families are view-native,
`ExtractAllView` can stop materializing the parsed ledger entirely.
