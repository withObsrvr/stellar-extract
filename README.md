# stellar-extract

Shared Go library for extracting typed rows from Stellar ledger data. Single source of truth for bronze-layer extraction logic across the Obsrvr data platform.

## Install

```bash
go get github.com/withObsrvr/stellar-extract@latest
```

Only dependency: `github.com/stellar/go-stellar-sdk v0.7.3` (Protocol 28).

Upgrading that pin is a protocol change, not a routine bump — see
[docs/SDK_UPGRADES.md](docs/SDK_UPGRADES.md).

## Quick start

```go
package main

import (
    "fmt"
    "log"

    extract "github.com/withObsrvr/stellar-extract"
    "github.com/stellar/go-stellar-sdk/xdr"
)

func main() {
    // Direct raw XDR to the complete typed surface.
    data, errs := extract.ExtractAllFromXDR(xdrBytes, "Test SDF Network ; September 2015")

    // Or from an already-decoded LedgerCloseMeta (history loader, nebu)
    // input := extract.NewLedgerInput(lcm, "Test SDF Network ; September 2015")
    // data, errs := extract.ExtractAll(input)
    for _, e := range errs {
        log.Printf("warning: %v", e)
    }

    fmt.Printf("ledgers=%d transactions=%d operations=%d effects=%d\n",
        len(data.Ledgers), len(data.Transactions), len(data.Operations), len(data.Effects))

    // Or extract a single table
    transfers, err := extract.ExtractTokenTransfers(input)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("token_transfers=%d\n", len(transfers))
}
```

## API

### Entry points

```go
// Create input from decoded LedgerCloseMeta
extract.NewLedgerInput(lcm xdr.LedgerCloseMeta, networkPassphrase string) *LedgerInput

// Create input from raw XDR bytes
extract.NewLedgerInputFromXDR(xdrBytes []byte, networkPassphrase string) (*LedgerInput, error)

// Direct raw XDR to the complete typed LedgerData surface
extract.ExtractAllFromXDR(xdrBytes []byte, networkPassphrase string) (*LedgerData, []error)

// Create an explicit borrowed view input
extract.NewLedgerViewInput(xdrBytes []byte, networkPassphrase string) (*LedgerViewInput, error)

// View-backed contract events without a full ledger decode
extract.ExtractContractEventsView(input *LedgerViewInput) ([]ContractEventData, error)

// Run all extractors concurrently
extract.ExtractAll(input *LedgerInput) (*LedgerData, []error)
```

### Individual extractors

Every extractor has the same signature: `func Extract*(input *LedgerInput) ([]TypeData, error)`

| Function | Output type | Bronze table |
|---|---|---|
| `ExtractLedgers` | `[]LedgerRowData` | `ledgers_row_v2` |
| `ExtractTransactions` | `[]TransactionData` | `transactions_row_v2` |
| `ExtractOperations` | `[]OperationData` | `operations_row_v2` |
| `ExtractEffects` | `[]EffectData` | `effects_row_v1` |
| `ExtractTrades` | `[]TradeData` | `trades_row_v1` |
| `ExtractAccounts` | `[]AccountData` | `accounts_snapshot_v1` |
| `ExtractTrustlines` | `[]TrustlineData` | `trustlines_snapshot_v1` |
| `ExtractAccountSigners` | `[]AccountSignerData` | `account_signers_snapshot_v1` |
| `ExtractNativeBalances` | `[]NativeBalanceData` | `native_balances_snapshot_v1` |
| `ExtractContractEvents` | `[]ContractEventData` | `contract_events_stream_v1` |
| `ExtractContractData` | `[]ContractDataData` | `contract_data_snapshot_v1` |
| `ExtractContractCode` | `[]ContractCodeData` | `contract_code_snapshot_v1` |
| `ExtractContractCreations` | `[]ContractCreationData` | `contract_creations_v1` |
| `ExtractTokenTransfers` | `[]TokenTransferData` | `token_transfers_stream_v1` |
| `ExtractEvictedKeys` | `[]EvictedKeyData` | `evicted_keys_state_v1` |
| `ExtractRestoredKeys` | `[]RestoredKeyData` | `restored_keys_state_v1` |

### LedgerInput

```go
type LedgerInput struct {
    LCM               xdr.LedgerCloseMeta
    NetworkPassphrase string
    Sequence          uint32    // auto-set from LCM
    ClosedAt          time.Time // auto-set from LCM
    LedgerRange       uint32    // partition key, default: floor(seq/10000)
    EraID             *string   // optional DuckLake era identifier
}
```

`Sequence`, `ClosedAt`, and `LedgerRange` are populated automatically by `NewLedgerInput` / `NewLedgerInputFromXDR`. Override `LedgerRange` or set `EraID` after creation if needed.

### LedgerViewInput

