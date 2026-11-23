package mine

import (
	"context"
	"strings"
	"testing"
)

type fakeLLM struct {
	answers []string
	prompts []string
}

func (f *fakeLLM) Complete(_ context.Context, system, user string) (string, error) {
	f.prompts = append(f.prompts, user)
	a := f.answers[0]
	if len(f.answers) > 1 {
		f.answers = f.answers[1:]
	}
	return a, nil
}

func TestPropose(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/frame\n")
	write(t, dir, "frame.go", pkgSrc)
	write(t, dir, "wire/wire.go", "// Package wire encodes frames.\npackage wire\n\ntype Enc struct{}\n\nfunc (e *Enc) Put(b []byte) {}\n")
	r := &Report{Module: "example.com/frame", Evidence: []Evidence{
		{Kind: KindCommit, Ref: "abc123", Title: "fix panic on an empty frame", Text: "diff", Paths: []string{"frame.go"}, Before: "p0", After: "abc123full"},
		{Kind: KindAdvisory, Ref: "GHSA-x", Title: "other impl crashes on short frames", Project: "crates.io:other"},
	}}
	llm := &fakeLLM{answers: []string{"Here:\n```json\n" + `[
 {"property":"Parse never panics, whatever the bytes","harm":"a peer crashes the server","oracle":"no panic","inputs":["empty frame","short length"],"kind":"security","priority":1,
  "targets":[{"package":"example.com/frame","func":"Parse"},{"package":".","func":"Nope"}],"evidence":["abc123","GHSA-x"]},
 {"property":"parse never panics, whatever the bytes ","kind":"security","priority":1,"targets":[{"package":".","func":"Parse"}],"evidence":["GHSA-x"]},
 {"property":"Put keeps order","kind":"correctness","priority":3,"targets":[{"package":"wire","func":"(*Enc).Put"}],"evidence":["GHSA-x"]},
 {"property":"Unknown","kind":"correctness","priority":2,"targets":[{"package":"wire","func":"Missing"}],"evidence":["GHSA-x"]}
]` + "\n```"}}
	p, err := Propose(context.Background(), llm, r, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Candidates) != 2 {
		t.Fatalf("want 2 candidates (one duplicate, one with no known target), got %+v", p.Candidates)
	}
	c := p.Candidates[0]
	if c.ID != "c001" || len(c.Targets) != 1 || c.Targets[0] != (Target{".", "Parse"}) || c.Before != "p0" || c.After != "abc123full" {
		t.Errorf("first candidate: %+v", c)
	}
	if p.Candidates[1].Targets[0] != (Target{"wire", "Enc.Put"}) || p.Candidates[1].After != "" {
		t.Errorf("second candidate: %+v", p.Candidates[1])
	}
	if len(p.Dropped) != 3 {
		t.Errorf("dropped: %v", p.Dropped)
	}
	// The prompt lists the commit's package's functions and, for the
	// advisory without files, the package index.
	pr := llm.prompts[0]
	for _, want := range []string{"package .: Ack, Parse, helper", "package wire: Package wire encodes frames.", "(an advisory of a similar project: crates.io:other)", "before p0, after abc123full"} {
		if !strings.Contains(pr, want) {
			t.Errorf("prompt lacks %q:\n%s", want, pr)
		}
	}
}

func TestProposeBadAnswer(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/frame\n")
	write(t, dir, "frame.go", pkgSrc)
	r := &Report{Module: "example.com/frame", Evidence: []Evidence{{Kind: KindDoc, Ref: "frame.go:1", Title: "x", Paths: []string{"frame.go"}}}}
	p, err := Propose(context.Background(), &fakeLLM{answers: []string{"I cannot help with that."}}, r, dir)
	if err != nil || len(p.Candidates) != 0 || len(p.Dropped) != 1 {
		t.Errorf("err %v, proposal %+v", err, p)
	}
}

func TestProposeRepairsGuessedTargets(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/frame\n")
	write(t, dir, "frame.go", pkgSrc)
	r := &Report{Module: "example.com/frame", Evidence: []Evidence{{Kind: KindAdvisory, Ref: "GHSA-y", Title: "t", Project: "crates.io:other"}}}
	llm := &fakeLLM{answers: []string{
		`[{"property":"Parse rejects short frames","kind":"security","priority":1,"targets":[{"package":".","func":"ParseFrame"}],"evidence":["GHSA-y"]},
		  {"property":"elsewhere","kind":"security","priority":1,"targets":[{"package":"nope","func":"X"}],"evidence":["GHSA-y"]}]`,
		`[{"targets":[{"package":".","func":"Parse"}]}]`,
	}}
	p, err := Propose(context.Background(), llm, r, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Candidates) != 1 || p.Candidates[0].Targets[0] != (Target{".", "Parse"}) {
		t.Fatalf("candidates %+v, dropped %v", p.Candidates, p.Dropped)
	}
	if len(llm.prompts) != 2 || !strings.Contains(llm.prompts[1], "Guessed targets: . ParseFrame") || !strings.Contains(llm.prompts[1], "package .: Ack, Parse, helper") {
		t.Errorf("repair prompt: %v", llm.prompts)
	}
}
