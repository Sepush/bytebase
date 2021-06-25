package db

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"
)

type Type string

const (
	Mysql Type = "MYSQL"
)

func (e Type) String() string {
	switch e {
	case Mysql:
		return "MYSQL"
	}
	return "UNKNOWN"
}

type DBTable struct {
	Name      string
	CreatedTs int64
	UpdatedTs int64
	Engine    string
	Collation string
	RowCount  int64
	DataSize  int64
	IndexSize int64
}

type DBSchema struct {
	Name         string
	CharacterSet string
	Collation    string
	TableList    []DBTable
}

var (
	driversMu sync.RWMutex
	drivers   = make(map[Type]DriverFunc)
)

type DriverConfig struct {
	Logger *zap.Logger
}

type DriverFunc func(DriverConfig) Driver

type MigrationType string

const (
	Baseline MigrationType = "BASELINE"
	Sql      MigrationType = "SQL"
)

func (e MigrationType) String() string {
	switch e {
	case Baseline:
		return "BASELINE"
	case Sql:
		return "SQL"
	}
	return "UNKNOWN"
}

type MigrationInfo struct {
	Version     string
	Namespace   string
	Database    string
	Type        MigrationType
	Description string
	Creator     string
}

// Expected filename example, {{version}} can be arbitrary string without "_"
// - {{version}}_db1 (a normal migration without description)
// - {{version}}_db1_create_t1 (a normal migration with "create t1" as description)
// - {{version}}_db1_baseline  (a baseline migration without description)
// - {{version}}_db1_baseline_create_t1  (a baseline migration with "create t1" as description)
func ParseMigrationInfo(filename string) (*MigrationInfo, error) {
	parts := strings.Split(strings.TrimSuffix(filename, ".sql"), "_")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid filename format, got %v, want {{version}}_{{dbname}}[_{{type}}][_{{description}}].sql", filename)
	}
	mi := &MigrationInfo{
		Version:   parts[0],
		Namespace: parts[1],
		Database:  parts[1],
	}

	migrationType := Sql
	description := ""
	if len(parts) > 2 {
		if parts[2] == "baseline" {
			migrationType = Baseline
			if len(parts) > 3 {
				description = strings.Join(parts[3:], " ")
			}
		} else {
			description = strings.Join(parts[2:], " ")
		}
	}
	if description == "" {
		if migrationType == Baseline {
			description = fmt.Sprintf("Create %s baseline", mi.Database)
		} else {
			description = fmt.Sprintf("Create %s migration", mi.Database)
		}
	}
	mi.Type = migrationType
	// Capitalize first letter
	mi.Description = strings.ToUpper(description[:1]) + description[1:]

	return mi, nil
}

type Driver interface {
	open(config ConnectionConfig) (Driver, error)
	Ping(ctx context.Context) error
	SyncSchema(ctx context.Context) ([]*DBSchema, error)
	Execute(ctx context.Context, statement string) error

	// Migration related
	// Check whether we need to setup migration (e.g. creating/upgrading the migration related tables)
	NeedsSetupMigration(ctx context.Context) (bool, error)
	// Create or upgrade migration related tables
	SetupMigrationIfNeeded(ctx context.Context) error
	// Execute migration will apply the statement and record the migration history on success.
	ExecuteMigration(ctx context.Context, m *MigrationInfo, statement string) error
}

type ConnectionConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	Database string
}

// Register makes a database driver available by the provided type.
// If Register is called twice with the same name or if driver is nil,
// it panics.
func register(dbType Type, f DriverFunc) {
	driversMu.Lock()
	defer driversMu.Unlock()
	if f == nil {
		panic("db: Register driver is nil")
	}
	if _, dup := drivers[dbType]; dup {
		panic("db: Register called twice for driver " + dbType)
	}
	drivers[dbType] = f
}

// Open opens a database specified by its database driver type and connection config
func Open(dbType Type, driverConfig DriverConfig, connectionConfig ConnectionConfig) (Driver, error) {
	driversMu.RLock()
	f, ok := drivers[dbType]
	driversMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("db: unknown driver %v", dbType)
	}

	driver, err := f(driverConfig).open(connectionConfig)
	if err != nil {
		return nil, err
	}

	if err := driver.Ping(context.Background()); err != nil {
		return nil, err
	}

	return driver, nil
}