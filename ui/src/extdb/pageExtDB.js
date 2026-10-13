const PER_PAGE = 30;

// "column op value" quick filter typed in the searchbar, e.g. `uuid = abc`, `id >= 10`, `note ~ test`, `uuid like '%abc%'`
const FILTER_REGEX = /^\s*([a-zA-Z0-9_]+)\s*(!=|<>|>=|<=|!~|==|=|>|<|~)\s*(.*)$/;
const LIKE_FILTER_REGEX = /^\s*([a-zA-Z0-9_]+)\s+(not\s+)?like\s+(.*)$/i;
const NULL_FILTER_REGEX = /^\s*([a-zA-Z0-9_]+)\s+is\s+(not\s+)?null\s*$/i;

// strips a single pair of wrapping quotes/backticks ('abc', "abc", `abc`)
function unquote(str) {
    str = str.trim();
    if (str.length >= 2 && `'"\``.includes(str[0]) && str[str.length - 1] == str[0]) {
        return str.slice(1, -1);
    }
    return str;
}

const OP_ALIASES = { "==": "=", "<>": "!=" };

export function pageExtDB(route) {
    app.store.title = "External DB";

    const requestKey = "extdb_" + app.utils.randomString();

    const data = store({
        databases: [],
        hasHook: false,
        tables: [],
        db: route.query.db?.[0] || "",
        table: route.query.table?.[0] || "",
        search: route.query.search?.[0] || "",
        sort: "",
        page: 1,
        tableSearch: "",
        result: { items: [], totalItems: 0, totalPages: 0 },
        isLoadingDatabases: false,
        isLoadingTables: false,
        isLoadingRecords: false,
        errorMsg: "",
        get activeTable() {
            return data.tables.find((t) => t.name == data.table) || null;
        },
        get filteredTables() {
            const q = data.tableSearch.trim().toLowerCase();
            return q ? data.tables.filter((t) => t.name.toLowerCase().includes(q)) : data.tables;
        },
        get activeDatabase() {
            return data.databases.find((d) => d.name == data.db) || null;
        },
    });

    function extdbUrl(path) {
        return "/api/extdb" + path;
    }

    async function loadDatabases() {
        data.isLoadingDatabases = true;
        try {
            const res = await app.pb.send(extdbUrl("/databases"), { requestKey: requestKey + "dbs" });
            data.databases = res.databases || [];
            data.hasHook = !!res.hook;
            if (!data.db || !data.activeDatabase) {
                data.db = data.databases[0]?.name || "";
            }
        } catch (err) {
            app.checkApiError(err);
        }
        data.isLoadingDatabases = false;
    }

    async function loadTables(refresh = false) {
        if (!data.db) {
            data.tables = [];
            return;
        }

        data.isLoadingTables = true;
        try {
            data.tables = await app.pb.send(
                extdbUrl(`/databases/${encodeURIComponent(data.db)}/tables`),
                { query: refresh ? { refresh: 1 } : {}, requestKey: requestKey + "tables" },
            );
            if (data.table && !data.activeTable) {
                data.table = "";
            }
        } catch (err) {
            app.checkApiError(err);
        }
        data.isLoadingTables = false;
    }

    function parseSearch(search) {
        search = unquote(search);

        const hasColumn = (name) => data.activeTable?.columns?.some((c) => c.name == name);

        const nullMatch = search.match(NULL_FILTER_REGEX);
        if (nullMatch && hasColumn(nullMatch[1])) {
            return { filters: [{ column: nullMatch[1], op: nullMatch[2] ? "notnull" : "isnull", value: "" }] };
        }

        const likeMatch = search.match(LIKE_FILTER_REGEX);
        if (likeMatch && hasColumn(likeMatch[1])) {
            return {
                filters: [{
                    column: likeMatch[1],
                    op: likeMatch[2] ? "notlike" : "like",
                    value: unquote(likeMatch[3]),
                }],
            };
        }

        const match = search.match(FILTER_REGEX);
        if (match && hasColumn(match[1])) {
            return { filters: [{ column: match[1], op: OP_ALIASES[match[2]] || match[2], value: unquote(match[3]) }] };
        }

        return { search };
    }

    async function loadRecords() {
        if (!data.db || !data.activeTable) {
            data.result = { items: [], totalItems: 0, totalPages: 0 };
            return;
        }

        data.isLoadingRecords = true;
        data.errorMsg = "";

        const parsed = parseSearch(data.search);
        const query = {
            page: data.page,
            perPage: PER_PAGE,
            sort: data.sort,
        };
        if (parsed.search) {
            query.search = parsed.search;
        }
        if (parsed.filters) {
            query.filters = JSON.stringify(parsed.filters);
        }

        try {
            data.result = await app.pb.send(
                extdbUrl(
                    `/databases/${encodeURIComponent(data.db)}/tables/${encodeURIComponent(data.table)}/records`,
                ),
                { query, requestKey: requestKey + "records" },
            );
            data.isLoadingRecords = false;
        } catch (err) {
            if (!err?.isAbort) {
                data.isLoadingRecords = false;
                data.result = { items: [], totalItems: 0, totalPages: 0 };
                data.errorMsg = err?.response?.message || err?.message || "Failed to load records.";
            }
        }
    }

    function selectTable(name) {
        data.table = name;
        data.page = 1;
        data.sort = "";
        data.search = "";
    }

    function toggleSort(column) {
        if (data.sort == column) {
            data.sort = "-" + column;
        } else if (data.sort == "-" + column) {
            data.sort = "";
        } else {
            data.sort = column;
        }
        data.page = 1;
    }

    function openRecord(item) {
        app.modals.openExtDBRecord({
            db: data.db,
            table: data.activeTable,
            record: item,
            hasHook: data.hasHook,
            onsave: () => loadRecords(),
            ondelete: () => loadRecords(),
        });
    }

    function formatCell(value) {
        if (value === null || typeof value == "undefined") {
            return t.span({ className: "txt-hint" }, "NULL");
        }
        return app.utils.truncate(String(value), 150);
    }

    const watchers = [];

    return t.div(
        {
            pbEvent: "pageExtDB",
            className: "page page-extdb",
            onmount: async () => {
                await loadDatabases();

                watchers.push(
                    watch(() => data.db, async () => {
                        await loadTables();
                    }),
                    watch(
                        () => [data.db, data.table, data.page, data.sort, data.search, data.tables.length].join("|"),
                        () => loadRecords(),
                    ),
                    watch(() => {
                        app.utils.replaceHashQueryParams({
                            db: data.db,
                            table: data.table,
                            search: data.search,
                        });
                    }),
                );
            },
            onunmount: () => {
                app.pb.cancelRequest(requestKey + "dbs");
                app.pb.cancelRequest(requestKey + "tables");
                app.pb.cancelRequest(requestKey + "records");
                watchers.forEach((w) => w?.unwatch());
            },
        },
        // sidebar: database picker + tables
        app.components.pageSidebar(
            { className: "extdb-sidebar", widthHistoryKey: "pbExtDBSidebarWidth" },
            t.div(
                { className: "sidebar-search" },
                t.div(
                    { className: "fields" },
                    t.div(
                        { className: "field" },
                        () => {
                            // (re)create the select once the options are loaded so the preselected db is matched
                            if (!data.databases.length) {
                                return t.input({
                                    type: "text",
                                    disabled: true,
                                    placeholder: data.isLoadingDatabases ? "Loading..." : "No databases",
                                });
                            }

                            return app.components.select({
                                id: "extdb_db_select",
                                name: "db",
                                required: true,
                                placeholder: "Select database",
                                options: data.databases.map((d) => ({
                                    value: d.name,
                                    label: d.name + (d.readonly ? " (read-only)" : ""),
                                })),
                                value: () => data.db,
                                onchange: (selected) => {
                                    const name = selected?.[0]?.value || "";
                                    if (name != data.db) {
                                        data.db = name;
                                        selectTable("");
                                    }
                                },
                            });
                        },
                    ),
                ),
            ),
            t.div(
                { className: "sidebar-search" },
                t.div(
                    { className: "fields" },
                    t.div(
                        { className: "field" },
                        t.input({
                            type: "text",
                            placeholder: "Search tables...",
                            value: () => data.tableSearch,
                            oninput: (e) => data.tableSearch = e.target.value,
                        }),
                    ),
                    t.div(
                        { className: "field addon p-l-0 p-r-5 gap-0" },
                        t.button(
                            {
                                type: "button",
                                className: () =>
                                    `btn sm circle transparent secondary link-faded ${
                                        data.isLoadingTables ? "loading" : ""
                                    }`,
                                disabled: () => data.isLoadingTables,
                                ariaDescription: app.attrs.tooltip("Reload schema", "left"),
                                onclick: () => loadTables(true),
                            },
                            t.i({ className: "ri-refresh-line", ariaHidden: true }),
                        ),
                    ),
                ),
            ),
            t.nav(
                { className: "sidebar-content scrollable" },
                () => {
                    if (data.isLoadingTables && !data.tables.length) {
                        return t.div({ className: "txt-center p-sm" }, t.span({ className: "loader sm" }));
                    }

                    if (!data.filteredTables.length) {
                        return t.div({ className: "txt-center txt-hint p-sm" }, "No tables found.");
                    }

                    return t.details(
                        { className: "nav-group", open: true },
                        t.summary(
                            { tabIndex: -1, onfocusout: () => false, onclick: () => false, onkeyup: () => false },
                            () => `Tables (${data.filteredTables.length})`,
                        ),
                        data.filteredTables.map((table) => {
                            return t.a(
                                {
                                    href: "javascript:void(0)",
                                    className: () => `nav-item ${data.table == table.name ? "active" : ""}`,
                                    title: table.name,
                                    onclick: (e) => {
                                        e.preventDefault();
                                        selectTable(table.name);
                                    },
                                },
                                t.i({
                                    className: table.type == "view" ? "ri-eye-line" : "ri-table-line",
                                    ariaHidden: true,
                                }),
                                t.span({ className: "txt txt-ellipsis" }, table.name),
                                () => {
                                    if (table.editable || table.deletable) {
                                        return t.i({
                                            className: "ri-edit-2-line extdb-editable-icon",
                                            ariaDescription: app.attrs.tooltip("Editable", "left"),
                                        });
                                    }
                                },
                            );
                        }),
                    );
                },
            ),
        ),
        t.div(
            { className: "page-content full-height" },
            t.header(
                { className: "page-header" },
                t.nav(
                    { className: "breadcrumbs" },
                    t.div({ className: "breadcrumb-item" }, "External DB"),
                    t.div({ hidden: () => !data.db, className: "breadcrumb-item" }, () => data.db),
                    t.div({ hidden: () => !data.table, className: "breadcrumb-item" }, () => data.table),
                ),
                t.div(
                    { className: "inline-flex gap-sm" },
                    app.components.refreshButton({
                        onclick: () => loadRecords(),
                    }),
                ),
                app.components.searchbar({
                    className: "extdb-searchbar",
                    historyKey: "pbExtDBSearchHistory",
                    placeholder:
                        "Search term or filter like `uuid = abc`, `id >= 10`, `uuid like '%abc%'`, `note is null`",
                    disabled: () => !data.activeTable,
                    value: () => data.search,
                    onsubmit: (val) => {
                        data.search = val || "";
                        data.page = 1;
                    },
                }),
                t.div(
                    { className: "meta m-l-auto inline-flex gap-sm" },
                    () => {
                        const table = data.activeTable;
                        if (!table) {
                            return;
                        }
                        const badges = [];
                        if (data.activeDatabase?.readonly) {
                            badges.push(t.span({ className: "label" }, "read-only db"));
                        }
                        badges.push(
                            t.span(
                                { className: `label ${table.editable ? "success" : ""}` },
                                table.editable ? "editable" : "not editable",
                            ),
                        );
                        if (table.deletable) {
                            badges.push(t.span({ className: "label danger" }, "deletable"));
                        }
                        if (!data.hasHook) {
                            badges.push(t.span({ className: "label warning" }, "no hook configured"));
                        }
                        return badges;
                    },
                ),
            ),
            t.div(
                { hidden: () => !data.errorMsg, className: "alert danger m-b-sm" },
                t.pre(null, () => data.errorMsg),
            ),
            () => {
                if (!data.databases.length && !data.isLoadingDatabases) {
                    return t.div(
                        { className: "block txt-center txt-hint p-base" },
                        "No external databases configured. Start PocketBase with --extdb=path/to/extdb.json.",
                    );
                }
                if (!data.activeTable) {
                    return t.div({ className: "block txt-center txt-hint p-base" }, "Select a table from the sidebar.");
                }
            },
            t.div(
                {
                    hidden: () => !data.activeTable,
                    className: () => `page-table-wrapper ${data.isLoadingRecords ? "faded" : ""}`,
                },
                t.table(
                    { className: "extdb-table responsive-table optimize" },
                    t.thead(
                        { className: "sticky" },
                        t.tr(null, () => {
                            return data.activeTable?.columns?.map((col) => {
                                return t.th(
                                    {
                                        className: () => {
                                            let classes = "sort-handle";
                                            if (data.sort == col.name) {
                                                classes += " asc";
                                            } else if (data.sort == "-" + col.name) {
                                                classes += " desc";
                                            }
                                            return classes;
                                        },
                                        title: col.type,
                                        onclick: () => toggleSort(col.name),
                                    },
                                    () => col.pk ? t.i({ className: "ri-key-2-line", ariaHidden: true }) : null,
                                    " " + col.name,
                                );
                            });
                        }),
                    ),
                    t.tbody(
                        null,
                        () => {
                            const cols = data.activeTable?.columns || [];

                            if (!data.result.items?.length) {
                                return t.tr(
                                    null,
                                    t.td(
                                        { colSpan: cols.length || 1, className: "txt-center" },
                                        t.span(
                                            { className: "txt-hint" },
                                            data.isLoadingRecords ? "Loading..." : "No records found.",
                                        ),
                                    ),
                                );
                            }

                            return data.result.items.map((item) => {
                                return t.tr(
                                    {
                                        className: "row-handle",
                                        tabIndex: 0,
                                        onclick: () => openRecord(item),
                                        onkeydown: (e) => {
                                            if (e.key == "Enter") {
                                                openRecord(item);
                                            }
                                        },
                                    },
                                    cols.map((col) => t.td({ "html-data-name": col.name }, formatCell(item[col.name]))),
                                );
                            });
                        },
                    ),
                ),
            ),
            t.footer(
                { className: "page-footer" },
                t.span(
                    { hidden: () => !data.activeTable, className: "txt" },
                    "Total: ",
                    () => data.result.totalItems || 0,
                ),
                t.div(
                    { hidden: () => !data.activeTable || data.result.totalPages <= 1, className: "inline-flex gap-sm" },
                    t.button(
                        {
                            type: "button",
                            className: "btn sm transparent secondary",
                            disabled: () => data.page <= 1 || data.isLoadingRecords,
                            onclick: () => data.page--,
                        },
                        t.i({ className: "ri-arrow-left-s-line", ariaHidden: true }),
                    ),
                    t.span({ className: "txt" }, () => `Page ${data.page} / ${data.result.totalPages || 1}`),
                    t.button(
                        {
                            type: "button",
                            className: "btn sm transparent secondary",
                            disabled: () => data.page >= data.result.totalPages || data.isLoadingRecords,
                            onclick: () => data.page++,
                        },
                        t.i({ className: "ri-arrow-right-s-line", ariaHidden: true }),
                    ),
                ),
                app.components.credits(),
            ),
        ),
    );
}
