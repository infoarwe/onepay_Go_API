/*
 * @File: common.common.go
 * @Description: Defines common information of the service
 */
package common

import (
	"encoding/json"
	"os"
)

// Configuration stores setting values
type Configuration struct {
	Port                string `json:"port"`
	EnableGinConsoleLog bool   `json:"enableGinConsoleLog"`
	EnableGinFileLog    bool   `json:"enableGinFileLog"`
	Mode                string `json:"mode"`
	CorsOrigins         string `json:"corsOrigins"`

	LogFilename   string `json:"logFilename"`
	LogMaxSize    int    `json:"logMaxSize"`
	LogMaxBackups int    `json:"logMaxBackups"`
	LogMaxAge     int    `json:"logMaxAge"`

	MgAddrs      string `json:"mgAddrs"`
	MgDbName     string `json:"mgDbName"`
	MgDbUsername string `json:"mgDbUsername"`
	MgDbPassword string `json:"mgDbPassword"`

	UserKey    string `json:"user_key"`
	ProductKey string `json:"product_key"`

	// DefaultApiKey/TaxiexpressApiKey mirror the two hard-coded mobile_api_key
	// values from moddriverapi201.php's check_companydomain case, keyed by
	// domain so future domains don't need another code change.
	DefaultApiKey string `json:"default_api_key"`

	// DefaultMobileSocketBase/DefaultTimezone are process-wide fallbacks for
	// getcoreconfig fields the legacy PHP sourced from a per-tenant static
	// deploy file (Setting.php's NODE_SERVER_HTTP/BOOTSTRAP_TIMEZONE
	// constants) rather than the database - this Go service has one config
	// per process serving every tenant, so there's no equivalent per-tenant
	// deploy file. Used only when the tenant's siteinfo document doesn't
	// carry its own value for these.
	DefaultMobileSocketBase string `json:"default_mobile_socket_base"`
	DefaultTimezone         string `json:"default_timezone"`

	// DefaultAssetBaseURL is the same kind of fallback: the public base URL
	// (e.g. "https://uat.bluetaxiindia.com") static assets - driver
	// profile/license photos, the no-image placeholder - are served from.
	// Confirmed via a live siteinfo dump that there is no such field on
	// that document at all (an earlier version of this code assumed an
	// "api_base" field that was never real); like NODE_SERVER_HTTP, this is
	// a per-tenant static deploy constant in the legacy PHP, not DB data.
	DefaultAssetBaseURL string `json:"default_asset_base_url"`

	// LegacyAuthorizationKey is the fixed `Authorization` header value the
	// OnePayTaxi tenant's mobile client sends (fleetera_trial_mobilekey_
	// tokenization.php hard-codes this per-install rather than reading it
	// from the DB) - see middleware/legacyAuthMiddleware.go's
	// LegacyProductAuthenticate. Distinct from ProductKey/`authkey`, which
	// is BlueTaxi's separate scheme.
	LegacyAuthorizationKey string `json:"legacy_authorization_key"`
}

// Config shares the global configuration
var (
	Config *Configuration
)

// LoadConfig loads configuration from the config file
func LoadConfig() error {
	file, err := os.Open("configurations/config.json")
	if err != nil {
		return err
	}
	defer file.Close()

	Config = new(Configuration)
	decoder := json.NewDecoder(file)
	return decoder.Decode(&Config)
}
