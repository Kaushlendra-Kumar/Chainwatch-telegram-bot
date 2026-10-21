package bot

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"chainwatch/internal/chart"
	"chainwatch/internal/format"
	"chainwatch/internal/price"
	"chainwatch/internal/worker"
)

var timeNow = time.Now

func (b *Bot) cmdPrice(c tele.Context) error {
	args := c.Args()
	if len(args) == 0 {
		return c.Send("Usage: <code>/price btc eth sol</code>")
	}
	if len(args) > 10 {
		args = args[:10]
	}
	var symbols []string
	for _, a := range args {
		if !price.ValidSymbol(a) {
			return c.Send(fmt.Sprintf("<code>%s</code> doesn't look like a ticker symbol.", format.Escape(a)))
		}
		symbols = append(symbols, price.Normalize(a))
	}

	_ = c.Notify(tele.Typing)
	ctx, cancel := b.ctx()
	defer cancel()
	quotes, err := b.prices.Quotes(ctx, symbols, true)
	if err != nil {
		return b.fail(c, err, "Price sources are unavailable right now. Try again shortly.")
	}

	var lines, missing []string
	for _, s := range symbols {
		q, ok := quotes[s]
		if !ok {
			missing = append(missing, s)
			continue
		}
		line := fmt.Sprintf("%s <b>%s</b>  %s", format.Trend(q.Change24h), s, format.USD(q.Price))
		if q.HasChange {
			line += fmt.Sprintf("  <i>%s 24h</i>", format.Percent(q.Change24h))
		}
		lines = append(lines, line)
	}
	if len(missing) > 0 {
		lines = append(lines, "\n❓ Not found: "+format.Escape(strings.Join(missing, ", ")))
	}
	return c.Send(strings.Join(lines, "\n"))
}

func (b *Bot) cmdTop(c tele.Context) error {
	_ = c.Notify(tele.Typing)
	ctx, cancel := b.ctx()
	defer cancel()
	coins, err := b.prices.Top(ctx, 10)
	if err != nil {
		return b.fail(c, err, "Couldn't load the market list right now.")
	}
	var sb strings.Builder
	sb.WriteString("🏆 <b>Top 10 by market cap</b>\n")
	for _, coin := range coins {
		fmt.Fprintf(&sb, "\n%d. <b>%s</b> %s\n    %s · %s %s · MC %s",
			coin.Rank, format.Escape(coin.Symbol), format.Escape(coin.Name),
			format.USD(coin.Price), format.Trend(coin.Change24h), format.Percent(coin.Change24h), format.CompactUSD(coin.MarketCap))
	}
	sb.WriteString("\n\n<i>Data: CoinGecko</i>")
	return c.Send(sb.String())
}

func (b *Bot) cmdChart(c tele.Context) error {
	args := c.Args()
	if len(args) == 0 || !price.ValidSymbol(args[0]) {
		return c.Send("Usage: <code>/chart btc 7d</code> (1d, 7d, 30d, 90d, 1y)")
	}
	symbol := price.Normalize(args[0])
	window := ""
	if len(args) > 1 {
		window = args[1]
	}
	days, ok := ParseDays(window)
	if !ok {
		return c.Send("Pick a range: 1d, 7d, 30d, 90d or 1y.")
	}

	_ = c.Notify(tele.UploadingPhoto)
	ctx, cancel := b.ctx()
	defer cancel()
	points, err := b.prices.History(ctx, symbol, days)
	if errors.Is(err, price.ErrNotFound) {
		return c.Send(fmt.Sprintf("No chart data for <b>%s</b>.", format.Escape(symbol)))
	}
	if err != nil {
		return b.fail(c, err, "Couldn't load chart data right now.")
	}
	label := fmt.Sprintf("%dd", days)
	if days == 365 {
		label = "1y"
	}
	png, err := chart.Line(fmt.Sprintf("%s / USD · %s", symbol, label), points, days)
	if err != nil {
		return b.fail(c, err, "Couldn't draw the chart.")
	}
	first, last := points[0].Price, points[len(points)-1].Price
	change := (last - first) / first * 100
	caption := fmt.Sprintf("<b>%s</b> %s: %s → %s (%s %s)",
		format.Escape(symbol), label, format.USD(first), format.USD(last), format.Trend(change), format.Percent(change))
	return c.Send(&tele.Photo{File: tele.FromReader(bytes.NewReader(png)), Caption: caption})
}

