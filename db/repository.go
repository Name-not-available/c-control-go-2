package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// User represents a Telegram user in the database
type User struct {
	ID           int64     `json:"id"`
	TelegramID   int64     `json:"telegram_id"`
	Username     string    `json:"username"`
	FirstName    string    `json:"first_name"`
	LastName     string    `json:"last_name"`
	LanguageCode string    `json:"language_code"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SearchHistory represents a search history entry
type SearchHistory struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"user_id"`
	Latitude     float64   `json:"latitude"`
	Longitude    float64   `json:"longitude"`
	Category     string    `json:"category"`
	ResultsCount int       `json:"results_count"`
	APIProvider  string    `json:"api_provider"`
	CreatedAt    time.Time `json:"created_at"`
}

// FavoriteRestaurant represents a user's favorite restaurant
type FavoriteRestaurant struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	PlaceID   string    `json:"place_id"`
	Name      string    `json:"name"`
	Rating    float64   `json:"rating"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	Address   string    `json:"address"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

// CachedRestaurant represents a cached restaurant entry
type CachedRestaurant struct {
	ID             int64     `json:"id"`
	CacheKey       string    `json:"cache_key"`
	PlaceID        string    `json:"place_id"`
	Name           string    `json:"name"`
	Rating         float64   `json:"rating"`
	ReviewCount    int       `json:"review_count"`
	PriceLevel     int       `json:"price_level"`
	RestaurantType string    `json:"restaurant_type"`
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	Address        string    `json:"address"`
	Distance       float64   `json:"distance"`
	PhotoReference string    `json:"photo_reference"`
	Source         string    `json:"source"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// AnalyticsEvent represents an analytics event
type AnalyticsEvent struct {
	ID        int64                  `json:"id"`
	EventType string                 `json:"event_type"`
	UserID    *int64                 `json:"user_id,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
}

// UpsertUser creates or updates a user by Telegram ID
func (db *DB) UpsertUser(ctx context.Context, telegramID int64, username, firstName, lastName, languageCode string) (*User, error) {
	query := fmt.Sprintf(`
		INSERT INTO %s.users (telegram_id, username, first_name, last_name, language_code, updated_at)
		VALUES ($1, $2, $3, $4, $5, CURRENT_TIMESTAMP)
		ON CONFLICT (telegram_id) DO UPDATE SET
			username = EXCLUDED.username,
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			language_code = EXCLUDED.language_code,
			updated_at = CURRENT_TIMESTAMP
		RETURNING id, telegram_id, username, first_name, last_name, language_code, created_at, updated_at
	`, db.Config.Schema)

	var user User
	err := db.Pool.QueryRow(ctx, query, telegramID, username, firstName, lastName, languageCode).Scan(
		&user.ID, &user.TelegramID, &user.Username, &user.FirstName, &user.LastName,
		&user.LanguageCode, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to upsert user: %w", err)
	}
	return &user, nil
}

