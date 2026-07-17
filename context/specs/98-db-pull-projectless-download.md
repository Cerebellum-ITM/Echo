# Unit 98: Projectless `db-pull` — download by default, restore opt-in

## Goal

`db-pull` today assumes a laptop-dev shape: dump a remote DB over SSH,
then **restore it into the local Docker stack** (`requireDBContainer` +
`restoreBackupFile`). That breaks the link-only workflow, where the
project directory (e.g. `all_odoo`) is just the module **source** bound
with `link` to a remote server that *is* where `docker-compose.yml`,
Postgres and Odoo live. There is no local stack by design — so:

1. `db-pull` fails with `not inside a project` because it is **not** in
   the projectless allow-list, even though its siblings `deploy`, `push`
   and `i18n-pull` are; and
2. even past that gate, the mandatory local restore has nowhere to land.

The user's actual need is "make a backup on the remote and pull it down".
Restoring into a local stack is a separate, optional concern.

Make `db-pull`:

- **projectless** — runnable from a linked source directory with no local
  compose (like `deploy`/`push`/`i18n-pull`); and
- **download-only by default** — dump the remote DB over SSH into
  `./backups/` and stop. The local restore becomes **opt-in** via
  `--restore`.

## Design

`db-pull` splits into two clearly separated halves. The remote dump (read-
only `pg_dump` over SSH) always runs. What happens next depends on
`--restore`:

- **Default (no `--restore`)** — download only. The `.dump` lands in
  `./backups/` of the working directory (the linked source dir when
  projectless). With `--filestore`, the remote filestore is streamed down
  as a raw tar next to the dump (`<...>.filestore.tar`), not extracted.
  Nothing local is touched beyond writing files. No `requireDBContainer`
  check, no restore, no neutralize.

- **`--restore`** — after the download, restore into the local stack
  exactly as today: `requireDBContainer` gate, `restoreBackupFile`
  (drop/create/restore, `--force` to replace, neutralize auto-on for a
  prod source or per `--neutralize`/`--no-neutralize`), and, with
  `--filestore`, extract + copy the filestore into the local Odoo
  container. Emits the `→ db-use <name>` hint. This path needs a real
  local stack; without one it fails at `requireDBContainer`
  (`ErrNoDBContainer`) — the user explicitly opted in.

Flags that only make sense with a local restore (`--as`, `--neutralize`,
`--no-neutralize`, `--force`) are parsed unconditionally but only act
under `--restore`.

### Projectless gate

`db-pull` reaches a remote and only reads/writes local files (the dump,
and its own `./backups/`), so it joins the unconditional projectless set
next to `i18n-pull`/`deploy`/`push`. When run without `--restore` it
never needs a local Docker stack; with `--restore` the restore step
surfaces the missing-stack error itself. Either way the projectless
classification is correct — the local-project requirement, when it
applies, is enforced downstream by `requireDBContainer`, not by the
project-root check.

## Implementation

### `main.go`

- `projectlessOneShot`: add `"db-pull"` to the unconditional `case`
  (with `help`, `i18n-pull`, `link`, `deploy`, `push`, …). Update the
  doc comment to mention `db-pull` (downloads a remote dump into the
  local repo; restore is opt-in and self-guards).

### `internal/cmd/db_pull.go`

- `dbPullFlags`: add `restore bool`.
- `parseDBPullArgs`: recognize `--restore`.
- `RunDBPull`:
  - Remove the top-level `requireDBContainer` gate (it belongs to the
    restore branch only).
  - Keep resolve-remote + dump-to-`./backups/` unchanged.
  - Branch on `f.restore`:
    - **true** → `requireDBContainer`; `restoreBackupFile(...)`; if
      `f.filestore`, `pullFilestore(...)` (extract+copy into container);
      final "pull complete" line with the `neutralized` field and the
      `→ db-use <name>` hint (today's tail).
    - **false** → if `f.filestore`, stream the remote filestore tar into
      `./backups/<db>_<label>_<ts>.filestore.tar` via a new
      `pullFilestoreArchive`; final "pull complete" line reporting the
      downloaded dump path (and tar, if any). No db-use hint.
- New `pullFilestoreArchive(ctx, opts, rsc, remoteDB, outPath)`: builds
  the same `tar -cf - -C <parent> <db>` remote command as `pullFilestore`
  and `runSSHToFile`s it straight to `outPath` (no extract/copy).

### `internal/repl/commands.go`

- `db-pull` completion flags: add `"--restore"`.

### `internal/repl/repl.go`

- `db-pull` help description: reflect the new default — e.g.
  "Download a remote DB dump into ./backups/ (add --restore to load it
  into the local stack)".

### Tests

- `internal/cmd/db_pull_test.go`: extend `TestParseDBPullArgs` — a
  `--restore` case sets `f.restore`; defaults leave it false.
- `main_test.go`: `projectlessOneShot("db-pull", nil)` is `true`;
  `projectlessOneShot("db-pull", []string{"--restore"})` is `true`
  (restore self-guards downstream, classification is unconditional).

## Verify when done

- [ ] From a linked source dir with no local compose,
      `echo db-pull --from <target>` dumps the remote DB and writes the
      `.dump` into `./backups/` — no "not inside a project", no restore.
- [ ] `--filestore` (no `--restore`) also downloads the filestore tar to
      `./backups/`.
- [ ] `--restore` restores into the local stack as before (drop/create/
      restore, neutralize on prod source, `→ db-use` hint); `--filestore`
      copies the filestore into the local Odoo container.
- [ ] `--restore` from a dir with no local stack fails cleanly at
      `requireDBContainer` (`ErrNoDBContainer`).
- [ ] `go build ./... && go vet ./... && go test ./...` pass.
