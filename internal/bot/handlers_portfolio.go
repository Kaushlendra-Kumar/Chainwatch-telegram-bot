package bot

import (
	"errors"
	"fmt"
	"strings"

	tele "gopkg.in/telebot.v3"

	"chainwatch/internal/format"
	"chainwatch/internal/price"
	"chainwatch/internal/store"
)

type PortfolioLine struct {
	store.Holding
	Price     float64
	Change24h float64
	Known     bool
	Value     float64
	Cost      float64
	PnL       float64
	PnLPct    float64
	Weight    float64
}

type PortfolioSummary struct {
	Lines []PortfolioLine
	Value float64
	Cost  float64
	PnL   float64
}

func Summarize(holdings []store.Holding, quotes map[string]price.Quote) PortfolioSummary {
	var s PortfolioSummary
	for _, h := range holdings {
		l := PortfolioLine{Holding: h, Cost: h.Quantity * h.AvgCost}
		if q, ok := quotes[h.Symbol]; ok && q.Price > 0 {
			l.Known, l.Price, l.Change24h = true, q.Price, q.Change24h
			l.Value = h.Quantity * q.Price
			l.PnL = l.Value - l.Cost
			if l.Cost > 0 {
				l.PnLPct = l.PnL / l.Cost * 100
			}
			s.Value += l.Value
			s.Cost += l.Cost
		}
		s.Lines = append(s.Lines, l)
	}
	s.PnL = s.Value - s.Cost
	for i := range s.Lines {
		if s.Value > 0 && s.Lines[i].Known {
			s.Lines[i].Weight = s.Lines[i].Value / s.Value * 100
		}
	}
	return s
}

func (b *Bot) cmdAdd(c tele.Context) error {
	usage := "Usage: <code>/add btc 0.5 60000</code> — symbol, quantity, buy price in USD"
	args := c.Args()
	if len(args) != 3 || !price.ValidSymbol(args[0]) {
		return c.Send(usage)
	}
	symbol := price.Normalize(args[0])
	qty, err1 := ParseNumber(args[1])
	cost, err2 := ParseNumber(args[2])
	if err1 != nil || err2 != nil {
		return c.Send(usage)
	}
	ctx, cancel := b.ctx()
	defer cancel()
	if _, err := b.prices.Quote(ctx, symbol, false); errors.Is(err, price.ErrNotFound) {
		return c.Send(fmt.Sprintf("I can't find a price for <b>%s</b>, so I can't track it.", format.Escape(symbol)))
	}
	h, err := b.store.AddHolding(ctx, currentUser(c).ID, symbol, qty, cost)
	if err != nil {
		return b.fail(c, err, "Couldn't save that holding.")
	}
	return c.Send(fmt.Sprintf("✅ Added %s <b>%s</b> @ %s.\nNow holding <b>%s %s</b> · avg cost %s",
		format.Amount(qty), symbol, format.USD(cost), format.Amount(h.Quantity), symbol, format.USD(h.AvgCost)))
}

func (b *Bot) cmdSell(c tele.Context) error {
	usage := "Usage: <code>/sell btc 0.1 70000</code> — symbol, quantity, optional sell price"
	args := c.Args()
	if (len(args) != 2 && len(args) != 3) || !price.ValidSymbol(args[0]) {
		return c.Send(usage)
	}
	symbol := price.Normalize(args[0])
	qty, err := ParseNumber(args[1])
	if err != nil {
		return c.Send(usage)
	}
	sellPrice := 0.0
	if len(args) == 3 {
		if sellPrice, err = ParseNumber(args[2]); err != nil {
			return c.Send(usage)
		}
	}
	ctx, cancel := b.ctx()
	defer cancel()
	res, err := b.store.SellHolding(ctx, currentUser(c).ID, symbol, qty)
	if errors.Is(err, store.ErrNotFound) {
		return c.Send(fmt.Sprintf("You don't hold any <b>%s</b>.", symbol))
	}
	if err != nil {
		return b.fail(c, err, "Couldn't update that holding.")
	}
	msg := fmt.Sprintf("✅ Sold %s <b>%s</b>.", format.Amount(res.Sold), symbol)
	if sellPrice > 0 {
		realized := (sellPrice - res.AvgCost) * res.Sold
		msg += fmt.Sprintf("\nRealized P&amp;L: <b>%s</b> %s", format.SignedUSD(realized), format.Trend(realized))
	}
	if res.Remaining > 0 {
		msg += fmt.Sprintf("\nRemaining: %s %s", format.Amount(res.Remaining), symbol)
	} else {
		msg += "\nPosition closed."
	}
	return c.Send(msg)
}

func (b *Bot) cmdRemove(c tele.Context) error {
	args := c.Args()
	if len(args) != 1 || !price.ValidSymbol(args[0]) {
		return c.Send("Usage: <code>/remove btc</code>")
	}
	symbol := price.Normalize(args[0])
	ctx, cancel := b.ctx()
	defer cancel()
	removed, err := b.store.RemoveHolding(ctx, currentUser(c).ID, symbol)
	if err != nil {
		return b.fail(c, err, "Couldn't remove that holding.")
	}
	if !removed {
		return c.Send(fmt.Sprintf("You don't hold any <b>%s</b>.", symbol))
	}
	return c.Send(fmt.Sprintf("🗑 Removed <b>%s</b> from your portfolio.", symbol))
}

func (b *Bot) cmdPortfolio(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()
	holdings, err := b.store.Holdings(ctx, currentUser(c).ID)
	if err != nil {
		return b.fail(c, err, "Couldn't load your portfolio.")
	}
	if len(holdings) == 0 {
		return c.Send("Your portfolio is empty. Add a position: <code>/add btc 0.5 60000</code>")
	}
	symbols := make([]string, 0, len(holdings))
	for _, h := range holdings {
		symbols = append(symbols, h.Symbol)
	}
	_ = c.Notify(tele.Typing)
	quotes, err := b.prices.Quotes(ctx, symbols, true)
	if err != nil {
		return b.fail(c, err, "Price sources are unavailable right now.")
	}
	sum := Summarize(holdings, quotes)

	var sb strings.Builder
	sb.WriteString("💼 <b>Portfolio</b>\n")
	for _, l := range sum.Lines {
		fmt.Fprintf(&sb, "\n<b>%s</b> · %s @ %s", l.Symbol, format.Amount(l.Quantity), format.USD(l.AvgCost))
		if !l.Known {
			sb.WriteString("\n   price unavailable")
			continue
		}
		fmt.Fprintf(&sb, "\n   %s · %s %s (%s) · %.1f%%",
			format.USD(l.Value), format.Trend(l.PnL), format.SignedUSD(l.PnL), format.Percent(l.PnLPct), l.Weight)
	}
	pct := 0.0
	if sum.Cost > 0 {
		pct = sum.PnL / sum.Cost * 100
	}
	fmt.Fprintf(&sb, "\n\n<b>Total</b> %s\nCost %s · P&amp;L <b>%s</b> (%s) %s",
		format.USD(sum.Value), format.USD(sum.Cost), format.SignedUSD(sum.PnL), format.Percent(pct), format.Trend(sum.PnL))
	return c.Send(sb.String())
}
