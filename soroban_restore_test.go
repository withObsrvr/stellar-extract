package extract

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// The footprint-based reader this replaced could only ever record "unknown".
// Durability now comes off the entry, and the distinction matters: a temporary
// entry is deleted when it expires and can never be restored, so a restored
// entry reported as temporary would describe something that cannot happen.
func TestRestoredEntryDurabilityComesFromTheEntry(t *testing.T) {
	contractDataKey := func(d xdr.ContractDataDurability) xdr.LedgerKey {
		return xdr.LedgerKey{
			Type: xdr.LedgerEntryTypeContractData,
			ContractData: &xdr.LedgerKeyContractData{
				Durability: d,
			},
		}
	}

	for _, tc := range []struct {
		name string
		key  xdr.LedgerKey
		want string
	}{
		{
			name: "persistent contract data",
			key:  contractDataKey(xdr.ContractDataDurabilityPersistent),
			want: "persistent",
		},
		{
			name: "temporary contract data",
			key:  contractDataKey(xdr.ContractDataDurabilityTemporary),
			want: "temporary",
		},
		{
			// Code is never temporary, so a restoration of it is persistent
			// by construction rather than by inspection.
			name: "contract code",
			key:  xdr.LedgerKey{Type: xdr.LedgerEntryTypeContractCode},
			want: "persistent",
		},
		{
			// TTL entries accompany a restoration of the entry they govern.
			name: "ttl",
			key:  xdr.LedgerKey{Type: xdr.LedgerEntryTypeTtl},
			want: "persistent",
		},
		{
			// Classic entries are not archived at all; claiming a durability
			// for one would be inventing a fact.
			name: "account",
			key:  xdr.LedgerKey{Type: xdr.LedgerEntryTypeAccount},
			want: "unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := restoredEntryDurability(tc.key); got != tc.want {
				t.Errorf("restoredEntryDurability = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestContractDataKeyWithoutBodyIsUnknownNotAPanic(t *testing.T) {
	key := xdr.LedgerKey{Type: xdr.LedgerEntryTypeContractData}
	if got := restoredEntryDurability(key); got != "unknown" {
		t.Errorf("restoredEntryDurability = %q, want %q", got, "unknown")
	}
}

// A contract error reports a code only when the contract defined one. A host
// error carries no number, and reporting zero would read as error code 0.
func TestOnlyContractErrorsCarryACode(t *testing.T) {
	contractCode := xdr.Uint32(42)
	errType, code, ok := scErrorFacts(xdr.ScError{
		Type:         xdr.ScErrorTypeSceContract,
		ContractCode: &contractCode,
	})
	if !ok || errType != "contract" {
		t.Fatalf("got (%q, ok=%v), want (\"contract\", true)", errType, ok)
	}
	if code == nil || *code != 42 {
		t.Fatalf("code = %v, want 42", code)
	}

	errType, code, ok = scErrorFacts(xdr.ScError{Type: xdr.ScErrorTypeSceBudget})
	if !ok || errType != "budget" {
		t.Fatalf("got (%q, ok=%v), want (\"budget\", true)", errType, ok)
	}
	if code != nil {
		t.Errorf("code = %d, want nil — a host error has no contract-defined code", *code)
	}
}

// An unrecognised type keeps its numeric identity rather than being folded
// into an existing bucket.
func TestUnknownErrorTypeIsNotMislabelled(t *testing.T) {
	if got := canonicalScErrorType(xdr.ScErrorType(9999)); got != "unknown_9999" {
		t.Errorf("canonicalScErrorType = %q, want %q", got, "unknown_9999")
	}
}
