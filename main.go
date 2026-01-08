package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
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
	cacheTTL                 = 1 * time.Hour // Cache results for 1 hour
	cacheGridSize            = 0.01          // ~1km grid for caching (0.01 degrees)
)

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
var validFoodTypes = map[string]bool{
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
	items map[string]cacheItem
}

type cacheItem struct {
	restaurants []Restaurant
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

// NewLocationCache creates a new location cache
func NewLocationCache() *LocationCache {
	cache := &LocationCache{
		items: make(map[string]cacheItem),
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
		for key, item := range lc.items {
			if now.After(item.expiresAt) {
				delete(lc.items, key)
			}
		}
		lc.mu.Unlock()
	}
}

// getCacheKey generates a cache key based on location (rounded to grid)
func getCacheKey(lat, lon float64) string {
	// Round to grid to cache nearby locations together
	gridLat := math.Round(lat/cacheGridSize) * cacheGridSize
	gridLon := math.Round(lon/cacheGridSize) * cacheGridSize
	return fmt.Sprintf("%.4f,%.4f", gridLat, gridLon)
}

// Get retrieves cached restaurants for a location
func (lc *LocationCache) Get(lat, lon float64) ([]Restaurant, bool) {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	key := getCacheKey(lat, lon)
	item, exists := lc.items[key]
	if !exists || time.Now().After(item.expiresAt) {
		return nil, false
	}
	return item.restaurants, true
}

// Set stores restaurants in cache
func (lc *LocationCache) Set(lat, lon float64, restaurants []Restaurant) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	key := getCacheKey(lat, lon)
	lc.items[key] = cacheItem{
		restaurants: restaurants,
		expiresAt:   time.Now().Add(cacheTTL),
	}
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
	if cached, found := rb.cache.Get(location.Latitude, location.Longitude); found {
		log.Printf("Cache hit for location %.6f,%.6f", location.Latitude, location.Longitude)
		rb.sendRestaurantsFromCache(chatID, cached, location.Latitude, location.Longitude)
		return
	}

	// Send "searching" message
	rb.sendTextMessage(chatID, "🔍 Searching for nearby restaurants...")

	// Find nearby restaurants (default to all categories for Telegram)
	restaurants, err := rb.findNearbyRestaurants(location.Latitude, location.Longitude, CategoryAll)
	if err != nil {
		log.Printf("Error finding restaurants: %v", err)
		rb.sendTextMessage(chatID, "❌ Sorry, I couldn't find restaurants at the moment. Please try again later.")
		return
	}

	if len(restaurants) == 0 {
		rb.sendTextMessage(chatID, "😔 No restaurants found nearby. Try sharing a different location.")
		return
	}

	// Cache the results
	rb.cache.Set(location.Latitude, location.Longitude, restaurants)

	// Send results
	rb.sendRestaurantsFromCache(chatID, restaurants, location.Latitude, location.Longitude)
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
	switch rb.apiProvider {
	case "osm":
		return rb.findNearbyRestaurantsOSMWithParams(params)
	case "both":
		return rb.findNearbyRestaurantsBothWithParams(params)
	case "google":
		fallthrough
	default:
		return rb.findNearbyRestaurantsGoogleWithParams(params)
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
	type result struct {
		restaurants []Restaurant
		err         error
		source      string
	}

	resultsChan := make(chan result, 2)

	// Search Google Maps in parallel
	go func() {
		if rb.mapsClient == nil {
			resultsChan <- result{restaurants: []Restaurant{}, err: nil, source: "google"}
			return
		}
		restaurants, err := rb.findNearbyRestaurantsGoogleWithParams(params)
		resultsChan <- result{restaurants: restaurants, err: err, source: "google"}
	}()

	// Search OpenStreetMap in parallel
	go func() {
		restaurants, err := rb.findNearbyRestaurantsOSMWithParams(params)
		resultsChan <- result{restaurants: restaurants, err: err, source: "osm"}
	}()

	// Collect results from both providers
	var allRestaurants []Restaurant
	var errors []string

	for i := 0; i < 2; i++ {
		res := <-resultsChan
		if res.err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", res.source, res.err))
			log.Printf("Error from %s: %v", res.source, res.err)
		} else {
			// Mark each restaurant with its source
			for j := range res.restaurants {
				res.restaurants[j].Name = fmt.Sprintf("[%s] %s", strings.ToUpper(res.source), res.restaurants[j].Name)
			}
			allRestaurants = append(allRestaurants, res.restaurants...)
		}
	}

	// If both failed, return error
	if len(allRestaurants) == 0 && len(errors) > 0 {
		return nil, fmt.Errorf("all providers failed: %s", strings.Join(errors, "; "))
	}

	// Deduplicate restaurants based on name and location (within 50m)
	deduplicated := deduplicateRestaurants(allRestaurants)

	// Sort by rating (highest first), then by distance for same ratings
	sortRestaurantsByRating(deduplicated)

	// Return all results (no limit)
	return deduplicated, nil
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
		return rb.findNearbyRestaurantsGoogleByType(params.Lat, params.Lon, placeType, "")
	}

	// Multiple categories or keyword search - search in parallel
	return rb.findNearbyRestaurantsGoogleMultiple(params.Lat, params.Lon, categoriesToSearch, keyword)
}

// findNearbyRestaurantsGoogleAll searches all food categories in parallel
func (rb *RestaurantBot) findNearbyRestaurantsGoogleAll(lat, lon float64) ([]Restaurant, error) {
	return rb.findNearbyRestaurantsGoogleMultiple(lat, lon, allFoodCategories, "")
}

