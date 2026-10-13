# Session handoff: extdb plugin (2026-10-13)

Fork `github.com/truongnhan0311/pocketbase`, branch `fm` (based on upstream v0.40.5, commit `9695ca1a`).
**All changes are uncommitted.** Goal from the user: an admin dashboard to **view / edit / delete** records of the
external SQLite DBs of `../familylink-api` (`db/familylinkdb*`, 7 files), **connecting to many DBs**, with a
**hook to the main server API** after each change, integrated into the PocketBase UI.

PocketBase collections can't map the existing tables (PocketBase owns the schema and its own text `id`), so this is a
plugin that talks to the files directly and never creates tables in them.

## Changes

| File | What |
|---|---|
| `go.mod` | module path restored to `github.com/pocketbase/pocketbase` (the fork had renamed it in `ff232411` without updating imports, so it did not build) |
| `plugins/extdb/config.go` | `Config` (file `databases[]` + `hook`), per-DB `readonly`, `editableTables` / `deletableTables` (`"*"` = all, default none) |
| `plugins/extdb/schema.go` | tables/views via `sqlite_master` + `pragma_table_info`; pk = single pk column, else `rowid` (none for WITHOUT ROWID composite pk) |
| `plugins/extdb/records.go` | list (page/perPage/sort/search/filters), get, update, delete in a tx; date/time columns are read with `CAST(... AS TEXT)` so they round-trip |
| `plugins/extdb/hook.go` | signed POST: `X-ExtDB-Event`, `X-ExtDB-Timestamp`, `X-ExtDB-Signature = hex(HMAC_SHA256(secret, ts + "." + body))`, extra `headers` |
| `plugins/extdb/extdb.go` | routes `/api/extdb/*` behind `apis.RequireSuperuserAuth()`; lazy `sql.Open("sqlite3", "file:..?_pragma=busy_timeout(10000)[&mode=ro]")`; schema cache (`?refresh=1`); logs each change in the PocketBase Logs tab (`data.type = "extdb"`) |
| `plugins/extdb/README.md`, `extdb.example.json` | docs and example config |
| `examples/base/main.go` | flag `--extdb` / env `PB_EXTDB_CONFIG` → `extdb.MustRegister` |
| `ui/src/extdb/pageExtDB.js` | route `#/extdb?db=&table=&search=`: sidebar (DB select + tables), records table, sort, pagination; searchbar accepts `col = v`, `col >= 10`, `col ~ text`, `col is [not] null`, or free text |
| `ui/src/extdb/extdbRecordModal.js` | `app.modals.openExtDBRecord`: per-column inputs, NULL toggle, PATCH only changed fields, delete with confirm, warning toast if the hook fails |
| `ui/src/css/extdb.css`, `_main.css`, `router.js`, `store.js` (header link "External DB"), `main.js` | wiring |
| `ui/dist/*` | rebuilt with `cd ui && npm run build` (dprint formats the sources) |

The UI is **Shablon + vanilla JS** (`t.div(...)`, `store()`, `watch()`), not Svelte. Two gotchas:
- `app.components.select` only syncs `value` when it changes, so it is rendered after the options load.
- The searchbar is a code editor, not an `<input>`.

## Verified
- API (curl) against **copies** of `familylinkdb` and `familylinkdb_ai`:
  - 401 without auth; 403 for tables not in the allowlist or a readonly DB; 400 for an unknown column.
  - list / search / filter / sort / update / delete all work.
  - The mock hook received a valid HMAC signature, the `api-key` header, `before` / `after` / `changes` and the actor email.
- UI with headless Chrome over CDP (`.claude/sessions/extdb-test/ui_test.mjs`, `hook_server.py`):
  - The header tab shows; the sidebar lists 32 tables; DB select shows `main`.
  - The modal edit saves and shows a toast; filters return 1 / 2 / 1 rows as expected; no console errors.
- Not tested in the UI: switching to a readonly DB (only covered at the API level).

## How to run / test
```sh
go build -o /tmp/pb ./examples/base
/tmp/pb superuser upsert test@example.com 'Test123456!' --dir /tmp/pb_data
/tmp/pb serve --http 127.0.0.1:8091 --dir /tmp/pb_data --extdb ./extdb.json
python3 -I .claude/sessions/extdb-test/hook_server.py /tmp/hooks.log    # mock hook on :3998, secret "test-secret"
# headless UI test: start Chrome with --remote-debugging-port=9333, then
node .claude/sessions/extdb-test/ui_test.mjs 9333 http://127.0.0.1:8091 '<{"token":..,"record":..} from auth-with-password>' /tmp/shots
```
Always test against **copies** of the DB files, never the live `familylink-api/db`.

## Next steps
1. **Hook receiver in `familylink-api`** (`POST /v1/admin/extdb/hook`): verify the HMAC and timestamp, invalidate caches
   (`admin_role_<uuid>`, uuid patterns), optionally re-sync to the MySQL replica. See `../familylink-api/.claude/sessions/2026-10-13-handoff.md`.
2. Commit on `fm` (the user hasn't asked yet). Keep `ui/dist` in the commit (PocketBase embeds it).
3. Possible improvements:
   - Create record (not implemented; the user asked for view / edit / delete).
   - Auto-set `updated_at` on edit (format must match the goravel writes, e.g. `2026-10-07 09:32:48.9+00:00`).
   - Per-table edit permissions in the UI.
   - Count is `COUNT(*)` per page load, which is slow on huge tables.
- Writes bypass `familylink-api` caches, replica and events; only the hook can compensate. SQLite allows a single
  writer, and `busy_timeout(10000)` makes writes wait for the main app.
