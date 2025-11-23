# Telegram Restaurant Finder Bot

A Telegram bot written in Go that helps users find nearby restaurants using their location. Supports both Google Maps Places API and **free OpenStreetMap API** to minimize costs.

## Features

- 📍 Receives user location via Telegram
- 🍽️ Finds nearby restaurants using Google Maps Places API or OpenStreetMap (FREE)
- 💾 **Smart caching** - Reduces API calls by caching results for 1 hour
- 📊 Shows restaurant ratings, distance, and address
- 🔗 Provides direct links to restaurants on Google Maps
- ⚡ Fast and efficient with concurrent request handling
- 💰 **Cost-optimized** - Use OpenStreetMap for zero API costs!

## Prerequisites

- Go 1.21 or higher
- Telegram Bot Token (get it from [@BotFather](https://t.me/botfather))
- **Optional**: Google Maps API Key with Places API enabled (only needed if using Google Maps API)

## Setup

### 1. Get Telegram Bot Token

1. Open Telegram and search for [@BotFather](https://t.me/botfather)
2. Send `/newbot` command
3. Follow the instructions to create your bot
4. Copy the bot token you receive

### 2. Choose API Provider

You have three options:

#### Option A: OpenStreetMap (FREE - Recommended for cost savings)
- **No API key needed!** OpenStreetMap uses the Overpass API which is completely free and doesn't require authentication
- Completely free, no costs, no API key required
- Good coverage in most areas
- Set `API_PROVIDER=osm` in environment variables
- **Where to get OpenStreetMap API?** You don't need one! It's completely free and uses public endpoints:
  - Overpass API: https://wiki.openstreetmap.org/wiki/Overpass_API
  - Public endpoints: `https://overpass-api.de/api/interpreter` (used by default)
  - No registration, no API key, no limits (reasonable use)

#### Option B: Google Maps API
1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project or select an existing one
3. Enable the **Places API** for your project
4. Create credentials (API Key)
5. Copy the API key
6. Set `API_PROVIDER=google` (or leave unset, defaults to Google)

#### Option C: Both Providers (Parallel Search) ⚡
- **Search both Google Maps and OpenStreetMap simultaneously**
- Combines results from both sources
- Results are marked with `[GOOGLE]` or `[OSM]` prefix
- Automatically deduplicates restaurants found in both sources
- Set `API_PROVIDER=both` in environment variables
- Requires Google Maps API key (OSM doesn't need one)

### 3. Configure Environment Variables

Create a `.env` file in the project root (or export environment variables):

**For OpenStreetMap (FREE - No API key needed!):**
```bash
export TELEGRAM_BOT_TOKEN="your_telegram_bot_token_here"
export API_PROVIDER="osm"
```

**For Google Maps API:**
```bash
export TELEGRAM_BOT_TOKEN="your_telegram_bot_token_here"
export API_PROVIDER="google"
export GOOGLE_MAPS_API_KEY="your_google_maps_api_key_here"
```

**For Both Providers (Parallel Search):**
```bash
export TELEGRAM_BOT_TOKEN="your_telegram_bot_token_here"
export API_PROVIDER="both"
export GOOGLE_MAPS_API_KEY="your_google_maps_api_key_here"
```

Or create a `.env` file:
```
TELEGRAM_BOT_TOKEN=your_telegram_bot_token_here
API_PROVIDER=both  # Options: "google", "osm", or "both"
GOOGLE_MAPS_API_KEY=your_google_maps_api_key_here  # Only needed if API_PROVIDER=google or API_PROVIDER=both
```

### 4. Install Dependencies

```bash
go mod download
```

### 5. Run the Bot

```bash
go run main.go
```

Or build and run:

```bash
go build -o restaurant-bot
./restaurant-bot
```

## Usage

1. Start a conversation with your bot on Telegram
2. Send `/start` to begin
3. Share your location using the 📎 attachment button → Location
4. The bot will find and display nearby restaurants with:
   - Restaurant name
   - Rating (if available)
   - Distance from your location
   - Address
   - Direct link to Google Maps

## Commands

- `/start` - Start the bot and see welcome message
- `/help` - Show help information

## Project Structure

```
.
├── main.go          # Main bot implementation
├── go.mod           # Go module dependencies
├── README.md        # This file
├── .env.example     # Example environment variables
└── .gitignore       # Git ignore file
```

## Cost Optimization Features

### 1. **Smart Caching** 💾
- Results are cached for **1 hour** per location grid (~1km)
- Repeated requests for nearby locations use cached data
- **Potential savings: 50-90%** reduction in API calls

### 2. **OpenStreetMap Support** 🆓
- Use `API_PROVIDER=osm` for **zero API costs**
- **No API key needed!** OpenStreetMap uses public Overpass API endpoints
- OpenStreetMap data is completely free
- Good coverage in most urban areas
- **Where to get OpenStreetMap API key?** You don't need one! It's free and uses public endpoints

### 3. **Parallel Search (Both Providers)** ⚡
- Use `API_PROVIDER=both` to search Google Maps and OpenStreetMap simultaneously
- Combines results from both sources for maximum coverage
- Results are marked with `[GOOGLE]` or `[OSM]` prefix
- Automatically deduplicates restaurants found in both sources
- Sorted by distance from user location

### 4. **Cost Comparison**

| Provider | Cost per Request | Free Tier | API Key Required | Best For |
|----------|-----------------|-----------|------------------|----------|
| **OpenStreetMap** | **$0.00** | Unlimited | **No** | Cost-conscious users |
| Google Maps | $0.032 | $200/month (~6,250 requests) | Yes | Better data quality |
| **Both (Parallel)** | $0.032* | $200/month (Google only) | Google only | Maximum coverage |

*When using "both", only Google Maps API calls cost money. OSM is always free.

**Example Monthly Costs:**
- 1,000 requests: OSM = **$0**, Google = **$32**, Both = **$32** (only Google costs)
- 5,000 requests: OSM = **$0**, Google = **$160** (within free tier), Both = **$160**
- 10,000 requests: OSM = **$0**, Google = **$320** ($200 free + $120), Both = **$320**

**OpenStreetMap API Key Information:**
- **You don't need an API key!** OpenStreetMap is completely free
- Uses public Overpass API endpoints: `https://overpass-api.de/api/interpreter`
- No registration, no authentication, no limits (reasonable use)
- More info: https://wiki.openstreetmap.org/wiki/Overpass_API

## API Limits

- **OpenStreetMap Overpass API**: Free, no rate limits (reasonable use)
- **Google Maps Places API**: Free tier includes $200 credit per month (~6,250 requests)
- **Telegram Bot API**: No rate limits for basic usage

## Notes

- The bot searches for restaurants within a 2km radius
- Results are limited to 10 restaurants
- Cache TTL is 1 hour (configurable in code)
- The bot calculates distances using the Haversine formula
- OpenStreetMap may have less complete data than Google Maps in some areas

## License

MIT
