package config

import "os"

// Config holds the shared platform configuration, read from environment
// variables with development-friendly defaults.
type Config struct {
	LedgerURL  string // base URL of the Formance Ledger API
	LedgerName string // ledger name used for all platform postings
}

func Load() Config {
	return Config{
		LedgerURL:  getenv("FAWTARA_LEDGER_URL", "http://localhost:3068"),
		LedgerName: getenv("FAWTARA_LEDGER_NAME", "fawtara"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
