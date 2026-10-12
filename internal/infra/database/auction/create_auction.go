package auction

import (
	"context"
	"fmt"
	"fullcycle-auction_go/configuration/logger"
	"fullcycle-auction_go/internal/entity/auction_entity"
	"fullcycle-auction_go/internal/internal_error"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const defaultAuctionDuration = 5 * time.Minute

type AuctionEntityMongo struct {
	Id          string                          `bson:"_id"`
	ProductName string                          `bson:"product_name"`
	Category    string                          `bson:"category"`
	Description string                          `bson:"description"`
	Condition   auction_entity.ProductCondition `bson:"condition"`
	Status      auction_entity.AuctionStatus    `bson:"status"`
	Timestamp   int64                           `bson:"timestamp"`
}
type AuctionRepository struct {
	Collection      *mongo.Collection
	auctionDuration time.Duration
}

func NewAuctionRepository(database *mongo.Database) *AuctionRepository {
	return &AuctionRepository{
		Collection:      database.Collection("auctions"),
		auctionDuration: GetAuctionDuration(),
	}
}

func (ar *AuctionRepository) CreateAuction(
	ctx context.Context,
	auctionEntity *auction_entity.Auction) *internal_error.InternalError {
	auctionEntityMongo := &AuctionEntityMongo{
		Id:          auctionEntity.Id,
		ProductName: auctionEntity.ProductName,
		Category:    auctionEntity.Category,
		Description: auctionEntity.Description,
		Condition:   auctionEntity.Condition,
		Status:      auctionEntity.Status,
		Timestamp:   auctionEntity.Timestamp.Unix(),
	}
	_, err := ar.Collection.InsertOne(ctx, auctionEntityMongo)
	if err != nil {
		logger.Error("Error trying to insert auction", err)
		return internal_error.NewInternalServerError("Error trying to insert auction")
	}

	go ar.closeAuctionWhenExpired(auctionEntity.Id, auctionEntity.Timestamp.Add(ar.auctionDuration))

	return nil
}

// closeAuctionWhenExpired waits until endTime and then marks the auction as Completed.
// It runs in its own goroutine, so it must not use the request context: that context
// is cancelled as soon as the HTTP request that created the auction finishes.
func (ar *AuctionRepository) closeAuctionWhenExpired(auctionId string, endTime time.Time) {
	timer := time.NewTimer(time.Until(endTime))
	defer timer.Stop()
	<-timer.C

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := bson.M{"_id": auctionId, "status": auction_entity.Active}
	update := bson.M{"$set": bson.M{"status": auction_entity.Completed}}

	if _, err := ar.Collection.UpdateOne(ctx, filter, update); err != nil {
		logger.Error(fmt.Sprintf("Error trying to close auction id = %s", auctionId), err)
		return
	}

	logger.Info(fmt.Sprintf("Auction id = %s closed automatically", auctionId))
}

// ScheduleActiveAuctionsClosing restores the closing goroutines for auctions that are still
// active in the database (e.g. after an application restart). Auctions whose end time has
// already passed are closed right away.
func (ar *AuctionRepository) ScheduleActiveAuctionsClosing(ctx context.Context) *internal_error.InternalError {
	// FindAuctions can't be reused here: it ignores the status filter when it is Active (0).
	cursor, err := ar.Collection.Find(ctx, bson.M{"status": auction_entity.Active})
	if err != nil {
		logger.Error("Error trying to find active auctions", err)
		return internal_error.NewInternalServerError("Error trying to find active auctions")
	}
	defer cursor.Close(ctx)

	var auctionsMongo []AuctionEntityMongo
	if err := cursor.All(ctx, &auctionsMongo); err != nil {
		logger.Error("Error decoding active auctions", err)
		return internal_error.NewInternalServerError("Error decoding active auctions")
	}

	for _, auction := range auctionsMongo {
		endTime := time.Unix(auction.Timestamp, 0).Add(ar.auctionDuration)
		go ar.closeAuctionWhenExpired(auction.Id, endTime)
	}

	return nil
}

// GetAuctionDuration reads how long an auction stays open from AUCTION_DURATION
// (falling back to the legacy AUCTION_INTERVAL), using Go duration syntax such as "30s" or "5m".
func GetAuctionDuration() time.Duration {
	for _, envName := range []string{"AUCTION_DURATION", "AUCTION_INTERVAL"} {
		duration, err := time.ParseDuration(os.Getenv(envName))
		if err == nil && duration > 0 {
			return duration
		}
	}

	return defaultAuctionDuration
}
