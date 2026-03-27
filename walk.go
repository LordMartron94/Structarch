package structarch

import "fmt"

// =============================================================
// WALK STRATEGY & CALLBACKS
// =============================================================

type StructArchWalkStrategy int

const (
	WALK_STRATEGY_PRE StructArchWalkStrategy = iota + 1
	WALK_STRATEGY_POST
	WALK_STRATEGY_BREADTH
)

/*
WalkCallback is invoked for each visited node during StructArchWalk.

Return values:
- skipProcessingNode: when true, the current node's subtree is skipped
- stopProcessing: when true, traversal stops immediately
*/
type WalkCallback[TNode any] func(
	node TNode,
) (skipProcessingNode, stopProcessing bool)

/*
WalkCallbackWithContext is invoked for each visited node during StructArchWalkWithContext.

Return values:
- childCtx: context value propagated to children of this node
- skipSubtree: when true, children are not visited
- stopWalk: when true, traversal stops immediately
*/
type WalkCallbackWithContext[TNode any, TContext any] func(
	node TNode,
	ctx TContext,
) (childCtx TContext, skipSubtree, stopWalk bool)

// =============================================================
// WALK CONFIGURATION & SCRATCHPADS
// =============================================================

/*
WalkConfig defines traversal configuration for StructArchWalk.

ChildrenInto must append the current node's children into out and return the resulting
slice. The walker reuses one scratch child buffer across nodes to avoid per-node
allocations in hot traversal paths.
*/
type WalkConfig[TNode any, TNodeID comparable] struct {
	Strategy     StructArchWalkStrategy
	Callback     WalkCallback[TNode]
	ID           func(node TNode) TNodeID
	ChildrenInto func(node TNode, out []TNode) []TNode
	Parent       func(node TNode) TNode
	Scratch      *WalkScratch[TNode, TNodeID]
}

/*
WalkScratch stores reusable memory for StructArchWalk.

Reuse this across repeated walks to reduce allocations.
*/
type WalkScratch[TNode any, TNodeID comparable] struct {
	Visited  map[TNodeID]struct{}
	Stack    []walkFrame[TNode]
	Queue    []TNode
	ChildBuf []TNode
}

type walkFrame[TNode any] struct {
	Node  TNode
	Phase uint8
}

/*
WalkConfigWithContext defines traversal configuration for StructArchWalkWithContext.

ChildrenInto must append the current node's children into out and return the resulting
slice. The walker reuses one scratch child buffer across nodes to avoid per-node
allocations in hot traversal paths.
*/
type WalkConfigWithContext[TNode any, TNodeID comparable, TContext any] struct {
	ID           func(node TNode) TNodeID
	ChildrenInto func(node TNode, out []TNode) []TNode
	Callback     WalkCallbackWithContext[TNode, TContext]
	Scratch      *WalkContextScratch[TNode, TNodeID, TContext]
}

/*
WalkContextScratch stores reusable memory for StructArchWalkWithContext.

Reuse this across repeated walks to reduce allocations.
*/
type WalkContextScratch[TNode any, TNodeID comparable, TContext any] struct {
	Visited  map[TNodeID]struct{}
	Stack    []ctxFrame[TNode, TContext]
	ChildBuf []TNode
}

type ctxFrame[TNode any, TContext any] struct {
	Node TNode
	Ctx  TContext
}

// =============================================================
// PUBLIC WALK ENTRY
// =============================================================

/*
StructArchWalk traverses a structure using the configured strategy.

The walk is cycle-safe by ID, supports subtree pruning and early stop signals, and uses
the caller-provided ChildrenInto collector to avoid per-node child-slice allocations.
*/
func StructArchWalk[TNode any, TNodeID comparable](
	config WalkConfig[TNode, TNodeID],
	rootNode TNode,
) error {
	if err := validateBasicConfig(config); err != nil {
		return err
	}

	scratch := prepareBasicScratch(config.Scratch)

	switch config.Strategy {
	case WALK_STRATEGY_PRE:
		walkPre(rootNode, config, scratch)
	case WALK_STRATEGY_POST:
		walkPost(rootNode, config, scratch)
	case WALK_STRATEGY_BREADTH:
		walkBreadth(rootNode, config, scratch)
	default:
		return fmt.Errorf("unknown walk strategy")
	}

	return nil
}

