package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/recurso-dev/recurso/internal/core/domain"
)

// Per-workspace database routing.
//
// The routed *sql.DB looks like any other *sql.DB to the repositories, but each
// connection it opens belongs to ONE database, chosen from the tenant on the
// query's context (domain.TenantIDKey, which the auth middleware stamps on every
// authenticated request). A tenant with no dedicated database — and any query
// whose context carries no tenant — uses the shared database, exactly as before.
//
// Two rules keep it safe:
//   - fail closed: if the route for a tenant cannot be determined, the query
//     fails. It is never sent to the shared database as a fallback, because that
//     would silently write a dedicated workspace's data into the wrong place.
//   - a pooled connection never serves a statement for a database other than the
//     one it is currently connected to: it re-points itself first (see routedConn).

// RouteFunc returns the dedicated database name for the tenant on ctx, or ""
// for the shared database. An error means "unknown": the query must not run.
type RouteFunc func(ctx context.Context) (string, error)

// TenantRouter resolves a tenant to its dedicated database, reading
// tenants.database_name from the SHARED database and caching the answer.
type TenantRouter struct {
	shared *sql.DB // always the unrouted handle: routing must not route itself
	ttl    time.Duration

	mu    sync.Mutex
	cache map[uuid.UUID]routeEntry
}

type routeEntry struct {
	name    string
	expires time.Time
}

// NewTenantRouter builds a router over the shared (unrouted) database handle.
func NewTenantRouter(shared *sql.DB, ttl time.Duration) *TenantRouter {
	return &TenantRouter{shared: shared, ttl: ttl, cache: map[uuid.UUID]routeEntry{}}
}

// Route implements RouteFunc.
func (r *TenantRouter) Route(ctx context.Context) (string, error) {
	tenantID, ok := ctx.Value(domain.TenantIDKey).(uuid.UUID)
	if !ok || tenantID == uuid.Nil {
		return "", nil // no tenant on the context: shared database
	}

	r.mu.Lock()
	if e, ok := r.cache[tenantID]; ok && time.Now().Before(e.expires) {
		r.mu.Unlock()
		return e.name, nil
	}
	r.mu.Unlock()

	// Detached context: the lookup must not be cut short by (or inherit the
	// routing of) the request context it is serving.
	lctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var name sql.NullString
	err := r.shared.QueryRowContext(lctx, `SELECT database_name FROM tenants WHERE id = $1`, tenantID).Scan(&name)
	switch {
	case err == sql.ErrNoRows:
		// Unknown tenant row (e.g. the dev-bypass tenant): shared database.
	case err != nil:
		return "", fmt.Errorf("resolve database for tenant %s: %w", tenantID, err)
	}

	r.mu.Lock()
	r.cache[tenantID] = routeEntry{name: name.String, expires: time.Now().Add(r.ttl)}
	r.mu.Unlock()
	return name.String, nil
}

// Forget drops a tenant's cached route (call after assigning it a database).
func (r *TenantRouter) Forget(tenantID uuid.UUID) {
	r.mu.Lock()
	delete(r.cache, tenantID)
	r.mu.Unlock()
}

// RoutedDBOptions sizes the routed handle. Zero values take the defaults.
type RoutedDBOptions struct {
	// MaxOpen caps *sql.DB connection slots (default 25): the concurrent queries
	// the process can have in flight across ALL databases.
	MaxOpen int
	// MaxCachedConns caps warm, idle server connections kept per process so a
	// slot can switch databases without dialing (default 50). Slots plus cached
	// connections is the most server connections this process can hold; keep
	// that sum, times the number of API instances, under Postgres
	// max_connections — or put PgBouncer in front.
	MaxCachedConns int
	// MaxCachedPerDB caps warm connections for one database (default 2), so one
	// busy workspace cannot crowd the cache.
	MaxCachedPerDB int
	// CachedConnIdle is how long a warm connection may sit unused (default 2m).
	CachedConnIdle time.Duration
	// OnFirstUse, when set, runs once per database per process before the first
	// connection to it is used (name is the database, dsn its connection URL).
	// main.go uses it to apply pending migrations, so a release that adds a
	// migration reaches every dedicated database without a fleet-wide job.
	// If it fails the query fails and the next attempt retries; the database is
	// never used half-migrated.
	OnFirstUse func(ctx context.Context, name, dsn string) error
}

