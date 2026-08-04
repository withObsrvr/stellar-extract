# Upgrading `go-stellar-sdk`

This library turns XDR unions into bronze-layer columns. Almost every extractor
is a `switch` on a union discriminant, which makes SDK upgrades a specific kind
of risk:

> **A protocol upgrade adds a new union arm. Go does not warn about the
> `switch` that ignores it. The build passes, the tests pass, and a column
> starts being empty or wrong.**

This is not hypothetical. The v0.6.0 → v0.7.0 upgrade (Protocol 28) added three
arms at once, and every one of them landed in a `switch` this library owned:

| New arm | Our switch | Symptom had we shipped the bump alone |
|---|---|---|
| `SCV_EXECUTABLE_TAG` | `ConvertScValToJSON` | Contract event topics/data of this type written as `{"error": "unsupported ScVal type"}` |
| `CONTRACT_EXECUTABLE_EXTERNAL_REF` | contract creation extraction | `wasm_hash` NULL with nothing distinguishing it from a SAC deployment |
| `STELLAR_VALUE_EMPTY_TX_SET` | `scpValueSignature` | `node_id` and `signature` NULL on affected ledgers |

The SDK updated its *own* switches for these arms (`xdr/scval.go`'s `Equals`
and `String`). Nothing told us to update ours.

An earlier instance of the same failure went unnoticed for two protocols:
Protocol 23 moved Soroban meta from `TransactionMeta` V3 to V4, and the V3-only
read left `rent_fee_charged` NULL on every ledger after the upgrade.

## Checklist

Compiling is not evidence. Work through this instead.

**1. Read the changelog, not just the release title.**

```bash
V=v0.7.1
go mod download github.com/stellar/go-stellar-sdk@$V
M=$(go env GOMODCACHE)/github.com/stellar/go-stellar-sdk@$V
sed -n '1,60p' "$M/CHANGELOG.md"
diff "$(go env GOMODCACHE)/github.com/stellar/go-stellar-sdk@<current>/ingest/CHANGELOG.md" "$M/ingest/CHANGELOG.md"
```

**2. Diff the XDR definitions.** This is the highest-signal step; `.x` files are
short and show new arms directly.

```bash
OLD=$(go env GOMODCACHE)/github.com/stellar/go-stellar-sdk@<current>
for f in "$M"/xdr/*.x; do diff "$OLD/xdr/$(basename $f)" "$f"; done
```

**3. List new enum constants.** Each one is a `switch` somewhere in this repo
that may need a case.

```bash
diff "$OLD/xdr/xdr_generated.go" "$M/xdr/xdr_generated.go" \
  | grep '^>' | grep -oE '[A-Z][A-Za-z0-9]+ [A-Z][A-Za-z0-9]+ = [0-9]+' | sort -u
```

**4. Check whether the SDK updated its own switches.** If `xdr/scval.go`,
`xdr/ledger_close_meta_view.go`, or the `ingest` helpers gained a case for a new
arm, we almost certainly need the mirror of it.

```bash
diff "$OLD/xdr/scval.go" "$M/xdr/scval.go"
```

**5. Bump, then run the guards.**

```bash
go get github.com/stellar/go-stellar-sdk@$V && go mod tidy
go test ./...   # protocol_coverage_test.go fails on unhandled discriminants
```

**6. Extend the guards to cover any newly-discovered switch.** If step 3 turned
up an enum this repo switches on but `protocol_coverage_test.go` does not yet
enumerate, add it. The guard is only as good as its coverage.

**7. Validate against real ledgers.** The guards prove the switches are
exhaustive; they do not prove the extraction is correct. Run the upgraded
library over a recent pubnet ledger range and diff the row counts and NULL rates
per column against the previous pin. A column that goes from 0% NULL to 100%
NULL is the signature of this bug class.

## Writing extractors so this keeps working

- **Do not use `Get*()`/`Must*()` as a substitute for a `switch`.**
  `exec.GetWasmHash()` returning `ok == false` is indistinguishable from "this
  is a new arm I have never seen". Switch on the discriminant and give the
  default branch a visible outcome.
- **Record the discriminant, do not infer it.** `contract_creations` now carries
  `executable_type` rather than making consumers infer SAC-vs-WASM from a NULL
  `wasm_hash`. When a third kind arrived, the inference broke; the explicit
  column would not have.
- **Add the enum to `protocol_coverage_test.go`** whenever you write a new
  `switch` on an XDR union. The guards probe `ValidEnum` over a bounded window
  (`discriminantScanMin`/`discriminantScanMax`) because the generated per-enum
  maps are unexported. XDR discriminants are not all small positives — the
  result-code enums run to -18, `CryptoKeyType` puts `KEY_TYPE_MUXED_ED25519` at
  `0x100`, and `SCSpecType` uses a 1000-2000 band — so if you add an enum whose
  values sit outside the window, widen it. `TestDiscriminantScanWindowHasHeadroom`
  fails when a guarded enum approaches an edge.
- **Populated unions only in tests.** The generated `Must*` accessors dereference
  the arm pointer, so a zero-valued `xdr.ContractExecutable{Type: ...}` panics.
  Build a valid payload per arm.
