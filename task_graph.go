package structarch

import (
	"fmt"
	"memarch"
	"memcore"
	"memstruct"
	"unsafe"
)

/*
TaskID is a dense task index for STRUCTARCH_TaskGraph_Resolve.

Task indices are assigned in registration order: the first registered task is 0.
*/
type TaskID = DagNodeID

/*
ResourceID is a dense resource index allocated by TaskGraphResourceRegister.
*/
type ResourceID uint32

const (
	TaskIDNone     TaskID     = DagNodeID(^uint32(0))
	ResourceIDNone ResourceID = ResourceID(^uint32(0))
)

/*
Task partitions read and write dependency lists in the builder pools.

ReadDependencyStartIndex and WriteDependencyStartIndex index into the builder's flat
readDependencies and writeDependencies arrays respectively.
*/
type Task struct {
	ReadDependencyStartIndex  uint32
	ReadDependencyCount       uint32
	WriteDependencyStartIndex uint32
	WriteDependencyCount      uint32
}

/*
TaskRequest describes one batch registration entry.

ReadDependencies and WriteDependencies are memstruct array marks (Array[ResourceID]).
The logical dependency count is the array capacity (same convention as SPLASH frame graph).
*/
type TaskRequest struct {
	ReadDependencies  memcore.MarkRaw
	WriteDependencies memcore.MarkRaw
}

/*
TaskGraphBuilder stores bipartite task-resource topology before resolve.

The builder does not track resource lifetimes; execution spans are computed after CSR resolve.
*/
type TaskGraphBuilder struct {
	maxTasks     uint32
	maxResources uint32

	tasksMark  memcore.MarkRaw
	taskCursor uint32

	readDependenciesMark memcore.MarkRaw
	readDependencyCursor uint32

	writeDependenciesMark memcore.MarkRaw
	writeDependencyCursor uint32

	resourceCursor uint32
}

/*
TaskGraphRequiredBytesGet returns the byte size for a builder backing store.

Uses the same read/write pool heuristics as SPLASH FrameGraphRequiredBytesGet (8 reads and 3 writes per task slot).
*/
func TaskGraphRequiredBytesGet(maxTasks, maxResources uint64) uint64 {
	taskArraySize := memstruct.ArrayRequiredBytesGet[Task](maxTasks)
	readDependencyArraySize := memstruct.ArrayRequiredBytesGet[ResourceID](maxTasks * 8)
	writeDependencyArraySize := memstruct.ArrayRequiredBytesGet[ResourceID](maxTasks * 3)
	return taskArraySize + readDependencyArraySize + writeDependencyArraySize
}

/*
TaskGraphBuilderCreate allocates builder pools with the given caps.

allocFn must outlive the builder; the builder stores marks only.
*/
func TaskGraphBuilderCreate(allocFn memarch.AllocationFn, maxTasks, maxResources uint32) (*TaskGraphBuilder, error) {
	if allocFn == nil {
		return nil, fmt.Errorf("cannot create task graph builder with nil alloc function")
	}
	if maxTasks == 0 {
		return nil, fmt.Errorf("cannot create task graph builder with zero max tasks")
	}

	tasksMark, _ := memarch.MemArchArrayCreate[Task](allocFn, uint64(maxTasks))
	readDependenciesMark, _ := memarch.MemArchArrayCreate[ResourceID](allocFn, uint64(maxTasks)*8)
	writeDependenciesMark, _ := memarch.MemArchArrayCreate[ResourceID](allocFn, uint64(maxTasks)*3)

	return &TaskGraphBuilder{
		maxTasks:              maxTasks,
		maxResources:          maxResources,
		tasksMark:             tasksMark,
		readDependenciesMark:  readDependenciesMark,
		writeDependenciesMark: writeDependenciesMark,
	}, nil
}

/*
TaskGraphBuilderDestroy releases builder ownership from the caller.

Pool memory lifetime remains tied to allocFn; this function only nils the builder handle.
*/
func TaskGraphBuilderDestroy(builder *TaskGraphBuilder) {
	if builder == nil {
		return
	}
	*builder = TaskGraphBuilder{}
}

