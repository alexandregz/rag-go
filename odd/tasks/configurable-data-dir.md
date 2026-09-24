# Feature: configurable data directory (`-data` / `DATA_DIR`)

Feature file: `odd/tasks/configurable-data-dir.md` · Branch: `feat/flag-data-dir`
Mirror: Engram topic `odd/configurable-data-dir/tasks`
Status: **CLOSED** (implemented, verified, committed)

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
- `T4` — Verify and commit. Writer self-verification: all 4 commands pass.
  Parent spot-check: `go build ./...` OK (re-run). Native assessment (RDD off):
  first call unassessable (untracked `odd/`), after commit `7892c84` re-assessed
  → risk high (`shell_process` signals on docker-entrypoint.sh) → independent
  verifier required. `gentle-ai-verify` (task mug2fcs1-2-yzbd):
  `status: pass` — build/vet/sh -n/`-h` all exit 0, scope confirmed (4 files).
  Route: inline parent (spot check + commit) + on-demand verifier. → DONE

## Acceptance criteria

- `./pdfbot -index -data data/` and `./pdfbot -web -data data/` work from the
  repo root with `data/` holding the PDFs; `/docs/` lists them and links open.
  → Verified statically; runtime check requires Ollama (user-run).
- Container: `DATA_DIR=/data` (entrypoint default) → first run without index
  generates it from the mounted corpus, then serves on `$PORT`. → Verified
  statically (entrypoint passes `-data /data`); runtime user-run.
- Default behavior unchanged with no `-data` / `DATA_DIR`. → Verified
  statically (fallback `.` in both flag default and envOrDefault).
- `go build ./...` passes; entrypoint shell syntax valid; `-h` shows `-data`.
  → Verified (writer + parent spot-check + independent verifier).

## Progress

- [x] T1 — main.go flag + DataDir wiring
- [x] T2 — docker-entrypoint.sh DATA_DIR + `-data`
- [x] T3 — README.md documentation
- [x] T4 — verification + work-unit commit

## Verification evidence

- Writer (gentle-ai-worker, task mug2c0c3-1-7pcg): `go build ./...` success;
  `go vet ./...` success; `sh -n docker-entrypoint.sh` OK; `go run . -h` shows
  `  -data string`. Grep for old patterns (`cd /data`, `http.Dir(".")`, bare
  IndexFile open/create, `Source: archivo`, `Glob("*.pdf")`) → no matches.
- Parent spot-check: `go build ./...` → OK (re-run).
- Parent structural readback of `git show 7892c84`: change matches spec exactly,
  including `Chunk.Source` = `filepath.Base(archivo)`.
- Independent verifier (gentle-ai-verify, task mug2fcs1-2-yzbd): `status: pass`
  — `go build ./...` exit 0, `go vet ./...` exit 0, `sh -n` exit 0,
  `go run . -h | grep -i -- '-data'` → `  -data string`, `git show --stat`
  confirms scope (README.md 3+, docker-entrypoint.sh 11, main.go 29,
  odd/tasks/... 101 new).
- Commits on `feat/flag-data-dir`:
  - `12f01d6` build(docker): persist PDFs/index/logs via /data volume and
    document port 8987
  - `7892c84` feat: add -data flag and DATA_DIR for configurable data directory
- Runtime behavior (index/globbing/HTTP against a real corpus) still requires a
  user-run with Ollama — out of scope of static verification.

## Next step

- User: populate `data/` with the PDFs, rebuild the image (`container build -t
  pdfbot .`), and run with `-p 8876:8876 -e PORT=8876 -v "$(pwd)/data:/data"`.
- User decision (not performed): push / PR / merge of `feat/flag-data-dir`
  (2 commits) into `main`.
- Optional follow-up: flip the real PORT default to 8987 in `main.go` and the
  Dockerfile ENV so docs and code fully agree.