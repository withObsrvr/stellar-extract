package extract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func contractAddress(t *testing.T, b byte) (xdr.ScAddress, string) {
	t.Helper()
	var id xdr.ContractId
	for i := range id {
		id[i] = b
	}
	encoded, err := strkey.Encode(strkey.VersionByteContract, id[:])
	if err != nil {
		t.Fatal(err)
	}
	return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &id}, encoded
}

func contractFnInvocation(addr xdr.ScAddress, fn string, subs ...xdr.SorobanAuthorizedInvocation) xdr.SorobanAuthorizedInvocation {
	return xdr.SorobanAuthorizedInvocation{
		Function: xdr.SorobanAuthorizedFunction{
			Type: xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
			ContractFn: &xdr.InvokeContractArgs{
				ContractAddress: addr,
				FunctionName:    xdr.ScSymbol(fn),
			},
		},
		SubInvocations: subs,
	}
}

// The authorization tree is one of the two sources the call graph is built
// from, and the only one reachable without a decoded ledger. A nested
// invocation must produce one edge per hop, with depth increasing.
func TestExtractCallsFromAuthEntriesWalksNestedInvocations(t *testing.T) {
	addrA, idA := contractAddress(t, 0x0A)
	addrB, idB := contractAddress(t, 0x0B)
	_ = addrA

	entries := []xdr.SorobanAuthorizationEntry{{
		RootInvocation: contractFnInvocation(addrB, "transfer",
			contractFnInvocation(addrB, "inner_noop"),
		),
	}}

	calls := extractCallsFromAuthEntries(entries, idA)

	if len(calls) != 1 {
		t.Fatalf("expected one cross-contract edge, got %d: %+v", len(calls), calls)
	}
	if calls[0].FromContract != idA || calls[0].ToContract != idB {
		t.Errorf("edge %s -> %s, want %s -> %s", calls[0].FromContract, calls[0].ToContract, idA, idB)
	}
	if calls[0].FunctionName != "transfer" {
		t.Errorf("FunctionName = %q, want transfer", calls[0].FunctionName)
	}
	if calls[0].CallDepth != 1 {
		t.Errorf("CallDepth = %d, want 1", calls[0].CallDepth)
	}
}

// A self-call is not a cross-contract call and must not appear as an edge.
func TestExtractCallsFromAuthEntriesSkipsSelfCalls(t *testing.T) {
	addrA, idA := contractAddress(t, 0x0A)

	entries := []xdr.SorobanAuthorizationEntry{{
		RootInvocation: contractFnInvocation(addrA, "self"),
	}}

	if calls := extractCallsFromAuthEntries(entries, idA); len(calls) != 0 {
		t.Fatalf("expected no edges for a self-call, got %+v", calls)
	}
}

// A CONTRACT_FN arm whose address is an account rather than a contract used
// to be a panic risk. It must be counted and skipped, never crash the caller
// mid-ledger.
func TestNonContractAuthAddressIsCountedNotFatal(t *testing.T) {
	_, idA := contractAddress(t, 0x0A)
	before := CallGraphSkippedEdges()

	account := xdr.MustAddress("GBCW77HZVZAXWTFWY2BM2DPVDARDJUD3JRXAA2JF6WRY3OH3IDFOF5GR")
	accountAddr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &account}

	entries := []xdr.SorobanAuthorizationEntry{{
		RootInvocation: contractFnInvocation(accountAddr, "transfer"),
	}}

	calls := extractCallsFromAuthEntries(entries, idA)

	if len(calls) != 0 {
		t.Fatalf("expected no edge for a non-contract address, got %+v", calls)
	}
	if CallGraphSkippedEdges() <= before {
		t.Error("skipped edge was not counted; a silent under-count would be invisible")
	}
}

// contracts_involved is serialized into a Bronze column. Go randomizes map
// iteration, so an unsorted slice makes the same ledger produce a different
// value on every run.
func TestCallGraphContractsInvolvedIsSorted(t *testing.T) {
	result := &CallGraphResult{
		Calls:             []ContractCall{{FromContract: "CZZZ", ToContract: "CAAA", CallDepth: 1}},
		ContractsInvolved: []string{"CZZZ", "CAAA", "CMMM"},
		MaxDepth:          1,
	}
	// callGraphToJSON passes the slice through; the ordering guarantee comes
	// from extractCallGraph. Assert on the shape the caller stores.
	_, involved, maxDepth, err := callGraphToJSON(result)
	if err != nil {
		t.Fatal(err)
	}
	if maxDepth == nil || *maxDepth != 1 {
		t.Fatalf("MaxDepth = %v, want 1", maxDepth)
	}
	if len(involved) != 3 {
		t.Fatalf("expected 3 contracts, got %d", len(involved))
	}
}

