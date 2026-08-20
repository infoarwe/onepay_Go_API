package models

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mobileapi/collections"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// CompanyDomain mirrors the fields read by moddriverapi113.php's
// check_company_domain() from the master company_domain collection.
// DomainID is kept as the raw BSON value (rather than a typed int) because
// the real _id type (numeric vs ObjectID vs string) hasn't been confirmed
// yet - decoding it strictly was causing "Database Connection Failed" on
// real data. MarkCompanyDomainUsed writes it straight back into the filter
// unchanged, so any type works without us needing to know it up front.
type CompanyDomain struct {
	DomainID     interface{}
	DBtype       string
	MobileApiKey string
	LiveDomain   string
	LivePort     string
	DbExists     string
}

// FindCompanyDomain looks up a company_domain slug in the master database.
// Returns (nil, nil) when the slug doesn't resolve to a usable tenant,
// mirroring the PHP function returning an empty $result array both when no
// row matches and when db_exists === '1'.
//
// Decodes into bson.M first and converts field-by-field instead of a typed
// struct, since we don't yet know the exact BSON types this collection uses
// for every field (int vs int32/int64 vs string are all plausible given the
// legacy PHP's loose typing) - a strict struct decode errors out on any
// mismatch, a tolerant conversion doesn't.
func FindCompanyDomain(masterDB *mongo.Database, companyDomain string) (*CompanyDomain, error) {
	companyDomain = strings.ToLower(strings.TrimSpace(companyDomain))
	if companyDomain == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := masterDB.Collection(collections.COMPANY_DOMAIN).
		FindOne(ctx, bson.M{"company_domain": companyDomain}).
		Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("company_domain lookup for %q: %w", companyDomain, err)
	}

	result := &CompanyDomain{
		DomainID:     raw["_id"],
		DBtype:       toString(raw["DBtype"]),
		MobileApiKey: toString(raw["mobile_api_key"]),
		LiveDomain:   toString(raw["live_domain"]),
		LivePort:     toString(raw["live_port"]),
		DbExists:     toString(raw["db_exists"]),
	}
	if result.DbExists == "1" {
		return nil, nil
	}
	return result, nil
}

// toString tolerantly stringifies whatever BSON type a field actually
// turned out to be, instead of assuming one ahead of time.
func toString(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case int32:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// MarkCompanyDomainUsed mirrors update_used_status(): flags that this
// tenant's bootstrap call has succeeded at least once. domainID is passed
// through as whatever raw type FindCompanyDomain read off _id.
func MarkCompanyDomainUsed(masterDB *mongo.Database, domainID interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := masterDB.Collection(collections.COMPANY_DOMAIN).UpdateOne(
		ctx,
		bson.M{"_id": domainID},
		bson.M{"$set": bson.M{"used_status": 1, "used_date": time.Now()}},
	)
	return err
}
