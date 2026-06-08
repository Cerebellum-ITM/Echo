import json
import logging
import os

logging.getLogger().setLevel(logging.ERROR)

import odoo
import odoo.modules.registry
from odoo.api import Environment
from odoo.http import root

dbname = os.environ["ECHO_DB"]
uid = int(os.environ["ECHO_UID"])


def _conn_args():
    # Mirror Echo's connection flags so parse_config picks the same
    # Postgres the running Odoo uses (separate compose service, not
    # the local Unix socket).
    pairs = (
        ("ECHO_DB_HOST", "--db_host"),
        ("ECHO_DB_PORT", "--db_port"),
        ("ECHO_DB_USER", "--db_user"),
        ("ECHO_DB_PASSWORD", "--db_password"),
    )
    return [f"{flag}={os.environ[var]}" for var, flag in pairs if os.environ.get(var)]


odoo.tools.config.parse_config(_conn_args())
registry = odoo.modules.registry.Registry(dbname)
with registry.cursor() as cr:
    env = Environment(cr, odoo.SUPERUSER_ID, {})
    user = env["res.users"].browse(uid)
    login = user.login
    user_context = dict(user.context_get())
    base_url = (
        env["ir.config_parameter"].sudo().get_param("web.base.url") or ""
    ).rstrip("/")

    # root.session_store.new() is the canonical Session initializer in
    # Odoo 17+: it returns a Session object pre-populated with every
    # field the HTTP layer expects (_trace, debug, geoip, etc.). The
    # previous manual `Session(get_default_session(), sid, new=True)`
    # construction left some of those fields unset, which made the
    # runtime treat the saved session as invalid even though the file
    # existed on disk.
    session = root.session_store.new()
    session.update({
        "db": dbname,
        "login": login,
        "uid": uid,
        "context": user_context,
    })
    session["session_token"] = user._compute_session_token(session.sid)
    root.session_store.save(session)

print(json.dumps({
    "sid": session.sid,
    "session_file": root.session_store.get_session_filename(session.sid),
    "login": login,
    "uid": uid,
    "base_url": base_url,
}))
