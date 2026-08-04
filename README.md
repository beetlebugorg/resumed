# resumed

resumed tracks job applications. It builds a tailored resume and cover letter
for each one from a fact base. It is one Go binary and one SQLite database.

You can use it three ways:

- **MCP server** (`resumed mcp`). Claude Code drives this.
- **Web UI** (`resumed serve`). You use this to answer questions and read output.
- **Command line** (`resumed render`, `resumed import`, and so on).

The output is Typst. Typst compiles it to PDF.

## How it works

The database has two halves.

**The fact base** holds your profile, contacts, roles, bullets, projects,
patents, and skills. Every row is something that is true about you, and every
row has an id.

**The application track** holds jobs, notes, questions, one resume per job, and
one cover letter per job.

A tailored resume is a selection of facts. It is not free text. The
`save_resume` tool takes ids, not prose. An item can carry an `override_text`
value to reword a bullet for one posting, but the item cannot exist without a
fact row behind it. `CreateResume` rejects any id that the fact base does not
hold. That rule is what keeps a tailored resume honest.

When a posting needs something the fact base does not have, follow this path:

```
ask_questions  ->  you answer in the web UI  ->  add_facts
```

New material enters through you, and the questions record where it came from.
Nothing is invented at render time.

**Cover letters work differently.** A letter argues instead of selecting, so
resumed stores it as text. The schema cannot enforce honesty here. Instead, the
tool description tells the assistant to trace every claim back to a fact. A
`rationale` field records which facts the letter uses. Write a letter with
`save_cover_letter`.

**Retire a fact** when it is superseded, worded badly, or no longer worth
claiming. Retiring keeps the row but hides it from new tailoring. It is not a
delete. A resume you already sent points at its facts by id and must render the
same way later, so the renderer still sees retired facts. Retire a fact on the
Facts page or with the `retire_facts` tool. Pass `restore` to undo it.

**Each job has one resume and one cover letter.** Tailoring again replaces the
old one. The files are always `resume.typ`, `resume.pdf`, `cover-letter.typ`,
and `cover-letter.pdf`.

## Install

```sh
brew install typst          # optional; resumed always writes the .typ file
make build                  # or: go build -o bin/resumed ./cmd/resumed
bin/resumed import seed/facts.json
```

`seed/facts.json` holds example data. Replace it with your own facts.

## Register the MCP server

```sh
bin/resumed install --db ../resumed.db --out ../jobs
```

This writes `.mcp.json` in the current directory. The paths in it are absolute.
Claude Code starts the server with a working directory that you do not control,
so a relative path would point at the wrong place. The command keeps any other
servers already in the file.

| Flag | Meaning |
| --- | --- |
| `--scope project` | Write `.mcp.json` in the current directory. This is the default. |
| `--scope local` | Register for you only, in this project only. |
| `--scope user` | Register for every project you open. |
| `--name` | Set the server name that Claude Code shows. The default is `resumed`. |
| `--force` | Replace an entry that has the same name. |
| `--print` | Show what the command would write, and change nothing. |

Project scope is merged into the file directly. Local scope and user scope live
in `~/.claude.json`, so `install` calls `claude mcp add` for those instead of
editing that file. If the `claude` binary is missing, `install` prints the
command for you to run.

Restart Claude Code afterwards, or run `/mcp`. A project server needs your
approval the first time it runs.

## Run the web UI

```sh
make serve DB=../resumed.db OUT=../jobs
```

The UI binds to `127.0.0.1:7777` by default. To reach it from another device,
run `make serve-lan`. Read the warning it prints first. The UI has no
authentication. It shows your contact details, your employment history, and
every application you are tracking.

Three flags work on every command: `--db` (or `RESUMED_DB`), `--out` (or
`RESUMED_OUT`), and `--addr` (or `RESUMED_ADDR`). The defaults live in
`~/.resumed/`.

## Use it

