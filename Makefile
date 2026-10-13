lint:
	golangci-lint run -c ./golangci.yml ./...

test:
	go test ./... -v --cover

jstypes:
	go run ./plugins/jsvm/internal/types/types.go

test-report:
	go test ./... -v --cover -coverprofile=coverage.out
	go tool cover -html=coverage.out

# ---------------------------------------------------------------
# extdb (external SQLite databases) local dev
# ---------------------------------------------------------------
EXTDB_SRC    ?= ../familylink-api/db
EXTDB_DIR    ?= ./.extdb/dbs
EXTDB_CONFIG ?= ./plugins/extdb/extdb.dev.json
EXTDB_DBS     = familylinkdb familylinkdb_ai familylinkdb_token familylinkdb_messages familylinkdb_transactions familylinkdb_logs

# snapshots the source databases into EXTDB_DIR (the dev server never touches the live files)
extdb-copy:
	mkdir -p $(EXTDB_DIR)
	for f in $(EXTDB_DBS); do rm -f $(EXTDB_DIR)/$$f* && sqlite3 -readonly $(EXTDB_SRC)/$$f ".backup '$(EXTDB_DIR)/$$f'" || exit 1; done

# builds the dashboard UI (embedded in the binary) and the static release binaries into ./.builds/release
RELEASE_VERSION ?= $(shell git describe --tags --always)-fm
RELEASE_TARGETS ?= linux/amd64 linux/arm64

release:
	cd ui && npm run build
	rm -rf ./.builds/release && mkdir -p ./.builds/release
	for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "-s -w -X github.com/pocketbase/pocketbase.Version=$(RELEASE_VERSION)" \
			-o ./.builds/release/pocketbase_$${os}_$${arch} ./examples/base || exit 1; \
	done
	cp ./plugins/extdb/extdb.example.json ./.builds/release/
	ls -lh ./.builds/release

extdb-run:
	go build -o ./.extdb/pocketbase ./examples/base
	./.extdb/pocketbase serve --dir ./.extdb/pb_data --extdb $(EXTDB_CONFIG) --extdbDir $(EXTDB_DIR)
