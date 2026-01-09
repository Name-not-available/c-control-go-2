package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"googlemaps.github.io/maps"
)

const (
	telegramMaxMessageLength = 4096
	maxRestaurantsPerMessage = 5
	requestTimeout           = 10 * time.Second
	cacheTTL                 = 48 * time.Hour      // Cache results for 48 hours
	cacheRadiusMeters        = 20.0                // 20 meter radius for cache matching
	photoCachePath           = "/restaurant/photo" // Path to permanent photo storage directory

	// Generic photo constants - used for restaurants that shouldn't trigger Google API calls
	genericPhotoReference = "GENERIC"           // Special marker for generic/placeholder photo
	minRatingForPhoto     = 4.0                 // Minimum rating to fetch real photo (below this = generic)
	minReviewsForPhoto    = 5                   // Minimum reviews to fetch real photo (below this = generic)
	genericPhotoFilename  = "generic_placeholder.jpg" // Filename for the generic placeholder image
)

// generateGenericPlaceholderImage creates a simple placeholder image for restaurants
// without photos or with low ratings/reviews. Returns JPEG bytes.
// The image is a 400x300 light gray rectangle with a darker gray center icon area.
func generateGenericPlaceholderImage() ([]byte, error) {
	// Create a 400x300 image
	width, height := 400, 300
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Fill with light gray background (#E8E8E8)
	bgColor := color.RGBA{232, 232, 232, 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{bgColor}, image.Point{}, draw.Src)

	// Draw a darker gray rectangle in the center (simulating a plate/placeholder icon)
	iconColor := color.RGBA{180, 180, 180, 255}
	iconRect := image.Rect(150, 100, 250, 200)
	draw.Draw(img, iconRect, &image.Uniform{iconColor}, image.Point{}, draw.Src)

	// Draw a simple fork-like shape (3 vertical lines) - left side
	forkColor := color.RGBA{140, 140, 140, 255}
	for i := 0; i < 3; i++ {
		x := 120 + i*8
		forkRect := image.Rect(x, 90, x+4, 180)
		draw.Draw(img, forkRect, &image.Uniform{forkColor}, image.Point{}, draw.Src)
	}
	// Fork handle
	handleRect := image.Rect(120, 180, 140, 210)
	draw.Draw(img, handleRect, &image.Uniform{forkColor}, image.Point{}, draw.Src)

	// Draw a simple knife shape - right side
	knifeRect := image.Rect(265, 90, 275, 210)
	draw.Draw(img, knifeRect, &image.Uniform{forkColor}, image.Point{}, draw.Src)

	// Encode to JPEG
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("failed to encode placeholder image: %w", err)
	}

	return buf.Bytes(), nil
}

// getOrCreateGenericPlaceholder returns the generic placeholder image, creating it if needed.
// The image is saved to disk for future use.
func getOrCreateGenericPlaceholder() ([]byte, error) {
	placeholderPath := filepath.Join(photoCachePath, genericPhotoFilename)

	// Check if placeholder already exists on disk
	if data, err := os.ReadFile(placeholderPath); err == nil && len(data) > 0 {
		return data, nil
	}

	// Generate the placeholder image
	data, err := generateGenericPlaceholderImage()
	if err != nil {
		return nil, err
	}

	// Save to disk (create directory if needed)
	if err := os.MkdirAll(photoCachePath, 0755); err != nil {
		log.Printf("[PHOTO][GENERIC] Warning: could not create directory %s: %v", photoCachePath, err)
	} else {
		if err := os.WriteFile(placeholderPath, data, 0644); err != nil {
			log.Printf("[PHOTO][GENERIC] Warning: could not save placeholder to disk: %v", err)
		} else {
			log.Printf("[PHOTO][GENERIC] Created and saved placeholder image: %s (%d bytes)", placeholderPath, len(data))
		}
	}

	return data, nil
}

// shouldUseGenericPhoto determines if a restaurant should use the generic placeholder
// instead of fetching a real photo from Google API.
// Returns true if:
// - Restaurant has no photo reference
// - Restaurant rating is below minRatingForPhoto (4.0)
// - Restaurant has fewer than minReviewsForPhoto (5) reviews
func shouldUseGenericPhoto(photoRef string, rating float64, reviewCount int) bool {
	// No photo available
	if photoRef == "" {
		return true
	}
	// Already marked as generic
	if photoRef == genericPhotoReference {
		return true
	}
	// Low rating - don't waste API calls
	if rating > 0 && rating < minRatingForPhoto {
		return true
	}
	// Too few reviews - likely unreliable/new place
	if reviewCount < minReviewsForPhoto {
		return true
	}
	return false
}

// saveGenericPlaceholderForFailedPhoto saves the generic placeholder image to the given path
// so that future requests for this photo reference won't call the Google API again.
// This is called when the Google API fails to return a valid photo.
func saveGenericPlaceholderForFailedPhoto(storedPath, filename string) {
	placeholderData, err := getOrCreateGenericPlaceholder()
	if err != nil {
		log.Printf("[PHOTO][FALLBACK][ERROR] Failed to get placeholder: %v", err)
		return
	}

	if err := os.MkdirAll(photoCachePath, 0755); err != nil {
		log.Printf("[PHOTO][FALLBACK][ERROR] Failed to create directory: %v", err)
		return
	}

	if err := os.WriteFile(storedPath, placeholderData, 0644); err != nil {
		log.Printf("[PHOTO][FALLBACK][ERROR] Failed to save placeholder to %s: %v", storedPath, err)
	} else {
		log.Printf("[PHOTO][FALLBACK] Saved generic placeholder as %s - future requests will use this (no API calls)", filename)
	}
}

