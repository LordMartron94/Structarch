package structarch

import (
	"fmt"
	"foundation/formatting"
	"slices"
)

/*
CycleError reports a dependency cycle discovered while resolving a DAG.

[Context]
Returned from STRUCTARCH_DAG_Resolve when depth-first resolution revisits a node
still marked in-progress. The stored path is built during unwind; Error reverses
it for display so the message reads dependency-first along the closing edge.

[Invariants]
path is non-empty and lists each node ID on the cycle once, in unwind order.
*/
type CycleError[TID comparable] struct {
	path []TID
}

/*
Error formats the cycle as a human-readable dependency chain.

[Returns]
A string of the form `DAG invariant violation: cycle detected; path = ...` where
each ID is separated by ` -> ` in dependency order (from prerequisite toward the
node that closed the cycle).

[Side Effects]
Pure function. Does not mutate the receiver's path slice.
*/
func (c CycleError[TID]) Error() string {
	cp := slices.Clone(c.path)
	slices.Reverse(cp)

	formatter := formatting.FormatSliceOptions[TID]{
		Separator: " -> ",
		Prefix:    "",
		Suffix:    "",
		FormatItem: func(index int, value TID) string {
			return fmt.Sprintf("%v", value)
		},
	}

	return fmt.Sprintf("DAG invariant violation: cycle detected; path = %s", formatting.FormatSlice(cp, formatter))
}

/*
STRUCTARCH_DAG_Resolve partitions elements into ordered dependency phases.

[Context]
Callers supply a directed acyclic graph as an adjacency map: each key is an
element ID and its slice lists IDs that must appear in an earlier phase before
the key may run. The result groups elements that may execute concurrently within
the same phase while respecting all dependency edges.

[Algorithmic Approach]
Depth-first resolution with a three-state memo table per ID: unseen, in-progress
(-1), or finished with an assigned phase index (>= 0). Each node's phase is one
greater than the maximum phase among its dependencies. A second visit to an
in-progress node raises CycleError with the IDs on the closing path.

[Parameters]
elements maps each element ID to the IDs it depends on. A missing map entry for a
dependency is treated as an empty dependency list when that ID is reached via DFS,
but only keys present in elements appear in the returned groups.

[Returns]
groups is a slice of phases. groups[i] holds every element whose dependencies all
lie in phases 0..i-1; phase 0 contains elements with no dependencies (or only
dependencies outside elements' keys). IDs within a phase may be processed in any
order relative to each other.

[Exceptions & Errors]
Returns CycleError when a cycle is detected. The error path lists the involved IDs.

[Edge Cases]
An empty elements map yields an empty groups slice and a nil error. Elements with
no dependencies are placed in groups[0]. Duplicate keys are impossible in a map;
self-dependencies and longer cycles are reported via CycleError.

[Complexity]
Time: O(V + E) over elements and their dependency slices.
Space: O(V) for the memo table plus output groups.

[Side Effects]
Pure function. Does not mutate elements. Allocates groups and the internal memo map.

[Example]
// A -> B -> C (B and C must run before A)

	groups, err := STRUCTARCH_DAG_Resolve(map[string][]string{
		"A": {"B"},
		"B": {"C"},
		"C": nil,
	})

// groups[0] contains "C", groups[1] contains "B", groups[2] contains "A"
*/
func STRUCTARCH_DAG_Resolve[TID comparable](elements map[TID][]TID) (groups [][]TID, error error) {
	out := make([][]TID, 0)
	stateMap := map[TID]int{}

	for elementID := range elements {
		groupID, err := extractGroupForElement(elementID, elements, stateMap)
		if err != nil {
			return nil, err
		}

		for groupID >= len(out) {
			out = append(out, []TID{})
		}

		out[groupID] = append(out[groupID], elementID)
	}

	return out, nil
}

func extractGroupForElement[TID comparable](
	elementID TID,
	elements map[TID][]TID,
	stateMap map[TID]int,
) (int, error) {
	if elementState, seen := stateMap[elementID]; seen {
		if elementState == -1 {
			return -2, CycleError[TID]{
				path: []TID{elementID},
			}
		}

		return elementState, nil
	}

	stateMap[elementID] = -1

	latestDependency := -1
	elementDependencies := elements[elementID]
	for _, dependency := range elementDependencies {
		groupPhase, err := extractGroupForElement(dependency, elements, stateMap)

		if err != nil {
			if castedErr, ok := err.(CycleError[TID]); ok {
				castedErr.path = append(castedErr.path, elementID)
				return -2, castedErr
			}
			return -2, err
		}

		if groupPhase > latestDependency {
			latestDependency = groupPhase
		}
	}

	currentGroupID := latestDependency + 1
	stateMap[elementID] = currentGroupID

	return currentGroupID, nil
}
