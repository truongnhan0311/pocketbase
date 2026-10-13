package extdb

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/search"
)

// External request logs in the familylink-api "system_logs" format:
//
//   - path:     "GET:/v1/family/me?x=1" for http requests, "WS-USER_REQUEST_u_<user>_c_<chat>" for websocket messages
//   - data:     `Request_header:{...} method:GET body:{} queries:{} url:/v1/family/me ip:1.2.3.4 RequestId:<uuid>`
//     (or the raw websocket message json)
//   - response: "[3 ms]Ok::200, <body>" / "[0 ms]Error::401, ..." (older rows store the response in a
//     separate row with the same uuid and an empty path)
//
// They are normalized into the PocketBase log shape (id, created, level, message, data) so that the
// dashboard Logs page and its filter syntax (e.g. `data.status >= 400`) work unchanged.

const (
	logsDefaultPerPage = 50
	logsMaxPerPage     = 500

	// max response length included in the list items (the full one is returned by the view endpoint)
	logsListResponseLimit = 1000
)

func logsSubquery(table string) string {
	t := quoteIdent(table)

	return `(SELECT
	id, created, level, message, method, url, status, execTime, userIP, requestId,
	json_object(
		'type', 'request', 'method', method, 'url', url, 'status', status, 'execTime', execTime,
		'userIP', userIP, 'requestId', requestId, 'request', raw, 'response', resp
	) AS data
FROM (SELECT *,
	CASE WHEN status >= 500 THEN 8 WHEN status >= 400 THEN 4 ELSE 0 END AS level,
	method || ' ' || url AS message
FROM (SELECT
	id, requestId, raw, resp,
	COALESCE(strftime('%Y-%m-%d %H:%M:%fZ', created_at), '') AS created,
	CASE WHEN path LIKE 'WS-%' THEN 'WS' ELSE substr(path, 1, instr(path, ':') - 1) END AS method,
	CASE WHEN path LIKE 'WS-%' THEN substr(path, 4) ELSE substr(path, instr(path, ':') + 1) END AS url,
	CASE WHEN resp LIKE '[%ms]%::%' THEN CAST(substr(resp, instr(resp, '::') + 2) AS INTEGER) END AS status,
	CASE WHEN resp LIKE '[%ms]%' THEN CAST(substr(resp, 2, instr(resp, ' ms]') - 2) AS REAL) END AS execTime,
	CASE WHEN instr(raw, ' ip:') > 0 AND instr(raw, ' RequestId:') > instr(raw, ' ip:')
		THEN substr(raw, instr(raw, ' ip:') + 4, instr(raw, ' RequestId:') - instr(raw, ' ip:') - 4) END AS userIP
FROM (SELECT
	s.id, s.uuid AS requestId, s.created_at, s.path, s.data AS raw,
	COALESCE(NULLIF(s.response, ''), (
		SELECT r.response FROM ` + t + ` r
		WHERE r.uuid = s.uuid AND r.id <> s.id AND COALESCE(r.path, '') = ''
		LIMIT 1
	)) AS resp
FROM ` + t + ` s
WHERE COALESCE(s.path, '') <> '')))) logs`
}

// logsResolver resolves the PocketBase log filter fields against the normalized logs subquery.
// The common data.* keys map to plain columns so that they don't have to be extracted from the json.
type logsResolver struct{}

var logsDataColumns = map[string]bool{
	"method": true, "url": true, "status": true, "execTime": true, "userIP": true, "requestId": true,
}

var logsJSONResolver = search.NewSimpleFieldResolver("data", `^data\.[\w\.\:]*\w+$`)

func (logsResolver) UpdateQuery(query *dbx.SelectQuery) error {
	return nil
}

func (logsResolver) Resolve(field string) (*search.ResolverResult, error) {
	switch field {
	case "id", "created", "level", "message", "data":
		return &search.ResolverResult{Identifier: "[[" + field + "]]"}, nil
	}

	if key, ok := strings.CutPrefix(field, "data."); ok && logsDataColumns[key] {
		return &search.ResolverResult{Identifier: "[[" + key + "]]"}, nil
	}

	return logsJSONResolver.Resolve(field)
}

type logRow struct {
	ID      int64  `db:"id"`
	Created string `db:"created"`
	Level   int    `db:"level"`
	Message string `db:"message"`
	Data    string `db:"data"`
}