// serveGenericPlaceholderOnError serves the generic placeholder image when an API call fails.
// This ensures the client still gets an image even when the Google API fails.
func serveGenericPlaceholderOnError(w http.ResponseWriter) {
	placeholderData, err := getOrCreateGenericPlaceholder()
	if err != nil {
		log.Printf("[PHOTO][FALLBACK][ERROR] Failed to serve placeholder: %v", err)
		http.Error(w, "Failed to load placeholder image", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Photo-Source", "generic")
	w.Header().Set("Access-Control-Expose-Headers", "X-Photo-Source")
	w.Write(placeholderData)
}

// FoodCategory represents a category of food establishments
type FoodCategory string

const (
	CategoryAll        FoodCategory = "all"
	CategoryRestaurant FoodCategory = "restaurant"
	CategoryCafe       FoodCategory = "cafe"
	CategoryBar        FoodCategory = "bar"
	CategoryTakeaway   FoodCategory = "takeaway"
	CategoryBakery     FoodCategory = "bakery"
	CategoryDelivery   FoodCategory = "delivery"
	CategoryNightclub  FoodCategory = "nightclub"
)

// categoryToGoogleType maps categories to Google Places types
var categoryToGoogleType = map[FoodCategory]maps.PlaceType{
	CategoryRestaurant: maps.PlaceTypeRestaurant,
	CategoryCafe:       maps.PlaceTypeCafe,
	CategoryBar:        maps.PlaceTypeBar,
	CategoryTakeaway:   maps.PlaceTypeMealTakeaway,
	CategoryBakery:     maps.PlaceTypeBakery,
	CategoryDelivery:   maps.PlaceTypeMealDelivery,
	CategoryNightclub:  maps.PlaceTypeNightClub,
}

// categoryToOSMAmenities maps categories to OSM amenity values
var categoryToOSMAmenities = map[FoodCategory][]string{
	CategoryAll:        {"restaurant", "fast_food", "cafe", "bar", "pub", "biergarten", "food_court", "ice_cream", "bakery"},
	CategoryRestaurant: {"restaurant"},
	CategoryCafe:       {"cafe"},
	CategoryBar:        {"bar", "pub", "biergarten"},
	CategoryTakeaway:   {"fast_food"},
	CategoryBakery:     {"bakery"},
	CategoryDelivery:   {"restaurant", "fast_food"}, // OSM doesn't have specific delivery tag
	CategoryNightclub:  {"nightclub"},
}

// allFoodCategories lists all categories to search for "all" option
var allFoodCategories = []FoodCategory{
	CategoryRestaurant,
	CategoryCafe,
	CategoryBar,
	CategoryTakeaway,
	CategoryBakery,
	CategoryDelivery,
}

// cuisineKeywordsForSearch are keywords to search with type='restaurant'
// Google's NearbySearch does NOT support cuisine-specific types as filters
// (e.g., 'indian_restaurant' as type returns random places, not Indian restaurants)
// Instead, we search type='restaurant' with keyword='indian', 'chinese', etc.
var cuisineKeywordsForSearch = []string{
	"indian",
	"chinese",
	"thai",
	"japanese",
	"korean",
	"vietnamese",
	"italian",
	"mexican",
	"french",
	"greek",
	"mediterranean",
	"american",
	"seafood",
	"steak",
	"barbecue",
	"pizza",
	"burger",
	"sushi",
	"ramen",
	"vegetarian",
	"vegan",
	"breakfast",
	"brunch",
}

// Cuisine keywords for more specific searches
// These are used with Google's Keyword parameter
var cuisineKeywords = map[string]string{
	// Diet/Health
	"healthy":    "healthy food",
	"vegan":      "vegan",
	"vegetarian": "vegetarian",
	"organic":    "organic food",
	"gluten-free": "gluten free",
	"halal":      "halal",
	"kosher":     "kosher",
	
	// Cuisines
	"italian":    "italian",
	"chinese":    "chinese",
	"japanese":   "japanese",
	"sushi":      "sushi",
	"mexican":    "mexican",
	"indian":     "indian",
	"thai":       "thai",
	"vietnamese": "vietnamese",
	"korean":     "korean",
	"french":     "french",
	"greek":      "greek",
	"mediterranean": "mediterranean",
	"american":   "american",
	"bbq":        "bbq barbecue",
	"seafood":    "seafood",
	"steakhouse": "steakhouse steak",
	"burger":     "burger",
	"pizza":      "pizza",
	"ramen":      "ramen",
	"tacos":      "tacos",
	"breakfast":  "breakfast brunch",
	"dessert":    "dessert ice cream",
}

// validFoodTypes is a whitelist for last-resort post-filtering
// Includes both generic types and Google's cuisine-specific restaurant types
var validFoodTypes = map[string]bool{
	// Generic food establishment types
	"restaurant":      true,
	"cafe":            true,
	"bar":             true,
	"bakery":          true,
	"meal_delivery":   true,
	"meal_takeaway":   true,
	"night_club":      true,
	"food":            true,
	"fast_food":       true,
	"pub":             true,
	"biergarten":      true,
	"food_court":      true,
	"ice_cream":       true,
	
	// Google's cuisine-specific restaurant types (added 2023+)
	"indian_restaurant":         true,
	"chinese_restaurant":        true,
	"thai_restaurant":           true,
	"japanese_restaurant":       true,
	"korean_restaurant":         true,
	"vietnamese_restaurant":     true,
	"italian_restaurant":        true,
	"mexican_restaurant":        true,
	"french_restaurant":         true,
	"greek_restaurant":          true,
	"mediterranean_restaurant":  true,
	"american_restaurant":       true,
	"brazilian_restaurant":      true,
	"spanish_restaurant":        true,
	"middle_eastern_restaurant": true,
	"turkish_restaurant":        true,
	"lebanese_restaurant":       true,
	"indonesian_restaurant":     true,
	"asian_restaurant":          true,
	"african_restaurant":        true,
	"seafood_restaurant":        true,
	"steak_house":               true,
	"barbecue_restaurant":       true,
	"pizza_restaurant":          true,
	"hamburger_restaurant":      true,
	"sandwich_shop":             true,
	"ramen_restaurant":          true,
	"sushi_restaurant":          true,
	"vegetarian_restaurant":     true,
	"vegan_restaurant":          true,
	"brunch_restaurant":         true,
	"breakfast_restaurant":      true,
	"buffet_restaurant":         true,
	"fine_dining_restaurant":    true,
	"fast_food_restaurant":      true,
	"coffee_shop":               true,
	"tea_house":                 true,
	"juice_shop":                true,
	"ice_cream_shop":            true,
	"dessert_shop":              true,
	"donut_shop":                true,
	"candy_store":               true,
	"wine_bar":                  true,
	"cocktail_bar":              true,
	"sports_bar":                true,
	"beer_hall":                 true,
	"beer_garden":               true,
}

type RestaurantBot struct {
	telegramBot *tgbotapi.BotAPI
	mapsClient  *maps.Client
	cache       *LocationCache
	apiProvider string // "google", "osm", or "both"
}

// LocationCache stores cached restaurant results
type LocationCache struct {
	mu    sync.RWMutex
	items []cacheItem
}

type cacheItem struct {
	lat         float64
	lon         float64
	restaurants []Restaurant
	stats       SearchStats
	expiresAt   time.Time
}

// Restaurant represents a restaurant (unified format for different APIs)
type Restaurant struct {
	Name           string  `json:"Name"`
	Rating         float64 `json:"Rating"`
	ReviewCount    int     `json:"ReviewCount,omitempty"`
	PriceLevel     int     `json:"PriceLevel,omitempty"`
	Type           string  `json:"Type,omitempty"`
	Latitude       float64 `json:"Latitude"`
	Longitude      float64 `json:"Longitude"`
	Address        string  `json:"Address"`
	Distance       float64 `json:"Distance"`
	PhotoReference string  `json:"PhotoReference,omitempty"`
	PlaceID        string  `json:"PlaceID,omitempty"`
}

// SearchStats contains statistics about the search operation
type SearchStats struct {
	GooglePagesSearched   int  `json:"googlePagesSearched"`   // Total number of Google API pages fetched
	GoogleSearchQueries   int  `json:"googleSearchQueries"`   // Number of different Google search queries made
	GoogleResultsRaw      int  `json:"googleResultsRaw"`      // Raw results from Google before filtering
	GoogleResultsFiltered int  `json:"googleResultsFiltered"` // Results from Google after food filtering
	OSMResultsTotal       int  `json:"osmResultsTotal"`       // Total results from OSM
	TotalBeforeDedup      int  `json:"totalBeforeDedup"`      // Combined total before deduplication
	TotalAfterDedup       int  `json:"totalAfterDedup"`       // Final count after deduplication
	CachedResult          bool `json:"cachedResult"`          // True if results were returned from cache
}

// SearchResult contains both restaurants and statistics
type SearchResult struct {
	Restaurants []Restaurant `json:"restaurants"`
	Stats       SearchStats  `json:"stats"`
}

// PaginatedSearchResult extends SearchResult with pagination info
type PaginatedSearchResult struct {
	Restaurants []Restaurant `json:"restaurants"`
	Stats       SearchStats  `json:"stats"`
	Pagination  Pagination   `json:"pagination"`
}

// Pagination contains pagination metadata
type Pagination struct {
	Page       int `json:"page"`       // Current page (1-indexed)
	Limit      int `json:"limit"`      // Items per page
	TotalItems int `json:"totalItems"` // Total number of items
	TotalPages int `json:"totalPages"` // Total number of pages
	HasNext    bool `json:"hasNext"`   // Whether there's a next page
	HasPrev    bool `json:"hasPrev"`   // Whether there's a previous page
}

// NewLocationCache creates a new location cache
func NewLocationCache() *LocationCache {
	cache := &LocationCache{
		items: make([]cacheItem, 0),
	}
	// Start cleanup goroutine
	go cache.cleanup()
	return cache
}

// cleanup removes expired cache entries
func (lc *LocationCache) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		lc.mu.Lock()
		now := time.Now()
		newItems := make([]cacheItem, 0, len(lc.items))
		for _, item := range lc.items {
			if now.Before(item.expiresAt) {
				newItems = append(newItems, item)
			}
		}
		lc.items = newItems
		lc.mu.Unlock()
	}
}

// Get retrieves cached restaurants for a location within 20m radius
func (lc *LocationCache) Get(lat, lon float64) ([]Restaurant, *SearchStats, bool) {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	now := time.Now()
	for _, item := range lc.items {
		if now.After(item.expiresAt) {
			continue
		}
		// Calculate distance in meters (calculateDistance returns km)
		distanceKm := calculateDistance(lat, lon, item.lat, item.lon)
		distanceMeters := distanceKm * 1000
		if distanceMeters <= cacheRadiusMeters {
			// Return a copy of stats with CachedResult set to true
			cachedStats := item.stats
			cachedStats.CachedResult = true
			return item.restaurants, &cachedStats, true
		}
	}
	return nil, nil, false
}

// Set stores restaurants in cache with their location and stats
func (lc *LocationCache) Set(lat, lon float64, restaurants []Restaurant, stats SearchStats) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	// Check if we already have a cache entry for this location (within radius)
	for i, item := range lc.items {
		distanceKm := calculateDistance(lat, lon, item.lat, item.lon)
		distanceMeters := distanceKm * 1000
		if distanceMeters <= cacheRadiusMeters {
			// Update existing entry
			lc.items[i] = cacheItem{
				lat:         lat,
				lon:         lon,
				restaurants: restaurants,
				stats:       stats,
				expiresAt:   time.Now().Add(cacheTTL),
			}
			return
		}
	}
	// Add new entry
	lc.items = append(lc.items, cacheItem{
		lat:         lat,
		lon:         lon,
		restaurants: restaurants,
		stats:       stats,
		expiresAt:   time.Now().Add(cacheTTL),
	})
}

func NewRestaurantBot(telegramToken string, googleMapsAPIKey string, apiProvider string) (*RestaurantBot, error) {
	var bot *tgbotapi.BotAPI
	var err error

	// Only create Telegram bot if token is provided
	if telegramToken != "" {
		bot, err = tgbotapi.NewBotAPI(telegramToken)
		if err != nil {
			return nil, fmt.Errorf("failed to create telegram bot: %w", err)
		}
	}

	var mapsClient *maps.Client
	// Initialize Google Maps client if needed
	if apiProvider == "" || apiProvider == "google" || apiProvider == "both" {
		if googleMapsAPIKey == "" && apiProvider != "osm" {
			return nil, fmt.Errorf("GOOGLE_MAPS_API_KEY is required when using Google Maps API")
		}
		if googleMapsAPIKey != "" {
			mapsClient, err = maps.NewClient(maps.WithAPIKey(googleMapsAPIKey))
			if err != nil {
				return nil, fmt.Errorf("failed to create maps client: %w", err)
			}
		}
	}

	if apiProvider == "" {
		apiProvider = "google"
	}

	return &RestaurantBot{
		telegramBot: bot,
		mapsClient:  mapsClient,
		cache:       NewLocationCache(),
		apiProvider: apiProvider,
	}, nil
}

