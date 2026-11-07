// Package config loads settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds every runtime setting.
type Config struct {
	Port                string
	DatabaseURL         string
	Keyring             string
	BaseURL             string
	BufferClientID      string
	BufferClientSecret  string
	GitHubAppID         string
	GitHubAppSlug       string
	GitHubPrivateKey    string
	GitHubWebhookSecret string
	GitHubClientID      string
	GitHubClientSecret  string
	AnthropicAPIKey     string
	R2Endpoint          string
	R2Bucket            string
	R2AccessKey         string
	R2SecretKey         string
	R2PublicBase        string
	SupportEmail        string
	// MailAPIKey (Resend) and MailFrom send the captain's emails; email is
	// hidden in settings when either is empty.
	MailAPIKey        string
	MailFrom          string
	WorkerConcurrency int
}

// Load reads the environment and reports all missing required variables at once.
func Load() (Config, error) {
	var missing []string
	req := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	workers := 2
	if v := os.Getenv("WORKER_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("WORKER_CONCURRENCY must be a positive integer, got %q", v)
		}
		workers = n
	}
	c := Config{
		Port:                port,
		DatabaseURL:         req("DATABASE_URL"),
		Keyring:             req("KEYRING"),
		BaseURL:             strings.TrimRight(req("BASE_URL"), "/"),
		BufferClientID:      req("BUFFER_CLIENT_ID"),
		BufferClientSecret:  req("BUFFER_CLIENT_SECRET"),
		GitHubAppID:         req("GITHUB_APP_ID"),
		GitHubAppSlug:       req("GITHUB_APP_SLUG"),
		GitHubPrivateKey:    req("GITHUB_PRIVATE_KEY"),
		GitHubWebhookSecret: req("GITHUB_WEBHOOK_SECRET"),
		GitHubClientID:      req("GITHUB_CLIENT_ID"),
		GitHubClientSecret:  req("GITHUB_CLIENT_SECRET"),
		AnthropicAPIKey:     req("ANTHROPIC_API_KEY"),
		R2Endpoint:          req("R2_ENDPOINT"),
		R2Bucket:            req("R2_BUCKET"),
		R2AccessKey:         req("R2_ACCESS_KEY"),
		R2SecretKey:         req("R2_SECRET_KEY"),
		R2PublicBase:        req("R2_PUBLIC_BASE"),
		SupportEmail:        os.Getenv("SUPPORT_EMAIL"),
		MailAPIKey:          os.Getenv("RESEND_API_KEY"),
		MailFrom:            os.Getenv("MAIL_FROM"),
		WorkerConcurrency:   workers,
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return c, nil
}
