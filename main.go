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
		client := &Client{conn: conn, send: make(chan []byte, 256)}
		hub.Register(client)

		readDone := make(chan struct{})
		writeDone := make(chan struct{})

		// Track pong activity to detect unresponsive clients
		pongReceived := make(chan struct{}, 1)
		pongTimeout := time.NewTicker(60 * time.Second) // Close if no pong for 60s
		defer pongTimeout.Stop()

		// Read goroutine - monitors incoming messages and pings
		go func() {
			defer close(readDone)

			// Reset read deadline initially
			if err := conn.SetReadDeadline(time.Now().Add(90 * time.Second)); err != nil {
				log.Printf("[ws] set read deadline error: %v", err)
				return
			}

			conn.SetPongHandler(func(appData string) error {
				log.Printf("[ws] pong received")
				if err := conn.SetReadDeadline(time.Now().Add(90 * time.Second)); err != nil {
					log.Printf("[ws] pong handler: set read deadline error: %v", err)
					return err
				}
				select {
				case pongReceived <- struct{}{}:
				default:
				}
				return nil
			})

			for {
				select {
				case <-writeDone:
					return
				case <-pongTimeout.C:
					log.Printf("[ws] pong timeout - no response for 60s, closing connection")
					return
				default:
					conn.SetReadDeadline(time.Now().Add(90 * time.Second))
					if _, _, err := conn.ReadMessage(); err != nil {
						log.Printf("[ws] read error: %v", err)
						return
					}
				}
			}
		}()

		// Write goroutine - sends broadcasts and pings, tracks pong responses
		go func() {
			defer func() {
				hub.Unregister(client)
				conn.Close()
				close(writeDone)
			}()

			pingTicker := time.NewTicker(20 * time.Second)
			defer pingTicker.Stop()

			for {
				select {
				case <-readDone:
					return
				case <-pongReceived:
					// Reset pong timeout when we receive a pong
					pongTimeout.Reset(60 * time.Second)
				case msg, ok := <-client.send:
					if !ok {
						return
					}
					conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
						log.Printf("[ws] write error: %v", err)
						return
					}
				case <-pingTicker.C:
					conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
						log.Printf("[ws] ping error: %v", err)
						return
					}
					log.Printf("[ws] ping sent")
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