func (o RoutedDBOptions) withDefaults() RoutedDBOptions {
	if o.MaxOpen <= 0 {
		o.MaxOpen = 25
	}
	if o.MaxCachedConns <= 0 {
		o.MaxCachedConns = 50
	}
	if o.MaxCachedPerDB <= 0 {
		o.MaxCachedPerDB = 2
	}
	if o.CachedConnIdle <= 0 {
		o.CachedConnIdle = 2 * time.Minute
	}
	return o
}

// NewRoutedDB opens a *sql.DB whose connections follow route. sharedDSN is the
// shared database; dedicated databases are reached by swapping the database name
// in the same DSN. Slots are shared across all databases; a bounded cache of
// warm connections (see RoutedDBOptions) makes switching between databases cheap.
func NewRoutedDB(sharedDSN string, route RouteFunc, opts ...RoutedDBOptions) (*sql.DB, error) {
	var o RoutedDBOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	o = o.withDefaults()

	shared, err := pq.NewConnector(sharedDSN)
	if err != nil {
		return nil, err
	}
	c := &routingConnector{
		sharedDSN: sharedDSN,
		route:     route,
		pools:     map[string]driver.Connector{"": shared},
		first:     map[string]*sync.Mutex{},
		opts:      o,
		idle:      map[string][]idleConn{},
	}
	d := sql.OpenDB(c)

	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.PingContext(pingCtx); err != nil {
		_ = d.Close()
		return nil, err
	}
	d.SetMaxOpenConns(o.MaxOpen)
	// Keep slots alive so the physical connections inside them stay warm.
	d.SetMaxIdleConns(o.MaxOpen)
	d.SetConnMaxLifetime(5 * time.Minute)
	return d, nil
}

type routingConnector struct {
	sharedDSN string
	route     RouteFunc

	mu    sync.Mutex
	pools map[string]driver.Connector // database name ("" = shared) -> connector
	first map[string]*sync.Mutex      // per-database lock around OnFirstUse

	opts      RoutedDBOptions
	idleMu    sync.Mutex
	idle      map[string][]idleConn // warm connections by database name
	idleTotal int
}

type idleConn struct {
	conn driver.Conn
	at   time.Time
}

// takeIdle returns a warm connection for database name, or nil.
func (c *routingConnector) takeIdle(name string) driver.Conn {
	c.idleMu.Lock()
	defer c.idleMu.Unlock()
	list := c.idle[name]
	for len(list) > 0 {
		ic := list[len(list)-1] // most recently used
		list = list[:len(list)-1]
		c.idleTotal--
		if time.Since(ic.at) > c.opts.CachedConnIdle {
			_ = ic.conn.Close()
			continue
		}
		c.idle[name] = list
		return ic.conn
	}
	delete(c.idle, name)
	return nil
}

// putIdle parks a healthy connection for later reuse, evicting the least
// recently used warm connection when the cache is full. It reports whether the
// connection was kept; if not, the caller closes it.
func (c *routingConnector) putIdle(name string, conn driver.Conn) bool {
	if v, ok := conn.(driver.Validator); ok && !v.IsValid() {
		return false
	}
	c.idleMu.Lock()
	defer c.idleMu.Unlock()
	if len(c.idle[name]) >= c.opts.MaxCachedPerDB {
		return false
	}
	for c.idleTotal >= c.opts.MaxCachedConns {
		if !c.evictOldestLocked() {
			return false
		}
	}
	c.idle[name] = append(c.idle[name], idleConn{conn: conn, at: time.Now()})
	c.idleTotal++
	return true
}