/*
TaskGraphResourceRegister allocates the next dense ResourceID.
*/
func TaskGraphResourceRegister(builder *TaskGraphBuilder) (ResourceID, error) {
	if builder == nil {
		return ResourceIDNone, fmt.Errorf("cannot register resource on nil builder")
	}
	if builder.resourceCursor >= builder.maxResources {
		return ResourceIDNone, fmt.Errorf("task graph resource cap %d exceeded", builder.maxResources)
	}

	resourceID := ResourceID(builder.resourceCursor)
	builder.resourceCursor++
	return resourceID, nil
}

/*
TaskGraphTasksRegister appends tasks and copies dependency lists into builder pools.

requestCount is the number of TaskRequest entries in requestsMark.
Returns Array[TaskID] marks for the registered task indices (length requestCount).
*/
func TaskGraphTasksRegister(
	builder *TaskGraphBuilder,
	requestsMark memcore.MarkRaw,
	requestCount uint32,
	allocFn memarch.AllocationFn,
) (memcore.MarkRaw, error) {
	if builder == nil {
		return memcore.MarkRaw{}, fmt.Errorf("cannot register tasks on nil builder")
	}
	if allocFn == nil {
		return memcore.MarkRaw{}, fmt.Errorf("cannot register tasks with nil alloc function")
	}
	if requestCount == 0 {
		outMark, _ := memarch.MemArchArrayCreate[TaskID](allocFn, 0)
		return outMark, nil
	}
	if !memcore.MemcoreMarkIsValid(requestsMark) {
		return memcore.MarkRaw{}, fmt.Errorf("task requests mark is invalid")
	}
	if uint64(builder.taskCursor)+uint64(requestCount) > uint64(builder.maxTasks) {
		return memcore.MarkRaw{}, fmt.Errorf("task graph task cap %d exceeded", builder.maxTasks)
	}

	requestCapacity := memstruct.ArrayCapacityGet[TaskRequest](requestsMark)
	if uint64(requestCount) > requestCapacity {
		return memcore.MarkRaw{}, fmt.Errorf("task request count %d exceeds requests array capacity %d", requestCount, requestCapacity)
	}

	outMark, _ := memarch.MemArchArrayCreate[TaskID](allocFn, uint64(requestCount))
	outBase, outInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[TaskID]](outMark)

	requestBase, requestInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[TaskRequest]](requestsMark)
	taskBase, taskInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[Task]](builder.tasksMark)
	readBase, readInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceID]](builder.readDependenciesMark)
	writeBase, writeInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceID]](builder.writeDependenciesMark)

	for index := uint32(0); index < requestCount; index++ {
		requestPtr := memstruct.ArrayItemPtrGetAtUnsafeFast(requestInst, requestBase, uint64(index))
		taskPtr := memstruct.ArrayItemPtrGetAtUnsafeFast(taskInst, taskBase, uint64(builder.taskCursor))

		var readCount uint32
		if memcore.MemcoreMarkIsValid(requestPtr.ReadDependencies) {
			readCount = uint32(memstruct.ArrayCapacityGet[ResourceID](requestPtr.ReadDependencies))
		}
		var writeCount uint32
		if memcore.MemcoreMarkIsValid(requestPtr.WriteDependencies) {
			writeCount = uint32(memstruct.ArrayCapacityGet[ResourceID](requestPtr.WriteDependencies))
		}

		if uint64(builder.readDependencyCursor)+uint64(readCount) > memstruct.ArrayCapacityGet[ResourceID](builder.readDependenciesMark) {
			return memcore.MarkRaw{}, fmt.Errorf("read dependency pool capacity exceeded")
		}
		if uint64(builder.writeDependencyCursor)+uint64(writeCount) > memstruct.ArrayCapacityGet[ResourceID](builder.writeDependenciesMark) {
			return memcore.MarkRaw{}, fmt.Errorf("write dependency pool capacity exceeded")
		}

		if readCount > 0 {
			if !memcore.MemcoreMarkIsValid(requestPtr.ReadDependencies) {
				return memcore.MarkRaw{}, fmt.Errorf("task %d read dependencies mark is invalid", builder.taskCursor)
			}
			reqReadBase, reqReadInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceID]](requestPtr.ReadDependencies)
			if err := memstruct.ArrayCopyFromRangeFast[ResourceID](
				readInst, readBase,
				reqReadInst, reqReadBase,
				0, uint64(readCount), uint64(builder.readDependencyCursor),
			); err != nil {
				return memcore.MarkRaw{}, fmt.Errorf("could not copy read dependencies for task %d: %w", builder.taskCursor, err)
			}
		}

		if writeCount > 0 {
			if !memcore.MemcoreMarkIsValid(requestPtr.WriteDependencies) {
				return memcore.MarkRaw{}, fmt.Errorf("task %d write dependencies mark is invalid", builder.taskCursor)
			}
			reqWriteBase, reqWriteInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceID]](requestPtr.WriteDependencies)
			if err := memstruct.ArrayCopyFromRangeFast[ResourceID](
				writeInst, writeBase,
				reqWriteInst, reqWriteBase,
				0, uint64(writeCount), uint64(builder.writeDependencyCursor),
			); err != nil {
				return memcore.MarkRaw{}, fmt.Errorf("could not copy write dependencies for task %d: %w", builder.taskCursor, err)
			}
		}

		if err := taskGraphValidateDependencies(
			builder,
			readInst, readBase,
			builder.readDependencyCursor, readCount,
			writeInst, writeBase,
			builder.writeDependencyCursor, writeCount,
		); err != nil {
			return memcore.MarkRaw{}, err
		}

		*taskPtr = Task{
			ReadDependencyStartIndex:  builder.readDependencyCursor,
			ReadDependencyCount:       readCount,
			WriteDependencyStartIndex: builder.writeDependencyCursor,
			WriteDependencyCount:      writeCount,
		}

		memstruct.ArraySetAtUnsafeFast(outInst, outBase, uint64(index), TaskID(builder.taskCursor))
		builder.taskCursor++
		builder.readDependencyCursor += readCount
		builder.writeDependencyCursor += writeCount
	}

	return outMark, nil
}

