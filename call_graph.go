package extract

// Cross-contract call graph extraction.
//
// Ported from stellar-ingest's historical extractor, which is the only
// implementation that was reaching production output. Until this landed, the
// library declared OperationData.ContractCallsJSON, .ContractsInvolved and
// .MaxCallDepth and assigned none of them: integrateCallGraph was a no-op
// stub, so every consumer read nil and had no way to tell that from a ledger
// with no cross-contract calls.

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync/atomic"

	"github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// extractCallGraph extracts the cross-contract call graph from a transaction's operation
// This is the main entry point for call graph extraction, combining:
// 1. Diagnostic events (fn_call/fn_return patterns)
// 2. Authorization sub-invocations
// Returns a CallGraphResult with all calls, contracts involved, and max depth
func extractCallGraph(
	tx ingest.LedgerTransaction,
	opIndex int,
	op xdr.Operation,
) (*CallGraphResult, error) {
	// Only process InvokeHostFunction operations (type 24)
	invokeOp, ok := op.Body.GetInvokeHostFunctionOp()
	if !ok {
		return nil, nil
	}

	// Only process InvokeContract host function
	if invokeOp.HostFunction.Type != xdr.HostFunctionTypeHostFunctionTypeInvokeContract {
		return nil, nil
	}

	// Get the main contract being invoked
	if invokeOp.HostFunction.InvokeContract == nil {
		return nil, nil
	}

	invokeContract := invokeOp.HostFunction.InvokeContract
	mainContractID, err := invokeContract.ContractAddress.String()
	if err != nil {
		return nil, fmt.Errorf("failed to get main contract ID: %w", err)
	}

	var allCalls []ContractCall
	contractsSet := make(map[string]bool)
	contractsSet[mainContractID] = true

	// Source 1: Extract from diagnostic events (fn_call/fn_return patterns)
	diagnosticCalls := extractCrossContractCallsFromDiagnosticEvents(tx, opIndex, mainContractID)
	allCalls = append(allCalls, diagnosticCalls...)

	// Source 2: Extract from authorization sub-invocations
	authCalls := extractCallsFromAuthEntries(invokeOp.Auth, mainContractID)
	allCalls = append(allCalls, authCalls...)

	// Deduplicate calls (same from->to->function at same depth)
	allCalls = deduplicateCalls(allCalls)

	// Build contracts involved set and find max depth
	maxDepth := 0
	for _, call := range allCalls {
		contractsSet[call.FromContract] = true
		contractsSet[call.ToContract] = true
		if call.CallDepth > maxDepth {
			maxDepth = call.CallDepth
		}
	}

	// Sorted because Go randomises map iteration order, and this slice is
	// serialised into the operations.contracts_involved JSON array. Without
	// it the same ledger produces a different value on every run, which
	// makes Bronze output irreproducible and breaks any downstream
	// comparison or deduplication on the column.
	contractsInvolved := sortedSetKeys(contractsSet)

	// If no cross-contract calls, return nil (no call graph to store)
	if len(allCalls) == 0 {
		return nil, nil
	}

	return &CallGraphResult{
		Calls:             allCalls,
		ContractsInvolved: contractsInvolved,
		MaxDepth:          maxDepth,
	}, nil
}

