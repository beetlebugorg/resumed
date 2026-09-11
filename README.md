# resumed

**An ATS-friendly resume builder and job application tracker that cannot make
things up.**

resumed tailors a resume and a cover letter for every job posting you track,
built from a fact base you confirmed. It runs as an MCP server for Claude Code,
as a local web app, and as a command line tool. Documents render through Typst
to PDF.

Ask an AI to write your resume and you get a good one. Parts of it will not be
true. resumed changes what a resume is: a selection from your own fact base,
chosen by id. The assistant picks which facts fit a posting and how to word them
for that reader. It cannot add a fact you never gave it.

One Go binary. One SQLite file. Your data stays on your machine.

## What you get

- **A resume and a cover letter for every application**, tailored to the
  posting and kept with the job.
- **PDFs built to survive applicant tracking systems.** Standard fonts, a
  single column, no icons or graphics, and real text the parser can read.
- **Emphasis per posting.** Bold the few terms this employer scans for. The
  same bullet can bold different terms for the next job.
- **A frozen copy of what you sent.** Mark a job applied and resumed saves the
  exact PDF. Later edits to your facts leave it alone.
- **Questions when a posting asks for something you have not recorded.** You
  answer in the browser, your answers become facts, and the next version uses
  them.
- **A record of every application**, with notes, status, and the reasoning
  behind each tailoring decision.

## How it stays honest

Your fact base holds your roles, bullets, projects, patents, and skills. Every
row is something true about you, and every row has an id.

A tailored resume is a list of those ids. An assistant can reorder them, drop
the weak ones, and reword a bullet for a posting. It cannot cite an id the fact
base does not hold, because resumed rejects it.

New material comes from you:

```
the assistant asks  ->  you answer in the browser  ->  the answer becomes a fact
```

Cover letters are prose, so the schema cannot police them the same way. Each
letter stores a rationale recording which facts it rests on, so you can check
the argument later.

## Requirements

- [Typst](https://typst.app) turns the generated source into a PDF. Homebrew
  installs it alongside resumed. Without it resumed still writes the `.typ`
  file.
- [Claude Code](https://claude.com/claude-code), or another MCP client, does the
  tailoring.
- [Go](https://go.dev) 1.26 or newer, to build from source.

## Install

```sh
brew install beetlebugorg/tap/resumed
```

Or build it:

```sh
make build
```

Then load a fact base. `seed/facts.json` in this repository is example data for
Pat Example of Springfield, IL:

```sh
resumed import seed/facts.json
```

Replace it with your own facts.

## Connect it to Claude Code

```sh
bin/resumed install --db ../resumed.db --out ../jobs
```

This writes `.mcp.json` in the current directory with absolute paths, because
Claude Code starts the server from a working directory you do not control. Any
other servers in the file are kept.

| Flag | Meaning |
| --- | --- |
| `--scope project` | Write `.mcp.json` here. The default. |
| `--scope local` | Register for you, in this project only. |
| `--scope user` | Register for every project you open. |
| `--name` | Set the server name Claude Code shows. Default `resumed`. |
| `--force` | Replace an entry with the same name. |
| `--print` | Show what the command writes without writing it. |

Restart Claude Code afterwards, or run `/mcp`.

## Run the web UI

```sh
make serve DB=../resumed.db OUT=../jobs
```

It binds to `127.0.0.1:7777`. Four pages:

- **Jobs** lists what you are tracking. Add a posting by URL.
- **A job page** shows the posting, your resume and cover letter, notes, and
  status.
- **Questions** is where you answer what the assistant asked.
- **Facts** browses your fact base and retires facts you no longer claim.

`make serve-lan` opens it to your network. Read the warning it prints first.
There is no authentication, and the pages show your contact details, your
history, and every application you are tracking.

Every command accepts `--db`, `--out`, and `--addr`, or the environment variables
`RESUMED_DB`, `RESUMED_OUT`, and `RESUMED_ADDR`. The defaults live in
`~/.resumed/`.

## Using it

Ask Claude Code:

> Tailor my resume for https://example.com/careers/staff-backend-engineer

It saves the posting, reads your fact base, writes a resume, and usually queues
a few questions. Answer them at <http://localhost:7777/questions>, then ask for
another pass. Files land in `jobs/<company>/<title>/`.

Some postings are built by JavaScript, so a fetch returns no description.
resumed saves the job anyway. Paste the description into the job page and carry
on.

When you send an application, mark the job applied. resumed keeps a copy of the
PDF as sent and stops further edits to it.

## Retiring a fact

Retire a fact when it is superseded or you no longer want to claim it. The row
stays and the resumes that already cite it still render, so a document you sent
last month reads the same today. Retire and restore on the Facts page.

## License

MIT. See [LICENSE](LICENSE).
