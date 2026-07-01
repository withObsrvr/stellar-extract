package extract

import (
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestAppendTradesFromClaimsSkipsZeroClaimsAndFormatsAmounts(t *testing.T) {
	seller := xdr.MustAddress("GBCW77HZVZAXWTFWY2BM2DPVDARDJUD3JRXAA2JF6WRY3OH3IDFOF5GR")
	issuer := "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
	soldAsset := xdr.MustNewCreditAsset("USDC", issuer)
	boughtAsset := xdr.MustNewNativeAsset()

	claim := mustClaimAtom(t, xdr.ClaimOfferAtom{
		SellerId:     seller,
		OfferId:      123,
		AssetSold:    soldAsset,
		AmountSold:   9553,
		AssetBought:  boughtAsset,
		AmountBought: 63101,
	})
	zeroClaim := mustClaimAtom(t, xdr.ClaimOfferAtom{
		SellerId:     seller,
		OfferId:      124,
		AssetSold:    soldAsset,
		AmountSold:   0,
		AssetBought:  boughtAsset,
		AmountBought: 0,
	})

	rows := appendTradesFromClaims(nil, []xdr.ClaimAtom{claim, zeroClaim}, tradeContext{
		ledgerSeq:      62080000,
		txHash:         "abc",
		operationIndex: 7,
		closedAt:       time.Unix(100, 0).UTC(),
		ledgerRange:    62080000,
		buyerAccount:   "GBLVLKGRDU66WLWY4XRORJXCC4LDZ347AQTUYBEPBABIZTVITW2OAGIP",
	})

	if len(rows) != 1 {
		t.Fatalf("expected one nonzero trade row, got %d", len(rows))
	}

	row := rows[0]
	if row.SellingAmount != "0.0009553" {
		t.Fatalf("unexpected selling amount: %s", row.SellingAmount)
	}
	if row.BuyingAmount != "0.0063101" {
		t.Fatalf("unexpected buying amount: %s", row.BuyingAmount)
	}
	if row.Price != "0.0063101/0.0009553" {
		t.Fatalf("unexpected price: %s", row.Price)
	}
	if row.SellingAssetCode == nil || *row.SellingAssetCode != "USDC" {
		t.Fatalf("unexpected selling asset code: %#v", row.SellingAssetCode)
	}
	if row.SellingAssetIssuer == nil || *row.SellingAssetIssuer != issuer {
		t.Fatalf("unexpected selling asset issuer: %#v", row.SellingAssetIssuer)
	}
	if row.BuyingAssetCode != nil || row.BuyingAssetIssuer != nil {
		t.Fatalf("native buying asset should not have code/issuer: %#v %#v", row.BuyingAssetCode, row.BuyingAssetIssuer)
	}
	if row.TradeIndex != 0 {
		t.Fatalf("unexpected trade index: %d", row.TradeIndex)
	}
}

func mustClaimAtom(t *testing.T, offer xdr.ClaimOfferAtom) xdr.ClaimAtom {
	t.Helper()
	claim, err := xdr.NewClaimAtom(xdr.ClaimAtomTypeClaimAtomTypeOrderBook, offer)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}
