# Telegram Restaurant Finder Bot

A Telegram bot written in Go that helps users find nearby restaurants using their location and Google Maps Places API.

## Features

- 📍 Receives user location via Telegram
- 🍽️ Finds nearby restaurants using Google Maps Places API
- 📊 Shows restaurant ratings, distance, and address
- 🔗 Provides direct links to restaurants on Google Maps
- ⚡ Fast and efficient with concurrent request handling

## Prerequisites

- Go 1.21 or higher
- Telegram Bot Token (get it from [@BotFather](https://t.me/botfather))
- Google Maps API Key with Places API enabled

## Setup

### 1. Get Telegram Bot Token

1. Open Telegram and search for [@BotFather](https://t.me/botfather)
2. Send `/newbot` command
3. Follow the instructions to create your bot
4. Copy the bot token you receive

### 2. Get Google Maps API Key

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project or select an existing one
3. Enable the **Places API** for your project
4. Create credentials (API Key)
5. Copy the API key

### 3. Configure Environment Variables

Create a `.env` file in the project root (or export environment variables):

```bash
export TELEGRAM_BOT_TOKEN="your_telegram_bot_token_here"
export GOOGLE_MAPS_API_KEY="your_google_maps_api_key_here"
```

Or create a `.env` file:
```
TELEGRAM_BOT_TOKEN=your_telegram_bot_token_here
GOOGLE_MAPS_API_KEY=your_google_maps_api_key_here
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

## API Limits

- **Google Maps Places API**: Free tier includes $200 credit per month
- **Telegram Bot API**: No rate limits for basic usage

## Notes

- The bot searches for restaurants within a 2km radius
- Results are limited to 10 restaurants
- Make sure your Google Maps API key has Places API enabled
- The bot calculates distances using the Haversine formula

## License

MIT
