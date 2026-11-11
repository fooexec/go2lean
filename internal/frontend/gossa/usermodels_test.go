package gossa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A user model stands for the dependency method it names: the target's
// call goes to the model's package (#204). A model whose signature does not
// match, or that names nothing, is an error.
func TestUserModels(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write("sdk/sdk.go", `package sdk

type Client struct{ url string }

// Get talks to a server the model does not know.
func (c *Client) Get(key string) (string, error) { return remote(c.url, key) }

func remote(url, key string) (string, error)
`)
	write("sdk/stub.s", "")
	write("app/app.go", `package app

import "example.com/m/sdk"

func Name(c *sdk.Client) string {
	v, err := c.Get("name")
	if err != nil {
		return ""
	}
	return v
}
`)
	models := filepath.Join(dir, ".go2lean", "models")
	write(".go2lean/models/sdk.go", `package models

import "example.com/m/sdk"

//go2lean:model example.com/m/sdk.(*Client).Get
func ClientGet(c *sdk.Client, key string) (string, error) { return "v-" + key, nil }
`)
	progs, _, err := TranslateAllOpts(dir, Options{Deps: true, Models: models}, "./app")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range progs {
		for _, f := range p.Funcs {
			for _, x := range f.XCalls {
				if x.Package == usermodelsPath {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("app's call of (*sdk.Client).Get does not go to the user model")
	}

	write(".go2lean/models/sdk.go", `package models

import "example.com/m/sdk"

//go2lean:model example.com/m/sdk.(*Client).Get
func ClientGet(c *sdk.Client, key int) (string, error) { return "", nil }
`)
	if _, _, err := TranslateAllOpts(dir, Options{Deps: true, Models: models}, "./app"); err == nil || !strings.Contains(err.Error(), "want func") {
		t.Errorf("a mismatched signature: err = %v", err)
	}
	write(".go2lean/models/sdk.go", `package models

//go2lean:model example.com/m/sdk.(*Client).Put
func ClientPut() {}
`)
	if _, _, err := TranslateAllOpts(dir, Options{Deps: true, Models: models}, "./app"); err == nil || !strings.Contains(err.Error(), "no method Put") {
		t.Errorf("a missing target: err = %v", err)
	}
}
