package extract

import (
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
)

func mustLiquidityClaimAtom(t *testing.T, atom xdr.ClaimLiquidityAtom) xdr.ClaimAtom {
	t.Helper()
	claim, err := xdr.NewClaimAtom(xdr.ClaimAtomTypeClaimAtomTypeLiquidityPool, atom)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func testTradeContext() tradeContext {
	return tradeContext{
		ledgerSeq:      62080000,
		txHash:         "abc",
		operationIndex: 7,
		closedAt:       time.Unix(100, 0).UTC(),
		ledgerRange:    62080000,
		buyerAccount:   "GBLVLKGRDU66WLWY4XRORJXCC4LDZ347AQTUYBEPBABIZTVITW2OAGIP",
	}
}

// AMM matches were dropped outright before this: appendTradesFromClaims had an
// explicit `continue` for the liquidity-pool arm. Both the historical and the
// live ingester therefore reported zero pool trades on every ledger — and
// agreed with each other, which is why a parity comparison could not surface
// it. The effects extractor has emitted LP trade effects the whole time.
func TestAppendTradesFromClaimsEmitsLiquidityPoolTrades(t *testing.T) {
	issuer := "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
	poolID := xdr.PoolId{0xAA, 0xBB}

	claim := mustLiquidityClaimAtom(t, xdr.ClaimLiquidityAtom{
		LiquidityPoolId: poolID,
		AssetSold:       xdr.MustNewCreditAsset("USDC", issuer),
		AmountSold:      9553,
		AssetBought:     xdr.MustNewNativeAsset(),
		AmountBought:    63101,
	})

	rows := appendTradesFromClaims(nil, []xdr.ClaimAtom{claim}, testTradeContext())

	if len(rows) != 1 {
		t.Fatalf("expected one liquidity pool trade, got %d", len(rows))
	}
	row := rows[0]

	if row.TradeType != TradeTypeLiquidityPool {
		t.Errorf("TradeType = %q, want %q", row.TradeType, TradeTypeLiquidityPool)
	}
	if row.LiquidityPoolID == nil {
		t.Fatal("LiquidityPoolID is nil on a liquidity pool trade")
	}
	if want := poolIDToString(poolID); *row.LiquidityPoolID != want {
		t.Errorf("LiquidityPoolID = %q, want %q", *row.LiquidityPoolID, want)
	}
	// There is no resting offer and so no counterparty account. TradeType
	// and LiquidityPoolID carry that, not a fabricated seller.
	if row.SellerAccount != "" {
		t.Errorf("SellerAccount = %q, want empty on a pool trade", row.SellerAccount)
	}
	// Amounts go through the same decimal conversion as orderbook trades.
	if row.SellingAmount != "0.0009553" {
		t.Errorf("SellingAmount = %q, want 0.0009553", row.SellingAmount)
	}
	if row.BuyingAmount != "0.0063101" {
		t.Errorf("BuyingAmount = %q, want 0.0063101", row.BuyingAmount)
	}
	if row.SellingAssetCode == nil || *row.SellingAssetCode != "USDC" {
		t.Errorf("SellingAssetCode = %#v, want USDC", row.SellingAssetCode)
	}
}

// An orderbook trade must stay identifiable as one, with no pool id attached.
func TestOrderbookTradeHasNoLiquidityPoolID(t *testing.T) {
	claim := mustClaimAtom(t, xdr.ClaimOfferAtom{
		SellerId:     xdr.MustAddress("GBCW77HZVZAXWTFWY2BM2DPVDARDJUD3JRXAA2JF6WRY3OH3IDFOF5GR"),
		OfferId:      123,
		AssetSold:    xdr.MustNewNativeAsset(),
		AmountSold:   10,
		AssetBought:  xdr.MustNewNativeAsset(),
		AmountBought: 20,
	})

	rows := appendTradesFromClaims(nil, []xdr.ClaimAtom{claim}, testTradeContext())

	if len(rows) != 1 {
		t.Fatalf("expected one trade, got %d", len(rows))
	}
	if rows[0].TradeType != TradeTypeOrderbook {
		t.Errorf("TradeType = %q, want %q", rows[0].TradeType, TradeTypeOrderbook)
	}
	if rows[0].LiquidityPoolID != nil {
		t.Errorf("LiquidityPoolID = %q, want nil on an orderbook trade", *rows[0].LiquidityPoolID)
	}
}

// TradeIndex is the claim's position, not a count of emitted rows.
//
// This is what makes (transaction_hash, operation_index, trade_index) a
// stable handle on one claim atom. Under the previous dense numbering,
// starting to emit pool trades would have renumbered every orderbook trade
// that followed one in the same operation, silently repointing any consumer
// keyed on that triple.
func TestTradeIndexIsPositionalOverClaims(t *testing.T) {
	seller := xdr.MustAddress("GBCW77HZVZAXWTFWY2BM2DPVDARDJUD3JRXAA2JF6WRY3OH3IDFOF5GR")
	native := xdr.MustNewNativeAsset()

	offer := func(id xdr.Int64, sold, bought xdr.Int64) xdr.ClaimAtom {
		return mustClaimAtom(t, xdr.ClaimOfferAtom{
			SellerId: seller, OfferId: id,
			AssetSold: native, AmountSold: sold,
			AssetBought: native, AmountBought: bought,
		})
	}

	claims := []xdr.ClaimAtom{
		offer(1, 0, 0), // skipped: zero amounts
		offer(2, 10, 20),
		offer(3, 30, 40),
	}

	rows := appendTradesFromClaims(nil, claims, testTradeContext())

	if len(rows) != 2 {
		t.Fatalf("expected two emitted trades, got %d", len(rows))
	}
	if rows[0].TradeIndex != 1 {
		t.Errorf("first emitted trade has TradeIndex %d, want 1 — the skipped claim must leave a gap, not shift the rest", rows[0].TradeIndex)
	}
	if rows[1].TradeIndex != 2 {
		t.Errorf("second emitted trade has TradeIndex %d, want 2", rows[1].TradeIndex)
	}
}

// A mixed operation is the case that motivated positional indexing: the
// orderbook trade keeps the index it would have had before pool trades were
// emitted at all.
func TestMixedClaimsKeepStableIndices(t *testing.T) {
	native := xdr.MustNewNativeAsset()
	pool := mustLiquidityClaimAtom(t, xdr.ClaimLiquidityAtom{
		LiquidityPoolId: xdr.PoolId{0x01},
		AssetSold:       native, AmountSold: 5,
		AssetBought: native, AmountBought: 6,
	})
	book := mustClaimAtom(t, xdr.ClaimOfferAtom{
		SellerId:  xdr.MustAddress("GBCW77HZVZAXWTFWY2BM2DPVDARDJUD3JRXAA2JF6WRY3OH3IDFOF5GR"),
		OfferId:   9,
		AssetSold: native, AmountSold: 7,
		AssetBought: native, AmountBought: 8,
	})

	rows := appendTradesFromClaims(nil, []xdr.ClaimAtom{pool, book}, testTradeContext())

	if len(rows) != 2 {
		t.Fatalf("expected both claims emitted, got %d", len(rows))
	}
	if rows[0].TradeType != TradeTypeLiquidityPool || rows[0].TradeIndex != 0 {
		t.Errorf("pool trade: type=%q index=%d, want %q/0", rows[0].TradeType, rows[0].TradeIndex, TradeTypeLiquidityPool)
	}
	if rows[1].TradeType != TradeTypeOrderbook || rows[1].TradeIndex != 1 {
		t.Errorf("orderbook trade: type=%q index=%d, want %q/1", rows[1].TradeType, rows[1].TradeIndex, TradeTypeOrderbook)
	}
}
