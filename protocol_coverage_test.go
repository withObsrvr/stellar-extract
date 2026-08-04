package extract

import (
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// The tests in this file are exhaustiveness guards, not behaviour tests. Every
// extractor in this library switches on XDR union discriminants, and Go does
// not warn when a protocol upgrade adds an arm that an existing switch does not
// handle -- the code keeps compiling and starts writing wrong or empty columns.
// Protocol 28 (go-stellar-sdk v0.7.0) added three such arms at once:
// SCV_EXECUTABLE_TAG, CONTRACT_EXECUTABLE_EXTERNAL_REF, and
// STELLAR_VALUE_EMPTY_TX_SET. These tests enumerate the discriminants the SDK
// declares valid and fail when one is unhandled, so the next upgrade surfaces
// the gap in CI instead of in the bronze tables.

// Discriminant search window. XDR enums are int32, so no bounded scan is
// exhaustive in principle -- this is a search window, not a proof. The bounds
// are taken from what the SDK actually declares: across every enum in
// xdr_generated.go the discriminants span [-18, 2000]. Three conventions put
// values outside a naive 0..255 range:
//
//   - the result-code enums run negative (transactionResultCode reaches -18),
//   - CryptoKeyType places KEY_TYPE_MUXED_ED25519 at 0x100 (256), which
//     transactions.go switches on in getMuxedAddress,
//   - SCSpecType uses a 1000-2000 band.
//
// The window below clears the observed range by ~32x above and ~3600x below.
// TestDiscriminantScanWindowHasHeadroom fails if a guarded enum creeps toward
// an edge, which is the signal to widen it.
const (
	discriminantScanMin = -65536
	discriminantScanMax = 65536
)

// validDiscriminants returns the discriminants the SDK accepts for an enum
// within [discriminantScanMin, discriminantScanMax], discovered through the
// generated ValidEnum method. The generated per-enum maps are unexported, so
// probing is the only way to enumerate them from outside the xdr package.
func validDiscriminants(e interface{ ValidEnum(int32) bool }) []int32 {
	var valid []int32
	for v := discriminantScanMin; v <= discriminantScanMax; v++ {
		if e.ValidEnum(int32(v)) {
			valid = append(valid, int32(v))
		}
	}
	return valid
}

// TestDiscriminantScanWindowHasHeadroom keeps the search window above honest.
// CryptoKeyType is included even though no guard below uses it: its
// KEY_TYPE_MUXED_ED25519 = 0x100 is the concrete reason the window cannot stop
// at 255, and asserting it here pins that reason to a failing test rather than
// a comment.
func TestDiscriminantScanWindowHasHeadroom(t *testing.T) {
	guarded := map[string]interface{ ValidEnum(int32) bool }{
		"ScValType":              xdr.ScValType(0),
		"ContractExecutableType": xdr.ContractExecutableType(0),
		"StellarValueType":       xdr.StellarValueType(0),
		"CryptoKeyType":          xdr.CryptoKeyType(0),
	}

	const margin = 1024
	for name, e := range guarded {
		discs := validDiscriminants(e)
		if len(discs) == 0 {
			t.Errorf("no valid %s discriminants found in [%d, %d]; the scan window "+
				"no longer covers this enum", name, discriminantScanMin, discriminantScanMax)
			continue
		}
		lo, hi := discs[0], discs[len(discs)-1]
		if int(lo) < discriminantScanMin+margin || int(hi) > discriminantScanMax-margin {
			t.Errorf("%s discriminants span [%d, %d], within %d of the scan window "+
				"[%d, %d]; widen the window or the exhaustiveness guards below may "+
				"miss an arm", name, lo, hi, margin, discriminantScanMin, discriminantScanMax)
		}
	}
}

func TestConvertScValToJSONHandlesEveryScValType(t *testing.T) {
	discs := validDiscriminants(xdr.ScValType(0))
	if len(discs) == 0 {
		t.Fatal("no valid ScValType discriminants discovered")
	}

	for _, disc := range discs {
		scValType := xdr.ScValType(disc)
		// A zero-valued ScVal leaves every arm pointer nil, so a handled type
		// returns either a value or its own "has nil value" error. Only an
		// unhandled type reaches the default branch.
		_, err := ConvertScValToJSON(xdr.ScVal{Type: scValType})
		if err != nil && strings.Contains(err.Error(), "unsupported ScVal type") {
			t.Errorf("ConvertScValToJSON has no case for %s (discriminant %d); "+
				"a protocol upgrade added this arm and contract event topics/data "+
				"of this type will be written as error objects", scValType, disc)
		}
	}
}

func TestApplyExecutableHandlesEveryContractExecutableType(t *testing.T) {
	var wasmHash xdr.Hash
	owner := xdr.MustAddress("GBCW77HZVZAXWTFWY2BM2DPVDARDJUD3JRXAA2JF6WRY3OH3IDFOF5GR")

	discs := validDiscriminants(xdr.ContractExecutableType(0))
	if len(discs) == 0 {
		t.Fatal("no valid ContractExecutableType discriminants discovered")
	}

	// A union arm must be populated to be readable -- the generated Must*
	// accessors dereference the arm pointer -- so each known type gets a valid
	// payload here. A discriminant added by a future protocol hits the default
	// case, which is the signal to update applyExecutable alongside this test.
	for _, disc := range discs {
		execType := xdr.ContractExecutableType(disc)
		exec := xdr.ContractExecutable{Type: execType}
		var want string

		switch execType {
		case xdr.ContractExecutableTypeContractExecutableWasm:
			exec.WasmHash = &wasmHash
			want = "wasm"
		case xdr.ContractExecutableTypeContractExecutableStellarAsset:
			want = "stellar_asset"
		case xdr.ContractExecutableTypeContractExecutableExternalRef:
			exec.ExternalRef = &xdr.ContractExecutableExternalRef{
				ExecutableOwner: xdr.ScAddress{
					Type:      xdr.ScAddressTypeScAddressTypeAccount,
					AccountId: &owner,
				},
				Tag: xdr.ScString("tag"),
			}
			want = "external_ref"
		default:
			t.Errorf("ContractExecutableType %s (discriminant %d) is unknown to this "+
				"test and to applyExecutable; contract_creations rows cannot "+
				"distinguish this executable kind until both are updated", execType, disc)
			continue
		}

		var creation ContractCreationData
		applyExecutable(&creation, exec)
		if creation.ExecutableType != want {
			t.Errorf("applyExecutable(%s) set ExecutableType = %q, want %q",
				execType, creation.ExecutableType, want)
		}
	}
}

func TestScpValueSignatureHandlesEveryStellarValueType(t *testing.T) {
	nodeID := xdr.MustAddress("GBCW77HZVZAXWTFWY2BM2DPVDARDJUD3JRXAA2JF6WRY3OH3IDFOF5GR")
	sig := xdr.LedgerCloseValueSignature{
		NodeId:    xdr.NodeId(nodeID),
		Signature: []byte("signature-bytes"),
	}

	// Every arm that carries a LedgerCloseValueSignature must be read. Arms
	// added by a future protocol land in the default case below and fail.
	for _, disc := range validDiscriminants(xdr.StellarValueType(0)) {
		valueType := xdr.StellarValueType(disc)
		sv := xdr.StellarValue{Ext: xdr.StellarValueExt{V: valueType}}

		var wantSig bool
		switch valueType {
		case xdr.StellarValueTypeStellarValueBasic:
			// Carries no signature.
		case xdr.StellarValueTypeStellarValueSigned:
			sv.Ext.LcValueSignature = &sig
			wantSig = true
		case xdr.StellarValueTypeStellarValueEmptyTxSet:
			sv.Ext.ProposedValue = &xdr.StellarValueProposedValue{LcValueSignature: sig}
			wantSig = true
		default:
			t.Errorf("StellarValueType %s (discriminant %d) is unknown to this test "+
				"and to ledgers.go; check whether it carries a "+
				"LedgerCloseValueSignature and update scpValueSignature", valueType, disc)
			continue
		}

		got := scpValueSignature(sv)
		if wantSig && got == nil {
			t.Errorf("scpValueSignature(%s) = nil, want the ledger close value "+
				"signature; node_id and signature will be NULL on these ledgers", valueType)
		}
		if !wantSig && got != nil {
			t.Errorf("scpValueSignature(%s) = %+v, want nil", valueType, *got)
		}
	}
}

// TestSorobanMetaExtHandlesEveryMetaVersion guards the same failure mode a
// protocol version earlier: Protocol 23 moved Soroban meta from TransactionMeta
// V3 to V4, and the V3-only read silently left rent_fee_charged NULL on every
// ledger after the upgrade. A V5 arm would do the same.
func TestSorobanMetaExtHandlesEveryMetaVersion(t *testing.T) {
	ext := xdr.SorobanTransactionMetaExt{
		V:  1,
		V1: &xdr.SorobanTransactionMetaExtV1{RentFeeCharged: 4321},
	}

	var probe xdr.TransactionMeta
	for disc := int32(0); disc < 32; disc++ {
		if _, ok := probe.ArmForSwitch(disc); !ok {
			continue
		}

		meta := xdr.TransactionMeta{V: disc}
		var wantExt bool

		switch disc {
		case 0:
			meta.Operations = &[]xdr.OperationMeta{}
		case 1:
			meta.V1 = &xdr.TransactionMetaV1{}
		case 2:
			meta.V2 = &xdr.TransactionMetaV2{}
		case 3:
			meta.V3 = &xdr.TransactionMetaV3{
				SorobanMeta: &xdr.SorobanTransactionMeta{Ext: ext},
			}
			wantExt = true
		case 4:
			meta.V4 = &xdr.TransactionMetaV4{
				SorobanMeta: &xdr.SorobanTransactionMetaV2{Ext: ext},
			}
			wantExt = true
		default:
			t.Errorf("TransactionMeta version %d is unknown to this test and to "+
				"sorobanMetaExt; if it can carry Soroban meta, rent_fee_charged "+
				"will be NULL on every ledger using it", disc)
			continue
		}

		got := sorobanMetaExt(meta)
		if wantExt && (got == nil || got.V1 == nil || got.V1.RentFeeCharged != 4321) {
			t.Errorf("sorobanMetaExt(meta V%d) = %v, want the Soroban meta extension", disc, got)
		}
		if !wantExt && got != nil {
			t.Errorf("sorobanMetaExt(meta V%d) = %v, want nil", disc, got)
		}
	}
}

func TestConvertScValToJSONExecutableTag(t *testing.T) {
	tag := xdr.ScString("my-executable")
	got, err := ConvertScValToJSON(xdr.ScVal{
		Type:          xdr.ScValTypeScvExecutableTag,
		ExecutableTag: &tag,
	})
	if err != nil {
		t.Fatalf("ConvertScValToJSON returned error: %v", err)
	}
	m, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("ConvertScValToJSON returned %T, want map[string]interface{}", got)
	}
	if m["type"] != "executable_tag" || m["value"] != "my-executable" {
		t.Errorf("ConvertScValToJSON = %v, want type=executable_tag value=my-executable", m)
	}
}