/*
StructArchWalkWithContext traverses a structure in pre-order while propagating context.

The callback returns the context for children, plus subtree-prune and early-stop flags.
Traversal is cycle-safe by ID and uses ChildrenInto with reusable scratch memory.
*/
func StructArchWalkWithContext[TNode any, TNodeID comparable, TContext any](
	config WalkConfigWithContext[TNode, TNodeID, TContext],
	rootNode TNode,
	initialContext TContext,
) error {
	if config.ID == nil || config.Callback == nil {
		return fmt.Errorf("walk with context requires ID and Callback functions")
	}
	if config.ChildrenInto == nil {
		return fmt.Errorf("walk with context requires ChildrenInto")
	}

	scratch := prepareContextScratch(config.Scratch)
	walkPreWithContext(rootNode, initialContext, config, scratch)
	return nil
}

// =============================================================
// CORE WALK IMPLEMENTATIONS
// =============================================================

func walkPre[TNode any, TNodeID comparable](
	root TNode,
	config WalkConfig[TNode, TNodeID],
	scratch *WalkScratch[TNode, TNodeID],
) {
	scratch.Stack = append(scratch.Stack, walkFrame[TNode]{Node: root})

	for len(scratch.Stack) > 0 {
		if processPreNode(config, scratch) {
			return
		}
	}
}

func processPreNode[TNode any, TNodeID comparable](
	config WalkConfig[TNode, TNodeID],
	scratch *WalkScratch[TNode, TNodeID],
) bool {
	topIdx := len(scratch.Stack) - 1
	frame := scratch.Stack[topIdx]
	scratch.Stack = scratch.Stack[:topIdx]

	if markVisited(config.ID, frame.Node, scratch.Visited) {
		return false
	}

	skipChildren, stop := config.Callback(frame.Node)
	if stop {
		return true
	}
	if skipChildren {
		return false
	}

	scratch.ChildBuf = config.ChildrenInto(frame.Node, scratch.ChildBuf[:0])
	pushChildrenToStack(scratch.ChildBuf, &scratch.Stack, 0)
	return false
}

func walkPost[TNode any, TNodeID comparable](
	root TNode,
	config WalkConfig[TNode, TNodeID],
	scratch *WalkScratch[TNode, TNodeID],
) {
	scratch.Stack = append(scratch.Stack, walkFrame[TNode]{Node: root, Phase: 0})

	for len(scratch.Stack) > 0 {
		if processPostNode(config, scratch) {
			return
		}
	}
}

func processPostNode[TNode any, TNodeID comparable](
	config WalkConfig[TNode, TNodeID],
	scratch *WalkScratch[TNode, TNodeID],
) bool {
	topIdx := len(scratch.Stack) - 1
	frame := scratch.Stack[topIdx]
	scratch.Stack = scratch.Stack[:topIdx]

	if frame.Phase == 1 {
		_, stop := config.Callback(frame.Node)
		return stop
	}

	if markVisited(config.ID, frame.Node, scratch.Visited) {
		return false
	}

	scratch.Stack = append(scratch.Stack, walkFrame[TNode]{Node: frame.Node, Phase: 1})
	scratch.ChildBuf = config.ChildrenInto(frame.Node, scratch.ChildBuf[:0])
	pushChildrenToStack(scratch.ChildBuf, &scratch.Stack, 0)
	return false
}

func walkBreadth[TNode any, TNodeID comparable](
	root TNode,
	config WalkConfig[TNode, TNodeID],
	scratch *WalkScratch[TNode, TNodeID],
) {
	scratch.Queue = append(scratch.Queue, root)

	for len(scratch.Queue) > 0 {
		if processBreadthNode(config, scratch) {
			break
		}
	}

	// Prevent memory leak by clearing references in the underlying array
	scratch.Queue = scratch.Queue[:0]
}

