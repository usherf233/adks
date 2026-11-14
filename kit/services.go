package kit

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"

	"github.com/usherf233/adks/manifest"
)

// NewSessionService builds the session backend. Every SQL backend runs
// AutoMigrate on construction because ADK adds columns between releases and a
// missing column only shows up at write time.
func NewSessionService(_ context.Context, spec manifest.SessionSpec) (session.Service, error) {
	switch spec.Backend {
	case "", "memory":
		return session.InMemoryService(), nil

	case "sqlite":
		if err := ensureSQLiteDir(spec.DSN); err != nil {
			return nil, err
		}
		return newSQLSession(sqlite.Open(spec.DSN), spec)
	case "postgres":
		return newSQLSession(postgres.Open(spec.DSN), spec)
	case "mysql":
		return newSQLSession(mysql.Open(spec.DSN), spec)
	}
	return nil, fmt.Errorf("unsupported session backend %q", spec.Backend)
}

// ensureSQLiteDir creates the parent directory of a file-backed sqlite DSN.
//
// sqlite reports a missing directory as "unable to open database file: out of
// memory (14)", which points at memory rather than at the missing folder. Making
// the directory first turns a confusing failure into a working database.
func ensureSQLiteDir(dsn string) error {
	path := dsn
	if strings.HasPrefix(path, "file:") {
		path = strings.TrimPrefix(path, "file:")
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if path == "" || path == ":memory:" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" || dir == string(filepath.Separator) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sqlite directory %s: %w", dir, err)
	}
	return nil
}

func newSQLSession(dialector gorm.Dialector, spec manifest.SessionSpec) (session.Service, error) {
	db, err := gorm.Open(dialector, &gorm.Config{
		// ADK's queries are already logged by our audit sink; gorm's default
		// logger prints every SELECT to stdout, which buries the agent transcript.
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open %s session database: %w", spec.Backend, err)
	}
	maxOpen := spec.MaxOpenConns
	if maxOpen <= 0 {
		if spec.Backend == "sqlite" {
			maxOpen = 1
		} else {
			maxOpen = 10
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("session database handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxOpen)
	// SQLite with a single writer still benefits from a lifetime bound; remote
	// databases get one so a dead connection is replaced rather than reused.
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	svc, err := database.NewSessionServiceFromDB(db)
	if err != nil {
		return nil, fmt.Errorf("session service: %w", err)
	}
	// ADK never creates its own tables: without this call the first Create fails
	// with "no such table: app_states" at runtime, long after startup looked fine.
	if err := database.AutoMigrate(svc); err != nil {
		return nil, fmt.Errorf("session schema migration: %w", err)
	}
	return &sqlSession{Service: svc, db: db}, nil
}

// sqlSession carries the gorm handle so the connection pool can be released.
// Without it a closed builder would keep sqlite files locked on Windows.
type sqlSession struct {
	session.Service
	db *gorm.DB
}

// Close releases the underlying connection pool. It is idempotent enough for the
// Layer's reverse-order close.
func (s *sqlSession) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// CloseService releases a session service if it owns a pool.
func CloseService(svc session.Service) error {
	if c, ok := svc.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// DBStats reports connection pool facts, used by the builder's SQL probe.
func DBStats(svc session.Service) (open int, idle int, err error) {
	type statser interface{ Stats() sql.DBStats }
	if s, ok := svc.(statser); ok {
		st := s.Stats()
		return st.OpenConnections, st.Idle, nil
	}
	return 0, 0, nil
}

// NewArtifactService builds the artifact backend.
func NewArtifactService(spec manifest.ArtifactSpec) (artifact.Service, error) {
	switch spec.Backend {
	case "", "memory":
		return artifact.InMemoryService(), nil
	case "local":
		return NewLocalArtifactService(spec.Dir)
	}
	return nil, fmt.Errorf("unsupported artifact backend %q", spec.Backend)
}

// NewMemoryService builds the long-term memory backend. v1 ships only the
// in-process one: a durable memory store needs a retention and trust policy, and
// defaulting that on would be a decision the operator did not make.
func NewMemoryService(spec manifest.BackendSpec) (memory.Service, error) {
	switch spec.Backend {
	case "", "memory":
		return memory.InMemoryService(), nil
	}
	return nil, fmt.Errorf("unsupported memory backend %q", spec.Backend)
}