func (rb *RestaurantBot) Start() error {
	if rb.telegramBot == nil {
		return fmt.Errorf("telegram bot is not initialized")
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := rb.telegramBot.GetUpdatesChan(u)

	log.Printf("Bot started. Username: %s", rb.telegramBot.Self.UserName)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		// Handle location messages
		if update.Message.Location != nil {
			go rb.handleLocation(update.Message)
			continue
		}

		// Handle text commands
		if update.Message.IsCommand() {
			switch update.Message.Command() {
			case "start":
				rb.sendWelcomeMessage(update.Message.Chat.ID)
			case "help":
				rb.sendHelpMessage(update.Message.Chat.ID)
			default:
				rb.sendTextMessage(update.Message.Chat.ID, "Unknown command. Use /help to see available commands.")
			}
		} else {
			// Respond to regular text messages
			rb.sendTextMessage(update.Message.Chat.ID, "Please send your location to find nearby restaurants, or use /help for instructions.")
		}
	}

	return nil
}

func (rb *RestaurantBot) handleLocation(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	location := msg.Location

	log.Printf("Received location from user %d: lat=%.6f, lon=%.6f", chatID, location.Latitude, location.Longitude)

	// Check cache first
	if cached, _, found := rb.cache.Get(location.Latitude, location.Longitude); found {
		log.Printf("Cache hit for location %.6f,%.6f", location.Latitude, location.Longitude)
		rb.sendRestaurantsFromCache(chatID, cached, location.Latitude, location.Longitude)
		return
	}

	// Send "searching" message
	rb.sendTextMessage(chatID, "🔍 Searching for nearby restaurants...")

	// Find nearby restaurants (default to all categories for Telegram)
	params := SearchParams{
		Lat:        location.Latitude,
		Lon:        location.Longitude,
		Categories: nil, // all categories
	}
	result, err := rb.findNearbyRestaurantsWithStats(params)
	if err != nil {
		log.Printf("Error finding restaurants: %v", err)
		rb.sendTextMessage(chatID, "❌ Sorry, I couldn't find restaurants at the moment. Please try again later.")
		return
	}

	if len(result.Restaurants) == 0 {
		rb.sendTextMessage(chatID, "😔 No restaurants found nearby. Try sharing a different location.")
		return
	}

	// Cache the results with stats
	rb.cache.Set(location.Latitude, location.Longitude, result.Restaurants, result.Stats)

	// Send results
	rb.sendRestaurantsFromCache(chatID, result.Restaurants, location.Latitude, location.Longitude)
}

// SearchParams holds all search parameters
type SearchParams struct {
	Lat        float64
	Lon        float64
	Categories []FoodCategory // Multiple categories (e.g., ["restaurant", "cafe"])
	Keyword    string         // Cuisine/keyword filter (e.g., "vegan", "italian")
}

func (rb *RestaurantBot) findNearbyRestaurants(lat, lon float64, category FoodCategory) ([]Restaurant, error) {
	// Legacy single-category support
	params := SearchParams{
		Lat:        lat,
		Lon:        lon,
		Categories: []FoodCategory{category},
	}
	if category == "" || category == CategoryAll {
		params.Categories = nil // nil means all
	}
	return rb.findNearbyRestaurantsWithParams(params)
}

func (rb *RestaurantBot) findNearbyRestaurantsWithParams(params SearchParams) ([]Restaurant, error) {
	result, err := rb.findNearbyRestaurantsWithStats(params)
	if err != nil {
		return nil, err
	}
	return result.Restaurants, nil
}

func (rb *RestaurantBot) findNearbyRestaurantsWithStats(params SearchParams) (*SearchResult, error) {
	switch rb.apiProvider {
	case "osm":
		return rb.findNearbyRestaurantsOSMWithStats(params)
	case "both":
		return rb.findNearbyRestaurantsBothWithStats(params)
	case "google":
		fallthrough
	default:
		return rb.findNearbyRestaurantsGoogleWithStats(params)
	}
}

// findNearbyRestaurantsBoth searches both providers in parallel and combines results
func (rb *RestaurantBot) findNearbyRestaurantsBoth(lat, lon float64, category FoodCategory) ([]Restaurant, error) {
	params := SearchParams{Lat: lat, Lon: lon, Categories: []FoodCategory{category}}
	if category == "" || category == CategoryAll {
		params.Categories = nil
	}
	return rb.findNearbyRestaurantsBothWithParams(params)
}

func (rb *RestaurantBot) findNearbyRestaurantsBothWithParams(params SearchParams) ([]Restaurant, error) {
	result, err := rb.findNearbyRestaurantsBothWithStats(params)
	if err != nil {
		return nil, err
	}
	return result.Restaurants, nil
}

func (rb *RestaurantBot) findNearbyRestaurantsBothWithStats(params SearchParams) (*SearchResult, error) {
	type result struct {
		searchResult *SearchResult
		err          error
		source       string
	}

	resultsChan := make(chan result, 2)

	// Search Google Maps in parallel
	go func() {
		if rb.mapsClient == nil {
			resultsChan <- result{searchResult: &SearchResult{Restaurants: []Restaurant{}, Stats: SearchStats{}}, err: nil, source: "google"}
			return
		}
		sr, err := rb.findNearbyRestaurantsGoogleWithStats(params)
		resultsChan <- result{searchResult: sr, err: err, source: "google"}
	}()

	// Search OpenStreetMap in parallel
	go func() {
		sr, err := rb.findNearbyRestaurantsOSMWithStats(params)
		resultsChan <- result{searchResult: sr, err: err, source: "osm"}
	}()

	// Collect results from both providers
	var allRestaurants []Restaurant
	var errors []string
	stats := SearchStats{}

	for i := 0; i < 2; i++ {
		res := <-resultsChan
		if res.err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", res.source, res.err))
			log.Printf("Error from %s: %v", res.source, res.err)
		} else if res.searchResult != nil {
			// Merge stats
			if res.source == "google" {
				stats.GooglePagesSearched = res.searchResult.Stats.GooglePagesSearched
				stats.GoogleSearchQueries = res.searchResult.Stats.GoogleSearchQueries
				stats.GoogleResultsRaw = res.searchResult.Stats.GoogleResultsRaw
				stats.GoogleResultsFiltered = res.searchResult.Stats.GoogleResultsFiltered
			} else {
				stats.OSMResultsTotal = res.searchResult.Stats.OSMResultsTotal
			}
			// Mark each restaurant with its source
			for j := range res.searchResult.Restaurants {
				res.searchResult.Restaurants[j].Name = fmt.Sprintf("[%s] %s", strings.ToUpper(res.source), res.searchResult.Restaurants[j].Name)
			}
			allRestaurants = append(allRestaurants, res.searchResult.Restaurants...)
		}
	}

	// If both failed, return error
	if len(allRestaurants) == 0 && len(errors) > 0 {
		return nil, fmt.Errorf("all providers failed: %s", strings.Join(errors, "; "))
	}

	stats.TotalBeforeDedup = len(allRestaurants)

	// Deduplicate restaurants based on name and location (within 50m)
	deduplicated := deduplicateRestaurants(allRestaurants)

	stats.TotalAfterDedup = len(deduplicated)

	// Sort by rating (highest first), then by distance for same ratings
	sortRestaurantsByRating(deduplicated)

	return &SearchResult{
		Restaurants: deduplicated,
		Stats:       stats,
	}, nil
}

// deduplicateRestaurants removes duplicate restaurants based on name similarity and proximity
func deduplicateRestaurants(restaurants []Restaurant) []Restaurant {
	if len(restaurants) == 0 {
		return restaurants
	}

	seen := make(map[string]bool)
	var unique []Restaurant
	const proximityThreshold = 0.0005 // ~50 meters

	for _, r := range restaurants {
		// Create a key based on normalized name and rounded coordinates
		normalizedName := strings.ToLower(strings.TrimSpace(r.Name))
		// Remove source prefix for deduplication
		normalizedName = strings.TrimPrefix(normalizedName, "[google] ")
		normalizedName = strings.TrimPrefix(normalizedName, "[osm] ")

		// Round coordinates to proximity threshold
		roundedLat := math.Round(r.Latitude/proximityThreshold) * proximityThreshold
		roundedLon := math.Round(r.Longitude/proximityThreshold) * proximityThreshold
		key := fmt.Sprintf("%s_%.6f_%.6f", normalizedName, roundedLat, roundedLon)

		if !seen[key] {
			seen[key] = true
			unique = append(unique, r)
		}
	}

	return unique
}

// sortRestaurantsByDistance sorts restaurants by distance from user location
func sortRestaurantsByDistance(restaurants []Restaurant, userLat, userLon float64) {
	for i := 0; i < len(restaurants)-1; i++ {
		for j := i + 1; j < len(restaurants); j++ {
			if restaurants[i].Distance > restaurants[j].Distance {
				restaurants[i], restaurants[j] = restaurants[j], restaurants[i]
			}
		}
	}
}

// sortRestaurantsByRating sorts restaurants by rating (highest first), then by distance for same ratings
// calculateWeightedScore computes a confidence-weighted rating score
// Uses Bayesian average: considers both rating AND number of reviews
// A 5-star with 1 review will score lower than 4.8-star with 10,000 reviews
func calculateWeightedScore(rating float64, reviewCount int) float64 {
	// Bayesian average formula: (v*R + m*C) / (v + m)
	// Where:
	//   R = restaurant's rating
	//   v = number of reviews
	//   m = minimum reviews needed for "full confidence" (threshold)
	//   C = prior mean (what we assume with no data - neutral rating)
	
	const (
		m = 15.0  // minimum reviews for full confidence
		C = 3.5   // prior/neutral rating (slightly below average)
	)
	
	v := float64(reviewCount)
	R := rating
	
	// Bayesian weighted average
	// With few reviews, score pulls toward C (3.5)
	// With many reviews, score approaches actual rating R
	weightedRating := (v*R + m*C) / (v + m)
	
	return weightedRating
}

