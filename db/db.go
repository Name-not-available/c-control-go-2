package db

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds database configuration from environment variables
type Config struct {
	Host             string
	Port             int
	User             string
	Password         string
	DBName           string
	Schema           string
	SSLMode          string
	AllowInsecureSSL bool
}

// DB wraps the pgx connection pool and provides database operations
type DB struct {
	Pool   *pgxpool.Pool
	Config *Config
}

// LoadConfig loads database configuration from environment variables
func LoadConfig() (*Config, error) {
	port := 5432 // default PostgreSQL port
	if portStr := os.Getenv("DB_PORT"); portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("invalid DB_PORT value: %w", err)
		}
		port = p
	}

	allowInsecureSSL := false
	if insecureStr := os.Getenv("DB_ALLOW_INSECURE_SSL"); insecureStr != "" {
		allowInsecureSSL = insecureStr == "true" || insecureStr == "1"
	}

	sslMode := os.Getenv("DB_SSLMODE")
	if sslMode == "" {
		sslMode = "prefer" // default SSL mode
	}

	schema := os.Getenv("DB_SCHEMA")
	if schema == "" {
		schema = "public" // default schema
	}

	return &Config{
		Host:             os.Getenv("DB_HOST"),
		Port:             port,
		User:             os.Getenv("DB_USER"),
		Password:         os.Getenv("DB_PASSWORD"),
		DBName:           os.Getenv("DB_NAME"),
		Schema:           schema,
		SSLMode:          sslMode,
		AllowInsecureSSL: allowInsecureSSL,
	}, nil
}

// IsConfigured returns true if database configuration is provided
func (c *Config) IsConfigured() bool {
	return c.Host != "" && c.DBName != "" && c.User != ""
}

// ConnectionString builds a PostgreSQL connection string from config
func (c *Config) ConnectionString() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s search_path=%s",
		c.Host, c.Port, c.User, c.Password, c.DBName, c.SSLMode, c.Schema,
	)
}

// Connect establishes a connection to the database
func Connect(ctx context.Context, config *Config) (*DB, error) {
	if !config.IsConfigured() {
		return nil, fmt.Errorf("database configuration is incomplete")
	}

	// Parse the connection string
	poolConfig, err := pgxpool.ParseConfig(config.ConnectionString())
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	// Configure connection pool
	poolConfig.MaxConns = 10
	poolConfig.MinConns = 2
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute

	// Handle insecure SSL if enabled
	if config.AllowInsecureSSL && (config.SSLMode == "require" || config.SSLMode == "verify-ca" || config.SSLMode == "verify-full") {
		poolConfig.ConnConfig.TLSConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	// Set after connect hook to set search path
	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s", config.Schema))
		return err
	}

	// Create the connection pool
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Test the connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	log.Printf("Connected to database %s on %s:%d (schema: %s)", config.DBName, config.Host, config.Port, config.Schema)

	return &DB{
		Pool:   pool,
		Config: config,
	}, nil
}

// Close closes the database connection pool
func (db *DB) Close() {
	if db.Pool != nil {
		db.Pool.Close()
		log.Println("Database connection pool closed")
	}
}

// Ping checks if the database connection is alive
func (db *DB) Ping(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}

// SchemaName returns the configured schema name
func (db *DB) SchemaName() string {
	return db.Config.Schema
}

// TableName returns a fully qualified table name with schema
func (db *DB) TableName(table string) string {
	return fmt.Sprintf("%s.%s", db.Config.Schema, table)
}
