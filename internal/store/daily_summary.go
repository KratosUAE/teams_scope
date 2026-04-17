package store

import (
	"context"
	"fmt"
	"math"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// DaySummary is the per-day quality aggregate returned by DailySummaryRepo.
type DaySummary struct {
	Date            string  `bson:"date"            json:"date"`            // "2026-04-16"
	Calls           int     `bson:"calls"           json:"calls"`
	Good            int     `bson:"good"            json:"good"`
	Poor            int     `bson:"poor"            json:"poor"`
	Bad             int     `bson:"bad"             json:"bad"`
	GroupCalls      int     `bson:"groupCalls"      json:"groupCalls"`
	P2PCalls        int     `bson:"p2pCalls"        json:"p2pCalls"`
	StreamsWithLoss int     `bson:"streamsWithLoss" json:"streamsWithLoss"`
	Over30Pct       int     `bson:"over30pct"       json:"streamsOver30pct"`
	Over60Pct       int     `bson:"over60pct"       json:"streamsOver60pct"`
	Over90Pct       int     `bson:"over90pct"       json:"streamsOver90pct"`
	PeakLossMaxPct  float64 `bson:"peakLossMaxPct"  json:"peakLossMaxPct"`
}

// DailySummaryRepo provides per-day quality aggregates over the calls and
// streams collections. It is wired into Client and accessed as
// store.DailySummary.Summary(ctx, from, to).
type DailySummaryRepo struct {
	calls   *mongo.Collection
	streams *mongo.Collection
}

func newDailySummaryRepo(db *mongo.Database) *DailySummaryRepo {
	return &DailySummaryRepo{
		calls:   db.Collection("calls"),
		streams: db.Collection("streams"),
	}
}

// callDaySummary is the intermediate shape from the calls aggregate.
type callDaySummary struct {
	Date       string `bson:"_id"`
	Calls      int    `bson:"calls"`
	Good       int    `bson:"good"`
	Poor       int    `bson:"poor"`
	Bad        int    `bson:"bad"`
	GroupCalls int    `bson:"groupCalls"`
	P2PCalls   int    `bson:"p2pCalls"`
}

func (r *DailySummaryRepo) aggregateCalls(ctx context.Context, from, to time.Time) (map[string]callDaySummary, error) {
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "startTimeUtc", Value: bson.D{
				{Key: "$gte", Value: from},
				{Key: "$lt", Value: to},
			}},
		}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "$dateToString", Value: bson.D{
				{Key: "format", Value: "%Y-%m-%d"},
				{Key: "date", Value: "$startTimeUtc"},
			}}}},
			{Key: "calls", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "good", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{"$verdict", "Good"}}}, 1, 0,
			}}}}}},
			{Key: "poor", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{"$verdict", "Poor"}}}, 1, 0,
			}}}}}},
			{Key: "bad", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{"$verdict", "Bad"}}}, 1, 0,
			}}}}}},
			{Key: "groupCalls", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{"$callType", "groupCall"}}}, 1, 0,
			}}}}}},
			{Key: "p2pCalls", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{"$callType", "peerToPeer"}}}, 1, 0,
			}}}}}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}

	cursor, err := r.calls.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	out := make(map[string]callDaySummary)
	for cursor.Next(ctx) {
		var row callDaySummary
		if err := cursor.Decode(&row); err != nil {
			return nil, err
		}
		out[row.Date] = row
	}
	return out, cursor.Err()
}

// streamDaySummary is the intermediate shape from the streams aggregate.
type streamDaySummary struct {
	Date            string  `bson:"_id"`
	StreamsWithLoss int     `bson:"streamsWithLoss"`
	Over30Pct       int     `bson:"over30pct"`
	Over60Pct       int     `bson:"over60pct"`
	Over90Pct       int     `bson:"over90pct"`
	PeakLossMaxPct  float64 `bson:"peakLossMaxPct"`
}

func (r *DailySummaryRepo) aggregateStreams(ctx context.Context, from, to time.Time) (map[string]streamDaySummary, error) {
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "segmentStart", Value: bson.D{
				{Key: "$gte", Value: from},
				{Key: "$lt", Value: to},
			}},
			{Key: "maxLossPct", Value: bson.D{{Key: "$gt", Value: 0}}},
		}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "$dateToString", Value: bson.D{
				{Key: "format", Value: "%Y-%m-%d"},
				{Key: "date", Value: "$segmentStart"},
			}}}},
			{Key: "streamsWithLoss", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "over30pct", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{"$maxLossPct", 30}}}, 1, 0,
			}}}}}},
			{Key: "over60pct", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{"$maxLossPct", 60}}}, 1, 0,
			}}}}}},
			{Key: "over90pct", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{"$maxLossPct", 90}}}, 1, 0,
			}}}}}},
			{Key: "peakLossMaxPct", Value: bson.D{{Key: "$max", Value: "$maxLossPct"}}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}

	cursor, err := r.streams.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	out := make(map[string]streamDaySummary)
	for cursor.Next(ctx) {
		var row streamDaySummary
		if err := cursor.Decode(&row); err != nil {
			return nil, err
		}
		out[row.Date] = row
	}
	return out, cursor.Err()
}

// Summary returns per-day quality data for [from, to). Days with no
// calls are included with zero values so the consumer gets a contiguous
// series.
func (r *DailySummaryRepo) Summary(ctx context.Context, from, to time.Time) ([]DaySummary, error) {
	callMap, err := r.aggregateCalls(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("daily summary calls: %w", err)
	}
	streamMap, err := r.aggregateStreams(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("daily summary streams: %w", err)
	}

	// Build contiguous day series.
	var out []DaySummary
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		ds := DaySummary{Date: key}
		if c, ok := callMap[key]; ok {
			ds.Calls = c.Calls
			ds.Good = c.Good
			ds.Poor = c.Poor
			ds.Bad = c.Bad
			ds.GroupCalls = c.GroupCalls
			ds.P2PCalls = c.P2PCalls
		}
		if s, ok := streamMap[key]; ok {
			ds.StreamsWithLoss = s.StreamsWithLoss
			ds.Over30Pct = s.Over30Pct
			ds.Over60Pct = s.Over60Pct
			ds.Over90Pct = s.Over90Pct
			ds.PeakLossMaxPct = math.Round(s.PeakLossMaxPct*10) / 10
		}
		out = append(out, ds)
	}
	return out, nil
}