func sortRestaurantsByRating(restaurants []Restaurant) {
	// Sort by weighted score (considers both rating AND review count)
	for i := 0; i < len(restaurants)-1; i++ {
		for j := i + 1; j < len(restaurants); j++ {
			scoreI := calculateWeightedScore(restaurants[i].Rating, restaurants[i].ReviewCount)
			scoreJ := calculateWeightedScore(restaurants[j].Rating, restaurants[j].ReviewCount)
			
			// Sort by weighted score (descending)
			if scoreI < scoreJ {
				restaurants[i], restaurants[j] = restaurants[j], restaurants[i]
			} else if scoreI == scoreJ {
				// If scores are equal, sort by distance (ascending)
				if restaurants[i].Distance > restaurants[j].Distance {
					restaurants[i], restaurants[j] = restaurants[j], restaurants[i]
				}
			}
		}
	}
}

func (rb *RestaurantBot) findNearbyRestaurantsGoogle(lat, lon float64, category FoodCategory) ([]Restaurant, error) {
	params := SearchParams{Lat: lat, Lon: lon, Categories: []FoodCategory{category}}
	if category == "" || category == CategoryAll {
		params.Categories = nil
	}
	return rb.findNearbyRestaurantsGoogleWithParams(params)
}

func (rb *RestaurantBot) findNearbyRestaurantsGoogleWithParams(params SearchParams) ([]Restaurant, error) {
	result, err := rb.findNearbyRestaurantsGoogleWithStats(params)
	if err != nil {
		return nil, err
	}
	return result.Restaurants, nil
}

func (rb *RestaurantBot) findNearbyRestaurantsGoogleWithStats(params SearchParams) (*SearchResult, error) {
	// Resolve keyword if it's a known cuisine
	keyword := params.Keyword
	if kw, ok := cuisineKeywords[strings.ToLower(keyword)]; ok {
		keyword = kw
	}

	// Determine which categories to search
	var categoriesToSearch []FoodCategory
	if len(params.Categories) == 0 {
		// Search all food categories
		categoriesToSearch = allFoodCategories
	} else {
		categoriesToSearch = params.Categories
	}

	// If only one category and no keyword, use simple search
	if len(categoriesToSearch) == 1 && keyword == "" {
		placeType, ok := categoryToGoogleType[categoriesToSearch[0]]
		if !ok {
			placeType = maps.PlaceTypeRestaurant
		}
		return rb.findNearbyRestaurantsGoogleByTypeWithStats(params.Lat, params.Lon, placeType, "")
	}

	// Multiple categories or keyword search - search in parallel
	return rb.findNearbyRestaurantsGoogleMultipleWithStats(params.Lat, params.Lon, categoriesToSearch, keyword)
}

// findNearbyRestaurantsGoogleAll searches all food categories in parallel
func (rb *RestaurantBot) findNearbyRestaurantsGoogleAll(lat, lon float64) ([]Restaurant, error) {
	return rb.findNearbyRestaurantsGoogleMultiple(lat, lon, allFoodCategories, "")
}

// findNearbyRestaurantsGoogleMultiple searches multiple categories in parallel with optional keyword
func (rb *RestaurantBot) findNearbyRestaurantsGoogleMultiple(lat, lon float64, categories []FoodCategory, keyword string) ([]Restaurant, error) {
	result, err := rb.findNearbyRestaurantsGoogleMultipleWithStats(lat, lon, categories, keyword)
	if err != nil {
		return nil, err
	}
	return result.Restaurants, nil
}

func (rb *RestaurantBot) findNearbyRestaurantsGoogleMultipleWithStats(lat, lon float64, categories []FoodCategory, keyword string) (*SearchResult, error) {
	type result struct {
		searchResult *SearchResult
		err          error
		source       string
	}

	// Check if we're searching for restaurants (include cuisine keyword searches and text search)
	includesRestaurantSearch := false
	for _, cat := range categories {
		if cat == CategoryRestaurant {
			includesRestaurantSearch = true
			break
		}
	}

	// Calculate total searches: categories + cuisine keyword searches + text searches (if applicable)
	totalSearches := len(categories)
	textSearchQueries := []string{} // Text Search queries to run
	
	// Only do cuisine keyword searches if no specific keyword filter is set
	cuisineSearches := []string{}
	if includesRestaurantSearch && keyword == "" {
		// Search for each cuisine with type='restaurant' + keyword='cuisine'
		cuisineSearches = cuisineKeywordsForSearch
		totalSearches += len(cuisineSearches)
		// Add Text Search queries for comprehensive coverage
		textSearchQueries = append(textSearchQueries, "restaurant", "food")
		totalSearches += len(textSearchQueries)
	} else if includesRestaurantSearch && keyword != "" {
		// If keyword is set, just do a text search with that keyword
		textSearchQueries = append(textSearchQueries, keyword+" restaurant", keyword+" food")
		totalSearches += len(textSearchQueries)
	}

	resultsChan := make(chan result, totalSearches)

	// Search all categories in parallel
	for _, cat := range categories {
		go func(c FoodCategory) {
			placeType := categoryToGoogleType[c]
			sr, err := rb.findNearbyRestaurantsGoogleByTypeWithStats(lat, lon, placeType, keyword)
			resultsChan <- result{searchResult: sr, err: err, source: string(c)}
		}(cat)
	}

	// Search with cuisine keywords (type='restaurant' + keyword='indian', etc.)
	// This is the correct way to find cuisine-specific restaurants
	if len(cuisineSearches) > 0 {
		for _, cuisineKw := range cuisineSearches {
			go func(kw string) {
				sr, err := rb.findNearbyRestaurantsGoogleByTypeWithStats(lat, lon, maps.PlaceTypeRestaurant, kw)
				resultsChan <- result{searchResult: sr, err: err, source: "cuisine:" + kw}
			}(cuisineKw)
		}
	}

	// Also run Text Search for comprehensive coverage
	if len(textSearchQueries) > 0 {
		for _, query := range textSearchQueries {
			go func(q string) {
				sr, err := rb.findNearbyRestaurantsGoogleTextSearchWithStats(lat, lon, q)
				resultsChan <- result{searchResult: sr, err: err, source: "text:" + q}
			}(query)
		}
	}

	// Collect results from all searches
	var allRestaurants []Restaurant
	var errors []string
	stats := SearchStats{
		GoogleSearchQueries: totalSearches,
	}

	log.Printf("[Search] Waiting for %d search results...", totalSearches)

	for i := 0; i < totalSearches; i++ {
		res := <-resultsChan
		if res.err != nil {
			// Log errors but don't fail for cuisine/text searches (they're supplementary)
			if strings.HasPrefix(res.source, "cuisine:") || strings.HasPrefix(res.source, "text:") {
				log.Printf("[Search] Supplementary search error from %s: %v", res.source, res.err)
			} else {
				errors = append(errors, fmt.Sprintf("%s: %v", res.source, res.err))
				log.Printf("[Search] Error from %s: %v", res.source, res.err)
			}
		} else if res.searchResult != nil {
			log.Printf("[Search] Got %d results from %s", len(res.searchResult.Restaurants), res.source)
			// Aggregate stats
			stats.GooglePagesSearched += res.searchResult.Stats.GooglePagesSearched
			stats.GoogleResultsRaw += res.searchResult.Stats.GoogleResultsRaw
			stats.GoogleResultsFiltered += res.searchResult.Stats.GoogleResultsFiltered
			// Check if any result contains "baba"
			for _, r := range res.searchResult.Restaurants {
				if strings.Contains(strings.ToLower(r.Name), "baba") {
					log.Printf("[Search] BABA found in %s results: name='%s', rating=%.1f, reviews=%d, placeID='%s'", 
						res.source, r.Name, r.Rating, r.ReviewCount, r.PlaceID)
				}
			}
			allRestaurants = append(allRestaurants, res.searchResult.Restaurants...)
		}
	}

	stats.TotalBeforeDedup = len(allRestaurants)
	log.Printf("[Search] Total restaurants before dedup: %d", len(allRestaurants))

	// Check for Baba before dedup
	for _, r := range allRestaurants {
		if strings.Contains(strings.ToLower(r.Name), "baba") {
			log.Printf("[Search] BABA before dedup: name='%s', lat=%.6f, lon=%.6f, placeID='%s'", 
				r.Name, r.Latitude, r.Longitude, r.PlaceID)
		}
	}

	// If all failed, return error
	if len(allRestaurants) == 0 && len(errors) > 0 {
		return nil, fmt.Errorf("all category searches failed: %s", strings.Join(errors, "; "))
	}

	// Deduplicate restaurants (same place might appear in multiple categories)
	deduplicated := deduplicateRestaurants(allRestaurants)

	stats.TotalAfterDedup = len(deduplicated)
	log.Printf("[Search] Total restaurants after dedup: %d", len(deduplicated))

	// Check for Baba after dedup
	for _, r := range deduplicated {
		if strings.Contains(strings.ToLower(r.Name), "baba") {
			log.Printf("[Search] BABA after dedup: name='%s', rating=%.1f, reviews=%d", r.Name, r.Rating, r.ReviewCount)
		}
	}

	// Sort by rating (highest first), then by distance for same ratings
	sortRestaurantsByRating(deduplicated)

	return &SearchResult{
		Restaurants: deduplicated,
		Stats:       stats,
	}, nil
}

// findNearbyRestaurantsGoogleTextSearch uses Text Search API for more comprehensive results
// Text Search can find restaurants that NearbySearch might miss
func (rb *RestaurantBot) findNearbyRestaurantsGoogleTextSearch(lat, lon float64, query string) ([]Restaurant, error) {
	result, err := rb.findNearbyRestaurantsGoogleTextSearchWithStats(lat, lon, query)
	if err != nil {
		return nil, err
	}
	return result.Restaurants, nil
}

