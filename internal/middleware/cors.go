package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders sets security headers on all responses.
// Generates a per-request CSP nonce stored in context as "csp_nonce".
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Generate per-request nonce for CSP
		b := make([]byte, 16)
		rand.Read(b)
		nonce := base64.StdEncoding.EncodeToString(b)
		c.Set("csp_nonce", nonce)

		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		c.Header("Content-Security-Policy", fmt.Sprintf(
			"default-src 'self'; script-src 'self' 'nonce-%s' https://cdn.tailwindcss.com https://unpkg.com https://d3js.org; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' https://cdn.tailwindcss.com; font-src 'self' data:; ",
			nonce))
		c.Next()
	}
}

// CORS allows cross-origin API requests from known origins only.
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		// Allow requests from our own domain; localhost only in development
		allowed := origin == "https://shrike.mp.ls" ||
			(os.Getenv("SHRIKE_ENV") == "development" && origin == "http://localhost:8043")

		if allowed {
			c.Header("Access-Control-Allow-Origin", origin)
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, X-API-Key, Content-Type")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// LocalhostOnly rejects requests not originating from loopback addresses.
func LocalhostOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if ip != "127.0.0.1" && ip != "::1" {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
}
