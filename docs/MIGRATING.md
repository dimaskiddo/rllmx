# Router-LLM - Migrating from 9Router

Moving a 9Router install to Router-LLM, and moving back. The import reads a copy of the 9Router database and seeds a database Router-LLM owns, so the original is never opened and the new install may use SQLite or PostgreSQL. Setup goes through a `.env` file; the shell environment is shown as the alternative.

The source must be a database 9Router itself migrated (schema version 2 and later). The legacy `db.json` and `usage.json` are not imported.

---

## What moves

| Copied | Notes |
|---|---|
| API keys | With the restriction flag and its allow list |
| Providers and connections | Per-model locks, error state and the per-connection proxy settings are cleared; the credential and the rest of the row are kept |
| Custom nodes, combos | As stored |
| Aliases, custom and disabled models, pricing overrides | From the `kv` scopes 9Router writes |
| Settings | Active skills and savers, and the login policy with `--with-sso` |
| Usage history | A separate step, `db:import-analytics` |

Left behind: the dashboard password (the new install keeps its own), 9Router's `requireLogin` and `requireApiKey`, its per-connection proxy settings (they have no effect here; a backup is taken before a start removes them from a database opened in place), and its proxy pool tables, tunnel, MITM and observability settings.

---

## Before you start

These values come from the old install. The two key secrets must match, or every imported API key stops validating:

| 9Router variable | Router-LLM variable | Why |
|---|---|---|
| `API_KEY_SECRET` | `AUTH_API_KEY_SECRET` | The checksum inside every key |
| `MACHINE_ID_SALT` | `AUTH_MACHINE_ID_SALT` | The machine-id salt inside every key |
| `JWT_SECRET` | `JWT_SECRET` | The same name; keeping it keeps dashboard sessions valid |
| `INITIAL_PASSWORD` | `AUTH_INITIAL_PASSWORD` | Not copied; the new install signs in with this one |

Read the old values from the `.env` 9Router runs with, or from its service definition. 9Router stores the JWT secret in `~/.9router/jwt-secret` when the variable is unset. When 9Router left `API_KEY_SECRET` or `MACHINE_ID_SALT` unset it used the public default (`endpoint-proxy-api-key-secret`, `endpoint-proxy-salt`), so leaving the Router-LLM variable empty gives the same value. An unset `JWT_SECRET` means a random secret each side generates into its own data directory, and dashboard sessions do not survive that.

9Router's variable names are not read by Router-LLM. An unknown `AUTH_` or `DB_` name in `.env` stops start-up with `settings: unknown variable NAME`.

---

## 1. Stop 9Router and copy its database

```sh
mkdir -p ~/9router-source && chmod 700 ~/9router-source
for f in data.sqlite data.sqlite-wal data.sqlite-shm; do
  [ -f ~/.9router/db/$f ] && cp ~/.9router/db/$f ~/9router-source/
done
chmod 600 ~/9router-source/*
```

On Windows the default folder is `%APPDATA%\9router\db`. Copy `data.sqlite` and, when present, `data.sqlite-wal` and `data.sqlite-shm`.

The copy still holds 9Router's credentials in plain text and is readable by you alone. Delete it once the import is verified.

## 2. Install Router-LLM

