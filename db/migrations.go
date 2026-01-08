package db

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Migration represents a database migration
type Migration struct {
	Version     int
	Description string
	Up          string
}

// migrations defines all database migrations in order
// Each migration creates tables in the configured schema
func getMigrations(schema string) []Migration {
	return []Migration{
		{
			Version:     1,
			Description: "Create schema and migrations table",
			Up: fmt.Sprintf(`
				-- Create schema if not exists
				CREATE SCHEMA IF NOT EXISTS %s;
				
				-- Create migrations tracking table
				CREATE TABLE IF NOT EXISTS %s.schema_migrations (
					version INTEGER PRIMARY KEY,
					description TEXT NOT NULL,
					applied_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				);
			`, schema, schema),
		},
		{
			Version:     2,
			Description: "Create users table",
			Up: fmt.Sprintf(`
				CREATE TABLE IF NOT EXISTS %s.users (
					id BIGSERIAL PRIMARY KEY,
					telegram_id BIGINT UNIQUE NOT NULL,
					username VARCHAR(255),
					first_name VARCHAR(255),
					last_name VARCHAR(255),
					language_code VARCHAR(10),
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
					updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				);
				
				CREATE INDEX IF NOT EXISTS idx_users_telegram_id ON %s.users(telegram_id);
			`, schema, schema),
		},
		{
			Version:     3,
			Description: "Create search_history table",
			Up: fmt.Sprintf(`
				CREATE TABLE IF NOT EXISTS %s.search_history (
					id BIGSERIAL PRIMARY KEY,
					user_id BIGINT REFERENCES %s.users(id) ON DELETE CASCADE,
					latitude DOUBLE PRECISION NOT NULL,
					longitude DOUBLE PRECISION NOT NULL,
					category VARCHAR(50),
					results_count INTEGER DEFAULT 0,
					api_provider VARCHAR(20),
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				);
				
				CREATE INDEX IF NOT EXISTS idx_search_history_user_id ON %s.search_history(user_id);
				CREATE INDEX IF NOT EXISTS idx_search_history_created_at ON %s.search_history(created_at);
			`, schema, schema, schema, schema),
		},
		{
			Version:     4,
			Description: "Create favorite_restaurants table",
			Up: fmt.Sprintf(`
				CREATE TABLE IF NOT EXISTS %s.favorite_restaurants (
					id BIGSERIAL PRIMARY KEY,
					user_id BIGINT REFERENCES %s.users(id) ON DELETE CASCADE,
					place_id VARCHAR(255),
					name VARCHAR(500) NOT NULL,
					rating DOUBLE PRECISION,
					latitude DOUBLE PRECISION NOT NULL,
					longitude DOUBLE PRECISION NOT NULL,
					address TEXT,
					source VARCHAR(20),
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
					UNIQUE(user_id, place_id)
				);
				
				CREATE INDEX IF NOT EXISTS idx_favorite_restaurants_user_id ON %s.favorite_restaurants(user_id);
			`, schema, schema, schema),
		},
		{
			Version:     5,
			Description: "Create cached_restaurants table",
			Up: fmt.Sprintf(`
				CREATE TABLE IF NOT EXISTS %s.cached_restaurants (
					id BIGSERIAL PRIMARY KEY,
					cache_key VARCHAR(100) NOT NULL,
					place_id VARCHAR(255),
					name VARCHAR(500) NOT NULL,
					rating DOUBLE PRECISION,
					review_count INTEGER,
					price_level INTEGER,
					restaurant_type VARCHAR(100),
					latitude DOUBLE PRECISION NOT NULL,
					longitude DOUBLE PRECISION NOT NULL,
					address TEXT,
					distance DOUBLE PRECISION,
					photo_reference TEXT,
					source VARCHAR(20),
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
					expires_at TIMESTAMP WITH TIME ZONE NOT NULL
				);
				
				CREATE INDEX IF NOT EXISTS idx_cached_restaurants_cache_key ON %s.cached_restaurants(cache_key);
				CREATE INDEX IF NOT EXISTS idx_cached_restaurants_expires_at ON %s.cached_restaurants(expires_at);
			`, schema, schema, schema),
		},
		{
			Version:     6,
			Description: "Create analytics table",
			Up: fmt.Sprintf(`
				CREATE TABLE IF NOT EXISTS %s.analytics (
					id BIGSERIAL PRIMARY KEY,
					event_type VARCHAR(50) NOT NULL,
					user_id BIGINT REFERENCES %s.users(id) ON DELETE SET NULL,
					metadata JSONB,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				);
				
				CREATE INDEX IF NOT EXISTS idx_analytics_event_type ON %s.analytics(event_type);
				CREATE INDEX IF NOT EXISTS idx_analytics_created_at ON %s.analytics(created_at);
				CREATE INDEX IF NOT EXISTS idx_analytics_user_id ON %s.analytics(user_id);
			`, schema, schema, schema, schema, schema),
		},
	}
}

// RunMigrations executes all pending migrations
func (db *DB) RunMigrations(ctx context.Context) error {
	migrations := getMigrations(db.Config.Schema)

	// Run migration 1 first to ensure schema and migrations table exist
	if len(migrations) > 0 {
		_, err := db.Pool.Exec(ctx, migrations[0].Up)
		if err != nil {
			return fmt.Errorf("failed to create schema and migrations table: %w", err)
		}
	}

	// Get current version
	currentVersion := 0
	row := db.Pool.QueryRow(ctx, fmt.Sprintf(
		"SELECT COALESCE(MAX(version), 0) FROM %s.schema_migrations",
		db.Config.Schema,
	))
	if err := row.Scan(&currentVersion); err != nil {
		// Table might not exist yet, which is fine
		currentVersion = 0
	}

	log.Printf("Current database schema version: %d", currentVersion)

	// Run pending migrations
	for _, migration := range migrations {
		if migration.Version <= currentVersion {
			continue
		}

		log.Printf("Running migration %d: %s", migration.Version, migration.Description)

		// Execute migration
		if _, err := db.Pool.Exec(ctx, migration.Up); err != nil {
			return fmt.Errorf("failed to run migration %d (%s): %w", migration.Version, migration.Description, err)
		}

		// Record migration
		_, err := db.Pool.Exec(ctx, fmt.Sprintf(
			"INSERT INTO %s.schema_migrations (version, description) VALUES ($1, $2)",
			db.Config.Schema,
		), migration.Version, migration.Description)
		if err != nil {
			return fmt.Errorf("failed to record migration %d: %w", migration.Version, err)
		}

		log.Printf("Migration %d completed successfully", migration.Version)
	}

	log.Printf("All migrations completed. Schema version: %d", len(migrations))
	return nil
}

// GetSchemaVersion returns the current schema version
func (db *DB) GetSchemaVersion(ctx context.Context) (int, error) {
	var version int
	row := db.Pool.QueryRow(ctx, fmt.Sprintf(
		"SELECT COALESCE(MAX(version), 0) FROM %s.schema_migrations",
		db.Config.Schema,
	))
	if err := row.Scan(&version); err != nil {
		return 0, fmt.Errorf("failed to get schema version: %w", err)
	}
	return version, nil
}

// CleanupExpiredCache removes expired entries from the cache table
func (db *DB) CleanupExpiredCache(ctx context.Context) (int64, error) {
	result, err := db.Pool.Exec(ctx, fmt.Sprintf(
		"DELETE FROM %s.cached_restaurants WHERE expires_at < $1",
		db.Config.Schema,
	), time.Now())
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup expired cache: %w", err)
	}
	return result.RowsAffected(), nil
}
