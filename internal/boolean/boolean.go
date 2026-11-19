// Package boolean evaluates closed-contour union, intersection and difference
// with an exact-topology planar subdivision. Coincident edges are welded into a
// single graph edge so overlapping boundaries never produce fragments; holes,
// opposite winding and mirrored transforms are handled by classifying each
// planar face against the operands' own fill rules.
package boolean

import (
	"fmt"
	"math"
	"prismplate/internal/geom"
	"sort"
)

// MaxInputSegments bounds pairwise intersection work for one boolean request.
const MaxInputSegments = 2000

// MaxSubedges bounds the subdivided planar graph that one request may create.
const MaxSubedges = 20000

// Operand is a flattened contour set already mapped into document coordinates.
type Operand struct {
	Path      geom.Path
	Rule      string
	Transform geom.Matrix
	Inverse   geom.Matrix
}

type fseg struct {
	a, b geom.Point
	op   int
}

type welder struct {
	tol   float64
	pts   []geom.Point
	cells map[[2]int][]int
}

func newWelder(tol float64) *welder {
	return &welder{tol: tol, cells: map[[2]int][]int{}}
}
func (w *welder) cell(p geom.Point) [2]int {
	return [2]int{int(math.Floor(p.X / w.tol)), int(math.Floor(p.Y / w.tol))}
}

// Get returns the canonical node index for p, merging every point within tol.
func (w *welder) Get(p geom.Point) int {
	k := w.cell(p)
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			for _, idx := range w.cells[[2]int{k[0] + dx, k[1] + dy}] {
				q := w.pts[idx]
				if math.Hypot(q.X-p.X, q.Y-p.Y) <= w.tol {
					return idx
				}
			}
		}
	}
	idx := len(w.pts)
	w.pts = append(w.pts, p)
	w.cells[k] = append(w.cells[k], idx)
	return idx
}

func sub(a, b geom.Point) geom.Point { return geom.Point{X: a.X - b.X, Y: a.Y - b.Y} }
func cross(a, b geom.Point) float64  { return a.X*b.Y - a.Y*b.X }

// pairCuts returns points where s1 and s2 meet: proper crossings, T junctions,
// endpoint touches and collinear overlap endpoints.
func pairCuts(s1, s2 fseg, tol float64) []geom.Point {
	d1, d2 := sub(s1.b, s1.a), sub(s2.b, s2.a)
	l1, l2 := math.Hypot(d1.X, d1.Y), math.Hypot(d2.X, d2.Y)
	scale := math.Max(l1, l2)
	cr := cross(d1, d2)
	if math.Abs(cr) <= tol*math.Max(scale, 1) {
		// Parallel: only collinear lines can contribute cuts.
		if math.Abs(cross(d1, sub(s2.a, s1.a))) > tol*math.Max(l1, 1) {
			return nil
		}
		var out []geom.Point
		project := func(p geom.Point, a geom.Point, d geom.Point, l float64) (geom.Point, float64) {
			t := dot(sub(p, a), d) / (l * l)
			return geom.Point{X: a.X + t*d.X, Y: a.Y + t*d.Y}, t
		}
		if l1 > 0 {
			for _, p := range []geom.Point{s2.a, s2.b} {
				if q, t := project(p, s1.a, d1, l1); t >= -tol/l1 && t <= 1+tol/l1 {
					out = append(out, q)
				}
			}
		}
		if l2 > 0 {
			for _, p := range []geom.Point{s1.a, s1.b} {
				if q, t := project(p, s2.a, d2, l2); t >= -tol/l2 && t <= 1+tol/l2 {
					out = append(out, q)
				}
			}
		}
		return out
	}
	eps1 := tol / math.Max(l1, 1e-12)
	eps2 := tol / math.Max(l2, 1e-12)
	t := cross(sub(s2.a, s1.a), d2) / cr
	u := cross(sub(s2.a, s1.a), d1) / cr
	if t < -eps1 || t > 1+eps1 || u < -eps2 || u > 1+eps2 {
		return nil
	}
	t = math.Max(0, math.Min(1, t))
	return []geom.Point{{X: s1.a.X + t*d1.X, Y: s1.a.Y + t*d1.Y}}
}

func dot(a, b geom.Point) float64 { return a.X*b.X + a.Y*b.Y }

type hedge struct {
	from, to int
	twin     int
	next     int
	face     int
}