func (c *routingConnector) evictOldestLocked() bool {
	var oldestName string
	var oldest time.Time
	found := false
	for name, list := range c.idle {
		if len(list) == 0 {
			continue
		}
		if !found || list[0].at.Before(oldest) {
			oldestName, oldest, found = name, list[0].at, true
		}
	}
	if !found {
		return false
	}
	list := c.idle[oldestName]
	_ = list[0].conn.Close()
	c.idle[oldestName] = list[1:]
	if len(c.idle[oldestName]) == 0 {
		delete(c.idle, oldestName)
	}
	c.idleTotal--
	return true
}

func (c *routingConnector) Driver() driver.Driver { return &pq.Driver{} }

// connectorFor returns the connector for a database name, creating it on first
// use. The name came from tenants.database_name, but it is validated again here
// so a tampered row can never aim a connection at an arbitrary database.
func (c *routingConnector) connectorFor(name string) (driver.Connector, error) {
	c.mu.Lock()
	if conn, ok := c.pools[name]; ok {
		c.mu.Unlock()
		return conn, nil
	}
	if !strings.HasPrefix(name, TenantDatabasePrefix) {
		c.mu.Unlock()
		return nil, fmt.Errorf("refusing to route to %q: not a dedicated tenant database", name)
	}
	if _, err := TenantDatabaseName(strings.TrimPrefix(name, TenantDatabasePrefix)); err != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("refusing to route to %q: %w", name, err)
	}
	dsn, err := TenantDatabaseDSN(c.sharedDSN, name)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	nameMu := c.first[name]
	if nameMu == nil {
		nameMu = &sync.Mutex{}
		c.first[name] = nameMu
	}
	c.mu.Unlock()

	// Serialise first use per database, without blocking other databases.
	nameMu.Lock()
	defer nameMu.Unlock()
	c.mu.Lock()
	if conn, ok := c.pools[name]; ok { // another caller finished while we waited
		c.mu.Unlock()
		return conn, nil
	}
	c.mu.Unlock()

	if c.opts.OnFirstUse != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := c.opts.OnFirstUse(ctx, name, dsn); err != nil {
			return nil, fmt.Errorf("prepare %s: %w", name, err)
		}
	}
	conn, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.pools[name] = conn
	c.mu.Unlock()
	return conn, nil
}

func (c *routingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	r := &routedConn{c: c}
	if err := r.ensure(ctx); err != nil {
		return nil, err // fail closed
	}
	return r, nil
}

// routedConn is a pool slot holding one physical pq connection to ONE database.
// database/sql hands pooled slots to whichever caller is next, with no regard
// for which database that caller needs (under load a waiting caller receives
// whatever slot was just freed). So the slot checks the route of EVERY statement
// and, when it differs, closes its physical connection and opens one to the
// right database before running anything. That makes correctness independent of
// how database/sql schedules the pool; the cost of a mismatch is a reconnect.
//
// Like slowLogConn it forwards every optional driver interface pq implements so
// database/sql keeps its fast paths.
type routedConn struct {
	c        *routingConnector
	conn     driver.Conn
	database string
	inTx     bool
}

var (
	_ driver.QueryerContext     = (*routedConn)(nil)
	_ driver.ExecerContext      = (*routedConn)(nil)
	_ driver.ConnPrepareContext = (*routedConn)(nil)
	_ driver.ConnBeginTx        = (*routedConn)(nil)
	_ driver.Pinger             = (*routedConn)(nil)
	_ driver.SessionResetter    = (*routedConn)(nil)
	_ driver.Validator          = (*routedConn)(nil)
	_ driver.NamedValueChecker  = (*routedConn)(nil)
)

