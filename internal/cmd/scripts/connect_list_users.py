import json
import logging
import os

logging.getLogger().setLevel(logging.ERROR)

import odoo
import odoo.modules.registry
from odoo.api import Environment

dbname = os.environ["ECHO_DB"]
include_inactive = os.environ.get("ECHO_INCLUDE_INACTIVE") == "1"


def _conn_args():
    # Same connection flags Echo passes to other Odoo CLI invocations
    # (install/update/shell). Required because parse_config([]) falls
    # back to the Unix socket default, which does not exist when
    # Postgres runs in a separate compose service.
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
    domain = [] if include_inactive else [("active", "=", True)]
    users = (
        env["res.users"]
        .with_context(active_test=False)
        .search_read(domain, ["id", "login", "name", "active"], order="login")
    )
print(json.dumps(users))
