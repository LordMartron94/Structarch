/*
Package structarch provides cycle-safe traversal and dependency ordering over graphs.

[Context]
Consumers model trees or DAGs with caller-supplied ID and edge accessors. Walk APIs
visit nodes with optional pruning and early stop; the DAG resolver assigns
elements to parallelizable phases from a dependency adjacency map.

[Capabilities]
  - StructArchWalk / StructArchWalkWithContext: pre, post, and breadth walks with
    reusable scratch buffers.
  - STRUCTARCH_DAG_Resolve: topological phase grouping with cycle detection (map adjacency).
  - STRUCTARCH_DAG_ResolveCSR: dense CSR graph, flat execution order and phase offsets
    via caller allocFn (memarch/memstruct); for fixed-size graphs and manual-memory pipelines.
*/
package structarch
