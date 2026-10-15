// Package extract provides a shared library for extracting typed rows from
// Stellar ledger data. It is the single source of truth for bronze-layer
// extraction logic, used by the history loader, streaming ingester, nebu
// processors, and flowctl-sdk processors.
//
// All extractors take a [LedgerInput] and return typed row slices. Callers
// are responsible for serialization (Parquet, PG inserts, protobuf, JSON).
package extract

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/stellar/go-stellar-sdk/ingest"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// LedgerInput is the minimal context needed to run any extractor.
type LedgerInput struct {
	LCM               xdr.LedgerCloseMeta
	NetworkPassphrase string
	Sequence          uint32
	ClosedAt          time.Time
	LedgerRange       uint32  // partition key, typically floor(sequence / 10000)
	EraID             *string // optional DuckLake era identifier

	// Decoded transactions, shared by every extractor that needs them.
	// See Transactions.
	transactionsOnce sync.Once
	transactions     []ingest.LedgerTransaction
	transactionsErr  error

	// Decoded entry changes, shared the same way. See Changes.
	changesOnce sync.Once
	changes     []ingest.Change
	changesErr  error
}

// Transactions returns the ledger's transactions in apply order, decoding
// them at most once per LedgerInput.
//
// Seven extractors need the transaction stream, and each used to open its
// own ingest.LedgerTransactionReader — seven full walks of the same ledger
// per ExtractAll, on top of eleven more for the change stream. The walk is
// the expensive part, so it happens once and the result is shared.
//
// The returned slice is shared, not copied: callers read it and must not
// mutate it. A decode failure is remembered too, so a broken ledger fails
// the same way for every caller instead of being retried six more times.
func (input *LedgerInput) Transactions() ([]ingest.LedgerTransaction, error) {
	input.transactionsOnce.Do(func() {
		reader, err := ingest.NewLedgerTransactionReaderFromLedgerCloseMeta(input.NetworkPassphrase, input.LCM)
		if err != nil {
			input.transactionsErr = fmt.Errorf("create transaction reader: %w", err)
			return
		}
		defer reader.Close()

		for {
			tx, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				input.transactionsErr = fmt.Errorf("read transaction: %w", err)
				return
			}
			input.transactions = append(input.transactions, tx)
		}
	})
	return input.transactions, input.transactionsErr
}

// Changes returns the ledger's entry changes in order, reading them at most
// once per LedgerInput.
//
// Eleven extractors need the change stream — the snapshot tables, plus TTL
// and the Soroban state tables — and each used to open its own
// ingest.LedgerChangeReader. Same reasoning as Transactions: the walk is
// the cost, so it happens once.
//
// The returned slice is shared, not copied.
func (input *LedgerInput) Changes() ([]ingest.Change, error) {
	input.changesOnce.Do(func() {
		reader, err := ingest.NewLedgerChangeReaderFromLedgerCloseMeta(input.NetworkPassphrase, input.LCM)
		if err != nil {
			input.changesErr = fmt.Errorf("create change reader: %w", err)
			return
		}
		defer reader.Close()

		for {
			change, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				input.changesErr = fmt.Errorf("read change: %w", err)
				return
			}
			input.changes = append(input.changes, change)
		}
	})
	return input.changes, input.changesErr
}

// NewLedgerInput creates a LedgerInput from an already-decoded LedgerCloseMeta.
func NewLedgerInput(lcm xdr.LedgerCloseMeta, networkPassphrase string) *LedgerInput {
	seq := uint32(lcm.LedgerSequence())
	closedAt := time.Unix(int64(lcm.LedgerHeaderHistoryEntry().Header.ScpValue.CloseTime), 0).UTC()
	return &LedgerInput{
		LCM:               lcm,
		NetworkPassphrase: networkPassphrase,
		Sequence:          seq,
		ClosedAt:          closedAt,
		LedgerRange:       (seq / 10000) * 10000,
	}
}

// NewLedgerInputFromXDR creates a LedgerInput from raw XDR bytes.
// This is the adapter for callers that receive bytes over gRPC or protobuf.
func NewLedgerInputFromXDR(xdrBytes []byte, networkPassphrase string) (*LedgerInput, error) {
	var lcm xdr.LedgerCloseMeta
	if err := lcm.UnmarshalBinary(xdrBytes); err != nil {
		return nil, fmt.Errorf("unmarshal LedgerCloseMeta: %w", err)
	}
	return NewLedgerInput(lcm, networkPassphrase), nil
}

// LedgerData holds all extracted rows from a single ledger.
type LedgerData struct {
	Ledgers           []LedgerRowData
	Transactions      []TransactionData
	Operations        []OperationData
	Effects           []EffectData
	Trades            []TradeData
	Accounts          []AccountData
	Offers            []OfferData
	Trustlines        []TrustlineData
	AccountSigners    []AccountSignerData
	ClaimableBalances []ClaimableBalanceData
	LiquidityPools    []LiquidityPoolData
	ConfigSettings    []ConfigSettingData
	TTLEntries        []TTLData
	NativeBalances    []NativeBalanceData
	ContractEvents    []ContractEventData
	ContractData      []ContractDataData
	ContractCode      []ContractCodeData
	ContractCreations []ContractCreationData
	TokenTransfers    []TokenTransferData
	EvictedKeys       []EvictedKeyData
	RestoredKeys      []RestoredKeyData
}

