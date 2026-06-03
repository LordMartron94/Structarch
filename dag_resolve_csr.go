package structarch

import (
	"fmt"
	"memarch"
	"memcore"
	"memstruct"
	"unsafe"
)

/*
DagNodeID is a dense graph node index for STRUCTARCH_DAG_ResolveCSR.

Use contiguous IDs in [0, nodeCount). External string keys must be mapped to these indices by the client.
*/
type DagNodeID uint32

/*
DagNodeCSR is one row of a compressed-sparse-row dependency graph.

EdgeOffset and EdgeCount index into the shared Edges array passed to STRUCTARCH_DAG_ResolveCSR.
Each edge ID is a dependency that must execute in an earlier phase than this node.
*/
type DagNodeCSR struct {
	EdgeOffset uint32
	EdgeCount  uint32
}

/*
DagResolveCSRResult holds a flat execution schedule and phase boundaries.

ExecutionOrder is a memstruct array mark (Array[DagNodeID]) of length ExecutionCount.
PhaseOffsets is a memstruct array mark (Array[uint32]) of length PhaseCount+1:
  - PhaseOffsets[k] is the start index in ExecutionOrder for phase k.
  - PhaseOffsets[PhaseCount] equals ExecutionCount (end sentinel).

The renderer may iterate ExecutionOrder linearly; consult PhaseOffsets only when inserting barriers between phases.
*/
type DagResolveCSRResult struct {
	ExecutionOrder memcore.MarkRaw
	ExecutionCount uint32
	PhaseOffsets   memcore.MarkRaw
	PhaseCount     uint32
}

/*
DagCycleErrorCSR reports a cycle discovered during STRUCTARCH_DAG_ResolveCSR.

Path is a memstruct array mark (Array[DagNodeID]) of length PathLength listing nodes on the closing path.
*/
type DagCycleErrorCSR struct {
	Path       memcore.MarkRaw
	PathLength uint32
}

func (c DagCycleErrorCSR) Error() string {
	if !memcore.MemcoreMarkIsValid(c.Path) || c.PathLength == 0 {
		return "DAG invariant violation: cycle detected"
	}

	var builder string
	for index := uint64(0); index < uint64(c.PathLength); index++ {
		if index > 0 {
			builder += " -> "
		}
		nodeID := memstruct.ArrayItemGetAtUnsafe[DagNodeID](c.Path, index)
		builder += fmt.Sprintf("%d", nodeID)
	}
	return fmt.Sprintf("DAG invariant violation: cycle detected; path = %s", builder)
}

const (
	dagCSRStateUnseen     int32 = -2
	dagCSRStateInProgress int32 = -1
)

