package bot

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	tele "gopkg.in/telebot.v3"

	"chainwatch/internal/chain"
	"chainwatch/internal/format"
)

func (b *Bot) chainList() string {
	var ids []string
	for _, t := range b.chains.All() {
		ids = append(ids, t.ID())
	}
	return strings.Join(ids, ", ")
}

func (b *Bot) cmdChains(c tele.Context) error {
	var sb strings.Builder
	sb.WriteString("⛓ <b>Supported chains</b>\n")
	for _, t := range b.chains.All() {
		fmt.Fprintf(&sb, "\n<code>%s</code> — %s (%s)", t.ID(), t.Name(), t.NativeSymbol())
	}
	sb.WriteString("\n\nExample: <code>/watch eth 0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045 Vitalik</code>")
	return c.Send(sb.String())
}

func (b *Bot) resolveAddress(c tele.Context, chainArg, addrArg string) (chain.Tracker, string, error) {
	tracker, ok := b.chains.Get(chainArg)
	if !ok {
		return nil, "", c.Send(fmt.Sprintf("Unknown chain <code>%s</code>. Supported: %s", format.Escape(chainArg), b.chainList()))
	}
	addr, err := tracker.Normalize(addrArg)
	if err != nil {
		return nil, "", c.Send(fmt.Sprintf("That doesn't look like a valid %s address.", tracker.Name()))
	}
	return tracker, addr, nil
}

func (b *Bot) cmdWatch(c tele.Context) error {
	args := c.Args()
	if len(args) < 2 {
		return c.Send("Usage: <code>/watch &lt;chain&gt; &lt;address&gt; [label]</code>\nChains: " + b.chainList())
	}
	tracker, addr, err := b.resolveAddress(c, args[0], args[1])
	if tracker == nil {
		return err
	}
	label := strings.TrimSpace(strings.Join(args[2:], " "))
	if utf8.RuneCountInString(label) > 40 {
		label = string([]rune(label)[:40])
	}

	u := currentUser(c)
	_ = c.Notify(tele.Typing)
	ctx, cancel := b.ctx()
	defer cancel()

	count, err := b.store.CountSubscriptions(ctx, u.ID)
	if err != nil {
		return b.fail(c, err, "Couldn't save that. Please try again.")
	}
	if b.opts.MaxWallets > 0 && count >= b.opts.MaxWallets {
		return c.Send(fmt.Sprintf("You're already watching %d wallets (the limit). Remove one with /unwatch.", count))
	}

	walletID, found, err := b.store.FindWallet(ctx, tracker.ID(), addr)
	if err != nil {
		return b.fail(c, err, "Couldn't save that. Please try again.")
	}
	if !found {
		cursor, err := tracker.Bootstrap(ctx, addr)
		if errors.Is(err, chain.ErrInvalidAddress) {
			return c.Send(fmt.Sprintf("%s rejected that address.", tracker.Name()))
		}
		if err != nil {
			return b.fail(c, err, fmt.Sprintf("Couldn't reach the %s data provider. Try again in a minute.", tracker.Name()))
		}
		walletID, err = b.store.CreateWallet(ctx, tracker.ID(), addr, cursor)
		if err != nil {
			return b.fail(c, err, "Couldn't save that. Please try again.")
		}
	}

	subID, created, err := b.store.Subscribe(ctx, u.ID, walletID, label)
	if err != nil {
		return b.fail(c, err, "Couldn't save that. Please try again.")
	}
	b.wallets.Refresh()

	name := format.Escape(format.ShortAddr(addr))
	if label != "" {
		name = "<b>" + format.Escape(label) + "</b> (" + name + ")"
	}
	if !created {
		return c.Send(fmt.Sprintf("You're already watching %s on %s (#%d).", name, tracker.Name(), subID))
	}
	return c.Send(fmt.Sprintf("👀 Watching %s on %s as <b>#%d</b>.\n\nI'll message you when new transfers confirm. To skip small ones: <code>/minusd %d 1000</code>",
		name, tracker.Name(), subID, subID))
}

