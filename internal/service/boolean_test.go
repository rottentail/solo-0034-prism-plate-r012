package service

import (
	"prismplate/internal/geom"
	"prismplate/internal/repository"
	"prismplate/internal/scene"
	"testing"
)

func newBooleanService(t *testing.T) *Service {
	t.Helper()
	repo, err := repository.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return New(repo)
}
func booleeDocument() scene.Document {
	solid := func(id string, x0, y0, x1, y1 float64) scene.Shape {
		return scene.Shape{
			ID:       id,
			FillRule: "nonzero",
			Path: []geom.Op{
				{Kind: "M", Points: []geom.Point{{X: x0, Y: y0}}},
				{Kind: "L", Points: []geom.Point{{X: x1, Y: y0}}},
				{Kind: "L", Points: []geom.Point{{X: x1, Y: y1}}},
				{Kind: "L", Points: []geom.Point{{X: x0, Y: y1}}},
				{Kind: "Z"},
			},
			Transform: geom.Identity,
			Brush:     scene.Brush{Kind: "solid", Color: scene.Color{R: 1, G: 0, B: 0, A: 1}},
		}
	}
	return scene.Document{
		Width: 100, Height: 100,
		Background: scene.Color{R: 1, G: 1, B: 1, A: 1},
		Layers: []scene.Layer{{
			ID:      "sheet",
			Opacity: 1,
			Blend:   "normal",
			Shapes:  []scene.Shape{solid("a", 0, 0, 10, 10), solid("b", 5, 5, 15, 15)},
		}},
	}
}

func seedBooleanPlate(t *testing.T, svc *Service) {
	t.Helper()
	doc := booleeDocument()
	if _, err := svc.Create("p", "t", doc); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestBooleanAppendsRevision(t *testing.T) {
	svc := newBooleanService(t)
	seedBooleanPlate(t, svc)

	p, err := svc.Boolean("p", BooleanRequest{
		ExpectedRevision: 1,
		SourceRevision:   1,
		Operation:        "union",
		Operands:         []ShapeRef{{Layer: "sheet", Shape: "a"}, {Layer: "sheet", Shape: "b"}},
		BrushSource:      "first",
		LayerID:          "result-layer",
		ShapeID:          "result-shape",
	})
	if err != nil {
		t.Fatalf("boolean: %v", err)
	}
	if len(p.Revisions) != 2 {
		t.Fatalf("want 2 revisions, got %d", len(p.Revisions))
	}
	doc := p.Revisions[1].Document
	if len(doc.Layers) != 2 {
		t.Fatalf("want source + result layer, got %d", len(doc.Layers))
	}
	res := doc.Layers[1].Shapes[0]
	if res.ID != "result-shape" || res.Transform != geom.Identity || res.FillRule != "evenodd" {
		t.Fatalf("unexpected result shape %+v", res)
	}
	if res.Brush.Color.R != 1 {
		t.Fatalf("first-operand brush not applied: %+v", res.Brush)
	}
	if len(res.Path) == 0 || res.Path[0].Kind != "M" || res.Path[len(res.Path)-1].Kind != "Z" {
		t.Fatalf("result contour must open with M and close with Z: %v", res.Path)
	}
	// Original revision remains untouched.
	if len(p.Revisions[0].Document.Layers) != 1 {
		t.Fatal("source revision was mutated")
	}
}

func TestBooleanExplicitBrushAndConflict(t *testing.T) {
	svc := newBooleanService(t)
	seedBooleanPlate(t, svc)

	base := BooleanRequest{
		SourceRevision: 1,
		Operation:      "difference",
		Operands:       []ShapeRef{{Layer: "sheet", Shape: "a"}, {Layer: "sheet", Shape: "b"}},
		BrushSource:    "explicit",
		Brush:          &scene.Brush{Kind: "solid", Color: scene.Color{R: 0.2, G: 0.4, B: 0.6, A: 0.8}},
		LayerID:        "diff-layer",
		ShapeID:        "diff-shape",
	}
	base.ExpectedRevision = 1
	if _, err := svc.Boolean("p", base); err != nil {
		t.Fatalf("first boolean: %v", err)
	}
	stale := base
	stale.ExpectedRevision = 1
	stale.LayerID, stale.ShapeID = "stale-layer", "stale-shape"
	if _, err := svc.Boolean("p", stale); err != repository.ErrConflict {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestBooleanRejectsBadInput(t *testing.T) {
	svc := newBooleanService(t)
	seedBooleanPlate(t, svc)

	cases := []struct {
		name string
		req  BooleanRequest
	}{
		{"unknown operation", BooleanRequest{ExpectedRevision: 1, SourceRevision: 1, Operation: "xor", Operands: []ShapeRef{{Layer: "sheet", Shape: "a"}, {Layer: "sheet", Shape: "b"}}, BrushSource: "first", LayerID: "l", ShapeID: "s"}},
		{"single operand", BooleanRequest{ExpectedRevision: 1, SourceRevision: 1, Operation: "union", Operands: []ShapeRef{{Layer: "sheet", Shape: "a"}}, BrushSource: "first", LayerID: "l", ShapeID: "s"}},
		{"missing shape", BooleanRequest{ExpectedRevision: 1, SourceRevision: 1, Operation: "union", Operands: []ShapeRef{{Layer: "sheet", Shape: "ghost"}, {Layer: "sheet", Shape: "b"}}, BrushSource: "first", LayerID: "l", ShapeID: "s"}},
		{"explicit without brush", BooleanRequest{ExpectedRevision: 1, SourceRevision: 1, Operation: "union", Operands: []ShapeRef{{Layer: "sheet", Shape: "a"}, {Layer: "sheet", Shape: "b"}}, BrushSource: "explicit", LayerID: "l", ShapeID: "s"}},
		{"duplicate layer id", BooleanRequest{ExpectedRevision: 1, SourceRevision: 1, Operation: "union", Operands: []ShapeRef{{Layer: "sheet", Shape: "a"}, {Layer: "sheet", Shape: "b"}}, BrushSource: "first", LayerID: "sheet", ShapeID: "s"}},
		{"tolerance out of range", BooleanRequest{ExpectedRevision: 1, SourceRevision: 1, Operation: "union", Operands: []ShapeRef{{Layer: "sheet", Shape: "a"}, {Layer: "sheet", Shape: "b"}}, BrushSource: "first", Tolerance: 50, LayerID: "l", ShapeID: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Boolean("p", tc.req); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
