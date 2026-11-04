package contract

import (
	"errors"
	"fmt"
)

func (t *Target) Validate() error {
	var errs []error
	if t.ID == "" {
		errs = append(errs, errors.New("target: missing id"))
	}
	if t.Package == "" || t.Func == "" {
		errs = append(errs, fmt.Errorf("target %s: package and func are required", t.ID))
	}
	if len(t.Models) == 0 {
		errs = append(errs, fmt.Errorf("target %s: at least one model is required", t.ID))
	}
	return errors.Join(errs...)
}

func (p *Property) Validate() error {
	var errs []error
	if p.ID == "" || p.Target == "" {
		errs = append(errs, errors.New("property: id and target are required"))
	}
	switch p.Kind {
	case KindNoPanic, KindSequence, KindNoHang:
	case KindPostcondition, KindEquivalence:
		if p.Spec == nil || p.Spec.GoFunc == "" {
			errs = append(errs, fmt.Errorf("property %s: kind %s requires spec.go_func", p.ID, p.Kind))
		}
	case KindCrosscheck:
	case KindCustom:
		if p.Precondition == nil && p.Spec == nil {
			errs = append(errs, fmt.Errorf("property %s: custom property needs a Go oracle (spec or precondition)", p.ID))
		}
	default:
		errs = append(errs, fmt.Errorf("property %s: unknown kind %q", p.ID, p.Kind))
	}
	if p.Frontend == "" {
		errs = append(errs, fmt.Errorf("property %s: frontend is required", p.ID))
	}
	return errors.Join(errs...)
}

func (c *Counterexample) Validate() error {
	var errs []error
	if c.ID == "" || c.Property == "" {
		errs = append(errs, errors.New("counterexample: id and property are required"))
	}
	if c.Frontend == "" || c.Engine == "" {
		errs = append(errs, fmt.Errorf("counterexample %s: frontend and engine are required", c.ID))
	}
	if c.Inputs == nil {
		errs = append(errs, fmt.Errorf("counterexample %s: inputs is required (may be empty)", c.ID))
	}
	return errors.Join(errs...)
}

func (a *ABI) Validate() error {
	var errs []error
	if a.GoFunc == "" || a.Frontend == "" {
		errs = append(errs, errors.New("abi: go_func and frontend are required"))
	}
	for i, p := range a.Params {
		want := 0
		switch p.Kind {
		case ABIInt, ABIFloat, ABIBool, ABIError, ABIStruct, ABIPtr, ABIString, ABIOpaque, ABIArray, ABIMap, ABIAny:
			want = 1
		case ABISlice:
			switch a.Frontend {
			case "gossa":
				want = 4
			case "llvm":
				want = 3
			default:
				want = len(p.Model)
			}
			if p.Elem == "" {
				errs = append(errs, fmt.Errorf("abi %s: param %d (%s): slice needs elem", a.GoFunc, i, p.Go))
			}
		default:
			errs = append(errs, fmt.Errorf("abi %s: param %d (%s): unknown kind %q", a.GoFunc, i, p.Go, p.Kind))
			continue
		}
		if len(p.Model) != want {
			errs = append(errs, fmt.Errorf("abi %s: param %d (%s): kind %s expects %d model names, got %d",
				a.GoFunc, i, p.Go, p.Kind, want, len(p.Model)))
		}
	}
	return errors.Join(errs...)
}
