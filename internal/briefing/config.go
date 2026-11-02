package briefing

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Listen, DB, Token, BaseURL, Model string
	Timeout                           time.Duration
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
func LoadConfig() (Config, error) {
	c := Config{Listen: env("BRIEFING_LISTEN_ADDR", "127.0.0.1:18084"), DB: env("BRIEFING_DB_PATH", "data/briefing.db"), Token: os.Getenv("BRIEFING_API_TOKEN"), BaseURL: env("OLLAMA_BASE_URL", "http://127.0.0.1:11434"), Model: env("OLLAMA_MODEL", "qwen3:8b")}
	var err error
	c.Timeout, err = time.ParseDuration(env("OLLAMA_TIMEOUT", "120s"))
	if err != nil || c.Timeout <= 0 {
		return c, errors.New("invalid OLLAMA_TIMEOUT")
	}
	if strings.TrimSpace(c.Token) == "" {
		return c, errors.New("BRIEFING_API_TOKEN is required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, errors.New("invalid OLLAMA_BASE_URL")
	}
	return c, nil
}
