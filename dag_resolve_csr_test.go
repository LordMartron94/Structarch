package structarch

import (
	"memarch"
	"memcore"
	"memforge"
	"memstruct"
	"testing"
)

func testDagAllocFn(t *testing.T) memarch.AllocationFn {
	t.Helper()
	allocator := memforge.FixedLinearAllocatorCreate(uint64(64*memcore.KiloByte), "structarch dag test")
	return func(sizeBytes, alignment uint64) memcore.MarkRaw {
		return memforge.FixedLinearAllocatorMalloc(allocator, sizeBytes, alignment)
	}
}

func dagCSRBuildChainABC(t *testing.T, allocFn memarch.AllocationFn) (memcore.MarkRaw, memcore.MarkRaw) {
	t.Helper()
	// C -> B -> A  (A depends on B, B depends on C)
	const nodeCount = 3
	nodesMark, _ := memarch.MemArchArrayCreate[DagNodeCSR](allocFn, nodeCount)
	edgesMark, _ := memarch.MemArchArrayCreate[DagNodeID](allocFn, nodeCount)

	memstruct.ArraySetAtUnsafe(nodesMark, 0, DagNodeCSR{EdgeOffset: 0, EdgeCount: 1})
	memstruct.ArraySetAtUnsafe(nodesMark, 1, DagNodeCSR{EdgeOffset: 1, EdgeCount: 1})
	memstruct.ArraySetAtUnsafe(nodesMark, 2, DagNodeCSR{EdgeOffset: 2, EdgeCount: 0})

	memstruct.ArraySetAtUnsafe(edgesMark, 0, DagNodeID(1)) // A -> B
	memstruct.ArraySetAtUnsafe(edgesMark, 1, DagNodeID(2)) // B -> C

	return nodesMark, edgesMark
}

func dagCSRPhaseOfNode(result DagResolveCSRResult, nodeID DagNodeID) (uint32, bool) {
	for index := uint64(0); index < uint64(result.PhaseCount); index++ {
		start := memstruct.ArrayItemGetAtUnsafe[uint32](result.PhaseOffsets, index)
		end := memstruct.ArrayItemGetAtUnsafe[uint32](result.PhaseOffsets, index+1)
		for slot := start; slot < end; slot++ {
			if memstruct.ArrayItemGetAtUnsafe[DagNodeID](result.ExecutionOrder, uint64(slot)) == nodeID {
				return uint32(index), true
			}
		}
	}
	return 0, false
}

func TestSTRUCTARCH_DAG_ResolveCSR_ChainMatchesMapPhases(t *testing.T) {
	allocFn := testDagAllocFn(t)
	nodesMark, edgesMark := dagCSRBuildChainABC(t, allocFn)

	var csrResult DagResolveCSRResult
	if err := STRUCTARCH_DAG_ResolveCSR(allocFn, 3, nodesMark, edgesMark, &csrResult); err != nil {
		t.Fatalf("csr resolve: %v", err)
	}

	if csrResult.ExecutionCount != 3 || csrResult.PhaseCount != 3 {
		t.Fatalf("csr counts: execution=%d phases=%d", csrResult.ExecutionCount, csrResult.PhaseCount)
	}

	endOffset := memstruct.ArrayItemGetAtUnsafe[uint32](csrResult.PhaseOffsets, 3)
	if endOffset != 3 {
		t.Fatalf("phase end sentinel: got %d want 3", endOffset)
	}

	// C phase 0, B phase 1, A phase 2
	for nodeID, wantPhase := range map[DagNodeID]uint32{2: 0, 1: 1, 0: 2} {
		phase, ok := dagCSRPhaseOfNode(csrResult, nodeID)
		if !ok {
			t.Fatalf("node %d missing from execution order", nodeID)
		}
		if phase != wantPhase {
			t.Fatalf("node %d phase: got %d want %d", nodeID, phase, wantPhase)
		}
	}

	mapGroups, err := STRUCTARCH_DAG_Resolve(map[string][]string{
		"A": {"B"},
		"B": {"C"},
		"C": nil,
	})
	if err != nil {
		t.Fatalf("map resolve: %v", err)
	}

	mapPhase := map[string]uint32{}
	for phaseIndex, group := range mapGroups {
		for _, id := range group {
			mapPhase[id] = uint32(phaseIndex)
		}
	}

	csrPhaseA, _ := dagCSRPhaseOfNode(csrResult, 0)
	if csrPhaseA != mapPhase["A"] {
		t.Fatalf("A phase csr=%d map=%d", csrPhaseA, mapPhase["A"])
	}
}

func TestSTRUCTARCH_DAG_ResolveCSR_Cycle(t *testing.T) {
	allocFn := testDagAllocFn(t)
	const nodeCount = 2
	nodesMark, _ := memarch.MemArchArrayCreate[DagNodeCSR](allocFn, nodeCount)
	edgesMark, _ := memarch.MemArchArrayCreate[DagNodeID](allocFn, nodeCount)

	memstruct.ArraySetAtUnsafe(nodesMark, 0, DagNodeCSR{EdgeOffset: 0, EdgeCount: 1})
	memstruct.ArraySetAtUnsafe(nodesMark, 1, DagNodeCSR{EdgeOffset: 1, EdgeCount: 1})
	memstruct.ArraySetAtUnsafe(edgesMark, 0, DagNodeID(1))
	memstruct.ArraySetAtUnsafe(edgesMark, 1, DagNodeID(0))

	var csrResult DagResolveCSRResult
	err := STRUCTARCH_DAG_ResolveCSR(allocFn, nodeCount, nodesMark, edgesMark, &csrResult)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if _, ok := err.(DagCycleErrorCSR); !ok {
		t.Fatalf("expected DagCycleErrorCSR, got %T", err)
	}
}
