<!--
Date: 2026-10-24
Author: Spicer Matthews (spicer@cloudmanic.com)
Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
-->

# apple-store-watcher

Checks Apple Stores for in-store pickup stock every 30 seconds. When a watched
product is in stock, it sends a text through Twilio.

## Setup

1. Copy the config and edit it:

   ```bash
   cp config.example.json config.json
   ```

2. Copy the env file and add the Twilio keys. The keys are in 1Password:
   Autumn / "Twilio Cloudmanic API Keys".

   ```bash
   cp .env.example .env
   ```

`config.json`, `.env` and `watcher.log` are kept out of git.

## Run

```bash
make run caffeine
```

| Command | What it does |
|---|---|
| `make run` | Build and start the watcher |
| `make run caffeine` | Same, and keep the Mac awake so checks do not stop |
| `make caffeine` | Same as `make run caffeine` |
| `make test` | Run the tests |
| `make build` | Build only |
| `make clean` | Remove the built binary |

Press Ctrl-C to stop it. Flags: `-config config.json` and `-env .env`.

Logs go to the screen and to `watcher.log`.

## How texting works

- Each store + product pair that comes in stock is one alert.
- The first text goes out right away. More texts go out every 3 minutes while
  the item stays in stock.
- One text lists every in-stock item. Texts never go out more than once per
  3 minutes.
- Each alert stops after 30 texts. If the item sells out and comes back, it
  starts a new alert.

## Config

| Key | Default | What it does |
|---|---|---|
| `check_interval_seconds` | 30 | Time between stock checks |
| `text_interval_seconds` | 180 | Shortest time between texts |
| `max_texts_per_trigger` | 30 | Texts per alert before it stops |
| `log_file` | `watcher.log` | Where the log file goes |
| `text_to` | (required) | Phone numbers to text, like `+15035551234` |
| `text_link` | (none) | Link added to the end of each text |
| `stores` | (required) | Apple store numbers, like `R077` |
| `products` | (required) | Apple part numbers, like `MK1E4LL/A` |

To find a store number or part number, open the product on apple.com and check
store pickup. The store numbers and part numbers show in the page's network
requests to `/shop/retail/pickup-message`.
