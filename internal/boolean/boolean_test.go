package boolean

import (
	"math"
	"prismplate/internal/geom"
	"testing"
)

func rect(x0, y0, x1, y1 float64, rule string) Operand {
	ring := geom.Contour{
		{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x1, Y: y1}, {X: x0, Y: y1},
	}
	return Operand{Path: geom.Path{ring}, Rule: rule, Transform: geom.Identity, Inverse: geom.Identity}
}

func ringPath(coords ...float64) geom.Path {
	var r geom.Contour
	for i := 0; i < len(coords); i += 2 {
		r = append(r, geom.Point{X: coords[i], Y: coords[i+1]})
	}
	return geom.Path{r}
}

func totalArea(p geom.Path) float64 {
	a := 0.0
	for _, r := range p {
		for i, v := range r {
			q := r[(i+1)%len(r)]
			a += v.X*q.Y - q.X*v.Y
		}
	}
	return math.Abs(a / 2)
}

func checkResult(t *testing.T, name string, p geom.Path, wantRings int, wantArea float64) {
	t.Helper()
	if len(p) != wantRings {
		t.Fatalf("%s: want %d rings, got %d: %v", name, wantRings, len(p), p)
	}
	if got := totalArea(p); math.Abs(got-wantArea) > 1e-3 {
		t.Fatalf("%s: want area %v, got %v", name, wantArea, got)
	}
}

func TestBasicOperations(t *testing.T) {
	tol := 1e-9
	a, b := rect(0, 0, 10, 10, "nonzero"), rect(5, 5, 15, 15, "nonzero")

	r, err := Combine("union", []Operand{a, b}, tol)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "union", r, 1, 175)

	r, err = Combine("intersect", []Operand{a, b}, tol)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "intersect", r, 1, 25)

	r, err = Combine("difference", []Operand{a, b}, tol)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "difference", r, 1, 75)
}

func TestOppositeWinding(t *testing.T) {
	// Screen-coordinate reverse orientation; classification is winding based.
	back := Operand{Path: ringPath(5, 5, 15, 5, 15, 15, 5, 15), Rule: "nonzero", Transform: geom.Identity, Inverse: geom.Identity}
	r, err := Combine("union", []Operand{rect(0, 0, 10, 10, "nonzero"), back}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "opposite winding union", r, 1, 175)
}

func TestCoincidentEdge(t *testing.T) {
	// Adjacent squares share the exact edge x=10; it must weld into one ring.
	r, err := Combine("union", []Operand{rect(0, 0, 10, 10, "nonzero"), rect(10, 0, 20, 10, "nonzero")}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "shared edge union", r, 1, 200)
	// No duplicated collinear vertices along the shared seam.
	for _, ring := range r {
		for _, p := range ring {
			if p.X == 10 && p.Y != 0 && p.Y != 10 {
				t.Fatalf("shared seam produced a fragment vertex at %v", p)
			}
		}
	}
}

func TestHoleDifference(t *testing.T) {
	r, err := Combine("difference", []Operand{rect(0, 0, 20, 20, "nonzero"), rect(5, 5, 15, 15, "nonzero")}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "hole difference", r, 2, 300) // outer 400 minus hole 100
}

func TestMirrorTransform(t *testing.T) {
	// Reflect x about x=5: the small square maps back onto the big square.
	m := geom.Matrix{-1, 0, 0, 1, 10, 0}
	inv, ok := m.Inverse()
	if !ok {
		t.Fatal("mirror singular")
	}
	mirrored := Operand{
		Path:      geom.Path{{{X: 0, Y: 0}, {X: 5, Y: 0}, {X: 5, Y: 5}, {X: 0, Y: 5}}},
		Rule:      "nonzero",
		Transform: m,
		Inverse:   inv,
	}
	r, err := Combine("union", []Operand{rect(0, 0, 10, 10, "nonzero"), mirrored}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "mirror union", r, 1, 100)
}

func TestNarrowChannelTolerance(t *testing.T) {
	a := rect(0, 0, 10, 10, "nonzero")
	b := rect(10.0005, 0, 20, 10, "nonzero")
	// Tight tolerance keeps the 0.0005 channel: two separate rings whose areas
	// sum to the two input rectangles (100 + 9.9995*10).
	r, err := Combine("union", []Operand{a, b}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "narrow channel tight", r, 2, 199.995)
	// Loose tolerance welds across the channel into one rectangle; the weld
	// closes the gap so the merged area is the full 10..20 span (200).
	r, err = Combine("union", []Operand{a, b}, 1e-3)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "narrow channel welded", r, 1, 200)
}

func TestEvenOdd(t *testing.T) {
	outer := ringPath(0, 0, 20, 0, 20, 20, 0, 20)
	inner := ringPath(5, 5, 15, 5, 15, 15, 5, 15)
	eo := Operand{Path: geom.Path{outer[0], inner[0]}, Rule: "evenodd", Transform: geom.Identity, Inverse: geom.Identity}

	// Corner clip opens the hole into an L-shaped notch: one outer boundary.
	r, err := Combine("intersect", []Operand{eo, rect(0, 0, 10, 10, "nonzero")}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "evenodd corner clip", r, 1, 75)

	// A covering rectangle preserves the enclosed hole: outer ring plus hole.
	r, err = Combine("intersect", []Operand{eo, rect(-5, -5, 25, 25, "nonzero")}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "evenodd enclosed hole", r, 2, 300)
}

func TestContainedAndDisjoint(t *testing.T) {
	r, err := Combine("union", []Operand{rect(0, 0, 20, 20, "nonzero"), rect(5, 5, 10, 10, "nonzero")}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, "contained union", r, 1, 400)

	r, err = Combine("intersect", []Operand{rect(0, 0, 1, 1, "nonzero"), rect(100, 100, 101, 101, "nonzero")}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 0 {
		t.Fatalf("disjoint intersect: want empty, got %d rings", len(r))
	}
}

func TestValidation(t *testing.T) {
	if _, err := Combine("union", []Operand{rect(0, 0, 1, 1, "nonzero")}, 1e-9); err == nil {
		t.Fatal("expected error for single operand")
	}
	two := []Operand{rect(0, 0, 1, 1, "nonzero"), rect(0, 0, 1, 1, "nonzero")}
	if _, err := Combine("xor", two, 1e-9); err == nil {
		t.Fatal("expected error for unknown operation")
	}
	if _, err := Combine("union", two, 0); err == nil {
		t.Fatal("expected error for non-positive tolerance")
	}
}

func TestToOpsRoundTrip(t *testing.T) {
	r, err := Combine("union", []Operand{rect(0, 0, 10, 10, "nonzero"), rect(5, 5, 15, 15, "nonzero")}, 1e-9)
	if err != nil {
		t.Fatal(err)
	}
	ops := ToOps(r)
	if len(ops) == 0 || ops[0].Kind != "M" {
		t.Fatalf("ToOps must start each ring with M, got %v", ops)
	}
	// Re-flattening the emitted ops must reproduce the same geometry.
	back := geom.Flatten(ops)
	if totalArea(back) != totalArea(r) {
		t.Fatal("ToOps round trip changed the geometry")
	}
}