type face struct {
	nodes []int
	area  float64 // signed; bounded interior faces are positive, exterior sides negative
}

// Combine runs operation ("union", "intersect", "difference") over operands and
// returns closed contours in document coordinates. Difference subtracts every
// operand after the first from the first operand.
func Combine(operation string, operands []Operand, tol float64) (geom.Path, error) {
	if len(operands) < 2 {
		return nil, fmt.Errorf("boolean requires at least two operands")
	}
	switch operation {
	case "union", "intersect", "difference":
	default:
		return nil, fmt.Errorf("operation must be union, intersect or difference")
	}
	if tol <= 0 {
		return nil, fmt.Errorf("tolerance must be positive")
	}

	// 1. Flatten every operand ring into document-space segments.
	var segs []fseg
	for oi, op := range operands {
		for _, ring := range op.Path {
			for i := range ring {
				a := op.Transform.Apply(ring[i])
				b := op.Transform.Apply(ring[(i+1)%len(ring)])
				if math.Hypot(b.X-a.X, b.Y-a.Y) <= tol {
					continue
				}
				segs = append(segs, fseg{a: a, b: b, op: oi})
			}
		}
	}
	if len(segs) > MaxInputSegments {
		return nil, fmt.Errorf("boolean operand complexity exceeds bounded work (%d segments)", len(segs))
	}
	cuts := make([][]geom.Point, len(segs))
	for i := range segs {
		cuts[i] = []geom.Point{segs[i].a, segs[i].b}
	}

	// 2. Pairwise intersections collect every split point, including within one
	// operand so self-crossing contours subdivide correctly.
	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			for _, p := range pairCuts(segs[i], segs[j], tol) {
				cuts[i] = append(cuts[i], p)
				cuts[j] = append(cuts[j], p)
			}
		}
	}

	// 3. Weld globally and rebuild the graph as unique undirected node pairs.
	// Coincident edges collapse here, which is what prevents duplicate fragments.
	w := newWelder(tol)
	pairs := map[[2]int]bool{}
	for si, s := range segs {
		d := sub(s.b, s.a)
		l2 := math.Max(dot(d, d), 1e-24)
		type tn struct {
			id int
			t  float64
		}
		var ordered []tn
		seen := map[int]bool{}
		for _, p := range cuts[si] {
			id := w.Get(p)
			if seen[id] {
				continue
			}
			seen[id] = true
			t := dot(sub(p, s.a), d) / l2
			ordered = append(ordered, tn{id, t})
		}
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].t != ordered[j].t {
				return ordered[i].t < ordered[j].t
			}
			return ordered[i].id < ordered[j].id
		})
		for k := 1; k < len(ordered); k++ {
			u, v := ordered[k-1].id, ordered[k].id
			if u == v {
				continue
			}
			if u > v {
				u, v = v, u
			}
			pairs[[2]int{u, v}] = true
			if len(pairs) > MaxSubedges {
				return nil, fmt.Errorf("boolean subdivision exceeds bounded work")
			}
		}
	}

	// 4. Directed half-edges with angle ordering at each node.
	var edges []hedge
	directed := map[[2]int]int{}
	outgoing := map[int][]int{}
	addEdge := func(u, v int) {
		idx := len(edges)
		edges = append(edges, hedge{from: u, to: v, face: -1})
		directed[[2]int{u, v}] = idx
		outgoing[u] = append(outgoing[u], idx)
	}
	for p := range pairs {
		addEdge(p[0], p[1])
		addEdge(p[1], p[0])
	}
	// Outgoing edges are ordered by mathematical atan2. The renderer uses screen
	// coordinates (y grows down); negate y when comparing angles so the ordering
	// is a genuine counter-clockwise order in an equivalent y-up frame.
	for u, list := range outgoing {
		sort.Slice(list, func(i, j int) bool {
			ei, ej := list[i], list[j]
			ai := math.Atan2(-(w.pts[edges[ei].to].Y - w.pts[u].Y), w.pts[edges[ei].to].X-w.pts[u].X)
			aj := math.Atan2(-(w.pts[edges[ej].to].Y - w.pts[u].Y), w.pts[edges[ej].to].X-w.pts[u].X)
			if ai != aj {
				return ai < aj
			}
			return edges[ei].to < edges[ej].to
		})
	}
	// Standard left-hand rotation: at target node v, next(e) is the edge
	// immediately after e's twin in v's counter-clockwise outgoing order.
	for _, list := range outgoing {
		for _, e := range list {
			v := edges[e].to
			twin := directed[[2]int{v, edges[e].from}]
			edges[e].twin = twin
			atV := outgoing[v]
			pos := -1
			for k, cand := range atV {
				if cand == twin {
					pos = k
					break
				}
			}
			edges[e].next = atV[(pos+1)%len(atV)]
		}
	}

	// 5. Enumerate faces: each half-edge walk keeps the face on its left.
	var faces []face
	faceOf := make([]int, len(edges))
	for i := range faceOf {
		faceOf[i] = -1
	}
	for start := range edges {
		if faceOf[start] != -1 {
			continue
		}
		var nodes []int
		cur := start
		for faceOf[cur] == -1 {
			faceOf[cur] = len(faces)
			nodes = append(nodes, edges[cur].from)
			cur = edges[cur].next
		}
		if cur != start {
			return nil, fmt.Errorf("non-manifold boolean subdivision")
		}
		ring := make([]geom.Point, len(nodes))
		for i, n := range nodes {
			ring[i] = w.pts[n]
		}
		faces = append(faces, face{nodes: nodes, area: signedArea(ring)})
	}
	for i := range edges {
		edges[i].face = faceOf[i]
	}

	// 6. Bounding span drives the side-sampling offset used below.
	bboxMinX, bboxMinY, bboxMaxX, bboxMaxY := w.pts[0].X, w.pts[0].Y, w.pts[0].X, w.pts[0].Y
	for _, p := range w.pts {
		bboxMinX, bboxMinY = math.Min(bboxMinX, p.X), math.Min(bboxMinY, p.Y)
		bboxMaxX, bboxMaxY = math.Max(bboxMaxX, p.X), math.Max(bboxMaxY, p.Y)
	}
	span := math.Max(bboxMaxX-bboxMinX, bboxMaxY-bboxMinY)
	if span <= 0 {
		span = 1
	}

	// classify reports whether a document-space point belongs to the result set.
	// Winding numbers honor each operand's fill rule, so opposite winding, holes
	// and even-odd parity are all resolved without face identity bookkeeping.
	classify := func(p geom.Point) bool {
		inside := make([]bool, len(operands))
		count := 0
		for oi, op := range operands {
			local := op.Inverse.Apply(p)
			wn := op.Path.Winding(local)
			inside[oi] = geom.InsideWith(wn, op.Rule)
			if inside[oi] {
				count++
			}
		}
		switch operation {
		case "union":
			return count >= 1
		case "intersect":
			return count == len(operands)
		default: // difference: keep operand-0 region not covered by any other
			return inside[0] && count == 1
		}
	}

	// 7. Classify every face once at its area-weighted centroid. Each half-edge
	// references its left face directly, so coincident/collinear edges cannot
	// make two distinct regions collapse into one sample: every planar face
	// keeps its own centroid and its own boolean verdict.
	faceKept := make([]bool, len(faces))
	deltas := []float64{2 * tol, 8 * tol, 32 * tol, 128 * tol, span * 1e-6, span * 1e-4, span * 1e-2}
	// Under canonical CCW half-edge ordering the unbounded face carries the most
	// negative signed area (it winds around the whole subdivision); hole rings
	// add smaller negative faces and never exceed it.
	unbounded := 0
	for fi := 1; fi < len(faces); fi++ {
		if faces[fi].area < faces[unbounded].area {
			unbounded = fi
		}
	}
	faceRing := make([][]geom.Point, len(faces))
	for fi, f := range faces {
		faceRing[fi] = f.points(w)
	}
	// First pass classifies only positive-area faces. Such a face is the
	// interior side of an enumerated ring, so its area centroid is a reliable
	// probe (an annular face's band is handled by falling back to an edge
	// offset). Negative-area faces are the exterior sides and are resolved in
	// the topology passes below instead of being sampled directly.
	for fi, f := range faces {
		if fi == unbounded || f.area <= 0 || math.Abs(f.area) <= tol*tol {
			continue
		}
		ring := faceRing[fi]
		q, ok := interiorPoint(ring, deltas)
		if !ok {
			if c, cOK := centroid(ring); cOK && ringContains(ring, c) {
				q, ok = c, true
			}
		}
		if !ok {
			continue
		}
		faceKept[fi] = classify(q)
	}
	// 8. Resolve the kept state of each half-edge's immediate left side. A
	// positive-area (interior) face reuses its face-level verdict. A
	// negative-area (exterior-side) face cannot carry one boolean for the whole
	// unbounded region, so each of its half-edges is classified locally by a
	// probe a weld-scale distance to its left: the probe stays inside the
	// narrowest genuine channel instead of jumping across it. Coincident edges,
	// holes, mirrored/opposite winding and sub-tolerance gaps therefore resolve
	// at the exact edge separating two differently classified regions.
	sideKept := make([]bool, len(edges))
	for i, e := range edges {
		if faces[e.face].area > 0 {
			sideKept[i] = faceKept[e.face]
			continue
		}
		a, b := w.pts[e.from], w.pts[e.to]
		d := sub(b, a)
		l := math.Hypot(d.X, d.Y)
		if l == 0 {
			continue
		}
		mid := geom.Point{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2}
		off := math.Max(tol*1.5, span*1e-12)
		q := geom.Point{X: mid.X - d.Y/l*off, Y: mid.Y + d.X/l*off}
		sideKept[i] = classify(q)
	}

	// A geometric edge is a result boundary when its two sides classify
	// differently. Emit one directed half-edge per such edge, oriented with the
	// kept region on its left; hole boundaries take the opposite direction.
	// Coincident edges were welded into one graph edge, so overlaps never produce
	// duplicate fragments.
	var boundary []int
	isBoundary := make([]bool, len(edges))
	paired := make([]bool, len(edges))
	for i, e := range edges {
		if paired[i] {
			continue
		}
		paired[i], paired[e.twin] = true, true
		kL, kR := sideKept[i], sideKept[e.twin]
		if kL == kR {
			continue
		}
		choice := i
		if !kL {
			choice = e.twin
		}
		boundary = append(boundary, choice)
		isBoundary[choice] = true
	}

	// 9. Connect boundary half-edges into closed rings. Welding guarantees that
	// each node has at most one outgoing boundary half-edge per ring, so a
	// direct from-node lookup closes every loop, including holes (opposite
	// orientation) and disconnected rings, without angle-scan ambiguity.
	var path geom.Path
	used := make([]bool, len(edges))
	boundaryOut := map[int]int{}
	for _, e := range boundary {
		boundaryOut[edges[e].from] = e
	}
	for _, start := range boundary {
		if used[start] {
			continue
		}
		var nodes []int
		cur := start
		for {
			used[cur] = true
			nodes = append(nodes, edges[cur].from)
			next, ok := boundaryOut[edges[cur].to]
			if !ok {
				return nil, fmt.Errorf("boolean boundary did not close")
			}
			cur = next
			if cur == start {
				break
			}
			if used[cur] {
				return nil, fmt.Errorf("boolean boundary did not close")
			}
		}
		ring := make([]geom.Point, 0, len(nodes))
		for _, n := range nodes {
			p := w.pts[n]
			if len(ring) > 0 && ring[len(ring)-1] == p {
				continue
			}
			ring = append(ring, p)
		}
		ring = simplify(ring, tol)
		if len(ring) < 3 || math.Abs(signedArea(ring)) <= tol*tol {
			continue
		}
		path = append(path, ring)
	}

	// Deterministic output regardless of map iteration order.
	sort.Slice(path, func(i, j int) bool {
		a, b := path[i], path[j]
		if a[0].X != b[0].X {
			return a[0].X < b[0].X
		}
		if a[0].Y != b[0].Y {
			return a[0].Y < b[0].Y
		}
		return len(a) < len(b)
	})
	return path, nil
}

