package bot

import (
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"chainwatch/internal/store"
)

const userKey = "user"

type cachedUser struct {
	user    store.User
	expires time.Time
}

type userCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[int64]cachedUser
}

func newUserCache(ttl time.Duration) *userCache {
	return &userCache{ttl: ttl, items: map[int64]cachedUser{}}
}

func (c *userCache) get(telegramID int64, now time.Time) (store.User, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[telegramID]
	if !ok || now.After(it.expires) {
		return store.User{}, false
	}
	return it.user, true
}

func (c *userCache) put(u store.User, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) > 50000 {
		c.items = map[int64]cachedUser{}
	}
	c.items[u.TelegramID] = cachedUser{user: u, expires: now.Add(c.ttl)}
}

type bucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[int64]*bucket
}

func newRateLimiter(perSecond, burst float64) *rateLimiter {
	return &rateLimiter{rate: perSecond, burst: burst, buckets: map[int64]*bucket{}}
}

func (r *rateLimiter) allow(id int64, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buckets) > 50000 {
		r.buckets = map[int64]*bucket{}
	}
	b, ok := r.buckets[id]
	if !ok {
		b = &bucket{tokens: r.burst, last: now}
		r.buckets[id] = b
	}
	b.tokens = min(r.burst, b.tokens+now.Sub(b.last).Seconds()*r.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (b *Bot) privateOnly(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if c.Chat() == nil || c.Sender() == nil || c.Chat().Type != tele.ChatPrivate || c.Sender().IsBot {
			return nil
		}
		return next(c)
	}
}

func (b *Bot) rateLimit(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if !b.throttle.allow(c.Sender().ID, time.Now()) {
			return nil
		}
		return next(c)
	}
}

func (b *Bot) identify(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		now := time.Now()
		s := c.Sender()
		u, ok := b.users.get(s.ID, now)
		if !ok || u.ChatID != c.Chat().ID {
			ctx, cancel := b.ctx()
			defer cancel()
			var err error
			u, err = b.store.UpsertUser(ctx, s.ID, c.Chat().ID, s.Username, s.FirstName)
			if err != nil {
				return b.fail(c, err, "I couldn't reach my database. Please try again in a moment.")
			}
			b.users.put(u, now)
		}
		c.Set(userKey, u)
		return next(c)
	}
}

func currentUser(c tele.Context) store.User {
	u, _ := c.Get(userKey).(store.User)
	return u
}
