package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v4"
	"github.com/mr-tron/base58"
	"github.com/redis/go-redis/v9"
	_ "modernc.org/sqlite" // SQLite driver
)

var (
	ctx        = context.Background()
	client     *redis.Client
	appBaseURL string
	dbFile     string
)

func init() {
	godotenv.Load()
	appBaseURL = os.Getenv("APP_BASE_URL")
	dbFile = os.Getenv("DB_FILE") // SQLite file configurable

	if appBaseURL == "" {
		log.Fatal("APP_BASE_URL must be set in .env")
	}
	if dbFile == "" {
		dbFile = "short_urls.db" // Default if not set
	}

	client = redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
}

// Generate a 5-character short code using Base58 encoding
func generateShortCode() string {
	var b [5]byte
	_, err := rand.Read(b[:])
	if err != nil {
		log.Fatal("Failed to generate random bytes")
	}
	return base58.Encode(b[:])[:5]
}

type ShortenRequest struct {
	URL string `json:"url"`
}

func shortenURL(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		req := new(ShortenRequest)
		if err := c.Bind(req); err != nil {
			return c.JSON(http.StatusBadRequest, echo.Map{"error": "Invalid request"})
		}

		// Validate URL: Must contain "heymarvin.com"
		if !strings.Contains(req.URL, "heymarvin.com") {
			return c.JSON(http.StatusBadRequest, echo.Map{"error": "URL must contain 'heymarvin.com'"})
		}

		// Check Redis for existing short code
		existingCode, err := client.Get(ctx, "url:"+req.URL).Result()
		if err == nil {
			return c.JSON(http.StatusOK, echo.Map{"short_url": fmt.Sprintf("%s%s", appBaseURL, existingCode)})
		} else if err != redis.Nil {
			log.Printf("Redis error: %v", err) // Log Redis errors instead of failing silently
		}

		// Check SQLite if Redis doesn't have it
		var shortCode string
		err = db.QueryRow("SELECT short_code FROM short_urls WHERE original_url = ?", req.URL).Scan(&shortCode)
		if err == nil {
			client.Set(ctx, "url:"+req.URL, shortCode, 0)
			client.Set(ctx, shortCode, req.URL, 0)
			return c.JSON(http.StatusOK, echo.Map{"short_url": fmt.Sprintf("%s%s", appBaseURL, shortCode)})
		} else if err != sql.ErrNoRows {
			log.Printf("SQLite error: %v", err) // Log unexpected SQLite errors
		}

		// Generate new short code
		shortCode = generateShortCode()

		// Save in SQLite
		_, err = db.Exec("INSERT INTO short_urls (short_code, original_url) VALUES (?, ?)", shortCode, req.URL)
		if err != nil {
			log.Printf("Failed to store in SQLite: %v", err)
			return c.JSON(http.StatusInternalServerError, echo.Map{"error": "Could not store URL in SQLite"})
		}

		// Save in Redis
		client.Set(ctx, "url:"+req.URL, shortCode, 0)
		client.Set(ctx, shortCode, req.URL, 0)

		return c.JSON(http.StatusOK, echo.Map{"short_url": fmt.Sprintf("%s%s", appBaseURL, shortCode)})
	}
}

func redirectURL(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		shortCode := c.Param("shortcode")

		// Check Redis
		originalURL, err := client.Get(ctx, shortCode).Result()
		if err == nil {
			client.Incr(ctx, fmt.Sprintf("count:%s", shortCode))
			return c.Redirect(http.StatusMovedPermanently, originalURL)
		} else if err != redis.Nil {
			log.Printf("Redis error: %v", err)
		}

		// Check SQLite if not in Redis
		err = db.QueryRow("SELECT original_url FROM short_urls WHERE short_code = ?", shortCode).Scan(&originalURL)
		if err == sql.ErrNoRows {
			return c.JSON(http.StatusNotFound, echo.Map{"error": "Short URL not found"})
		} else if err != nil {
			log.Printf("SQLite error: %v", err)
			return c.JSON(http.StatusInternalServerError, echo.Map{"error": "Database error"})
		}

		// Cache in Redis
		client.Set(ctx, shortCode, originalURL, 0)

		client.Incr(ctx, fmt.Sprintf("count:%s", shortCode))
		return c.Redirect(http.StatusMovedPermanently, originalURL)
	}
}

func main() {
	// Initialize SQLite
	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		log.Fatal("Failed to connect to SQLite:", err)
	}
	defer db.Close()

	// Create table if not exists
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS short_urls (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		short_code TEXT UNIQUE,
		original_url TEXT UNIQUE
	);`)
	if err != nil {
		log.Fatal("Failed to create table:", err)
	}

	e := echo.New()

	e.GET("/", func(c echo.Context) error {
		return c.Redirect(http.StatusMovedPermanently, "https://heymarvin.com/")
	})

	e.POST("/shorten", shortenURL(db))
	e.GET("/:shortcode", redirectURL(db))

	log.Printf("Server started at %s", appBaseURL)
	e.Start(":9003")
}