// extractCrossContractCallsFromDiagnosticEvents extracts cross-contract calls from diagnostic events
// Uses fn_call/fn_return patterns to track the call stack
// Ported from cdp-pipeline-workflow/processor/processor_contract_invocation_v3.go lines 803-926
func extractCrossContractCallsFromDiagnosticEvents(
	tx ingest.LedgerTransaction,
	opIndex int,
	mainContractID string,
) []ContractCall {
	var calls []ContractCall

	// Get diagnostic events
	txEvents, err := tx.GetTransactionEvents()
	if err != nil {
		return calls
	}

	diagnosticEvents := txEvents.DiagnosticEvents
	if len(diagnosticEvents) == 0 {
		return calls
	}

	executionOrder := 0
	currentDepth := 0

	// Track call stack for determining "from" contract
	callStack := []string{mainContractID}

	for i, diagEvent := range diagnosticEvents {
		// Skip events with nil ContractId
		if diagEvent.Event.ContractId == nil {
			continue
		}

		// Get the contract that emitted this event
		eventContractID, err := strkey.Encode(strkey.VersionByteContract, diagEvent.Event.ContractId[:])
		if err != nil {
			continue
		}

		// Get first topic if available (to detect fn_call/fn_return)
		firstTopic := extractFirstTopic(diagEvent.Event)

		// Pattern 1: fn_call from main contract followed by event from different contract
		if firstTopic == "fn_call" && eventContractID == mainContractID && i+1 < len(diagnosticEvents) {
			nextEvent := diagnosticEvents[i+1]
			if nextEvent.Event.ContractId != nil {
				nextContractID, err := strkey.Encode(strkey.VersionByteContract, nextEvent.Event.ContractId[:])
				if err == nil && nextContractID != eventContractID {
					// This fn_call is calling another contract
					// Try to get function name from callee's fn_call event first (more accurate)
					var functionName string
					nextFirstTopic := extractFirstTopic(nextEvent.Event)
					if nextFirstTopic == "fn_call" {
						functionName = extractFunctionNameFromDiagnosticEvent(nextEvent.Event)
					}
					// Fallback to caller's event if callee's doesn't have it
					if functionName == "" || functionName == "unknown" {
						functionName = extractFunctionNameFromDiagnosticEvent(diagEvent.Event)
					}
					arguments := extractArgumentsFromDiagnosticEvent(diagEvent.Event)

					// Final fallback: extract fn_name from arguments (auth entries)
					if functionName == "" || functionName == "unknown" {
						if argFnName := extractFunctionNameFromArguments(arguments); argFnName != "" {
							functionName = argFnName
						}
					}

					call := ContractCall{
						FromContract:   eventContractID,
						ToContract:     nextContractID,
						FunctionName:   functionName,
						Arguments:      arguments,
						CallDepth:      currentDepth + 1,
						ExecutionOrder: executionOrder,
						Successful:     diagEvent.InSuccessfulContractCall,
					}

					calls = append(calls, call)
					executionOrder++
				}
			}
		}

		// Pattern 2: Detect cross-contract call when contract changes in call stack
		if firstTopic == "fn_call" && eventContractID != mainContractID {
			// A different contract is making a fn_call
			fromContract := mainContractID
			if len(callStack) > 0 {
				fromContract = callStack[len(callStack)-1]
			}

			// Only record if it's actually a different contract calling
			if fromContract != eventContractID {
				functionName := extractFunctionNameFromDiagnosticEvent(diagEvent.Event)
				arguments := extractArgumentsFromDiagnosticEvent(diagEvent.Event)

				// Fallback: extract fn_name from arguments (auth entries)
				if functionName == "" || functionName == "unknown" {
					if argFnName := extractFunctionNameFromArguments(arguments); argFnName != "" {
						functionName = argFnName
					}
				}

				call := ContractCall{
					FromContract:   fromContract,
					ToContract:     eventContractID,
					FunctionName:   functionName,
					Arguments:      arguments,
					CallDepth:      currentDepth + 1,
					ExecutionOrder: executionOrder,
					Successful:     diagEvent.InSuccessfulContractCall,
				}

				calls = append(calls, call)
				executionOrder++
			}
		}

		// Update call stack based on fn_call/fn_return
		if firstTopic == "fn_call" {
			// Push to call stack
			if eventContractID != "" && (len(callStack) == 0 || callStack[len(callStack)-1] != eventContractID) {
				callStack = append(callStack, eventContractID)
				currentDepth++
			}
		} else if firstTopic == "fn_return" && len(callStack) > 1 {
			// Pop from call stack
			callStack = callStack[:len(callStack)-1]
			if currentDepth > 0 {
				currentDepth--
			}
		}
	}

	return calls
}

// extractCallsFromAuthEntries extracts cross-contract calls from authorization entries
// Ported from cdp-pipeline-workflow/processor/processor_contract_invocation_v2.go
func extractCallsFromAuthEntries(
	authEntries []xdr.SorobanAuthorizationEntry,
	mainContractID string,
) []ContractCall {
	var calls []ContractCall
	executionOrder := 0

	for _, authEntry := range authEntries {
		authCalls := extractCallsFromAuthInvocation(
			&authEntry.RootInvocation,
			mainContractID,
			1,
			&executionOrder,
		)
		calls = append(calls, authCalls...)
	}

	return calls
}

