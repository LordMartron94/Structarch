# structarch

Structural walk over node trees and DAGs. Cycle-safe; supports subtree pruning and early termination.

- **StructArchWalk**: Strategy-based walk (pre, post, breadth). Use `WalkConfig` with `ID`, `ChildrenInto`, `Callback`.
- **StructArchWalkWithContext**: Pre-order walk with inherited context. Callback receives `(node, ctx)` and returns `(childCtx, skip, stop)`. Use when traversal logic depends on context from ancestors (e.g. recovery token sets, repeat nesting).
- **STRUCTARCH_DAG_Resolve**: Map-based adjacency (`map[TID][]TID`) returning phased groups `[][]TID` for prototyping and tests.
- **STRUCTARCH_DAG_ResolveCSR**: Dense `DagNodeID` (`uint32`) graph in CSR form (`DagNodeCSR` rows + flat `edges` array). Outputs a flat `ExecutionOrder` and `PhaseOffsets` via `memarch`/`memstruct` marks—no hash maps or jagged slices. Intended for graphs with a known node cap and manual-memory clients.
