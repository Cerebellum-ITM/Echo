# REQUEST · agent-inspection
> From Echo (Units 143-147) to the `odoo-probe` skill (`~/Documents/Projects/odoo-probe`). Written 2026-10-19. Status: applied 2026-10-20 in the odoo-probe repo (see STATE).

## What is asked

Once the release that ships Units 143-147 is installed as `echo_cli`, rework the skill around the new headless
surface instead of Python heredocs and log scraping:

1. **The core probe becomes `echo_cli probe`.** `probe -c <expr>` (repeatable, `name=expr`), `probe -` for a
   script that sets `result`, and the presets `ref`, `read`, `fields`, `param`, `arch`. It runs in a Postgres
   `READ ONLY` transaction (a write fails, `commit()` raises), returns one framed result, and `--json` prints
   `{ok, kind, result | error{type,message,traceback,file,line}, stdout, db, stage, target, duration_ms}` on
   stdout alone. No prod gate. Most of `references/recipes.md` collapses into one-liners.
2. **Pre-flight reads `link --show --json`** (stage, `stage_declared`, db, odoo_version, deployed, containers)
   instead of parsing the `echo.system.status` line.
3. **Write-mode results come from `--json`** on `install`/`update`/`uninstall`/`test`: `errors` with frames,
   `tests.failures` with `test`, `file`, `line`, `message`. Unknown flags and unknown local module names now
   exit 2, and a `test` that ran zero tests exits 1.
4. **"Why does it break" starts with `logs --errors [--from t] --json`**: the last ERROR blocks with exception
   and frames.
5. **Headless runs no longer touch the clipboard**; `ECHO_NO_CLIPBOARD=1` forces it.

## Stale skill text found in the 2026-10-19 audit (fix in the same pass)

- Teach `help <cmd> [--json]`, `help agents`, `ECHO_LOG_FORMAT=json` and `-C <dir|alias>` (the skill still
  does `cd <project> &&`). This is the open [command-help request](REQUEST-odoo-probe-command-help.md).
- `logview`, `compare` and `view` take `--from`/`--remote`; the command table says local only.
- `lint [--json]` is missing; it is a cheap offline pre-deploy gate.
- Name `db-push`, `db-pull`, `db-admin --from`, `sideload`, `connect`, `up/stop/restart --from` in the
  human-only list. `db-push` replaces a remote database.
- `modstate` is installed-only by default (`--all` for every state), not a full dump.
- `references/recipes.md:163` (i18n-pull must run in the main agent) contradicts `SKILL.md:246`.
- Evals: add cases for pre-flight on prod, `probe` reads, a failing `test --json`, and `logs --errors`.

## Done looks like

- [ ] The probe section and recipes use `echo_cli probe`; heredocs remain only for what no preset covers.
- [ ] Pre-flight and write-mode sections read the JSON forms above.
- [ ] The stale items are fixed. Help text is pointed to, not copied.

## Do not

- Do not apply before the release with these units is installed; the old binary rejects these flags.
- Do not copy help text into the skill.