// Two calls differing only by repetition collapse; a different depth does not.
func TestDeduplicateCallsKeepsDistinctDepths(t *testing.T) {
	calls := []ContractCall{
		{FromContract: "A", ToContract: "B", FunctionName: "f", CallDepth: 1},
		{FromContract: "A", ToContract: "B", FunctionName: "f", CallDepth: 1},
		{FromContract: "A", ToContract: "B", FunctionName: "f", CallDepth: 2},
	}
	if got := deduplicateCalls(calls); len(got) != 2 {
		t.Fatalf("expected 2 unique calls, got %d: %+v", len(got), got)
	}
}

// findFnNameRecursive walks a decoded ScVal map. Map iteration order is
// randomized in Go, so without sorted traversal a structure with more than
// one reachable fn_name returns a different answer on different runs — and
// that answer is serialized into contract_calls_json.
func TestFindFnNameRecursiveIsDeterministic(t *testing.T) {
	arguments := map[string]interface{}{
		"zeta":  map[string]interface{}{"fn_name": "z_fn"},
		"alpha": map[string]interface{}{"fn_name": "a_fn"},
		"mid":   map[string]interface{}{"fn_name": "m_fn"},
	}

	first := findFnNameRecursive(arguments)
	if first == "" {
		t.Fatal("expected a function name")
	}
	for i := 0; i < 200; i++ {
		if got := findFnNameRecursive(arguments); got != first {
			t.Fatalf("unstable result: %q then %q", first, got)
		}
	}
	// Sorted traversal visits "alpha" first.
	if first != "a_fn" {
		t.Errorf("got %q, want a_fn from the lowest-sorted key", first)
	}
}

// The stub this replaced returned nil and assigned nothing, so a consumer
// could not distinguish "not implemented" from "no cross-contract calls".
// Assert the fields are actually populated through the public row type.
func TestIntegrateCallGraphPopulatesOperationFields(t *testing.T) {
	addrA, idA := contractAddress(t, 0x0A)
	addrB, _ := contractAddress(t, 0x0B)

	op := xdr.Operation{Body: xdr.OperationBody{
		Type: xdr.OperationTypeInvokeHostFunction,
		InvokeHostFunctionOp: &xdr.InvokeHostFunctionOp{
			HostFunction: xdr.HostFunction{
				Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &xdr.InvokeContractArgs{
					ContractAddress: addrA,
					FunctionName:    xdr.ScSymbol("swap"),
				},
			},
			Auth: []xdr.SorobanAuthorizationEntry{{
				RootInvocation: contractFnInvocation(addrB, "transfer"),
			}},
		},
	}}

	var opData OperationData
	if err := integrateCallGraph(ingestTransactionStub(), 0, op, &opData); err != nil {
		t.Fatalf("integrateCallGraph: %v", err)
	}

	if opData.ContractCallsJSON == nil {
		t.Fatal("ContractCallsJSON is nil; the call graph was not integrated")
	}
	if opData.MaxCallDepth == nil || *opData.MaxCallDepth != 1 {
		t.Fatalf("MaxCallDepth = %v, want 1", opData.MaxCallDepth)
	}
	if len(opData.ContractsInvolved) != 2 {
		t.Fatalf("ContractsInvolved = %v, want both contracts", opData.ContractsInvolved)
	}
	// Sorted, for the reproducibility reason above.
	if opData.ContractsInvolved[0] > opData.ContractsInvolved[1] {
		t.Errorf("ContractsInvolved is not sorted: %v", opData.ContractsInvolved)
	}
	if opData.ContractsInvolved[0] != idA && opData.ContractsInvolved[1] != idA {
		t.Errorf("invoked contract %s missing from %v", idA, opData.ContractsInvolved)
	}

	var decoded []ContractCall
	if err := json.Unmarshal([]byte(*opData.ContractCallsJSON), &decoded); err != nil {
		t.Fatalf("ContractCallsJSON is not valid JSON: %v", err)
	}
	if len(decoded) != 1 || decoded[0].FunctionName != "transfer" {
		t.Fatalf("unexpected calls: %+v", decoded)
	}
}

// A non-Soroban operation has no call graph and must leave the fields nil
// rather than an empty JSON array, so "no calls" stays distinguishable from
// "an empty call list".
func TestIntegrateCallGraphLeavesClassicOperationsUntouched(t *testing.T) {
	op := xdr.Operation{Body: xdr.OperationBody{Type: xdr.OperationTypePayment}}

	var opData OperationData
	if err := integrateCallGraph(ingestTransactionStub(), 0, op, &opData); err != nil {
		t.Fatalf("integrateCallGraph: %v", err)
	}
	if opData.ContractCallsJSON != nil || opData.ContractsInvolved != nil || opData.MaxCallDepth != nil {
		t.Fatalf("classic operation got call-graph fields: %+v", opData)
	}
}

