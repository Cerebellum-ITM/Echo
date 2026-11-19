# Fed to `odoo shell` on stdin by `echo_cli i18n-update` on series below 19;
# runs in the shell's namespace (env). `--i18n-import=<path>` cannot be used:
# Odoo's file_open refuses any path outside the addons paths and load_file
# swallows that FileNotFoundError, so the run loads nothing and exits 0.
# Loading from a file object opened here avoids file_open. The arguments arrive
# as base64 JSON so the path and language are never spliced into this source.
import base64
import json
import sys

ARGS = json.loads(base64.b64decode("__ECHO_I18N_ARGS__"))
PATH, LANG = ARGS["path"], ARGS["lang"]

if not env["res.lang"].search_count([("code", "=", LANG), ("active", "=", True)]):
    sys.exit("language %s is not active in the database" % LANG)

try:
    po = open(PATH, "rb")
except OSError as exc:
    sys.exit("cannot open the .po: %s" % exc)

with po:
    try:
        from odoo.tools.translate import TranslationImporter
    except ImportError:
        # Odoo 15 and older: trans_load opens the path through file_open too,
        # so the file object goes to trans_load_data instead.
        from odoo.tools.translate import trans_load_data
        trans_load_data(env.cr, po, "po", LANG, overwrite=True)
    else:
        importer = TranslationImporter(env.cr)
        importer.load(po, "po", LANG)
        importer.save(overwrite=True)

env.cr.commit()
print("loaded %s for %s" % (PATH, LANG))
