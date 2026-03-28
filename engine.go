package main

import (
	"encoding/json"
	"log"
	"math"
	"math/rand"
	"sync"
	"time"

	ker "github.com/Curton/GoMatchingKernel"
	"github.com/Curton/GoMatchingKernel/types"
)

// DemoEngine wraps the matching engine and handles trade conversion.
type DemoEngine struct {
	engine      *ker.MatchingEngine
	hub         *Hub
	stats       *Stats
	idSeq       uint64
	lastPrice   float64
	lastPriceMu sync.RWMutex
	// orderSource tracks the origin of recent taker orders for display
	// value is the source string + timestamp for TTL-based cleanup
	orderSource   map[uint64]sourceEntry
	orderSourceMu sync.RWMutex
	// feedPriceWindow tracks the last N Binance feed prices for rolling average
	feedPriceWindow []float64
	feedPriceMu     sync.Mutex
}

type sourceEntry struct {
	source  string
	addedAt int64 // unix timestamp in milliseconds
}

const sourceTTL = 10 * 60 * 1000 // 10 minutes in milliseconds

// NewDemoEngine creates a new DemoEngine.
func NewDemoEngine(engine *ker.MatchingEngine, hub *Hub, stats *Stats) *DemoEngine {
	return &DemoEngine{engine: engine, hub: hub, stats: stats}
}

// SetLastPrice updates the last seen Binance trade price.
func (d *DemoEngine) SetLastPrice(price float64) {
	d.lastPriceMu.Lock()
	d.lastPrice = price
	d.lastPriceMu.Unlock()
	d.addFeedPrice(price)
}

// GetLastPrice returns the last seen Binance trade price.
func (d *DemoEngine) GetLastPrice() float64 {
	d.lastPriceMu.RLock()
	defer d.lastPriceMu.RUnlock()
	return d.lastPrice
}

const feedWindowSize = 10

func (d *DemoEngine) addFeedPrice(price float64) {
	d.feedPriceMu.Lock()
	d.feedPriceWindow = append(d.feedPriceWindow, price)
	if len(d.feedPriceWindow) > feedWindowSize {
		d.feedPriceWindow = d.feedPriceWindow[len(d.feedPriceWindow)-feedWindowSize:]
	}
	d.feedPriceMu.Unlock()
}

// AvgFeedPrice returns the rolling average of the last N Binance feed prices.
func (d *DemoEngine) AvgFeedPrice() float64 {
	d.feedPriceMu.Lock()
	defer d.feedPriceMu.Unlock()
	if len(d.feedPriceWindow) == 0 {
		return 0
	}
	var sum float64
	for _, p := range d.feedPriceWindow {
		sum += p
	}
	return sum / float64(len(d.feedPriceWindow))
}

func (d *DemoEngine) setOrderSource(orderID uint64, source string) {
	d.orderSourceMu.Lock()
	defer d.orderSourceMu.Unlock()
	if d.orderSource == nil {
		d.orderSource = make(map[uint64]sourceEntry)
	}
	d.orderSource[orderID] = sourceEntry{source: source, addedAt: time.Now().UnixMilli()}
}

func (d *DemoEngine) getOrderSource(orderID uint64) string {
	d.orderSourceMu.RLock()
	defer d.orderSourceMu.RUnlock()
	if d.orderSource == nil {
		return "UNKNOWN"
	}
	if entry, ok := d.orderSource[orderID]; ok {
		return entry.source
	}
	return "UNKNOWN"
}

// cleanupOrderSources periodically removes old entries from orderSource map.
// This prevents memory leaks from orders that have been fully matched or cancelled.
func (d *DemoEngine) cleanupOrderSources() {
	d.orderSourceMu.Lock()
	defer d.orderSourceMu.Unlock()
	if d.orderSource == nil {
		return
	}
	now := time.Now().UnixMilli()
	before := len(d.orderSource)
	for id, entry := range d.orderSource {
		if now-entry.addedAt > sourceTTL {
			delete(d.orderSource, id)
		}
	}
	if len(d.orderSource) != before {
		log.Printf("[source] cleanup: removed %d entries, %d remaining", before-len(d.orderSource), len(d.orderSource))
	}
}

