// Package agent is M2's agentic layer: an LLM proposes spec-in-Go properties
// for a package, the pipeline hunts and verifies them, and the LLM turns the
// verdicts into a report. Every prompt and response is written to the
// workspace so a run can be inspected and replayed.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fooexec/go2lean/internal/sandbox"
)

// LLM is the one call the agent makes. Implementations: Claude (the
// Anthropic API) and Mock (scripted responses for tests and offline runs).
type LLM interface {
	Name() string
	Complete(ctx context.Context, system, user string) (string, error)
}

// New builds an LLM from a spec: "claude", "claude-code" (the claude CLI,
// with its own login) or "mock:<file-or-dir>". A mock
// directory holds round-1.md, round-2.md, ...; a mock file answers every call.
// cfg applies to "claude" only.
func New(spec string, cfg ClaudeConfig) (LLM, error) {
	switch {
	case spec == "" || spec == "claude":
		if cfg.Model == "" && cfg.BaseURL == "" {
			return NewClaude(), nil
		}
		return NewClaudeWith(cfg), nil
	case spec == "claude-code":
		effort := ""
		if cfg.Thinking {
			effort = "high"
		}
		return &ClaudeCode{Model: cfg.Model, Effort: effort}, nil
	case strings.HasPrefix(spec, "mock:"):
		return &Mock{Path: strings.TrimPrefix(spec, "mock:")}, nil
	}
	return nil, fmt.Errorf("unknown llm %q (want claude, claude-code or mock:<path>)", spec)
}

// Mock replays scripted responses.
type Mock struct {
	Path  string
	calls int
}

func (m *Mock) Name() string { return "mock:" + m.Path }

func (m *Mock) Complete(_ context.Context, _, _ string) (string, error) {
	m.calls++
	info, err := os.Stat(m.Path)
	if err != nil {
		return "", err
	}
	path := m.Path
	if info.IsDir() {
		path = filepath.Join(m.Path, fmt.Sprintf("round-%d.md", m.calls))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("mock response for call %d: %w", m.calls, err)
	}
	return string(b), nil
}

// ClaudeCode answers through the claude CLI (`claude -p`), with no tools
// and the given system prompt in place of its own: a completion, on the
// CLI's login rather than an API key.
type ClaudeCode struct {
	Model  string
	Effort string // --effort: low, medium, high, xhigh, max; empty: the CLI's default
	// MCPConfig, when set, is an --mcp-config file whose servers' tools
	// named in AllowedTools the model may call (the built-in tools stay
	// off), for up to MaxTurns turns.
	MCPConfig    string
	AllowedTools []string
	MaxTurns     int
	LastTurns    int
}

func (c *ClaudeCode) Name() string {
	if c.Model == "" {
		return "claude-code"
	}
	return "claude-code:" + c.Model
}

func (c *ClaudeCode) Complete(ctx context.Context, system, user string) (string, error) {
	args := []string{"-p", "--tools", "", "--system-prompt", system, "--no-session-persistence", "--output-format", "json"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	if c.MCPConfig != "" {
		args = append(args, "--mcp-config", c.MCPConfig, "--strict-mcp-config", "--allowedTools", strings.Join(c.AllowedTools, ","))
		if c.MaxTurns > 0 {
			args = append(args, "--max-turns", fmt.Sprint(c.MaxTurns))
		}
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = strings.NewReader(user)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude -p: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var res struct {
		Result   string  `json:"result"`
		IsError  bool    `json:"is_error"`
		Subtype  string  `json:"subtype"`
		NumTurns int     `json:"num_turns"`
		Cost     float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("claude -p: unreadable answer: %v", err)
	}
	c.LastTurns = res.NumTurns
	if res.IsError || strings.TrimSpace(res.Result) == "" {
		return "", fmt.Errorf("claude -p: no answer (%s, %d turns)", res.Subtype, res.NumTurns)
	}
	return res.Result, nil
}

// Turns is the number of turns the last answer took (tool calls included).
func (c *ClaudeCode) Turns() int { return c.LastTurns }

// LeanTools are the lean-lsp-mcp tools the proof agent may call: checking
// code and reading goals and diagnostics in the workspace's Lean project.
// The search tools that call out to the network are left off.
var LeanTools = []string{"lean_run_code", "lean_goal", "lean_term_goal", "lean_multi_attempt",
	"lean_diagnostic_messages", "lean_hover_info", "lean_local_search", "lean_file_outline", "lean_declaration_file"}

// WithLeanTools gives a claude-code LLM lean-lsp-mcp (the binary at bin)
// on a disposable copy of the Lean project at leanDir, made next to
// cfgPath (where the config is written). The server runs confined
// (sandbox.Confine): it, and the Lean code the agent sends it, can write
// only that copy and read only the toolchain, the core library and the
// server's own installation; no network. Other LLMs are returned as they
// are.
func WithLeanTools(llm LLM, bin, leanDir, cfgPath string) (LLM, error) {
	cc, ok := llm.(*ClaudeCode)
	if !ok {
		return llm, nil
	}
	work := filepath.Join(filepath.Dir(cfgPath), "lean-tools")
	if err := os.RemoveAll(work); err != nil {
		return nil, err
	}
	if err := copyLeanProject(leanDir, work); err != nil {
		return nil, err
	}
	work, err := filepath.Abs(work)
	if err != nil {
		return nil, err
	}
	ro := sandbox.LeanReadOnly()
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		// A venv's script, its interpreter and the interpreter's home
		// (uv installs both under the user's data directory).
		ro = append(ro, filepath.Dir(bin), filepath.Dir(filepath.Dir(filepath.Dir(real))))
	}
	if home, err := os.UserHomeDir(); err == nil {
		ro = append(ro, filepath.Join(home, ".local", "share", "uv"))
	}
	prefix, err := sandbox.Confine(ro, []string{work}, work)
	if err != nil {
		return nil, err
	}
	disabled := "lean_leansearch,lean_loogle,lean_leanfinder,lean_state_search,lean_hammer_premise,lean_build"
	argv := append(prefix, bin, "--lean-project-path", work, "--disable-tools", disabled)
	cfg := map[string]any{"mcpServers": map[string]any{"lean": map[string]any{
		"command": argv[0],
		"args":    argv[1:],
		"env":     map[string]string{"PATH": os.Getenv("PATH")},
	}}}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
		return nil, err
	}
	c := *cc
	c.MCPConfig = cfgPath
	c.AllowedTools = nil
	for _, t := range LeanTools {
		c.AllowedTools = append(c.AllowedTools, "mcp__lean__"+t)
	}
	c.MaxTurns = 60
	return &c, nil
}

// copyLeanProject copies a workspace's Lean project for the agent's tools:
// its sources and built modules (with their traces' outputs), not its
// executables or proof files.
func copyLeanProject(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			switch rel {
			case "Proofs", filepath.Join(".lake", "build", "bin"):
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		// Keep the times lake's traces were taken at.
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}
