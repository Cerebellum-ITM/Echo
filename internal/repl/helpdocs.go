package repl

import "strings"

// helpItem is one flag or subcommand row of a command page. name is exactly
// one token (the flag or the subcommand), arg its value placeholder.
type helpItem struct{ name, arg, desc string }

type helpExample struct{ cmd, desc string }

// commandDoc is the single source of a command's help: the pager rows, the
// flat index, the command page, `help --json` and the sequence picker's
// description all derive from it. Its flags must match commandFlags
// (help_test.go enforces both directions).
type commandDoc struct {
	name, section, usage, summary string
	subs, flags                   []helpItem
	notes                         []string
	examples                      []helpExample
}

type helpTopic struct {
	name, title string
	rows        []helpItem
}

var commandDocs = []commandDoc{
	{
		name:    "init",
		section: "Project",
		usage:   "init [--remote [--host <alias>] [--path <dir>] [--name <target>] [--link] | --from <target>] [--stage <stage>] [--odoo-version <N>] [--force]",
		summary: "Configure Odoo project (containers, version, DB)",
		flags: []helpItem{
			{"--remote", "", "Write a remote host's Echo profile over SSH (no echo_cli needed there)"},
			{"--from", "<target>", "Write or fix the profile of a registered target"},
			{"--host", "<alias>", "SSH host alias from ~/.ssh/config (with --remote; picker when omitted)"},
			{"--path", "<dir>", "Remote directory holding docker-compose.yml (with --remote)"},
			{"--stage", "dev|staging|prod", "Stage of the profile (required without a TTY)"},
			{"--odoo-version", "17|18|19", "Odoo version (default: detected in the container)"},
			{"--odoo-container", "<service>", "Odoo compose service (default: detected)"},
			{"--db-container", "<service>", "Database compose service (default: detected)"},
			{"--db", "<name>", "Database name (default: existing profile, else the first DB found)"},
			{"--name", "<target>", "Register the connect target under <name> (with --remote)"},
			{"--link", "", "Bind this directory to the registered target without asking (with --remote)"},
			{"--force", "", "Allow lowering the stage of a prod profile without a confirm"},
		},
		notes: []string{
			"Bare init is the local interactive form and needs a TTY (exit 2 without one); any flag switches to the remote form",
			"The remote form needs exactly one of --remote or --from; --host, --path, --name and --link only apply to --remote",
			"Without a TTY pass --stage (and --host and --path with --remote), else exit 2; detected values fill the rest",
			"Headless --remote registers a target only with --name and binds this directory only with --link",
			"An existing profile is edited in place (init fields only); lowering a prod stage needs a TTY confirm or --force",
		},
		examples: []helpExample{
			{"init --remote --host staging --path /srv/odoo --stage staging --name staging --link", "Write the server profile, register the target and link this directory"},
			{"init --from staging --stage staging --db odoo_dev", "Fix the profile of an already registered target"},
		},
	},
	{
		name:    "reset",
		section: "Project",
		usage:   "reset",
		summary: "Wipe Echo configuration (global / per-project / all)",
		notes: []string{
			"Interactive only: picks the scope and confirms, so it exits 2 without a TTY",
			"Deletes files under ~/.config/echo and cannot be undone; restart Echo afterwards",
		},
	},
	{
		name:    "alias",
		section: "Project",
		usage:   "alias [<name>] [--list | --rm <name> | --migrate]",
		summary: "Register this project as `-C <name>` (no args: list)",
		flags: []helpItem{
			{"--list", "", "List all project aliases"},
			{"--rm", "<name>", "Remove an alias"},
			{"--migrate", "", "Backfill aliases from connect targets (local paths)"},
		},
		notes: []string{
			"Aliases live in ~/.config/echo/global.toml; a real directory named like the alias wins for -C",
		},
		examples: []helpExample{
			{"alias sale", "Let -C sale stand for this project's path"},
		},
	},
	{
		name:    "link",
		section: "Project",
		usage:   "link [<target>] [--add | --next | --show | --rm | --list [--json]]",
		summary: "Switch this directory's connect target (no args: picker, current marked)",
		flags: []helpItem{
			{"--add", "", "Register a new target (SSH host + remote project) and bind to it"},
			{"--next", "", "Switch to the next target, wrapping (the two-target toggle)"},
			{"--show", "", "Show the binding, probe the remote, stream its `ps`"},
			{"--rm", "", "Remove this directory's [connect] binding"},
			{"--list", "", "List the targets, marking the current one (no SSH, no write)"},
			{"--json", "", "With --list: print the targets as one JSON array on stdout"},
		},
		notes: []string{
			"A target name, --add, --next, --show, --rm and --list are mutually exclusive; --json only applies to --list",
			"The binding is what --remote and the default target of deploy, push, doctor and i18n-pull use",
			"Bare link with several targets and --add open pickers and exit 2 without a TTY; name the target instead",
			"Registering a target headlessly is init --remote ... --name <target> --link",
			"The binding is saved before the probe; an unreachable remote is a WARNING, not a failure",
		},
		examples: []helpExample{
			{"link staging", "Bind this directory to the staging target"},
			{"link --list --json", "List the registered targets as JSON, current one marked"},
			{"link --show", "Verify the binding end to end (profile and remote ps)"},
		},
	},
	{
		name:    "doctor",
		section: "Project",
		usage:   "doctor [--from <target> | --remote] [--json]",
		summary: "Check a remote target is ready for deploy, push and checkpoints (read-only)",
		flags: []helpItem{
			{"--from", "<target>", "Check a named connect target (default: this dir's link)"},
			{"--remote", "", "Check this directory's linked remote"},
			{"--json", "", "One JSON report on stdout, lines on stderr"},
		},
		notes: []string{
			"Read-only on both sides: checks ssh, profile, rsync, git, disk, lock and dest, and runs every check even after a failure",
			"Any failed check exits 1; warnings alone exit 0",
			"With no link, no --from and several targets it opens a target picker, which exits 2 without a TTY",
			"--json prints {target, checks, counts} on stdout; each check has id, status, reason, message and fields",
		},
		examples: []helpExample{
			{"doctor --from staging --json", "Readiness report of staging as JSON"},
			{"doctor --remote", "Check the target this directory is linked to"},
		},
	},
	{
		name:    "install",
		section: "Modules",
		usage:   "install <mod...> [--with-demo] [--level <lvl>]",
		summary: "Install modules in the current DB",
		flags: []helpItem{
			{"--with-demo", "", "Include demo data"},
			{"--level", "<lvl>", "Odoo --log-level (debug…critical; default info)"},
		},
		notes: []string{
			"Local only; with no module names it opens a picker, which exits 2 without a TTY",
		},
		examples: []helpExample{
			{"install sale_custom stock_custom", "Install two modules in the local database"},
		},
	},
	{
		name:    "update",
		section: "Modules",
		usage:   "update <mod...> [--all | --last] [--i18n] [--installed] [--level <lvl>] [--from <target> | --remote] [--force] [--no-checkpoint]",
		summary: "Update modules",
		flags: []helpItem{
			{"--from", "<target>", "Update on a remote instance (named connect target)"},
			{"--remote", "", "Update on this directory's linked remote (see link)"},
			{"--force", "", "Skip the confirm on a remote prod-stage target"},
			{"--no-checkpoint", "", "On a Reverb environment, skip the pre-update checkpoint"},
			{"--all", "", "Update every installed module"},
			{"--last", "", "Repeat the last update for this database"},
			{"--i18n", "", "Overwrite the modules' translations from their .po (all langs)"},
			{"--installed", "", "Pick from all installed modules (e.g. base), not just the repo"},
			{"--level", "<lvl>", "Odoo --log-level (debug…critical; default info)"},
		},
		notes: []string{
			"With no module names, --all or --last it opens a picker, which exits 2 without a TTY",
			"Remote runs odoo -u in the target's running container over SSH and needs no local compose project",
			"A remote prod-stage target asks for a confirm, which exits 2 without a TTY; --force skips it",
			"On a Reverb environment the update takes a pre_update checkpoint first unless --no-checkpoint",
			"--last is local-only and is a usage error with --from or --remote",
		},
		examples: []helpExample{
			{"update sale_custom --i18n", "Update a local module and overwrite its translations"},
			{"update sale_custom stock_custom --from staging", "Update two modules on the staging target"},
			{"update --last --level=debug", "Repeat the last local update with debug logs"},
		},
	},
	{
		name:    "uninstall",
		section: "Modules",
		usage:   "uninstall <mod...> [--level <lvl>]",
		summary: "Uninstall modules",
		flags: []helpItem{
			{"--level", "<lvl>", "Odoo --log-level (debug…critical; default info)"},
		},
		notes: []string{
			"Local only and destructive: the modules' data is removed from the database",
			"With no module names it opens a picker, which exits 2 without a TTY",
		},
	},
	{
		name:    "test",
		section: "Modules",
		usage:   "test <mod...> [--update] [--tags <spec>] [--from <target> | --remote] [--force]",
		summary: "Run tests for installed modules (filters to /<mod>)",
		flags: []helpItem{
			{"--from", "<target>", "Run the suite on a remote target (named connect target)"},
			{"--remote", "", "Run the suite on this directory's linked remote"},
			{"--force", "", "Skip the confirm on a remote prod-stage target"},
			{"--update", "", "Reload modules first (adds -u; needed for XML/schema changes)"},
			{"--tags", "<spec>", "Override --test-tags (e.g. :TestX.test_y)"},
		},
		notes: []string{
			"Without --update the modules are not reloaded, so view and schema changes are not picked up",
			"With no module names it opens a picker over the local modules, which exits 2 without a TTY",
			"A remote prod-stage target asks for a confirm, which exits 2 without a TTY; --force skips it",
			"Unknown flags are ignored, not rejected",
		},
		examples: []helpExample{
			{"test sale_custom --update", "Reload and test a module in the local stack"},
			{"test sale_custom --from staging", "Run a module's tests on the staging target"},
			{"test sale_custom --tags :TestSaleCustom.test_confirm", "Run a single test method"},
		},
	},
	{
		name:    "modules",
		section: "Modules",
		usage:   "modules [--config | --addons-path <a,b>]",
		summary: "List modules from configured addons paths",
		flags: []helpItem{
			{"--config", "", "Pick which folders are addons paths (form)"},
			{"--addons-path", "<a,b>", "Set addons paths without the form (empty clears)"},
		},
		notes: []string{
			"Addons paths are relative to the project root; with none set, Echo scans ., addons, custom, then discovers manifests",
			"--config exits 2 without a TTY; set the paths with --addons-path instead",
			"A discovery failure (odoo.conf unreadable in the container) exits 1; an empty result is not an error",
		},
	},
	{
		name:    "modinfo",
		section: "Modules",
		usage:   "modinfo [<mod>] [--copy] [--last]",
		summary: "Compare DB-installed version vs manifest version",
		flags: []helpItem{
			{"--copy", "", "Copy the report to the clipboard"},
			{"--last", "", "Re-show this session's last modinfo (skips the picker)"},
		},
		notes: []string{
			"Status is one of in sync, update pending, db ahead, not installed or no version; all but in sync log a WARNING",
			"With no module it opens a picker, which exits 2 without a TTY; --last only works inside one REPL session",
		},
		examples: []helpExample{
			{"modinfo sale_custom", "Check whether the module needs an update"},
		},
	},
	{
		name:    "modstate",
		section: "Modules",
		usage:   "modstate [--all] [--json]",
		summary: "Dump module states from the DB (installed-only)",
		flags: []helpItem{
			{"--all", "", "Include every module state, not just installed"},
			{"--json", "", "Emit a JSON array to stdout (logs to stderr)"},
		},
		notes: []string{
			"Reads the local database only; takes no positional arguments",
			"--json prints one array of {name, state, version} on stdout; version is null when unset",
		},
		examples: []helpExample{
			{"modstate --json", "Installed modules as a JSON array"},
			{"modstate --all", "Every module and its state as a table"},
		},
	},
	{
		name:    "probe",
		section: "Modules",
		usage:   "probe -c <expr>... | probe - | probe ref|read|fields|param|arch <arg> [--from <target> | --remote] [--json]",
		summary: "Ask the live database a read-only question and get the answer as data",
		subs: []helpItem{
			{"ref", "<xmlid>", "Record behind an external ID: model, id, display_name, module, noupdate"},
			{"read", "<model>", "search_count + search_read; --domain, --fields, --limit (20), --order, --count"},
			{"fields", "<model> [name...]", "Field definitions: type, relation, required, store, compute, related, modules"},
			{"param", "<key|prefix>", "System parameters: exact key, else prefix match; without it, all of them"},
			{"arch", "<xmlid|model>", "Combined arch of the primary view and its inheritance chain; --type for a model"},
		},
		flags: []helpItem{
			{"-c", "<expr>", "Evaluate a Python expression (env, self, Command, fields, datetime, json); repeat, name=expr names it"},
			{"--domain", "<json>", "With read: domain as a JSON array"},
			{"--fields", "<a,b>", "With read: fields to return (default all)"},
			{"--limit", "<n>", "With read: maximum rows (default 20)"},
			{"--order", "<spec>", "With read: order clause, e.g. 'id desc'"},
			{"--count", "", "With read: only the count"},
			{"--type", "<view>", "With arch on a model: view type (default form; tree and list both work)"},
			{"--reveal", "", "With param: show values of secret-looking keys instead of ***"},
			{"--from", "<target>", "Probe a remote target (named connect target)"},
			{"--remote", "", "Probe this directory's linked remote"},
			{"--json", "", "One payload object on stdout, every log line on stderr"},
		},
		notes: []string{
			"Runs through odoo shell in a read-only transaction: commit raises and Postgres refuses every write; files, HTTP and other side effects are not blocked",
			"Read-only for the database, so there is no prod gate, also on prod targets",
			"probe - runs the Python piped on stdin and returns its result variable (null when unset); a TTY stdin exits 2",
			"--json prints {ok, kind, result, stdout, error, db, stage, target, duration_ms}; error has type, message, traceback, file and line",
			"Values of keys containing secret, password, token, api_key or apikey print as *** unless --reveal",
			"Exit 0 with a result, 1 when the Python raised or no result came back, 2 on a usage error",
		},
		examples: []helpExample{
			{`probe -c "env['res.partner'].search_count([])" --from staging --json`, "Count partners on staging as a JSON payload"},
			{`probe read sale.order --domain '[["state","=","sale"]]' --fields name,amount_total --limit 5`, "Five confirmed sale orders"},
			{"probe arch sale.view_order_form --remote", "Combined order form and every view that extends it on the linked remote"},
		},
	},
	{
		name:    "view",
		section: "Modules",
		usage:   "view [<mod>] [--copy] [--last] [--from <target> | --remote]",
		summary: "Pick a module file and view it (bat, else plain)",
		flags: []helpItem{
			{"--from", "<target>", "View the file from a remote target (named connect target)"},
			{"--remote", "", "View the file from this directory's linked remote"},
			{"--copy", "", "Copy the file to the clipboard instead"},
			{"--last", "", "Re-display this session's last viewed file (skips pickers)"},
		},
		notes: []string{
			"The file is always chosen in a picker, so view exits 2 without a TTY; --last replays only inside one REPL session",
			"Remote mode reads the deployed copy over SSH and has no prod gate (read-only)",
		},
		examples: []helpExample{
			{"view sale_custom --from staging", "Browse the module's files as deployed on staging"},
			{"view sale_custom --copy", "Pick a file of the module and copy it to the clipboard"},
		},
	},
	{
		name:    "compare",
		section: "Modules",
		usage:   "compare [<mod>] [--all] [--copy] [--from <target> | --remote] | compare --targets <a>,<b> [<mod>...] [--json]",
		summary: "Diff a local module file against its Docker copy",
		flags: []helpItem{
			{"--from", "<target>", "Compare against a remote target (named connect target)"},
			{"--remote", "", "Compare against this directory's linked remote"},
			{"--all", "", "Compare the whole module: changed/added/missing table"},
			{"--targets", "<a>,<b>", "Compare two targets' deploy locks module by module (read-only)"},
			{"--copy", "", "Copy the diff (or the --all or --targets table) to the clipboard"},
			{"--json", "", "With --targets: emit the comparison as JSON to stdout (logs to stderr)"},
		},
		notes: []string{
			"The single-file form picks the file in a picker and exits 2 without a TTY; --all with a module runs headless",
			"--targets reads both deploy locks over SSH, writes nothing and needs no compose project; it excludes --all, --from and --remote",
			"--targets exits 0 whatever the differences and 1 when a lock cannot be read or parsed",
			"--json prints {a, b, modules, counts} on stdout and is a usage error without --targets",
		},
		examples: []helpExample{
			{"compare sale_custom --all --from staging", "Whole-module sync table against staging"},
			{"compare --targets dev,staging --json", "What each target's deploy lock ships, as JSON"},
			{"compare --targets dev,staging sale_custom", "Compare one module across two targets"},
		},
	},
	{
		name:    "lint",
		section: "Modules",
		usage:   "lint [<mod>...|<path>...] [--json]",
		summary: "Check Odoo XML the way the data loader does (also a file path)",
		flags: []helpItem{
			{"--json", "", "Emit findings + summary as JSON to stdout (logs to stderr)"},
		},
		notes: []string{
			"Offline: no container, database or network; with no argument it lints every addons path",
			"A positional that exists on disk is a path, anything else a module name; an unknown module exits 2",
			"Errors in manifest-listed files exit 1; warnings alone exit 0, so it works as a git hook or CI step",
			"--json prints {findings, summary} on stdout; a missing xmllint skips a pass and is listed in summary.skipped_passes",
		},
		examples: []helpExample{
			{"lint sale_custom --json", "Lint one module and read the findings as JSON"},
			{"lint sale_custom/views/sale_order_views.xml", "Lint a single file, still cross-checked against its manifest"},
		},
	},
	{
		name:    "i18n-export",
		section: "i18n",
		usage:   "i18n-export <mod> [lang] [--out <path>]",
		summary: "Export <mod>/i18n/<lang>.po (default es_MX)",
		flags: []helpItem{
			{"--out", "<path>", "Write to <path> instead of the module's i18n/"},
		},
		notes: []string{
			"Exports from the local database and overwrites an existing .po; with no module it opens a picker (exit 2 without a TTY)",
		},
		examples: []helpExample{
			{"i18n-export sale_custom es_MX", "Write sale_custom/i18n/es_MX.po from the local DB"},
		},
	},
	{
		name:    "i18n-update",
		section: "i18n",
		usage:   "i18n-update <mod> [lang] [--force]",
		summary: "Import the module's <lang>.po into the DB (--i18n-overwrite)",
		flags: []helpItem{
			{"--force", "", "Skip the prod-stage confirmation prompt"},
		},
		notes: []string{
			"Replaces the language's existing translations in the local DB; on a prod stage the confirm exits 2 without a TTY unless --force",
		},
		examples: []helpExample{
			{"i18n-update sale_custom es_MX", "Load sale_custom/i18n/es_MX.po into the local DB"},
		},
	},
	{
		name:    "i18n-pull",
		section: "i18n",
		usage:   "i18n-pull [<mod>...] [lang] [--from <target>] [--lang <code>] [--all] [--installed] [--to-worktree[=<branch>]]",
		summary: "Pull one or more modules' <lang>.po from a remote into the repo",
		flags: []helpItem{
			{"--from", "<target>", "Use a named connect target (default: project's [connect])"},
			{"--lang", "<code>", "Language to pull (default es_MX); makes every positional a module"},
			{"--all", "", "Pull every candidate module"},
			{"--installed", "", "List candidates from the DB (all installed), not just the project's addons"},
			{"--to-worktree", "[=<branch>]", "Write the .po into another worktree (bare form opens a picker)"},
		},
		notes: []string{
			"Read-only on the remote: exports over SSH and overwrites <addons>/<mod>/i18n/<lang>.po locally; no local compose needed",
			"Needs the server's Echo profile (init --remote); with no link and several targets the target picker exits 2 without a TTY",
			"With no module and no --all it opens a picker over the remote's modules, which exits 2 without a TTY",
			"A trailing locale-shaped positional is the language when two or more are given; --lang removes the ambiguity",
			"Bare --to-worktree is a usage error (exit 2) without a TTY; pass --to-worktree=<branch>",
		},
		examples: []helpExample{
			{"i18n-pull sale_custom stock_custom --from staging --lang es_MX", "Pull two modules' Spanish .po from staging"},
			{"i18n-pull --all --from staging", "Pull every project module's es_MX.po from staging"},
			{"i18n-pull sale_custom --from staging --to-worktree=feature/sale", "Write the .po into the worktree of a feature branch"},
		},
	},
	{
		name:    "db-admin",
		section: "Database",
		usage:   "db-admin [<db>] [--from <target> | --remote] [--password <pw> | --insecure] [--save [--vault <name>]] [--force]",
		summary: "Reset admin (uid 2) to a generated password, shown once",
		flags: []helpItem{
			{"--from", "<target>", "Reset the admin on a remote instance (named connect target)"},
			{"--remote", "", "Reset the admin on this directory's linked remote"},
			{"--password", "<pw>", "Use this password instead of a generated one"},
			{"--insecure", "", "Set the password to admin (known credentials, dev only)"},
			{"--save", "", "Store the credential in 1Password, tagged by server (needs the op CLI)"},
			{"--vault", "<name>", "Vault for --save (default: op's own default)"},
			{"--force", "", "Skip the confirmation"},
		},
		notes: []string{
			"Sets login admin on user id 2; the new password is printed once and Echo stores only its pbkdf2_sha512 hash",
			"DB defaults to the active one (remote: the server profile's db_name); with neither, a picker opens, which exits 2 without a TTY",
			"Confirms on a prod-stage target (the target's stage when remote) and on any stage with --insecure; --force skips it, required headless",
			"With --from or --remote no local compose project is needed; the hash is computed locally, only the hash crosses SSH",
			"--password with --insecure, or --vault without --save, is a usage error; --save checks op before the reset runs",
		},
		examples: []helpExample{
			{"db-admin odoo_dev", "Reset the admin of the local odoo_dev DB to a generated password"},
			{"db-admin --from staging --save --force", "Reset the admin on staging headlessly and store it in 1Password"},
		},
	},
	{
		name:    "db-backup",
		section: "Database",
		usage:   "db-backup [<db>] [--with-filestore]",
		summary: "Dump DB (default: configured) to ./backups/",
		flags: []helpItem{
			{"--with-filestore", "", "Include filestore (.zip instead of .dump)"},
		},
		notes: []string{
			"Writes ./backups/<db>_<YYYYMMDD-HHMMSS>.dump (pg_dump -Fc), or a .zip with the container filestore; safe against a live DB",
			"Local stack only; for a remote DB use db-pull or checkpoint",
		},
		examples: []helpExample{
			{"db-backup odoo_dev --with-filestore", "Back up odoo_dev with its attachments into one .zip"},
		},
	},
	{
		name:    "db-restore",
		section: "Database",
		usage:   "db-restore [--as <name>] [--force] [--neutralize]",
		summary: "Pick a backup, name the target, and restore",
		flags: []helpItem{
			{"--as", "<name>", "Restore under this DB name (skips the rename prompt)"},
			{"--force", "", "Replace target DB (terminates its connections)"},
			{"--neutralize", "", "Neutralize the DB after restoring"},
		},
		notes: []string{
			"Always opens the backup picker over ./backups/*.dump and *.zip, so it exits 2 without a TTY; the headless restore is db-pull --restore",
			"Takes Echo .dump/.zip and native Odoo .zip (dump.sql + filestore) backups; the format is detected from the contents",
			"An existing target DB fails unless --force, which drops it first; the active DB does not change",
		},
		examples: []helpExample{
			{"db-restore --as odoo_dev --force --neutralize", "Restore a picked backup over odoo_dev and neutralize it"},
		},
	},
	{
		name:    "db-pull",
		section: "Database",
		usage:   "db-pull [--from <target> | --remote] [--restore [--as <name>] [--neutralize | --no-neutralize] [--force]] [--filestore]",
		summary: "Download a remote DB dump into ./backups/ (add --restore to load it into the local stack)",
		flags: []helpItem{
			{"--from", "<target>", "Pull from a named connect target"},
			{"--remote", "", "Pull from this directory's linked remote"},
			{"--restore", "", "Load the dump into the local stack after downloading it"},
			{"--as", "<name>", "With --restore: local DB name (default: <remoteDB>_<target>)"},
			{"--neutralize", "", "With --restore: force neutralize (default: only when source is prod)"},
			{"--no-neutralize", "", "With --restore: skip neutralize even for a prod source"},
			{"--force", "", "With --restore: replace an existing local DB of the target name"},
			{"--filestore", "", "Also pull the DB's filestore (a .filestore.tar without --restore)"},
		},
		notes: []string{
			"Runs without a compose project; without --from it uses this dir's link, else a target picker that exits 2 without a TTY",
			"The remote side is read-only (one pg_dump over SSH), so there is no prod gate; the dump lands in ./backups/",
			"--as, --neutralize, --no-neutralize and --force only act with --restore and are ignored otherwise",
			"--restore needs a local stack, checked only after the download; the active DB does not change (run db-use)",
		},
		examples: []helpExample{
			{"db-pull --from staging", "Download staging's DB dump into ./backups/ only"},
			{"db-pull --from staging --restore --as odoo_dev --force --filestore", "Clone staging with attachments into the local odoo_dev"},
		},
	},
	{
		name:    "db-push",
		section: "Database",
		usage:   "db-push <dump> (--to <target> | --remote) [--filestore[=<tar>]] [--as <db>] [--neutralize] [--no-checkpoint] [--dry-run] [--force] [--json]",
		summary: "Replace a remote target's database (and filestore) with a local db-pull dump",
		flags: []helpItem{
			{"--to", "<target>", "Replace the database of this connect target"},
			{"--remote", "", "Replace the database of this directory's linked remote"},
			{"--filestore", "[=<tar>]", "Also replace the filestore (bare: the dump's sibling .filestore.tar)"},
			{"--as", "<db>", "Target database name (default: the server profile's db_name)"},
			{"--neutralize", "", "Run Odoo's neutralize on the restored DB before Odoo starts (mail, crons, payments off)"},
			{"--no-checkpoint", "", "Skip the checkpoint of the database being replaced"},
			{"--dry-run", "", "Print the plan (sizes, steps, checkpoint, lock); change nothing on the server"},
			{"--force", "", "Allow a prod target (asks once on a TTY); take over an operation lock older than 6 h"},
			{"--json", "", "Print the summary (steps reached, checkpoint, modules) as one JSON object on stdout"},
		},
		notes: []string{
			"Exactly one of --to or --remote is required; there is no picker and no implicit target",
			"Steps: preflight, lock, upload, stop Odoo, checkpoint, restore, filestore, neutralize, registry check, start, report, cleanup",
			"Stage prod or undeclared without --force exits 2; dev and staging never prompt, so it runs headless",
			"The previous filestore is kept as <db>.pre-push-<ts>; the checkpoint covers the database only",
			"A failure after the restore leaves Odoo stopped; `deploy --rollback --from <target> --force` brings the old DB back",
			"Another db-push on the same target exits 1 (`target busy`); Reverb environments are refused",
		},
		examples: []helpExample{
			{"db-push backups/staging.dump --to dev --filestore --neutralize --dry-run", "Show what replacing dev with a staging snapshot would do"},
			{"db-push backups/staging.dump --to dev --filestore --neutralize --json", "Replace dev's database and filestore, headless"},
		},
	},
	{
		name:    "db-drop",
		section: "Database",
		usage:   "db-drop [<db>] [--force]",
		summary: "Drop a database (confirmation by default)",
		flags: []helpItem{
			{"--force", "", "Skip confirm and terminate active connections"},
		},
		notes: []string{
			"Destructive and irreversible; confirms in every stage, so headless use needs --force",
			"Without --force a DB with open connections is refused (stop Odoo with down odoo, or pass --force)",
			"With no name a picker opens, which exits 2 without a TTY",
		},
		examples: []helpExample{
			{"db-drop odoo_dev --force", "Drop odoo_dev without a prompt, closing its connections"},
		},
	},
	{
		name:    "db-neutralize",
		section: "Database",
		usage:   "db-neutralize [<db>] [--force]",
		summary: "Neutralize a DB (disable mail/cron/payments)",
		flags: []helpItem{
			{"--force", "", "Skip the confirmation on the active DB or a prod stage"},
		},
		notes: []string{
			"Runs odoo neutralize in the Odoo container; it wipes real mail, cron and payment config",
			"The target defaults to the active DB, which always confirms (as does a prod stage); headless use needs --force",
			"Needs odoo_container and an active db_name configured even when a DB name is passed",
		},
		examples: []helpExample{
			{"db-neutralize odoo_dev --force", "Neutralize odoo_dev without a prompt"},
		},
	},
	{
		name:    "db-list",
		section: "Database",
		usage:   "db-list",
		summary: "List DBs with size, date; ● marks the active one",
	},
	{
		name:    "db-use",
		section: "Database",
		usage:   "db-use [<db>]",
		summary: "Switch the active DB (picker when no name)",
		notes: []string{
			"Persists db_name to the project config; update, shell, psql, db-admin and the rest follow it",
			"The name must exist locally; with no name a picker opens, which exits 2 without a TTY",
		},
		examples: []helpExample{
			{"db-use odoo_dev", "Make odoo_dev the active database"},
		},
	},
	{
		name:    "bash",
		section: "Shell",
		usage:   "bash [--force]",
		summary: "Bash session inside the Odoo container",
		flags: []helpItem{
			{"--force", "", "Skip the prod-stage confirmation prompt"},
		},
		notes: []string{
			"Local stack only; extra arguments are ignored",
			"On a prod-stage project it confirms first (exits 2 without a TTY); --force skips it",
		},
	},
	{
		name:    "psql",
		section: "Shell",
		usage:   "psql [--force]",
		summary: "PostgreSQL client against the configured DB",
		flags: []helpItem{
			{"--force", "", "Skip the prod-stage confirmation prompt"},
		},
		notes: []string{
			"Opens on the active DB as POSTGRES_USER from .env; switch DBs with db-use; extra arguments are ignored",
			"Local stack only; on a prod-stage project it confirms first (exits 2 without a TTY); --force skips it",
		},
	},
	{
		name:    "shell",
		section: "Shell",
		usage:   "shell [--from <target> | --remote] [--force]",
		summary: "Odoo Python shell against the configured DB",
		flags: []helpItem{
			{"--from", "<target>", "Open the shell on a remote instance (named connect target)"},
			{"--remote", "", "Open the shell on this directory's linked remote"},
			{"--force", "", "Skip the prod-stage confirmation prompt"},
		},
		notes: []string{
			"With piped stdin it runs the piped Python headless instead of opening the interactive session, local or remote",
			"The interactive remote shell needs a TTY (exit 2 without one); with --from or --remote no compose project is needed",
			"A prod-stage target (the remote profile's stage when remote) confirms first; headless runs need --force",
		},
		examples: []helpExample{
			{"shell --from staging", "Open an interactive Odoo shell on staging"},
			{"shell --remote --force", "Run piped Python on this dir's linked remote, skipping the prod confirm"},
		},
	},
	{
		name:    "shell-run",
		section: "Shell",
		usage:   "shell-run [<file>] [--from <target> | --remote] [--no-copy] [--force]",
		summary: "Run a .py through the Odoo shell (stdin); no file → picker",
		flags: []helpItem{
			{"--from", "<target>", "Run the script on a remote instance (named connect target)"},
			{"--remote", "", "Run the script on this directory's linked remote"},
			{"--no-copy", "", "Don't auto-copy the script output to the clipboard"},
			{"--force", "", "Skip the prod-stage confirmation prompt"},
		},
		notes: []string{
			"A bare name is looked up in scripts_dir (else ./scripts/, else the project root); a path is relative to the project root",
			"No file opens a picker, which exits 2 without a TTY; a lone - reads the script from piped stdin",
			"The script runs with the target's DB loaded; it is sent from this machine, so nothing is copied to the server",
			"A prod-stage target (the remote profile's stage when remote) confirms first; headless runs need --force",
			"On success the output is copied to the clipboard unless --no-copy (on a terminal only; see help agents)",
		},
		examples: []helpExample{
			{"shell-run fix_taxes.py --from staging --no-copy", "Run scripts/fix_taxes.py on staging without touching the clipboard"},
			{"shell-run scripts/fix_taxes.py --remote --force", "Run a script on this dir's linked remote, skipping the prod confirm"},
		},
	},
	{
		name:    "connect",
		section: "Shell",
		usage:   "connect [<login>] [--all] [--fresh] [--new-window] [--force]",
		summary: "Impersonate a user (mint session, open Chrome logged in)",
		flags: []helpItem{
			{"--all", "", "Include inactive users in the picker"},
			{"--fresh", "", "Ignore the cached session and mint a new one"},
			{"--new-window", "", "Open an isolated incognito window (default: reuse a tab)"},
			{"--force", "", "Skip the prod-stage confirmation prompt"},
		},
		notes: []string{
			"In the REPL it targets this project, or its [connect] remote over SSH when one is set",
			"One-shot echo_cli connect is projectless: connect [<target>] [<login>] picks a saved connect target (--add registers one)",
			"Opens a local Chrome through CDP; with no login (or no target one-shot) a picker opens, which needs a TTY",
			"A prod-stage target confirms first; --force skips it",
		},
		examples: []helpExample{
			{"connect staging admin --new-window", "One-shot: open staging as admin in an incognito window"},
			{"connect admin --fresh", "In the REPL: re-mint a session for admin on this project"},
		},
	},
	{
		name:    "up",
		section: "Docker",
		usage:   "up [service...] [--from <target> | --remote]",
		summary: "Start containers (compose up -d)",
		flags: []helpItem{
			{"--from", "<target>", "Start on a remote instance (named connect target)"},
			{"--remote", "", "Start on this directory's linked remote"},
		},
		notes: []string{
			"Without a remote flag it starts the local compose stack; with no service it starts every service",
			"--remote needs this directory's link binding; without one (and no --from) a target picker opens, which exits 2 without a TTY",
			"Remote mode reads the server's Echo profile and needs no local docker-compose.yml; starting never asks for confirmation",
			"On a Reverb environment the start goes through the Reverb API instead of compose",
		},
		examples: []helpExample{
			{"up --remote", "Start the whole stack of the linked remote"},
			{"up --from staging", "Start the stack of the staging target"},
		},
	},
	{
		name:    "down",
		section: "Docker",
		usage:   "down [service...] [--force]",
		summary: "Stop and remove containers (prod confirm)",
		flags: []helpItem{
			{"--force", "", "Skip the prod-stage confirmation prompt"},
		},
		notes: []string{
			"Local only: runs compose down on this project's stack; volumes are kept",
			"On a prod stage it asks for a red confirm, which exits 2 without a TTY; --force skips it",
		},
		examples: []helpExample{
			{"down", "Remove the local containers and networks"},
		},
	},
	{
		name:    "stop",
		section: "Docker",
		usage:   "stop [service...] [--from <target> | --remote] [--force]",
		summary: "Stop containers without removing them",
		flags: []helpItem{
			{"--from", "<target>", "Stop on a remote instance (named connect target)"},
			{"--remote", "", "Stop on this directory's linked remote"},
			{"--force", "", "Skip the remote prod-stage confirmation prompt"},
		},
		notes: []string{
			"With no service it stops the whole stack, locally or on the remote",
			"--remote needs this directory's link binding; without one (and no --from) a target picker opens, which exits 2 without a TTY",
			"A remote prod stage asks for a red confirm (exit 2 without a TTY); --force skips it. Use --force only with --from or --remote",
			"On a Reverb environment the stop goes through the Reverb API instead of compose",
		},
		examples: []helpExample{
			{"stop --from staging", "Stop the staging stack, keeping its containers"},
			{"stop odoo --remote", "Stop only the odoo service of the linked remote"},
		},
	},
	{
		name:    "restart",
		section: "Docker",
		usage:   "restart [service...] [--from <target> | --remote] [--force]",
		summary: "Restart services",
		flags: []helpItem{
			{"--from", "<target>", "Restart on a remote instance (named connect target)"},
			{"--remote", "", "Restart on this directory's linked remote"},
			{"--force", "", "Skip the remote prod-stage confirmation prompt"},
		},
		notes: []string{
			"Locally, no service restarts every service; remotely, no service restarts only the profile's Odoo container",
			"--remote needs this directory's link binding; without one (and no --from) a target picker opens, which exits 2 without a TTY",
			"A remote prod stage asks for a red confirm (exit 2 without a TTY); --force skips it. Use --force only with --from or --remote",
			"On a Reverb environment the restart goes through the Reverb API instead of compose",
		},
		examples: []helpExample{
			{"restart --remote", "Restart the Odoo container of the linked remote"},
			{"restart --from staging --force", "Restart staging's Odoo without a prompt, even if its stage is prod"},
		},
	},
	{
		name:    "ps",
		section: "Docker",
		usage:   "ps [--from <target> | --remote]",
		summary: "Show container status",
		flags: []helpItem{
			{"--from", "<target>", "Show container status on a remote instance (named connect target)"},
			{"--remote", "", "Show container status on this directory's linked remote"},
		},
		notes: []string{
			"Read-only, no prod gate; remote mode reads compose over SSH and renders the same table as the local one",
			"--remote needs this directory's link binding; without one (and no --from) a target picker opens, which exits 2 without a TTY",
			"There is no --json; for a machine-readable readiness check use doctor --json",
		},
		examples: []helpExample{
			{"ps --remote", "Container table of the linked remote"},
			{"ps --from staging", "Container table of the staging target"},
		},
	},
	{
		name:    "logs",
		section: "Docker",
		usage:   "logs [service...] [-t <N>] [-f | --no-follow | -c] [--all] [--from <target> | --remote]",
		summary: "Follow logs of Odoo (or [service]) — Ctrl+C to exit",
		flags: []helpItem{
			{"--from", "<target>", "Follow logs on a remote instance (named connect target)"},
			{"--remote", "", "Follow logs on this directory's linked remote"},
			{"-t", "<N>", "Tail last N lines (default 100)"},
			{"--tail", "<N>", "Same as -t"},
			{"-f", "", "Follow the log (the default; undoes an earlier --no-follow)"},
			{"--follow", "", "Same as -f"},
			{"--no-follow", "", "Disable follow; print bounded output"},
			{"-c", "", "Bounded output and copy to clipboard"},
			{"--copy", "", "Bounded output and copy to clipboard (same as -c)"},
			{"--all", "", "All compose services (instead of just Odoo)"},
		},
		notes: []string{
			"Follows forever by default; headless callers should pass --no-follow (or -c) so the command ends",
			"With no service and without --all it shows the Odoo container of the local config or of the remote profile",
			"--remote needs this directory's link binding; without one (and no --from) a target picker opens, which exits 2 without a TTY",
			"Read-only, no prod gate; with -c on a remote the captured text lands on the local clipboard",
		},
		examples: []helpExample{
			{"logs --remote -t 200 --no-follow", "Print the last 200 Odoo lines of the linked remote and exit"},
			{"logs --from staging --all --no-follow", "Print the last 100 lines of every staging service"},
		},
	},
	{
		name:    "push",
		section: "Docker",
		usage:   "push [<mod>...] [--from <target> | --remote] [--dirty] [--dest <path> | --pick-dest] [--dry-run] [--force]",
		summary: "Rsync local modules to the remote addons dir",
		flags: []helpItem{
			{"--from", "<target>", "Use a named connect target (default: this dir's link)"},
			{"--remote", "", "Push to this directory's linked remote (the default without --from)"},
			{"--dirty", "", "Push every module with uncommitted changes"},
			{"--dry-run", "", "List the changes rsync would make; transfer nothing"},
			{"--delete", "", "Remove remote files no longer present locally"},
			{"--dest", "<path>", "Push to an explicit remote dir (skips auto-detect); relative paths join the project dir"},
			{"--pick-dest", "", "Browse the remote filesystem to choose the destination (needs a TTY)"},
			{"--mkdir", "", "Create the destination dir if it doesn't exist"},
			{"--force", "", "Skip the prod-stage confirmation prompt (and the --clean confirm)"},
			{"--set-dest", "", "Save the remote push destination ([push] path) and exit (no push); takes --dest or opens the picker"},
			{"--clean", "", "Revert the remote dirty overlay of the named modules on a git-deploy target"},
			{"--all", "", "With --clean: revert the overlay of every module"},
		},
		notes: []string{
			"Needs this directory's link binding or --from <target>, and rsync on both ends; the destination follows the remote layout or [push] path",
			"No module names and no --dirty opens a picker: without a TTY that exits 2 (ErrNonInteractive), so name the modules",
			"A prod-stage target asks for confirmation (--force skips it; without a TTY it exits 2); --dry-run never asks",
			"push only syncs files: no -u, no code snapshot, no rollback; it records the modules in the deploy lock as via=push, unverified",
			"--set-dest only configures and exits: without --dest it opens the remote directory picker (TTY only)",
			"--clean is destructive (git checkout and git clean -fd of the module paths) and needs a git-deploy target; it confirms unless --force",
		},
		examples: []helpExample{
			{"push sale_custom stock_custom --from staging --dry-run", "List what rsync would change for two modules, transferring nothing"},
			{"push --set-dest --dest docker/addons --from staging", "Save the push destination headlessly, without pushing"},
			{"push --clean --all --from dev --force", "Revert the whole dirty overlay of a git-deploy target without a prompt"},
		},
	},
	{
		name:    "deploy",
		section: "Docker",
		usage:   "deploy [--from <target>] [--commits <shas> | --modules <names> | --auto] [--push] [--dry-run [--save-plan <file>]] [--apply <file>] [--json]",
		summary: "Deploy picked commits + dirty modules to a remote (stop, up, -i/-u)",
		flags: []helpItem{
			{"--from", "<target>", "Use a named connect target (default: this dir's link)"},
			{"--commits", "<shas>", "Deploy these commits non-interactively (skips the picker)"},
			{"--modules", "<names>", "Deploy these modules from the working tree (skips the picker); mod@ref ships one as committed at that ref"},
			{"--at", "<ref>", "Ship every --modules entry as committed at <ref> (branch/tag/SHA), never from the working tree"},
			{"--auto", "", "Headless: deploy pending commits (ahead of upstream) + dirty modules, no picker"},
			{"--limit", "<N>", "Commits offered in the picker (default 20)"},
			{"--push", "", "Ship the resolved modules' code to the remote before the run"},
			{"--no-push", "", "Skip the push even when [deploy] push is the default"},
			{"--dry-run", "", "Resolve modules and show the plan; execute nothing on the server"},
			{"--save-plan", "<file>", "With --dry-run: write the resolved plan to <file> for a later --apply"},
			{"--apply", "<file>", "Run a saved plan exactly, or refuse before any server write when anything changed since"},
			{"--force", "", "Skip the prod-stage, dependency-check and rollback confirmations; required for tests on prod"},
			{"--i18n", "", "Force --i18n-overwrite on the update run (default: auto when i18n/ changed)"},
			{"--no-i18n", "", "Suppress --i18n-overwrite even when i18n/ changes are detected"},
			{"--checkpoint", "[=db|dump]", "Force a DB checkpoint before the run (default: auto on staging/prod)"},
			{"--no-checkpoint", "", "Skip the DB checkpoint even on staging/prod"},
			{"--rollback-on-fail", "", "On failure, restore code and DB without asking"},
			{"--no-rollback-on-fail", "", "On failure, keep the broken state without asking (no code snapshot is taken)"},
			{"--test", "", "Run the deployed modules' tests in the same Odoo run; a failing suite fails the deploy"},
			{"--no-test", "", "Skip the tests even when [deploy] test is on"},
			{"--no-actions", "", "Skip declared [[deploy.actions]] for this run"},
			{"--no-git", "", "Force the legacy rsync push on a git-deploy target for this run"},
			{"--no-lint", "", "Skip the pre-flight lint of the selected modules (see lint)"},
			{"--no-dep-check", "", "Skip the check for methods, fields and XML ids the shipped modules drop while staying modules still use them"},
			{"--fetch", "", "With --set-code or a ref-pinned module: always fetch the ref's remote before resolving"},
			{"--no-fetch", "", "With --set-code or a ref-pinned module: never fetch the ref's remote before resolving"},
			{"--rollback", "", "Restore the target's most recent checkpoint, DB and code (no deploy)"},
			{"--consume-checkpoint", "", "With --rollback: rename a db checkpoint over the live DB (cheaper, but destroys the checkpoint)"},
			{"--restore-code", "[<sha>]", "Move a git-deploy target's code to a hash it already ran (bare = picker) and restart Odoo (no DB)"},
			{"--set-code", "<ref>", "Re-baseline a git-deploy target's code onto any ref (branch/tag/SHA) and restart Odoo (no DB)"},
			{"--keep-overlay", "", "With --set-code: keep the server's dirty overlay (default: clean the module paths)"},
			{"--with-local", "", "With --set-code: reset the local [promote] branch onto the same ref first"},
			{"--lock", "", "Print the target's deploy lock: what each module runs and where it came from (no deploy)"},
			{"--set-push", "[=true|false]", "Set deploy to push by default and exit (no deploy)"},
			{"--set-checkpoint", "[=on|off|auto]", "Persist the local checkpoint policy and exit (bare = on; no deploy)"},
			{"--set-checkpoint-method", "=db|dump", "Persist the local checkpoint method and exit (no deploy)"},
			{"--set-checkpoint-keep", "=<N>", "Persist how many checkpoints to keep (N >= 1) and exit (no deploy)"},
			{"--test-toggle", "", "Flip [deploy] test on/off (always-run) and print the result (no deploy)"},
			{"--test-modules", "[=<list>]", "Pin which modules get tested: bare = picker, =csv sets, empty = auto (no deploy)"},
			{"--test-add", "<list>", "Add modules to the pinned test list (no deploy)"},
			{"--test-rm", "<list>", "Remove modules from the pinned test list (no deploy)"},
			{"--test-clear", "", "Clear the pinned test modules, back to testing what's deployed (no deploy)"},
			{"--set-git-branch", "<name>", "Name the branch a git-deploy target's code lives on and exit (no deploy)"},
			{"--rename", "", "With --set-git-branch: also rename the branch already on the server"},
			{"--json", "", "Emit a machine-readable deploy summary to stdout (logs to stderr); with --lock, the raw lock file"},
		},
		notes: []string{
			"Needs this directory's link binding or --from <target> and the server's Echo profile (init --remote); deploy takes no positional arguments",
			"Code ships only with --push or [deploy] push = true; otherwise -i/-u runs on the code already on the server, and mod@ref or --at refuse",
			"Without a TTY pass --commits, --modules or --auto; --rollback, --set-code and --set-git-branch --rename need --force; else exit 2",
			"Real-run gates: prod confirm, dependency confirm on staging/prod, --test on prod; --force skips them, without a TTY they exit 2",
			"On failure code and DB roll back (asked on a TTY, automatic headless or with --force); --rollback-on-fail or --no-rollback-on-fail fix it",
			"--json prints one object on stdout; a failed run prints nothing there except a stale --apply plan (plan_stale), and exit is non-zero",
			"Configure-and-exit flags write local config only: --set-push, --set-checkpoint*, --test-toggle/-modules/-add/-rm/-clear, --set-git-branch",
			"--save-plan needs --dry-run; --apply accepts only --from, --force, --json, --dry-run and the rollback-on-fail pair (others exit 2)",
		},
		examples: []helpExample{
			{"deploy --from staging --modules sale_custom --push --dry-run --json", "Preview a headless deploy of one module and print the plan as JSON"},
			{"deploy --from staging --auto --push --rollback-on-fail --json", "Ship pending commits and dirty modules, rolling back on failure"},
			{"deploy --rollback --from staging --force", "Restore the newest checkpoint (DB and code) without a prompt"},
		},
	},
	{
		name:    "sideload",
		section: "Docker",
		usage:   "sideload [<module|path>...] [--from <target>] [--dry-run] [--resync] [--json]",
		summary: "Install/update addons that live outside the repo on a dev/staging remote; no argument → picker over your library",
		flags: []helpItem{
			{"--from", "<target>", "Use a named connect target (default: this dir's link)"},
			{"--dry-run", "", "Probe, hash and show the plan; execute nothing"},
			{"--resync", "", "Ignore stored hashes; re-sync every addon"},
			{"--no-lint", "", "Skip the pre-flight lint of the addons"},
			{"--allow-prod", "", "Allow a prod target; still asks interactively and fails without a TTY"},
			{"--list", "", "List the addons the target's deploy lock records as sideloaded (no changes)"},
			{"--available", "", "List the library modules that fit the target, with their status (no changes)"},
			{"--library", "", "Print the library roots ([sideload] library in global.toml) and exit"},
			{"--library-add", "<path>", "Add a library root and exit: a folder of modules, or a repo with one branch per Odoo series"},
			{"--library-rm", "<path>", "Remove a library root and exit"},
			{"--json", "", "Emit a machine-readable result to stdout (logs to stderr)"},
		},
		notes: []string{
			"A name resolves from the library, anything with a / or an existing directory is a path; names may be comma-separated",
			"Without a TTY a module (or --available / --list) is required; the library picker exits 2",
			"Prod targets are refused (exit 2); --allow-prod turns that into an interactive confirm, so prod can never run headless",
			"Runs stop, up -d and one -i/-u on the target; the manifest version must match the target's Odoo series. Reverb is not supported",
			"--library, --library-add and --library-rm only configure and exit; they take no other argument",
			"With --json the result object is the only stdout; logs go to stderr",
		},
		examples: []helpExample{
			{"sideload --available --from staging --json", "List the library modules that fit staging, with their status"},
			{"sideload sale_custom --from staging --dry-run", "Show the install/update plan without writing to the server"},
			{"sideload sale_custom,stock_custom --from dev --json", "Ship two library modules to dev and print the result"},
		},
	},
	{
		name:    "watch",
		section: "Docker",
		usage:   "watch [<branch>] [--from <target> | --remote] [--interval <sec>] [--no-logs] [--force]",
		summary: "Auto push+deploy when new commits land on a branch; no branch → picker (Ctrl+C to stop)",
		flags: []helpItem{
			{"--from", "<target>", "Use a named connect target (default: this dir's link)"},
			{"--remote", "", "Target this directory's linked remote (the default without --from)"},
			{"--interval", "<sec>", "Poll interval in seconds (default 10, min 2)"},
			{"--force", "", "Required to watch a prod-stage target"},
			{"--no-logs", "", "Don't follow the remote logs between cycles (silent wait)"},
			{"--no-checkpoint", "", "Skip the DB checkpoint on each cycle's deploy"},
			{"--no-actions", "", "Skip declared [[deploy.actions]] on each cycle's deploy"},
		},
		notes: []string{
			"Needs this directory's link binding or --from <target>, rsync locally, and a local branch (commits from any worktree count)",
			"No branch opens a picker: without a TTY pass <branch> or it exits 2 (ErrNonInteractive)",
			"A prod-stage target is refused (exit 2) unless --force",
			"Runs until Ctrl+C; each cycle runs deploy --commits <new> --push --force, so a failed cycle rolls back without asking and polling goes on",
			"Each deploying cycle writes a watch-deploy record with deployed_tip; read it with logview --json",
		},
		examples: []helpExample{
			{"watch main --from dev --no-logs", "Auto-deploy every commit landing on main to dev, without following logs (tmux/CI)"},
			{"watch main --from staging --interval 30", "Poll every 30 seconds and deploy new commits to staging"},
		},
	},
	{
		name:    "checkpoint",
		section: "Docker",
		usage:   "checkpoint [list|create|rm [<name>]] [--from <target> | --remote] [--method <db|dump>] [--all] [--force] [--json]",
		summary: "List a target's DB checkpoints (size, age) + live DB size and disk free",
		subs: []helpItem{
			{"list", "", "List the checkpoints (default subcommand)"},
			{"create", "", "Take a manual checkpoint (before a risky change)"},
			{"rm", "[<name>]", "Delete a checkpoint (picker when no name); --all for every one"},
		},
		flags: []helpItem{
			{"--from", "<target>", "Use a named connect target (default: this dir's link)"},
			{"--remote", "", "Target this directory's linked remote"},
			{"--method", "db|dump", "With create: checkpoint method (default: configured, else db)"},
			{"--all", "", "With rm: delete every checkpoint of the target"},
			{"--force", "", "Skip the rm confirmation and the prod-stage confirm of create"},
			{"--json", "", "Emit the checkpoint list as JSON to stdout (logs to stderr)"},
		},
		notes: []string{
			"Always acts on a remote target: --from, else this directory's link, else a target picker (exit 2 without a TTY)",
			"create with method db stops the Odoo app for the template copy and brings the stack back up; dump is a live pg_dump",
			"rm without a name opens a picker and every rm asks a red confirm; both exit 2 without a TTY, so pass the name (or --all) and --force",
			"list drops store entries whose object is gone and shows untracked __ckpt_ databases as orphan",
			"--json only changes list: one object on stdout (rows, db size, disk free); logs go to stderr",
			"On a Reverb environment the checkpoints are Reverb snapshots",
		},
		examples: []helpExample{
			{"checkpoint list --from staging --json", "Machine-readable checkpoint list of staging"},
			{"checkpoint create --from staging --method dump", "Take a full pg_dump checkpoint before a risky change"},
			{"checkpoint rm --remote --all --force", "Delete every checkpoint of the linked remote without a prompt"},
		},
	},
	{
		name:    "actions",
		section: "Docker",
		usage:   "actions [list|add|edit|rm [<name>]] [--from <target> | --remote] [--force] [--json]",
		summary: "Manage [[deploy.actions]] interactively (list/add/edit/rm)",
		subs: []helpItem{
			{"list", "", "Show the action list (default subcommand)"},
			{"add", "", "Wizard: name → phase → where → exec dir (picker) → command"},
			{"edit", "[<name>]", "Edit an action in place (picker when no name)"},
			{"rm", "[<name>]", "Delete an action (picker when no name)"},
		},
		flags: []helpItem{
			{"--from", "<target>", "Scope to that target's server profile (else the local list)"},
			{"--remote", "", "Scope to the linked remote's server profile (else the local list)"},
			{"--force", "", "With rm: skip the confirm; also skips the prod-stage confirm of a server write"},
			{"--json", "", "Emit the action list as JSON to stdout (with list)"},
		},
		notes: []string{
			"Without --from/--remote every subcommand works on the local project list and opens no SSH",
			"With a remote flag, list shows the effective list (a non-empty server list wins wholesale) and add/edit/rm write the server profile",
			"add and edit are wizards and exit 2 without a TTY; rm needs the name and --force to run headless",
			"--json only changes list: {source, actions} on stdout, logs on stderr",
		},
		examples: []helpExample{
			{"actions list --from staging --json", "Effective deploy actions of staging and which side they come from"},
			{"actions rm rebuild_assets --remote --force", "Remove an action from the linked remote's server profile"},
		},
	},
	{
		name:    "promote",
		section: "Docker",
		usage:   "promote [<branch>] [--dirty [<folder>...]] [--commits <shas>] [--to <branch>] [--reset [<base>]] [--dry-run]",
		summary: "Funnel this worktree's changes onto the deploy branch (no args: picker)",
		flags: []helpItem{
			{"--to", "<branch>", "Destination branch (else saved [promote] branch; prompts to pick if unset)"},
			{"--dirty", "", "Move the current worktree's dirty patch (by folder positionals); stays uncommitted"},
			{"--commits", "<shas>", "Cherry-pick these commits from the source branch (needs <branch>)"},
			{"--create-dest", "<path>", "Create the destination branch's worktree if none exists"},
			{"--reset", "", "Move the promote branch back onto <base> (else saved [promote] base); keeps non-colliding work"},
			{"--discard", "", "With --reset: hard reset, dropping uncommitted work and the modules' untracked files"},
			{"--no-fetch", "", "With --reset: don't refresh the base's remote first"},
			{"--force", "", "With --reset: skip the confirm when commits leave the branch or work is discarded"},
			{"--dry-run", "", "Preview the change tree / commit list; move nothing"},
			{"--set-branch", "<name>", "Persist the default promote branch and exit"},
			{"--set-base", "<ref>", "Persist the default base the promote branch is reset onto and exit"},
			{"--show-branch", "", "Report the promote branch + provenance/worktree and how far it drifted from its base"},
		},
		notes: []string{
			"Local git only and projectless: no SSH, no push, no deploy, no commit; run deploy from the deploy branch afterwards",
			"Without --to or a saved [promote] branch a headless run exits 2; save it once with --set-branch",
			"Without a TTY name the source: --dirty with folders, or <branch> with --commits; the pickers exit 2",
			"Dirty mode overwrites the destination's files (last write wins); a cherry-pick conflict aborts and leaves the branch untouched",
			"--set-branch, --set-base and --show-branch take no other argument; the first two only configure and exit",
			"--reset refuses to clobber uncommitted work; --discard does, and --force skips its confirm",
		},
		examples: []helpExample{
			{"promote --dirty sale_custom --to develop --dry-run", "Preview moving sale_custom's uncommitted changes onto develop"},
			{"promote feature_x --commits a1b2c3d --to develop", "Cherry-pick one commit from feature_x onto develop"},
			{"promote --reset origin/main --to develop --force", "Send develop back onto origin/main without a prompt"},
		},
	},
	{
		name:    "copy-last",
		section: "Session",
		usage:   "copy-last [--errors]",
		summary: "Copy the last command's output to clipboard",
		flags: []helpItem{
			{"--errors", "", "Only copy error/warning lines"},
		},
	},
	{
		name:    "report",
		section: "Session",
		usage:   "report [--step=<N>] [--level=<lvl> | --min-level=<lvl>] [--copy]",
		summary: "Inspect/copy the last run's logs by step and level",
		flags: []helpItem{
			{"--step", "=<N>", "Only that step (default: all)"},
			{"--level", "=<lvl>", "Only lines of that level (debug…critical)"},
			{"--min-level", "=<lvl>", "That level and more severe"},
			{"--copy", "", "Copy the matched lines (default: print)"},
		},
		notes: []string{
			"Reads the record the last `run <file>` persisted, so it works across invocations",
			"--level and --min-level are mutually exclusive and take the value after `=` only",
		},
	},
	{
		name:    "logview",
		section: "Session",
		usage:   "logview [--from <target> | --remote] [--list | --json | --last | --clear [--force]]",
		summary: "Browse past commands' logs (filter by text and level)",
		flags: []helpItem{
			{"--from", "<target>", "Browse a named connect target's log history over SSH (read-only)"},
			{"--remote", "", "Browse this directory's linked remote's log history"},
			{"--list", "", "Print the run list without the interactive browser"},
			{"--json", "", "Dump the run list as JSON to stdout (headless / agents)"},
			{"--last", "", "Open the most recent run directly"},
			{"--clear", "", "Delete this project's log history (local only)"},
			{"--force", "", "With --clear: skip the confirmation"},
		},
		notes: []string{
			"Every dispatched command (REPL, one-shot, recipe step) saves its output under ~/.config/echo/cmd-logs/",
			"Without --list or --json it opens a full-screen browser and needs a TTY",
			"--json prints a JSON array of run records (command, args, exit, timestamps, db) on stdout; `[]` when empty",
			"Each `watch` cycle writes a `watch-deploy` record with `deployed_tip`: poll it to learn whether a commit shipped",
		},
		examples: []helpExample{
			{"logview --json", "List past runs as JSON"},
			{"logview --from staging --list", "Print the run list recorded on the staging server"},
		},
	},
	{
		name:    "sequence",
		section: "Session",
		usage:   "sequence [--from <target> | --remote] [--last] [--continue-on-error]",
		summary: "Pick several commands in order and run them (tri-state Tab)",
		flags: []helpItem{
			{"--remote", "", "Run the whole sequence on this directory's linked remote"},
			{"--from", "<target>", "Run the whole sequence on a named connect target"},
			{"--last", "", "Repeat the last sequence run for this project (no picker)"},
			{"--continue-on-error", "", "Run every step instead of stopping at the first failure"},
		},
		notes: []string{
			"Without --last it opens the command picker and a review, so it needs a TTY",
			"--last replays the plan saved by the previous sequence of this project headlessly; none saved → exit 2",
			"Remote sequences only offer up, stop, restart, logs, i18n-pull and deploy",
			"Headless multi-step runs are usually a recipe instead: see `help scripting`",
		},
		examples: []helpExample{
			{"sequence --last", "Replay this project's last sequence without prompts"},
			{"sequence --remote --last --continue-on-error", "Replay it on the linked remote, running every step"},
		},
	},
	{
		name:    "clear",
		section: "Session",
		usage:   "clear",
		summary: "Clear screen and reprint header",
	},
	{
		name:    "help",
		section: "Session",
		usage:   "help [<command> | <topic> | <text>] [--json]",
		summary: "Show this help",
		flags: []helpItem{
			{"--json", "", "Print the index, page, topic or search result as one JSON document on stdout"},
		},
		notes: []string{
			"`help <command>` and `<command> --help` print one command's page; topics: scripting, build, reverb, agents",
			"Any other text searches command names, flags and descriptions; no match → exit 2",
			"--json prints the same content as one JSON document on stdout",
		},
		examples: []helpExample{
			{"help deploy", "Usage, flags, notes and examples of deploy"},
			{"help agents", "How to drive Echo headlessly"},
			{"help checkpoint --json", "The checkpoint page as JSON"},
		},
	},
	{
		name:    "exit",
		section: "Session",
		usage:   "exit, quit, Ctrl+D, Ctrl+X",
		summary: "Quit Echo",
	},
}

