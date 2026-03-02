# structarch

Structural walk over node trees and DAGs. Cycle-safe; supports subtree pruning and early termination.

- **StructArchWalk**: Strategy-based walk (pre, post, breadth). Use `WalkConfig` with `ID`, `Children`, `Callback`.
- **StructArchWalkWithContext**: Pre-order walk with inherited context. Callback receives `(node, ctx)` and returns `(childCtx, skip, stop)`. Use when traversal logic depends on context from ancestors (e.g. recovery token sets, repeat nesting).