// extractCallsFromAuthInvocation recursively extracts calls from auth invocations
func extractCallsFromAuthInvocation(
	invocation *xdr.SorobanAuthorizedInvocation,
	fromContract string,
	depth int,
	executionOrder *int,
) []ContractCall {
	var calls []ContractCall

	if invocation == nil || depth > 10 { // Max depth protection
		return calls
	}

	// First, capture this invocation's function call (the root or current node).
	// This was previously missing - we only captured sub-invocations.
	// Some auth payloads carry a CONTRACT_FN arm whose ContractAddress
	// is not a contract ScAddress (e.g. an account/muxed arm). The generated union
	// getter returns ok=false for non-contract arms, so we skip those cleanly
	// instead of dereferencing ContractAddress.ContractId directly and panicking
	// the caller.
	if invocation.Function.Type == xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn {
		contractFn := invocation.Function.ContractFn
		if contractFn != nil {
			toContractID, ok := scAddressContractStrKey(contractFn.ContractAddress)
			if ok && toContractID != fromContract {
				// Only record if it's a cross-contract call (different from/to)
				functionName := string(contractFn.FunctionName)
				call := ContractCall{
					FromContract:   fromContract,
					ToContract:     toContractID,
					FunctionName:   functionName,
					CallDepth:      depth,
					ExecutionOrder: *executionOrder,
					Successful:     true,
				}
				calls = append(calls, call)
				*executionOrder++

				// Update fromContract for sub-invocations
				fromContract = toContractID
			}
		}
	}

	// Process sub-invocations recursively
	// The recursive call will capture each sub-invocation's function via the code above
	for _, subInvocation := range invocation.SubInvocations {
		subCalls := extractCallsFromAuthInvocation(
			&subInvocation,
			fromContract, // Pass current contract as the caller
			depth+1,
			executionOrder,
		)
		calls = append(calls, subCalls...)
	}

	return calls
}

func scAddressContractStrKey(address xdr.ScAddress) (string, bool) {
	contractID, ok := address.GetContractId()
	if !ok {
		// Non-contract arm (e.g. account/muxed address). Counted but not logged
		// per-occurrence; the aggregate is surfaced at end of run so silent
		// call-graph under-counting stays observable without log spam.
		callGraphSkippedEdges.Add(1)
		return "", false
	}
	encoded, err := strkey.Encode(strkey.VersionByteContract, contractID[:])
	if err != nil {
		// A contract-arm address that fails strkey encoding is genuinely
		// unexpected; count it and log per-occurrence.
		callGraphSkippedEdges.Add(1)
		log.Printf("[call_graph] failed to strkey-encode contract address: %v", err)
		return "", false
	}
	return encoded, true
}

// callGraphSkippedEdges counts CONTRACT_FN auth entries skipped because their
// address was not an encodable contract address.
var callGraphSkippedEdges atomic.Int64

// CallGraphSkippedEdges returns the running skip count so callers can surface it
// at end of run and detect silent call-graph under-counting.
func CallGraphSkippedEdges() int64 { return callGraphSkippedEdges.Load() }

// extractFirstTopic extracts the first topic from a contract event as a string
func extractFirstTopic(event xdr.ContractEvent) string {
	if event.Body.V != 0 || event.Body.V0 == nil {
		return ""
	}

	topics := event.Body.V0.Topics
	if len(topics) == 0 {
		return ""
	}

	// Convert first topic to string
	decoded, err := ConvertScValToJSON(topics[0])
	if err != nil {
		return ""
	}

	// Return as string if it is one
	if str, ok := decoded.(string); ok {
		return str
	}

	return fmt.Sprintf("%v", decoded)
}

// extractFunctionNameFromDiagnosticEvent extracts function name from the second topic of a fn_call event
// For fn_call diagnostic events, topics are: [0] = "fn_call", [1] = function_name (symbol)
func extractFunctionNameFromDiagnosticEvent(event xdr.ContractEvent) string {
	if event.Body.V != 0 || event.Body.V0 == nil {
		return "unknown"
	}

	topics := event.Body.V0.Topics
	if len(topics) < 2 {
		return "unknown"
	}

	// Use the dedicated function that handles symbols, strings, and bytes
	name := GetFunctionNameFromScVal(topics[1])
	if name == "" {
		return "unknown"
	}
	return name
}

