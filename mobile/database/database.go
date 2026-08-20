/*
 * @File: databases.databases.go
 * @Description: Creates global database instance
 */
package database

import "go.mongodb.org/mongo-driver/mongo"

type DatabaseInterface interface {
	Init() error
	GetDatabase(domain string) (*mongo.Database, error)
	OpenCollection(database *mongo.Database, collectionName string) interface{}
	Close()
}

// Global variable for the current DB
var (
	DB DatabaseInterface
)
