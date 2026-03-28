package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"net/http"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	ker "github.com/Curton/GoMatchingKernel"
	"github.com/gorilla/websocket"
)

//go:embed static/*
var staticFiles embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	addr := flag.String("addr", ":8088", "HTTP server address")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	ker.SetSaveOrderLog(false)

	engine := ker.NewMatchingEngine(1, "btc_usdt_demo")
	engine.Start()
	defer engine.Stop()

	hub := NewHub()
	go hub.Run()

	stats := NewStats()
	demo := NewDemoEngine(engine, hub, stats)

	// Seed book with initial resting orders
	log.Println("[engine] fetching initial price from Binance...")
	seedPrice := fetchFeedPrice()
	log.Printf("[engine] initial BTC price: %.2f", seedPrice)
	demo.SeedBook(seedPrice)

	// Consume match results (must drain matchedInfoChan)
	go demo.ConsumeMatches()

	// Periodic broadcast: order book + stats every 200ms
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		for range t.C {
			demo.BroadcastOrderBook()
			demo.BroadcastStats()
		}
	}()

	// Replenish book when levels get thin
	go func() {
		t := time.NewTicker(5 * time.Second)
		for range t.C {
			demo.ReplenishBook(seedPrice)
		}
	}()

	// Cleanup old order sources to prevent memory leak
	go func() {
		t := time.NewTicker(5 * time.Minute)
		for range t.C {
			demo.cleanupOrderSources()
		}
	}()

	// Binance feed — decoupled via buffered channel so reader never blocks
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	tradeCh := make(chan AggTrade, 1024)

	// Binance reader: never blocks
	go func() {
		BinanceFeed(ctx, func(trade AggTrade) {
			stats.AddBinance(1)
			demo.BroadcastBinanceTrade(trade)
			select {
			case tradeCh <- trade:
			default:
			}
		})
		log.Println("[binance] feed stopped")
	}()

	// Trade processor: submits 1 taker order per Binance trade
	go func() {
		for trade := range tradeCh {
			demo.FeedTrade(trade)
			price, _ := trade.PriceFloat()
			demo.SetLastPrice(price)
		}
	}()

	// Spread adjustment: when Binance price drifts >x% from spread midpoint, consume the lagging side
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		for range t.C {
			demo.AdjustSpread()
		}
	}()

	// Random order generator: 30s startup delay, then submits orders at random intervals (100ms-1000ms)
	go func() {
		time.Sleep(30 * time.Second)
		for {
			interval := time.Duration(rand.Intn(900)+100) * time.Millisecond
			time.Sleep(interval)
			demo.GenerateRandomOrder()
		}
	}()

	// HTTP server
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFiles.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("[ws] upgrade error:", err)
			return
		}
		client := &Client{conn: conn, send: make(chan []byte, 64)}
		hub.Register(client)

		go func() {
			defer func() {
				hub.Unregister(client)
				conn.Close()
			}()
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					log.Printf("[ws] read error: %v", err)
					return
				}
			}
		}()

		go func() {
			for msg := range client.send {
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			}
		}()
	})

	log.Printf("[server] starting on %s", *addr)
	log.Printf("[server] open http://localhost%s in your browser", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatal(err)
	}
}

func fetchFeedPrice() float64 {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://api.binance.com/api/v3/ticker/price?symbol=BTCUSDT")
	if err != nil {
		log.Printf("[engine] failed to fetch price, using default 66000: %v", err)
		return 66000.0
	}
	defer resp.Body.Close()
	var result struct {
		Price string `json:"price"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 66000.0
	}
	price, _ := strconv.ParseFloat(result.Price, 64)
	return price
}