// extractArgumentsFromDiagnosticEvent extracts function arguments from the Data field of a fn_call event
// For fn_call diagnostic events, the Data field contains the function arguments as ScVal
func extractArgumentsFromDiagnosticEvent(event xdr.ContractEvent) interface{} {
	if event.Body.V != 0 || event.Body.V0 == nil {
		return nil
	}

	// Arguments are in the Data field
	decoded, err := ConvertScValToJSON(event.Body.V0.Data)
	if err != nil {
		return nil
	}

	return decoded
}

// extractFunctionNameFromArguments searches the arguments structure for fn_name
// This is a fallback when diagnostic event extraction fails
// The fn_name is typically found in authorization entries nested in the arguments
func extractFunctionNameFromArguments(arguments interface{}) string {
	if arguments == nil {
		return ""
	}

	// Recursively search for fn_name in the structure
	return findFnNameRecursive(arguments)
}

// findFnNameRecursive recursively searches a JSON-like structure for fn_name.
//
// Map keys are visited in sorted order. Go randomizes map iteration, so a
// structure containing more than one reachable fn_name would otherwise
// return a different answer on each run, and that answer is serialized into
// operations.contract_calls_json. This is the same class of bug as the
// unsorted contracts_involved slice above: an extractor whose output varies
// over identical input cannot be compared, deduplicated, or regression
// tested.
func findFnNameRecursive(v interface{}) string {
	switch val := v.(type) {
	case map[string]interface{}:
		// Check if this map has fn_name
		if fnName, ok := val["fn_name"]; ok {
			if str, ok := fnName.(string); ok && str != "" {
				return str
			}
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if result := findFnNameRecursive(val[k]); result != "" {
				return result
			}
		}
	case []interface{}:
		// Search through array elements
		for _, item := range val {
			if result := findFnNameRecursive(item); result != "" {
				return result
			}
		}
	}
	return ""
}

// deduplicateCalls removes duplicate calls (same from->to->function at same depth)
func deduplicateCalls(calls []ContractCall) []ContractCall {
	seen := make(map[string]bool)
	var unique []ContractCall

	for _, call := range calls {
		key := fmt.Sprintf("%s->%s:%s@%d", call.FromContract, call.ToContract, call.FunctionName, call.CallDepth)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, call)
		}
	}

	return unique
}

// callGraphToJSON converts a CallGraphResult to JSON strings for database storage
func callGraphToJSON(result *CallGraphResult) (*string, []string, *int, error) {
	if result == nil || len(result.Calls) == 0 {
		return nil, nil, nil, nil
	}

	// Convert calls to JSON
	callsJSON, err := json.Marshal(result.Calls)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal calls to JSON: %w", err)
	}

	callsStr := string(callsJSON)
	maxDepth := result.MaxDepth

	return &callsStr, result.ContractsInvolved, &maxDepth, nil
}

// integrateCallGraph populates an operation row's call-graph fields.
//
// A call-graph failure never fails the operation: the three fields stay nil
// and the rest of the row is still correct. That is a deliberate asymmetry —
// a malformed diagnostic event should not cost the caller an entire
// operations row — but it is also why CallGraphSkippedEdges exists, so a
// silent under-count stays observable.
func integrateCallGraph(
	tx ingest.LedgerTransaction,
	opIndex int,
	op xdr.Operation,
	opData *OperationData,
) error {
	// Auth credentials first: they do not depend on the call graph and are
	// worth recording on an operation that has no cross-contract calls at
	// all, so a call-graph failure below must not cost them.
	if invokeOp, ok := op.Body.GetInvokeHostFunctionOp(); ok {
		opData.SorobanAuthCredentialsTypes, opData.SorobanAuthAddresses =
			extractAuthCredentials(invokeOp.Auth)
	}

	callGraph, err := extractCallGraph(tx, opIndex, op)
	if err != nil {
		log.Printf("Warning: Failed to extract call graph for op %d: %v", opIndex, err)
		return nil
	}
	if callGraph == nil {
		return nil // No cross-contract calls on this operation.
	}

	callsJSON, contractsInvolved, maxDepth, err := callGraphToJSON(callGraph)
	if err != nil {
		log.Printf("Warning: Failed to convert call graph to JSON for op %d: %v", opIndex, err)
		return nil
	}

	opData.ContractCallsJSON = callsJSON
	opData.ContractsInvolved = contractsInvolved
	opData.MaxCallDepth = maxDepth

	return nil
}

