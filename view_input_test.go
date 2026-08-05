package extract

import (
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestNewLedgerViewInputMetadata(t *testing.T) {
	raw := emptyLedgerXDR(t, 60200000, 1700000000)
	input, err := NewLedgerViewInput(raw, network.PublicNetworkPassphrase)
	if err != nil {
		t.Fatalf("NewLedgerViewInput returned error: %v", err)
	}
	if input.Sequence != 60200000 {
		t.Errorf("Sequence = %d, want 60200000", input.Sequence)
	}
	if got := input.ClosedAt.Unix(); got != 1700000000 {
		t.Errorf("ClosedAt = %d, want 1700000000", got)
	}
	if input.LedgerRange != 60200000 {
		t.Errorf("LedgerRange = %d, want 60200000", input.LedgerRange)
	}
	eraID := "backfill-v1"
	input.LedgerRange = 60160000
	input.EraID = &eraID

	parsed, err := input.Decode()
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if parsed.LCM.LedgerSequence() != input.Sequence {
		t.Errorf("decoded sequence = %d, want %d", parsed.LCM.LedgerSequence(), input.Sequence)
	}
	if parsed.LedgerRange != input.LedgerRange {
		t.Errorf("decoded ledger range = %d, want %d", parsed.LedgerRange, input.LedgerRange)
	}
	if parsed.EraID == nil || *parsed.EraID != eraID {
		t.Errorf("decoded era ID = %v, want %q", parsed.EraID, eraID)
	}
}

func TestNewLedgerViewInputRejectsMalformedXDR(t *testing.T) {
	if _, err := NewLedgerViewInput([]byte{0, 0, 0}, network.PublicNetworkPassphrase); err == nil {
		t.Fatal("expected malformed XDR error")
	}
}

func TestExtractAllFromXDRRejectsMalformedXDR(t *testing.T) {
	data, errs := ExtractAllFromXDR([]byte{0, 0, 0}, network.PublicNetworkPassphrase)
	if data == nil {
		t.Fatal("ExtractAllFromXDR returned nil data")
	}
	if len(errs) != 1 {
		t.Fatalf("error count = %d, want 1", len(errs))
	}
}

func TestExtractContractEventsFromViews(t *testing.T) {
	contractEvent := fixtureContractEvent("transfer")
	diagnostic := xdr.DiagnosticEvent{InSuccessfulContractCall: true, Event: contractEvent}
	transactionEvent := xdr.TransactionEvent{
		Stage: xdr.TransactionEventStageTransactionEventStageAfterTx,
		Event: contractEvent,
	}

	var hash [32]byte
	hash[0] = 0xab
	eraID := "backfill-v1"
	input := &LedgerViewInput{
		Sequence:    60200000,
		ClosedAt:    time.Unix(1700000000, 0).UTC(),
		LedgerRange: 60200000,
		EraID:       &eraID,
	}
	transactions := []ingest.LedgerTransactionView{{
		Hash:              hash,
		Successful:        true,
		DiagnosticEvents:  [][]byte{mustMarshalXDR(t, diagnostic)},
		ContractEvents:    [][][]byte{{mustMarshalXDR(t, contractEvent)}},
		TransactionEvents: [][]byte{mustMarshalXDR(t, transactionEvent)},
	}}

	rows, err := extractContractEventsFromViews(input, transactions)
	if err != nil {
		t.Fatalf("extractContractEventsFromViews returned error: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("row count = %d, want 3", len(rows))
	}
	if rows[0].EventType != "diagnostic" {
		t.Errorf("diagnostic EventType = %q", rows[0].EventType)
	}
	if rows[1].EventType != "contract" || rows[1].OperationIndex != 0 {
		t.Errorf("operation event = type %q operation %d", rows[1].EventType, rows[1].OperationIndex)
	}
	if !rows[2].InSuccessfulContractCall {
		t.Error("transaction-level event is not marked in a successful call")
	}
	for index := range rows {
		if !rows[index].Successful {
			t.Errorf("row %d Successful = false", index)
		}
		if rows[index].EraID == nil || *rows[index].EraID != eraID {
			t.Errorf("row %d EraID = %v", index, rows[index].EraID)
		}
	}
}

func TestExtractAllViewEmptyLedgerRowCountParity(t *testing.T) {
	raw := emptyLedgerXDR(t, 60200000, 1700000000)
	parsedInput, err := NewLedgerInputFromXDR(raw, network.PublicNetworkPassphrase)
	if err != nil {
		t.Fatalf("NewLedgerInputFromXDR returned error: %v", err)
	}
	viewInput, err := NewLedgerViewInput(raw, network.PublicNetworkPassphrase)
	if err != nil {
		t.Fatalf("NewLedgerViewInput returned error: %v", err)
	}

	parsed, parsedErrs := ExtractAll(parsedInput)
	view, viewErrs := ExtractAllView(viewInput)
	assertExtractionErrorParity(t, parsedErrs, viewErrs)
	viewEvents, err := ExtractContractEventsView(viewInput)
	if err != nil {
		t.Fatalf("ExtractContractEventsView returned error: %v", err)
	}
	view.ContractEvents = viewEvents
	assertLedgerDataRowCountParity(t, parsed, view)
}

func TestExtractAllViewRealLedgerParity(t *testing.T) {
	fixturePath := os.Getenv("STELLAR_EXTRACT_LEDGER_FIXTURE")
	if fixturePath == "" {
		t.Skip("set STELLAR_EXTRACT_LEDGER_FIXTURE to run real-ledger differential parity")
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	parsedInput, err := NewLedgerInputFromXDR(raw, network.PublicNetworkPassphrase)
	if err != nil {
		t.Fatalf("NewLedgerInputFromXDR returned error: %v", err)
	}
	viewInput, err := NewLedgerViewInput(raw, network.PublicNetworkPassphrase)
	if err != nil {
		t.Fatalf("NewLedgerViewInput returned error: %v", err)
	}

	parsed, parsedErrs := ExtractAll(parsedInput)
	view, viewErrs := ExtractAllView(viewInput)
	assertExtractionErrorParity(t, parsedErrs, viewErrs)
	viewEvents, err := ExtractContractEventsView(viewInput)
	if err != nil {
		t.Fatalf("ExtractContractEventsView returned error: %v", err)
	}
	view.ContractEvents = viewEvents
	assertLedgerDataRowCountParity(t, parsed, view)

	for index := range parsed.ContractEvents {
		parsed.ContractEvents[index].CreatedAt = time.Time{}
	}
	for index := range view.ContractEvents {
		view.ContractEvents[index].CreatedAt = time.Time{}
	}
	if !reflect.DeepEqual(parsed.ContractEvents, view.ContractEvents) {
		t.Fatal("view-backed contract events differ from parsed contract events")
	}
}

func assertExtractionErrorParity(t *testing.T, parsed, view []error) {
	t.Helper()
	parsedMessages := make([]string, len(parsed))
	for index := range parsed {
		parsedMessages[index] = parsed[index].Error()
	}
	viewMessages := make([]string, len(view))
	for index := range view {
		viewMessages[index] = view[index].Error()
	}
	sort.Strings(parsedMessages)
	sort.Strings(viewMessages)
	if !reflect.DeepEqual(parsedMessages, viewMessages) {
		t.Fatalf("extraction errors differ: parsed=%v view=%v", parsedMessages, viewMessages)
	}
}

func assertLedgerDataRowCountParity(t *testing.T, parsed, view *LedgerData) {
	t.Helper()
	parsedValue := reflect.ValueOf(parsed).Elem()
	viewValue := reflect.ValueOf(view).Elem()
	dataType := parsedValue.Type()
	for index := 0; index < parsedValue.NumField(); index++ {
		parsedCount := parsedValue.Field(index).Len()
		viewCount := viewValue.Field(index).Len()
		if parsedCount != viewCount {
			t.Errorf("%s row count: parsed=%d view=%d", dataType.Field(index).Name, parsedCount, viewCount)
		}
	}
}

func emptyLedgerXDR(t *testing.T, sequence uint32, closeTime uint64) []byte {
	t.Helper()
	ledger := xdr.LedgerCloseMeta{
		V: 0,
		V0: &xdr.LedgerCloseMetaV0{
			LedgerHeader: xdr.LedgerHeaderHistoryEntry{
				Header: xdr.LedgerHeader{
					LedgerSeq: xdr.Uint32(sequence),
					ScpValue:  xdr.StellarValue{CloseTime: xdr.TimePoint(closeTime)},
				},
			},
		},
	}
	return mustMarshalXDR(t, ledger)
}

func fixtureContractEvent(topic string) xdr.ContractEvent {
	var contractID xdr.ContractId
	contractID[0] = 0xab
	symbol := xdr.ScSymbol(topic)
	return xdr.ContractEvent{
		ContractId: &contractID,
		Type:       xdr.ContractEventTypeContract,
		Body: xdr.ContractEventBody{V: 0, V0: &xdr.ContractEventV0{
			Topics: []xdr.ScVal{{Type: xdr.ScValTypeScvSymbol, Sym: &symbol}},
			Data:   xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &symbol},
		}},
	}
}

func mustMarshalXDR(t *testing.T, value interface{ MarshalBinary() ([]byte, error) }) []byte {
	t.Helper()
	raw, err := value.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal XDR: %v", err)
	}
	return raw
}
