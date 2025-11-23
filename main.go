package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"googlemaps.github.io/maps"
)

type RestaurantBot struct {
	telegramBot *tgbotapi.BotAPI
	mapsClient  *maps.Client
}

func NewRestaurantBot(telegramToken string, googleMapsAPIKey string) (*RestaurantBot, error) {
	bot, err := tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	mapsClient, err := maps.NewClient(maps.WithAPIKey(googleMapsAPIKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create maps client: %w", err)
	}

	return &RestaurantBot{
		telegramBot: bot,
		mapsClient:  mapsClient,
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

	// Send "searching" message
	searchMsg := tgbotapi.NewMessage(chatID, "🔍 Searching for nearby restaurants...")
	rb.telegramBot.Send(searchMsg)

	// Find nearby restaurants
	restaurants, err := rb.findNearbyRestaurants(location.Latitude, location.Longitude)
	if err != nil {
		log.Printf("Error finding restaurants: %v", err)
		errorMsg := tgbotapi.NewMessage(chatID, "❌ Sorry, I couldn't find restaurants at the moment. Please try again later.")
		rb.telegramBot.Send(errorMsg)
		return
	}

	if len(restaurants) == 0 {
		noResultsMsg := tgbotapi.NewMessage(chatID, "😔 No restaurants found nearby. Try sharing a different location.")
		rb.telegramBot.Send(noResultsMsg)
		return
	}

	// Send results
	rb.sendRestaurants(chatID, restaurants, location.Latitude, location.Longitude)
}

func (rb *RestaurantBot) findNearbyRestaurants(lat, lon float64) ([]maps.PlacesSearchResult, error) {
	ctx := context.Background()

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
		return nil, err
	}

	// Limit to top 10 results
	maxResults := 10
	if len(resp.Results) > maxResults {
		return resp.Results[:maxResults], nil
	}

	return resp.Results, nil
}

func (rb *RestaurantBot) sendRestaurants(chatID int64, restaurants []maps.PlacesSearchResult, userLat, userLon float64) {
	message := "🍽️ *Nearby Restaurants:*\n\n"

	for i, place := range restaurants {
		// Get distance
		distance := calculateDistance(userLat, userLon, place.Geometry.Location.Lat, place.Geometry.Location.Lng)
		distanceStr := formatDistance(distance)

		// Build message
		message += fmt.Sprintf("%d. *%s*\n", i+1, place.Name)
		
		if place.Rating > 0 {
			message += fmt.Sprintf("   ⭐ Rating: %.1f/5.0\n", place.Rating)
		}
		
		message += fmt.Sprintf("   📍 Distance: %s\n", distanceStr)
		
		if len(place.Vicinity) > 0 {
			message += fmt.Sprintf("   📌 Address: %s\n", place.Vicinity)
		}

		// Add Google Maps link
		mapsURL := fmt.Sprintf("https://www.google.com/maps/search/?api=1&query=%.6f,%.6f&query_place_id=%s",
			place.Geometry.Location.Lat, place.Geometry.Location.Lng, place.PlaceID)
		message += fmt.Sprintf("   🔗 [View on Maps](%s)\n", mapsURL)
		
		message += "\n"
	}

	msg := tgbotapi.NewMessage(chatID, message)
	msg.ParseMode = tgbotapi.ModeMarkdown
	msg.DisableWebPagePreview = false

	rb.telegramBot.Send(msg)
}

func (rb *RestaurantBot) sendWelcomeMessage(chatID int64) {
	message := `👋 *Welcome to Restaurant Finder Bot!*

I can help you find nearby restaurants based on your location.

📱 *How to use:*
1. Share your location with me (use the 📎 attachment button)
2. I'll find the closest restaurants near you

Use /help for more information.`

	msg := tgbotapi.NewMessage(chatID, message)
	msg.ParseMode = tgbotapi.ModeMarkdown
	rb.telegramBot.Send(msg)
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

	msg := tgbotapi.NewMessage(chatID, message)
	msg.ParseMode = tgbotapi.ModeMarkdown
	rb.telegramBot.Send(msg)
}

func (rb *RestaurantBot) sendTextMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	rb.telegramBot.Send(msg)
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
	if googleMapsAPIKey == "" {
		log.Fatal("GOOGLE_MAPS_API_KEY environment variable is required")
	}

	// Create bot
	bot, err := NewRestaurantBot(telegramToken, googleMapsAPIKey)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	// Start bot
	if err := bot.Start(); err != nil {
		log.Fatalf("Bot error: %v", err)
	}
}