The binary, the container and the source build are in the [README's Deployment section](../README.md#-deployment). Run every command below from the folder that holds `.env`.

## 3. Write `.env`

```sh
cp .env.example .env
./rllmx artisan key:generate     # from a source tree: ./artisan key:generate
```

`key:generate` writes `APP_KEY`, the framework's own key; it is not the secret that seals provider credentials. Then edit `.env`.

SQLite, the default:

```dotenv
# Data: empty means storage/app inside the folder holding .env.
DATA_DIR=

# Required. Exactly 64 hex characters: openssl rand -hex 32
DB_ENCRYPTION_SECRET=
# The first dashboard password; 9Router's is not copied. Choose a strong one:
# empty falls back to the public default.
AUTH_INITIAL_PASSWORD=

# Copied from 9Router, under Router-LLM's names.
AUTH_API_KEY_SECRET=
AUTH_MACHINE_ID_SALT=
JWT_SECRET=
```

The database lands in `DATA_DIR/db/data.sqlite` (`storage/app/db/data.sqlite` with `DATA_DIR` empty), the usage store beside it.

PostgreSQL, for a target that is not SQLite:

```dotenv
DATA_DIR=

DB_CONNECTION=postgres
DB_HOST=db.internal
DB_PORT=5432
DB_DATABASE=rllmx
DB_USERNAME=rllmx
DB_PASSWORD=
DB_SSLMODE=require
# DB_SCHEMA=public
# DB_DATABASE_ANALYTICS=          # empty: rllmx_analytics in the same database

DB_ENCRYPTION_SECRET=
AUTH_INITIAL_PASSWORD=
AUTH_API_KEY_SECRET=
AUTH_MACHINE_ID_SALT=
JWT_SECRET=
```

Notes that save a support round trip:

- A value containing `$` must be single-quoted, or the loader expands `$NAME`.
- `.env` is read from the working directory and the process environment overrides it. That is the alternative: `export AUTH_API_KEY_SECRET=...` before the commands (PowerShell `$env:AUTH_API_KEY_SECRET = "..."`), and the secret stays out of the file.
- Never commit `.env`. Back up `DB_ENCRYPTION_SECRET`: losing it loses every stored credential.
- Do not reuse 9Router's `APP_KEY`; it is new to this install.

## 4. Create the schema

```sh
./rllmx artisan db:migrate
```

No server may be running against the same `DATA_DIR`; the command refuses while another `rllmx` holds the lock.

## 5. Import the configuration

Read the plan first. It prints counts and setting names, never a value:

```sh
./rllmx artisan db:import-config --from ~/9router-source/data.sqlite --dry-run
```

Then run it. Add `--with-sso` to copy the login policy too:

```sh
./rllmx artisan db:import-config --from ~/9router-source/data.sqlite --with-sso
```

It asks `y/N`; `--yes` skips the prompt. The destination must be empty; a run against one that already holds configuration stops with `the destination database is not empty; import-config only seeds an empty one`. The dry run checks the destination the same way.

## 6. Import the usage history

```sh
./rllmx artisan db:import-analytics --from ~/9router-source/data.sqlite
```

Row counts, token sums and cost are verified against the source, and a mismatch exits 1. A rerun adds nothing twice. Do not add `--drop-old`: it is for a source you still own, and this source is a copy.

With SQLite the rows land in `DATA_DIR/db/analytics.sqlite`; with PostgreSQL, in the schema `rllmx_analytics` of the same server.

## 7. Check, then start

```sh
./rllmx artisan db:check
./rllmx artisan keys:list
./rllmx
```

`keys:list` asks for the dashboard password; before one is stored, that is `AUTH_INITIAL_PASSWORD`. Every imported key should read `ok` in the `checksum` column; `mismatch` means `AUTH_API_KEY_SECRET` or `AUTH_MACHINE_ID_SALT` did not carry over.

Open `http://localhost:20128`, sign in, change the password in Settings, then send one request with an imported key. Providers, combos, aliases and usage should match the old dashboard.

## 8. Retire 9Router

Keep it stopped. Providers with rotating refresh tokens (Codex, xAI, MiniMax) would invalidate each other's tokens while both run. Delete `~/9router-source` once the result is verified.

---

## The container

The image has no entrypoint, so the command after the image name replaces the server. Run the migration commands as one-off containers against the same volume, then start the normal container as in the README.

`rllmx.env` holds the same names as `.env`, in plain `KEY=value` lines (Docker's `--env-file` does not read quotes or expand `$`). It replaces `.env` for these commands, so it also needs `APP_KEY`; write a 32-character value there, for example from `openssl rand -hex 16`, because `key:generate` only edits `.env`:

```sh
mkdir -p rllmx-source
sudo cp ~/.9router/db/data.sqlite* rllmx-source/
sudo chown -R 65532:65532 rllmx-source
sudo chmod 700 rllmx-source

docker run --rm -it --env-file rllmx.env \
  -v rllmx-storage:/usr/local/rllmx/storage \
  dimaskiddo/rllmx:latest /usr/local/rllmx/rllmx artisan db:migrate

docker run --rm -it --env-file rllmx.env \
  -v rllmx-storage:/usr/local/rllmx/storage \
  -v "$PWD/rllmx-source:/import:ro" \
  dimaskiddo/rllmx:latest /usr/local/rllmx/rllmx artisan db:import-config --from /import/data.sqlite --with-sso

docker run --rm -it --env-file rllmx.env \
  -v rllmx-storage:/usr/local/rllmx/storage \
  -v "$PWD/rllmx-source:/import:ro" \
  dimaskiddo/rllmx:latest /usr/local/rllmx/rllmx artisan db:import-analytics --from /import/data.sqlite
```

The image runs as uid 65532, which is why the copy is handed to that user. Leave `DATA_DIR` empty: the working directory is `/usr/local/rllmx` and the volume is mounted over `storage`.

---

## Alternative: open 9Router's directory in place

SQLite only. Point `DATA_DIR` at the old directory instead of importing:

```dotenv
# The absolute path of 9Router's data directory.
DATA_DIR=/home/you/.9router
AUTH_API_KEY_SECRET=<9router API_KEY_SECRET>
AUTH_MACHINE_ID_SALT=<9router MACHINE_ID_SALT>
JWT_SECRET=<9router JWT_SECRET>
DB_ENCRYPTION_SECRET=<openssl rand -hex 32>
```

The first start backs the file up to `db/backups/pre-encryption-*` and seals every credential in place; 9Router cannot open it afterwards. Old usage stays until `db:import-analytics` runs with no `--from`. Two limits: PostgreSQL is out, and the file is no longer shareable with 9Router.

## Going back to 9Router

While Router-LLM still runs on SQLite:

```sh
mkdir -p ~/rllmx-export
./rllmx artisan db:export-plain --out ~/rllmx-export
```

The command asks for the dashboard password, then writes a `VACUUM INTO` copy with every credential opened and the sealing rows removed, under `~/rllmx-export/plain-rllmx-<version>-<time>-<id>/data.sqlite`. `--out` must name an existing directory; without it the copy goes under `DATA_DIR/db/backups`.

Then:

1. Stop Router-LLM.
2. Stop 9Router and remove its `db/data.sqlite`, `data.sqlite-wal` and `data.sqlite-shm` from the active folder (move them to a backup folder first; a stale `-wal` must not be left beside a different file).
3. Copy the exported `data.sqlite` into `~/.9router/db/`.
4. Keep `API_KEY_SECRET`, `MACHINE_ID_SALT` and `JWT_SECRET` set to the same values 9Router used before.
5. Start 9Router and check a key and a provider.

The copy holds plain credentials: delete `~/rllmx-export` once 9Router works.

Two limits: the command refuses a PostgreSQL target, and it exports the configuration store only, so usage rows collected in Router-LLM stay in `DATA_DIR/db/analytics.sqlite` or the `rllmx_analytics` schema. With PostgreSQL the import never touched the original 9Router database, so the way back is to start 9Router on that file again; rotating-token providers may then need a fresh login, because Router-LLM refreshed them after the import.

---

## Troubleshooting

| Message | Meaning | Fix |
|---|---|---|
| `no such table: providerConnections` | `--from` is not an already-migrated 9Router database (a legacy `db.json`, an empty file, the wrong path) | Point it at 9Router's own `db/data.sqlite` after 9Router migrated it |
| `the destination database is not empty; import-config only seeds an empty one` | The target already holds connections, keys, nodes, combos or `kv` rows | Use a fresh database, or export and remove the old one first |
| `repository: the database needs a schema change; run rllmx artisan db:migrate, or start the server once` | The target has no schema yet | `./rllmx artisan db:migrate` |
| `lock: another rllmx process is using this data directory` | The server or another command holds `DATA_DIR` | Stop it, then rerun |
| `settings: unknown variable API_KEY_SECRET` (or another 9Router name) | A 9Router variable was pasted into `.env` | Use `AUTH_API_KEY_SECRET`, `AUTH_MACHINE_ID_SALT`, `JWT_SECRET` |
| `keys:list` shows `mismatch` | The two key secrets do not match the source | Copy the old values again; empty is correct only when 9Router left both unset |
| `repository: export-plain needs dialect.SQLite` | Going back was requested on a PostgreSQL install | Start 9Router on its original database file instead |
| `APP_KEY` length or missing error at start-up | `key:generate` was not run in the folder holding `.env` | Rerun it there |