// GetFunctionNameFromScVal reads a contract function name out of an ScVal.
//
// Symbols and strings are taken as-is. Bytes are accepted only when every
// byte is printable ASCII: a function name can legitimately arrive as bytes,
// but so can a hash, and rendering 32 bytes of hash as a function name is
// worse than reporting none.
func GetFunctionNameFromScVal(val xdr.ScVal) string {
	switch val.Type {
	case xdr.ScValTypeScvSymbol:
		if val.Sym != nil {
			return string(*val.Sym)
		}
	case xdr.ScValTypeScvString:
		if val.Str != nil {
			return string(*val.Str)
		}
	case xdr.ScValTypeScvBytes:
		if val.Bytes != nil {
			if b := *val.Bytes; isPrintableASCII(b) {
				return string(b)
			}
		}
	}
	return ""
}

// isPrintableASCII reports whether every byte is in the printable ASCII range.
func isPrintableASCII(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// Soroban authorization credential discriminants, as stored in
// OperationData.SorobanAuthCredentialsTypes.
const (
	AuthCredentialsSourceAccount       = "SOURCE_ACCOUNT"
	AuthCredentialsAddress             = "ADDRESS"
	AuthCredentialsAddressV2           = "ADDRESS_V2"
	AuthCredentialsAddressWithDelegate = "ADDRESS_WITH_DELEGATES"
)

// extractAuthCredentials describes each authorization entry on an
// InvokeHostFunction operation: the credentials discriminant, and the
// strkey authorizer for the address-bearing arms.
//
// Returns (nil, nil) for zero auth entries so the caller can leave the row
// NULL rather than storing an empty array — "this operation carries no
// authorization" and "this operation carries an authorization with no
// authorizer" are different facts. The two returned slices always have
// equal length, so position i describes the same entry in both.
//
// Useful independently of the call graph: a passkey wallet authorizing a
// plain token transfer has credentials worth recording and no
// cross-contract calls at all.
func extractAuthCredentials(authEntries []xdr.SorobanAuthorizationEntry) ([]string, []string) {
	if len(authEntries) == 0 {
		return nil, nil
	}

	types := make([]string, 0, len(authEntries))
	addrs := make([]string, 0, len(authEntries))

	for _, entry := range authEntries {
		switch entry.Credentials.Type {
		case xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount:
			types = append(types, AuthCredentialsSourceAccount)
			addrs = append(addrs, "")
		case xdr.SorobanCredentialsTypeSorobanCredentialsAddress:
			types = append(types, AuthCredentialsAddress)
			addr := ""
			if ac, ok := entry.Credentials.GetAddress(); ok {
				if s, err := ac.Address.String(); err == nil {
					addr = s
				}
			}
			addrs = append(addrs, addr)
		case xdr.SorobanCredentialsTypeSorobanCredentialsAddressV2:
			types = append(types, AuthCredentialsAddressV2)
			addr := ""
			if ac, ok := entry.Credentials.GetAddressV2(); ok {
				if s, err := ac.Address.String(); err == nil {
					addr = s
				}
			}
			addrs = append(addrs, addr)
		case xdr.SorobanCredentialsTypeSorobanCredentialsAddressWithDelegates:
			types = append(types, AuthCredentialsAddressWithDelegate)
			addr := ""
			if ac, ok := entry.Credentials.GetAddressWithDelegates(); ok {
				if s, err := ac.AddressCredentials.Address.String(); err == nil {
					addr = s
				}
			}
			addrs = append(addrs, addr)
		default:
			// Forward compatibility. A protocol upgrade can add a
			// credentials arm, and Go will not warn about the switch that
			// ignores it; recording the numeric discriminant keeps the new
			// arm visible instead of dropping the entry. See
			// docs/SDK_UPGRADES.md.
			types = append(types, fmt.Sprintf("UNKNOWN_%d", entry.Credentials.Type))
			addrs = append(addrs, "")
		}
	}
	return types, addrs
}