// SeedBook places large resting limit orders on both sides around midPrice.
// Uses $0.01 increments so that Binance trades (also $0.01 ticks) can cross levels.
// Levels at i=0 are AT midPrice so the first incoming taker trade will cross.
func (d *DemoEngine) SeedBook(midPrice float64) {
	levels := 2000
	tick := 0.01 // $0.01 per level (matches Binance BTCUSDT tick) — 2000 levels = $20 range

	for i := 0; i <= levels; i++ {
		d.idSeq++
		sizeBTC := rand.Float64()*0.9 + 0.1 // random from 0.1 to 1.0
		qty := int64(sizeBTC * float64(types.ONE))
		// Ask (sell) at midPrice + i*tick
		askPrice := int64((midPrice + float64(i)*tick) * float64(types.ONE))
		askID := d.idSeq * 1000
		d.setOrderSource(askID, "SEED")
		d.engine.SubmitOrder(&types.KernelOrder{
			Id: askID, Amount: -qty, Price: askPrice, Left: -qty,
			Type: types.LIMIT, TimeInForce: types.GTC, Status: types.OPEN,
		})
		// Bid (buy) at midPrice - i*tick
		bidPrice := int64((midPrice - float64(i)*tick) * float64(types.ONE))
		bidID := d.idSeq*1000 + 1
		d.setOrderSource(bidID, "SEED")
		d.engine.SubmitOrder(&types.KernelOrder{
			Id: bidID, Amount: qty, Price: bidPrice, Left: qty,
			Type: types.LIMIT, TimeInForce: types.GTC, Status: types.OPEN,
		})
	}
	log.Printf("[engine] seeded book: %d levels each side, tick=$0.01, around %.2f", levels, midPrice)
}

// FeedTrade submits a single taker order from a Binance aggTrade.
func (d *DemoEngine) FeedTrade(trade AggTrade) {
	priceF, err := trade.PriceFloat()
	if err != nil {
		return
	}
	qtyF, err := trade.QtyFloat()
	if err != nil {
		return
	}
	price := int64(priceF * float64(types.ONE))
	qty := int64(qtyF * float64(types.ONE))
	if price <= 0 || qty <= 0 {
		return
	}

	d.idSeq++
	orderID := d.idSeq * 1000

	// Set source BEFORE SubmitOrder so entry exists if order matches synchronously
	d.setOrderSource(orderID, "BINANCE_FEED")

	// m=true  -> buyer is maker -> taker SOLD -> sell order crosses bids
	// m=false -> seller is maker -> taker BOUGHT -> buy order crosses asks
	if trade.IsBuyerMaker {
		d.engine.SubmitOrder(&types.KernelOrder{
			Id: orderID, Amount: -qty, Price: price, Left: -qty,
			Type: types.LIMIT, TimeInForce: types.GTC, Status: types.OPEN,
		})
	} else {
		d.engine.SubmitOrder(&types.KernelOrder{
			Id: orderID, Amount: qty, Price: price, Left: qty,
			Type: types.LIMIT, TimeInForce: types.GTC, Status: types.OPEN,
		})
	}

	// Drain any pending matches so this order's match is consumed before the next FeedTrade's setOrderSource.
	// This closes the race where ConsumeMatches (running in its own goroutine) would otherwise
	// fire for this order AFTER the next order's setOrderSource has already been called.
	d.drainPendingMatches()
}

// AdjustSpread checks if Binance price has drifted >0.1% from spread midpoint.
// If so, submits a taker order to consume the lagging side, bringing midpoint toward price.
func (d *DemoEngine) AdjustSpread() {
	bestAsk := d.engine.BestAsk()
	bestBid := d.engine.BestBid()

	if bestAsk == 0 || bestBid == 0 || bestAsk == math.MaxInt64 {
		return
	}

	price := d.GetLastPrice()
	if price <= 0 {
		return
	}

	bestAskF := float64(bestAsk) / float64(types.ONE)
	bestBidF := float64(bestBid) / float64(types.ONE)
	midpointF := (bestAskF + bestBidF) / 2

	diff := math.Abs(price-midpointF) / midpointF
	if diff < 0.001 {
		return
	}
	log.Printf("[spread] triggered: diff=%.4f%%, price=%.2f midpoint=%.2f", diff*100, price, midpointF)

	qty := int64(1.0 * float64(types.ONE))
	if price > midpointF {
		// Binance price above midpoint -> buy to consume asks
		d.idSeq++
		orderID := d.idSeq * 1000
		d.setOrderSource(orderID, "SPREAD_ADJ")
		d.engine.SubmitOrder(&types.KernelOrder{
			Id: orderID, Amount: qty, Price: bestAsk, Left: qty,
			Type: types.LIMIT, TimeInForce: types.GTC, Status: types.OPEN,
		})
	} else {
		// Binance price below midpoint -> sell to consume bids
		d.idSeq++
		orderID := d.idSeq * 1000
		d.setOrderSource(orderID, "SPREAD_ADJ")
		d.engine.SubmitOrder(&types.KernelOrder{
			Id: orderID, Amount: -qty, Price: bestBid, Left: -qty,
			Type: types.LIMIT, TimeInForce: types.GTC, Status: types.OPEN,
		})
	}
	d.drainPendingMatches()
}

