package extract

import "time"

// LedgerRowData represents a single ledger row (ledgers_row_v2)
type LedgerRowData struct {
	Sequence             uint32
	LedgerHash           string
	PreviousLedgerHash   string
	ClosedAt             time.Time
	ProtocolVersion      uint32
	TotalCoins           int64
	FeePool              int64
	BaseFee              uint32
	BaseReserve          uint32
	MaxTxSetSize         uint32
	TransactionCount     int
	OperationCount       int
	SuccessfulTxCount    int
	FailedTxCount        int
	TxSetOperationCount  int
	SorobanFeeWrite1kb   *int64
	NodeID               *string
	Signature            *string
	LedgerHeader         *string
	BucketListSize       *int64
	LiveSorobanStateSize *int64
	EvictedKeysCount     *int32
	SorobanOpCount       *int32
	TotalFeeCharged      *int64
	ContractEventsCount  *int32
	IngestionTimestamp   time.Time
	LedgerRange          uint32
	PipelineVersion      string
	EraID                *string
}

// TransactionData represents a single transaction
type TransactionData struct {
	LedgerSequence        uint32
	TransactionHash       string
	SourceAccount         string
	SourceAccountMuxed    *string
	FeeCharged            int64
	MaxFee                int64
	Successful            bool
	TransactionResultCode string
	OperationCount        int
	MemoType              *string
	Memo                  *string
	// MemoRaw is base64 of the raw memo bytes, set for text memos only and
	// authoritative when set. A MEMO_TEXT payload is opaque bytes that need
	// not be valid UTF-8, so Memo carries the text only when it genuinely is
	// text and is NULL otherwise — a value the producer cannot represent is
	// absent, not mangled. id, hash and return memos are already lossless as
	// decimal and hex, so MemoRaw stays nil for them.
	MemoRaw           *string
	CreatedAt         time.Time
	AccountSequence   int64
	LedgerRange       uint32
	SignaturesCount   int
	NewAccount        bool
	TimeboundsMinTime *int64
	TimeboundsMaxTime *int64
	// Soroban fields
	SorobanHostFunctionType *string
	SorobanContractID       *string
	RentFeeCharged          *int64
	// Soroban resource fields
	SorobanResourcesInstructions *int64
	SorobanResourcesReadBytes    *int64
	SorobanResourcesWriteBytes   *int64
	// Declared footprint entry counts. The protocol charges ledger capacity
	// against what a transaction declares, not against the changes it turns
	// out to make, so these are the numerators for a capacity meter. Counting
	// observed changes instead undercounts badly.
	SorobanFootprintReadEntries  *int32
	SorobanFootprintWriteEntries *int32
	// Contract failure cause, set only when the transaction emitted an
	// ScError. Successful transactions and classic failures leave it nil.
	ContractErrorType *string
	ContractErrorCode *int32
	// Raw XDR, base64-encoded, and the signature list. These are what makes
	// a Bronze transaction row reprocessable without going back to the
	// archive: every derived column above can be recomputed from TxEnvelope,
	// TxResult and TxMeta. Horizon exposes the same five.
	TxEnvelope *string
	TxResult   *string
	TxMeta     *string
	TxFeeMeta  *string
	// TxSigners is a JSON array of base64 signatures, in envelope order.
	TxSigners *string
	// EnvelopeSizeBytes is the length of the decoded envelope XDR, not of its
	// base64 encoding — the size the protocol charges against.
	EnvelopeSizeBytes int
	// TOID
	TransactionID int64
	EraID         *string
}

