# extdb

Browse, edit and delete records of **external SQLite databases** (files not managed by PocketBase)
from the dashboard ("External DB" tab), with a signed webhook to the main server after every change.

PocketBase never creates tables or runs migrations in these files.

## Run

```sh
go build -o pocketbase ./examples/base
./pocketbase serve --extdb=./extdb.json        # or env PB_EXTDB_CONFIG=./extdb.json
./pocketbase serve --extdb=./extdb.json --extdbDir=/srv/familylink-api/db   # or env PB_EXTDB_DIR
```

See [`extdb.example.json`](extdb.example.json).

| Option | |
|---|---|
| `databases[].name` | id used in urls/UI (`a-z 0-9 _ -`) |
| `baseDir` | directory for the relative `databases[].path` values; overridden by `--extdbDir` / `PB_EXTDB_DIR`; a relative value is resolved against the config file directory (default: the config file directory) |
| `databases[].path` | SQLite file path (must be on the same machine); absolute, or relative to `baseDir` |
| `databases[].readonly` | open with `mode=ro` - no edits/deletes at all |
| `databases[].editableTables` | tables allowed to be updated (`"*"` = all); default none |
| `databases[].deletableTables` | tables allowed to be deleted from (`"*"` = all); default none |
| `hook.url` | main server endpoint, POSTed after every update/delete |
| `hook.secret` | HMAC secret for `X-ExtDB-Signature` |
| `hook.headers` | extra headers (e.g. `api-key`) |

`baseDir` and `databases[].path` may contain `$VAR` / `${VAR}` env references.

Only tables with a single-column primary key (or a rowid) can be edited/deleted. Views are read-only.
Date/time columns are read and written as raw text, so values round-trip unchanged.

## Request logs in the Logs page

`logs[]` shows an external request logs table (familylink-api `system_logs` format) in the dashboard **Logs** page,
switchable with the "PocketBase" / "<label>" buttons next to the title:

```json
"logs": [{ "name": "familylink", "label": "Familylink", "database": "logs", "table": "system_logs" }]
```

| Option | |
|---|---|
| `logs[].name` | id used in urls (`a-z 0-9 _ -`) |
| `logs[].label` | button label (default `name`) |
| `logs[].database` | name of a configured `databases[]` entry |
| `logs[].table` | default `system_logs` |

The rows are read directly (nothing is copied) and normalized into the PocketBase log shape, so the chart, the list,
the log details and the filter syntax work as for the PocketBase logs:

- `message` = `METHOD /url` (`WS ...` for websocket messages); `level` = 8 (ERROR) for 5xx, 4 (WARN) for 4xx, else 0.
- `data.status`, `data.method`, `data.url`, `data.execTime` (ms), `data.userIP`, `data.requestId` are filterable,
  e.g. `data.status >= 400`, `data.execTime > 1000`, `data.method = "WS"`, `message ~ "/v1/family"`.
  A plain search term also matches the raw request headers/body and response.
- `data.authId` (user uuid from the jwt `key` claim or the websocket path), `data.userAgent`, `data.request`
  (headers/body/queries) and `data.error` (response message of 4xx/5xx) are display only.
- Older rows that logged the response in a separate row (same `uuid`, empty `path`) are merged into one entry.
- The `Api-Key` / `Authorization` / cookie headers, `authorization=`/`token=`/`api_key=`... url params and
  `password`/`secret`/`token` body keys are masked. **familylink-api logs these in plain text**, so the table
  itself still contains them.
- The list is always sorted by id (newest first); responses are truncated to 1000 chars in the list and returned in full
  by the log details.

## API (superusers only)

```
GET    /api/extdb/databases
GET    /api/extdb/databases/{db}/tables[?refresh=1]
GET    /api/extdb/databases/{db}/tables/{table}/records?page=&perPage=&sort=-col&search=&filters=[{"column","op","value"}]
GET    /api/extdb/databases/{db}/tables/{table}/records/{id}
PATCH  /api/extdb/databases/{db}/tables/{table}/records/{id}   body: {"col": "value", "other": null}
DELETE /api/extdb/databases/{db}/tables/{table}/records/{id}
GET    /api/extdb/logs
GET    /api/extdb/logs/{source}?page=&perPage=&sort=&filter=
GET    /api/extdb/logs/{source}/stats?filter=
GET    /api/extdb/logs/{source}/{id}
```

Filter ops: `= != > >= < <= ~ !~ like notlike isnull notnull` (`~` wraps the value in `%...%`, `like` uses it as is).
The dashboard searchbar accepts `col = value`, `col >= 10`, `col ~ text`, `col like '%abc%'`, `col is null` /
`col is not null`, or a plain search term. Wrapping quotes/backticks are stripped and `==` / `<>` work as `=` / `!=`.

## Hook

`POST hook.url` with JSON:

```json
{
  "event": "update",               // or "delete"
  "database": "main", "table": "admin_role", "primaryKey": "id", "id": "3",
  "before": {...}, "after": {...}, "changes": {"role": "admin,ai"},
  "actor": {"id": "...", "email": "..."},
  "timestamp": 1791000000
}
```

Headers: `X-ExtDB-Event`, `X-ExtDB-Timestamp`, `X-ExtDB-Signature = hex(HMAC_SHA256(secret, timestamp + "." + rawBody))`.
Verify the signature and reject old timestamps on the main server. A failed hook doesn't roll back
the change - the dashboard shows a warning toast and the event is logged (Logs tab, `data.type = "extdb"`).

Writes go straight to the SQLite file: the main server's caches, replicas and events are only
refreshed through what the hook endpoint does.
