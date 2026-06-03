package structarch

import (
	"fmt"
	"memarch"
	"memcore"
	"memstruct"
	"unsafe"
)

/*
ResourceAccessIndexNone means a resource was not accessed in the resolved execution order.
*/
const ResourceAccessIndexNone uint32 = ^uint32(0)

/*
ResourceLifetime is the execution-order span of a resource after STRUCTARCH_TaskGraph_Resolve.

FirstAccessIndex and LastAccessIndex refer to positions in ExecutionOrder, not TaskID.
*/
type ResourceLifetime struct {
	FirstAccessIndex uint32
	LastAccessIndex  uint32
}

/*
TaskGraphResolveResult holds CSR schedule output and per-resource execution lifetimes.
*/
type TaskGraphResolveResult struct {
	ExecutionOrder    memcore.MarkRaw
	ExecutionCount    uint32
	PhaseOffsets      memcore.MarkRaw
	PhaseCount        uint32
	ResourceLifetimes memcore.MarkRaw
}

/*
TaskGraphResolveReason classifies STRUCTARCH_TaskGraph_Resolve failures.
*/
type TaskGraphResolveReason uint8

const (
	TaskGraphResolveReasonNoWriter TaskGraphResolveReason = iota
	TaskGraphResolveReasonInvalidMark
	TaskGraphResolveReasonCapacityExceeded
)

/*
TaskGraphResolveError reports a resolve failure with task and resource context.
*/
type TaskGraphResolveError struct {
	Task     TaskID
	Resource ResourceID
	Reason   TaskGraphResolveReason
}

func (e TaskGraphResolveError) Error() string {
	switch e.Reason {
	case TaskGraphResolveReasonNoWriter:
		return fmt.Sprintf("task graph resolve: task %d reads resource %d with no prior in-graph writer", e.Task, e.Resource)
	case TaskGraphResolveReasonInvalidMark:
		return fmt.Sprintf("task graph resolve: invalid mark for task %d resource %d", e.Task, e.Resource)
	case TaskGraphResolveReasonCapacityExceeded:
		return "task graph resolve: scratch or edge capacity exceeded"
	default:
		return fmt.Sprintf("task graph resolve: task %d resource %d reason %d", e.Task, e.Resource, e.Reason)
	}
}

/*
TaskGraphResolveScratchBytesGet estimates resolve scratch for latestWriter and per-task edge dedupe.
*/
func TaskGraphResolveScratchBytesGet(maxTasks, maxResources uint64) uint64 {
	latestWriterSize := memstruct.ArrayRequiredBytesGet[TaskID](maxResources)
	dedupeScratchSize := memstruct.ArrayRequiredBytesGet[TaskID](maxTasks)
	csrNodesSize := memstruct.ArrayRequiredBytesGet[DagNodeCSR](maxTasks)
	csrEdgesSize := memstruct.ArrayRequiredBytesGet[DagNodeID](maxTasks * maxTasks)
	return latestWriterSize + dedupeScratchSize + csrNodesSize + csrEdgesSize
}

