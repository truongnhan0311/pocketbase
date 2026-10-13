window.app = window.app || {};
window.app.modals = window.app.modals || {};

/**
 * Opens the external DB record view/edit modal.
 *
 * @param {Object} options
 * @param {string} options.db
 * @param {Object} options.table   Table metadata from /api/extdb/databases/{db}/tables.
 * @param {Object} options.record  Record item (with the `_pk` identifier).
 * @param {boolean} [options.hasHook]
 * @param {Function} [options.onsave]
 * @param {Function} [options.ondelete]
 */
window.app.modals.openExtDBRecord = function(options) {
    const modal = extdbRecordModal(options);

    document.body.appendChild(modal);

    app.modals.open(modal);
};

const LONG_VALUE_LENGTH = 80;

function extdbRecordModal({ db, table, record, hasHook = false, onsave, ondelete }) {
    const uniqueId = "extdb_record_" + app.utils.randomString();
    const recordUrl = `/api/extdb/databases/${encodeURIComponent(db)}/tables/${encodeURIComponent(table.name)}`
        + `/records/${encodeURIComponent(record._pk)}`;

    // editable copy: { [column]: { value: string, isNull: bool } }
    const initial = {};
    for (const col of table.columns) {
        const val = record[col.name];
        initial[col.name] = {
            value: val === null || typeof val == "undefined" ? "" : String(val),
            isNull: val === null || typeof val == "undefined",
        };
    }

    const data = store({
        fields: JSON.parse(JSON.stringify(initial)),
        isSaving: false,
        isDeleting: false,
        get changes() {
            const result = {};
            for (const col of table.columns) {
                if (col.pk) {
                    continue;
                }
                const field = data.fields[col.name];
                const orig = initial[col.name];
                if (field.isNull != orig.isNull || (!field.isNull && field.value != orig.value)) {
                    result[col.name] = field.isNull ? null : field.value;
                }
            }
            return result;
        },
        get hasChanges() {
            return Object.keys(data.changes).length > 0;
        },
    });

    function reportHook(hook) {
        if (!hasHook || !hook) {
            return;
        }
        if (hook.error) {
            app.toasts.error("Saved, but the main server hook failed: " + hook.error);
        }
    }

    async function save() {
        if (data.isSaving || !data.hasChanges || !table.editable) {
            return;
        }

        data.isSaving = true;
        try {
            const res = await app.pb.send(recordUrl, {
                method: "PATCH",
                body: data.changes,
            });

            app.toasts.success(`Successfully updated ${table.name} #${record._pk}.`);
            reportHook(res?.hook);

            onsave?.(res?.record);
            app.modals.close();
        } catch (err) {
            app.checkApiError(err);
        }
        data.isSaving = false;
    }

    function deleteWithConfirm() {
        if (!table.deletable || data.isDeleting) {
            return;
        }

        app.modals.confirm(
            t.div(
                { className: "txt-center" },
                t.h6(null, `Do you really want to delete ${table.name} #${record._pk}?`),
                t.p({ className: "txt-hint" }, "The record is removed directly from the external database file."),
            ),
            async () => {
                data.isDeleting = true;
                try {
                    const res = await app.pb.send(recordUrl, { method: "DELETE" });

                    app.toasts.success(`Successfully deleted ${table.name} #${record._pk}.`);
                    reportHook(res?.hook);

                    ondelete?.();
                    app.modals.close();
                } catch (err) {
                    app.checkApiError(err);
                }
                data.isDeleting = false;
            },
            null,
            { yesButton: "Delete", noButton: "Cancel" },
        );
    }

    function fieldInput(col) {
        const id = uniqueId + "." + col.name;
        const readonly = col.pk || !table.editable;
        const isLong = (initial[col.name].value || "").length > LONG_VALUE_LENGTH;

        const common = {
            id: id,
            name: col.name,
            readOnly: readonly,
            disabled: () => !readonly && data.fields[col.name].isNull,
            placeholder: () => data.fields[col.name].isNull ? "NULL" : "",
            value: () => data.fields[col.name].value,
            oninput: (e) => data.fields[col.name].value = e.target.value,
        };

        const field = t.div(
            { className: "field" },
            t.label(
                { htmlFor: id },
                () => col.pk ? t.i({ className: "ri-key-2-line", ariaHidden: true }) : null,
                t.span({ className: "txt" }, col.name),
                t.small({ className: "txt-hint m-l-5" }, col.type || "ANY"),
            ),
            isLong ? t.textarea({ ...common, rows: 4 }) : t.input({ ...common, type: "text" }),
        );

        return [
            field,
            () => {
                if (readonly || col.notNull) {
                    return;
                }

                return t.div(
                    { className: "field extdb-null-toggle" },
                    t.input({
                        type: "checkbox",
                        id: id + ".null",
                        className: "switch sm",
                        checked: () => data.fields[col.name].isNull,
                        onchange: (e) => data.fields[col.name].isNull = e.target.checked,
                    }),
                    t.label({ htmlFor: id + ".null" }, t.small({ className: "txt" }, "NULL")),
                );
            },
        ];
    }

    return t.div(
        {
            className: "modal popup lg extdb-record-modal",
            onafterclose: (el) => el?.remove(),
        },
        t.header(
            { className: "modal-header" },
            t.h5({ className: "m-auto" }, `${table.name} #${record._pk}`),
        ),
        t.form(
            {
                id: uniqueId,
                className: "modal-content",
                onsubmit: (e) => {
                    e.preventDefault();
                    save();
                },
            },
            () => {
                if (!table.editable) {
                    return t.div(
                        { className: "alert info m-b-sm" },
                        t.p(null, "This table is read-only. Allow it in the extdb config `editableTables` to edit."),
                    );
                }
                if (!hasHook) {
                    return t.div(
                        { className: "alert warning m-b-sm" },
                        t.p(null, "No main server hook is configured - caches on the main server won't be refreshed."),
                    );
                }
            },
            t.div(
                { className: "grid" },
                table.columns.map((col) => t.div({ className: "col-lg-12" }, fieldInput(col))),
            ),
        ),
        t.footer(
            { className: "modal-footer" },
            () => {
                if (!table.deletable) {
                    return;
                }
                return t.button(
                    {
                        type: "button",
                        className: () => `btn transparent danger ${data.isDeleting ? "loading" : ""}`,
                        disabled: () => data.isSaving || data.isDeleting,
                        onclick: deleteWithConfirm,
                    },
                    t.i({ className: "ri-delete-bin-7-line", ariaHidden: true }),
                    t.span({ className: "txt" }, "Delete"),
                );
            },
            t.button(
                {
                    type: "button",
                    className: "btn transparent m-l-auto",
                    disabled: () => data.isSaving || data.isDeleting,
                    onclick: () => app.modals.close(),
                },
                t.span({ className: "txt" }, "Close"),
            ),
            () => {
                if (!table.editable) {
                    return;
                }
                return t.button(
                    {
                        "html-form": uniqueId,
                        type: "submit",
                        className: () => `btn expanded ${data.isSaving ? "loading" : ""}`,
                        disabled: () => data.isSaving || data.isDeleting || !data.hasChanges,
                    },
                    t.span({ className: "txt" }, "Save changes"),
                );
            },
        ),
    );
}