// ConsumeMatches reads match results and broadcasts trade events.
func (d *DemoEngine) ConsumeMatches() {
	for mr := range d.engine.MatchedInfoChan() {
		d.stats.AddTrade(1)
		taker := mr.TakerOrder
		side := "buy"
		if taker.Amount < 0 {
			side = "sell"
			d.stats.AddSell(1)
		} else {
			d.stats.AddBuy(1)
		}

		takerFilled := taker.Amount - taker.Left
		totalSize := takerFilled
		if totalSize < 0 {
			totalSize = -totalSize
		}
		d.stats.AddVolume(totalSize / 2)
		src := d.getOrderSource(taker.Id)
		// Don't remove source — same taker order can match multiple makers (multiple matchedInfo
		// with same taker.Id). Removing after first match causes UNKNOWN for subsequent matches.
		// Source map grows to at most ~# submitted orders, which is bounded for this demo.

		msg, _ := json.Marshal(map[string]interface{}{
			"type":       "trade",
			"taker_id":   taker.Id,
			"price":      taker.Price,
			"side":       side,
			"size":       totalSize,
			"maker_ids":  makerIDs(mr.MakerOrders),
			"created_by": src,
			"timestamp":  taker.CreateTime / 1e6,
		})
		d.hub.Broadcast(msg)
	}
}

// drainPendingMatches drains ALL currently-available matches from matchResultCh
// using blocking receives. This ensures that when FeedTrade/AdjustSpread returns,
// any matches for the just-submitted order have been consumed (and their source
// entries are still in the map). ConsumeMatches will then only receive NEW matches
// that arrive after this function returns.
func (d *DemoEngine) drainPendingMatches() {
	for {
		select {
		case mr, ok := <-d.engine.MatchedInfoChan():
			if !ok {
				return
			}
			d.stats.AddTrade(1)
			taker := mr.TakerOrder
			if taker.Amount < 0 {
				d.stats.AddSell(1)
			} else {
				d.stats.AddBuy(1)
			}
			takerFilled := taker.Amount - taker.Left
			totalSize := takerFilled
			if totalSize < 0 {
				totalSize = -totalSize
			}
			d.stats.AddVolume(totalSize / 2)
			// DON'T broadcast here — ConsumeMatches handles all broadcasts
			// DON'T remove source here — ConsumeMatches does it
		default:
			// Channel empty, no more pending matches right now
			return
		}
	}
}

const broadcastLevels = 10

// BroadcastOrderBook sends the current order book to all clients.
func (d *DemoEngine) BroadcastOrderBook() {
	ob := d.engine.OrderBook()
	asks := ob.Asks
	bids := ob.Bids
	if len(asks) > broadcastLevels {
		asks = asks[:broadcastLevels]
	}
	if len(bids) > broadcastLevels {
		bids = bids[:broadcastLevels]
	}
	msg, _ := json.Marshal(map[string]interface{}{
		"type": "orderbook",
		"asks": asks,
		"bids": bids,
	})
	d.hub.Broadcast(msg)
}

// BroadcastBinanceTrade sends a raw Binance trade to the dashboard.
func (d *DemoEngine) BroadcastBinanceTrade(trade AggTrade) {
	side := "buy"
	if trade.IsBuyerMaker {
		side = "sell"
	}
	msg, _ := json.Marshal(map[string]interface{}{
		"type":      "binance",
		"price":     trade.Price,
		"qty":       trade.Quantity,
		"side":      side,
		"trade_id":  trade.AggTradeID,
		"timestamp": trade.TradeTime,
	})
	d.hub.Broadcast(msg)
}

