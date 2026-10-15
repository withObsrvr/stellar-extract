package extract

import "time"

// TokenTransferData represents a unified token transfer event (token_transfers_stream_v1)
// Covers: transfer, mint, burn, clawback, fee events from both classic and Soroban operations
type TokenTransferData struct {
	LedgerSequence  uint32
	TransactionHash string
	TransactionID   int64  // TOID of parent transaction
	OperationID     *int64 // TOID of parent operation (nullable - fee events have no operation)
	OperationIndex  *int32 // 1-indexed (nullable)
	// EventIndex is the row's position within its transaction, counted over
	// the ledger's token-transfer event list in order. It exists because
	// nothing else identifies a transfer: one operation may transfer the
	// same amount between the same parties twice, so even a key of ledger,
	// transaction, operation, event type, from, to, asset and raw amount
	// collides. With it, (LedgerSequence, TransactionHash, EventIndex) is a
	// key.
	EventIndex  uint32
	EventType   string  // transfer, mint, burn, clawback, fee
	From        *string // nullable (mint has no from)
	To          *string // nullable (burn/clawback/fee have no to)
	Asset       string  // canonical: "native" or "credit_alphanum4:CODE:ISSUER"
	AssetType   string  // native, credit_alphanum4, credit_alphanum12
	AssetCode   *string // nullable (native has none)
	AssetIssuer *string // nullable
	Amount      float64 // human-readable (stroops * 0.0000001)
	AmountRaw   string  // raw stroops string from SDK
	ContractID  string
	ClosedAt    time.Time
	CreatedAt   time.Time
	LedgerRange uint32
	EraID       *string
}
