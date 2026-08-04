// Command resumed tracks job applications and tailors resumes from a factual
// base. It has two faces over one SQLite database: an MCP server that Claude
// drives, and a web UI for the human.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/beetlebugorg/resumed/internal/app"
	"github.com/beetlebugorg/resumed/internal/install"
	"github.com/beetlebugorg/resumed/internal/mcpsrv"
	"github.com/beetlebugorg/resumed/internal/store"
	"github.com/beetlebugorg/resumed/internal/web"
)

const version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "resumed: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `resumed — job tracker and resume tailor

usage:
  resumed install [flags]          register this MCP server with Claude
  resumed mcp [flags]              serve MCP over stdio (for Claude)
  resumed serve [flags]            serve the web UI
  resumed import <file.json>       load a fact base into the database
  resumed export                   dump the fact base as JSON
  resumed render <resume-id>       re-render a stored resume version
  resumed render-cover <id>        re-render a stored cover letter

flags:
  --db      path to the SQLite database (env RESUMED_DB)
  --out     directory for generated resumes (env RESUMED_OUT)
  --addr    listen address for serve (default 127.0.0.1:7777)

install flags:
  --scope   project (.mcp.json, default), local, or user
  --name    server name Claude will show (default resumed)
  --force   replace an existing entry with the same name
  --print   show what would be written, change nothing

examples:
  resumed install                       # register in ./.mcp.json for this repo
  resumed install --scope user          # register for every project you open
  resumed install --print               # preview the config
`)
}

func defaultPath(env, rel string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return rel
	}
	return filepath.Join(home, ".resumed", rel)
}

func run() error {
	if len(os.Args) < 2 {
		usage()
		return fmt.Errorf("no command given")
	}
	cmd := os.Args[1]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dbPath := fs.String("db", defaultPath("RESUMED_DB", "resumed.db"), "path to the SQLite database")
	outRoot := fs.String("out", defaultPath("RESUMED_OUT", "jobs"), "directory for generated resumes")
	addr := fs.String("addr", envOr("RESUMED_ADDR", "127.0.0.1:7777"), "listen address for the web UI")
	scope := fs.String("scope", "project", "install scope: project, local, or user")
	name := fs.String("name", "resumed", "MCP server name")
	force := fs.Bool("force", false, "replace an existing entry")
	printOnly := fs.Bool("print", false, "show what would be written, change nothing")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	args := fs.Args()

	switch cmd {
	case "help", "-h", "--help":
		usage()
		return nil
	case "install", "mcp", "serve", "import", "export", "render", "render-cover":
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}

	// install touches no data, so it runs before the database is opened —
	// registering the server should work on a machine that has never run it.
	if cmd == "install" {
		return runInstall(install.Options{
			Scope: *scope, Name: *name, DBPath: *dbPath, OutRoot: *outRoot,
			Force: *force, Print: *printOnly,
		})
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	a := app.New(st, *outRoot)

	switch cmd {
	case "mcp":
		return runMCP(a)
	case "serve":
		return web.Serve(a, *addr)
	case "import":
		if len(args) != 1 {
			return fmt.Errorf("import needs exactly one file")
		}
		return importFacts(st, args[0])
	case "export":
		fb, err := st.FactBase()
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(fb)
	case "render":
		if len(args) != 1 {
			return fmt.Errorf("render needs a resume id")
		}
		var id int64
		if _, err := fmt.Sscan(args[0], &id); err != nil {
			return fmt.Errorf("bad resume id %q", args[0])
		}
		res, err := a.Render(id)
		if err != nil {
			return err
		}
		fmt.Println(res.TypstPath)
		if res.PDFPath != "" {
			fmt.Println(res.PDFPath)
		}
		if res.Warning != "" {
			fmt.Fprintln(os.Stderr, "warning: "+res.Warning)
		}
		return nil
	case "render-cover":
		if len(args) != 1 {
			return fmt.Errorf("render-cover needs a cover letter id")
		}
		var id int64
		if _, err := fmt.Sscan(args[0], &id); err != nil {
			return fmt.Errorf("bad cover letter id %q", args[0])
		}
		res, err := a.RenderCover(id)
		if err != nil {
			return err
		}
		fmt.Println(res.TypstPath)
		if res.PDFPath != "" {
			fmt.Println(res.PDFPath)
		}
		if res.Warning != "" {
			fmt.Fprintln(os.Stderr, "warning: "+res.Warning)
		}
		return nil
	}
	return nil
}

func runInstall(opts install.Options) error {
	res, err := install.Run(opts)
	if err != nil {
		return err
	}
	if opts.Print {
		return nil
	}
	fmt.Printf("%s %q in scope %s\n", res.Action, res.Name, res.Scope)
	fmt.Printf("  config:  %s\n", res.Target)
	fmt.Printf("  command: %s\n", strings.Join(res.Command, " "))
	for _, n := range res.Notes {
		fmt.Printf("  note:    %s\n", n)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func runMCP(a *app.App) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := mcpsrv.New(a, version)
	// Anything written to stdout would corrupt the protocol stream, so status
	// goes to stderr.
	fmt.Fprintf(os.Stderr, "resumed mcp: db=%s out=%s\n", a.Store.Path, a.OutRoot)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// importFacts loads a fact base JSON document, replacing what is there. Ids in
// the file are ignored; fresh ones are assigned.
func importFacts(st *store.Store, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var fb store.FactBase
	if err := json.Unmarshal(raw, &fb); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	if err := st.SetProfile(fb.Profile); err != nil {
		return err
	}
	for i, c := range fb.Contacts {
		c.Position = i
		if _, err := st.AddContact(c); err != nil {
			return err
		}
	}
	for i, r := range fb.Roles {
		r.Position = i
		roleID, err := st.AddRole(r)
		if err != nil {
			return err
		}
		for j, b := range r.Bullets {
			b.RoleID, b.Position = roleID, j
			if _, err := st.AddBullet(b); err != nil {
				return err
			}
		}
	}
	for i, p := range fb.Projects {
		p.Position = i
		projID, err := st.AddProject(p)
		if err != nil {
			return err
		}
		for j, b := range p.Bullets {
			b.ProjectID, b.Position = projID, j
			if _, err := st.AddProjectBullet(b); err != nil {
				return err
			}
		}
	}
	for i, p := range fb.Patents {
		p.Position = i
		if _, err := st.AddPatent(p); err != nil {
			return err
		}
	}
	for i, sk := range fb.Skills {
		sk.Position = i
		if _, err := st.AddSkill(sk); err != nil {
			return err
		}
	}

	fmt.Printf("imported %d roles, %d projects, %d patents, %d skills\n",
		len(fb.Roles), len(fb.Projects), len(fb.Patents), len(fb.Skills))
	return nil
}
