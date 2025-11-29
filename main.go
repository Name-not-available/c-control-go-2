package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	Name      string  `json:"Name"`
	Rating    float64 `json:"Rating"`
	Latitude  float64 `json:"Latitude"`
	Longitude float64 `json:"Longitude"`
	Address   string  `json:"Address"`
	Distance  float64 `json:"Distance"`
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
	bot, err := tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
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

	// Find nearby restaurants
	restaurants, err := rb.findNearbyRestaurants(location.Latitude, location.Longitude)
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

func (rb *RestaurantBot) findNearbyRestaurants(lat, lon float64) ([]Restaurant, error) {
	switch rb.apiProvider {
	case "osm":
		return rb.findNearbyRestaurantsOSM(lat, lon)
	case "both":
		return rb.findNearbyRestaurantsBoth(lat, lon)
	case "google":
		fallthrough
	default:
		return rb.findNearbyRestaurantsGoogle(lat, lon)
	}
}

// findNearbyRestaurantsBoth searches both providers in parallel and combines results
func (rb *RestaurantBot) findNearbyRestaurantsBoth(lat, lon float64) ([]Restaurant, error) {
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
		restaurants, err := rb.findNearbyRestaurantsGoogle(lat, lon)
		resultsChan <- result{restaurants: restaurants, err: err, source: "google"}
	}()

	// Search OpenStreetMap in parallel
	go func() {
		restaurants, err := rb.findNearbyRestaurantsOSM(lat, lon)
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

	// Sort by distance
	sortRestaurantsByDistance(deduplicated, lat, lon)

	// Limit to top 10 results
	maxResults := 10
	if len(deduplicated) > maxResults {
		deduplicated = deduplicated[:maxResults]
	}

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

func (rb *RestaurantBot) findNearbyRestaurantsGoogle(lat, lon float64) ([]Restaurant, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	request := &maps.NearbySearchRequest{
		Location: &maps.LatLng{
			Lat: lat,
			Lng: lon,
		},
		Radius:   2000, // 2km radius
		Type:     maps.PlaceTypeRestaurant,
		Language: "en",
	}

	resp, err := rb.mapsClient.NearbySearch(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("nearby search failed: %w", err)
	}

	// Convert to unified Restaurant format
	restaurants := make([]Restaurant, 0, len(resp.Results))
	maxResults := 10
	for i, place := range resp.Results {
		if i >= maxResults {
			break
		}
		distance := calculateDistance(lat, lon, place.Geometry.Location.Lat, place.Geometry.Location.Lng)
		restaurants = append(restaurants, Restaurant{
			Name:      place.Name,
			Rating:    float64(place.Rating),
			Latitude:  place.Geometry.Location.Lat,
			Longitude: place.Geometry.Location.Lng,
			Address:   place.Vicinity,
			Distance:  distance,
		})
	}

	return restaurants, nil
}

func (rb *RestaurantBot) findNearbyRestaurantsOSM(lat, lon float64) ([]Restaurant, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	// Overpass API query to find restaurants within 2km
	// Using Overpass Turbo API (free, no API key needed)
	radius := 2000 // meters
	query := fmt.Sprintf(`
		[out:json][timeout:10];
		(
		  node["amenity"="restaurant"](around:%d,%.6f,%.6f);
		  node["amenity"="fast_food"](around:%d,%.6f,%.6f);
		  node["amenity"="cafe"](around:%d,%.6f,%.6f);
		  way["amenity"="restaurant"](around:%d,%.6f,%.6f);
		  way["amenity"="fast_food"](around:%d,%.6f,%.6f);
		  way["amenity"="cafe"](around:%d,%.6f,%.6f);
		);
		out center meta;
	`, radius, lat, lon, radius, lat, lon, radius, lat, lon, radius, lat, lon, radius, lat, lon, radius, lat, lon)

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
			Type   string            `json:"type"`
			ID     int64             `json:"id"`
			Lat    float64           `json:"lat,omitempty"`
			Lon    float64           `json:"lon,omitempty"`
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
			Distance:  distance,
		})
	}

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
	telegramToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if telegramToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	googleMapsAPIKey := os.Getenv("GOOGLE_MAPS_API_KEY")
	apiProvider := os.Getenv("API_PROVIDER") // "google", "osm", or "both", defaults to "google"

	// Create bot
	bot, err := NewRestaurantBot(telegramToken, googleMapsAPIKey, apiProvider)
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

			// Get lat/lon from query params or JSON body
			var lat, lon float64
			var err error

			if r.Method == "GET" {
				latStr := r.URL.Query().Get("lat")
				lonStr := r.URL.Query().Get("lon")
				if latStr == "" || lonStr == "" {
					http.Error(w, "lat and lon parameters are required", http.StatusBadRequest)
					return
				}
				lat, err = strconv.ParseFloat(latStr, 64)
				if err != nil {
					http.Error(w, "Invalid lat parameter", http.StatusBadRequest)
					return
				}
				lon, err = strconv.ParseFloat(lonStr, 64)
				if err != nil {
					http.Error(w, "Invalid lon parameter", http.StatusBadRequest)
					return
				}
			} else {
				var req struct {
					Lat float64 `json:"lat"`
					Lon float64 `json:"lon"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "Invalid JSON body", http.StatusBadRequest)
					return
				}
				lat = req.Lat
				lon = req.Lon
			}

			// Check cache first
			var restaurants []Restaurant
			if cached, found := bot.cache.Get(lat, lon); found {
				log.Printf("Cache hit for location %.6f,%.6f", lat, lon)
				restaurants = cached
			} else {
				// Find restaurants
				restaurants, err = bot.findNearbyRestaurants(lat, lon)
				if err != nil {
					log.Printf("Error finding restaurants: %v", err)
					http.Error(w, fmt.Sprintf("Error finding restaurants: %v", err), http.StatusInternalServerError)
					return
				}
				// Cache the results
				if len(restaurants) > 0 {
					bot.cache.Set(lat, lon, restaurants)
				}
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(restaurants)
		})

		// Serve index-new.html
		http.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, "index-new.html")
		})
		http.HandleFunc("/index-new.html", func(w http.ResponseWriter, r *http.Request) {
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

	// Start bot
	if err := bot.Start(); err != nil {
		log.Fatalf("Bot error: %v", err)
	}
}
