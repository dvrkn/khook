package engine

import (
	"strings"
	"testing"

	"github.com/dvrkn/khook/internal/spec"
)

func steps(defs ...spec.Step) []spec.Step { return defs }

func step(name string, needs ...string) spec.Step {
	return spec.Step{Name: name, Needs: needs}
}

func levelNames(levels [][]*spec.Step) [][]string {
	out := make([][]string, len(levels))
	for i, level := range levels {
		for _, s := range level {
			out[i] = append(out[i], s.Name)
		}
	}
	return out
}

func TestLevelsDiamond(t *testing.T) {
	levels, err := Levels(steps(
		step("top"),
		step("left", "top"),
		step("right", "top"),
		step("bottom", "left", "right"),
	))
	if err != nil {
		t.Fatal(err)
	}
	got := levelNames(levels)
	want := [][]string{{"top"}, {"left", "right"}, {"bottom"}}
	if len(got) != 3 || got[0][0] != "top" || len(got[1]) != 2 || got[2][0] != "bottom" {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLevelsIndependentChains(t *testing.T) {
	levels, err := Levels(steps(
		step("a1"),
		step("a2", "a1"),
		step("b1"),
		step("b2", "b1"),
	))
	if err != nil {
		t.Fatal(err)
	}
	got := levelNames(levels)
	if len(got) != 2 || len(got[0]) != 2 || len(got[1]) != 2 {
		t.Fatalf("got %v, want two levels of two", got)
	}
	// Deterministic: spec order within a level.
	if got[0][0] != "a1" || got[0][1] != "b1" {
		t.Fatalf("level 1 order = %v, want [a1 b1]", got[0])
	}
}

func TestLevelsCycle(t *testing.T) {
	_, err := Levels(steps(
		step("a", "c"),
		step("b", "a"),
		step("c", "b"),
		step("free"),
	))
	if err == nil {
		t.Fatal("want cycle error")
	}
	for _, name := range []string{"a", "b", "c"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("cycle error %q should name step %q", err, name)
		}
	}
	if strings.Contains(err.Error(), "free") {
		t.Errorf("cycle error %q should not name acyclic step", err)
	}
}

func TestLevelsSelfCycle(t *testing.T) {
	if _, err := Levels(steps(step("a", "a"))); err == nil {
		t.Fatal("want self-cycle error")
	}
}

func TestLevelsUnknownNeed(t *testing.T) {
	if _, err := Levels(steps(step("a", "ghost"))); err == nil {
		t.Fatal("want unknown-need error")
	}
}

func TestReversedDiamond(t *testing.T) {
	original := steps(
		step("top"),
		step("left", "top"),
		step("right", "top"),
		step("bottom", "left", "right"),
	)
	levels, err := Levels(Reversed(original))
	if err != nil {
		t.Fatal(err)
	}
	got := levelNames(levels)
	if len(got) != 3 || got[0][0] != "bottom" || len(got[1]) != 2 || got[2][0] != "top" {
		t.Fatalf("got %v, want [[bottom] [left right] [top]]", got)
	}
	// The input must not be mutated.
	if len(original[0].Needs) != 0 || len(original[3].Needs) != 2 {
		t.Fatalf("Reversed mutated its input: %v", original)
	}
}

func TestReversedIndependentSteps(t *testing.T) {
	levels, err := Levels(Reversed(steps(step("a"), step("b"))))
	if err != nil {
		t.Fatal(err)
	}
	got := levelNames(levels)
	if len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("got %v, want one level of two", got)
	}
}