`LedgerViewInput` is the explicit borrowed-XDR boundary for full-history work.
Finish extraction before the upstream `LedgerStream` advances or reuses its
buffer. View-backed typed rows do not retain the borrowed bytes. The full
contract, differential gate, benchmarks, and migration status are documented
in [docs/VIEW_EXTRACTION.md](docs/VIEW_EXTRACTION.md).

## Usage patterns

### Obsrvr Lake: history loader

Replace 16 local extractor files with a single library import:

```go
input := extract.NewLedgerInput(lcm, networkPassphrase)
input.LedgerRange = customRange
input.EraID = &eraID
data, errs := extract.ExtractAll(input)
// Write data.Transactions to Parquet, data.Effects to Parquet, etc.
```

### Obsrvr Lake: streaming ingester

Replace `(w *Writer) extract*` methods:

```go
input, _ := extract.NewLedgerInputFromXDR(rawLedger.LedgerCloseMetaXdr, networkPassphrase)
data, errs := extract.ExtractAll(input)
// Batch insert data.Transactions into PG, data.Effects into PG, etc.
```

### Nebu processor

Use a single extractor for a focused processor:

```go
func (p *Processor) ProcessLedger(lcm xdr.LedgerCloseMeta) error {
    input := extract.NewLedgerInput(lcm, p.networkPassphrase)
    transfers, err := extract.ExtractTokenTransfers(input)
    if err != nil {
        return err
    }
    for _, t := range transfers {
        p.emit(toProtobuf(t))
    }
    return nil
}
```

### flowctl-sdk processor

Zero-code extraction via the stellar helper:

```go
func main() {
    stellar.Run(func(np string, lcm xdr.LedgerCloseMeta) (proto.Message, error) {
        input := extract.NewLedgerInput(lcm, np)
        events, err := extract.ExtractContractEvents(input)
        if err != nil {
            return nil, err
        }
        return toProtobuf(events), nil
    })
}
```

## Upgrading to v0.3.0

Both remaining Bronze tables without a working row identity now have one.
Both changes alter existing column values, not just add columns.

### `contract_events`: one index across all three event streams

A transaction carries three independent event lists — diagnostic,
per-operation, and transaction-level — and each numbered from zero into the
same `(operation_index, event_index)` pair. A diagnostic event, an operation
event and a transaction event could all land on `0:0`. `event_id` is built
from that pair, so it collided too: roughly 4% of events on a busy Soroban
ledger shared an identity with another, and any sink deduplicating on it
dropped them. One did, losing 86% of the table.

`EventIndex` is now a single counter across all three streams, so
`(ledger_sequence, transaction_hash, event_index)` identifies a row and
`event_id` is `txHash:eventIndex`. Verified unique over 104,730 events.

`EventStream` is new: `diagnostic`, `operation` or `transaction`. It exists
because `OperationIndex` was never meaningful on two of the three streams —
diagnostic events were carrying their position in the diagnostic list in
that column, which reads like an operation index and never was one. It is
now 0 on diagnostic and transaction-level rows, with `EventStream` saying
why.

### `token_transfers`: `EventIndex` added

The row carried no index at all, and no combination of its attributes
identifies a transfer: one operation may transfer the same amount between
the same parties twice, which occurs in practice. `EventIndex` is the row's
position within its transaction, making
`(ledger_sequence, transaction_hash, event_index)` a key. Verified unique
over 40,606 transfers.

### Migration

Both `event_id` and `event_index` change value for existing rows, so a
consumer keyed on either needs its ranges re-ingested rather than topped up.
Sinks must also widen any unique constraint before ingesting: rows that
previously collided now arrive as distinct rows.

## Upgrading to v0.2.0

Three fields that this library declared but never populated now carry data,
and one index changes meaning. Read this before bumping.

### `operations`: the call graph and auth credentials are populated

`OperationData.ContractCallsJSON`, `.ContractsInvolved` and `.MaxCallDepth`
existed since the type was introduced and were assigned nowhere —
`integrateCallGraph` was a no-op stub. Every consumer read nil, with no way
to tell that apart from a ledger with no cross-contract calls.

They are now extracted from diagnostic events and the authorization tree.
A consumer that treated nil as "feature absent" will start seeing values.
`ContractsInvolved` is sorted, so the column is reproducible across runs.

`SorobanAuthCredentialsTypes` and `SorobanAuthAddresses` are new: parallel
arrays describing each authorization entry's credentials discriminant and
strkey authorizer. Both are nil when an operation carries no auth entries,
which keeps "no authorization" distinct from "an authorization with no
authorizer". Both consuming ingesters had implemented this separately; it
belongs here.

### `accounts`: `SequenceLedger` and `SequenceTime` added

