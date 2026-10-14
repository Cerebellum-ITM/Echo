# Unit 138 · sideload-obfuscation (proposal, needs a decision)
> Not a spec yet: the options, what each protects, and a spike to run before choosing. Last verified: 2026-10-14.

## Goal

A module sideloaded to a server Echo does not own should not sit there as readable source. Ask: hide the code and,
if possible, the names.

## What Odoo needs to stay readable

Read in `odoo-19.0+e.20260311`:

- `__manifest__.py` is parsed as text with `ast.literal_eval` (`odoo/modules/module.py:53,307`). It stays a
  readable file.
- Code is imported with a plain `__import__` of `odoo.addons.<module>` (`odoo/modules/module.py:505`). Anything the
  standard Python import system loads works: a `.py`, a sourceless `.pyc` placed next to where the `.py` was, or a
  compiled extension (`.so`). The package `__init__.py` can stay as one line of imports.
- XML (views, data, security), CSV, `.po` and the `static/` assets (JS, OWL templates, SCSS) are read as text by
  the server or sent to the browser. They cannot be hidden; JS can only be minified.

## Names cannot be hidden in practice

The technical module name, model names (`_name`), field names, XML ids (`<module>.<id>`) and table names are stored
in the database (`ir_module_module`, `ir_model`, `ir_model_fields`, `ir_model_data`) and appear in the UI, the
logs and every URL. Renaming them at ship time would mean rewriting every reference in Python, XML and JS, and it
gives a different module identity on each target, so updates and the lock break. Proposal: no name rewriting. A
module meant to be discreet gets a neutral technical name and manifest text in the library itself, chosen once.

## Options for the code

| Option | How Echo would do it | Protection | Cost and risk |
|---|---|---|---|
| A. Sourceless bytecode | compile each `.py` to `.pyc` with the **target's** Python (inside its own container), ship the `.pyc`, drop the `.py` | low: bytecode disassembles with `dis`, and decompilers partly handle 3.12 | trivial; tied to the exact Python minor (fuentebuena-dev: 3.12.3, x86_64, glibc 2.39, measured 2026-10-14) |
| B. Cython to native `.so` | build in a throwaway container from the target's image plus `gcc`, `python3-dev` and `cython`, ship the `.so` files | medium-high: native code, no source, only reverse engineering | per target build (Python minor, arch, libc); Odoo's ORM idioms (decorators, `super()`, class attributes) must survive compilation; needs the spike below |
| C. PyArmor | obfuscate with PyArmor and ship its runtime | high | commercial licence for real use, a runtime dependency on the server, same per-Python binding |

All three keep tests, XML and assets as they are. All three are a deterrent, not a lock: whoever runs the code can
observe it.

**Licence:** a module shipped without source should not be LGPL, which obliges giving recipients the source. Use
OPL-1 or a proprietary licence in its manifest; whether the client may hold the code is a contract question.

## Recommendation

Spike B on `forced_config_parameter` before deciding: compile it with Cython in a container from the
fuentebuena-dev image, sideload the result to dev, and run its own tests (`--test-tags /forced_config_parameter`).
If the ORM works compiled, Unit 138 becomes `sideload --compile` (B, with A as a fallback flag); if it does not,
A is the only cheap option and its protection is low enough that it may not be worth a flag.