/*
STRUCTARCH_DAG_ResolveCSR assigns phases and emits a flat execution order for a CSR DAG.

[Parameters]
allocFn allocates resolver scratch and output arrays.
nodeCount is the number of nodes (valid IDs are 0..nodeCount-1).
nodesMark is Array[DagNodeCSR] with capacity >= nodeCount.
edgesMark is Array[DagNodeID] holding all dependency edge targets packed by node CSR rows.

[Returns]
Fills result with ExecutionOrder and PhaseOffsets marks. Returns DagCycleErrorCSR on cycles.

[Complexity]
Time O(V + E). Space O(V) for state plus O(V) output arrays via allocFn.
*/
func STRUCTARCH_DAG_ResolveCSR(
	allocFn memarch.AllocationFn,
	nodeCount uint32,
	nodesMark memcore.MarkRaw,
	edgesMark memcore.MarkRaw,
	result *DagResolveCSRResult,
) error {
	if result == nil {
		return fmt.Errorf("cannot resolve DAG with nil result")
	}
	if allocFn == nil {
		return fmt.Errorf("cannot resolve DAG with nil alloc function")
	}

	result.ExecutionOrder = memcore.MarkRaw{}
	result.ExecutionCount = 0
	result.PhaseOffsets = memcore.MarkRaw{}
	result.PhaseCount = 0

	if nodeCount == 0 {
		return nil
	}

	if !memcore.MemcoreMarkIsValid(nodesMark) {
		return fmt.Errorf("DAG nodes mark is invalid")
	}
	if memstruct.ArrayCapacityGet[DagNodeCSR](nodesMark) < uint64(nodeCount) {
		return fmt.Errorf("DAG nodes array capacity is smaller than node count")
	}
	if !memcore.MemcoreMarkIsValid(edgesMark) {
		return fmt.Errorf("DAG edges mark is invalid")
	}

	edgeCapacity := memstruct.ArrayCapacityGet[DagNodeID](edgesMark)
	nodesBase, nodesInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[DagNodeCSR]](nodesMark)
	edgesBase, edgesInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[DagNodeID]](edgesMark)

	for nodeIndex := uint32(0); nodeIndex < nodeCount; nodeIndex++ {
		node := memstruct.ArrayItemGetAtUnsafeFast(nodesInst, nodesBase, uint64(nodeIndex))
		edgeEnd := uint64(node.EdgeOffset) + uint64(node.EdgeCount)
		if edgeEnd < uint64(node.EdgeOffset) || edgeEnd > edgeCapacity {
			return fmt.Errorf("DAG node %d edge range [%d,%d) exceeds edges array capacity %d",
				nodeIndex, node.EdgeOffset, node.EdgeOffset+node.EdgeCount, edgeCapacity)
		}
	}

	stateMark, _ := memarch.MemArchArrayCreate[int32](allocFn, uint64(nodeCount))
	stateBase, stateInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[int32]](stateMark)
	memstruct.ArraySetAllFast(stateInst, stateBase, dagCSRStateUnseen)

	cyclePathMark, _ := memarch.MemArchArrayCreate[DagNodeID](allocFn, uint64(nodeCount))

	var maxPhase int32 = -1
	for nodeID := uint32(0); nodeID < nodeCount; nodeID++ {
		phase, err := dagResolveCSRAssignPhase(
			nodeID,
			nodeCount,
			nodesBase,
			nodesInst,
			edgesBase,
			edgesInst,
			stateBase,
			stateInst,
			cyclePathMark,
			0,
		)
		if err != nil {
			return err
		}
		if phase > maxPhase {
			maxPhase = phase
		}
	}

	phaseCount := uint32(maxPhase + 1)
	phaseCountsMark, _ := memarch.MemArchArrayCreate[uint32](allocFn, uint64(phaseCount))
	phaseCountsBase, phaseCountsInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[uint32]](phaseCountsMark)

	for nodeID := uint32(0); nodeID < nodeCount; nodeID++ {
		phase := uint32(memstruct.ArrayItemGetAtUnsafeFast(stateInst, stateBase, uint64(nodeID)))
		if phase >= phaseCount {
			return fmt.Errorf("DAG node %d assigned invalid phase %d", nodeID, phase)
		}
		countIndex := uint64(phase)
		memstruct.ArraySetAtUnsafeFast(
			phaseCountsInst,
			phaseCountsBase,
			countIndex,
			memstruct.ArrayItemGetAtUnsafeFast(phaseCountsInst, phaseCountsBase, countIndex)+1,
		)
	}

	phaseOffsetsMark, _ := memarch.MemArchArrayCreate[uint32](allocFn, uint64(phaseCount)+1)
	phaseOffsetsBase, phaseOffsetsInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[uint32]](phaseOffsetsMark)
	memstruct.ArraySetAtUnsafeFast(phaseOffsetsInst, phaseOffsetsBase, 0, 0)

	runningOffset := uint32(0)
	for phaseIndex := uint32(0); phaseIndex < phaseCount; phaseIndex++ {
		memstruct.ArraySetAtUnsafeFast(phaseOffsetsInst, phaseOffsetsBase, uint64(phaseIndex), runningOffset)
		phaseSize := memstruct.ArrayItemGetAtUnsafeFast(phaseCountsInst, phaseCountsBase, uint64(phaseIndex))
		runningOffset += phaseSize
	}
	memstruct.ArraySetAtUnsafeFast(phaseOffsetsInst, phaseOffsetsBase, uint64(phaseCount), runningOffset)

	writeCursorMark, _ := memarch.MemArchArrayCreate[uint32](allocFn, uint64(phaseCount))
	writeCursorBase, writeCursorInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[uint32]](writeCursorMark)
	for phaseIndex := uint32(0); phaseIndex < phaseCount; phaseIndex++ {
		memstruct.ArraySetAtUnsafeFast(
			writeCursorInst,
			writeCursorBase,
			uint64(phaseIndex),
			memstruct.ArrayItemGetAtUnsafeFast(phaseOffsetsInst, phaseOffsetsBase, uint64(phaseIndex)),
		)
	}

	executionOrderMark, _ := memarch.MemArchArrayCreate[DagNodeID](allocFn, uint64(nodeCount))
	executionOrderBase, executionOrderInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[DagNodeID]](executionOrderMark)
	for nodeID := uint32(0); nodeID < nodeCount; nodeID++ {
		phase := uint32(memstruct.ArrayItemGetAtUnsafeFast(stateInst, stateBase, uint64(nodeID)))
		writeIndex := memstruct.ArrayItemGetAtUnsafeFast(writeCursorInst, writeCursorBase, uint64(phase))
		memstruct.ArraySetAtUnsafeFast(executionOrderInst, executionOrderBase, uint64(writeIndex), DagNodeID(nodeID))
		memstruct.ArraySetAtUnsafeFast(writeCursorInst, writeCursorBase, uint64(phase), writeIndex+1)
	}

	result.ExecutionOrder = executionOrderMark
	result.ExecutionCount = nodeCount
	result.PhaseOffsets = phaseOffsetsMark
	result.PhaseCount = phaseCount
	return nil
}

