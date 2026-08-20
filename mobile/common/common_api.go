package common

import (
	"context"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GetNextAutoID mirrors the web pack's helper for collections that still use
// an incrementing numeric-as-string id instead of ObjectID.
func GetNextAutoID(db *mongo.Database, collectionName string, autoIDField string) (string, error) {
	collection := db.Collection(collectionName)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	findOptions := options.Find().SetProjection(bson.M{autoIDField: 1}).SetSort(bson.D{{Key: autoIDField, Value: -1}}).SetLimit(1)

	cursor, err := collection.Find(ctx, bson.D{}, findOptions)
	if err != nil {
		return "-1", err
	}
	defer cursor.Close(ctx)

	var result bson.M
	if cursor.Next(ctx) {
		if err := cursor.Decode(&result); err != nil {
			return "-1", err
		}
	}

	var nextID string
	if id, ok := result[autoIDField].(string); ok {
		intID, err := strconv.Atoi(id)
		if err != nil {
			return "-1", err
		}
		nextID = strconv.Itoa(intID + 1)
	} else {
		nextID = "1"
	}

	return nextID, nil
}