var helpTopics = []helpTopic{
	{"scripting", "Scripting (one-shot, outside the REPL)", []helpItem{
		{"echo_cli <cmd> [args]", "", "Run one command and exit with a status code"},
		{"echo_cli run <file>", "", "Run a recipe (one command per line); - reads stdin"},
		{"  --pick", "", "Pick a .echo recipe from the current directory"},
		{"  --last", "", "Run the most recently created .echo recipe"},
		{"  --continue-on-error", "", "Run every step instead of stopping at the first failure"},
		{"  --log[=<path>]", "", "Save a plain transcript (default dir, a file, or --log=. for ./<recipe>.log)"},
		{"  <step> --silent[=lvl]", "", "Silence a step's output (screen+log); =lvl keeps that level and above"},
		{"echo_cli -C <dir> <cmd>", "", "Run from outside the project directory"},
	}},
	{"build", "Build mode (compose interactively)", []helpItem{
		{"<cmd> --build", "", "Interactively compose the command (pickers + flags), then run/copy it"},
	}},
	{"reverb", "Reverb mode (resolve a target by name)", []helpItem{
		{"link <target>", "", "A Reverb environment is a plain connect target — register it and link it"},
		{"  target from", "", "Reverb's Overview hands out the [connect_targets.<env>] snippet"},
		{"  free from it", "", "stage, db, odoo version, and push landing in the environment's overlay"},
		{"  push --clean", "", "Empties the overlay (--all = every module in it)"},
		{"  [reverb] token", "", "Optional: adds snapshots for checkpoint and API lifecycle verbs"},
		{"  without it", "", "checkpoint and up/stop/restart run through compose; one line says so"},
		{"  link --show", "", "Reports the environment and whether the API is reachable (api=on|off)"},
		{"-E <project>/<env>", "", "Deprecated: resolves over HTTP per call, persists nothing — use link"},
		{"  -E does not", "", "support deploy, watch or i18n-pull; a linked target supports all three"},
	}},
	{"agents", "Agents (headless use)", []helpItem{
		{"echo_cli <cmd> [args]", "", "Run one command and exit; no REPL, no banner"},
		{"-C <dir|alias>", "", "Run against a project from anywhere (`alias` registers names)"},
		{"stdout / stderr", "", "With --json, stdout carries only the JSON payload; every log line goes to stderr"},
		{"ECHO_LOG_FORMAT=json", "", "Every log line becomes one JSON object {time,level,logger,msg,fields,text}"},
		{"clipboard", "", "No automatic copy without a TTY; ECHO_NO_CLIPBOARD=1 disables it on a terminal too"},
		{"exit codes", "", "0 ok · 1 error or ERROR/CRITICAL lines · 2 usage, or a picker/confirm without a TTY · 3 cancelled"},
		{"no TTY", "", "Every picker needs its flag (--commits, --modules, --auto, <mod...>); prod confirms need --force"},
		{"remote targets", "", "--from <target> names a connect target; --remote uses this directory's link (see link)"},
		{"live data", "", "probe -c <expr> --json (or ref, read, fields, param, arch) reads the target's DB in a read-only transaction"},
		{"review, then run", "", "deploy --dry-run [--save-plan <file>], then deploy --apply <file> runs exactly that plan"},
		{"multi-step", "", "run <file> executes a recipe; sequence --last replays the last sequence"},
		{"past runs", "", "logview --json lists runs; report inspects the last recipe run by step and level"},
		{"help <cmd> --json", "", "A command's usage, flags, notes and examples as JSON; help --json for all of them"},
	}},
}