// ensure points the slot at the database ctx routes to. It never switches while
// a transaction is open: a transaction lives on one physical connection, and a
// statement for another database inside it is a bug to surface, not to paper
// over.
func (r *routedConn) ensure(ctx context.Context) error {
	want, err := r.c.route(ctx)
	if err != nil {
		return err
	}
	if r.conn != nil && want == r.database {
		return nil
	}
	if r.inTx {
		return fmt.Errorf("db routing: statement for database %q inside a transaction on %q", want, r.database)
	}
	base, err := r.c.connectorFor(want)
	if err != nil {
		return err
	}
	// Park the old connection in the warm cache (or close it if the cache is
	// full) BEFORE acquiring the next one, so a slot never holds two server
	// connections at once. A failed dial leaves the slot empty; the next
	// statement simply tries again.
	if r.conn != nil {
		if !r.c.putIdle(r.database, r.conn) {
			_ = r.conn.Close()
		}
		r.conn, r.database = nil, ""
	}
	if warm := r.c.takeIdle(want); warm != nil {
		// A parked connection may have gone stale or hold leftover session
		// state: reset it, and fall back to dialing if it is no good.
		if rs, ok := warm.(driver.SessionResetter); !ok || rs.ResetSession(ctx) == nil {
			r.conn, r.database = warm, want
			return nil
		}
		_ = warm.Close()
	}
	next, err := base.Connect(ctx)
	if err != nil {
		return err
	}
	r.conn, r.database = next, want
	return nil
}

func (r *routedConn) Close() error {
	if r.conn == nil {
		return nil
	}
	return r.conn.Close()
}

// Prepare / Begin are the legacy, context-free entry points. database/sql uses
// the *Context variants below because routedConn implements them; if these are
// ever called they stay on the current database rather than guess a route.
func (r *routedConn) Prepare(query string) (driver.Stmt, error) {
	if r.conn == nil {
		return nil, driver.ErrBadConn
	}
	return r.conn.Prepare(query)
}

//nolint:staticcheck // legacy driver.Conn method required by the interface
func (r *routedConn) Begin() (driver.Tx, error) {
	return r.BeginTx(context.Background(), driver.TxOptions{})
}

func (r *routedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	return r.conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (r *routedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	return r.conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (r *routedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	return r.conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (r *routedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := r.ensure(ctx); err != nil {
		return nil, err
	}
	tx, err := r.conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	r.inTx = true
	return &routedTx{Tx: tx, r: r}, nil
}

// routedTx clears the slot's in-transaction flag when the transaction ends.
type routedTx struct {
	driver.Tx
	r *routedConn
}

func (t *routedTx) Commit() error   { defer func() { t.r.inTx = false }(); return t.Tx.Commit() }
func (t *routedTx) Rollback() error { defer func() { t.r.inTx = false }(); return t.Tx.Rollback() }

// Ping is a health check with no tenant; it must not re-point the slot.
func (r *routedConn) Ping(ctx context.Context) error {
	if r.conn == nil {
		return driver.ErrBadConn
	}
	return r.conn.(driver.Pinger).Ping(ctx)
}

func (r *routedConn) IsValid() bool {
	return r.conn != nil && r.conn.(driver.Validator).IsValid()
}

func (r *routedConn) CheckNamedValue(nv *driver.NamedValue) error {
	if r.conn == nil {
		return driver.ErrBadConn
	}
	return r.conn.(driver.NamedValueChecker).CheckNamedValue(nv)
}

// ResetSession runs when database/sql is about to hand a POOLED slot to a new
// caller. Re-pointing here (not just per statement) means the common case is
// settled before the first query. If the route cannot be resolved the slot is
// discarded (driver.ErrBadConn), so the retry fails closed.
func (r *routedConn) ResetSession(ctx context.Context) error {
	if err := r.ensure(ctx); err != nil {
		slog.WarnContext(ctx, "db routing: discarding pooled connection, route unresolved", "error", err)
		return driver.ErrBadConn
	}
	return r.conn.(driver.SessionResetter).ResetSession(ctx)
}
