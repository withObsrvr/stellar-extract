package extract

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/toid"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ExtractTransactions extracts transaction data from a pre-decoded ledger.
func ExtractTransactions(input *LedgerInput) ([]TransactionData, error) {
	ledgerSeq := input.Sequence
	closedAt := input.ClosedAt
	ledgerRange := input.LedgerRange

	var transactions []TransactionData

	txs, err := input.Transactions()
	if err != nil {
		return nil, fmt.Errorf("failed to read transactions: %w", err)
	}

	var txIndex int32 = 1 // 1-indexed per TOID convention
	for _, tx := range txs {

		txData := TransactionData{
			LedgerSequence:        ledgerSeq,
			TransactionHash:       hex.EncodeToString(tx.Result.TransactionHash[:]),
			SourceAccount:         tx.Envelope.SourceAccount().ToAccountId().Address(),
			SourceAccountMuxed:    getMuxedAddress(tx.Envelope.SourceAccount()),
			FeeCharged:            int64(tx.Result.Result.FeeCharged),
			MaxFee:                int64(tx.Envelope.Fee()),
			Successful:            tx.Result.Successful(),
			TransactionResultCode: tx.Result.Result.Result.Code.String(),
			OperationCount:        len(tx.Envelope.Operations()),
			CreatedAt:             closedAt,
			AccountSequence:       int64(tx.Envelope.SeqNum()),
			LedgerRange:           ledgerRange,
			SignaturesCount:       len(tx.Envelope.Signatures()),
			NewAccount:            false,
			EraID:                 input.EraID,
		}

		// Extract timebounds. Stored as BIGINT in v3_bronze_schema.sql, so
		// we keep them as int64 (matching PG/DuckLake) instead of formatting
		// to a string.
		if tb := tx.Envelope.TimeBounds(); tb != nil {
			minTime := int64(tb.MinTime)
			txData.TimeboundsMinTime = &minTime
			maxTime := int64(tb.MaxTime)
			txData.TimeboundsMaxTime = &maxTime
		}

		// Extract Soroban host function type and contract ID from operations
		for _, op := range tx.Envelope.Operations() {
			if op.Body.Type == xdr.OperationTypeInvokeHostFunction {
				if invokeOp, ok := op.Body.GetInvokeHostFunctionOp(); ok {
					fnType := invokeOp.HostFunction.Type.String()
					txData.SorobanHostFunctionType = &fnType
					// Extract contract ID from soroban meta if available
					if invokeOp.HostFunction.Type == xdr.HostFunctionTypeHostFunctionTypeInvokeContract && invokeOp.HostFunction.InvokeContract != nil {
						contractIDStr, err := invokeOp.HostFunction.InvokeContract.ContractAddress.String()
						if err == nil && contractIDStr != "" {
							txData.SorobanContractID = &contractIDStr
						}
					}
				}
				break // Only need the first InvokeHostFunction op
			}
		}

		// Extract memo
		memo := tx.Envelope.Memo()
		switch memo.Type {
		case xdr.MemoTypeMemoNone:
			memoType := "none"
			txData.MemoType = &memoType
		case xdr.MemoTypeMemoText:
			// MemoText is opaque bytes and need not be valid UTF-8, which a
			// Parquet string column and a Postgres TEXT column both reject.
			// The raw bytes go to MemoRaw and are authoritative; Memo carries
			// the text only when it genuinely is text.
			memoType := "text"
			txData.MemoType = &memoType
			if text, ok := memo.GetText(); ok {
				raw := base64.StdEncoding.EncodeToString([]byte(text))
				txData.MemoRaw = &raw
				if utf8.ValidString(text) {
					memoText := text
					txData.Memo = &memoText
				}
			}
		case xdr.MemoTypeMemoId:
			memoType := "id"
			txData.MemoType = &memoType
			if id, ok := memo.GetId(); ok {
				memoStr := fmt.Sprintf("%d", id)
				txData.Memo = &memoStr
			}
		case xdr.MemoTypeMemoHash:
			memoType := "hash"
			txData.MemoType = &memoType
			if hash, ok := memo.GetHash(); ok {
				memoStr := hex.EncodeToString(hash[:])
				txData.Memo = &memoStr
			}
		case xdr.MemoTypeMemoReturn:
			memoType := "return"
			txData.MemoType = &memoType
			if ret, ok := memo.GetRetHash(); ok {
				memoStr := hex.EncodeToString(ret[:])
				txData.Memo = &memoStr
			}
		}

		// Check for CREATE_ACCOUNT operation
		for _, op := range tx.Envelope.Operations() {
			if op.Body.Type == xdr.OperationTypeCreateAccount {
				txData.NewAccount = true
				break
			}
		}

		// Extract Soroban rent fee charged (C13)
		if ext := sorobanMetaExt(tx.UnsafeMeta); ext != nil && ext.V == 1 && ext.V1 != nil {
			rentFee := int64(ext.V1.RentFeeCharged)
			txData.RentFeeCharged = &rentFee
		}

		// Extract Soroban resource fields from envelope
		if instructions, ok := tx.SorobanResourcesInstructions(); ok {
			val := int64(instructions)
			txData.SorobanResourcesInstructions = &val
		}
		if readBytes, ok := tx.SorobanResourcesDiskReadBytes(); ok {
			val := int64(readBytes)
			txData.SorobanResourcesReadBytes = &val
		}
		if writeBytes, ok := tx.SorobanResourcesWriteBytes(); ok {
			val := int64(writeBytes)
			txData.SorobanResourcesWriteBytes = &val
		}

		// Declared footprint entry counts. GetSorobanData resolves fee-bumped
		// transactions to their inner envelope, so this covers both shapes.
		//
		// A Soroban transaction declaring an empty footprint records 0, not
		// nil — "declared nothing" and "not a Soroban transaction" are
		// different facts, and only the latter should be absent.
		if sorobanData, ok := tx.GetSorobanData(); ok {
			readEntries := int32(len(sorobanData.Resources.Footprint.ReadOnly))
			writeEntries := int32(len(sorobanData.Resources.Footprint.ReadWrite))
			txData.SorobanFootprintReadEntries = &readEntries
			txData.SorobanFootprintWriteEntries = &writeEntries
		}

		if errorType, errorCode, ok := contractErrorFacts(tx); ok {
			txData.ContractErrorType = &errorType
			txData.ContractErrorCode = errorCode
		}

		if err := populateTransactionXDRFields(tx, &txData); err != nil {
			return nil, fmt.Errorf("transaction %s: %w", txData.TransactionHash, err)
		}

		// Compute TOID
		txData.TransactionID = toid.New(int32(ledgerSeq), txIndex, 0).ToInt64()

		transactions = append(transactions, txData)
		txIndex++
	}

	return transactions, nil
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// decomposeAsset splits an XDR asset into type, code, and issuer components.
func decomposeAsset(asset xdr.Asset) (assetType, assetCode, assetIssuer string) {
	switch asset.Type {
	case xdr.AssetTypeAssetTypeNative:
		return "native", "", ""
	case xdr.AssetTypeAssetTypeCreditAlphanum4:
		if a4, ok := asset.GetAlphaNum4(); ok {
			code := strings.TrimRight(string(a4.AssetCode[:]), "\x00")
			return "credit_alphanum4", code, a4.Issuer.Address()
		}
	case xdr.AssetTypeAssetTypeCreditAlphanum12:
		if a12, ok := asset.GetAlphaNum12(); ok {
			code := strings.TrimRight(string(a12.AssetCode[:]), "\x00")
			return "credit_alphanum12", code, a12.Issuer.Address()
		}
	}
	return "", "", ""
}

// setAssetFields populates decomposed asset fields on an OperationData from an XDR asset.
func setAssetFields(opData *OperationData, asset xdr.Asset) {
	canonical := asset.StringCanonical()
	opData.Asset = &canonical
	aType, aCode, aIssuer := decomposeAsset(asset)
	opData.AssetType = &aType
	if aCode != "" {
		opData.AssetCode = &aCode
	}
	if aIssuer != "" {
		opData.AssetIssuer = &aIssuer
	}
}

// setSourceAssetFields populates source asset fields on an OperationData from an XDR asset.
func setSourceAssetFields(opData *OperationData, asset xdr.Asset) {
	canonical := asset.StringCanonical()
	opData.SourceAsset = &canonical
	aType, aCode, aIssuer := decomposeAsset(asset)
	opData.SourceAssetType = &aType
	if aCode != "" {
		opData.SourceAssetCode = &aCode
	}
	if aIssuer != "" {
		opData.SourceAssetIssuer = &aIssuer
	}
}

// setBuyingAssetFields populates buying asset fields on an OperationData from an XDR asset.
func setBuyingAssetFields(opData *OperationData, asset xdr.Asset) {
	canonical := asset.StringCanonical()
	opData.BuyingAsset = &canonical
	aType, aCode, aIssuer := decomposeAsset(asset)
	opData.BuyingAssetType = &aType
	if aCode != "" {
		opData.BuyingAssetCode = &aCode
	}
	if aIssuer != "" {
		opData.BuyingAssetIssuer = &aIssuer
	}
}

// setSellingAssetFields populates selling asset fields on an OperationData from an XDR asset.
func setSellingAssetFields(opData *OperationData, asset xdr.Asset) {
	canonical := asset.StringCanonical()
	opData.SellingAsset = &canonical
	aType, aCode, aIssuer := decomposeAsset(asset)
	opData.SellingAssetType = &aType
	if aCode != "" {
		opData.SellingAssetCode = &aCode
	}
	if aIssuer != "" {
		opData.SellingAssetIssuer = &aIssuer
	}
}

// sorobanMetaExt returns the Soroban meta extension for a transaction, or nil
// when the transaction carries no Soroban meta. Soroban meta appears in
// TransactionMeta V3 (SorobanTransactionMeta) and, since Protocol 23, in V4
// (SorobanTransactionMetaV2); both share SorobanTransactionMetaExt. Reading
// only V3 leaves rent_fee_charged NULL on every current-protocol ledger.
func sorobanMetaExt(meta xdr.TransactionMeta) *xdr.SorobanTransactionMetaExt {
	switch meta.V {
	case 3:
		if v3 := meta.MustV3(); v3.SorobanMeta != nil {
			return &v3.SorobanMeta.Ext
		}
	case 4:
		if v4 := meta.MustV4(); v4.SorobanMeta != nil {
			return &v4.SorobanMeta.Ext
		}
	}
	return nil
}

// getMuxedAddress returns the muxed account string if the account is muxed, nil otherwise.
func getMuxedAddress(account xdr.MuxedAccount) *string {
	if account.Type == xdr.CryptoKeyTypeKeyTypeMuxedEd25519 {
		addr := account.Address()
		return &addr
	}
	return nil
}

// getAssetString returns the canonical string representation of an XDR asset.
func getAssetString(asset xdr.Asset) string {
	switch asset.Type {
	case xdr.AssetTypeAssetTypeNative:
		return "native"
	case xdr.AssetTypeAssetTypeCreditAlphanum4:
		if a4, ok := asset.GetAlphaNum4(); ok {
			code := string(a4.AssetCode[:])
			issuer := a4.Issuer.Address()
			return fmt.Sprintf("%s:%s", code, issuer)
		}
	case xdr.AssetTypeAssetTypeCreditAlphanum12:
		if a12, ok := asset.GetAlphaNum12(); ok {
			code := string(a12.AssetCode[:])
			issuer := a12.Issuer.Address()
			return fmt.Sprintf("%s:%s", code, issuer)
		}
	}
	return ""
}

// marshalToBase64 encodes an XDR-marshallable value to a base64 string.
func marshalToBase64(v interface{ MarshalBinary() ([]byte, error) }) *string {
	if bytes, err := v.MarshalBinary(); err == nil {
		encoded := base64.StdEncoding.EncodeToString(bytes)
		return &encoded
	}
	return nil
}

// extractContractInvocationDetails extracts contract ID, function name, and arguments
// from an InvokeHostFunction operation.
func extractContractInvocationDetails(op xdr.Operation) (*string, *string, *string, error) {
	invokeOp, ok := op.Body.GetInvokeHostFunctionOp()
	if !ok {
		return nil, nil, nil, nil
	}

	if invokeOp.HostFunction.Type != xdr.HostFunctionTypeHostFunctionTypeInvokeContract {
		return nil, nil, nil, nil
	}

	if invokeOp.HostFunction.InvokeContract == nil {
		return nil, nil, nil, nil
	}

	invokeContract := invokeOp.HostFunction.InvokeContract

	// Extract contract address
	var contractID *string
	contractIDStr, err := invokeContract.ContractAddress.String()
	if err == nil && contractIDStr != "" {
		contractID = &contractIDStr
	}

	// Extract function name
	var functionName *string
	if invokeContract.FunctionName != "" {
		fnName := string(invokeContract.FunctionName)
		functionName = &fnName
	}

	// Extract arguments - encode each ScVal as base64 XDR for portability
	// (avoids dependency on the full ScVal-to-JSON converter)
	args := invokeContract.Args
	var argsJSON []interface{}
	for _, arg := range args {
		argBytes, marshalErr := arg.MarshalBinary()
		if marshalErr != nil {
			log.Printf("Warning: Failed to marshal ScVal arg: %v", marshalErr)
			argsJSON = append(argsJSON, map[string]interface{}{
				"error": marshalErr.Error(),
				"type":  arg.Type.String(),
			})
		} else {
			argsJSON = append(argsJSON, map[string]interface{}{
				"type":    arg.Type.String(),
				"xdr_b64": base64.StdEncoding.EncodeToString(argBytes),
			})
		}
	}

	var argsStr *string
	if len(argsJSON) > 0 {
		argsJSONBytes, marshalErr := json.Marshal(argsJSON)
		if marshalErr != nil {
			return contractID, functionName, nil, fmt.Errorf("failed to marshal arguments to JSON: %w", marshalErr)
		}
		s := string(argsJSONBytes)
		argsStr = &s
	}

	return contractID, functionName, argsStr, nil
}

// populateTransactionXDRFields records the raw envelope, result, meta, fee
// meta and signatures so a Bronze row can be reprocessed without re-reading
// the archive.
func populateTransactionXDRFields(tx ingest.LedgerTransaction, txData *TransactionData) error {
	envelopeBase64, err := xdr.MarshalBase64(tx.Envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	envelopeSize, err := decodedBase64Length(envelopeBase64)
	if err != nil {
		return fmt.Errorf("size envelope: %w", err)
	}
	resultBase64, err := xdr.MarshalBase64(&tx.Result.Result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	metaBase64, err := xdr.MarshalBase64(tx.UnsafeMeta)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	feeMetaBase64, err := xdr.MarshalBase64(tx.FeeChanges)
	if err != nil {
		return fmt.Errorf("marshal fee meta: %w", err)
	}
	signersJSON, err := marshalDecoratedSignatures(tx.Envelope.Signatures())
	if err != nil {
		return fmt.Errorf("marshal signatures: %w", err)
	}

	txData.EnvelopeSizeBytes = envelopeSize
	txData.TxEnvelope = &envelopeBase64
	txData.TxResult = &resultBase64
	txData.TxMeta = &metaBase64
	txData.TxFeeMeta = &feeMetaBase64
	txData.TxSigners = &signersJSON
	return nil
}

// marshalDecoratedSignatures renders the signature list as a JSON array of
// base64 signatures, in envelope order. The hint is omitted: it is derivable
// from the signing key and is not what a consumer reconstructs.
func marshalDecoratedSignatures(signatures []xdr.DecoratedSignature) (string, error) {
	out := make([]string, len(signatures))
	for i, sig := range signatures {
		out[i] = base64.StdEncoding.EncodeToString(sig.Signature)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// decodedBase64Length reports the size of the encoded value's decoded bytes,
// which is the size the protocol charges against, not the base64 length.
func decodedBase64Length(encoded string) (int, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return 0, err
	}
	return len(raw), nil
}