// BroadcastStats sends current statistics.
func (d *DemoEngine) BroadcastStats() {
	snap := d.stats.Snapshot()
	bestAsk := d.engine.BestAsk()
	bestBid := d.engine.BestBid()

	var avgFeedDiffPct float64
	avgFeed := d.AvgFeedPrice()
	if avgFeed > 0 && bestAsk > 0 && bestBid > 0 && bestBid < 9223372036854775807 {
		midpoint := (float64(bestAsk) + float64(bestBid)) / 2 / float64(types.ONE)
		avgFeedDiffPct = (avgFeed - midpoint) / midpoint * 100
	}

	msg, _ := json.Marshal(map[string]interface{}{
		"type":           "stats",
		"total_trades":   snap.TotalTrades,
		"total_volume":   snap.TotalVolume,
		"buy_trades":     snap.BuyTrades,
		"sell_trades":    snap.SellTrades,
		"binance_trades": snap.BinanceTrades,
		"best_ask":       bestAsk,
		"best_bid":       bestBid,
		"ask_levels":     d.engine.AskLength(),
		"bid_levels":     d.engine.BidLength(),
		"avg_feed_diff":  avgFeedDiffPct,
		"mem_sys":        snap.MemSys,
	})
	d.hub.Broadcast(msg)
}

// ReplenishBook adds new resting orders when levels get thin.
func (d *DemoEngine) ReplenishBook(midPrice float64) {
	askLevels := d.engine.AskLength()
	bidLevels := d.engine.BidLength()
	qty := int64(5.0 * float64(types.ONE))
	tick := types.ONE / 100 // $0.01

	if askLevels < 30 {
		bestAsk := d.engine.BestAsk()
		if bestAsk > 0 {
			for i := 0; i < 20; i++ {
				d.idSeq++
				price := bestAsk + int64(i+1)*tick
				replID := d.idSeq * 1000
				d.setOrderSource(replID, "REPLENISH")
				d.engine.SubmitOrder(&types.KernelOrder{
					Id: replID, Amount: -qty, Price: price, Left: -qty,
					Type: types.LIMIT, TimeInForce: types.GTC, Status: types.OPEN,
				})
			}
		}
	}

	if bidLevels < 30 {
		bestBid := d.engine.BestBid()
		if bestBid > 0 {
			for i := 0; i < 20; i++ {
				d.idSeq++
				price := bestBid - int64(i+1)*tick
				replID := d.idSeq * 1000
				d.setOrderSource(replID, "REPLENISH")
				d.engine.SubmitOrder(&types.KernelOrder{
					Id: replID, Amount: qty, Price: price, Left: qty,
					Type: types.LIMIT, TimeInForce: types.GTC, Status: types.OPEN,
				})
			}
		}
	}
	d.drainPendingMatches()
}

// GenerateRandomOrder submits a random limit order within ±0.1% of spread midpoint.
// Size is 0.01-0.1 BTC. Random interval is handled by the caller.
func (d *DemoEngine) GenerateRandomOrder() {
	bestAsk := d.engine.BestAsk()
	bestBid := d.engine.BestBid()

	if bestAsk == 0 || bestBid == 0 {
		return
	}

	bestAskF := float64(bestAsk) / float64(types.ONE)
	bestBidF := float64(bestBid) / float64(types.ONE)
	midpointF := (bestAskF + bestBidF) / 2

	// Price within ±0.1% of midpoint
	priceOffset := (rand.Float64()*0.002 - 0.001) * midpointF
	price := midpointF + priceOffset
	priceInt := int64(price * float64(types.ONE))

	// Size 0.01-0.1 BTC
	sizeBTC := rand.Float64()*0.09 + 0.01
	qty := int64(sizeBTC * float64(types.ONE))

	d.idSeq++
	orderID := d.idSeq * 1000

	// Random buy or sell
	side := "buy"
	amount := qty
	if rand.Float64() < 0.5 {
		side = "sell"
		amount = -qty
	}

	d.setOrderSource(orderID, "RANDOM")
	d.engine.SubmitOrder(&types.KernelOrder{
		Id: orderID, Amount: amount, Price: priceInt, Left: amount,
		Type: types.LIMIT, TimeInForce: types.GTC, Status: types.OPEN,
	})
	log.Printf("[random] id=%d side=%s price=%.2f size=%.4f", orderID, side, price, sizeBTC)
	d.drainPendingMatches()
}

func makerIDs(orders []types.KernelOrder) []uint64 {
	ids := make([]uint64, len(orders))
	for i, o := range orders {
		ids[i] = o.KernelOrderID
	}
	return ids
}