func processBreadthNode[TNode any, TNodeID comparable](
	config WalkConfig[TNode, TNodeID],
	scratch *WalkScratch[TNode, TNodeID],
) bool {
	node := scratch.Queue[0]
	scratch.Queue = scratch.Queue[1:]

	if markVisited(config.ID, node, scratch.Visited) {
		return false
	}

	skipChildren, stop := config.Callback(node)
	if stop {
		return true
	}
	if skipChildren {
		return false
	}

	scratch.ChildBuf = config.ChildrenInto(node, scratch.ChildBuf[:0])
	scratch.Queue = append(scratch.Queue, scratch.ChildBuf...)
	return false
}

func walkPreWithContext[TNode any, TNodeID comparable, TContext any](
	root TNode,
	initialCtx TContext,
	config WalkConfigWithContext[TNode, TNodeID, TContext],
	scratch *WalkContextScratch[TNode, TNodeID, TContext],
) {
	scratch.Stack = append(scratch.Stack, ctxFrame[TNode, TContext]{Node: root, Ctx: initialCtx})

	for len(scratch.Stack) > 0 {
		if processContextNode(config, scratch) {
			return
		}
	}
}

func processContextNode[TNode any, TNodeID comparable, TContext any](
	config WalkConfigWithContext[TNode, TNodeID, TContext],
	scratch *WalkContextScratch[TNode, TNodeID, TContext],
) bool {
	topIdx := len(scratch.Stack) - 1
	frame := scratch.Stack[topIdx]
	scratch.Stack = scratch.Stack[:topIdx]

	if markVisited(config.ID, frame.Node, scratch.Visited) {
		return false
	}

	childCtx, skipChildren, stop := config.Callback(frame.Node, frame.Ctx)
	if stop {
		return true
	}
	if skipChildren {
		return false
	}

	scratch.ChildBuf = config.ChildrenInto(frame.Node, scratch.ChildBuf[:0])
	for i := len(scratch.ChildBuf) - 1; i >= 0; i-- {
		scratch.Stack = append(scratch.Stack, ctxFrame[TNode, TContext]{
			Node: scratch.ChildBuf[i],
			Ctx:  childCtx,
		})
	}
	return false
}

// =============================================================
// HELPER METHODS
// =============================================================

func validateBasicConfig[TNode any, TNodeID comparable](config WalkConfig[TNode, TNodeID]) error {
	if config.ID == nil || config.Callback == nil {
		return fmt.Errorf("walk requires ID and Callback functions")
	}
	if config.ChildrenInto == nil {
		return fmt.Errorf("walk requires ChildrenInto")
	}
	return nil
}

func prepareBasicScratch[TNode any, TNodeID comparable](
	provided *WalkScratch[TNode, TNodeID],
) *WalkScratch[TNode, TNodeID] {
	if provided == nil {
		provided = &WalkScratch[TNode, TNodeID]{}
	}
	if provided.Visited == nil {
		provided.Visited = make(map[TNodeID]struct{})
	} else {
		clear(provided.Visited)
	}
	provided.Stack = provided.Stack[:0]
	provided.Queue = provided.Queue[:0]
	return provided
}

func prepareContextScratch[TNode any, TNodeID comparable, TContext any](
	provided *WalkContextScratch[TNode, TNodeID, TContext],
) *WalkContextScratch[TNode, TNodeID, TContext] {
	if provided == nil {
		provided = &WalkContextScratch[TNode, TNodeID, TContext]{}
	}
	if provided.Visited == nil {
		provided.Visited = make(map[TNodeID]struct{})
	} else {
		clear(provided.Visited)
	}
	provided.Stack = provided.Stack[:0]
	return provided
}

func markVisited[TNode any, TNodeID comparable](
	idFn func(TNode) TNodeID,
	node TNode,
	visited map[TNodeID]struct{},
) bool {
	id := idFn(node)
	if _, seen := visited[id]; seen {
		return true
	}
	visited[id] = struct{}{}
	return false
}

func pushChildrenToStack[TNode any](
	children []TNode,
	stack *[]walkFrame[TNode],
	phase uint8,
) {
	for i := len(children) - 1; i >= 0; i-- {
		*stack = append(*stack, walkFrame[TNode]{Node: children[i], Phase: phase})
	}
}