// ExtractAll runs all extractors concurrently and returns combined results.
// Errors from individual extractors are collected; partial results are returned.
func ExtractAll(input *LedgerInput) (*LedgerData, []error) {
	data := &LedgerData{}
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup

	run := func(name string, fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("panic in %s: %v", name, r))
					mu.Unlock()
				}
			}()
			fn()
		}()
	}

	collect := func(name string, fn func(*LedgerInput) error) {
		run(name, func() {
			if err := fn(input); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				mu.Unlock()
			}
		})
	}

	collect("ledgers", func(in *LedgerInput) error {
		rows, err := ExtractLedgers(in)
		if err == nil {
			mu.Lock()
			data.Ledgers = rows
			mu.Unlock()
		}
		return err
	})
	collect("transactions", func(in *LedgerInput) error {
		rows, err := ExtractTransactions(in)
		if err == nil {
			mu.Lock()
			data.Transactions = rows
			mu.Unlock()
		}
		return err
	})
	collect("operations", func(in *LedgerInput) error {
		rows, err := ExtractOperations(in)
		if err == nil {
			mu.Lock()
			data.Operations = rows
			mu.Unlock()
		}
		return err
	})
	collect("effects", func(in *LedgerInput) error {
		rows, err := ExtractEffects(in)
		if err == nil {
			mu.Lock()
			data.Effects = rows
			mu.Unlock()
		}
		return err
	})
	collect("trades", func(in *LedgerInput) error {
		rows, err := ExtractTrades(in)
		if err == nil {
			mu.Lock()
			data.Trades = rows
			mu.Unlock()
		}
		return err
	})
	collect("accounts", func(in *LedgerInput) error {
		rows, err := ExtractAccounts(in)
		if err == nil {
			mu.Lock()
			data.Accounts = rows
			mu.Unlock()
		}
		return err
	})
	collect("trustlines", func(in *LedgerInput) error {
		rows, err := ExtractTrustlines(in)
		if err == nil {
			mu.Lock()
			data.Trustlines = rows
			mu.Unlock()
		}
		return err
	})
	collect("account_signers", func(in *LedgerInput) error {
		rows, err := ExtractAccountSigners(in)
		if err == nil {
			mu.Lock()
			data.AccountSigners = rows
			mu.Unlock()
		}
		return err
	})
	collect("offers", func(in *LedgerInput) error {
		rows, err := ExtractOffers(in)
		if err == nil {
			mu.Lock()
			data.Offers = rows
			mu.Unlock()
		}
		return err
	})
	collect("claimable_balances", func(in *LedgerInput) error {
		rows, err := ExtractClaimableBalances(in)
		if err == nil {
			mu.Lock()
			data.ClaimableBalances = rows
			mu.Unlock()
		}
		return err
	})
	collect("liquidity_pools", func(in *LedgerInput) error {
		rows, err := ExtractLiquidityPools(in)
		if err == nil {
			mu.Lock()
			data.LiquidityPools = rows
			mu.Unlock()
		}
		return err
	})
	collect("config_settings", func(in *LedgerInput) error {
		rows, err := ExtractConfigSettings(in)
		if err == nil {
			mu.Lock()
			data.ConfigSettings = rows
			mu.Unlock()
		}
		return err
	})
	collect("ttl", func(in *LedgerInput) error {
		rows, err := ExtractTTL(in)
		if err == nil {
			mu.Lock()
			data.TTLEntries = rows
			mu.Unlock()
		}
		return err
	})
	collect("native_balances", func(in *LedgerInput) error {
		rows, err := ExtractNativeBalances(in)
		if err == nil {
			mu.Lock()
			data.NativeBalances = rows
			mu.Unlock()
		}
		return err
	})
	collect("contract_events", func(in *LedgerInput) error {
		rows, err := ExtractContractEvents(in)
		if err == nil {
			mu.Lock()
			data.ContractEvents = rows
			mu.Unlock()
		}
		return err
	})
	collect("contract_data", func(in *LedgerInput) error {
		rows, err := ExtractContractData(in)
		if err == nil {
			mu.Lock()
			data.ContractData = rows
			mu.Unlock()
		}
		return err
	})
	collect("contract_code", func(in *LedgerInput) error {
		rows, err := ExtractContractCode(in)
		if err == nil {
			mu.Lock()
			data.ContractCode = rows
			mu.Unlock()
		}
		return err
	})
	collect("contract_creations", func(in *LedgerInput) error {
		rows, err := ExtractContractCreations(in)
		if err == nil {
			mu.Lock()
			data.ContractCreations = rows
			mu.Unlock()
		}
		return err
	})
	collect("token_transfers", func(in *LedgerInput) error {
		rows, err := ExtractTokenTransfers(in)
		if err == nil {
			mu.Lock()
			data.TokenTransfers = rows
			mu.Unlock()
		}
		return err
	})
	collect("evicted_keys", func(in *LedgerInput) error {
		rows, err := ExtractEvictedKeys(in)
		if err == nil {
			mu.Lock()
			data.EvictedKeys = rows
			mu.Unlock()
		}
		return err
	})
	collect("restored_keys", func(in *LedgerInput) error {
		rows, err := ExtractRestoredKeys(in)
		if err == nil {
			mu.Lock()
			data.RestoredKeys = rows
			mu.Unlock()
		}
		return err
	})

	wg.Wait()
	return data, errs
}