type logItem struct {
	ID      string         `json:"id"`
	Created string         `json:"created"`
	Level   int            `json:"level"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

type logsSourceInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

func (p *plugin) logsConfig(name string) (LogsConfig, bool) {
	for _, l := range p.config.Logs {
		if l.Name == name {
			return l, true
		}
	}
	return LogsConfig{}, false
}

// logsQuery returns a new select query from the normalized logs of the requested source.
func (p *plugin) logsQuery(e *core.RequestEvent, cols ...string) (*dbx.SelectQuery, error) {
	cfg, ok := p.logsConfig(e.Request.PathValue("source"))
	if !ok {
		return nil, e.NotFoundError("Unknown logs source.", nil)
	}

	dbCfg, _ := p.databaseConfig(cfg.Database)
	db, err := p.open(dbCfg)
	if err != nil {
		return nil, e.InternalServerError("Failed to open the logs database.", err)
	}

	return dbx.NewFromDB(db, "sqlite").Select(cols...).From(logsSubquery(cfg.Table)), nil
}

func logsFilterExpr(e *core.RequestEvent) (dbx.Expression, error) {
	filter := e.Request.URL.Query().Get(search.FilterQueryParam)
	if filter == "" {
		return nil, nil
	}

	expr, err := search.FilterData(filter).BuildExpr(logsResolver{})
	if err != nil {
		return nil, e.BadRequestError("Invalid filter format.", err)
	}
	return expr, nil
}

func (p *plugin) handleLogSources(e *core.RequestEvent) error {
	result := make([]logsSourceInfo, 0, len(p.config.Logs))
	for _, l := range p.config.Logs {
		result = append(result, logsSourceInfo{Name: l.Name, Label: l.Label})
	}
	return e.JSON(http.StatusOK, result)
}

// handleLogsList lists the newest logs first (the total is never counted, the stats endpoint has it).
func (p *plugin) handleLogsList(e *core.RequestEvent) error {
	query, err := p.logsQuery(e, "id", "created", "level", "message", "data")
	if err != nil {
		return err
	}

	expr, err := logsFilterExpr(e)
	if err != nil {
		return err
	}
	if expr != nil {
		query.AndWhere(expr)
	}

	q := e.Request.URL.Query()
	page, _ := strconv.Atoi(q.Get(search.PageQueryParam))
	page = max(page, 1)
	perPage, _ := strconv.Atoi(q.Get(search.PerPageQueryParam))
	if perPage <= 0 {
		perPage = logsDefaultPerPage
	}
	perPage = min(perPage, logsMaxPerPage)

	order := "id DESC"
	if sort := q.Get(search.SortQueryParam); sort != "" && !strings.HasPrefix(sort, "-") {
		order = "id ASC"
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), queryTimeout)
	defer cancel()

	rows := []logRow{}
	err = query.OrderBy(order).
		Limit(int64(perPage)).
		Offset(int64((page - 1) * perPage)).
		WithContext(ctx).
		All(&rows)
	if err != nil {
		return e.BadRequestError("Failed to list logs: "+err.Error(), nil)
	}

	items := make([]logItem, len(rows))
	for i, row := range rows {
		items[i] = row.toItem(false)
	}

	return e.JSON(http.StatusOK, map[string]any{
		"page":       page,
		"perPage":    perPage,
		"totalItems": -1,
		"totalPages": -1,
		"items":      items,
	})
}

func (p *plugin) handleLogsStats(e *core.RequestEvent) error {
	query, err := p.logsQuery(e, "count(*) as total", "strftime('%Y-%m-%d %H:00:00.000Z', created) as date")
	if err != nil {
		return err
	}

	expr, err := logsFilterExpr(e)
	if err != nil {
		return err
	}
	if expr != nil {
		query.AndWhere(expr)
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), queryTimeout)
	defer cancel()

	stats := []struct {
		Total int    `db:"total" json:"total"`
		Date  string `db:"date" json:"date"`
	}{}
	err = query.GroupBy("date").OrderBy("date ASC").WithContext(ctx).All(&stats)
	if err != nil {
		return e.BadRequestError("Failed to generate logs stats: "+err.Error(), nil)
	}

	return e.JSON(http.StatusOK, stats)
}

func (p *plugin) handleLogsView(e *core.RequestEvent) error {
	id, err := strconv.ParseInt(e.Request.PathValue("id"), 10, 64)
	if err != nil {
		return e.NotFoundError("", nil)
	}

	query, err := p.logsQuery(e, "id", "created", "level", "message", "data")
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), queryTimeout)
	defer cancel()

	row := logRow{}
	err = query.AndWhere(dbx.HashExp{"id": id}).Limit(1).WithContext(ctx).One(&row)
	if errors.Is(err, sql.ErrNoRows) {
		return e.NotFoundError("", nil)
	}
	if err != nil {
		return e.BadRequestError("Failed to load the log: "+err.Error(), nil)
	}

	return e.JSON(http.StatusOK, row.toItem(true))
}

// -------------------------------------------------------------------
// formatting and masking
// -------------------------------------------------------------------

// sensitive values in urls (e.g. the websocket `?authorization=<jwt>`)
var sensitiveQueryRegex = regexp.MustCompile(`(?i)((?:authorization|access_token|refresh_token|token|api[-_]?key|password)=)[^&\s]+`)

var sensitiveHeaders = map[string]bool{
	"authorization": true, "proxy-authorization": true, "api-key": true, "x-api-key": true, "cookie": true, "set-cookie": true,
}

var sensitiveBodyKeyRegex = regexp.MustCompile(`(?i)password|secret|token`)

var responsePrefixRegex = regexp.MustCompile(`^\[[\d.]+ ms\]\w+::\d+, ?`)

var jwtRegex = regexp.MustCompile(`eyJ[\w-]+\.(eyJ[\w-]+)\.[\w-]+`)

// websocket log paths: "WS-USER_REQUEST_u_<user uuid>_c_<chat uuid>"
var wsUserRegex = regexp.MustCompile(`_u_([0-9a-fA-F-]{36})`)

// requestTailMarkers are the "key:value" parts after the headers json, in their logged order.
var requestTailMarkers = []string{" method:", " body:", " queries:", " url:", " ip:", " RequestId:"}

func maskValue(v string) string {
	if len(v) <= 12 {
		return "***"
	}
	return v[:12] + "***"
}

func maskURL(u string) string {
	return sensitiveQueryRegex.ReplaceAllString(u, "${1}***")
}

func (row logRow) toItem(full bool) logItem {
	item := logItem{
		ID:      strconv.FormatInt(row.ID, 10),
		Created: row.Created,
		Level:   row.Level,
		Message: maskURL(row.Message),
		Data:    map[string]any{},
	}

	if err := json.Unmarshal([]byte(row.Data), &item.Data); err != nil {
		return item
	}

	// the authenticated user from the jwt "key" claim (searched before the masking)
	raw, _ := item.Data["request"].(string)
	if userID := jwtUserID(raw + " " + row.Message); userID != "" {
		item.Data["authId"] = userID
	} else if m := wsUserRegex.FindStringSubmatch(row.Message); m != nil {
		item.Data["authId"] = m[1]
	}

	if url, ok := item.Data["url"].(string); ok {
		item.Data["url"] = maskURL(url)
	}

	if raw != "" {
		request := parseRequest(raw)
		if headers, ok := request["headers"].(map[string]string); ok {
			if ua := headers["User-Agent"]; ua != "" {
				item.Data["userAgent"] = ua
			}
		}
		item.Data["request"] = request
	}

	if resp, ok := item.Data["response"].(string); ok {
		body, message := splitResponse(resp)
		item.Data["response"] = formatResponseBody(body, full)
		if message != "" && row.Level > 0 {
			item.Data["error"] = message
		}
	}

	for k, v := range item.Data {
		if v == nil {
			delete(item.Data, k)
		}
	}

	return item
}

func jwtUserID(str string) string {
	m := jwtRegex.FindStringSubmatch(str)
	if m == nil {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(m[1])
	if err != nil {
		return ""
	}

	claims := struct {
		Key string `json:"key"`
	}{}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Key
}

func parseRequest(raw string) map[string]any {
	rest, ok := strings.CutPrefix(raw, "Request_header:")
	if !ok {
		// websocket message
		return map[string]any{"body": parseBody(raw)}
	}

	request := map[string]any{}

	decoder := json.NewDecoder(strings.NewReader(rest))
	rawHeaders := map[string][]string{}
	if err := decoder.Decode(&rawHeaders); err != nil {
		request["raw"] = raw
		return request
	}

	headers := make(map[string]string, len(rawHeaders))
	for k, v := range rawHeaders {
		value := strings.Join(v, ", ")
		if sensitiveHeaders[strings.ToLower(k)] {
			value = maskValue(value)
		}
		headers[k] = value
	}
	request["headers"] = headers

	// split the tail from the right since the body may contain anything
	tail := rest[decoder.InputOffset():]
	end := len(tail)
	for i := len(requestTailMarkers) - 1; i >= 0; i-- {
		marker := requestTailMarkers[i]
		idx := strings.LastIndex(tail[:end], marker)
		if idx < 0 {
			continue
		}

		value := strings.TrimSpace(tail[idx+len(marker) : end])
		end = idx

		switch marker {
		case " body:", " queries:":
			request[strings.Trim(marker, " :")] = parseBody(value)
		case " url:":
			request["url"] = maskURL(value)
		}
	}

	return request
}

// parseBody decodes a json body (masking the sensitive top level keys) or returns it as it is.
func parseBody(str string) any {
	var body any
	if json.Unmarshal([]byte(str), &body) != nil {
		return str
	}

	if m, ok := body.(map[string]any); ok {
		for k := range m {
			if sensitiveBodyKeyRegex.MatchString(k) {
				m[k] = "***"
			}
		}
	}

	return body
}

// splitResponse splits "[3 ms]Ok::200, <body>, message: <message>" into its body and message.
func splitResponse(resp string) (string, string) {
	body := responsePrefixRegex.ReplaceAllString(resp, "")

	idx := strings.LastIndex(body, ", message: ")
	if idx < 0 {
		return body, ""
	}
	return body[:idx], strings.TrimSpace(body[idx+len(", message: "):])
}

func formatResponseBody(body string, full bool) any {
	if body == `""` {
		return ""
	}

	if !full {
		if len(body) > logsListResponseLimit {
			return strings.ToValidUTF8(body[:logsListResponseLimit], "") + "..."
		}
		return body
	}

	var decoded any
	if json.Unmarshal([]byte(body), &decoded) == nil {
		return decoded
	}
	return body
}
