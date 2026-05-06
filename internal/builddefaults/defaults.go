package builddefaults

import "os"

const (
	ProductionServerURL = "https://reliantapi.com"
)

var (
	ServerURL  string
	GatewayURL string
	AuthURL    string
	AuthKey    string
)

func Value(envKey, compiledDefault, fallback string) string {
	if value := os.Getenv(envKey); value != "" {
		return value
	}
	if compiledDefault != "" {
		return compiledDefault
	}
	return fallback
}
