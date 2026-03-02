package structarch

import "fmt"

// =============================================================
// WALK STRATEGY
// =============================================================

/*
StructArchWalkStrategy defines the structural traversal order.

Traversal semantics:

	WALK_STRATEGY_PRE:
	  - visit node before its children (top-down)

	WALK_STRATEGY_POST:
	  - visit node after its children (bottom-up)

	WALK_STRATEGY_BREADTH:
	  - visit nodes level-by-level (breadth-first)

All strategies are cycle-safe and support pruning and early termination.
*/
type StructArchWalkStrategy int

const (
	WALK_STRATEGY_PRE StructArchWalkStrategy = iota + 1
	WALK_STRATEGY_POST
	WALK_STRATEGY_BREADTH
)

// =============================================================
// CALLBACK
// =============================================================

/*
WalkCallback is invoked when a node is visited.

Return values:

	skipProcessingNode:
	  - when true, the node's children are not traversed

	stopProcessing:
	  - when true, the entire walk terminates immediately

skipProcessingNode only affects subtree traversal.
stopProcessing halts the walk globally.
*/
type WalkCallback[TNode any] func(
	node TNode,
) (skipProcessingNode, stopProcessing bool)

// =============================================================
// WALK CONFIGURATION
// =============================================================

/*
WalkConfig defines the structural accessors and behavior for a walk.

Required fields:

	ID:
	  returns a stable unique identifier for a node
	  used for cycle detection and performance

	Children:
	  returns the direct child nodes of a node

	Callback:
	  invoked during traversal

Optional fields:

	Parent:
	  provided for future extensions (upward traversal, queries, etc.)
*/
type WalkConfig[TNode any, TNodeID comparable] struct {
	Strategy StructArchWalkStrategy

	Callback WalkCallback[TNode]

	ID func(node TNode) TNodeID

	Children func(node TNode) []TNode
	Parent   func(node TNode) TNode
}

// =============================================================
// PUBLIC WALK ENTRY
// =============================================================

/*
StructArchWalk traverses a structural hierarchy starting at rootNode.

Traversal is:

  - cycle-safe
  - supports subtree pruning
  - supports early termination

It works for trees, DAGs, and general node graphs.
*/
func StructArchWalk[TNode any, TNodeID comparable](
	config WalkConfig[TNode, TNodeID],
	rootNode TNode,
) error {
	if config.ID == nil {
		return fmt.Errorf("walk requires an ID function")
	}
	if config.Children == nil {
		return fmt.Errorf("walk requires a children collection function")
	}
	if config.Callback == nil {
		return fmt.Errorf("walk requires a callback function")
	}

	visited := make(map[TNodeID]struct{})

	switch config.Strategy {
	case WALK_STRATEGY_PRE:
		walkPre(rootNode, config, visited)
	case WALK_STRATEGY_POST:
		walkPost(rootNode, config, visited)
	case WALK_STRATEGY_BREADTH:
		walkBreadth(rootNode, config, visited)
	default:
		return fmt.Errorf("unknown walk strategy")
	}

	return nil
}

// =============================================================
// PRE-ORDER WALK (TOP-DOWN)
// =============================================================

func walkPre[TNode any, TNodeID comparable](
	node TNode,
	config WalkConfig[TNode, TNodeID],
	visited map[TNodeID]struct{},
) bool {
	id := config.ID(node)
	if _, seen := visited[id]; seen {
		return false
	}
	visited[id] = struct{}{}

	skipChildren, stop := config.Callback(node)
	if stop {
		return true
	}

	if skipChildren {
		return false
	}

	for _, child := range config.Children(node) {
		if walkPre(child, config, visited) {
			return true
		}
	}

	return false
}

// =============================================================
// WALK WITH INHERITED CONTEXT
// =============================================================

/*
WalkCallbackWithContext is invoked when a node is visited during a context-carrying walk.

Return values:

	childCtx:
	  context to pass to this node's children (inherited down the tree)

	skipSubtree:
	  when true, the node's children are not traversed

	stopWalk:
	  when true, the entire walk terminates immediately

Context flows top-down: the callback receives the parent's context and returns the context
for its children. Only pre-order traversal is defined for context walks.
*/
type WalkCallbackWithContext[TNode any, TContext any] func(
	node TNode,
	ctx TContext,
) (childCtx TContext, skipSubtree, stopWalk bool)

/*
WalkConfigWithContext defines the structural accessors and behavior for a walk that carries
inherited context. Strategy must be WALK_STRATEGY_PRE; context flows from root to children.
*/
type WalkConfigWithContext[TNode any, TNodeID comparable, TContext any] struct {
	ID       func(node TNode) TNodeID
	Children func(node TNode) []TNode
	Callback WalkCallbackWithContext[TNode, TContext]
}

/*
StructArchWalkWithContext traverses a structural hierarchy in pre-order, passing inherited
context from each node to its children. Cycle-safe; supports subtree pruning and early
termination. Use when traversal logic depends on context accumulated from ancestors
(e.g. recovery token sets, repeat nesting).
*/
func StructArchWalkWithContext[TNode any, TNodeID comparable, TContext any](
	config WalkConfigWithContext[TNode, TNodeID, TContext],
	rootNode TNode,
	initialContext TContext,
) error {
	if config.ID == nil {
		return fmt.Errorf("walk with context requires an ID function")
	}
	if config.Children == nil {
		return fmt.Errorf("walk with context requires a children collection function")
	}
	if config.Callback == nil {
		return fmt.Errorf("walk with context requires a callback function")
	}

	visited := make(map[TNodeID]struct{})
	walkPreWithContext(rootNode, initialContext, config, visited)
	return nil
}

func walkPreWithContext[TNode any, TNodeID comparable, TContext any](
	node TNode,
	ctx TContext,
	config WalkConfigWithContext[TNode, TNodeID, TContext],
	visited map[TNodeID]struct{},
) bool {
	id := config.ID(node)
	if _, seen := visited[id]; seen {
		return false
	}
	visited[id] = struct{}{}

	childCtx, skipChildren, stop := config.Callback(node, ctx)
	if stop {
		return true
	}
	if skipChildren {
		return false
	}

	for _, child := range config.Children(node) {
		if walkPreWithContext(child, childCtx, config, visited) {
			return true
		}
	}
	return false
}

// =============================================================
// POST-ORDER WALK (BOTTOM-UP)
// =============================================================

func walkPost[TNode any, TNodeID comparable](
	node TNode,
	config WalkConfig[TNode, TNodeID],
	visited map[TNodeID]struct{},
) bool {
	id := config.ID(node)
	if _, seen := visited[id]; seen {
		return false
	}
	visited[id] = struct{}{}

	for _, child := range config.Children(node) {
		if walkPost(child, config, visited) {
			return true
		}
	}

	_, stop := config.Callback(node)
	return stop
}

// =============================================================
// BREADTH-FIRST WALK
// =============================================================

func walkBreadth[TNode any, TNodeID comparable](
	root TNode,
	config WalkConfig[TNode, TNodeID],
	visited map[TNodeID]struct{},
) {
	queue := []TNode{root}

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		id := config.ID(node)
		if _, seen := visited[id]; seen {
			continue
		}
		visited[id] = struct{}{}

		skipChildren, stop := config.Callback(node)
		if stop {
			return
		}

		if skipChildren {
			continue
		}

		children := config.Children(node)
		if len(children) > 0 {
			queue = append(queue, children...)
		}
	}
}
