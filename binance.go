package main

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const binanceAggTradeURL = "wss://stream.binance.com:9443/ws/btcusdt@aggTrade"

// AggTrade represents a Binance aggregate trade event.
type AggTrade struct {
	EventType    string `json:"e"`
	EventTime    int64  `json:"E"`
	Symbol       string `json:"s"`
	AggTradeID   int64  `json:"a"`
	Price        string `json:"p"`
	Quantity     string `json:"q"`
	FirstTradeID int64  `json:"f"`
	LastTradeID  int64  `json:"l"`
	TradeTime    int64  `json:"T"`
	IsBuyerMaker bool   `json:"m"`
	IsBestMatch  bool   `json:"M"` // absorb uppercase "M" to prevent overwriting "m"
}

func (t AggTrade) PriceFloat() (float64, error) { return strconv.ParseFloat(t.Price, 64) }
func (t AggTrade) QtyFloat() (float64, error) { return strconv.ParseFloat(t.Quantity, 64) }

// BinanceFeed connects to the Binance aggTrade WebSocket and calls onTrade for each event.
// The onTrade callback is called from the reader goroutine and must not block.
func BinanceFeed(ctx context.Context, onTrade func(AggTrade)) {
	var mu sync.Mutex
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := connectBinance(ctx, onTrade)
		if err != nil {
			log.Printf("[binance] error: %v", err)
		}

		mu.Lock()
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff = time.Duration(math.Min(float64(backoff)*2, float64(30*time.Second)))
		}
		mu.Unlock()

		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func connectBinance(ctx context.Context, onTrade func(AggTrade)) error {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, binanceAggTradeURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	conn.SetPingHandler(func(appData string) error {
		return conn.WriteMessage(websocket.PongMessage, []byte(appData))
	})

	log.Printf("[binance] connected to %s", binanceAggTradeURL)

	var buyCount, sellCount int
	for {
		select {
		case <-ctx.Done():
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return nil
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var trade AggTrade
		if err := json.Unmarshal(msg, &trade); err != nil {
			continue
		}
		if trade.EventType != "aggTrade" {
			continue
		}
		if trade.IsBuyerMaker {
			sellCount++
		} else {
			buyCount++
		}
		if (buyCount+sellCount)%20 == 0 {
			log.Printf("[binance] stats: buy=%d sell=%d", buyCount, sellCount)
		}
		onTrade(trade)
	}
}