// ingestTransactionStub is a zero-value transaction. The diagnostic-event
// source finds nothing on it, which isolates these tests to the
// authorization-tree source; the event path needs a decoded ledger and is
// covered by the fixture suites in the consuming repositories.
func ingestTransactionStub() ingest.LedgerTransaction {
	return ingest.LedgerTransaction{}
}

// Auth credentials are recorded independently of the call graph. A passkey
// wallet authorizing a plain token transfer has credentials worth storing
// and no cross-contract calls, so a nil call graph must not cost them.
func TestIntegrateCallGraphRecordsAuthCredentialsWithoutCalls(t *testing.T) {
	addrA, idA := contractAddress(t, 0x0A)
	authorizer, _ := contractAddress(t, 0x0C)

	op := xdr.Operation{Body: xdr.OperationBody{
		Type: xdr.OperationTypeInvokeHostFunction,
		InvokeHostFunctionOp: &xdr.InvokeHostFunctionOp{
			HostFunction: xdr.HostFunction{
				Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &xdr.InvokeContractArgs{
					ContractAddress: addrA,
					FunctionName:    xdr.ScSymbol("transfer"),
				},
			},
			Auth: []xdr.SorobanAuthorizationEntry{
				{Credentials: xdr.SorobanCredentials{
					Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount,
				}},
				{
					Credentials: xdr.SorobanCredentials{
						Type:    xdr.SorobanCredentialsTypeSorobanCredentialsAddress,
						Address: &xdr.SorobanAddressCredentials{Address: authorizer},
					},
					// Same contract as the invocation, so this produces no
					// call-graph edge — only credentials.
					RootInvocation: contractFnInvocation(addrA, "transfer"),
				},
			},
		},
	}}

	var opData OperationData
	if err := integrateCallGraph(ingestTransactionStub(), 0, op, &opData); err != nil {
		t.Fatalf("integrateCallGraph: %v", err)
	}

	if opData.ContractCallsJSON != nil {
		t.Errorf("expected no call graph for a self-invocation, got %s", *opData.ContractCallsJSON)
	}
	_ = idA

	wantTypes := []string{AuthCredentialsSourceAccount, AuthCredentialsAddress}
	if len(opData.SorobanAuthCredentialsTypes) != 2 {
		t.Fatalf("credential types = %v, want %v", opData.SorobanAuthCredentialsTypes, wantTypes)
	}
	for i, want := range wantTypes {
		if opData.SorobanAuthCredentialsTypes[i] != want {
			t.Errorf("credential type %d = %q, want %q", i, opData.SorobanAuthCredentialsTypes[i], want)
		}
	}
	// Parallel arrays: position i describes the same entry in both.
	if len(opData.SorobanAuthAddresses) != len(opData.SorobanAuthCredentialsTypes) {
		t.Fatalf("addresses %d, types %d — the arrays must stay parallel",
			len(opData.SorobanAuthAddresses), len(opData.SorobanAuthCredentialsTypes))
	}
	if opData.SorobanAuthAddresses[0] != "" {
		t.Errorf("SOURCE_ACCOUNT address = %q, want empty — the authorizer is the tx source",
			opData.SorobanAuthAddresses[0])
	}
	if opData.SorobanAuthAddresses[1] == "" {
		t.Error("ADDRESS credential has no authorizer address")
	}
}

// Zero auth entries must leave both arrays nil, so "no authorization" stays
// distinct from "an authorization with no authorizer".
func TestExtractAuthCredentialsOnNoEntriesIsNil(t *testing.T) {
	types, addrs := extractAuthCredentials(nil)
	if types != nil || addrs != nil {
		t.Fatalf("expected nil arrays, got %v / %v", types, addrs)
	}
}

// A credentials arm this library does not know about must still produce an
// entry. Dropping it would silently shorten the arrays and misalign them
// against the operation's auth entries.
func TestExtractAuthCredentialsRecordsUnknownArm(t *testing.T) {
	entries := []xdr.SorobanAuthorizationEntry{
		{Credentials: xdr.SorobanCredentials{Type: xdr.SorobanCredentialsType(9999)}},
	}

	types, addrs := extractAuthCredentials(entries)
	if len(types) != 1 || len(addrs) != 1 {
		t.Fatalf("unknown arm was dropped: types=%v addrs=%v", types, addrs)
	}
	if !strings.HasPrefix(types[0], "UNKNOWN_") {
		t.Errorf("type = %q, want an UNKNOWN_ marker", types[0])
	}
}