/*
STRUCTARCH_TaskGraph_Resolve lowers bipartite dependencies to a task DAG, runs CSR resolve,
then computes execution-order resource lifetimes.

[Parameters]
builder must be populated via TaskGraphTasksRegister.
resolveAllocFn allocates scratch (latestWriter, CSR) and result arrays.
result is cleared then filled on success.

[Returns]
TaskGraphResolveError when a read has no prior writer in registration order.
DagCycleErrorCSR when the lowered task graph contains a cycle.
*/
func STRUCTARCH_TaskGraph_Resolve(
	builder *TaskGraphBuilder,
	resolveAllocFn memarch.AllocationFn,
	result *TaskGraphResolveResult,
) error {
	if builder == nil {
		return fmt.Errorf("cannot resolve nil task graph builder")
	}
	if resolveAllocFn == nil {
		return fmt.Errorf("cannot resolve task graph with nil alloc function")
	}
	if result == nil {
		return fmt.Errorf("cannot resolve task graph with nil result")
	}

	result.ExecutionOrder = memcore.MarkRaw{}
	result.ExecutionCount = 0
	result.PhaseOffsets = memcore.MarkRaw{}
	result.PhaseCount = 0
	result.ResourceLifetimes = memcore.MarkRaw{}

	taskCount := builder.taskCursor
	if taskCount == 0 {
		lifetimesMark, _ := memarch.MemArchArrayCreate[ResourceLifetime](resolveAllocFn, uint64(builder.maxResources))
		result.ResourceLifetimes = lifetimesMark
		return nil
	}

	latestWriterMark, _ := memarch.MemArchArrayCreate[TaskID](resolveAllocFn, uint64(builder.maxResources))
	latestWriterBase, latestWriterInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[TaskID]](latestWriterMark)
	for index := uint64(0); index < uint64(builder.maxResources); index++ {
		memstruct.ArraySetAtUnsafeFast(latestWriterInst, latestWriterBase, index, TaskIDNone)
	}

	dedupeMark, _ := memarch.MemArchArrayCreate[TaskID](resolveAllocFn, uint64(builder.maxTasks))
	dedupeBase, dedupeInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[TaskID]](dedupeMark)

	taskBase, taskInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[Task]](builder.tasksMark)
	readBase, readInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceID]](builder.readDependenciesMark)
	writeBase, writeInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceID]](builder.writeDependenciesMark)

	edgeCounts, err := taskGraphCountEdgesPerTask(
		builder, taskCount,
		taskInst, taskBase,
		readInst, readBase,
		writeInst, writeBase,
		latestWriterInst, latestWriterBase,
		dedupeInst, dedupeBase,
	)
	if err != nil {
		return err
	}

	totalEdges := uint64(0)
	for taskID := uint32(0); taskID < taskCount; taskID++ {
		totalEdges += uint64(edgeCounts[taskID])
	}
	maxEdges := uint64(taskCount) * uint64(taskCount)
	if totalEdges > maxEdges {
		return TaskGraphResolveError{Reason: TaskGraphResolveReasonCapacityExceeded}
	}

	nodesMark, _ := memarch.MemArchArrayCreate[DagNodeCSR](resolveAllocFn, uint64(taskCount))
	edgesMark, _ := memarch.MemArchArrayCreate[DagNodeID](resolveAllocFn, totalEdges)
	nodesBase, nodesInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[DagNodeCSR]](nodesMark)
	edgesBase, edgesInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[DagNodeID]](edgesMark)

	for index := uint64(0); index < uint64(builder.maxResources); index++ {
		memstruct.ArraySetAtUnsafeFast(latestWriterInst, latestWriterBase, index, TaskIDNone)
	}

	edgeCursor := uint32(0)
	for taskID := uint32(0); taskID < taskCount; taskID++ {
		task := memstruct.ArrayItemGetAtUnsafeFast(taskInst, taskBase, uint64(taskID))
		startOffset := edgeCursor

		dedupeCount, packErr := taskGraphPackTaskEdges(
			builder, taskID, task,
			readInst, readBase,
			writeInst, writeBase,
			latestWriterInst, latestWriterBase,
			dedupeInst, dedupeBase,
			edgesInst, edgesBase,
			edgeCursor,
		)
		if packErr != nil {
			return packErr
		}
		edgeCursor += dedupeCount

		memstruct.ArraySetAtUnsafeFast(nodesInst, nodesBase, uint64(taskID), DagNodeCSR{
			EdgeOffset: startOffset,
			EdgeCount:  dedupeCount,
		})
	}

	var csrResult DagResolveCSRResult
	if err := STRUCTARCH_DAG_ResolveCSR(
		resolveAllocFn,
		taskCount,
		nodesMark,
		edgesMark,
		&csrResult,
	); err != nil {
		return err
	}

	result.ExecutionOrder = csrResult.ExecutionOrder
	result.ExecutionCount = csrResult.ExecutionCount
	result.PhaseOffsets = csrResult.PhaseOffsets
	result.PhaseCount = csrResult.PhaseCount

	lifetimesMark, _ := memarch.MemArchArrayCreate[ResourceLifetime](resolveAllocFn, uint64(builder.maxResources))
	lifetimesBase, lifetimesInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceLifetime]](lifetimesMark)
	for index := uint64(0); index < uint64(builder.maxResources); index++ {
		memstruct.ArraySetAtUnsafeFast(lifetimesInst, lifetimesBase, index, ResourceLifetime{
			FirstAccessIndex: ResourceAccessIndexNone,
			LastAccessIndex:  ResourceAccessIndexNone,
		})
	}

	executionBase, executionInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[DagNodeID]](result.ExecutionOrder)
	for executionIndex := uint32(0); executionIndex < result.ExecutionCount; executionIndex++ {
		taskID := memstruct.ArrayItemGetAtUnsafeFast(executionInst, executionBase, uint64(executionIndex))
		task := memstruct.ArrayItemGetAtUnsafeFast(taskInst, taskBase, uint64(taskID))

		taskGraphTouchResourceLifetimes(
			lifetimesInst, lifetimesBase,
			readInst, readBase,
			task.ReadDependencyStartIndex, task.ReadDependencyCount,
			executionIndex,
		)
		taskGraphTouchResourceLifetimes(
			lifetimesInst, lifetimesBase,
			writeInst, writeBase,
			task.WriteDependencyStartIndex, task.WriteDependencyCount,
			executionIndex,
		)
	}

	result.ResourceLifetimes = lifetimesMark
	return nil
}

