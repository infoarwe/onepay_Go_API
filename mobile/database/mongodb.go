package database

import (
	"context"
	"fmt"
	"log"
	"mobileapi/collections"
	"mobileapi/common"
	"mobileapi/models"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

type MongoDB struct {
	Client *mongo.Client
	Dbname *mongo.Database
}

var (
	domainDBMap  = map[string]*mongo.Database{}
	domainDBLock sync.RWMutex
)

func (m *MongoDB) Init() error {
	uri := fmt.Sprint(common.Config.MgAddrs)

	// No SetServerAPIOptions here: that declares MongoDB's Stable API
	// (apiVersion: "1"), which Atlas wants but this self-hosted server
	// rejects outright ("Unrecognized field 'apiVersion'") - it predates
	// that feature.
	clientOptions := options.Client().ApplyURI(uri)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return err
	}

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return err
	}

	log.Println("Connected to MongoDB")
	m.Client = client
	return nil
}

// GetDatabase returns the MongoDB database for a tenant. The `domain`
// value (the company_domain string from the Domain header) IS the database
// name - confirmed against the real deployment, there is no separate
// domain -> db_name registry. Callers must validate the domain first via
// IsDomainValid; this method itself can't fail just from an unknown string
// since the driver only opens a handle lazily.
func (m *MongoDB) GetDatabase(domain string) (*mongo.Database, error) {
	domainDBLock.RLock()
	if d, ok := domainDBMap[domain]; ok {
		domainDBLock.RUnlock()
		return d, nil
	}
	domainDBLock.RUnlock()

	dbName := common.Config.MgDbName
	if domain != "" {
		dbName = domain
	}

	mongoDB := m.Client.Database(dbName)

	domainDBLock.Lock()
	domainDBMap[domain] = mongoDB
	domainDBLock.Unlock()

	return mongoDB, nil
}

// OpenCollection returns a collection
func (m *MongoDB) OpenCollection(database *mongo.Database, collectionName string) interface{} {
	return database.Collection(collectionName)
}

// Close disconnects MongoDB
func (m *MongoDB) Close() {
	if m.Client != nil {
		if err := m.Client.Disconnect(context.TODO()); err != nil {
			log.Printf("Error closing MongoDB client: %v\n", err)
		} else {
			log.Println("MongoDB client disconnected")
		}
	}
}

// LoadDomains just sanity-checks connectivity to the master company_domain
// registry at startup and logs how many tenants are currently valid. There's
// no map to build ahead of time - GetDatabase derives the db name directly
// from the domain string - so this is a health check, not a cache warm-up.
func (m *MongoDB) LoadDomains() {
	masterDB := m.Client.Database(common.Config.MgDbName)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	count, err := masterDB.Collection(collections.COMPANY_DOMAIN).CountDocuments(ctx, map[string]string{})
	if err != nil {
		log.Printf("Warning: could not read %s collection at startup: %v\n", collections.COMPANY_DOMAIN, err)
		return
	}
	fmt.Println("company_domain rows visible at startup:", count)
}

// IsDomainValid checks for a matching, active company_domain row - see
// models.FindCompanyDomain (ported from moddriverapi113.php's
// check_company_domain()).
//
// Confirmed against live data across three tenant databases (onepaytaxi,
// uatonepaytaxi, ridelogic) that there is NO single shared master registry:
// each tenant database self-registers exactly one company_domain row for
// itself (e.g. the `onepaytaxi` db's own company_domain collection holds
// `{company_domain: "onepaytaxi"}`, not a cross-tenant list). This used to
// query common.Config.MgDbName as a fixed "master" db instead - which only
// ever validated successfully for the one domain that happened to equal
// MgDbName (uatonepaytaxi in every deployment's config.json seen so far),
// silently rejecting every other real tenant domain as "Invalid Domain".
func (m *MongoDB) IsDomainValid(domain string) bool {
	tenantDB, err := m.GetDatabase(domain)
	if err != nil {
		log.Printf("IsDomainValid lookup error for %q: %v\n", domain, err)
		return false
	}
	result, err := models.FindCompanyDomain(tenantDB, domain)
	if err != nil {
		log.Printf("IsDomainValid lookup error for %q: %v\n", domain, err)
		return false
	}
	return result != nil
}
