package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	tele "gopkg.in/telebot.v3"

	"chainwatch/internal/config"
	"chainwatch/internal/price"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type UpdateSink interface {
	Ingest(ctx context.Context, u tele.Update) error
}

type Deps struct {
	DB            Pinger
	Prices        *price.Service
	Updates       UpdateSink
	WebhookSecret string
	Webhook       bool
	Production    bool
	Log           *slog.Logger
}

func New(addr string, d Deps) *http.Server {
	if d.Production {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery(), requestLogger(d.Log))

	started := time.Now()
	health := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "uptime": time.Since(started).Round(time.Second).String()})
	}
	r.GET("/", health)
	r.HEAD("/", health)
	r.GET("/healthz", health)
	r.HEAD("/healthz", health)

	r.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		if err := d.DB.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "db unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	r.GET("/api/v1/price/:symbol", func(c *gin.Context) {
		symbol := c.Param("symbol")
		if !price.ValidSymbol(symbol) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid symbol"})
			return
		}
		q, err := d.Prices.Quote(c.Request.Context(), symbol, true)
		if errors.Is(err, price.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "symbol not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "price sources unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"symbol":     q.Symbol,
			"price_usd":  q.Price,
			"change_24h": q.Change24h,
			"source":     q.Source,
		})
	})

	if d.Webhook {
		r.POST(config.WebhookPath, func(c *gin.Context) {
			got := c.GetHeader("X-Telegram-Bot-Api-Secret-Token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(d.WebhookSecret)) != 1 {
				c.Status(http.StatusUnauthorized)
				return
			}
			var u tele.Update
			if err := c.ShouldBindJSON(&u); err != nil {
				c.Status(http.StatusBadRequest)
				return
			}
			if err := d.Updates.Ingest(c.Request.Context(), u); err != nil {
				d.Log.Warn("drop telegram update", "err", err)
				c.Status(http.StatusServiceUnavailable)
				return
			}
			c.Status(http.StatusOK)
		})
	}

	return &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
}

func requestLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		path := c.FullPath()
		if path == "/healthz" || path == "/" {
			return
		}
		log.Debug("http request",
			"method", c.Request.Method,
			"path", path,
			"status", c.Writer.Status(),
			"duration", time.Since(start).String(),
		)
	}
}