func (b *Bot) cmdWallets(c tele.Context) error {
	ctx, cancel := b.ctx()
	defer cancel()
	subs, err := b.store.UserSubscriptions(ctx, currentUser(c).ID)
	if err != nil {
		return b.fail(c, err, "Couldn't load your wallets.")
	}
	if len(subs) == 0 {
		return c.Send("You aren't watching any wallets yet.\nTry: <code>/watch eth 0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045 Vitalik</code>")
	}
	var sb strings.Builder
	sb.WriteString("👛 <b>Watched wallets</b>\n")
	for _, s := range subs {
		chainName := s.Chain
		link := ""
		if t, ok := b.chains.Get(s.Chain); ok {
			chainName = t.Name()
			link = t.AddressURL(s.Address)
		}
		fmt.Fprintf(&sb, "\n<b>#%d</b> %s · <a href=\"%s\">%s</a>", s.ID, chainName, link, format.Escape(format.ShortAddr(s.Address)))
		if s.Label != "" {
			sb.WriteString(" · " + format.Escape(s.Label))
		}
		if s.MinUSD > 0 {
			sb.WriteString(" · min " + format.USD(s.MinUSD))
		}
	}
	sb.WriteString("\n\nStop with <code>/unwatch &lt;id&gt;</code>")
	return c.Send(sb.String(), tele.NoPreview)
}

func (b *Bot) cmdUnwatch(c tele.Context) error {
	args := c.Args()
	id, ok := int64(0), false
	if len(args) == 1 {
		id, ok = ParseID(args[0])
	}
	if !ok {
		return c.Send("Usage: <code>/unwatch 5</code> (see /wallets for ids)")
	}
	ctx, cancel := b.ctx()
	defer cancel()
	deleted, err := b.store.Unsubscribe(ctx, currentUser(c).ID, id)
	if err != nil {
		return b.fail(c, err, "Couldn't update your wallets.")
	}
	if !deleted {
		return c.Send(fmt.Sprintf("No watched wallet #%d found.", id))
	}
	b.wallets.Refresh()
	return c.Send(fmt.Sprintf("🛑 Stopped watching #%d.", id))
}

func (b *Bot) cmdMinUSD(c tele.Context) error {
	args := c.Args()
	usage := "Usage: <code>/minusd 5 1000</code> — only notify transfers worth at least $1,000 (0 = everything)"
	if len(args) != 2 {
		return c.Send(usage)
	}
	id, ok := ParseID(args[0])
	if !ok {
		return c.Send(usage)
	}
	minUSD := 0.0
	if strings.TrimSpace(args[1]) != "0" {
		v, err := ParseNumber(args[1])
		if err != nil {
			return c.Send(usage)
		}
		minUSD = v
	}
	ctx, cancel := b.ctx()
	defer cancel()
	updated, err := b.store.SetMinUSD(ctx, currentUser(c).ID, id, minUSD)
	if err != nil {
		return b.fail(c, err, "Couldn't update that wallet.")
	}
	if !updated {
		return c.Send(fmt.Sprintf("No watched wallet #%d found.", id))
	}
	b.wallets.Refresh()
	if minUSD == 0 {
		return c.Send(fmt.Sprintf("✅ #%d will notify every transfer.", id))
	}
	return c.Send(fmt.Sprintf("✅ #%d will only notify transfers worth ≥ %s. Unpriced or unverified tokens are skipped.", id, format.USD(minUSD)))
}

func (b *Bot) cmdBalance(c tele.Context) error {
	args := c.Args()
	if len(args) != 2 {
		return c.Send("Usage: <code>/balance &lt;chain&gt; &lt;address&gt;</code>\nChains: " + b.chainList())
	}
	tracker, addr, err := b.resolveAddress(c, args[0], args[1])
	if tracker == nil {
		return err
	}
	_ = c.Notify(tele.Typing)
	ctx, cancel := b.ctx()
	defer cancel()
	bal, err := tracker.Balance(ctx, addr)
	if errors.Is(err, chain.ErrInvalidAddress) {
		return c.Send(fmt.Sprintf("%s rejected that address.", tracker.Name()))
	}
	if err != nil {
		return b.fail(c, err, fmt.Sprintf("Couldn't reach the %s data provider.", tracker.Name()))
	}
	msg := fmt.Sprintf("💰 <b>%s balance</b>\n<a href=\"%s\">%s</a>\n\n<b>%s %s</b>",
		tracker.Name(), tracker.AddressURL(addr), format.Escape(format.ShortAddr(addr)), format.Amount(bal.Amount), bal.Symbol)
	if q, err := b.prices.Quote(ctx, bal.Symbol, false); err == nil {
		msg += " (≈ " + format.USD(bal.Amount*q.Price) + ")"
	}
	msg += "\n\n<i>Native coin only; token balances aren't included.</i>"
	return c.Send(msg, tele.NoPreview)
}