func (f face) points(w *welder) []geom.Point {
	out := make([]geom.Point, len(f.nodes))
	for i, n := range f.nodes {
		out[i] = w.pts[n]
	}
	return out
}

func signedArea(ring []geom.Point) float64 {
	a := 0.0
	for i, p := range ring {
		q := ring[(i+1)%len(ring)]
		a += p.X*q.Y - q.X*p.Y
	}
	return a / 2
}

// centroid is the area-weighted centroid of a ring. It lies strictly inside
// every convex face and is a stable representative point for subdivision faces.
func centroid(ring []geom.Point) (geom.Point, bool) {
	a2 := 0.0
	var cx, cy float64
	for i, p := range ring {
		q := ring[(i+1)%len(ring)]
		c := p.X*q.Y - q.X*p.Y
		a2 += c
		cx += (p.X + q.X) * c
		cy += (p.Y + q.Y) * c
	}
	if math.Abs(a2) < 1e-18 {
		return geom.Point{}, false
	}
	f := 1 / (3 * a2)
	return geom.Point{X: cx * f, Y: cy * f}, true
}

func ringContains(ring []geom.Point, p geom.Point) bool {
	inside := false
	for i, a := range ring {
		b := ring[(i+1)%len(ring)]
		if (a.Y > p.Y) != (b.Y > p.Y) {
			x := a.X + (p.Y-a.Y)*(b.X-a.X)/(b.Y-a.Y)
			if x > p.X {
				inside = !inside
			}
		}
	}
	return inside
}

