import { loadLogsSources } from "@/extdb/extdbLogsApi";
import { logsChart } from "./logsChart";
import { logsList } from "./logsList";

export function pageLogs(route) {
    app.store.title = "Logs";

    const LOG_QUERY_KEY = "logId";
    const FILTER_QUERY_KEY = "filter";
    const SUPERUSER_REQUESTS_QUERY_KEY = "superuserRequests";
    const SUPERUSER_REQUESTS_STORAGE_KEY = "pbLogSuperuserRequests";
    const SOURCE_QUERY_KEY = "source";

    const withoutSuperusersPresets = "data.auth!='_superusers'";

    const queryLogId = route.query[LOG_QUERY_KEY]?.[0] || "";

    const queryFilter = route.query[FILTER_QUERY_KEY]?.[0] || "";

    // extdb logs source name (empty for the PocketBase logs)
    const querySource = route.query[SOURCE_QUERY_KEY]?.[0] || "";

    const querySuperuserRequests = !!(route.query[SUPERUSER_REQUESTS_QUERY_KEY]?.[0] << 0)
        || !!(window.localStorage.getItem(SUPERUSER_REQUESTS_STORAGE_KEY) << 0);

    const logsSettings = store({
        reset: null,
        isChartLoading: false,
        isListLoading: false,
        isFirstLoadReady: false, // used by the chart to reveal itself while the stats are still loading to minimize layout jumps
        hasListItems: false,
        zoom: {}, // only unidirectional from within the chart
        source: querySource,
        sources: [], // extdb logs sources
        presets: querySuperuserRequests || querySource ? [] : [withoutSuperusersPresets],
        filter: queryFilter,
        totalFound: null,
        activeLogIdOrModel: queryLogId,
        get hasIncludeRequestsBySuperusers() {
            return !logsSettings.presets.includes(withoutSuperusersPresets);
        },
        get isLoading() {
            return logsSettings.isListLoading || logsSettings.isChartLoading;
        },
    });

    function getLogId(logIdOrModel) {
        if (!logIdOrModel) {
            return null;
        }

        return typeof logIdOrModel === "string" ? logIdOrModel : logIdOrModel?.id;
    }

    function refreshLogsList() {
        logsSettings.reset = Date.now();
    }

    function changeSource(source) {
        if (logsSettings.source == source) {
            return;
        }

        const includeSuperusers = !!(window.localStorage.getItem(SUPERUSER_REQUESTS_STORAGE_KEY) << 0);

        logsSettings.source = source;
        logsSettings.presets = source || includeSuperusers ? [] : [withoutSuperusersPresets];
        logsSettings.filter = "";
        logsSettings.zoom = {};
        logsSettings.totalFound = null;
        refreshLogsList();
    }

    loadLogsSources().then((sources) => {
        logsSettings.sources = sources || [];
    });

    const watchers = [];

    return [
        t.div(
            { pbEvent: "logsChartContainer", className: "logs-chart-container accent-surface" },
            logsChart(logsSettings),
        ),
        t.div(
            {
                pbEvent: "pageLogs",
                className: "page page-logs",
                onmount(el) {
                    watchers.push(
                        watch(() => {
                            app.utils.replaceHashQueryParams({
                                [FILTER_QUERY_KEY]: logsSettings.filter,
                            });
                        }),
                        watch(() => {
                            app.utils.replaceHashQueryParams({
                                [SOURCE_QUERY_KEY]: logsSettings.source,
                            });
                        }),
                        watch(() => {
                            if (logsSettings.source) {
                                return; // the superusers preset is only for the PocketBase logs
                            }

                            const superuserRequests = logsSettings.hasIncludeRequestsBySuperusers ? 1 : 0;

                            app.utils.replaceHashQueryParams({
                                [SUPERUSER_REQUESTS_QUERY_KEY]: superuserRequests,
                            });

                            window.localStorage.setItem(SUPERUSER_REQUESTS_STORAGE_KEY, superuserRequests);
                        }),
                        watch(() => logsSettings.activeLogIdOrModel, () => {
                            app.utils.replaceHashQueryParams({
                                [LOG_QUERY_KEY]: getLogId(logsSettings.activeLogIdOrModel),
                            });

                            if (logsSettings.activeLogIdOrModel) {
                                // force close any previous modal
                                app.modals.close(null, true);

                                app.modals.openLogPreview(logsSettings.activeLogIdOrModel, {
                                    source: logsSettings.source,
                                    onafterclose: () => {
                                        logsSettings.activeLogIdOrModel = null;
                                    },
                                });
                            }
                        }),
                    );
                },
                onunmount(el) {
                    clearTimeout(el._chartTiemoutId);
                    watchers.forEach((w) => w?.unwatch());
                },
            },
            t.div(
                { className: "page-content full-height" },
                t.header(
                    { className: "page-header" },
                    t.nav({ className: "breadcrumbs" }, t.div(null, "Logs")),
                    () => {
                        if (!logsSettings.sources.length) {
                            return;
                        }

                        const sources = [{ name: "", label: "PocketBase" }].concat(logsSettings.sources);

                        return t.div(
                            { className: "inline-flex gap-5 logs-sources" },
                            sources.map((s) =>
                                t.button(
                                    {
                                        type: "button",
                                        className: () =>
                                            `btn sm pill ${
                                                logsSettings.source == s.name ? "" : "secondary transparent"
                                            }`,
                                        onclick: () => changeSource(s.name),
                                    },
                                    t.span({ className: "txt" }, s.label),
                                )
                            ),
                        );
                    },
                    t.div(
                        { className: "inline-flex gap-sm" },
                        t.button(
                            {
                                hidden: () => !!logsSettings.source,
                                className: "btn circle transparent secondary tooltip-right",
                                ariaLabel: app.attrs.tooltip("Logs settings"),
                                onclick: () =>
                                    app.modals.openLogsSettings({
                                        onsave: () => refreshLogsList(),
                                        ondelete: () => refreshLogsList(),
                                    }),
                            },
                            t.i({ className: "ri-settings-3-line", ariaHidden: true }),
                        ),
                        app.components.refreshButton({
                            onclick: refreshLogsList,
                        }),
                    ),
                    app.components.searchbar({
                        className: "logs-searchbar",
                        historyKey: "pbLogsSearchHistory",
                        placeholder: () =>
                            logsSettings.source
                                ? "Search term or filter like `data.status >= 400`, `data.execTime > 1000`, `data.method = 'WS'`"
                                : "Search term or filter like `level > 0`",
                        value: () => logsSettings.filter || "",
                        onsubmit: (val) => logsSettings.filter = val,
                        autocomplete: [
                            "id",
                            "level",
                            "message",
                            "created",
                            "data.status",
                            "data.method",
                            "data.url",
                            "data.execTime",
                            "data.userIP",
                            { value: "data.", label: "data.*" },
                        ],
                    }),
                    t.div(
                        { className: "meta m-l-auto" },
                        t.div(
                            {
                                hidden: () => !!logsSettings.source,
                                className: "field logs-include-superuser-requests",
                            },
                            t.input({
                                type: "checkbox",
                                id: "logs_checkbox",
                                className: "switch sm",
                                checked: () => logsSettings.hasIncludeRequestsBySuperusers,
                                onchange: (e) => {
                                    if (e.target.checked) {
                                        app.utils.removeByValue(logsSettings.presets, withoutSuperusersPresets);
                                    } else {
                                        app.utils.pushUnique(logsSettings.presets, withoutSuperusersPresets);
                                    }
                                },
                            }),
                            t.label(
                                { htmlFor: "logs_checkbox" },
                                t.small({ className: "txt" }, "Include requests by superusers"),
                            ),
                        ),
                    ),
                ),
                logsList(logsSettings),
                t.footer(
                    { className: "page-footer" },
                    t.span(
                        { className: "txt total-logs" },
                        "Total: ",
                        () => {
                            if (logsSettings.totalFound == null) {
                                return "...";
                            }
                            return logsSettings.totalFound;
                        },
                    ),
                    app.components.credits(),
                ),
            ),
        ),
    ];
}