func (rb *RestaurantBot) findNearbyRestaurantsGoogleTextSearchWithStats(lat, lon float64, query string) (*SearchResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	request := &maps.TextSearchRequest{
		Query: query,
		Location: &maps.LatLng{
			Lat: lat,
			Lng: lon,
		},
		Radius:   2000, // 2km radius
		Language: "en",
	}

	allRestaurants := make([]Restaurant, 0)
	var nextPageToken string
	stats := SearchStats{}

	log.Printf("[TextSearch] Starting search for query='%s' at %.6f,%.6f", query, lat, lon)

	for page := 0; page < 3; page++ { // Maximum 3 pages (60 results)
		if page > 0 {
			request.PageToken = nextPageToken
			time.Sleep(2 * time.Second)
		}

		resp, err := rb.mapsClient.TextSearch(ctx, request)
		if err != nil {
			log.Printf("[TextSearch] Error on page %d for query='%s': %v", page, query, err)
			if page == 0 {
				return nil, fmt.Errorf("text search failed: %w", err)
			}
			break
		}

		stats.GooglePagesSearched++
		stats.GoogleResultsRaw += len(resp.Results)

		log.Printf("[TextSearch] Page %d for query='%s': got %d results", page, query, len(resp.Results))

		// Log ALL raw API results for debugging
		for i, place := range resp.Results {
			log.Printf("[TextSearch][RAW] #%d: name='%s', placeID='%s', types=%v, rating=%.1f, reviews=%d, lat=%.6f, lon=%.6f",
				i+1, place.Name, place.PlaceID, place.Types, place.Rating, place.UserRatingsTotal,
				place.Geometry.Location.Lat, place.Geometry.Location.Lng)
		}

		for _, place := range resp.Results {
			// Log every place we see (for debugging)
			if strings.Contains(strings.ToLower(place.Name), "baba") {
				log.Printf("[TextSearch] FOUND BABA: name='%s', placeID='%s', types=%v", place.Name, place.PlaceID, place.Types)
			}

			if !isFoodRelatedPlace(place.Types) {
				log.Printf("[TextSearch] Filtered out: name='%s', types=%v", place.Name, place.Types)
				continue
			}

			distance := calculateDistance(lat, lon, place.Geometry.Location.Lat, place.Geometry.Location.Lng)

			photoRef := ""
			if len(place.Photos) > 0 {
				photoRef = place.Photos[0].PhotoReference
			}

			reviewCount := 0
			if place.UserRatingsTotal > 0 {
				reviewCount = place.UserRatingsTotal
			}

			// Determine if this restaurant should use generic placeholder image
			// Skip expensive Google Photo API calls for:
			// - Restaurants without photos
			// - Restaurants with rating below 4.0
			// - Restaurants with fewer than 5 reviews
			rating := float64(place.Rating)
			if shouldUseGenericPhoto(photoRef, rating, reviewCount) {
				photoRef = genericPhotoReference
				log.Printf("[TextSearch] Using generic photo for '%s' (rating=%.1f, reviews=%d)", 
					place.Name, rating, reviewCount)
			}

			allRestaurants = append(allRestaurants, Restaurant{
				Name:           place.Name,
				Rating:         rating,
				ReviewCount:    reviewCount,
				PriceLevel:     place.PriceLevel,
				Type:           formatPlaceType(place.Types),
				Latitude:       place.Geometry.Location.Lat,
				Longitude:      place.Geometry.Location.Lng,
				Address:        place.FormattedAddress,
				Distance:       distance,
				PhotoReference: photoRef,
				PlaceID:        place.PlaceID,
			})
		}

		if resp.NextPageToken == "" {
			break
		}
		nextPageToken = resp.NextPageToken
	}

	stats.GoogleResultsFiltered = len(allRestaurants)
	log.Printf("[TextSearch] Completed query='%s': returning %d restaurants", query, len(allRestaurants))
	
	return &SearchResult{
		Restaurants: allRestaurants,
		Stats:       stats,
	}, nil
}

// findNearbyRestaurantsGoogleByType searches for a specific place type with optional keyword
func (rb *RestaurantBot) findNearbyRestaurantsGoogleByType(lat, lon float64, placeType maps.PlaceType, keyword string) ([]Restaurant, error) {
	result, err := rb.findNearbyRestaurantsGoogleByTypeWithStats(lat, lon, placeType, keyword)
	if err != nil {
		return nil, err
	}
	return result.Restaurants, nil
}

func (rb *RestaurantBot) findNearbyRestaurantsGoogleByTypeWithStats(lat, lon float64, placeType maps.PlaceType, keyword string) (*SearchResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Longer timeout for pagination
	defer cancel()

	request := &maps.NearbySearchRequest{
		Location: &maps.LatLng{
			Lat: lat,
			Lng: lon,
		},
		Radius:   2000, // 2km radius
		Type:     placeType,
		Keyword:  keyword, // Optional keyword for cuisine/diet filtering
		Language: "en",
	}

	log.Printf("[NearbySearch] Starting search type='%s' keyword='%s' at %.6f,%.6f", placeType, keyword, lat, lon)

	// Collect all restaurants from all pages (up to 60 results)
	allRestaurants := make([]Restaurant, 0)
	var nextPageToken string
	stats := SearchStats{}

	for page := 0; page < 3; page++ { // Maximum 3 pages (60 results)
		if page > 0 {
			request.PageToken = nextPageToken
			// wait for next_page_token to become active
			time.Sleep(2 * time.Second)
		}

		resp, err := rb.mapsClient.NearbySearch(ctx, request)
		if err != nil {
			log.Printf("[NearbySearch] Error on page %d for type='%s': %v", page, placeType, err)
			if page > 0 && strings.Contains(strings.ToLower(err.Error()), "invalid_request") {
				// next_page_token not ready yet, wait longer and retry same page
				time.Sleep(2 * time.Second)
				page--
				continue
			}
			if page == 0 {
				return nil, fmt.Errorf("nearby search failed: %w", err)
			}
			// If pagination fails, return what we have
			break
		}

		stats.GooglePagesSearched++
		stats.GoogleResultsRaw += len(resp.Results)

		log.Printf("[NearbySearch] Page %d for type='%s': got %d results", page, placeType, len(resp.Results))

		// Log ALL raw API results for debugging
		for i, place := range resp.Results {
			log.Printf("[NearbySearch][RAW] #%d: name='%s', placeID='%s', types=%v, rating=%.1f, reviews=%d, lat=%.6f, lon=%.6f",
				i+1, place.Name, place.PlaceID, place.Types, place.Rating, place.UserRatingsTotal,
				place.Geometry.Location.Lat, place.Geometry.Location.Lng)
		}

		// Convert to unified Restaurant format
		for _, place := range resp.Results {
			// Log every place we see that contains "baba" (for debugging)
			if strings.Contains(strings.ToLower(place.Name), "baba") {
				log.Printf("[NearbySearch] FOUND BABA: name='%s', placeID='%s', types=%v, rating=%.1f, reviews=%d", 
					place.Name, place.PlaceID, place.Types, place.Rating, place.UserRatingsTotal)
			}

			// Last-resort post-filter: skip if no food-related types at all
			if !isFoodRelatedPlace(place.Types) {
				log.Printf("[NearbySearch] Filtered out non-food place: %s (types: %v)", place.Name, place.Types)
				continue
			}

			distance := calculateDistance(lat, lon, place.Geometry.Location.Lat, place.Geometry.Location.Lng)

			// Extract photo reference if available
			photoRef := ""
			if len(place.Photos) > 0 {
				photoRef = place.Photos[0].PhotoReference
			}

			// Extract review count (UserRatingsTotal)
			reviewCount := 0
			if place.UserRatingsTotal > 0 {
				reviewCount = place.UserRatingsTotal
			}

			// Determine if this restaurant should use generic placeholder image
			// Skip expensive Google Photo API calls for:
			// - Restaurants without photos
			// - Restaurants with rating below 4.0
			// - Restaurants with fewer than 5 reviews
			rating := float64(place.Rating)
			if shouldUseGenericPhoto(photoRef, rating, reviewCount) {
				photoRef = genericPhotoReference
				log.Printf("[NearbySearch] Using generic photo for '%s' (rating=%.1f, reviews=%d)", 
					place.Name, rating, reviewCount)
			}

			priceLevel := place.PriceLevel
			placeTypeStr := formatPlaceType(place.Types)

			allRestaurants = append(allRestaurants, Restaurant{
				Name:           place.Name,
				Rating:         rating,
				ReviewCount:    reviewCount,
				PriceLevel:     priceLevel,
				Type:           placeTypeStr,
				Latitude:       place.Geometry.Location.Lat,
				Longitude:      place.Geometry.Location.Lng,
				Address:        place.Vicinity,
				Distance:       distance,
				PhotoReference: photoRef,
				PlaceID:        place.PlaceID,
			})
		}

		// Check if there's a next page
		if resp.NextPageToken == "" {
			break
		}
		nextPageToken = resp.NextPageToken
	}

	stats.GoogleResultsFiltered = len(allRestaurants)

	// Sort by rating (highest first), then by distance for same ratings
	sortRestaurantsByRating(allRestaurants)

	// Return all results (up to 60)
	return &SearchResult{
		Restaurants: allRestaurants,
		Stats:       stats,
	}, nil
}

