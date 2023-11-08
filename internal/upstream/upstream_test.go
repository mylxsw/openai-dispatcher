package upstream

import (
	"testing"
)

func TestRoundRobinPolicy(t *testing.T) {
	index := 0
	items := []int{1, 2, 3}

	for i := 0; i < 10; i++ {
		index = (index + 1) % len(items)
		t.Log(index, "->", items[index])
	}
}