func dagResolveCSRAssignPhase(
	nodeID uint32,
	nodeCount uint32,
	nodesBase unsafe.Pointer,
	nodesInst *memstruct.Array[DagNodeCSR],
	edgesBase unsafe.Pointer,
	edgesInst *memstruct.Array[DagNodeID],
	stateBase unsafe.Pointer,
	stateInst *memstruct.Array[int32],
	cyclePathMark memcore.MarkRaw,
	stackDepth uint32,
) (int32, error) {
	nodeState := memstruct.ArrayItemGetAtUnsafeFast(stateInst, stateBase, uint64(nodeID))
	if nodeState != dagCSRStateUnseen {
		if nodeState == dagCSRStateInProgress {
			cyclePathBase, cyclePathInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[DagNodeID]](cyclePathMark)
			memstruct.ArraySetAtUnsafeFast(cyclePathInst, cyclePathBase, uint64(stackDepth), DagNodeID(nodeID))
			return -1, DagCycleErrorCSR{
				Path:       cyclePathMark,
				PathLength: stackDepth + 1,
			}
		}
		return nodeState, nil
	}

	memstruct.ArraySetAtUnsafeFast(stateInst, stateBase, uint64(nodeID), dagCSRStateInProgress)
	cyclePathBase, cyclePathInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[DagNodeID]](cyclePathMark)
	memstruct.ArraySetAtUnsafeFast(cyclePathInst, cyclePathBase, uint64(stackDepth), DagNodeID(nodeID))

	node := memstruct.ArrayItemGetAtUnsafeFast(nodesInst, nodesBase, uint64(nodeID))
	latestDependency := int32(-1)

	for edgeIndex := uint32(0); edgeIndex < node.EdgeCount; edgeIndex++ {
		dependencyID := memstruct.ArrayItemGetAtUnsafeFast(edgesInst, edgesBase, uint64(node.EdgeOffset+edgeIndex))
		if uint32(dependencyID) >= nodeCount {
			memstruct.ArraySetAtUnsafeFast(stateInst, stateBase, uint64(nodeID), dagCSRStateUnseen)
			return -1, fmt.Errorf("DAG node %d depends on out-of-range node %d", nodeID, dependencyID)
		}

		dependencyPhase, err := dagResolveCSRAssignPhase(
			uint32(dependencyID),
			nodeCount,
			nodesBase,
			nodesInst,
			edgesBase,
			edgesInst,
			stateBase,
			stateInst,
			cyclePathMark,
			stackDepth+1,
		)
		if err != nil {
			if cycleErr, ok := err.(DagCycleErrorCSR); ok {
				cycleErr.PathLength++
				cyclePathBase, cyclePathInst = memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[DagNodeID]](cycleErr.Path)
				memstruct.ArraySetAtUnsafeFast(cyclePathInst, cyclePathBase, uint64(cycleErr.PathLength-1), DagNodeID(nodeID))
				return -1, cycleErr
			}
			memstruct.ArraySetAtUnsafeFast(stateInst, stateBase, uint64(nodeID), dagCSRStateUnseen)
			return -1, err
		}

		if dependencyPhase > latestDependency {
			latestDependency = dependencyPhase
		}
	}

	currentPhase := latestDependency + 1
	memstruct.ArraySetAtUnsafeFast(stateInst, stateBase, uint64(nodeID), currentPhase)
	return currentPhase, nil
}
