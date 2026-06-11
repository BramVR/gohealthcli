package main

import (
	"testing"
)

// TestSupportedEndpointsMapMatchesPlannerForSteps is the tracer for
// #100: every existing Data Type's SupportedEndpoints map carries the
// endpoint family entries the planner used to look up via parallel
// booleans. Steps is the canonical multi-endpoint entry: list +
// reconcile + dailyRollUp.
func TestSupportedEndpointsMapMatchesPlannerForSteps(t *testing.T) {
	t.Parallel()
	entry, ok := googleHealthDataTypes.Lookup("steps")
	if !ok {
		t.Fatal("steps not in catalog")
	}
	for _, family := range []endpointFamily{
		endpointFamilyList,
		endpointFamilyReconcile,
		endpointFamilyDailyRollUp,
	} {
		if _, present := entry.SupportedEndpoints[family]; !present {
			t.Errorf("steps SupportedEndpoints missing %q", family)
		}
	}
	// list endpoint must carry the filter field — it used to live on
	// the entry as ListFilterField.
	if got := entry.SupportedEndpoints[endpointFamilyList].FilterField; got != "steps.interval.start_time" {
		t.Errorf("steps.list FilterField = %q, want steps.interval.start_time", got)
	}
}

// TestSupportedEndpointsCatalogConsistencyForEveryDataType pins the
// AC's drift guard: every catalog entry's SupportedEndpoints map agrees
// with what the legacy helpers used to return. Once the booleans are
// removed and helpers read from the map, this test guarantees the
// behaviour stays the same for the original 12 Data Types.
func TestSupportedEndpointsCatalogConsistencyForEveryDataType(t *testing.T) {
	t.Parallel()
	for _, dataType := range googleHealthDataTypes.order {
		dataType := dataType
		entry, _ := googleHealthDataTypes.Lookup(dataType)
		t.Run(dataType, func(t *testing.T) {
			_, hasList := entry.SupportedEndpoints[endpointFamilyList]
			_, hasReconcile := entry.SupportedEndpoints[endpointFamilyReconcile]
			_, hasDailyRollup := entry.SupportedEndpoints[endpointFamilyDailyRollUp]

			// Sync-supported types must have at least one of list/reconcile.
			if syncDataPointDataTypeSupported(dataType) && !hasList && !hasReconcile {
				t.Errorf("syncDataPointDataTypeSupported says yes but SupportedEndpoints has neither list nor reconcile")
			}
			// reconcileDataTypeSupported ↔ map has reconcile.
			if hasReconcile != reconcileDataTypeSupported(dataType) {
				t.Errorf("reconcileDataTypeSupported=%v but map has reconcile=%v",
					reconcileDataTypeSupported(dataType), hasReconcile)
			}
			// dailyRollupDataTypeSupported ↔ map has dailyRollUp.
			if hasDailyRollup != dailyRollupDataTypeSupported(dataType) {
				t.Errorf("dailyRollupDataTypeSupported=%v but map has dailyRollUp=%v",
					dailyRollupDataTypeSupported(dataType), hasDailyRollup)
			}
		})
	}
}

// TestSupportedEndpointsAddsFloorsTier1 pins the second AC: `floors`
// is the new interval-shaped Tier 1 Data Type and exercises the map's
// list + reconcile + dailyRollUp endpoints.
func TestSupportedEndpointsAddsFloorsTier1(t *testing.T) {
	t.Parallel()
	entry, ok := googleHealthDataTypes.Lookup("floors")
	if !ok {
		t.Fatal("floors not in catalog")
	}
	if entry.Parser != "interval" {
		t.Errorf("floors Parser = %q, want interval", entry.Parser)
	}
	for _, family := range []endpointFamily{
		endpointFamilyList,
		endpointFamilyReconcile,
		endpointFamilyDailyRollUp,
	} {
		if _, present := entry.SupportedEndpoints[family]; !present {
			t.Errorf("floors SupportedEndpoints missing %q", family)
		}
	}
}
