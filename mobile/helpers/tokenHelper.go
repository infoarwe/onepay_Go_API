package helper

import (
	"time"

	jwt "github.com/golang-jwt/jwt/v5"

	"mobileapi/common"
)

// SignedDetails is the JWT claim shape for driver-app tokens. Kept separate
// from the web pack's admin SignedDetails since a driver identity carries
// different fields (driver id + company domain, not role/user_type).
type SignedDetails struct {
	DriverID      string `json:"driver_id"`
	CompanyDomain string `json:"company_domain"`
	jwt.RegisteredClaims
}

// GenerateAllTokens generates access and refresh tokens for a driver.
func GenerateAllTokens(driverID, companyDomain string) (string, string, error) {
	claims := &SignedDetails{
		DriverID:      driverID,
		CompanyDomain: companyDomain,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	refreshClaims := &SignedDetails{
		DriverID:      driverID,
		CompanyDomain: companyDomain,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(common.Config.UserKey))
	if err != nil {
		return "", "", err
	}

	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString([]byte(common.Config.UserKey))
	if err != nil {
		return "", "", err
	}

	return token, refreshToken, nil
}

// BootstrapClaims is the JWT claim shape for the short-lived auth_key
// returned by check_companydomain, before any driver is logged in. It's
// deliberately thin (tenant + intended user type only) - it is not a login
// session token, see SignedDetails for that.
type BootstrapClaims struct {
	CompanyDomain string `json:"company_domain"`
	UserType      string `json:"user_type"` // "D" for driver
	jwt.RegisteredClaims
}

// GenerateAuthKey issues the auth_key check_companydomain returns.
// This replaces the legacy scheme (fleeteratokenization.php's
// key_generation(): insert a row into a `token` collection, return
// sha256(subdomain+rand)+"_"+insertedId, delete-and-reissue on next use)
// with a stateless short-lived JWT instead - no token collection, no
// single-use/rotation tracking. That's a deliberate tradeoff (see
// driverController.go): simpler, but a captured auth_key stays valid for
// its full 5-minute lifetime instead of being single-use.
func GenerateAuthKey(companyDomain, userType string) (string, error) {
	claims := &BootstrapClaims{
		CompanyDomain: companyDomain,
		UserType:      userType,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(common.Config.UserKey))
}

// ValidateAccessToken validates an access token.
func ValidateAccessToken(signedToken string) (*SignedDetails, string) {
	return validateToken(signedToken, true)
}

// ValidateRefreshToken validates a refresh token.
func ValidateRefreshToken(signedToken string) (*SignedDetails, string) {
	return validateToken(signedToken, false)
}

func validateToken(signedToken string, checkExpiry bool) (*SignedDetails, string) {
	token, err := jwt.ParseWithClaims(
		signedToken,
		&SignedDetails{},
		func(token *jwt.Token) (interface{}, error) {
			return []byte(common.Config.UserKey), nil
		},
	)
	if err != nil {
		return nil, err.Error()
	}

	claims, ok := token.Claims.(*SignedDetails)
	if !ok {
		return nil, "Invalid token"
	}

	if checkExpiry && claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(time.Now()) {
		return nil, "Token expired"
	}
	return claims, ""
}
