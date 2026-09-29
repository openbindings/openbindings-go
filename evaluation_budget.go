package openbindings

import (
	"maps"
	"reflect"
	"slices"
	"strconv"
)

// evaluationBudget bounds the schema applications evaluating one value may
// make: each time the schema library applies a schema to a value, in place or
// to a member, an item, or a property name. The library does not share work
// between two applications of one schema to one value, so a small acyclic
// graph that applies a schema twice at each of n levels applies it 2^n times.
// Before evaluating, CompiledSchema.Validate counts the applications the graph
// can make on the value, and past this budget, a resource limit met (§10.4),
// it reaches no verdict.
const evaluationBudget = 1 << 22

// costGraph is the schema graph one compiled schema reaches, as the budget
// counts it. nodes[0] is the compiled schema.
type costGraph struct {
	nodes []costNode
}

// costNode is one schema of a costGraph, or a hub (schemaNode.hub), which is
// no application itself.
type costNode struct {
	// always apply to the same value; of each group in choices, one alone
	// does (then or else, and where a $dynamicRef lands), so the costliest
	// counts.
	always  []int
	choices [][]int
	// names apply to each property name; member to the member a name names,
	// members to every member; item to the item an index names, items to
	// every item.
	names   []int
	member  map[string][]int
	members []int
	item    map[int][]int
	items   []int
	// meta is whether the schema applies a meta-schema the library carries,
	// which this SDK does not analyze: it counts once for every member and
	// item within the value, as it examines each.
	meta, hub bool
}

// costGraph returns the graph the schema at start reaches, which analyze
// found evaluable.
func (o *operationSchemas) costGraph(start string) *costGraph {
	g := &o.graph
	first := g.id[compiledAt{start, rootOf(start)}]
	index := map[int]int{first: 0}
	order := []int{first}
	id := func(node int) int {
		if i, seen := index[node]; seen {
			return i
		}
		index[node] = len(order)
		order = append(order, node)
		return index[node]
	}
	out := &costGraph{}
	for k := 0; k < len(order); k++ {
		n := &g.nodes[order[k]]
		c := costNode{meta: n.meta, hub: n.hub}
		groups := map[int][]int{}
		for i, to := range n.inPlace {
			if group := n.choiceOf[i]; group >= 0 {
				groups[group] = append(groups[group], id(to))
			} else {
				c.always = append(c.always, id(to))
			}
		}
		for _, group := range slices.Sorted(maps.Keys(groups)) {
			c.choices = append(c.choices, groups[group])
		}
		for _, to := range n.propertyNames {
			c.names = append(c.names, id(to))
		}
		for i, to := range n.advancing {
			switch by := n.advanceBy[i]; {
			case by.items && by.every:
				c.items = append(c.items, id(to))
			case by.items:
				index, _ := strconv.Atoi(by.key)
				if c.item == nil {
					c.item = map[int][]int{}
				}
				c.item[index] = append(c.item[index], id(to))
			case by.every:
				c.members = append(c.members, id(to))
			default:
				if c.member == nil {
					c.member = map[string][]int{}
				}
				c.member[by.key] = append(c.member[by.key], id(to))
			}
		}
		out.nodes = append(out.nodes, c)
	}
	return out
}

// exceeds reports whether evaluating a value against the graph can apply
// schemas more than evaluationBudget times. The count is an upper bound: it
// applies both branches of an if, every member to patternProperties,
// additionalProperties, and unevaluatedProperties, and every item to items
// and unevaluatedItems. It takes time in proportion to the distinct pairs of a
// schema and a value within value it counts, never to the applications
// themselves. value is a JSON value with no cycle (ValueProblem).
func (g *costGraph) exceeds(value any) bool {
	e := costEstimate{g: g, memo: map[costKey]uint64{}, sizes: map[costKey]uint64{}, leaves: make([]uint64, len(g.nodes))}
	return e.cost(value, 0) > evaluationBudget
}

type costEstimate struct {
	g *costGraph
	// memo holds the cost of a schema on a container value, by the
	// container; sizes the count of members and items within one, itself
	// included; leaves the cost of a schema on a value with no member or item,
	// which is the same for every such value (0 while not yet counted).
	memo, sizes map[costKey]uint64
	leaves      []uint64
}

// costKey identifies a container value, by its backing storage, and a schema.
type costKey struct {
	at     uintptr
	length int
	node   int
}

// container returns the key of a value that holds a member or an item.
func container(value any) (costKey, bool) {
	switch v := value.(type) {
	case map[string]any:
		if len(v) > 0 {
			return costKey{at: reflect.ValueOf(v).Pointer(), length: -1}, true
		}
	case []any:
		if len(v) > 0 {
			return costKey{at: reflect.ValueOf(v).Pointer(), length: len(v)}, true
		}
	}
	return costKey{}, false
}

// plus adds two counts, saturating past the budget.
func plus(a, b uint64) uint64 {
	if sum := a + b; sum <= evaluationBudget {
		return sum
	}
	return evaluationBudget + 1
}

// cost returns how many applications applying the schema node to value can
// make, saturating past the budget.
func (e *costEstimate) cost(value any, node int) uint64 {
	key, isContainer := container(value)
	key.node = node
	if isContainer {
		if c, done := e.memo[key]; done {
			return c
		}
	} else if e.leaves[node] > 0 {
		return e.leaves[node]
	}
	n := &e.g.nodes[node]
	c := uint64(1)
	if n.hub {
		c = 0
	}
	if n.meta {
		c = plus(c, e.size(value))
	}
	for _, to := range n.always {
		c = plus(c, e.cost(value, to))
	}
	for _, group := range n.choices {
		costliest := uint64(0)
		for _, to := range group {
			costliest = max(costliest, e.cost(value, to))
		}
		c = plus(c, costliest)
	}
	switch v := value.(type) {
	case map[string]any:
		for name, member := range v {
			for _, to := range n.names {
				c = plus(c, e.cost("", to))
			}
			for _, to := range n.member[name] {
				c = plus(c, e.cost(member, to))
			}
			for _, to := range n.members {
				c = plus(c, e.cost(member, to))
			}
			if c > evaluationBudget {
				break
			}
		}
	case []any:
		for i, item := range v {
			for _, to := range n.item[i] {
				c = plus(c, e.cost(item, to))
			}
			for _, to := range n.items {
				c = plus(c, e.cost(item, to))
			}
			if c > evaluationBudget {
				break
			}
		}
	}
	if isContainer {
		e.memo[key] = c
	} else {
		e.leaves[node] = c
	}
	return c
}

// size returns how many values a value is and holds, saturating past the
// budget.
func (e *costEstimate) size(value any) uint64 {
	key, isContainer := container(value)
	if !isContainer {
		return 1
	}
	key.node = -1
	if s, done := e.sizes[key]; done {
		return s
	}
	s := uint64(1)
	switch v := value.(type) {
	case map[string]any:
		for _, member := range v {
			s = plus(s, e.size(member))
		}
	case []any:
		for _, item := range v {
			s = plus(s, e.size(item))
		}
	}
	e.sizes[key] = s
	return s
}
