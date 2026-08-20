package models

import (
	"context"
	"time"

	"mobileapi/collections"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// This file replaces the earlier "site_config" approach (a manually-seeded,
// easily-stale snapshot document) with live reads against the real
// collections the admin panel actually writes to - matching the legacy
// getcoreconfig case (moddriverapi201.php:220-686, and its dependencies in
// modmobileapi111extended.php / fleeteracommonmodel.php, for the BlueTaxi
// tenant this was researched against). See driverController.go's
// GetCoreConfig doc comment for the full field-source mapping.
//
// Scope/fidelity note: the main siteinfo+theme_settings+map_settings join
// (FindSiteInfo) mirrors the legacy aggregation closely (same _id: 1 match,
// same two $lookups). The smaller list collections below (csc, vehicle_info,
// vehicle_color, vehicle_plate_prefix) are read with plain Find calls rather
// than byte-exact replicas of the legacy aggregation pipelines (e.g.
// getStateList()'s $unwind of a nested `stateinfo` array) - those pipelines'
// exact field shapes aren't confirmed against live data from this
// environment, so a faithful blind replica risks being confidently wrong.
// motor_model and payment_modules use the two filters that WERE confirmed
// (model_status: 'A', pay_mod_active: 1).

// SiteInfoDocID is the fixed _id of the single siteinfo document per tenant
// database (select_site_settings()/common_site_info() both $match _id: 1).
const SiteInfoDocID = 1

// FindSiteInfo reads the tenant's siteinfo document, left-joined with its
// theme_settings and map_settings docs (same _id: 1 join the legacy
// common_site_info()/select_site_settings() perform) - the single query
// covering the large majority of getcoreconfig's response fields, plus the
// cron_time/notification_settings fields driverLocationController.go reads
// for the driver-dispatch flow. Decoded as bson.M rather than a typed
// struct: this is mostly passthrough data with no Go-side behavior attached
// to individual fields, same rationale as FindCompanyDomain.
func FindSiteInfo(tenantDB *mongo.Database) (bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"_id": SiteInfoDocID}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         collections.THEME_SETTINGS,
			"localField":   "_id",
			"foreignField": "_id",
			"as":           "themesettings",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$themesettings", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         collections.MAP_SETTINGS,
			"localField":   "_id",
			"foreignField": "_id",
			"as":           "mapsettings",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$mapsettings", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$limit", Value: int64(1)}},
	}

	cursor, err := tenantDB.Collection(collections.SITEINFO).Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var results []bson.M
	if err := cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

// findAllTolerant is the shared "return every live document in this
// collection" helper for the list lookups below - the admin panel is still
// the source of truth being read from, just without a byte-exact replica of
// the legacy aggregation/projection pipeline.
func findAllTolerant(tenantDB *mongo.Database, collectionName string, filter bson.M) ([]bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if filter == nil {
		filter = bson.M{}
	}

	cursor, err := tenantDB.Collection(collectionName).Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	results := []bson.M{}
	if err := cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// FindVehicleInfoList reads active vehicle manufacturers/models
// (getVehicleInfo(), moddriverapi113.php:9299) - the manufacturer_status
// filter matches the field name confirmed via the original seed data shape.
func FindVehicleInfoList(tenantDB *mongo.Database) ([]bson.M, error) {
	return findAllTolerant(tenantDB, collections.VEHICLE_INFO, bson.M{"manufacturer_status": "A"})
}

// FindVehicleColorList reads active vehicle colors (getVehicleColor(),
// moddriverapi113.php:9354).
func FindVehicleColorList(tenantDB *mongo.Database) ([]bson.M, error) {
	return findAllTolerant(tenantDB, collections.VEHICLE_COLOR, bson.M{"status": "A"})
}

// FindVehiclePlatePrefixList reads active plate prefixes
// (getVehiclePlatePrefix(), moddriverapi113.php:9371).
func FindVehiclePlatePrefixList(tenantDB *mongo.Database) ([]bson.M, error) {
	return findAllTolerant(tenantDB, collections.VEHICLE_PLATE_PREFIX, bson.M{"status": "A"})
}

// FindModelDetails reads active taxi models/fare config
// (company_model_details()'s else-branch, modmobileapi111extended.php:911-966).
// model_status: 'A' is the one confirmed filter field for this collection.
func FindModelDetails(tenantDB *mongo.Database) ([]bson.M, error) {
	return findAllTolerant(tenantDB, collections.MOTOR_MODEL, bson.M{"model_status": "A"})
}

// FindGatewayDetails reads the tenant's active payment modules
// (gateway_details(), fleeteracommonmodel.php:2865-2909). The legacy
// getcoreconfig uses this same query result for both `gateway_array` and
// `passenger_payment_option` (moddriverapi201.php:469-484).
func FindGatewayDetails(tenantDB *mongo.Database) ([]bson.M, error) {
	return findAllTolerant(tenantDB, collections.PAYMENT_MODULES, bson.M{"pay_mod_active": 1})
}

// FindVehicleStateList reads the tenant's active states from the csc
// (country/state/city) collection. Simplified vs. the legacy getStateList()
// aggregation, which unwinds a nested per-country `stateinfo` array - that
// nested shape isn't confirmed against live data here, so this reads csc
// documents directly instead (unfiltered, since the unwound result's
// state_status field may not exist at the top level of the raw csc schema).
func FindVehicleStateList(tenantDB *mongo.Database) ([]bson.M, error) {
	return findAllTolerant(tenantDB, collections.CSC, nil)
}

// FindDefaultCurrency reads the tenant's default country/currency doc
// (common_currency_details(): findOne(csc, {default:1, country_status:'A'})),
// used for country_code/country_iso_code/site_currency.
func FindDefaultCurrency(tenantDB *mongo.Database) (bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := tenantDB.Collection(collections.CSC).FindOne(ctx, bson.M{"default": 1, "country_status": "A"}).Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// FindDefaultPaymentGateway reads the tenant's default payment gateway
// (Commonfunction::default_payment_details():
// findOne(payments_gateways, {default_payment_gateway: 1})).
func FindDefaultPaymentGateway(tenantDB *mongo.Database) (bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := tenantDB.Collection(collections.PAYMENT_GATEWAYS).FindOne(ctx, bson.M{"default_payment_gateway": 1}).Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}
