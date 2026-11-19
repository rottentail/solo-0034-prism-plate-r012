package service

import (
	"encoding/json"
	"fmt"
	"prismplate/internal/boolean"
	"prismplate/internal/geom"
	"prismplate/internal/repository"
	"prismplate/internal/scene"
	"prismplate/internal/validate"
	"time"
)

// ShapeRef names one compiled shape inside a source document.
type ShapeRef struct {
	Layer string `json:"layer"`
	Shape string `json:"shape"`
}

// BooleanRequest builds a new revision from a boolean combination.
type BooleanRequest struct {
	ExpectedRevision int          `json:"expected_revision"`
	SourceRevision   int          `json:"source_revision"`
	Operation        string       `json:"operation"`
	Operands         []ShapeRef   `json:"operands"`
	BrushSource      string       `json:"brush_source"`
	Brush            *scene.Brush `json:"brush,omitempty"`
	Tolerance        float64      `json:"tolerance"`
	LayerID          string       `json:"layer_id"`
	ShapeID          string       `json:"shape_id"`
	Title            string       `json:"title"`
}

const defaultBooleanTolerance = 1e-9

// Boolean runs a boolean operation on shapes from one source revision and
// appends the result as a new immutable revision. The new document contains the
// full source document plus one additional layer holding the single closed,
// identity-transformed result shape, so it can be transformed later.
func (s *Service) Boolean(plateID string, q BooleanRequest) (repository.Plate, error) {
	if q.Operation != "union" && q.Operation != "intersect" && q.Operation != "difference" {
		return repository.Plate{}, fmt.Errorf("operation must be union, intersect or difference")
	}
	if len(q.Operands) < 2 {
		return repository.Plate{}, fmt.Errorf("boolean requires at least two operands")
	}
	if len(q.Title) > 160 {
		return repository.Plate{}, fmt.Errorf("title too long")
	}
	if !validate.ID(q.LayerID) || !validate.ID(q.ShapeID) {
		return repository.Plate{}, fmt.Errorf("invalid result node id")
	}
	switch q.BrushSource {
	case "first", "last":
	case "explicit":
		if q.Brush == nil {
			return repository.Plate{}, fmt.Errorf("explicit brush source requires a brush")
		}
	default:
		return repository.Plate{}, fmt.Errorf("brush_source must be first, last or explicit")
	}
	tol := q.Tolerance
	if tol == 0 {
		tol = defaultBooleanTolerance
	}
	if tol < 1e-12 || tol > 1 {
		return repository.Plate{}, fmt.Errorf("tolerance must stay within 1e-12..1 document unit")
	}

	// Capture the source revision outside the write lock; geometry work happens
	// before any persistence attempt.
	p, err := s.Plate(plateID)
	if err != nil {
		return repository.Plate{}, err
	}
	if q.SourceRevision < 1 || q.SourceRevision > len(p.Revisions) {
		return repository.Plate{}, repository.ErrMissing
	}
	compiled, err := validate.Compile(p.Revisions[q.SourceRevision-1].Document)
	if err != nil {
		return repository.Plate{}, err
	}

	seen := map[string]bool{}
	var ops []boolean.Operand
	var chosen scene.Brush
	for i, ref := range q.Operands {
		key := ref.Layer + "/" + ref.Shape
		if seen[key] {
			return repository.Plate{}, fmt.Errorf("duplicate operand %s", key)
		}
		seen[key] = true
		var found *scene.CompiledShape
		for li := range compiled.Layers {
			if compiled.Layers[li].Layer.ID != ref.Layer {
				continue
			}
			for si := range compiled.Layers[li].Shapes {
				if compiled.Layers[li].Shapes[si].Shape.ID == ref.Shape {
					found = &compiled.Layers[li].Shapes[si]
				}
			}
		}
		if found == nil {
			return repository.Plate{}, fmt.Errorf("operand %s not found in source revision", key)
		}
		ops = append(ops, boolean.Operand{
			Path:      found.Path,
			Rule:      found.Shape.FillRule,
			Transform: found.Shape.Transform,
			Inverse:   found.Inverse,
		})
		if i == 0 {
			chosen = found.Shape.Brush
		}
		if i == len(q.Operands)-1 {
			if q.BrushSource == "last" {
				chosen = found.Shape.Brush
			}
		}
	}
	if q.BrushSource == "explicit" {
		chosen = *q.Brush
	}

	rings, err := boolean.Combine(q.Operation, ops, tol)
	if err != nil {
		return repository.Plate{}, err
	}
	if len(rings) == 0 {
		return repository.Plate{}, fmt.Errorf("boolean result is empty; nothing to persist")
	}
	contour := boolean.ToOps(rings)
	if len(contour) > 256 {
		return repository.Plate{}, fmt.Errorf("boolean result exceeds 256 contour operations; simplify operands or raise tolerance")
	}

	// Deep copy via the same serialization the repository uses, then append.
	var doc scene.Document
	raw, _ := json.Marshal(compiled.Document)
	if err := json.Unmarshal(raw, &doc); err != nil {
		return repository.Plate{}, err
	}
	if len(doc.Layers) >= 16 {
		return repository.Plate{}, fmt.Errorf("layer capacity reached")
	}
	existing := map[string]bool{}
	for _, l := range doc.Layers {
		existing[l.ID] = true
		for _, sh := range l.Shapes {
			existing[sh.ID] = true
		}
	}
	if existing[q.LayerID] || existing[q.ShapeID] {
		return repository.Plate{}, fmt.Errorf("result node id already exists")
	}
	result := scene.Shape{
		ID:        q.ShapeID,
		Path:      contour,
		FillRule:  "evenodd",
		Brush:     chosen,
		Transform: geom.Identity,
	}
	doc.Layers = append(doc.Layers, scene.Layer{
		ID:      q.LayerID,
		Opacity: 1,
		Blend:   "normal",
		Shapes:  []scene.Shape{result},
	})

	// Every HTTP scenario passes the same admission as manually authored scenes.
	if _, err := validate.Compile(doc); err != nil {
		return repository.Plate{}, err
	}
	var out repository.Plate
	err = s.Repo.Update(func(st *repository.State) error {
		cur, ok := st.Plates[plateID]
		if !ok {
			return repository.ErrMissing
		}
		if cur.State != "active" {
			return repository.ErrState
		}
		if len(cur.Revisions) != q.ExpectedRevision {
			return repository.ErrConflict
		}
		if len(cur.Revisions) >= 64 {
			return fmt.Errorf("revision capacity reached")
		}
		cur.Revisions = append(cur.Revisions, repository.Revision{
			Number:   q.ExpectedRevision + 1,
			Digest:   repository.SceneHash(doc),
			Document: doc,
			Created:  time.Now().UTC(),
		})
		st.Plates[plateID] = cur
		out = cur
		return nil
	})
	return out, err
}
