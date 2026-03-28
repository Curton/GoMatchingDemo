package main

import (
	"runtime"
	"sync/atomic"
)

// Stats holds atomic counters for the demo.
type Stats struct {
	totalTrades   atomic.Int64
	totalVolume   atomic.Int64
	buyTrades     atomic.Int64
	sellTrades    atomic.Int64
	binanceTrades atomic.Int64
}

// StatsSnapshot is a point-in-time snapshot for serialization.
type StatsSnapshot struct {
	TotalTrades   int64
	TotalVolume   int64
	BuyTrades     int64
	SellTrades    int64
	BinanceTrades int64
	MemSys        uint64
}

func NewStats() *Stats { return &Stats{} }

func (s *Stats) AddTrade(n int64)   { s.totalTrades.Add(n) }
func (s *Stats) AddVolume(n int64)   { s.totalVolume.Add(n) }
func (s *Stats) AddBuy(n int64)      { s.buyTrades.Add(n) }
func (s *Stats) AddSell(n int64)     { s.sellTrades.Add(n) }
func (s *Stats) AddBinance(n int64)  { s.binanceTrades.Add(n) }

func (s *Stats) Snapshot() StatsSnapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return StatsSnapshot{
		TotalTrades:   s.totalTrades.Load(),
		TotalVolume:   s.totalVolume.Load(),
		BuyTrades:     s.buyTrades.Load(),
		SellTrades:    s.sellTrades.Load(),
		BinanceTrades: s.binanceTrades.Load(),
		MemSys:        m.Sys,
	}
}