// findNearbyRestaurantsGoogleMultiple searches multiple categories in parallel with optional keyword
func (rb *RestaurantBot) findNearbyRestaurantsGoogleMultiple(lat, lon float64, categories []FoodCategory, keyword string) ([]Restaurant, error) {
	type result struct {
		restaurants []Restaurant
		err         error
		category    FoodCategory
	}

	resultsChan := make(chan result, len(categories))

	// Search all categories in parallel
	for _, cat := range categories {
		go func(c FoodCategory) {
			placeType := categoryToGoogleType[c]
			restaurants, err := rb.findNearbyRestaurantsGoogleByType(lat, lon, placeType, keyword)
			resultsChan <- result{restaurants: restaurants, err: err, category: c}
		}(cat)
	}

	// Collect results from all categories
	var allRestaurants []Restaurant
	var errors []string

	for i := 0; i < len(categories); i++ {
		res := <-resultsChan
		if res.err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", res.category, res.err))
			log.Printf("Error searching %s: %v", res.category, res.err)
		} else {
			allRestaurants = append(allRestaurants, res.restaurants...)
		}
	}

	// If all failed, return error
	if len(allRestaurants) == 0 && len(errors) > 0 {
		return nil, fmt.Errorf("all category searches failed: %s", strings.Join(errors, "; "))
	}

	// Deduplicate restaurants (same place might appear in multiple categories)
	deduplicated := deduplicateRestaurants(allRestaurants)

	// Sort by rating (highest first), then by distance for same ratings
	sortRestaurantsByRating(deduplicated)

	return deduplicated, nil
}

// findNearbyRestaurantsGoogleByType searches for a specific place type with optional keyword
func (rb *RestaurantBot) findNearbyRestaurantsGoogleByType(lat, lon float64, placeType maps.PlaceType, keyword string) ([]Restaurant, error) {
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

	// Collect all restaurants from all pages (up to 60 results)
	allRestaurants := make([]Restaurant, 0)
	var nextPageToken string

	for page := 0; page < 3; page++ { // Maximum 3 pages (60 results)
		if page > 0 {
			request.PageToken = nextPageToken
			// wait for next_page_token to become active
			time.Sleep(2 * time.Second)
		}

		resp, err := rb.mapsClient.NearbySearch(ctx, request)
		if err != nil {
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

		// Convert to unified Restaurant format
		for _, place := range resp.Results {
			// Last-resort post-filter: skip if no food-related types at all
			if !isFoodRelatedPlace(place.Types) {
				log.Printf("Filtered out non-food place: %s (types: %v)", place.Name, place.Types)
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

			priceLevel := place.PriceLevel
			placeTypeStr := formatPlaceType(place.Types)

			allRestaurants = append(allRestaurants, Restaurant{
				Name:           place.Name,
				Rating:         float64(place.Rating),
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

	// Sort by rating (highest first), then by distance for same ratings
	sortRestaurantsByRating(allRestaurants)

	// Return all results (up to 60)
	return allRestaurants, nil
}

// isFoodRelatedPlace checks if place has at least one food-related type (last-resort filter)
func isFoodRelatedPlace(types []string) bool {
	for _, t := range types {
		if validFoodTypes[t] {
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

	return restaurants, nil
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

			if r.Method == "GET" {
				latStr := r.URL.Query().Get("lat")
				lonStr := r.URL.Query().Get("lon")
				categoriesStr := r.URL.Query().Get("categories") // comma-separated: "restaurant,cafe"
				keyword := r.URL.Query().Get("keyword")          // cuisine/diet filter
				
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
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "Invalid JSON body", http.StatusBadRequest)
					return
				}
				params.Lat = req.Lat
				params.Lon = req.Lon
				params.Keyword = req.Keyword
				
				// Support both array and single category
				if len(req.Categories) > 0 {
					for _, c := range req.Categories {
						params.Categories = append(params.Categories, FoodCategory(c))
					}
				} else if req.Category != "" && req.Category != "all" {
					params.Categories = []FoodCategory{FoodCategory(req.Category)}
				}
			}

			// Find restaurants
			restaurants, err := bot.findNearbyRestaurantsWithParams(params)
			if err != nil {
				log.Printf("Error finding restaurants: %v", err)
				http.Error(w, fmt.Sprintf("Error finding restaurants: %v", err), http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(restaurants)
		})

		// Proxy endpoint for Google Places photos
		http.HandleFunc("/api/photo", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			photoRef := r.URL.Query().Get("photo_reference")
			if photoRef == "" {
				http.Error(w, "photo_reference parameter is required", http.StatusBadRequest)
				return
			}

			if googleMapsAPIKey == "" {
				http.Error(w, "Google Maps API key not configured", http.StatusServiceUnavailable)
				return
			}

			// Fetch photo from Google Places Photo API
			photoURL := fmt.Sprintf("https://maps.googleapis.com/maps/api/place/photo?maxwidth=400&photoreference=%s&key=%s", photoRef, googleMapsAPIKey)

			resp, err := http.Get(photoURL)
			if err != nil {
				http.Error(w, fmt.Sprintf("Failed to fetch photo: %v", err), http.StatusInternalServerError)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				http.Error(w, "Failed to fetch photo", resp.StatusCode)
				return
			}

			// Copy headers
			w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 1 day

			// Stream the image
			_, err = io.Copy(w, resp.Body)
			if err != nil {
				log.Printf("Error streaming photo: %v", err)
			}
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