/*
taskGraphLoweredTaskDependenciesGet runs the registration-order latestWriter lowering and
returns deduped prerequisite TaskIDs per task. Used to validate edge construction in tests.
*/
func taskGraphLoweredTaskDependenciesGet(
	builder *TaskGraphBuilder,
	scratchAllocFn memarch.AllocationFn,
) ([][]TaskID, error) {
	taskCount := builder.taskCursor
	if taskCount == 0 {
		return nil, nil
	}

	latestWriterMark, _ := memarch.MemArchArrayCreate[TaskID](scratchAllocFn, uint64(builder.maxResources))
	latestWriterBase, latestWriterInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[TaskID]](latestWriterMark)
	for index := uint64(0); index < uint64(builder.maxResources); index++ {
		memstruct.ArraySetAtUnsafeFast(latestWriterInst, latestWriterBase, index, TaskIDNone)
	}

	dedupeMark, _ := memarch.MemArchArrayCreate[TaskID](scratchAllocFn, uint64(builder.maxTasks))
	dedupeBase, dedupeInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[TaskID]](dedupeMark)

	taskBase, taskInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[Task]](builder.tasksMark)
	readBase, readInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceID]](builder.readDependenciesMark)
	writeBase, writeInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceID]](builder.writeDependenciesMark)

	dependencies := make([][]TaskID, taskCount)
	for taskID := uint32(0); taskID < taskCount; taskID++ {
		task := memstruct.ArrayItemGetAtUnsafeFast(taskInst, taskBase, uint64(taskID))
		dedupeCount, err := taskGraphAccumulateTaskEdges(
			builder, taskID, task,
			readInst, readBase,
			writeInst, writeBase,
			latestWriterInst, latestWriterBase,
			dedupeInst, dedupeBase,
			nil, nil,
			0,
		)
		if err != nil {
			return nil, err
		}
		taskDeps := make([]TaskID, dedupeCount)
		for dedupeIndex := uint32(0); dedupeIndex < dedupeCount; dedupeIndex++ {
			taskDeps[dedupeIndex] = memstruct.ArrayItemGetAtUnsafeFast(dedupeInst, dedupeBase, uint64(dedupeIndex))
		}
		dependencies[taskID] = taskDeps
	}

	return dependencies, nil
}

func taskGraphCountEdgesPerTask(
	builder *TaskGraphBuilder,
	taskCount uint32,
	taskInst *memstruct.Array[Task],
	taskBase unsafe.Pointer,
	readInst *memstruct.Array[ResourceID],
	readBase unsafe.Pointer,
	writeInst *memstruct.Array[ResourceID],
	writeBase unsafe.Pointer,
	latestWriterInst *memstruct.Array[TaskID],
	latestWriterBase unsafe.Pointer,
	dedupeInst *memstruct.Array[TaskID],
	dedupeBase unsafe.Pointer,
) ([]uint32, error) {
	edgeCounts := make([]uint32, taskCount)

	for taskID := uint32(0); taskID < taskCount; taskID++ {
		task := memstruct.ArrayItemGetAtUnsafeFast(taskInst, taskBase, uint64(taskID))
		dedupeCount, err := taskGraphAccumulateTaskEdges(
			builder, taskID, task,
			readInst, readBase,
			writeInst, writeBase,
			latestWriterInst, latestWriterBase,
			dedupeInst, dedupeBase,
			nil, nil,
			0,
		)
		if err != nil {
			return nil, err
		}
		edgeCounts[taskID] = dedupeCount
	}

	return edgeCounts, nil
}