// OperationData represents a single operation
type OperationData struct {
	TransactionHash       string
	TransactionIndex      int
	OperationIndex        int
	LedgerSequence        uint32
	SourceAccount         string
	SourceAccountMuxed    *string
	OpType                int
	TypeString            string
	CreatedAt             time.Time
	TransactionSuccessful bool
	OperationResultCode   *string
	LedgerRange           uint32
	// Core operation fields
	Amount      *int64
	Asset       *string
	Destination *string
	// Decomposed asset fields
	AssetType   *string
	AssetCode   *string
	AssetIssuer *string
	// Source asset (path payments)
	SourceAsset       *string
	SourceAssetType   *string
	SourceAssetCode   *string
	SourceAssetIssuer *string
	SourceAmount      *int64
	DestinationMin    *int64
	StartingBalance   *int64
	// Trustline
	TrustlineLimit *int64
	// Offer fields
	OfferID *int64
	Price   *string
	PriceR  *string
	// Buying/Selling assets (offers)
	BuyingAsset        *string
	BuyingAssetType    *string
	BuyingAssetCode    *string
	BuyingAssetIssuer  *string
	SellingAsset       *string
	SellingAssetType   *string
	SellingAssetCode   *string
	SellingAssetIssuer *string
	// Set options
	SetFlags        *int
	ClearFlags      *int
	HomeDomain      *string
	MasterWeight    *int
	LowThreshold    *int
	MediumThreshold *int
	HighThreshold   *int
	// Manage data
	DataName  *string
	DataValue *string
	// Claimable balance
	BalanceID *string
	// Sponsorship
	SponsoredID *string
	// Bump sequence
	BumpTo *int64
	// Soroban
	SorobanAuthRequired  *bool
	SorobanOperation     *string
	SorobanContractID    *string
	SorobanFunction      *string
	SorobanArgumentsJSON *string
	// Call graph fields
	ContractCallsJSON *string
	ContractsInvolved []string
	MaxCallDepth      *int

	// Soroban authorization credentials, one entry per
	// SorobanAuthorizationEntry on the operation. Parallel arrays of equal
	// length: Types holds the credentials discriminant and Addresses the
	// strkey authorizer, empty for SOURCE_ACCOUNT where the authorizer is
	// the transaction source. Both nil on non-Soroban operations and on
	// InvokeHostFunction operations with no auth entries, which keeps
	// "no auth" distinct from "an empty authorizer".
	SorobanAuthCredentialsTypes []string
	SorobanAuthAddresses        []string
	// TOID
	TransactionID int64
	OperationID   int64
	EraID         *string
}

// EffectData represents a single effect (state changes from operations)
type EffectData struct {
	LedgerSequence   uint32
	TransactionHash  string
	OperationIndex   int
	EffectIndex      int
	EffectType       int
	EffectTypeString string
	AccountID        *string
	Amount           *string
	AssetCode        *string
	AssetIssuer      *string
	AssetType        *string
	TrustlineLimit   *string
	AuthorizeFlag    *bool
	ClawbackFlag     *bool
	SignerAccount    *string
	SignerWeight     *int
	OfferID          *int64
	SellerAccount    *string
	OperationID      *int64
	DetailsJSON      *string
	CreatedAt        time.Time
	LedgerRange      uint32
	EraID            *string
}

// TradeData represents a single trade execution (DEX trades)
type TradeData struct {
	LedgerSequence  uint32
	TransactionHash string
	OperationIndex  int
	// TradeIndex is the position of the claim atom within the operation's
	// claim list, not a running count of emitted rows. Positional indexing
	// makes (transaction_hash, operation_index, trade_index) identify one
	// claim atom in the XDR, and keeps that identity stable when the set of
	// claims this library emits changes.
	TradeIndex int
	// TradeType is "orderbook" when the counterparty is a resting offer and
	// "liquidity_pool" when it is an AMM pool. A pool trade has no
	// counterparty account: SellerAccount is empty and LiquidityPoolID
	// carries the pool instead.
	TradeType          string
	TradeTimestamp     time.Time
	SellerAccount      string
	SellingAssetCode   *string
	SellingAssetIssuer *string
	SellingAmount      string
	BuyerAccount       string
	BuyingAssetCode    *string
	BuyingAssetIssuer  *string
	BuyingAmount       string
	Price              string
	// LiquidityPoolID is the hex-encoded pool hash, non-nil exactly when
	// TradeType is "liquidity_pool". Hex matches how this library already
	// renders pool identifiers in liquidity_pools and in trade effects.
	LiquidityPoolID *string
	CreatedAt       time.Time
	LedgerRange     uint32
	EraID           *string
}