func docFor(name string) (commandDoc, bool) {
	if name == "quit" {
		name = "exit"
	}
	for _, d := range commandDocs {
		if d.name == name {
			return d, true
		}
	}
	return commandDoc{}, false
}

func topicFor(name string) (helpTopic, bool) {
	for _, t := range helpTopics {
		if t.name == name {
			return t, true
		}
	}
	return helpTopic{}, false
}

// label is the left column of a command's pager row: its usage up to the
// first flag token, so positionals stay ("install <mod...>").
func (d commandDoc) label() string {
	var kept []string
	for _, tok := range strings.Fields(d.usage) {
		if strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "[-") {
			break
		}
		kept = append(kept, tok)
	}
	return strings.Join(kept, " ")
}

func (d commandDoc) hasFlag(flag string) bool {
	for _, f := range d.flags {
		if f.name == flag {
			return true
		}
	}
	return false
}

func (d commandDoc) isRemote() bool { return d.hasFlag("--from") || d.hasFlag("--remote") }

func (it helpItem) label() string {
	if it.arg == "" {
		return it.name
	}
	if strings.HasPrefix(it.arg, "=") || strings.HasPrefix(it.arg, "[") {
		return it.name + it.arg
	}
	return it.name + " " + it.arg
}

func helpSections() []helpSection {
	var out []helpSection
	for _, d := range commandDocs {
		if len(out) == 0 || out[len(out)-1].title != d.section {
			out = append(out, helpSection{title: d.section})
		}
		sec := &out[len(out)-1]
		sec.items = append(sec.items, helpEntry{d.label(), d.summary})
		for _, it := range d.subs {
			sec.items = append(sec.items, helpEntry{"  " + it.label(), it.desc})
		}
		for _, it := range d.flags {
			sec.items = append(sec.items, helpEntry{"  " + it.label(), it.desc})
		}
	}
	return out
}

func topicEntries(t helpTopic) []helpEntry {
	out := make([]helpEntry, len(t.rows))
	for i, r := range t.rows {
		out[i] = helpEntry{r.name, r.desc}
	}
	return out
}
