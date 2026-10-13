// Logs api for the Logs page: the PocketBase logs when `source` is empty,
// otherwise the external request logs of an extdb logs source (same item and stats shape).
export function logsApi(source) {
    if (!source) {
        return app.pb.logs;
    }

    const baseUrl = "/api/extdb/logs/" + encodeURIComponent(source);

    return {
        getList(page = 1, perPage = 50, options = {}) {
            const { filter, sort, skipTotal, ...rest } = options;
            return app.pb.send(baseUrl, {
                ...rest,
                query: { page, perPage, filter, sort, skipTotal, ...rest.query },
            });
        },
        getStats(options = {}) {
            const { filter, ...rest } = options;
            return app.pb.send(baseUrl + "/stats", {
                ...rest,
                query: { filter, ...rest.query },
            });
        },
        getOne(id, options = {}) {
            return app.pb.send(baseUrl + "/" + encodeURIComponent(id), options);
        },
    };
}

// Returns the configured extdb logs sources ([] if the extdb plugin is not registered).
export async function loadLogsSources() {
    try {
        return await app.pb.send("/api/extdb/logs", { requestKey: "extdb_logs_sources" });
    } catch (_) {
        return [];
    }
}
