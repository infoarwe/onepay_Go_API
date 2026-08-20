// Command mint_test_token prints a signed driver session JWT (the token
// DriverAuthenticate expects on the `userAuth` header) for local testing of
// driver-authenticated endpoints such as POST /driver_location_history_update.
// There's no driver-login endpoint in this service yet to issue these for
// real, so this is a stand-in for that until one exists - it doesn't touch
// Mongo, it just signs claims with the same helper (GenerateAllTokens) and
// secret (common.Config.UserKey) the real login flow will eventually use.
//
// Run from the mobile/ directory so the relative configurations/config.json
// path resolves the same way main.go's does:
//
//	go run ./scripts/mint_test_token -driver-id 6 -domain uatbluetaxi
package main

import (
	"flag"
	"fmt"
	"log"

	"mobileapi/common"
	helper "mobileapi/helpers"
)

func main() {
	driverID := flag.String("driver-id", "", "driver_id to embed in the token (required)")
	domain := flag.String("domain", "", "company_domain to embed in the token (required)")
	flag.Parse()

	if *driverID == "" {
		log.Fatal("-driver-id is required")
	}
	if *domain == "" {
		log.Fatal("-domain is required")
	}

	if err := common.LoadConfig(); err != nil {
		log.Fatalf("LoadConfig: %v", err)
	}

	accessToken, refreshToken, err := helper.GenerateAllTokens(*driverID, *domain)
	if err != nil {
		log.Fatalf("GenerateAllTokens: %v", err)
	}

	fmt.Println("userAuth (access, 15m):")
	fmt.Println(accessToken)
	fmt.Println()
	fmt.Println("refresh (7d):")
	fmt.Println(refreshToken)
}
