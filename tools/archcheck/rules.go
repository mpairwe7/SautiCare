package main

// Allowed lists, for each bounded context under internal/, the other
// contexts it may import (spec §4.1, ADR-0002). A context not listed here is
// an error: adding one is a reviewed change. internal/platform is not a
// context — it holds adapters and may import any context.
var Allowed = map[string][]string{
	"pii":       {},
	"config":    {},
	"crypto":    {"pii"},
	"incident":  {"pii", "crypto"},
	"audit":     {"crypto"},
	"location":  {},
	"responder": {"pii", "crypto"},
	"dispatch":  {"responder", "location", "incident"},
	"respond":   {"config", "crypto", "dispatch", "intake", "location", "pii", "responder", "incident"},
	"triage":    {"crypto"},
	"intake":    {"incident", "pii", "config", "crypto", "location", "triage"},
}

// netImports may be used only by adapters, processes, tests and tools (R3).
var netImports = []string{
	"net", "net/http", "net/http/httptest", "net/http/httputil", "net/rpc", "net/smtp",
	"os/exec", "database/sql",
}

// netPrefixes are third-party I/O libraries with the same restriction.
var netPrefixes = []string{
	"github.com/jackc/pgx", "github.com/valkey-io/", "github.com/fergusstrange/embedded-postgres", "github.com/coder/websocket",
}

// ioLocations may import network and database packages.
var ioLocations = []string{"internal/platform/", "cmd/", "tests/", "tools/"}

// storeJoin is the pair of store adapters only the pseudonymiser may link (R4).
var storeJoin = [2]string{"internal/platform/pg/opstore", "internal/platform/pg/analyticstore"}

const pseudonymiser = "cmd/pseudonymiser"
