// Package install registers the resumed MCP server with Claude.
//
// Two paths, chosen by scope:
//
//   - "project" writes .mcp.json next to the repo. The format is small and
//     stable, so we merge it directly and keep any other servers already there.
//   - "local" and "user" live inside ~/.claude.json, a large config owned by
//     the Claude CLI. We shell out to `claude mcp add` rather than hand-merge
//     someone else's file.
package install

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Options describes the registration to perform.
type Options struct {
	Scope   string // project | local | user
	Name    string // server name as Claude will show it
	DBPath  string
	OutRoot string
	Dir     string // project directory for scope=project
	Force   bool   // replace an existing entry
	Print   bool   // show what would happen, change nothing
}

// Result reports what was done, for printing.
type Result struct {
	Scope   string
	Name    string
	Target  string   // file written, or the command run
	Command []string // argv Claude will launch
	Action  string   // created | updated | would-create | would-update
	Notes   []string
}

// Server is one entry in the mcpServers map.
type Server struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// ValidScopes are the accepted --scope values.
var ValidScopes = []string{"project", "local", "user"}

// Run performs the installation.
func Run(opts Options) (*Result, error) {
	if opts.Name == "" {
		opts.Name = "resumed"
	}
	if opts.Scope == "" {
		opts.Scope = "project"
	}
	if !validScope(opts.Scope) {
		return nil, fmt.Errorf("unknown scope %q; want one of %s", opts.Scope, strings.Join(ValidScopes, ", "))
	}

	bin, err := selfPath()
	if err != nil {
		return nil, err
	}
	dbPath, err := filepath.Abs(opts.DBPath)
	if err != nil {
		return nil, err
	}
	outRoot, err := filepath.Abs(opts.OutRoot)
	if err != nil {
		return nil, err
	}

	// Absolute paths throughout: Claude launches the server with a working
	// directory we do not control, so anything relative would resolve wrong.
	argv := []string{bin, "mcp", "--db", dbPath, "--out", outRoot}

	res := &Result{Scope: opts.Scope, Name: opts.Name, Command: argv}
	if note := binaryWarning(bin); note != "" {
		res.Notes = append(res.Notes, note)
	}
	if _, err := exec.LookPath("typst"); err != nil {
		res.Notes = append(res.Notes,
			"typst is not on PATH — resumes will still generate .typ source, but no PDF (brew install typst)")
	}

	if opts.Scope == "project" {
		return installProject(opts, res, argv)
	}
	return installViaCLI(opts, res, argv)
}

func validScope(s string) bool {
	for _, v := range ValidScopes {
		if v == s {
			return true
		}
	}
	return false
}

// selfPath resolves the running binary to a stable absolute path.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate this binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

// binaryWarning catches `go run`, where the binary lives in a temp dir that
// disappears — Claude would then fail to start the server.
func binaryWarning(bin string) string {
	tmp := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = resolved
	}
	if strings.HasPrefix(bin, tmp) || strings.Contains(bin, "/go-build") {
		return "this binary is in a temporary directory (go run?) — build it first with " +
			"`go build -o bin/resumed ./cmd/resumed`, then run install from that binary"
	}
	return ""
}

// ---------------------------------------------------------------- project

// mcpConfig mirrors .mcp.json loosely: the servers map is typed, everything
// else is preserved verbatim so we never drop keys we do not understand.
func installProject(opts Options, res *Result, argv []string) (*Result, error) {
	dir := opts.Dir
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(dir, ".mcp.json")
	res.Target = path

	root := map[string]json.RawMessage{}
	existed := false
	if raw, err := os.ReadFile(path); err == nil {
		existed = true
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &root); err != nil {
				return nil, fmt.Errorf("parse %s: %w (fix or remove it, then retry)", path, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	servers := map[string]json.RawMessage{}
	if raw, ok := root["mcpServers"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, fmt.Errorf("parse mcpServers in %s: %w", path, err)
		}
	}

	_, replacing := servers[opts.Name]
	if replacing && !opts.Force {
		return nil, fmt.Errorf("%s already has a server named %q; pass --force to replace it", path, opts.Name)
	}

	entry, err := json.Marshal(Server{Command: argv[0], Args: argv[1:]})
	if err != nil {
		return nil, err
	}
	servers[opts.Name] = entry

	encoded, err := json.Marshal(servers)
	if err != nil {
		return nil, err
	}
	root["mcpServers"] = encoded

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	out = append(out, '\n')

	switch {
	case opts.Print:
		res.Action = actionLabel(replacing || existed, true)
		fmt.Print(string(out))
		return res, nil
	default:
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
		res.Action = actionLabel(replacing, false)
	}
	res.Notes = append(res.Notes,
		"restart Claude Code (or run /mcp) to pick up the change; project servers need approval on first use")
	return res, nil
}

func actionLabel(replacing, dry bool) string {
	switch {
	case dry && replacing:
		return "would-update"
	case dry:
		return "would-create"
	case replacing:
		return "updated"
	default:
		return "created"
	}
}

// ------------------------------------------------------------------- CLI

func installViaCLI(opts Options, res *Result, argv []string) (*Result, error) {
	// `claude mcp add -s <scope> <name> -- <command> [args...]`; the -- keeps
	// our --db/--out flags from being parsed by the claude CLI itself.
	cmdArgs := []string{"mcp", "add", "-s", opts.Scope, opts.Name, "--"}
	cmdArgs = append(cmdArgs, argv...)
	res.Target = "claude " + strings.Join(cmdArgs, " ")

	claude, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("the `claude` CLI is not on PATH, so scope %q cannot be written automatically.\n"+
			"Run this once Claude Code is installed:\n\n  %s\n", opts.Scope, res.Target)
	}
	if opts.Print {
		res.Action = "would-create"
		fmt.Println(res.Target)
		return res, nil
	}

	if opts.Force {
		// Remove first so re-running install is idempotent rather than an error.
		_ = exec.Command(claude, "mcp", "remove", "-s", opts.Scope, opts.Name).Run()
	}
	cmd := exec.Command(claude, cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if strings.Contains(trimmed, "already exists") && !opts.Force {
			return nil, fmt.Errorf("a server named %q already exists in scope %s; pass --force to replace it",
				opts.Name, opts.Scope)
		}
		return nil, fmt.Errorf("claude mcp add failed: %w\n%s", err, trimmed)
	}
	res.Action = "created"
	if s := strings.TrimSpace(string(out)); s != "" {
		res.Notes = append(res.Notes, s)
	}
	return res, nil
}
