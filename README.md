# GoMatchingDemo

> **This is the live demonstration project for [GoMatchingKernel](https://github.com/Curton/GoMatchingKernel)** — a high-performance, price-time priority matching engine written in Go.
>
> GoMatchingDemo wires the kernel to a real Binance BTC/USDT feed and streams the resulting order book, matched trades, and statistics to a browser dashboard via WebSocket. Its purpose is to showcase the matching engine's capabilities with real market data.

**[GoMatchingKernel](https://github.com/Curton/GoMatchingKernel) features demonstrated by this project:**

- Submitting limit orders (GTC) and receiving match results via `SubmitOrder` / `MatchedInfoChan`
- Querying the live order book with `OrderBook()`, `BestAsk()`, `BestBid()`, `AskLength()`, `BidLength()`
- Engine lifecycle management (`NewMatchingEngine`, `Start`, `Stop`)

## Features

- **Live Binance feed** — real-time BTC/USDT trades from Binance's public WebSocket
- **Order book matching** — each Binance trade is submitted as a taker order into a price-time priority matching engine
- **Real-time dashboard** — dark-themed UI with order book depth, trade tape, Binance feed, and statistics
- **Auto-seeded book** — initial resting orders placed around the current market price; book replenishes when levels thin out
- **Spread adjustment** — keeps the local spread tracking Binance price with drift correction
- **Random order generation** — simulates additional market activity with random limit orders

## Quick Start

```bash
# Clone (requires GoMatchingKernel as a sibling directory)
git clone https://github.com/Curton/GoMatchingDemo.git
git clone https://github.com/Curton/GoMatchingKernel.git

# Build and run (logs to ./log.txt)
cd GoMatchingDemo
./start.sh
```

Open [http://localhost:8088](http://localhost:8088) in your browser.

### View Live Logs

```bash
tail -f ./log.txt
```

### Custom Port

```bash
go build -o GoMatchingDemo . && ./GoMatchingDemo -addr :9090
```

## Architecture

```
main.go            — Entry point: HTTP server, WebSocket upgrade, goroutine orchestration
engine.go          — DemoEngine: wraps GoMatchingKernel, converts Binance trades to kernel orders
hub.go             — WebSocket hub: manages browser clients, broadcasts messages
stats.go           — Atomic counters for trade statistics
binance.go         — BinanceFeed: connects to Binance aggTrade WebSocket with auto-reconnect
static/index.html  — Embedded dashboard UI (served at /, WebSocket at /ws)
```

### Data Flow

```
Binance WebSocket ──> BinanceFeed ──> DemoEngine.FeedTrade()
                                            │
                                    GoMatchingKernel
                                            │
                                    ConsumeMatches() ──> trade events
                                            │
                              ┌──────────────┤
                              ▼              ▼
                     BroadcastOrderBook  BroadcastStats
                              │              │
                              ▼              ▼
                            Hub ──────────> Browser (WebSocket)
```

### WebSocket Message Types

| Type | Description |
|------|-------------|
| `orderbook` | Full order book snapshot (asks/bids arrays) |
| `trade` | Matched trade (side, price, size, source, timestamp) |
| `binance` | Raw Binance aggTrade event |
| `stats` | Statistics counters, best bid/ask, spread, memory usage |

### Order Sources

Each matched trade is tagged with its origin:

| Source | Description |
|--------|-------------|
| `SEED` | Initial resting orders placed at startup |
| `BINANCE_FEED` | Taker orders derived from Binance aggTrade events |
| `SPREAD_ADJ` | Spread adjustment orders to track Binance price |
| `REPLENISH` | New resting orders added when book levels thin out |
| `RANDOM` | Randomly generated limit orders for simulation |

## Dependencies

- [GoMatchingKernel](https://github.com/Curton/GoMatchingKernel) — price-time priority matching engine
- [gorilla/websocket](https://github.com/gorilla/websocket) — WebSocket implementation

## License

MIT
