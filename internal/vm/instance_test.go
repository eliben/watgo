package vm

import (
	"slices"
	"strings"
	"testing"

	"github.com/eliben/watgo/wasmir"
)

func TestTableAllocationLimit(t *testing.T) {
	for _, addressType := range []wasmir.ValueType{wasmir.ValueTypeI32, wasmir.ValueTypeI64} {
		t.Run(addressType.String(), func(t *testing.T) {
			def := wasmir.Table{AddressType: addressType, Min: maxTableElements + 1, RefType: wasmir.RefTypeExtern(true)}
			init := Value{Type: def.RefType, Ref: Reference{Kind: RefKindNull}}
			// Host-created and module-defined tables share the allocation bound.
			for name, create := range map[string]func() (*Table, error){
				"host":   func() (*Table, error) { return NewTable(def) },
				"module": func() (*Table, error) { return newTable(def, init) },
			} {
				t.Run(name, func(t *testing.T) {
					if table, err := create(); table != nil || err == nil || !strings.Contains(err.Error(), "table resource limit") {
						t.Fatalf("create oversized table = (%v, %v), want resource limit error", table, err)
					}
				})
			}
		})
	}
}

func TestTableGrowAllocationLimit(t *testing.T) {
	for _, addressType := range []wasmir.ValueType{wasmir.ValueTypeI32, wasmir.ValueTypeI64} {
		t.Run(addressType.String(), func(t *testing.T) {
			declaredMax := uint64(2 * maxTableElements)
			for name, max := range map[string]*uint64{"no maximum": nil, "large maximum": &declaredMax} {
				t.Run(name, func(t *testing.T) {
					table, err := NewTable(wasmir.Table{AddressType: addressType, Min: 2, Max: max, RefType: wasmir.RefTypeExtern(true)})
					if err != nil {
						t.Fatal(err)
					}
					inst := &Instance{tables: []*Table{table}}
					init := Value{Type: table.refType, Ref: Reference{Kind: RefKindExtern, ExternID: 7}}
					if err := inst.tableSet(0, 0, init); err != nil {
						t.Fatal(err)
					}
					before := slices.Clone(table.elems)
					// Include a total size just over the limit, the table64 regression
					// delta, and an addition that would overflow uint64.
					for _, delta := range []uint64{maxTableElements - 1, 1 << 32, ^uint64(0)} {
						old, ok, err := inst.tableGrow(0, init, delta)
						if err != nil || ok || old != 2 {
							t.Fatalf("grow(%d) = (%d, %v, %v), want (2, false, nil)", delta, old, ok, err)
						}
						if !slices.Equal(table.elems, before) {
							t.Fatal("failed growth changed the table")
						}
					}
					// A rejected allocation must leave ordinary growth usable.
					if old, ok, err := inst.tableGrow(0, init, 1); err != nil || !ok || old != 2 {
						t.Fatalf("grow(1) = (%d, %v, %v), want (2, true, nil)", old, ok, err)
					}
					if !slices.Equal(table.elems, append(before, init)) {
						t.Fatal("successful growth did not preserve and initialize elements")
					}
				})
			}
		})
	}
}

func TestTableAllocationLimitPreservesImportMaximum(t *testing.T) {
	declaredMax := uint64(2 * maxTableElements)
	for name, max := range map[string]*uint64{"no maximum": nil, "large maximum": &declaredMax} {
		t.Run(name, func(t *testing.T) {
			def := wasmir.Table{AddressType: wasmir.ValueTypeI64, Min: 1, Max: max, RefType: wasmir.RefTypeExtern(true)}
			table, err := NewTable(def)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkImportedTable(def, table); err != nil {
				t.Fatalf("matching import failed: %v", err)
			}
			// The allocation cap cannot stand in for a smaller declared maximum.
			importMax := uint64(maxTableElements)
			def.Max = &importMax
			if err := checkImportedTable(def, table); err == nil || !strings.Contains(err.Error(), "maximum size mismatch") {
				t.Fatalf("import with smaller maximum: got %v, want maximum size mismatch", err)
			}
		})
	}
}

func TestDroppedSegmentInitBounds(t *testing.T) {
	null := Value{Type: wasmir.RefTypeFunc(true), Ref: Reference{Kind: RefKindNull}}
	mem := &Memory{addressType: wasmir.ValueTypeI32, data: make([]byte, wasmPageSize)}
	table := &Table{addressType: wasmir.ValueTypeI32, refType: null.Type, elems: []Value{null, null}}
	inst := &Instance{
		memories: []*Memory{mem},
		tables:   []*Table{table},
		// Retained payloads must not make a dropped segment usable as a source.
		data:  []dataInst{{init: []byte{1, 2}, dropped: true}},
		elems: []elemInst{{values: []Value{null, null}, dropped: true}},
	}
	for _, op := range []struct {
		name string
		init func(uint32, uint32, uint64, uint64, uint64) error
		end  uint64
	}{
		{"memory", inst.memoryInit, uint64(len(mem.data))},
		{"table", inst.tableInit, uint64(len(table.elems))},
	} {
		t.Run(op.name, func(t *testing.T) {
			for _, tc := range []struct {
				name           string
				dst, src, size uint64
				wantErr        bool
			}{
				{"empty at start", 0, 0, 0, false},
				{"empty at destination end", op.end, 0, 0, false},
				{"source past end", 0, 1, 0, true},
				{"destination past end", op.end + 1, 0, 0, true},
				{"large destination", ^uint64(0), 0, 0, true},
				{"nonempty copy", 0, 0, 1, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					err := op.init(0, 0, tc.dst, tc.src, tc.size)
					if (err != nil) != tc.wantErr {
						t.Fatalf("init(%d, %d, %d) = %v, want error: %v", tc.dst, tc.src, tc.size, err, tc.wantErr)
					}
					if err != nil && !strings.Contains(err.Error(), "out of bounds") {
						t.Fatalf("expected bounds error, got %v", err)
					}
				})
			}
		})
	}
}
