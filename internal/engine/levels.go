// Package engine resolves a Khook document's steps into DAG levels and runs
// them level-parallel with retries, timeouts, and onError semantics.
package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dvrkn/khook/internal/spec"
)

// Reversed returns a copy of the steps with every needs edge inverted: if A
// needs B to build, B "needs" A to tear down. Feeding the result to Levels
// yields reverse-topological teardown order (destroy). Edge order follows
// spec order, so output is deterministic.
func Reversed(steps []spec.Step) []spec.Step {
	index := map[string]int{}
	for i := range steps {
		index[steps[i].Name] = i
	}
	out := make([]spec.Step, len(steps))
	copy(out, steps)
	for i := range out {
		out[i].Needs = nil
	}
	for i := range steps {
		for _, need := range steps[i].Needs {
			if j, ok := index[need]; ok {
				out[j].Needs = append(out[j].Needs, steps[i].Name)
			}
		}
	}
	return out
}

// Levels topologically sorts steps into execution levels (Kahn's algorithm):
// every step lands one level after the deepest step it needs. A dependency
// cycle is an error naming the steps involved. Order within a level follows
// spec order, so output is deterministic.
func Levels(steps []spec.Step) ([][]*spec.Step, error) {
	index := map[string]int{}
	for i := range steps {
		index[steps[i].Name] = i
	}

	indegree := make([]int, len(steps))
	dependents := make([][]int, len(steps))
	for i := range steps {
		for _, need := range steps[i].Needs {
			j, ok := index[need]
			if !ok {
				// Validation catches this; guard for direct callers.
				return nil, fmt.Errorf("step %q needs unknown step %q", steps[i].Name, need)
			}
			indegree[i]++
			dependents[j] = append(dependents[j], i)
		}
	}

	var levels [][]*spec.Step
	current := []int{}
	for i := range steps {
		if indegree[i] == 0 {
			current = append(current, i)
		}
	}

	placed := 0
	for len(current) > 0 {
		sort.Ints(current)
		level := make([]*spec.Step, 0, len(current))
		var next []int
		for _, i := range current {
			level = append(level, &steps[i])
			placed++
			for _, dep := range dependents[i] {
				indegree[dep]--
				if indegree[dep] == 0 {
					next = append(next, dep)
				}
			}
		}
		levels = append(levels, level)
		current = next
	}

	if placed != len(steps) {
		var cyclic []string
		for i := range steps {
			if indegree[i] > 0 {
				cyclic = append(cyclic, steps[i].Name)
			}
		}
		return nil, fmt.Errorf("dependency cycle involving steps: %s", strings.Join(cyclic, ", "))
	}
	return levels, nil
}
