# Unit 22: Odoo-native backup restore

## Goal

Make `db-restore` accept a **standard Odoo backup `.zip`** (the kind
downloaded from Odoo's database manager / odoo.sh), not just Echo's own
`.zip`. An Odoo backup contains `dump.sql` (plain SQL) + `filestore/<XX>/…`
+ `manifest.json`, whereas Echo's backup contains `dump.backup`
(pg_dump custom format) + `filestore/<db>/<XX>/…`. Today restoring an
Odoo zip fails twice: the target DB name keeps the timestamp, and
`restoreFromZip` looks for `dump.backup` which isn't there.

## Design

Restore must transparently handle **both** archive flavors, auto-detected
from the contents — the user just picks the file. No new flag, no UX
change. The three differences and how each is resolved:

| Concern | Echo backup | Odoo backup | Resolution |
|---|---|---|---|
| dump entry | `dump.backup` (`pg_dump -Fc`) | `dump.sql` (plain SQL) | detect which exists → `pg_restore` vs `psql -f` |
| filestore layout | `filestore/<db>/<XX>/…` | `filestore/<XX>/…` | detect the dir that directly holds the 2-char prefix dirs |
| filename timestamp | `<db>_YYYYMMDD-HHMMSS` | `<db>_YYYY-MM-DD_HH-MM-SS` | parse both formats |

## Implementation

### `dbNameFromBackup` (`internal/cmd/db.go`)

Strip the timestamp suffix for **both** conventions before returning the
db prefix. Add the Odoo pattern alongside the existing 15-char one:

```go
var odooBackupTS = regexp.MustCompile(`_\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}$`)
```

- If the (extension-stripped) base matches `odooBackupTS`, trim that match.
- Else keep the current `_YYYYMMDD-HHMMSS` (8+`-`+6) handling.
- Else fall back to the whole base.

So `habitta_prod_2026-06-08_23-42-53.zip` → `habitta_prod`.

### `restoreFromZip` — detect dump flavor (`internal/cmd/db.go`)

After unzip, pick the restore path by which dump file is present:

```go
switch {
case fileExists(tmpDir/"dump.backup"):
    docker.Restore(... tmpDir/"dump.backup")      // pg_restore (Echo)
case fileExists(tmpDir/"dump.sql"):
    docker.RestoreSQL(... tmpDir/"dump.sql")      // psql -f (Odoo)
default:
    return fmt.Errorf("no dump.backup or dump.sql found in archive")
}
```

Everything else (create DB, filestore copy, stream line) stays shared.

### `docker.RestoreSQL` — new helper (`internal/docker/pgdump.go`)

Mirror `Restore`, but stream a plain `.sql` file into `psql`:

```go
// RestoreSQL loads a plain-SQL dump (e.g. Odoo's dump.sql) into db by
// piping it to psql inside the container.
func RestoreSQL(ctx context.Context, composeCmd, dir, dbContainer, user, db, inPath string) error {
    args := append(SplitCompose(composeCmd), "exec", "-T", dbContainer,
        "psql", "-q", "-U", user, "-d", db)
    // cmd.Stdin = open(inPath); capture stderr; surface on non-zero exit
}
```

- No `ON_ERROR_STOP`: Odoo dumps are `--no-owner` plain SQL restored into
  a freshly-created empty DB, matching a manual `psql < dump.sql`. psql
  exits non-zero only on fatal/connection errors, which we surface.
- `-q` keeps the output quiet (a 160 MB dump would otherwise spam).

### Filestore layout detection (`internal/cmd/db.go`)

Fix `findFilestoreInDir` so it returns the directory that **directly
contains the 2-char hex prefix dirs**, handling both layouts:

```go
func findFilestoreInDir(root string) (string, bool) {
    fs := filepath.Join(root, "filestore")
    entries, err := os.ReadDir(fs)
    if err != nil { return "", false }

    dirs := 0; prefixDirs := 0
    var firstDir string
    for _, e := range entries {
        if !e.IsDir() { continue }
        dirs++
        if firstDir == "" { firstDir = e.Name() }
        if isHexPrefix(e.Name()) { prefixDirs++ } // len==2 && all hex
    }
    if dirs == 0 { return "", false }
    if prefixDirs == dirs {
        return fs, true            // Odoo: filestore/<XX>/… directly
    }
    return filepath.Join(fs, firstDir), true   // Echo: filestore/<db>/<XX>/…
}
```

The destination is unchanged: `copyDir(src, odooFilestorePath(target))` →
`~/.local/share/Odoo/filestore/<target>/`, so the prefix dirs land under
the new db's filestore regardless of source layout.

`manifest.json` is ignored — it's metadata, not needed to restore.

## Dependencies

None new. `regexp` (stdlib) for the timestamp pattern; everything else
reuses existing `docker` / `os` / `archive/zip` code.

## Verify when done

- [ ] Restoring an Odoo-native `.zip` (containing `dump.sql` +
      `filestore/<XX>/…` + `manifest.json`) creates the DB via `psql` and
      copies the filestore to `~/.local/share/Odoo/filestore/<target>/`
      with the `<XX>/<hash>` structure intact.
- [ ] `habitta_prod_2026-06-08_23-42-53.zip` resolves the target db to
      `habitta_prod` (timestamp stripped), not the full timestamped name.
- [ ] An Echo-native `.zip` (with `dump.backup` + `filestore/<db>/…`) still
      restores correctly via `pg_restore` — no regression.
- [ ] A `.dump` (plain pg custom file) still restores via `pg_restore`.
- [ ] `--as <name>` still overrides the derived target; `--force` still
      replaces an existing DB.
- [ ] An archive with neither `dump.backup` nor `dump.sql` fails with a
      clear error.
- [ ] Table tests for `dbNameFromBackup` (Echo ts, Odoo ts, no ts) and
      `isHexPrefix`.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
