# Design 23 — CLI (`app/console/commands/`)

**Status:** done

The Artisan command surface. `rllmx artisan <name>` from the binary, `./artisan <name>` from the source tree. The list is in `README.md`; the rules for when a command prompts are in this design doc.

| Command | State | Notes |
|---|---|---|
| `version` | done | Version and commit, printed from the build |
| `health` | unverified | Exits non-zero when the local `/api/health` is not 200. A live listener is what confirms it |
| `db:backup`, `db:check`, `db:vacuum` | done | See design doc 15 |
| `db:import-config`, `db:import-analytics` | done | See design doc 15 |
| `keys:create\|list\|revoke`, `password:reset` | done | Hidden prompt through `ctx.Secret`; three tries, 2 s after a wrong one; the key printed once |
| `db:export-plain` | done | Asks the dashboard password, confirms, then writes the copy |

**Rules**

- A command that changes or reveals a secret prompts with the console `Secret` prompt and never takes the password as a flag or environment variable.
- `db` commands ask no password: whoever can run them can already read the files. `db:export-plain` is the exception, because it writes every credential in plain text.
- Destructive commands confirm on the terminal: `db:vacuum` asks y/N, `db:import-config` shows its plan and asks y/N or takes `--yes`, `db:import-analytics --drop-old` needs a typed `DROP`.
- The confirmation is a typed answer read with `Ask`, never the framework's `Confirm` toggle. Without a terminal a command refuses unless `--yes` is given.
- The allow-list in `bootstrap/commands_filter.go` gates every provider's registration through `providers.CommandsServiceProvider`; a command not on the list does not exist.

---

## Remaining work

 brought `keys:*`, `password:reset` and `db:export-plain`. Every command on the list is built; `health` is `unverified` because only a live listener confirms it, which is a validation step and not missing behavior.

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 195 | Artisan commands (`rllmx artisan db:backup`) on local store only; passwords typed at hidden prompt, never from flag or environment; server starts when no command given | No remote command surface, no secret in process list or environment; one registry listed by `./artisan list` |
| 1, 2, 45 | No bypass header or token; self-calls in-process; terminal commands prompt | No shared long-lived secret, no remote command surface |

---

## Findings

None recorded.