// isFoodRelatedPlace checks if place has at least one food-related type (last-resort filter)
func isFoodRelatedPlace(types []string) bool {
	for _, t := range types {
		// Check exact match in whitelist
		if validFoodTypes[t] {
			return true
		}
		// Also match any type containing "restaurant", "food", "cafe", "bar", "bakery"
		// This catches new Google types we haven't added to the whitelist yet
		tLower := strings.ToLower(t)
		if strings.Contains(tLower, "restaurant") ||
			strings.Contains(tLower, "food") ||
			strings.Contains(tLower, "cafe") ||
			strings.Contains(tLower, "bar") ||
			strings.Contains(tLower, "bakery") ||
			strings.Contains(tLower, "dining") ||
			strings.Contains(tLower, "eatery") {
			return true
		}
	}
	// If no types provided, assume it's valid (don't filter)
	return len(types) == 0
}

func (rb *RestaurantBot) findNearbyRestaurantsOSM(lat, lon float64, category FoodCategory) ([]Restaurant, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	// Get amenities for this category
	amenities, ok := categoryToOSMAmenities[category]
	if !ok {
		amenities = categoryToOSMAmenities[CategoryAll]
	}

	// Build Overpass API query dynamically based on category
	radius := 2000 // meters
	var queryParts []string
	for _, amenity := range amenities {
		queryParts = append(queryParts,
			fmt.Sprintf(`node["amenity"="%s"](around:%d,%.6f,%.6f);`, amenity, radius, lat, lon),
			fmt.Sprintf(`way["amenity"="%s"](around:%d,%.6f,%.6f);`, amenity, radius, lat, lon),
		)
	}

	query := fmt.Sprintf(`
		[out:json][timeout:10];
		(
		  %s
		);
		out center meta;
	`, strings.Join(queryParts, "\n		  "))

	// Use Overpass API endpoint
	apiURL := "https://overpass-api.de/api/interpreter"
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, strings.NewReader(query))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("overpass API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("overpass API returned status %d", resp.StatusCode)
	}

	var overpassResp struct {
		Elements []struct {
			Type   string  `json:"type"`
			ID     int64   `json:"id"`
			Lat    float64 `json:"lat,omitempty"`
			Lon    float64 `json:"lon,omitempty"`
			Center struct {
				Lat float64 `json:"lat"`
				Lon float64 `json:"lon"`
			} `json:"center,omitempty"`
			Tags map[string]string `json:"tags"`
		} `json:"elements"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&overpassResp); err != nil {
		return nil, fmt.Errorf("failed to decode overpass response: %w", err)
	}

	restaurants := make([]Restaurant, 0)
	maxResults := 10

	for _, elem := range overpassResp.Elements {
		if len(restaurants) >= maxResults {
			break
		}

		var elemLat, elemLon float64
		if elem.Type == "node" {
			elemLat = elem.Lat
			elemLon = elem.Lon
		} else {
			elemLat = elem.Center.Lat
			elemLon = elem.Center.Lon
		}

		name := elem.Tags["name"]
		if name == "" {
			name = elem.Tags["amenity"] // Fallback to amenity type
		}

		restaurantType := formatAmenityType(elem.Tags["amenity"])

		// Calculate distance
		distance := calculateDistance(lat, lon, elemLat, elemLon)

		// Parse rating if available
		rating := 0.0
		if ratingStr, ok := elem.Tags["rating"]; ok {
			if r, err := strconv.ParseFloat(ratingStr, 64); err == nil {
				rating = r
			}
		}

		// Build address from available tags
		addressParts := []string{}
		if addr := elem.Tags["addr:street"]; addr != "" {
			addressParts = append(addressParts, addr)
		}
		if houseNum := elem.Tags["addr:housenumber"]; houseNum != "" {
			addressParts = append(addressParts, houseNum)
		}
		address := strings.Join(addressParts, " ")

		restaurants = append(restaurants, Restaurant{
			Name:      name,
			Rating:    rating,
			Latitude:  elemLat,
			Longitude: elemLon,
			Address:   address,
			Type:      restaurantType,
			Distance:  distance,
		})
	}

	return restaurants, nil
}

// findNearbyRestaurantsOSMWithParams searches OSM with full params support
func (rb *RestaurantBot) findNearbyRestaurantsOSMWithParams(params SearchParams) ([]Restaurant, error) {
	result, err := rb.findNearbyRestaurantsOSMWithStats(params)
	if err != nil {
		return nil, err
	}
	return result.Restaurants, nil
}

func (rb *RestaurantBot) findNearbyRestaurantsOSMWithStats(params SearchParams) (*SearchResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	// Collect all amenities from selected categories
	amenitySet := make(map[string]bool)
	if len(params.Categories) == 0 {
		// All categories
		for _, amenity := range categoryToOSMAmenities[CategoryAll] {
			amenitySet[amenity] = true
		}
	} else {
		for _, cat := range params.Categories {
			if amenities, ok := categoryToOSMAmenities[cat]; ok {
				for _, amenity := range amenities {
					amenitySet[amenity] = true
				}
			}
		}
	}

	// Build Overpass API query dynamically
	radius := 2000 // meters
	var queryParts []string
	for amenity := range amenitySet {
		queryParts = append(queryParts,
			fmt.Sprintf(`node["amenity"="%s"](around:%d,%.6f,%.6f);`, amenity, radius, params.Lat, params.Lon),
			fmt.Sprintf(`way["amenity"="%s"](around:%d,%.6f,%.6f);`, amenity, radius, params.Lat, params.Lon),
		)
	}

	// Add cuisine filter if keyword is provided
	keyword := strings.ToLower(params.Keyword)
	if keyword != "" {
		// Add cuisine-specific queries
		queryParts = append(queryParts,
			fmt.Sprintf(`node["cuisine"~"%s",i](around:%d,%.6f,%.6f);`, keyword, radius, params.Lat, params.Lon),
			fmt.Sprintf(`way["cuisine"~"%s",i](around:%d,%.6f,%.6f);`, keyword, radius, params.Lat, params.Lon),
		)
		// Add diet-specific queries for health keywords
		if keyword == "vegan" || keyword == "vegetarian" || keyword == "halal" || keyword == "kosher" {
			queryParts = append(queryParts,
				fmt.Sprintf(`node["diet:%s"="yes"](around:%d,%.6f,%.6f);`, keyword, radius, params.Lat, params.Lon),
				fmt.Sprintf(`way["diet:%s"="yes"](around:%d,%.6f,%.6f);`, keyword, radius, params.Lat, params.Lon),
			)
		}
	}

	query := fmt.Sprintf(`
		[out:json][timeout:15];
		(
		  %s
		);
		out center meta;
	`, strings.Join(queryParts, "\n		  "))

	// Use Overpass API endpoint
	apiURL := "https://overpass-api.de/api/interpreter"
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, strings.NewReader(query))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("overpass API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("overpass API returned status %d", resp.StatusCode)
	}

	var overpassResp struct {
		Elements []struct {
			Type   string  `json:"type"`
			ID     int64   `json:"id"`
			Lat    float64 `json:"lat,omitempty"`
			Lon    float64 `json:"lon,omitempty"`
			Center struct {
				Lat float64 `json:"lat"`
				Lon float64 `json:"lon"`
			} `json:"center,omitempty"`
			Tags map[string]string `json:"tags"`
		} `json:"elements"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&overpassResp); err != nil {
		return nil, fmt.Errorf("failed to decode overpass response: %w", err)
	}

	stats := SearchStats{
		OSMResultsTotal: len(overpassResp.Elements),
	}

	restaurants := make([]Restaurant, 0)
	keywordLower := strings.ToLower(params.Keyword)

	for _, elem := range overpassResp.Elements {
		var elemLat, elemLon float64
		if elem.Type == "node" {
			elemLat = elem.Lat
			elemLon = elem.Lon
		} else {
			elemLat = elem.Center.Lat
			elemLon = elem.Center.Lon
		}

		// Filter by keyword in name or cuisine if keyword is set
		if keywordLower != "" {
			nameMatch := strings.Contains(strings.ToLower(elem.Tags["name"]), keywordLower)
			cuisineMatch := strings.Contains(strings.ToLower(elem.Tags["cuisine"]), keywordLower)
			dietMatch := elem.Tags["diet:"+keywordLower] == "yes"
			if !nameMatch && !cuisineMatch && !dietMatch {
				continue
			}
		}

		name := elem.Tags["name"]
		if name == "" {
			name = elem.Tags["amenity"]
		}

		restaurantType := formatAmenityType(elem.Tags["amenity"])
		if cuisine := elem.Tags["cuisine"]; cuisine != "" {
			restaurantType = formatTypeString(cuisine)
		}

		distance := calculateDistance(params.Lat, params.Lon, elemLat, elemLon)

		rating := 0.0
		if ratingStr, ok := elem.Tags["rating"]; ok {
			if r, err := strconv.ParseFloat(ratingStr, 64); err == nil {
				rating = r
			}
		}

		addressParts := []string{}
		if addr := elem.Tags["addr:street"]; addr != "" {
			addressParts = append(addressParts, addr)
		}
		if houseNum := elem.Tags["addr:housenumber"]; houseNum != "" {
			addressParts = append(addressParts, houseNum)
		}
		address := strings.Join(addressParts, " ")

		restaurants = append(restaurants, Restaurant{
			Name:      name,
			Rating:    rating,
			Latitude:  elemLat,
			Longitude: elemLon,
			Address:   address,
			Type:      restaurantType,
			Distance:  distance,
		})
	}

	// Sort by rating
	sortRestaurantsByRating(restaurants)

	stats.TotalAfterDedup = len(restaurants)
	stats.TotalBeforeDedup = len(restaurants)

	return &SearchResult{
		Restaurants: restaurants,
		Stats:       stats,
	}, nil
}

