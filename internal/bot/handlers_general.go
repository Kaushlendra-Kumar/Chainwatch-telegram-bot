package bot

import (
	"fmt"

	tele "gopkg.in/telebot.v3"

	"chainwatch/internal/format"
)

const helpText = `<b>Prices</b>
/price btc eth — current prices
/top — top 10 coins by market cap
/chart btc 7d — price chart (1d, 7d, 30d, 90d, 1y)

<b>Price alerts</b>
/alert btc above 100000 — notify when price crosses a level
/alerts — your active alerts
/delalert 12 — delete alert #12

<b>Wallet tracking</b>
/watch eth 0xabc… Vitalik — watch a wallet (label optional)
/wallets — wallets you watch
/unwatch 5 — stop watching #5
/minusd 5 1000 — only notify transfers worth ≥ $1,000
/balance sol &lt;address&gt; — native coin balance
/chains — supported chains

<b>Portfolio</b>
/add btc 0.5 60000 — record a buy (quantity, price)
/sell btc 0.1 70000 — record a sell (price optional)
/remove btc — delete a holding
/portfolio — value and P&amp;L

Numbers accept shortcuts like 100k, 1.5m, $2,500.`

func (b *Bot) cmdStart(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()
	s := c.Sender()
	u, err := b.store.UpsertUser(ctx, s.ID, c.Chat().ID, s.Username, s.FirstName)
	if err == nil {
		b.users.put(u, timeNow())
		b.alerts.Refresh()
		b.wallets.Refresh()
	}
	name := s.FirstName
	if name == "" {
		name = "there"
	}
	return c.Send(fmt.Sprintf("👋 Hi %s! I'm <b>ChainWatch</b>.\n\nI track crypto prices, send price alerts, watch on-chain wallets and keep your portfolio P&amp;L.\n\n%s", format.Escape(name), helpText))
}

func (b *Bot) cmdHelp(c tele.Context) error {
	return c.Send(helpText)
}

func (b *Bot) onText(c tele.Context) error {
	return c.Send("I didn't get that. Try /help to see what I can do.")
}
