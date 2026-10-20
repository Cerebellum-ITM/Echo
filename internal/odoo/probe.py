# Fed to `odoo shell` on stdin by `echo_cli probe`; runs in the shell's
# namespace (env, self). The request arrives as base64 JSON so no argument is
# ever spliced into this source.
import base64
import contextlib
import datetime
import io
import json
import re
import sys
import traceback

import odoo
from lxml import etree
from odoo import Command, fields

ARGS = json.loads(base64.b64decode("__ECHO_PROBE_ARGS__"))
FRAME_PREFIX = "@@ECHO_PROBE@@"
FRAME_CHUNK = 32 * 1024
SECRET_KEY = re.compile(r"secret|password|token|api_?key", re.IGNORECASE)
MAJOR = ARGS["major"] or odoo.release.version_info[0]


def deny_commit():
    raise RuntimeError("probe is read-only")


def namespace():
    return {"env": env, "self": env.user, "Command": Command, "fields": fields,
            "datetime": datetime, "json": json}


def to_json(value):
    if isinstance(value, odoo.models.BaseModel):
        return {"model": value._name, "ids": value.ids,
                "display_names": value[:50].mapped("display_name")}
    if isinstance(value, datetime.date):
        return value.isoformat()
    if isinstance(value, bytes):
        return base64.b64encode(value).decode()
    if isinstance(value, (set, frozenset)):
        return list(value)
    return str(value)


def run_eval():
    exprs = ARGS["exprs"]
    ns = namespace()
    values = {item["name"] or item["expr"]: eval(compile(item["expr"], "<expr>", "eval"), ns)
              for item in exprs}
    if len(exprs) == 1 and not exprs[0]["name"]:
        return values[exprs[0]["expr"]]
    return values


def run_script():
    ns = namespace()
    exec(compile(ARGS["script"], "<script>", "exec"), ns)
    return ns.get("result")


def run_ref():
    xmlid = ARGS["xmlid"]
    module, _, name = xmlid.partition(".")
    data = env["ir.model.data"].search([("module", "=", module), ("name", "=", name)], limit=1)
    if not data:
        raise ValueError("External ID not found: %s" % xmlid)
    record = env[data.model].browse(data.res_id).exists()
    return {"xmlid": xmlid, "model": data.model, "id": data.res_id,
            "display_name": record.display_name if record else None,
            "module": data.module, "noupdate": data.noupdate}


def run_read():
    model = env[ARGS["model"]]
    domain = ARGS["domain"] or []
    result = {"model": model._name, "count": model.search_count(domain)}
    if not ARGS["count"]:
        result["rows"] = model.search_read(domain, ARGS["fields"] or None,
                                           limit=ARGS["limit"], order=ARGS["order"] or None)
    return result


def run_fields():
    model = env[ARGS["model"]]
    names = ARGS["names"] or list(model._fields)
    model_fields = env["ir.model.fields"]
    modules = {}
    if "modules" in model_fields._fields:
        found = model_fields.search([("model", "=", model._name), ("name", "in", names)])
        modules = {f.name: f.modules for f in found}
    out = []
    for name in names:
        if name not in model._fields:
            raise ValueError("Field %s does not exist on %s" % (name, model._name))
        field = model._fields[name]
        out.append({
            "name": name, "type": field.type, "string": field.string,
            "relation": getattr(field, "comodel_name", None),
            "required": bool(field.required), "readonly": bool(field.readonly),
            "store": bool(field.store),
            "compute": getattr(field.compute, "__name__", field.compute),
            "related": field.related or None,
            "modules": modules.get(name) or None,
        })
    return out


def run_param():
    params = env["ir.config_parameter"].sudo()
    key = ARGS["key"]
    if key:
        records = (params.search([("key", "=", key)])
                   or params.search([("key", "=like", key + "%")], order="key"))
    else:
        records = params.search([], order="key")
    return [{"key": r.key,
             "value": "***" if not ARGS["reveal"] and SECRET_KEY.search(r.key) else r.value}
            for r in records]


def view_ref(view):
    return {"id": view.id, "xmlid": view.xml_id or None, "name": view.name}


def run_arch():
    target = ARGS["xmlid"]
    views = env["ir.ui.view"]
    if target in env:
        view_type = ARGS["view_type"] or "form"
        if view_type in ("tree", "list"):
            view_type = "tree" if MAJOR < 18 else "list"
        root_id = views.default_view(target, view_type)
        if not root_id:
            raise ValueError("No %s view for %s" % (view_type, target))
        root = views.browse(root_id)
    else:
        root = env.ref(target)
        if root._name != "ir.ui.view":
            raise ValueError("%s is a %s record, not a view" % (target, root._name))
        while root.mode == "extension" and root.inherit_id:
            root = root.inherit_id

    if hasattr(root, "_get_combined_arch"):
        arch = root._get_combined_arch()
    else:
        arch = etree.fromstring(
            env[root.model].get_views([(root.id, root.type)])["views"][root.type]["arch"])
    # Reparsed without blank text, otherwise pretty_print keeps the source layout.
    arch = etree.fromstring(etree.tostring(arch), etree.XMLParser(remove_blank_text=True))

    chain = []
    all_views = views.with_context(active_test=False)
    level = root
    while level:
        level = all_views.search([("inherit_id", "in", level.ids)], order="priority, id")
        chain += [dict(view_ref(v), module=(v.xml_id or "").partition(".")[0] or None,
                       priority=v.priority, mode=v.mode, active=v.active,
                       inherit_id=v.inherit_id.id or None)
                  for v in level]
    return {"model": root.model, "type": root.type, "root": view_ref(root),
            "arch": etree.tostring(arch, pretty_print=True, encoding="unicode"),
            "chain": chain}


RUNNERS = {"eval": run_eval, "script": run_script, "ref": run_ref, "read": run_read,
           "fields": run_fields, "param": run_param, "arch": run_arch}


def describe(exc):
    here = describe.__code__.co_filename
    frames = [f for f in traceback.extract_tb(exc.__traceback__) if f.filename != here]
    deepest = frames[-1] if frames else None
    return {"type": type(exc).__name__, "message": str(exc),
            "traceback": traceback.format_exc(),
            "file": deepest.filename if deepest else None,
            "line": deepest.lineno if deepest else None}


def probe():
    captured = io.StringIO()
    payload = {"ok": True, "kind": ARGS["kind"]}
    try:
        with contextlib.redirect_stdout(captured):
            # Encoded here so a value json cannot handle becomes the probe's error.
            payload["result"] = json.loads(json.dumps(RUNNERS[ARGS["kind"]](), default=to_json))
    except Exception as exc:
        payload = {"ok": False, "kind": ARGS["kind"], "error": describe(exc)}
    payload["stdout"] = captured.getvalue()
    return payload


def emit(payload):
    data = base64.b64encode(json.dumps(payload).encode()).decode()
    chunks = [data[i:i + FRAME_CHUNK] for i in range(0, len(data), FRAME_CHUNK)]
    for seq, chunk in enumerate(chunks, 1):
        sys.stdout.write("%s %d %d %s\n" % (FRAME_PREFIX, seq, len(chunks), chunk))
    sys.stdout.flush()


env.cr.rollback()
# The session default also covers a transaction the probed code opens after its
# own rollback; SET TRANSACTION covers the one already open.
env.cr.execute("SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY")
env.cr.execute("SET TRANSACTION READ ONLY")
env.cr.commit = deny_commit
emit(probe())
# The shell commits when its cursor closes; give it back the real commit.
del env.cr.commit