// GetUserByTelegramID retrieves a user by their Telegram ID
func (db *DB) GetUserByTelegramID(ctx context.Context, telegramID int64) (*User, error) {
	query := fmt.Sprintf(`
		SELECT id, telegram_id, username, first_name, last_name, language_code, created_at, updated_at
		FROM %s.users WHERE telegram_id = $1
	`, db.Config.Schema)

	var user User
	err := db.Pool.QueryRow(ctx, query, telegramID).Scan(
		&user.ID, &user.TelegramID, &user.Username, &user.FirstName, &user.LastName,
		&user.LanguageCode, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return &user, nil
}

// RecordSearchHistory saves a search history entry
func (db *DB) RecordSearchHistory(ctx context.Context, userID int64, lat, lon float64, category string, resultsCount int, apiProvider string) error {
	query := fmt.Sprintf(`
		INSERT INTO %s.search_history (user_id, latitude, longitude, category, results_count, api_provider)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, db.Config.Schema)

	_, err := db.Pool.Exec(ctx, query, userID, lat, lon, category, resultsCount, apiProvider)
	if err != nil {
		return fmt.Errorf("failed to record search history: %w", err)
	}
	return nil
}

// GetUserSearchHistory retrieves search history for a user
func (db *DB) GetUserSearchHistory(ctx context.Context, userID int64, limit int) ([]SearchHistory, error) {
	query := fmt.Sprintf(`
		SELECT id, user_id, latitude, longitude, category, results_count, api_provider, created_at
		FROM %s.search_history
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, db.Config.Schema)

	rows, err := db.Pool.Query(ctx, query, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get search history: %w", err)
	}
	defer rows.Close()

	var history []SearchHistory
	for rows.Next() {
		var h SearchHistory
		if err := rows.Scan(&h.ID, &h.UserID, &h.Latitude, &h.Longitude, &h.Category, &h.ResultsCount, &h.APIProvider, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan search history: %w", err)
		}
		history = append(history, h)
	}
	return history, nil
}

// AddFavoriteRestaurant adds a restaurant to user's favorites
func (db *DB) AddFavoriteRestaurant(ctx context.Context, userID int64, placeID, name string, rating, lat, lon float64, address, source string) error {
	query := fmt.Sprintf(`
		INSERT INTO %s.favorite_restaurants (user_id, place_id, name, rating, latitude, longitude, address, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id, place_id) DO NOTHING
	`, db.Config.Schema)

	_, err := db.Pool.Exec(ctx, query, userID, placeID, name, rating, lat, lon, address, source)
	if err != nil {
		return fmt.Errorf("failed to add favorite restaurant: %w", err)
	}
	return nil
}

// RemoveFavoriteRestaurant removes a restaurant from user's favorites
func (db *DB) RemoveFavoriteRestaurant(ctx context.Context, userID int64, placeID string) error {
	query := fmt.Sprintf(`
		DELETE FROM %s.favorite_restaurants WHERE user_id = $1 AND place_id = $2
	`, db.Config.Schema)

	_, err := db.Pool.Exec(ctx, query, userID, placeID)
	if err != nil {
		return fmt.Errorf("failed to remove favorite restaurant: %w", err)
	}
	return nil
}

// GetUserFavorites retrieves user's favorite restaurants
func (db *DB) GetUserFavorites(ctx context.Context, userID int64) ([]FavoriteRestaurant, error) {
	query := fmt.Sprintf(`
		SELECT id, user_id, place_id, name, rating, latitude, longitude, address, source, created_at
		FROM %s.favorite_restaurants
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, db.Config.Schema)

	rows, err := db.Pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get favorites: %w", err)
	}
	defer rows.Close()

	var favorites []FavoriteRestaurant
	for rows.Next() {
		var f FavoriteRestaurant
		if err := rows.Scan(&f.ID, &f.UserID, &f.PlaceID, &f.Name, &f.Rating, &f.Latitude, &f.Longitude, &f.Address, &f.Source, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan favorite: %w", err)
		}
		favorites = append(favorites, f)
	}
	return favorites, nil
}

// CacheRestaurants stores restaurants in the database cache
func (db *DB) CacheRestaurants(ctx context.Context, cacheKey string, restaurants []CachedRestaurant, ttl time.Duration) error {
	// First, delete existing entries for this cache key
	_, err := db.Pool.Exec(ctx, fmt.Sprintf("DELETE FROM %s.cached_restaurants WHERE cache_key = $1", db.Config.Schema), cacheKey)
	if err != nil {
		return fmt.Errorf("failed to delete old cache entries: %w", err)
	}

	expiresAt := time.Now().Add(ttl)

	// Insert new entries
	for _, r := range restaurants {
		query := fmt.Sprintf(`
			INSERT INTO %s.cached_restaurants 
			(cache_key, place_id, name, rating, review_count, price_level, restaurant_type, latitude, longitude, address, distance, photo_reference, source, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		`, db.Config.Schema)

		_, err := db.Pool.Exec(ctx, query,
			cacheKey, r.PlaceID, r.Name, r.Rating, r.ReviewCount, r.PriceLevel,
			r.RestaurantType, r.Latitude, r.Longitude, r.Address, r.Distance,
			r.PhotoReference, r.Source, expiresAt,
		)
		if err != nil {
			return fmt.Errorf("failed to cache restaurant: %w", err)
		}
	}
	return nil
}

// GetCachedRestaurants retrieves cached restaurants by cache key
func (db *DB) GetCachedRestaurants(ctx context.Context, cacheKey string) ([]CachedRestaurant, bool, error) {
	query := fmt.Sprintf(`
		SELECT id, cache_key, place_id, name, rating, review_count, price_level, restaurant_type,
			   latitude, longitude, address, distance, photo_reference, source, created_at, expires_at
		FROM %s.cached_restaurants
		WHERE cache_key = $1 AND expires_at > $2
		ORDER BY rating DESC, distance ASC
	`, db.Config.Schema)

	rows, err := db.Pool.Query(ctx, query, cacheKey, time.Now())
	if err != nil {
		return nil, false, fmt.Errorf("failed to get cached restaurants: %w", err)
	}
	defer rows.Close()

	var restaurants []CachedRestaurant
	for rows.Next() {
		var r CachedRestaurant
		if err := rows.Scan(
			&r.ID, &r.CacheKey, &r.PlaceID, &r.Name, &r.Rating, &r.ReviewCount, &r.PriceLevel,
			&r.RestaurantType, &r.Latitude, &r.Longitude, &r.Address, &r.Distance,
			&r.PhotoReference, &r.Source, &r.CreatedAt, &r.ExpiresAt,
		); err != nil {
			return nil, false, fmt.Errorf("failed to scan cached restaurant: %w", err)
		}
		restaurants = append(restaurants, r)
	}

	if len(restaurants) == 0 {
		return nil, false, nil
	}

	return restaurants, true, nil
}

// RecordAnalyticsEvent records an analytics event
func (db *DB) RecordAnalyticsEvent(ctx context.Context, eventType string, userID *int64, metadata map[string]interface{}) error {
	var metadataJSON []byte
	var err error
	if metadata != nil {
		metadataJSON, err = json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}
	}

	query := fmt.Sprintf(`
		INSERT INTO %s.analytics (event_type, user_id, metadata)
		VALUES ($1, $2, $3)
	`, db.Config.Schema)

	_, err = db.Pool.Exec(ctx, query, eventType, userID, metadataJSON)
	if err != nil {
		return fmt.Errorf("failed to record analytics event: %w", err)
	}
	return nil
}

// GetAnalyticsStats retrieves basic analytics statistics
func (db *DB) GetAnalyticsStats(ctx context.Context, since time.Time) (map[string]int64, error) {
	query := fmt.Sprintf(`
		SELECT event_type, COUNT(*) as count
		FROM %s.analytics
		WHERE created_at >= $1
		GROUP BY event_type
	`, db.Config.Schema)

	rows, err := db.Pool.Query(ctx, query, since)
	if err != nil {
		return nil, fmt.Errorf("failed to get analytics stats: %w", err)
	}
	defer rows.Close()

	stats := make(map[string]int64)
	for rows.Next() {
		var eventType string
		var count int64
		if err := rows.Scan(&eventType, &count); err != nil {
			return nil, fmt.Errorf("failed to scan analytics stats: %w", err)
		}
		stats[eventType] = count
	}
	return stats, nil
}

// GetTotalUsers returns the total number of users
func (db *DB) GetTotalUsers(ctx context.Context) (int64, error) {
	var count int64
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s.users", db.Config.Schema)
	err := db.Pool.QueryRow(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to get total users: %w", err)
	}
	return count, nil
}

// GetTotalSearches returns the total number of searches
func (db *DB) GetTotalSearches(ctx context.Context) (int64, error) {
	var count int64
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s.search_history", db.Config.Schema)
	err := db.Pool.QueryRow(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to get total searches: %w", err)
	}
	return count, nil
}
