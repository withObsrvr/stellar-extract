package extract

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// The ledger-cost setting is the one a capacity meter divides by. Swapping
// read for write, or ledger-wide for per-transaction, produces a percentage
// that looks entirely reasonable and is wrong — nothing downstream can catch
// it, so it is pinned here.
func TestLedgerCostSettingMapsEachLimitToItsOwnField(t *testing.T) {
	entry := &xdr.ConfigSettingEntry{
		ConfigSettingId: xdr.ConfigSettingIdConfigSettingContractLedgerCostV0,
		ContractLedgerCost: &xdr.ConfigSettingContractLedgerCostV0{
			LedgerMaxDiskReadEntries:    200,
			LedgerMaxDiskReadBytes:      133120,
			LedgerMaxWriteLedgerEntries: 125,
			LedgerMaxWriteBytes:         66560,
			TxMaxDiskReadEntries:        40,
			TxMaxDiskReadBytes:          200000,
			TxMaxWriteLedgerEntries:     25,
			TxMaxWriteBytes:             66560,
		},
	}

	var data ConfigSettingData
	parseConfigSettingFields(entry, &data)

	for _, tc := range []struct {
		name string
		got  *uint32
		want uint32
	}{
		{"LedgerMaxReadLedgerEntries", data.LedgerMaxReadLedgerEntries, 200},
		{"LedgerMaxReadBytes", data.LedgerMaxReadBytes, 133120},
		{"LedgerMaxWriteLedgerEntries", data.LedgerMaxWriteLedgerEntries, 125},
		{"LedgerMaxWriteBytes", data.LedgerMaxWriteBytes, 66560},
		{"TxMaxReadLedgerEntries", data.TxMaxReadLedgerEntries, 40},
		{"TxMaxReadBytes", data.TxMaxReadBytes, 200000},
		{"TxMaxWriteLedgerEntries", data.TxMaxWriteLedgerEntries, 25},
		{"TxMaxWriteBytes", data.TxMaxWriteBytes, 66560},
	} {
		if tc.got == nil {
			t.Errorf("%s is nil, want %d", tc.name, tc.want)
			continue
		}
		if *tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, *tc.got, tc.want)
		}
	}

	// A ledger-cost entry says nothing about compute.
	if data.LedgerMaxInstructions != nil {
		t.Errorf("LedgerMaxInstructions = %d, want nil — that value lives in a different setting entry",
			*data.LedgerMaxInstructions)
	}
}

func TestComputeSettingMapsInstructionsAndMemory(t *testing.T) {
	entry := &xdr.ConfigSettingEntry{
		ConfigSettingId: xdr.ConfigSettingIdConfigSettingContractComputeV0,
		ContractCompute: &xdr.ConfigSettingContractComputeV0{
			LedgerMaxInstructions:           500000000,
			TxMaxInstructions:               100000000,
			FeeRatePerInstructionsIncrement: 25,
			TxMemoryLimit:                   41943040,
		},
	}

	var data ConfigSettingData
	parseConfigSettingFields(entry, &data)

	if data.LedgerMaxInstructions == nil || *data.LedgerMaxInstructions != 500000000 {
		t.Errorf("LedgerMaxInstructions = %v, want 500000000", data.LedgerMaxInstructions)
	}
	if data.TxMaxInstructions == nil || *data.TxMaxInstructions != 100000000 {
		t.Errorf("TxMaxInstructions = %v, want 100000000", data.TxMaxInstructions)
	}
	if data.FeeRatePerInstructionsIncrement == nil || *data.FeeRatePerInstructionsIncrement != 25 {
		t.Errorf("FeeRatePerInstructionsIncrement = %v, want 25", data.FeeRatePerInstructionsIncrement)
	}
	if data.TxMemoryLimit == nil || *data.TxMemoryLimit != 41943040 {
		t.Errorf("TxMemoryLimit = %v, want 41943040", data.TxMemoryLimit)
	}
}

// CAP-0062's per-transaction footprint cap arrives in its own entry, separate
// from the ledger-wide limits.
func TestLedgerCostExtSettingMapsFootprintCap(t *testing.T) {
	entry := &xdr.ConfigSettingEntry{
		ConfigSettingId: xdr.ConfigSettingIdConfigSettingContractLedgerCostExtV0,
		ContractLedgerCostExt: &xdr.ConfigSettingContractLedgerCostExtV0{
			TxMaxFootprintEntries: 100,
			FeeWrite1Kb:           1000,
		},
	}

	var data ConfigSettingData
	parseConfigSettingFields(entry, &data)

	if data.TxMaxFootprintEntries == nil || *data.TxMaxFootprintEntries != 100 {
		t.Errorf("TxMaxFootprintEntries = %v, want 100", data.TxMaxFootprintEntries)
	}
}

func TestMaxSizeSettingMapsContractSize(t *testing.T) {
	size := xdr.Uint32(65536)
	entry := &xdr.ConfigSettingEntry{
		ConfigSettingId:      xdr.ConfigSettingIdConfigSettingContractMaxSizeBytes,
		ContractMaxSizeBytes: &size,
	}

	var data ConfigSettingData
	parseConfigSettingFields(entry, &data)

	if data.ContractMaxSizeBytes == nil || *data.ContractMaxSizeBytes != 65536 {
		t.Errorf("ContractMaxSizeBytes = %v, want 65536", data.ContractMaxSizeBytes)
	}
}

// Settings this parser does not promote must leave every field alone rather
// than writing a zero that reads as a real limit of zero.
func TestUnhandledSettingLeavesEveryFieldNil(t *testing.T) {
	entry := &xdr.ConfigSettingEntry{
		ConfigSettingId: xdr.ConfigSettingIdConfigSettingContractBandwidthV0,
		ContractBandwidth: &xdr.ConfigSettingContractBandwidthV0{
			LedgerMaxTxsSizeBytes: 100000,
			TxMaxSizeBytes:        71680,
			FeeTxSize1Kb:          1000,
		},
	}

	var data ConfigSettingData
	parseConfigSettingFields(entry, &data)

	if data.LedgerMaxWriteLedgerEntries != nil {
		t.Errorf("LedgerMaxWriteLedgerEntries = %d, want nil", *data.LedgerMaxWriteLedgerEntries)
	}
	if data.ContractMaxSizeBytes != nil {
		t.Errorf("ContractMaxSizeBytes = %d, want nil", *data.ContractMaxSizeBytes)
	}
}

func TestNilEntryIsNotADereference(t *testing.T) {
	var data ConfigSettingData
	parseConfigSettingFields(nil, &data)
	if data.LedgerMaxWriteLedgerEntries != nil {
		t.Error("a nil entry populated a field")
	}
}
