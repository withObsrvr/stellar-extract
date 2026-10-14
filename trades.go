package extract

import (
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/amount"
	"github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ExtractTrades extracts trades from a pre-decoded ledger.
// Extracts trade data from MANAGE_SELL_OFFER, MANAGE_BUY_OFFER, and CREATE_PASSIVE_SELL_OFFER operations.
func ExtractTrades(input *LedgerInput) ([]TradeData, error) {
	lcm := input.LCM
	ledgerSeq := input.Sequence
	closedAt := input.ClosedAt
	ledgerRange := input.LedgerRange

	var trades []TradeData

	reader, err := ingest.NewLedgerTransactionReaderFromLedgerCloseMeta(input.NetworkPassphrase, lcm)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction reader: %w", err)
	}
	defer reader.Close()

	for {
		tx, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("Error reading transaction for trades in ledger %d: %v", ledgerSeq, err)
			continue
		}

		if !tx.Result.Successful() {
			continue // Only process successful transactions
		}

		txHash := hex.EncodeToString(tx.Result.TransactionHash[:])

		for opIdx, op := range tx.Envelope.Operations() {
			if opResults, ok := tx.Result.Result.OperationResults(); ok {
				if opIdx >= len(opResults) {
					continue
				}
				opResult := opResults[opIdx]

				switch op.Body.Type {
				case xdr.OperationTypeManageSellOffer, xdr.OperationTypeManageBuyOffer,
					xdr.OperationTypeCreatePassiveSellOffer:
					claims := offerClaimsFromResult(opResult)
					buyerAccount := tx.Envelope.SourceAccount().ToAccountId().Address()
					trades = appendTradesFromClaims(trades, claims, tradeContext{
						ledgerSeq:      ledgerSeq,
						txHash:         txHash,
						operationIndex: opIdx,
						closedAt:       closedAt,
						ledgerRange:    ledgerRange,
						eraID:          input.EraID,
						buyerAccount:   buyerAccount,
					})
				case xdr.OperationTypePathPaymentStrictReceive:
					if opResult.Code != xdr.OperationResultCodeOpInner {
						continue
					}
					result := opResult.MustTr().MustPathPaymentStrictReceiveResult()
					if result.Code != xdr.PathPaymentStrictReceiveResultCodePathPaymentStrictReceiveSuccess {
						continue
					}
					buyerAccount := op.Body.MustPathPaymentStrictReceiveOp().Destination.ToAccountId().Address()
					trades = appendTradesFromClaims(trades, result.MustSuccess().Offers, tradeContext{
						ledgerSeq:      ledgerSeq,
						txHash:         txHash,
						operationIndex: opIdx,
						closedAt:       closedAt,
						ledgerRange:    ledgerRange,
						eraID:          input.EraID,
						buyerAccount:   buyerAccount,
					})
				case xdr.OperationTypePathPaymentStrictSend:
					if opResult.Code != xdr.OperationResultCodeOpInner {
						continue
					}
					result := opResult.MustTr().MustPathPaymentStrictSendResult()
					if result.Code != xdr.PathPaymentStrictSendResultCodePathPaymentStrictSendSuccess {
						continue
					}
					buyerAccount := op.Body.MustPathPaymentStrictSendOp().Destination.ToAccountId().Address()
					trades = appendTradesFromClaims(trades, result.MustSuccess().Offers, tradeContext{
						ledgerSeq:      ledgerSeq,
						txHash:         txHash,
						operationIndex: opIdx,
						closedAt:       closedAt,
						ledgerRange:    ledgerRange,
						eraID:          input.EraID,
						buyerAccount:   buyerAccount,
					})
				}
			}
		}
	}

	return trades, nil
}

type tradeContext struct {
	ledgerSeq      uint32
	txHash         string
	operationIndex int
	closedAt       time.Time
	ledgerRange    uint32
	eraID          *string
	buyerAccount   string
}

func offerClaimsFromResult(opResult xdr.OperationResult) []xdr.ClaimAtom {
	if opResult.Code != xdr.OperationResultCodeOpInner {
		return nil
	}
	tr := opResult.MustTr()
	switch tr.Type {
	case xdr.OperationTypeManageSellOffer:
		if r, ok := tr.GetManageSellOfferResult(); ok && r.Code == xdr.ManageSellOfferResultCodeManageSellOfferSuccess {
			return r.MustSuccess().OffersClaimed
		}
	case xdr.OperationTypeManageBuyOffer:
		if r, ok := tr.GetManageBuyOfferResult(); ok && r.Code == xdr.ManageBuyOfferResultCodeManageBuyOfferSuccess {
			return r.MustSuccess().OffersClaimed
		}
	case xdr.OperationTypeCreatePassiveSellOffer:
		if r, ok := tr.GetCreatePassiveSellOfferResult(); ok && r.Code == xdr.ManageSellOfferResultCodeManageSellOfferSuccess {
			return r.MustSuccess().OffersClaimed
		}
	}
	return nil
}