func taskGraphPackTaskEdges(
	builder *TaskGraphBuilder,
	taskID uint32,
	task Task,
	readInst *memstruct.Array[ResourceID],
	readBase unsafe.Pointer,
	writeInst *memstruct.Array[ResourceID],
	writeBase unsafe.Pointer,
	latestWriterInst *memstruct.Array[TaskID],
	latestWriterBase unsafe.Pointer,
	dedupeInst *memstruct.Array[TaskID],
	dedupeBase unsafe.Pointer,
	edgesInst *memstruct.Array[DagNodeID],
	edgesBase unsafe.Pointer,
	edgeCursor uint32,
) (uint32, error) {
	return taskGraphAccumulateTaskEdges(
		builder, taskID, task,
		readInst, readBase,
		writeInst, writeBase,
		latestWriterInst, latestWriterBase,
		dedupeInst, dedupeBase,
		edgesInst, edgesBase,
		edgeCursor,
	)
}

func taskGraphAccumulateTaskEdges(
	builder *TaskGraphBuilder,
	taskID uint32,
	task Task,
	readInst *memstruct.Array[ResourceID],
	readBase unsafe.Pointer,
	writeInst *memstruct.Array[ResourceID],
	writeBase unsafe.Pointer,
	latestWriterInst *memstruct.Array[TaskID],
	latestWriterBase unsafe.Pointer,
	dedupeInst *memstruct.Array[TaskID],
	dedupeBase unsafe.Pointer,
	edgesInst *memstruct.Array[DagNodeID],
	edgesBase unsafe.Pointer,
	edgeCursor uint32,
) (uint32, error) {
	dedupeCount := uint32(0)

	for readIndex := uint32(0); readIndex < task.ReadDependencyCount; readIndex++ {
		resourceID := memstruct.ArrayItemGetAtUnsafeFast(
			readInst, readBase,
			uint64(task.ReadDependencyStartIndex+readIndex),
		)
		if uint32(resourceID) >= builder.maxResources {
			return 0, TaskGraphResolveError{
				Task: TaskID(taskID), Resource: resourceID, Reason: TaskGraphResolveReasonInvalidMark,
			}
		}

		producer := memstruct.ArrayItemGetAtUnsafeFast(latestWriterInst, latestWriterBase, uint64(resourceID))
		if producer == TaskIDNone {
			return 0, TaskGraphResolveError{
				Task: TaskID(taskID), Resource: resourceID, Reason: TaskGraphResolveReasonNoWriter,
			}
		}
		if producer == TaskID(taskID) {
			continue
		}

		alreadyAdded := false
		for dedupeIndex := uint32(0); dedupeIndex < dedupeCount; dedupeIndex++ {
			if memstruct.ArrayItemGetAtUnsafeFast(dedupeInst, dedupeBase, uint64(dedupeIndex)) == producer {
				alreadyAdded = true
				break
			}
		}
		if !alreadyAdded {
			if uint64(dedupeCount) >= uint64(builder.maxTasks) {
				return 0, TaskGraphResolveError{Reason: TaskGraphResolveReasonCapacityExceeded}
			}
			memstruct.ArraySetAtUnsafeFast(dedupeInst, dedupeBase, uint64(dedupeCount), producer)
			if edgesInst != nil {
				memstruct.ArraySetAtUnsafeFast(edgesInst, edgesBase, uint64(edgeCursor+dedupeCount), DagNodeID(producer))
			}
			dedupeCount++
		}
	}

	for writeIndex := uint32(0); writeIndex < task.WriteDependencyCount; writeIndex++ {
		resourceID := memstruct.ArrayItemGetAtUnsafeFast(
			writeInst, writeBase,
			uint64(task.WriteDependencyStartIndex+writeIndex),
		)
		memstruct.ArraySetAtUnsafeFast(latestWriterInst, latestWriterBase, uint64(resourceID), TaskID(taskID))
	}

	return dedupeCount, nil
}

func taskGraphTouchResourceLifetimes(
	lifetimesInst *memstruct.Array[ResourceLifetime],
	lifetimesBase unsafe.Pointer,
	dependenciesInst *memstruct.Array[ResourceID],
	dependenciesBase unsafe.Pointer,
	dependencyStart, dependencyCount uint32,
	executionIndex uint32,
) {
	for index := uint32(0); index < dependencyCount; index++ {
		resourceID := memstruct.ArrayItemGetAtUnsafeFast(
			dependenciesInst, dependenciesBase,
			uint64(dependencyStart+index),
		)
		lifetimePtr := memstruct.ArrayItemPtrGetAtUnsafeFast(lifetimesInst, lifetimesBase, uint64(resourceID))
		if lifetimePtr.FirstAccessIndex == ResourceAccessIndexNone {
			lifetimePtr.FirstAccessIndex = executionIndex
		}
		lifetimePtr.LastAccessIndex = executionIndex
	}
}
