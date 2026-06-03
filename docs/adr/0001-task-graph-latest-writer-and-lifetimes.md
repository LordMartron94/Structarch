# ADR 0001: Task graph latest-writer lowering and execution lifetimes

## Status

Accepted

## Context

Clients declare render (or compute) work as tasks with read/write resource dependencies. structarch must:

1. Lower bipartite task–resource dependencies to a task DAG and emit a parallel execution schedule.
2. Expose resource **lifetimes** suitable for memory aliasing (when the backing store must remain valid in execution order).

SPLASH [`frame_graph_system.go`](../../../splash/internal/graphical/rendering/frame_graph_system.go) already uses flat read/write pools and per-pass start/count partitioning. structarch mirrors that builder shape; domain metadata stays in SPLASH parallel arrays.

## Decision

### Builder: topology only

`TaskGraphBuilder` stores tasks, dependency pools, and dense `ResourceID` allocation. It does **not** store `ResourceLifetime`.

### DAG edges: registration-order `latestWriter`

During `STRUCTARCH_TaskGraph_Resolve`, scan tasks in **registration index order** (0 .. taskCount-1). Maintain `latestWriter[R]`:

1. For each read of `R` on task `T`, let `producer = latestWriter[R]`. If `producer` is unset, error (`noWriter`). If `producer != T`, add CSR edge `T → producer`.
2. After reads, for each write of `R` on `T`, set `latestWriter[R] = T`.

This is the **latest declared writer before `T`**, not the final writer after resolve. Registration order is the client's declaration order; the CSR resolver computes actual execution order.

**Do not** maintain `LastWriteTask` on register. Updating lifetimes at register time conflates declaration order with execution order and can attach reads to writers registered later (reverse dependencies).

### Lifetimes: post-CSR execution indices

After `STRUCTARCH_DAG_ResolveCSR`, walk `ExecutionOrder` from `0` to `ExecutionCount-1`. For each task's reads and writes, update:

```go
type ResourceLifetime struct {
    FirstAccessIndex uint32 // index in ExecutionOrder
    LastAccessIndex  uint32
}
```

Unset entries remain `ResourceAccessIndexNone`. These indices are what aliasing planners need—not `TaskID`.

### External reads

If a task reads `R` when `latestWriter[R]` is still unset at that task's scan position, resolve returns `TaskGraphResolveError` with `Reason: noWriter`.

### Lowered graph acyclicity

Edges only point from a task to a producer with a lower registration index, so the lowered graph is acyclic by construction. Cycles in client **declaration** that require reordering are out of scope; SPLASH must register producers before consumers in the dependency chain (or bootstrap writers first).

## Consequences

- SPLASH `FrameGraphResolve` can call `STRUCTARCH_TaskGraph_Resolve` and keep `RenderPassMetadata` / `ResourceMetadata` parallel to task/resource indices.
- Aliasing uses `TaskGraphResolveResult.ResourceLifetimes`, not builder state.
- ID width: structarch uses `uint32`; SPLASH may cast at the boundary.

## Alternatives considered

- Incremental `ResourceLifetime` on the builder: rejected (registration vs execution conflation).
- Using global `LastWriteTask` at resolve time: rejected (same trap as incremental register).
