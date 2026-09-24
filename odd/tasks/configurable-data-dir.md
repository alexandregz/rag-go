# Feature: configurable data directory (`-data` / `DATA_DIR`)

Feature file: `odd/tasks/configurable-data-dir.md` · Branch: `feat/flag-data-dir`
Mirror: Engram topic `odd/configurable-data-dir/tasks`

## Objective

Make pdfbot work against a configurable data directory instead of the process
working directory: PDF corpus, vector index (`db_vectores.gob`), consult logs and
the `/docs/` file server all honor a new CLI flag `-data` (default: env var
`DATA_DIR`, fallback `.` — current behavior unchanged).

## Problem

`main.go` resolves everything relative to the cwd: `filepath.Glob("*.pdf")` in
`indexarPDFs` (line ~918), `IndexFile` open in `cargarDB` (~144) and write in
`indexarPDFs` (~952), `logs/consultas_*.log` in `rexistrarConsulta` (~358-386),
and `http.Dir(".")` in `iniciarServidorWeb` (~314). The Docker entrypoint relied
on a `cd /data` hack, which is fragile and produced
`Non se atoparon PDFs no directorio actual` when the mounted host volume
(`data/`) is empty or when cwd assumptions break.

## Why

The user runs the container from the Container app (macOS) with
`-v "$(pwd)/data:/data"` and hit the empty-corpus failure. An explicit data
directory (flag + env var, consistent with the existing `HOST`/`PORT`/
`OLLAMA_URL` pattern via `envOrDefault`) is the robust fix: no more cwd tricks,
and the entrypoint passes `-data /data` explicitly.

## Scope

- `main.go`: package-level `var DataDir = "."`; `-data` flag registered in
  `main()` with default `envOrDefault("DATA_DIR", ".")`; `DataDir` assigned after
  `flag.Parse()`; used in `indexarPDFs` (glob, `Chunk.Source` → `filepath.Base`,
  index write), `cargarDB` (index open), `rexistrarConsulta` (logs dir),
  `iniciarServidorWeb` (`http.Dir(DataDir)`).
- `docker-entrypoint.sh`: drop `cd /data`; `DATA_DIR="${DATA_DIR:-/data}"`;
  pass `-data "${DATA_DIR}"` to both `-index` and `-web`; index check at
  `${DATA_DIR}/db_vectores.gob`.
- `README.md`: document `-data` / `DATA_DIR` (env-vars table row + brief note in
  usage sections).

## Constraints

- Behavior identical when neither `-data` nor `DATA_DIR` is set (default `.`).
- `Dockerfile` unchanged (`WORKDIR /data` + `VOLUME /data` stay; the entrypoint
  now drives the path explicitly).
- Keep existing Galician user-facing CLI/log/UI messages (project convention).
- `Chunk.Source` must stay the PDF basename so `/docs/<Source>` links keep
  resolving against the file-server root `DataDir`.
- No commit by the worker; parent commits.

## Tasks

- `T1` — main.go: add `-data` flag (`DATA_DIR` fallback), `DataDir` global, wire
  into `indexarPDFs` / `cargarDB` / `rexistrarConsulta` / `iniciarServidorWeb`.
  Route: delegated (`gentle-ai-worker`) — trigger: multi-file write rule (2+
  non-trivial files, main.go 5 zones). TDD: off — source: no test suite in
  repository; runner: none. → DONE
- `T2` — docker-entrypoint.sh: `DATA_DIR` handling + `-data` passthrough.
  Route: delegated (same writer, same trigger). → DONE
- `T3` — README.md: document `-data` and `DATA_DIR`. Route: delegated. → DONE
- `T4` — Verify: `go build ./...`, `go vet ./...`, `sh -n docker-entrypoint.sh`,
  `go run . -h` shows `-data`; parent spot-check re-runs build; work-unit commit
  on `feat/flag-data-dir`; native risk assessment + verifier per returned plan.
  Route: inline parent (spot check + commit) + on-demand verifier. → IN PROGRESS

## Acceptance criteria

- `./pdfbot -index -data data/` and `./pdfbot -web -data data/` work from the
  repo root with `data/` holding the PDFs; `/docs/` lists them and links open.
- Container: `DATA_DIR=/data` (entrypoint default) → first run without index
  generates it from the mounted corpus, then serves on `$PORT`.
- Default behavior unchanged with no `-data` / `DATA_DIR`.
- `go build ./...` passes; entrypoint shell syntax valid; `-h` shows `-data`.

## Progress

- [x] T1 — main.go flag + DataDir wiring
- [x] T2 — docker-entrypoint.sh DATA_DIR + `-data`
- [x] T3 — README.md documentation
- [~] T4 — verification evidence + work-unit commit

## Verification evidence

- Writer (gentle-ai-worker, task mug2c0c3-1-7pcg): `go build ./...` success;
  `go vet ./...` success; `sh -n docker-entrypoint.sh` OK; `go run . -h` shows
  `  -data string`. Writer grep for old patterns (`cd /data`, `http.Dir(".")`,
  bare IndexFile open/create, `Source: archivo`, `Glob("*.pdf")`) → no matches.
- Parent spot-check: `go build ./...` → OK (re-run, pass).
- Native assessment (RDD off → `gentle_review assess`): first call unassessable
  (untracked `odd/` required declaration); resolved by committing the work-unit,
  then re-assessed over the committed range.
- (pending) independent verifier per returned plan; commit SHA.

## Next step

- Commit the work-unit (code + this feature doc), re-run `gentle_review assess`
  with `baseRef=12f01d6, committedOnly:true`, follow the returned plan
  (independent `gentle-ai-verify` if the plan says so), close T4, report.