func appendTradesFromClaims(trades []TradeData, claims []xdr.ClaimAtom, ctx tradeContext) []TradeData {
	// tradeIndex is the claim's position, taken from the range rather than
	// counted over emitted rows. A claim this function declines to emit
	// therefore leaves a gap instead of shifting every later trade's index,
	// which is what makes the index a stable handle on the XDR.
	for tradeIndex, claim := range claims {
		if claim.AmountSold() == 0 && claim.AmountBought() == 0 {
			continue
		}
		trade, ok := tradeFromClaim(claim, ctx, tradeIndex)
		if !ok {
			continue
		}
		trades = append(trades, trade)
	}
	return trades
}

// Trade type discriminants, as stored in TradeData.TradeType.
const (
	TradeTypeOrderbook     = "orderbook"
	TradeTypeLiquidityPool = "liquidity_pool"
)

func tradeFromClaim(claim xdr.ClaimAtom, ctx tradeContext, tradeIndex int) (TradeData, bool) {
	var sellerAccount, sellingAmount, buyingAmount string
	var sellingCode, sellingIssuer, buyingCode, buyingIssuer *string
	var sellingAsset, buyingAsset xdr.Asset
	tradeType := TradeTypeOrderbook
	var liquidityPoolID *string

	switch claim.Type {
	case xdr.ClaimAtomTypeClaimAtomTypeOrderBook:
		ob := claim.MustOrderBook()
		sellerAccount = ob.SellerId.Address()
		sellingAmount = amount.String(ob.AmountSold)
		buyingAmount = amount.String(ob.AmountBought)
		sellingAsset = ob.AssetSold
		buyingAsset = ob.AssetBought
	case xdr.ClaimAtomTypeClaimAtomTypeV0:
		v0 := claim.MustV0()
		sellerAccount = fmt.Sprintf("%x", v0.SellerEd25519)
		sellingAmount = amount.String(v0.AmountSold)
		buyingAmount = amount.String(v0.AmountBought)
		sellingAsset = v0.AssetSold
		buyingAsset = v0.AssetBought
	case xdr.ClaimAtomTypeClaimAtomTypeLiquidityPool:
		// An AMM match. There is no resting offer and no counterparty
		// account, so SellerAccount stays empty and the pool is identified
		// by LiquidityPoolID. Note that claim.SellerId() panics on this arm
		// — the fields have to be read off the union directly.
		lp := claim.MustLiquidityPool()
		poolID := poolIDToString(lp.LiquidityPoolId)
		liquidityPoolID = &poolID
		tradeType = TradeTypeLiquidityPool
		sellingAmount = amount.String(lp.AmountSold)
		buyingAmount = amount.String(lp.AmountBought)
		sellingAsset = lp.AssetSold
		buyingAsset = lp.AssetBought
	default:
		return TradeData{}, false
	}

	sellingCode, sellingIssuer = assetCodeIssuer(sellingAsset)
	buyingCode, buyingIssuer = assetCodeIssuer(buyingAsset)

	return TradeData{
		LedgerSequence:     ctx.ledgerSeq,
		TransactionHash:    ctx.txHash,
		OperationIndex:     ctx.operationIndex,
		TradeIndex:         tradeIndex,
		TradeType:          tradeType,
		TradeTimestamp:     ctx.closedAt,
		SellerAccount:      sellerAccount,
		SellingAssetCode:   sellingCode,
		SellingAssetIssuer: sellingIssuer,
		SellingAmount:      sellingAmount,
		BuyerAccount:       ctx.buyerAccount,
		BuyingAssetCode:    buyingCode,
		BuyingAssetIssuer:  buyingIssuer,
		BuyingAmount:       buyingAmount,
		Price:              fmt.Sprintf("%s/%s", buyingAmount, sellingAmount),
		LiquidityPoolID:    liquidityPoolID,
		CreatedAt:          ctx.closedAt,
		LedgerRange:        ctx.ledgerRange,
		EraID:              ctx.eraID,
	}, true
}

func assetCodeIssuer(asset xdr.Asset) (*string, *string) {
	if asset.Type == xdr.AssetTypeAssetTypeNative {
		return nil, nil
	}
	switch asset.Type {
	case xdr.AssetTypeAssetTypeCreditAlphanum4:
		a4 := asset.MustAlphaNum4()
		code := strings.TrimRight(string(a4.AssetCode[:]), "\x00")
		issuer := a4.Issuer.Address()
		return &code, &issuer
	case xdr.AssetTypeAssetTypeCreditAlphanum12:
		a12 := asset.MustAlphaNum12()
		code := strings.TrimRight(string(a12.AssetCode[:]), "\x00")
		issuer := a12.Issuer.Address()
		return &code, &issuer
	default:
		return nil, nil
	}
}