func TestApplyExecutableExternalRef(t *testing.T) {
	owner := xdr.MustAddress("GBCW77HZVZAXWTFWY2BM2DPVDARDJUD3JRXAA2JF6WRY3OH3IDFOF5GR")
	ownerAddr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &owner}

	var creation ContractCreationData
	applyExecutable(&creation, xdr.ContractExecutable{
		Type: xdr.ContractExecutableTypeContractExecutableExternalRef,
		ExternalRef: &xdr.ContractExecutableExternalRef{
			ExecutableOwner: ownerAddr,
			Tag:             xdr.ScString("v1"),
		},
	})

	if creation.ExecutableType != "external_ref" {
		t.Errorf("ExecutableType = %q, want external_ref", creation.ExecutableType)
	}
	if creation.WasmHash != nil {
		t.Errorf("WasmHash = %v, want nil for an external-ref executable", *creation.WasmHash)
	}
	if creation.ExternalRefOwner == nil || *creation.ExternalRefOwner != owner.Address() {
		t.Errorf("ExternalRefOwner = %v, want %s", creation.ExternalRefOwner, owner.Address())
	}
	if creation.ExternalRefTag == nil || *creation.ExternalRefTag != "v1" {
		t.Errorf("ExternalRefTag = %v, want v1", creation.ExternalRefTag)
	}
}

func TestApplyExecutableWasm(t *testing.T) {
	var hash xdr.Hash
	hash[0], hash[31] = 0xab, 0xcd

	var creation ContractCreationData
	applyExecutable(&creation, xdr.ContractExecutable{
		Type:     xdr.ContractExecutableTypeContractExecutableWasm,
		WasmHash: &hash,
	})

	if creation.ExecutableType != "wasm" {
		t.Errorf("ExecutableType = %q, want wasm", creation.ExecutableType)
	}
	if creation.WasmHash == nil || !strings.HasPrefix(*creation.WasmHash, "ab") {
		t.Errorf("WasmHash = %v, want hex encoding starting with ab", creation.WasmHash)
	}
}