func onSegment(a, b, p geom.Point, tol float64) bool {
	d := sub(b, a)
	l := math.Hypot(d.X, d.Y)
	if l == 0 {
		return math.Hypot(p.X-a.X, p.Y-a.Y) <= tol
	}
	t := dot(sub(p, a), d) / (l * l)
	if t < 0 || t > 1 {
		return false
	}
	closest := geom.Point{X: a.X + t*d.X, Y: a.Y + t*d.Y}
	return math.Hypot(closest.X-p.X, closest.Y-p.Y) <= tol
}

// interiorPoint offsets the longest edge midpoint toward the face interior,
// trying both normals at increasing scales so genuine narrow channels still
// resolve instead of collapsing.
func interiorPoint(ring []geom.Point, deltas []float64) (geom.Point, bool) {
	best := 0
	bestLen := -1.0
	for i := range ring {
		a, b := ring[i], ring[(i+1)%len(ring)]
		if l := math.Hypot(b.X-a.X, b.Y-a.Y); l > bestLen {
			bestLen, best = l, i
		}
	}
	a, b := ring[best], ring[(best+1)%len(ring)]
	mid := geom.Point{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2}
	d := sub(b, a)
	l := math.Hypot(d.X, d.Y)
	if l == 0 {
		return geom.Point{}, false
	}
	nL := geom.Point{X: -d.Y / l, Y: d.X / l}
	nR := geom.Point{X: d.Y / l, Y: -d.X / l}
	for _, delta := range deltas {
		for _, n := range []geom.Point{nL, nR} {
			q := geom.Point{X: mid.X + n.X*delta, Y: mid.Y + n.Y*delta}
			if ringContains(ring, q) {
				onEdge := false
				for i := range ring {
					if onSegment(ring[i], ring[(i+1)%len(ring)], q, delta/2) {
						onEdge = true
						break
					}
				}
				if !onEdge {
					return q, true
				}
			}
		}
	}
	return geom.Point{}, false
}