func (rb *RestaurantBot) sendRestaurantsFromCache(chatID int64, restaurants []Restaurant, userLat, userLon float64) {
	if len(restaurants) == 0 {
		return
	}

	// Use strings.Builder for better performance
	var builder strings.Builder
	builder.WriteString("🍽️ *Nearby Restaurants:*\n\n")

	for i, restaurant := range restaurants {
		distanceStr := formatDistance(restaurant.Distance)

		// Escape markdown special characters in restaurant name (but keep asterisks for bold)
		escapedName := escapeMarkdownV2(restaurant.Name)

		// Build message with bold formatting
		builder.WriteString(fmt.Sprintf("%d. *%s*\n", i+1, escapedName))

		if restaurant.Rating > 0 {
			builder.WriteString(fmt.Sprintf("   ⭐ Rating: %.1f/5.0\n", restaurant.Rating))
		}

		builder.WriteString(fmt.Sprintf("   📍 Distance: %s\n", distanceStr))

		if len(restaurant.Address) > 0 {
			escapedAddress := escapeMarkdown(restaurant.Address)
			builder.WriteString(fmt.Sprintf("   📌 Address: %s\n", escapedAddress))
		}

		// Add Google Maps link (works for any coordinates)
		mapsURL := fmt.Sprintf("https://www.google.com/maps/search/?api=1&query=%.6f,%.6f",
			restaurant.Latitude, restaurant.Longitude)
		builder.WriteString(fmt.Sprintf("   🔗 [View on Maps](%s)\n", mapsURL))

		builder.WriteString("\n")

		// Check if message is getting too long (Telegram limit is 4096 chars)
		message := builder.String()
		if len(message) > telegramMaxMessageLength-200 { // Leave some buffer
			// Send current message and start a new one
			rb.sendMessage(chatID, message)
			builder.Reset()
			builder.WriteString(fmt.Sprintf("🍽️ *Restaurants (continued):*\n\n"))
		}
	}

	// Send remaining message
	message := builder.String()
	if len(message) > 0 {
		rb.sendMessage(chatID, message)
	}
}

func (rb *RestaurantBot) sendWelcomeMessage(chatID int64) {
	message := `👋 *Welcome to Restaurant Finder Bot!*

I can help you find nearby restaurants based on your location.

📱 *How to use:*
1. Share your location with me (use the 📎 attachment button)
2. I'll find the closest restaurants near you

Use /help for more information.`

	rb.sendMessage(chatID, message)
}

func (rb *RestaurantBot) sendHelpMessage(chatID int64) {
	message := `📖 *Help*

*Commands:*
/start - Start the bot
/help - Show this help message

*How to find restaurants:*
1. Tap the 📎 attachment button in Telegram
2. Select "Location" or "Share my location"
3. Send your location to me
4. I'll find nearby restaurants for you!

*Note:* Make sure location services are enabled on your device.`

	rb.sendMessage(chatID, message)
}

func (rb *RestaurantBot) sendTextMessage(chatID int64, text string) {
	rb.sendMessage(chatID, text)
}

// sendMessage sends a message with markdown formatting and proper error handling
func (rb *RestaurantBot) sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdown
	msg.DisableWebPagePreview = false

	if _, err := rb.telegramBot.Send(msg); err != nil {
		log.Printf("Failed to send message to chat %d: %v", chatID, err)
	}
}

