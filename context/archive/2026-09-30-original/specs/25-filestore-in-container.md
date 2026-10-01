# Unit 25: Filestore in the container (not the host)

## Goal

Fix `db-restore` / `db-backup --with-filestore` so the filestore is
read from and written to the **Odoo container**, not the host. Echo
currently uses `~/.local/share/Odoo/filestore/<db>` (a *native* Odoo
install path), but Echo targets Dockerized Odoo, whose filestore lives
inside the container at `/var/lib/odoo/filestore/<db>`. The mismatch
means a restored filestore lands on the host where the container can't
see it — Odoo then throws `FileNotFoundError` for every attachment.

## Design

- A new per-project config `filestore_path` gives the **container's**
  filestore base dir, defaulting to `/var/lib/odoo/filestore`.
- **Restore**: copy the unzipped filestore **into** the Odoo container at
  `<filestore_path>/<target>/` via `docker cp`, then fix ownership so
  Odoo can also write new attachments.
- **Backup**: copy the filestore **out of** the container first
  (`docker cp` from `<filestore_path>/<db>`), then zip it.
- The on-disk archive layout (`filestore/<db>/<XX>/…`) and the
  Odoo-native archive handling from Unit 22 are unchanged — only the
  host↔container hop is added.

## Implementation

### Config (`internal/config`)

- `Config` gains `FilestorePath string`; `projectFile` gains
  `filestore_path`. `applyDefaults` sets `/var/lib/odoo/filestore` when
  empty. Wired through `Load` / `SaveProject`. Add to `Defaults`.

### docker helpers (`internal/docker`)

Reuse existing `ContainerID`, `CopyToContainer`, `CopyFromContainer`,
`Exec`. Add one helper for root-side ownership fixes:

```go
// ExecAsRoot runs `docker exec -u 0 <container> <argv...>` (raw docker,
// not compose) so it can chown files that `docker cp` left root-owned.
func ExecAsRoot(ctx context.Context, container string, argv ...string) error
```

### Restore (`internal/cmd/db.go`, `restoreFromZip`)

Replace the host `copyDir(src, odooFilestorePath(target))` tail with a
container copy:

```go
src, ok := findFilestoreInDir(tmpDir)        // Unit 22 layout detection
if !ok { /* "no filestore in archive — sql only" */ return nil }

id := docker.ContainerID(ctx, compose, root, odooContainer)
dst := opts.Cfg.FilestorePath + "/" + target  // /var/lib/odoo/filestore/<target>

docker.Exec(ctx, compose, root, odooContainer, []string{"mkdir", "-p", dst}, noop)
docker.CopyToContainer(ctx, id, src+"/.", dst) // copy the <XX>/… contents in
// docker cp leaves files root-owned; match the filestore base dir's owner
// so Odoo (odoo user) can read AND write. Best-effort.
docker.ExecAsRoot(ctx, id, "sh", "-c",
    fmt.Sprintf("chown -R $(stat -c '%%u:%%g' %q) %q", opts.Cfg.FilestorePath, dst))
```

A chown failure is logged (`⚠ filestore copied but chown failed — Odoo can read it but may not write new attachments`) and not fatal.

### Backup (`internal/cmd/db.go`, `backupWithFilestore`)

Pull the filestore out of the container instead of reading the host path:

```go
id := docker.ContainerID(...)
containerSrc := opts.Cfg.FilestorePath + "/" + db
if err := docker.Exec(ctx, compose, root, odooContainer, []string{"test", "-d", containerSrc}, noop); err != nil {
    // no filestore in container → package dump only (current ⚠ message)
} else {
    tmp := os.MkdirTemp(...)
    defer os.RemoveAll(tmp)
    docker.CopyFromContainer(ctx, id, containerSrc, tmp) // → tmp/<db>/…
    addDirToZip(zw, filepath.Join(tmp, db), "filestore/"+db)
}
```

`odooFilestorePath` (host path) is removed from both paths; keep it only
if still referenced, otherwise delete.

## Dependencies

None new. Uses `docker cp` / `docker exec` (Docker CLI, already required).

## Verify when done

- [ ] Restoring an Odoo backup zip lands the filestore **inside** the
      container at `<filestore_path>/<target>/<XX>/…` — `docker compose exec
      <odoo> ls /var/lib/odoo/filestore/<target>/06/` shows the files, and
      Odoo no longer raises `FileNotFoundError` for attachments.
- [ ] The restored filestore is owned so Odoo can read existing and write
      new attachments (or, on chown failure, the warning is shown and reads
      still work).
- [ ] `db-backup --with-filestore` produces a zip whose `filestore/<db>/…`
      came from the container, and round-trips back via restore.
- [ ] A DB with no container filestore still backs up the dump only (with
      the existing ⚠ message), and restore of a dump-only archive still
      works.
- [ ] `filestore_path` is configurable and defaults to
      `/var/lib/odoo/filestore`.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