func taskGraphValidateDependencies(
	builder *TaskGraphBuilder,
	readInst *memstruct.Array[ResourceID],
	readBase unsafe.Pointer,
	readStart, readCount uint32,
	writeInst *memstruct.Array[ResourceID],
	writeBase unsafe.Pointer,
	writeStart, writeCount uint32,
) error {
	for index := uint32(0); index < readCount; index++ {
		resourceID := memstruct.ArrayItemGetAtUnsafeFast(readInst, readBase, uint64(readStart+index))
		if uint32(resourceID) >= builder.resourceCursor {
			return fmt.Errorf("read dependency resource %d is not registered", resourceID)
		}
	}
	for index := uint32(0); index < writeCount; index++ {
		resourceID := memstruct.ArrayItemGetAtUnsafeFast(writeInst, writeBase, uint64(writeStart+index))
		if uint32(resourceID) >= builder.resourceCursor {
			return fmt.Errorf("write dependency resource %d is not registered", resourceID)
		}
	}
	return nil
}

/*
TaskGraphReset clears builder cursors and pool contents for reuse.
*/
func TaskGraphReset(builder *TaskGraphBuilder) {
	if builder == nil {
		return
	}

	taskBase, taskInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[Task]](builder.tasksMark)
	readBase, readInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceID]](builder.readDependenciesMark)
	writeBase, writeInst := memcore.MemcoreMarkDereferenceObjectAltUnsafe[memstruct.Array[ResourceID]](builder.writeDependenciesMark)

	memstruct.ArrayClearFast(taskInst, taskBase)
	memstruct.ArrayClearFast(readInst, readBase)
	memstruct.ArrayClearFast(writeInst, writeBase)

	builder.taskCursor = 0
	builder.readDependencyCursor = 0
	builder.writeDependencyCursor = 0
	builder.resourceCursor = 0
}