// escapeMarkdownV2 escapes markdown special characters in text that will be wrapped in asterisks
// We escape asterisks in the text itself, but the wrapping asterisks will still work for bold
func escapeMarkdownV2(text string) string {
	// Escape all markdown special characters including asterisks
	// The wrapping asterisks in the caller will still create bold formatting
	replacer := strings.NewReplacer(
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(text)
}

// escapeMarkdown escapes all markdown special characters (for addresses and other text)
func escapeMarkdown(text string) string {
	// Escape all markdown special characters: * _ [ ] ( ) ~ ` > # + - = | { } . !
	replacer := strings.NewReplacer(
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(text)
}

// calculateDistance calculates the distance between two coordinates using Haversine formula
func calculateDistance(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0

	// Convert degrees to radians
	lat1Rad := lat1 * math.Pi / 180.0
	lat2Rad := lat2 * math.Pi / 180.0
	dLatRad := (lat2 - lat1) * math.Pi / 180.0
	dLonRad := (lon2 - lon1) * math.Pi / 180.0

	// Haversine formula: a = sin²(Δlat/2) + cos(lat1) * cos(lat2) * sin²(Δlon/2)
	a := math.Sin(dLatRad/2)*math.Sin(dLatRad/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*
			math.Sin(dLonRad/2)*math.Sin(dLonRad/2)

	// c = 2 * atan2(√a, √(1-a))
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	// Distance = R * c
	return earthRadiusKm * c
}

// formatDistance formats distance in meters or kilometers
func formatDistance(distanceKm float64) string {
	if distanceKm < 1.0 {
		return fmt.Sprintf("%.0f m", distanceKm*1000)
	}
	return fmt.Sprintf("%.2f km", distanceKm)
}

func main() {
	// Get environment variables
	enableTelegramBot := os.Getenv("ENABLE_TELEGRAM_BOT")
	telegramEnabled := enableTelegramBot == "true" || enableTelegramBot == "1"

	googleMapsAPIKey := os.Getenv("GOOGLE_MAPS_API_KEY")
	apiProvider := os.Getenv("API_PROVIDER") // "google", "osm", or "both", defaults to "google"

	var bot *RestaurantBot
	var err error

	if telegramEnabled {
		telegramToken := os.Getenv("TELEGRAM_BOT_TOKEN")
		if telegramToken == "" {
			log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required when ENABLE_TELEGRAM_BOT is true")
		}

		// Create bot
		bot, err = NewRestaurantBot(telegramToken, googleMapsAPIKey, apiProvider)
		if err != nil {
			log.Fatalf("Failed to create bot: %v", err)
		}

		log.Printf("Using API provider: %s", bot.apiProvider)
		switch bot.apiProvider {
		case "osm":
			log.Printf("Using OpenStreetMap (FREE) - no API costs!")
		case "both":
			log.Printf("Using BOTH Google Maps and OpenStreetMap - searching in parallel!")
			if googleMapsAPIKey == "" {
				log.Printf("WARNING: GOOGLE_MAPS_API_KEY not set, only OSM will be used")
			}
		default:
			log.Printf("Using Google Maps API - costs apply per request")
		}
	} else {
		log.Printf("Telegram bot is disabled (set ENABLE_TELEGRAM_BOT=true to enable)")
		// Create a minimal bot instance just for the HTTP server functionality
		bot, err = NewRestaurantBot("", googleMapsAPIKey, apiProvider)
		if err != nil {
			log.Fatalf("Failed to create bot instance: %v", err)
		}
		log.Printf("Using API provider: %s", bot.apiProvider)
	}

	// Start HTTP server for web interface
	go func() {
		http.HandleFunc("/api/restaurants", func(w http.ResponseWriter, r *http.Request) {
			// Enable CORS
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			if r.Method != "GET" && r.Method != "POST" {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			// Get lat/lon/categories/keyword from query params or JSON body
			var params SearchParams
			var err error
			var page, limit int = 1, 20 // Default pagination: page 1, 20 items per page

			if r.Method == "GET" {
				latStr := r.URL.Query().Get("lat")
				lonStr := r.URL.Query().Get("lon")
				categoriesStr := r.URL.Query().Get("categories") // comma-separated: "restaurant,cafe"
				keyword := r.URL.Query().Get("keyword")          // cuisine/diet filter
				pageStr := r.URL.Query().Get("page")             // pagination: page number (1-indexed)
				limitStr := r.URL.Query().Get("limit")           // pagination: items per page
				
				// Legacy support: also check "category" (single)
				if categoriesStr == "" {
					categoriesStr = r.URL.Query().Get("category")
				}
				
				if latStr == "" || lonStr == "" {
					http.Error(w, "lat and lon parameters are required", http.StatusBadRequest)
					return
				}
				params.Lat, err = strconv.ParseFloat(latStr, 64)
				if err != nil {
					http.Error(w, "Invalid lat parameter", http.StatusBadRequest)
					return
				}
				params.Lon, err = strconv.ParseFloat(lonStr, 64)
				if err != nil {
					http.Error(w, "Invalid lon parameter", http.StatusBadRequest)
					return
				}
				
				// Parse pagination parameters
				if pageStr != "" {
					if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
						page = p
					}
				}
				if limitStr != "" {
					if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
						limit = l
					}
				}
				
				// Parse categories
				if categoriesStr != "" && categoriesStr != "all" {
					for _, c := range strings.Split(categoriesStr, ",") {
						c = strings.TrimSpace(c)
						if c != "" {
							params.Categories = append(params.Categories, FoodCategory(c))
						}
					}
				}
				params.Keyword = keyword
			} else {
				var req struct {
					Lat        float64  `json:"lat"`
					Lon        float64  `json:"lon"`
					Categories []string `json:"categories"` // array of categories
					Category   string   `json:"category"`   // legacy single category
					Keyword    string   `json:"keyword"`
					Page       int      `json:"page"`       // pagination: page number
					Limit      int      `json:"limit"`      // pagination: items per page
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "Invalid JSON body", http.StatusBadRequest)
					return
				}
				params.Lat = req.Lat
				params.Lon = req.Lon
				params.Keyword = req.Keyword
				
				// Parse pagination from JSON
				if req.Page > 0 {
					page = req.Page
				}
				if req.Limit > 0 && req.Limit <= 100 {
					limit = req.Limit
				}
				
				// Support both array and single category
				if len(req.Categories) > 0 {
					for _, c := range req.Categories {
						params.Categories = append(params.Categories, FoodCategory(c))
					}
				} else if req.Category != "" && req.Category != "all" {
					params.Categories = []FoodCategory{FoodCategory(req.Category)}
				}
			}

			// Get all restaurants (from cache or fresh search)
			var allRestaurants []Restaurant
			var stats SearchStats

			// Check cache first (only for searches without keyword filter for now)
			if params.Keyword == "" {
				if cached, cachedStats, found := bot.cache.Get(params.Lat, params.Lon); found {
					log.Printf("API Cache hit for location %.6f,%.6f", params.Lat, params.Lon)
					allRestaurants = cached
					stats = *cachedStats
				}
			}

			// If not cached, fetch fresh results
			if allRestaurants == nil {
				result, err := bot.findNearbyRestaurantsWithStats(params)
				if err != nil {
					log.Printf("Error finding restaurants: %v", err)
					http.Error(w, fmt.Sprintf("Error finding restaurants: %v", err), http.StatusInternalServerError)
					return
				}
				allRestaurants = result.Restaurants
				stats = result.Stats

				// Cache the results (only for searches without keyword filter)
				if params.Keyword == "" {
					bot.cache.Set(params.Lat, params.Lon, allRestaurants, stats)
				}
			}

			// Apply pagination
			totalItems := len(allRestaurants)
			totalPages := (totalItems + limit - 1) / limit // Ceiling division
			if totalPages == 0 {
				totalPages = 1
			}
			
			// Clamp page to valid range
			if page > totalPages {
				page = totalPages
			}
			
			// Calculate slice indices
			startIdx := (page - 1) * limit
			endIdx := startIdx + limit
			if endIdx > totalItems {
				endIdx = totalItems
			}
			if startIdx > totalItems {
				startIdx = totalItems
			}
			
			// Get paginated slice
			paginatedRestaurants := allRestaurants[startIdx:endIdx]

			// Build paginated response
			paginatedResult := PaginatedSearchResult{
				Restaurants: paginatedRestaurants,
				Stats:       stats,
				Pagination: Pagination{
					Page:       page,
					Limit:      limit,
					TotalItems: totalItems,
					TotalPages: totalPages,
					HasNext:    page < totalPages,
					HasPrev:    page > 1,
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(paginatedResult)
		})

		// Proxy endpoint for Google Places photos with permanent disk storage
		// Photos are saved indefinitely to avoid repeated API costs
		// Google Places Photo API pricing: $7.00 per 1,000 requests = $0.007 (0.7 cents) per photo
		//
		// IMPORTANT: Photos are stored by place_id (stable restaurant identifier), NOT by photo_reference
		// This ensures we never call the API twice for the same restaurant, even if Google
		// returns different photo_references over time.
		http.HandleFunc("/api/photo", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			photoRef := r.URL.Query().Get("photo_reference")
			placeID := r.URL.Query().Get("place_id")

			// Require place_id for proper caching by restaurant
			if placeID == "" {
				http.Error(w, "place_id parameter is required", http.StatusBadRequest)
				return
			}

			// Handle generic placeholder photo request
			// This is used for restaurants without photos or with low rating/few reviews
			// to avoid unnecessary Google API calls ($0.007 per photo)
			if photoRef == genericPhotoReference || photoRef == "" {
				log.Printf("[PHOTO][GENERIC] Serving generic placeholder image for place_id=%s - $0.00 cost", placeID)
				placeholderData, err := getOrCreateGenericPlaceholder()
				if err != nil {
					log.Printf("[PHOTO][GENERIC][ERROR] Failed to generate placeholder: %v", err)
					http.Error(w, "Failed to generate placeholder image", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set("Cache-Control", "public, max-age=86400")
				w.Header().Set("X-Photo-Source", "generic")
				w.Header().Set("Access-Control-Expose-Headers", "X-Photo-Source")
				w.Write(placeholderData)
				return
			}

			// Use place_id as filename - this is stable and unique per restaurant
			// This ensures we only fetch ONE photo per restaurant, ever
			// Sanitize place_id to be safe for filesystem (remove any path separators)
			safeePlaceID := strings.ReplaceAll(placeID, "/", "_")
			safeePlaceID = strings.ReplaceAll(safeePlaceID, "\\", "_")
			filename := safeePlaceID + ".jpg"
			storedPath := filepath.Join(photoCachePath, filename)

			// Check if photo exists on disk (permanent storage)
			if fileInfo, err := os.Stat(storedPath); err == nil && fileInfo.Size() > 0 {
				// Serve from disk - FREE, no API cost!
				log.Printf("[PHOTO][DISK] Serving from disk: %s (size: %d bytes) - $0.00 cost", filename, fileInfo.Size())
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set("Cache-Control", "public, max-age=86400")
				w.Header().Set("X-Photo-Source", "disk")
				w.Header().Set("Access-Control-Expose-Headers", "X-Photo-Source")
				http.ServeFile(w, r, storedPath)
				return
			}

			// Photo not on disk - need to fetch from Google API
			log.Printf("[PHOTO][API] Photo not found on disk, fetching from Google API: %s", filename)

			if googleMapsAPIKey == "" {
				http.Error(w, "Google Maps API key not configured", http.StatusServiceUnavailable)
				return
			}

			// Fetch photo from Google Places Photo API
			// Cost: $7.00 per 1,000 requests = $0.007 per request
			photoURL := fmt.Sprintf("https://maps.googleapis.com/maps/api/place/photo?maxwidth=400&photoreference=%s&key=%s", photoRef, googleMapsAPIKey)

			resp, err := http.Get(photoURL)
			if err != nil {
				log.Printf("[PHOTO][API][ERROR] Failed to fetch from Google API: %v - saving generic placeholder to prevent future API calls", err)
				// Save generic placeholder so we don't keep trying this photo reference
				saveGenericPlaceholderForFailedPhoto(storedPath, filename)
				serveGenericPlaceholderOnError(w)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				log.Printf("[PHOTO][API][ERROR] Google API returned status %d - saving generic placeholder to prevent future API calls", resp.StatusCode)
				// Save generic placeholder so we don't keep trying this photo reference
				saveGenericPlaceholderForFailedPhoto(storedPath, filename)
				serveGenericPlaceholderOnError(w)
				return
			}

			// Read the photo into memory
			photoData, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Printf("[PHOTO][API][ERROR] Failed to read photo data: %v - saving generic placeholder", err)
				saveGenericPlaceholderForFailedPhoto(storedPath, filename)
				serveGenericPlaceholderOnError(w)
				return
			}

			// Check if we got actual image data (sometimes API returns empty or error HTML)
			if len(photoData) < 1000 {
				log.Printf("[PHOTO][API][ERROR] Photo data too small (%d bytes), likely invalid - saving generic placeholder", len(photoData))
				saveGenericPlaceholderForFailedPhoto(storedPath, filename)
				serveGenericPlaceholderOnError(w)
				return
			}

			log.Printf("[PHOTO][API] Fetched from Google API: %s (size: %d bytes) - cost: $0.007", filename, len(photoData))

			// Save to disk permanently (don't fail request if this doesn't work)
			if err := os.MkdirAll(photoCachePath, 0755); err == nil {
				if err := os.WriteFile(storedPath, photoData, 0644); err != nil {
					log.Printf("[PHOTO][DISK][ERROR] Failed to save photo to disk: %v", err)
				} else {
					log.Printf("[PHOTO][DISK] Saved permanently to disk: %s (size: %d bytes)", filename, len(photoData))
				}
			} else {
				log.Printf("[PHOTO][DISK][ERROR] Failed to create photo storage directory %s: %v", photoCachePath, err)
			}

			// Serve the photo
			contentType := resp.Header.Get("Content-Type")
			if contentType == "" {
				contentType = "image/jpeg"
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Header().Set("X-Photo-Source", "api")
			w.Header().Set("Access-Control-Expose-Headers", "X-Photo-Source")
			w.Write(photoData)
		})

		// Serve index-new.html at hard-to-find URL
		http.HandleFunc("/vwrk4DFEv1RQpl3PxmWSZUeCkSVjAc5kbDqnIIu4DqDYVdNnGiu1xBWIE8IgbJ3X.html", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, "index-new.html")
		})

		// Serve HTML page
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, "index.html")
		})

		port := os.Getenv("HTTP_PORT")
		if port == "" {
			port = "8080"
		}
		log.Printf("HTTP server starting on port %s", port)
		log.Printf("Web interface available at http://localhost:%s", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	// Start bot only if enabled
	if telegramEnabled {
		if err := bot.Start(); err != nil {
			log.Fatalf("Bot error: %v", err)
		}
	} else {
		// Keep the program running for HTTP server
		log.Printf("HTTP server running. Telegram bot disabled.")
		select {} // Block forever to keep HTTP server running
	}
}

// formatPlaceType converts Google place types (e.g., "health_food_store") into readable text.
func formatPlaceType(placeTypes []string) string {
	if len(placeTypes) == 0 {
		return ""
	}
	return formatTypeString(placeTypes[0])
}

// formatAmenityType converts OSM amenity strings into readable text.
func formatAmenityType(amenity string) string {
	if amenity == "" {
		return ""
	}
	return formatTypeString(amenity)
}

func formatTypeString(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	parts := strings.Fields(value)
	for i, part := range parts {
		if len(part) == 0 {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
}