`AccountData` now carries the CAP-21 sequence bump ledger and time from
`AccountEntryExtensionV3`. Both read zero on accounts that predate the
extension. A consumer persisting these needs columns for them; one that was
writing zeros into existing columns will start writing real values.

### Row order is now deterministic

Thirteen extractors deduplicated rows through a map and then ranged over it
to build their output slice. Go randomizes map iteration, so each emitted the
same rows in a different sequence on every run over one ledger. Rows are now
ordered by the dedup key — the identity each extractor had already chosen.

Nothing downstream observed this: golden suites hash rows with `ORDER BY` and
the cross-ingester parity harness compares multisets, so the randomization
stayed invisible while still making Parquet output differ byte-for-byte
between two extractions of the same ledger. If you content-address or
deduplicate extraction output, this is the change that makes that work.

### `trades`: pool trades are emitted, and `TradeIndex` is positional

**This changes existing rows, not just new ones.**

Liquidity-pool matches were skipped outright, so no AMM trade has ever been
recorded. They are now emitted with `TradeType` `"liquidity_pool"`, an empty
`SellerAccount` — a pool has no counterparty account — and the new
`LiquidityPoolID` holding the hex pool hash. On one three-ledger pubnet
fixture this recovers 135 trades against 219 previously reported: roughly
38% of that ledger range's trades were missing.

`TradeIndex` is now the claim atom's **position within the operation's claim
list**, where it used to be a running count of emitted rows. The two differ
wherever a claim is skipped. This makes
`(transaction_hash, operation_index, trade_index)` a stable handle on one
claim atom in the XDR — without it, starting to emit pool trades would have
silently renumbered every orderbook trade that followed one in the same
operation, repointing any consumer keyed on that triple.

Backfill note: both the index change and the new rows apply retroactively, so
`trades` written by an earlier version is not comparable row-for-row with
output from this one.

## File layout

```
types_core.go        LedgerRowData, TransactionData, OperationData, EffectData, TradeData
types_accounts.go    AccountData, TrustlineData, AccountSignerData, NativeBalanceData, OfferData
types_soroban.go     ContractEventData, ContractDataData, ContractCodeData, ContractCreationData, WASMMetadata
types_state.go       ClaimableBalanceData, LiquidityPoolData, ConfigSettingData, TTLData, EvictedKeyData, RestoredKeyData
types_tokens.go      TokenTransferData

extract.go           LedgerInput, LedgerData, NewLedgerInput, NewLedgerInputFromXDR, ExtractAll
view_input.go        borrowed LedgerViewInput, ExtractAllFromXDR, ExtractAllView
ledgers.go           ExtractLedgers
transactions.go      ExtractTransactions + helpers
operations.go        ExtractOperations
effects.go           ExtractEffects (50+ effect types)
trades.go            ExtractTrades
call_graph.go        cross-contract call graph for OperationData
accounts.go          ExtractAccounts, ExtractTrustlines, ExtractAccountSigners, ExtractNativeBalances
soroban.go           ExtractContractEvents, ExtractContractData, ExtractContractCode, ExtractContractCreations, ExtractRestoredKeys
scval_converter.go   ConvertScValToJSON
token_transfers.go   ExtractTokenTransfers
evicted_keys.go      ExtractEvictedKeys
```

## stellar-etl parity

Based on comparison with [stellar-etl](https://github.com/stellar/stellar-etl) (SDF's official ETL pipeline for BigQuery), the following features have been adopted:

- **`ContractEventXDR`** — base64-encoded XDR of the full `ContractEvent`, enabling reprocessing without re-reading the archive
- **`Successful`** — whether the parent transaction succeeded (broader than `InSuccessfulContractCall` which only tracks the contract call context)
- **C-address contract IDs** — contract IDs encoded via `strkey.Encode(strkey.VersionByteContract, ...)` producing `C...` addresses matching block explorers, instead of raw hex

## Design principles

- **Extraction only.** The library converts `xdr.LedgerCloseMeta` into typed Go structs. It doesn't know about Parquet, PostgreSQL, gRPC, protobuf, or any output format. Callers own serialization.
- **Explicit input representations.** Stable parsed extractors take `*LedgerInput`; migrated zero-copy extractors take borrowed `*LedgerViewInput`. The boundary prevents accidental mixed lifetimes.
- **Concurrent by default.** `ExtractAll` runs every table extractor in goroutines. Individual extractors are also safe to call concurrently.
- **Single SDK pin.** All extraction logic uses one version of `go-stellar-sdk`. When the SDK is upgraded, every consumer gets the fix.
- **Protocol changes must fail loudly.** Every extractor switches on XDR union discriminants, and Go does not warn when a protocol upgrade adds an arm an existing switch ignores — the build stays green and the columns go quietly wrong. `protocol_coverage_test.go` enumerates the discriminants the SDK declares valid and fails when one is unhandled.