// simplify removes collinear vertices within tol while keeping rings closed and
// at least three vertices long.
func simplify(ring []geom.Point, tol float64) []geom.Point {
	if len(ring) <= 3 {
		return ring
	}
	out := append([]geom.Point{}, ring...)
	for changed := true; changed && len(out) > 3; {
		changed = false
		for i := 0; i < len(out); i++ {
			prev, cur, next := out[(i-1+len(out))%len(out)], out[i], out[(i+1)%len(out)]
			d := sub(next, prev)
			l := math.Hypot(d.X, d.Y)
			if l == 0 {
				continue
			}
			dist := math.Abs(cross(d, sub(cur, prev))) / l
			t := dot(sub(cur, prev), d) / (l * l)
			if dist <= tol && t >= -tol/l && t <= 1+tol/l {
				out = append(out[:i], out[i+1:]...)
				changed = true
				break
			}
		}
	}
	return out
}

// ToOps converts boolean output rings into the M/L/Z contour contract.
func ToOps(path geom.Path) []geom.Op {
	var ops []geom.Op
	for _, ring := range path {
		ops = append(ops, geom.Op{Kind: "M", Points: []geom.Point{ring[0]}})
		for _, p := range ring[1:] {
			ops = append(ops, geom.Op{Kind: "L", Points: []geom.Point{p}})
		}
		ops = append(ops, geom.Op{Kind: "Z"})
	}
	return ops
}
