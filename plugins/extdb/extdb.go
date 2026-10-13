// Package extdb lets superusers browse, edit and delete records of external
// SQLite databases (not managed by PocketBase) from the dashboard, and notifies
// a main server through a signed webhook after every change.
//
// Example:
//
//	extdb.MustRegister(app, extdb.Config{
//		ConfigFile: "./extdb.json",
//	})
package extdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

const queryTimeout = 30 * time.Second

type plugin struct {
	app    core.App
	config Config

	mu      sync.Mutex
	dbs     map[string]*sql.DB
	schemas map[string][]*Table // cached per database, reset with ?refresh=1
}

// MustRegister registers the extdb plugin and panics on error.
func MustRegister(app core.App, config Config) {
	if err := Register(app, config); err != nil {
		panic(err)
	}
}

// Register registers the extdb api routes (/api/extdb/*, superusers only).
func Register(app core.App, config Config) error {
	if err := config.load(); err != nil {
		return err
	}

	p := &plugin{
		app:     app,
		config:  config,
		dbs:     map[string]*sql.DB{},
		schemas: map[string][]*Table{},
	}

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		g := e.Router.Group("/api/extdb").Bind(apis.RequireSuperuserAuth())
		g.GET("/databases", p.handleDatabases)
		g.GET("/databases/{db}/tables", p.handleTables)
		g.GET("/databases/{db}/tables/{table}/records", p.handleList)
		g.GET("/databases/{db}/tables/{table}/records/{id}", p.handleView)
		g.PATCH("/databases/{db}/tables/{table}/records/{id}", p.handleUpdate)
		g.DELETE("/databases/{db}/tables/{table}/records/{id}", p.handleDelete)
		g.GET("/logs", p.handleLogSources)
		g.GET("/logs/{source}", p.handleLogsList)
		g.GET("/logs/{source}/stats", p.handleLogsStats)
		g.GET("/logs/{source}/{id}", p.handleLogsView)
		return e.Next()
	})

	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		p.mu.Lock()
		for _, db := range p.dbs {
			db.Close()
		}
		p.dbs = map[string]*sql.DB{}
		p.mu.Unlock()
		return e.Next()
	})

	return nil
}

func (p *plugin) databaseConfig(name string) (DatabaseConfig, bool) {
	for _, db := range p.config.Databases {
		if db.Name == name {
			return db, true
		}
	}
	return DatabaseConfig{}, false
}

// open lazily opens (and caches) the connection pool of an external database.
// The busy timeout lets writes wait for the main app instead of failing with "database is locked".
func (p *plugin) open(cfg DatabaseConfig) (*sql.DB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if db, ok := p.dbs[cfg.Name]; ok {
		return db, nil
	}

	dsn := "file:" + cfg.Path + "?_pragma=busy_timeout(10000)"
	if cfg.ReadOnly {
		dsn += "&mode=ro&_pragma=query_only(1)"
	}

	// "sqlite3" is the modernc driver registered by the PocketBase core
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	p.dbs[cfg.Name] = db
	return db, nil
}

func (p *plugin) tables(ctx context.Context, cfg DatabaseConfig, refresh bool) ([]*Table, error) {
	p.mu.Lock()
	cached, ok := p.schemas[cfg.Name]
	p.mu.Unlock()
	if ok && !refresh {
		return cached, nil
	}

	db, err := p.open(cfg)
	if err != nil {
		return nil, err
	}

	tables, err := loadTables(ctx, db, cfg)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.schemas[cfg.Name] = tables
	p.mu.Unlock()
	return tables, nil
}

// resolve loads the database config, connection and table referenced by the request path.
func (p *plugin) resolve(e *core.RequestEvent) (DatabaseConfig, *sql.DB, *Table, error) {
	cfg, ok := p.databaseConfig(e.Request.PathValue("db"))
	if !ok {
		return cfg, nil, nil, e.NotFoundError("Unknown database.", nil)
	}

	db, err := p.open(cfg)
	if err != nil {
		return cfg, nil, nil, e.InternalServerError("Failed to open the database.", err)
	}

	tables, err := p.tables(e.Request.Context(), cfg, false)
	if err != nil {
		return cfg, nil, nil, e.InternalServerError("Failed to load the database schema.", err)
	}

	name := e.Request.PathValue("table")
	idx := slices.IndexFunc(tables, func(t *Table) bool { return t.Name == name })
	if idx < 0 {
		return cfg, nil, nil, e.NotFoundError("Unknown table.", nil)
	}

	return cfg, db, tables[idx], nil
}

type databaseInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readonly"`
}

func (p *plugin) handleDatabases(e *core.RequestEvent) error {
	result := make([]databaseInfo, 0, len(p.config.Databases))
	for _, db := range p.config.Databases {
		result = append(result, databaseInfo{Name: db.Name, Path: db.Path, ReadOnly: db.ReadOnly})
	}
	return e.JSON(http.StatusOK, map[string]any{
		"databases": result,
		"hook":      p.config.Hook.URL != "",
	})
}

