# resumed

A job tracker and resume tailor. One Go binary, one SQLite database, two faces:

- **MCP server** (`resumed mcp`) — what Claude drives.
- **Web UI** (`resumed serve`) — what you use.

Output is Typst, compiled to PDF with the ATS-hardened template from
`templates/ats-resume.typ`.

## The idea

The database has two halves.

**The fact base** — profile, contacts, roles, bullets, projects, patents,
skills. Everything that is actually true about you, each row with an id.

**The application track** — jobs, notes, questions, and one resume per job.

A tailored resume is a *selection over the fact base*, never free text.
`save_resume` takes ids, not prose. An item may carry an `override_text` to
reword a bullet for a particular posting, but it cannot exist without a fact row
behind it, and `CreateResume` rejects any id that is not in the fact base. That
single constraint is what stops a tailored resume from drifting into fiction.

When a posting wants something the fact base does not cover, the path is:

```
ask_questions  ->  you answer in the web UI  ->  add_facts
```

so new material enters through you, with an audit trail, rather than being
invented at render time.

**Retiring a fact** keeps the row but stops new resumes using it — for facts
that were superseded, worded badly, or are no longer worth claiming. Retirement
is deliberately not a delete: the resume already generated for a job references
its facts by id and must keep rendering exactly as it was, so retired facts stay
visible to the renderer and are hidden only from new tailoring. Retire from the
Facts page or with the `retire_facts` tool; `restore` undoes it.

**One resume per job.** Re-tailoring replaces the job's resume rather than
stacking versions, and always writes `resume.typ` / `resume.pdf`.

## Setup

```sh
brew install typst                     # PDF rendering (optional; .typ is always written)
go build -o bin/resumed ./cmd/resumed
bin/resumed import seed/facts.json     # load the fact base
```

Register the MCP server with Claude:

```sh
bin/resumed install --db ../resumed.db --out ../jobs
```

That writes `.mcp.json` in the current directory with **absolute** paths —
Claude launches the server with a working directory you do not control, so
anything relative would resolve wrong. Existing servers in the file are kept.

| Flag | Meaning |
| --- | --- |
| `--scope project` | `.mcp.json` in the current directory (default; commit it to share) |
| `--scope local` | just you, just this project |
| `--scope user` | every project you open |
| `--name` | server name Claude shows (default `resumed`) |
| `--force` | replace an existing entry of the same name |
| `--print` | show what would be written, change nothing |

`project` scope is merged directly. `local` and `user` live in `~/.claude.json`,
so those delegate to `claude mcp add` rather than hand-editing the CLI's own
config; if the `claude` binary is missing, install prints the command to run.
Restart Claude Code (or `/mcp`) afterwards — project servers need approval on
first use.

Run the site:

```sh
bin/resumed serve --db ../resumed.db --out ../jobs --addr 0.0.0.0:7777
```

Flags apply to every command: `--db` (env `RESUMED_DB`), `--out`
(env `RESUMED_OUT`), `--addr` (env `RESUMED_ADDR`). Defaults live under
`~/.resumed/`.

## Using it

Ask Claude something like:

> Tailor my resume for https://example.com/careers/staff-backend-engineer

Claude will add the job, read the fact base, tailor a resume, and usually queue
a couple of questions. Answer those at <http://localhost:7777/questions>, then
tell Claude to fold them in and re-tailor. Generated files land in
`jobs/<company>/<title>/resume.{typ,pdf}`.

## MCP tools

| Tool | Purpose |
| --- | --- |
| `get_fact_base` | Every fact with its id — read before tailoring |
| `add_facts` | Promote confirmed answers into the fact base |
| `add_job` | Save a posting; fetches and parses the URL |
| `list_jobs` / `get_job` | Browse tracked jobs |
| `update_job` | Change status or metadata |
| `add_job_note` | Timestamped note on a job |
| `ask_questions` | Queue questions for the user |
| `list_questions` | Check for answers |
| `retire_facts` | Retire a fact from new tailoring, or restore it |
| `save_resume` | Tailor by selecting fact ids; renders Typst + PDF |
| `get_resume` | Fetch a job's resume with its Typst source |
| `render_resume` | Re-render after facts change |

Job fetching runs in layers, best source first: schema.org `JobPosting` JSON-LD,
then Open Graph tags, then the employer slug in the URL for known ATS hosts,
then the `<title>` split on "role at company". No single layer covers the field —
Greenhouse's `job-boards.greenhouse.io`, probably the most common board, serves
no JSON-LD at all and a placeholder `<title>`, so its metadata comes entirely
from Open Graph and the URL. A JavaScript-rendered page that yields nothing
still saves the job — paste the description in the UI or via `update_job`.

## Web UI

Server-rendered HTML with [htmx](https://htmx.org) (vendored, no CDN). Every
mutating endpoint returns the fragment it changed, so the same handler serves a
full page on a cold load and a partial to htmx. No JSON API, no client state.

- `/` — tracked jobs, filter by status, add by URL
- `/jobs/{id}` — posting, the current resume, notes, questions, status
- `/questions` — answer what Claude asked; answered ones can be edited in place
- `/facts` — browse the fact base with ids; retire and restore facts

Editing an answer preserves its original answered-at date, so fixing a typo does
not make the answer look new. Answers can be revised from either the questions
inbox or the job page. After changing one, ask Claude to re-tailor — the fact
base is not updated automatically.

## Layout

```
cmd/resumed/         CLI: mcp | serve | import | export | render
internal/store/      schema, queries, and Assemble (resume -> renderable doc)
internal/render/     Typst generation + escaping, typst compile
internal/fetch/      job posting fetch: JSON-LD then HTML text
internal/mcpsrv/     MCP tool definitions
internal/web/        htmx UI, templates, static assets
internal/app/        operations shared by MCP and web
seed/facts.json      importable fact base
```

## Tests

```sh
go test ./...
```

`internal/render` compiles adversarial bullet text (`C++`, `mod_dims`, `#1`,
`80%`, leading `-`/`=`) through real typst and asserts escaping is lossless —
silently swallowing a character is worse than failing to build.

`internal/store` covers the retirement invariant: a resume that cites a fact
must still render after that fact is retired. If that breaks, retiring a fact
silently corrupts a resume you may already have sent.