func (b *Bot) cmdAlert(c tele.Context) error {
	usage := "Usage: <code>/alert btc above 100000</code> or <code>/alert eth below 2500</code>"
	args := c.Args()
	if len(args) != 3 || !price.ValidSymbol(args[0]) {
		return c.Send(usage)
	}
	symbol := price.Normalize(args[0])
	dir, ok := ParseDirection(args[1])
	if !ok {
		return c.Send(usage)
	}
	target, err := ParseNumber(args[2])
	if err != nil {
		return c.Send("The target price must be a positive number, e.g. <code>100000</code> or <code>100k</code>.")
	}

	u := currentUser(c)
	ctx, cancel := b.ctx()
	defer cancel()

	count, err := b.store.CountActiveAlerts(ctx, u.ID)
	if err != nil {
		return b.fail(c, err, "Couldn't save the alert. Please try again.")
	}
	if b.opts.MaxAlerts > 0 && count >= b.opts.MaxAlerts {
		return c.Send(fmt.Sprintf("You already have %d active alerts (the limit). Remove one with /delalert.", count))
	}

	q, err := b.prices.Quote(ctx, symbol, false)
	if errors.Is(err, price.ErrNotFound) {
		return c.Send(fmt.Sprintf("I can't find a price for <b>%s</b>.", format.Escape(symbol)))
	}
	if err != nil {
		return b.fail(c, err, "Price sources are unavailable right now. Try again shortly.")
	}
	if worker.Triggered(dir, target, q.Price) {
		return c.Send(fmt.Sprintf("<b>%s</b> is already %s %s (now %s). Pick a target on the other side of the current price.",
			format.Escape(symbol), dir, format.USD(target), format.USD(q.Price)))
	}

	a, err := b.store.CreateAlert(ctx, u.ID, symbol, dir, target)
	if err != nil {
		return b.fail(c, err, "Couldn't save the alert. Please try again.")
	}
	b.alerts.Refresh()
	distance := math.Abs(target-q.Price) / q.Price * 100
	return c.Send(fmt.Sprintf("✅ Alert <b>#%d</b> set: I'll ping you when <b>%s</b> goes %s <b>%s</b>.\nNow %s · %.2f%% away.",
		a.ID, format.Escape(symbol), dir, format.USD(target), format.USD(q.Price), distance))
}

func (b *Bot) cmdAlerts(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()
	alerts, err := b.store.UserAlerts(ctx, currentUser(c).ID)
	if err != nil {
		return b.fail(c, err, "Couldn't load your alerts.")
	}
	if len(alerts) == 0 {
		return c.Send("You have no active alerts. Create one: <code>/alert btc above 100000</code>")
	}
	var sb strings.Builder
	sb.WriteString("⏰ <b>Your price alerts</b>\n")
	now := timeNow()
	for _, a := range alerts {
		fmt.Fprintf(&sb, "\n<b>#%d</b> %s %s %s · <i>%s</i>", a.ID, format.Escape(a.Symbol), a.Direction, format.USD(a.Target), format.Ago(a.CreatedAt, now))
	}
	sb.WriteString("\n\nDelete with <code>/delalert &lt;id&gt;</code>")
	return c.Send(sb.String())
}

func (b *Bot) cmdDelAlert(c tele.Context) error {
	args := c.Args()
	if len(args) != 1 {
		return c.Send("Usage: <code>/delalert 12</code>")
	}
	id, ok := ParseID(args[0])
	if !ok {
		return c.Send("Usage: <code>/delalert 12</code>")
	}
	ctx, cancel := b.ctx()
	defer cancel()
	deleted, err := b.store.DeleteAlert(ctx, currentUser(c).ID, id)
	if err != nil {
		return b.fail(c, err, "Couldn't delete the alert.")
	}
	if !deleted {
		return c.Send(fmt.Sprintf("No active alert #%d found.", id))
	}
	b.alerts.Refresh()
	return c.Send(fmt.Sprintf("🗑 Alert #%d deleted.", id))
}