func (p *plugin) handleTables(e *core.RequestEvent) error {
	cfg, ok := p.databaseConfig(e.Request.PathValue("db"))
	if !ok {
		return e.NotFoundError("Unknown database.", nil)
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), queryTimeout)
	defer cancel()

	tables, err := p.tables(ctx, cfg, e.Request.URL.Query().Get("refresh") != "")
	if err != nil {
		return e.InternalServerError("Failed to load the database schema.", err)
	}
	return e.JSON(http.StatusOK, tables)
}

func (p *plugin) handleList(e *core.RequestEvent) error {
	_, db, table, err := p.resolve(e)
	if err != nil {
		return err
	}

	q := e.Request.URL.Query()
	params := ListParams{Sort: q.Get("sort"), Search: q.Get("search")}
	params.Page, _ = strconv.Atoi(q.Get("page"))
	params.PerPage, _ = strconv.Atoi(q.Get("perPage"))
	if raw := q.Get("filters"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &params.Filters); err != nil {
			return e.BadRequestError("Invalid filters.", err)
		}
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), queryTimeout)
	defer cancel()

	result, err := listRecords(ctx, db, table, params)
	if err != nil {
		return e.BadRequestError("Failed to list records: "+err.Error(), nil)
	}
	return e.JSON(http.StatusOK, result)
}

func (p *plugin) handleView(e *core.RequestEvent) error {
	_, db, table, err := p.resolve(e)
	if err != nil {
		return err
	}
	if table.PrimaryKey == "" {
		return e.BadRequestError("The table has no addressable primary key.", nil)
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), queryTimeout)
	defer cancel()

	record, err := getRecord(ctx, db, table, e.Request.PathValue("id"))
	if err != nil {
		return e.BadRequestError("Failed to load the record: "+err.Error(), nil)
	}
	if record == nil {
		return e.NotFoundError("", nil)
	}
	return e.JSON(http.StatusOK, record)
}

func (p *plugin) handleUpdate(e *core.RequestEvent) error {
	cfg, db, table, err := p.resolve(e)
	if err != nil {
		return err
	}
	if !table.Editable {
		return e.ForbiddenError("Editing records of this table is not allowed.", nil)
	}

	changes := map[string]any{}
	decoder := json.NewDecoder(e.Request.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&changes); err != nil {
		return e.BadRequestError("Invalid request body.", err)
	}
	delete(changes, pkKey)

	ctx, cancel := context.WithTimeout(e.Request.Context(), queryTimeout)
	defer cancel()

	id := e.Request.PathValue("id")
	before, after, err := updateRecord(ctx, db, table, id, changes)
	if errors.Is(err, sql.ErrNoRows) {
		return e.NotFoundError("", nil)
	}
	if err != nil {
		return e.BadRequestError("Failed to update the record: "+err.Error(), nil)
	}

	hook := p.notify(e, cfg, table, "update", id, before, after, changes)
	return e.JSON(http.StatusOK, map[string]any{"record": after, "hook": hook})
}

func (p *plugin) handleDelete(e *core.RequestEvent) error {
	cfg, db, table, err := p.resolve(e)
	if err != nil {
		return err
	}
	if !table.Deletable {
		return e.ForbiddenError("Deleting records of this table is not allowed.", nil)
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), queryTimeout)
	defer cancel()

	id := e.Request.PathValue("id")
	before, err := deleteRecord(ctx, db, table, id)
	if errors.Is(err, sql.ErrNoRows) {
		return e.NotFoundError("", nil)
	}
	if err != nil {
		return e.BadRequestError("Failed to delete the record: "+err.Error(), nil)
	}

	hook := p.notify(e, cfg, table, "delete", id, before, nil, nil)
	return e.JSON(http.StatusOK, map[string]any{"hook": hook})
}

// notify logs the change (visible in the dashboard Logs) and calls the main server hook.
func (p *plugin) notify(e *core.RequestEvent, cfg DatabaseConfig, table *Table, event string, id string, before, after, changes map[string]any) HookResult {
	actor := HookActor{}
	if e.Auth != nil {
		actor = HookActor{ID: e.Auth.Id, Email: e.Auth.Email()}
	}

	hook := sendHook(context.Background(), p.config.Hook, HookPayload{
		Event:      event,
		Database:   cfg.Name,
		Table:      table.Name,
		PrimaryKey: table.PrimaryKey,
		ID:         id,
		Before:     before,
		After:      after,
		Changes:    changes,
		Actor:      actor,
		Timestamp:  time.Now().Unix(),
	})

	attrs := []any{
		slog.String("type", "extdb"),
		slog.String("event", event),
		slog.String("database", cfg.Name),
		slog.String("table", table.Name),
		slog.String("id", id),
		slog.String("actor", actor.Email),
		slog.Any("changes", changes),
		slog.Bool("hookSent", hook.Sent),
		slog.Int("hookStatus", hook.Status),
	}
	if hook.Error != "" {
		attrs = append(attrs, slog.String("hookError", hook.Error))
		p.app.Logger().Warn("extdb "+event+" (hook failed)", attrs...)
	} else {
		p.app.Logger().Info("extdb "+event, attrs...)
	}

	return hook
}