Ask Claude Code something like this:

> Tailor my resume for https://example.com/careers/staff-backend-engineer

Claude Code adds the job, reads the fact base, tailors a resume, and usually
queues a few questions. Answer them at <http://localhost:7777/questions>. Then
ask for the answers to be added and the resume to be tailored again. The files
are written to `jobs/<company>/<title>/`.

## MCP tools

| Tool | Purpose |
| --- | --- |
| `get_fact_base` | Return every fact with its id. Read this before tailoring. |
| `add_facts` | Add confirmed answers to the fact base. |
| `retire_facts` | Retire a fact from new tailoring, or restore it. |
| `add_job` | Save a posting. Fetches and parses the URL. |
| `list_jobs`, `get_job` | Browse tracked jobs. |
| `update_job` | Change a job's status or metadata. |
| `add_job_note` | Add a dated note to a job. |
| `ask_questions` | Queue questions for you to answer. |
| `list_questions` | Check for answers. |
| `save_resume` | Tailor a resume by selecting fact ids. Renders Typst and PDF. |
| `get_resume` | Return a job's resume and its Typst source. |
| `render_resume` | Render a resume again after its facts change. |
| `save_cover_letter` | Write or replace a job's cover letter. Renders Typst and PDF. |
| `get_cover_letter` | Return a job's cover letter and its Typst source. |
| `render_cover_letter` | Render a cover letter again. |

## Job fetching

`add_job` reads a posting in four steps and uses the best source it finds.
First it looks for schema.org `JobPosting` data in JSON-LD. Then it reads Open
Graph tags. Then it takes the employer name from the URL, for job boards it
recognises. Last it splits the `<title>` on "role at company".

No single step covers every board. Greenhouse serves no JSON-LD and a
placeholder `<title>`, so its data comes only from Open Graph and the URL.

A page built by JavaScript may yield nothing. resumed still saves the job. Paste
the description into the web UI, or send it with `update_job`.

## Web UI

The server renders HTML and uses [htmx](https://htmx.org), which is vendored
rather than loaded from a CDN. Every endpoint that changes something returns the
fragment it changed. The same handler serves a full page on a cold load and a
fragment to htmx. There is no JSON API and no client state.

- `/` lists tracked jobs. Filter by status, or add a job by URL.
- `/jobs/{id}` shows the posting, the resume, the cover letter, notes, questions, and status.
- `/questions` is where you answer what the assistant asked. You can edit an answer later.
- `/facts` browses the fact base with ids. Retire and restore facts here.

Editing an answer keeps its original answer date, so fixing a typo does not make
the answer look new. You can revise an answer from the questions page or the job
page. The fact base does not update by itself, so ask for the resume to be
tailored again afterwards.

## Layout

```
cmd/resumed/         CLI: mcp | serve | import | export | render | render-cover
internal/store/      schema, queries, and Assemble (resume -> renderable document)
internal/render/     Typst generation, escaping, and the typst compile step
internal/fetch/      job posting fetch
internal/mcpsrv/     MCP tool definitions
internal/web/        htmx UI, templates, and static assets
internal/app/        operations shared by all three interfaces
seed/facts.json      example fact base
```

## Tests

```sh
make check     # gofmt, vet, and tests
make test      # tests only
```

`internal/render` compiles difficult text through the real typst binary. The
text contains every character that Typst treats as markup, such as `C++`,
`mod_dims`, `#1`, `80%`, and a leading `-` or `=`. The test then asserts that
escaping loses nothing. A resume that drops a character quietly is worse than
one that fails to build.

`internal/store` tests the retirement rule: a resume that cites a fact must
still render after you retire that fact. If that breaks, retiring a fact
corrupts a resume you may have already sent.

`internal/app` tests that output paths resolve to absolute paths. The database
records where each file was written, so a relative path would only work from the
directory that ran the render.

## License

MIT. See [LICENSE](LICENSE).
