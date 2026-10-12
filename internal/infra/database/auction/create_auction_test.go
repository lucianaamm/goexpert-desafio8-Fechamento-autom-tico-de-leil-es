package auction

import (
	"context"
	"fullcycle-auction_go/internal/entity/auction_entity"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const defaultTestMongoURL = "mongodb://admin:admin@localhost:27017/?authSource=admin"

func newTestDatabase(t *testing.T) *mongo.Database {
	t.Helper()

	mongoURL := os.Getenv("MONGODB_TEST_URL")
	if mongoURL == "" {
		mongoURL = defaultTestMongoURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURL))
	if err != nil {
		t.Skipf("MongoDB not available at %s: %v", mongoURL, err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		t.Skipf("MongoDB not available at %s: %v", mongoURL, err)
	}

	database := client.Database("auctions_test_" + strings.ReplaceAll(uuid.NewString(), "-", ""))
	t.Cleanup(func() {
		_ = database.Drop(context.Background())
		_ = client.Disconnect(context.Background())
	})

	return database
}

func findStatus(t *testing.T, repository *AuctionRepository, auctionId string) auction_entity.AuctionStatus {
	t.Helper()

	var auctionMongo AuctionEntityMongo
	if err := repository.Collection.FindOne(
		context.Background(), bson.M{"_id": auctionId}).Decode(&auctionMongo); err != nil {
		t.Fatalf("error finding auction %s: %v", auctionId, err)
	}

	return auctionMongo.Status
}

func TestCreateAuction_ClosesAutomaticallyAfterAuctionDuration(t *testing.T) {
	t.Setenv("AUCTION_DURATION", "2s")
	repository := NewAuctionRepository(newTestDatabase(t))

	auction, internalErr := auction_entity.CreateAuction(
		"Notebook", "Electronics", "Notebook in great condition", auction_entity.Used)
	if internalErr != nil {
		t.Fatalf("error creating auction entity: %v", internalErr)
	}

	// The request context is cancelled right after creation, as an HTTP request would be.
	// The closing goroutine must not depend on it.
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	if err := repository.CreateAuction(requestCtx, auction); err != nil {
		t.Fatalf("error creating auction: %v", err)
	}
	cancelRequest()

	if status := findStatus(t, repository, auction.Id); status != auction_entity.Active {
		t.Fatalf("expected auction to be Active right after creation, got %d", status)
	}

	time.Sleep(time.Second)
	if status := findStatus(t, repository, auction.Id); status != auction_entity.Active {
		t.Fatalf("expected auction to still be Active before AUCTION_DURATION, got %d", status)
	}

	deadline := time.Now().Add(GetAuctionDuration() + 3*time.Second)
	for time.Now().Before(deadline) {
		if findStatus(t, repository, auction.Id) == auction_entity.Completed {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("expected auction to be Completed after AUCTION_DURATION, got %d",
		findStatus(t, repository, auction.Id))
}

func TestScheduleActiveAuctionsClosing_ClosesExpiredAuctions(t *testing.T) {
	t.Setenv("AUCTION_DURATION", "1s")
	repository := NewAuctionRepository(newTestDatabase(t))

	// Simulates an auction left Active by a previous run of the application.
	expiredAuction := &AuctionEntityMongo{
		Id:          "expired-auction",
		ProductName: "Phone",
		Category:    "Electronics",
		Description: "Phone left open by a previous run",
		Condition:   auction_entity.New,
		Status:      auction_entity.Active,
		Timestamp:   time.Now().Add(-time.Hour).Unix(),
	}
	if _, err := repository.Collection.InsertOne(context.Background(), expiredAuction); err != nil {
		t.Fatalf("error inserting auction: %v", err)
	}

	if err := repository.ScheduleActiveAuctionsClosing(context.Background()); err != nil {
		t.Fatalf("error scheduling auctions closing: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if findStatus(t, repository, expiredAuction.Id) == auction_entity.Completed {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatal("expected expired auction to be Completed")
}

func TestGetAuctionDuration(t *testing.T) {
	tests := []struct {
		name             string
		auctionDuration  string
		auctionInterval  string
		expectedDuration time.Duration
	}{
		{"uses AUCTION_DURATION", "45s", "", 45 * time.Second},
		{"AUCTION_DURATION takes precedence", "2m", "10s", 2 * time.Minute},
		{"falls back to AUCTION_INTERVAL", "", "10s", 10 * time.Second},
		{"invalid value uses default", "abc", "", defaultAuctionDuration},
		{"non positive value uses default", "0s", "", defaultAuctionDuration},
		{"unset uses default", "", "", defaultAuctionDuration},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AUCTION_DURATION", tt.auctionDuration)
			t.Setenv("AUCTION_INTERVAL", tt.auctionInterval)

			if got := GetAuctionDuration(); got != tt.expectedDuration {
				t.Errorf("expected %s, got %s", tt.expectedDuration, got)
			}
		})
	}
}
