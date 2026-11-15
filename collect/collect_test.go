package collect_test

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/atobeach/reef/collect"
)

func TestMapAppliesTheFunctionInOrder(t *testing.T) {
	if got := collect.Map([]int{3, 1, 2}, strconv.Itoa); !reflect.DeepEqual(got, []string{"3", "1", "2"}) {
		t.Fatalf("Map = %q", got)
	}
}

func TestAnEmptyMapEncodesAsAList(t *testing.T) {
	for name, items := range map[string][]int{"nil": nil, "empty": {}} {
		encoded, err := json.Marshal(collect.Map(items, strconv.Itoa))
		if err != nil || string(encoded) != "[]" {
			t.Errorf("%s: Map encodes as %s (%v), want []", name, encoded, err)
		}
	}
}

func TestGroupByKeepsFirstSeenKeyOrderAndItemOrder(t *testing.T) {
	type group struct {
		Key   string
		Items []string
	}

	var got []group
	for key, items := range collect.GroupBy([]string{"pear", "apple", "plum", "avocado", "banana"}, func(fruit string) string { return fruit[:1] }) {
		got = append(got, group{key, items})
	}

	want := []group{{"p", []string{"pear", "plum"}}, {"a", []string{"apple", "avocado"}}, {"b", []string{"banana"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupBy = %v, want %v", got, want)
	}
}

func TestGroupByStopsWhenTheLoopBreaks(t *testing.T) {
	visited := 0
	for range collect.GroupBy([]int{1, 2, 3}, func(n int) int { return n }) {
		visited++

		break
	}

	if visited != 1 {
		t.Fatalf("visited %d groups after a break, want 1", visited)
	}
}

func TestGroupByOfNothingYieldsNothing(t *testing.T) {
	for key := range collect.GroupBy([]int(nil), func(n int) int { return n }) {
		t.Fatalf("yielded %d from no items", key)
	}
}
