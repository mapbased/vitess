package worker

import (
	"fmt"

	"golang.org/x/net/context"

	"github.com/youtube/vitess/go/sqltypes"
	"github.com/youtube/vitess/go/vt/topo/topoproto"
	"github.com/youtube/vitess/go/vt/wrangler"

	tabletmanagerdatapb "github.com/youtube/vitess/go/vt/proto/tabletmanagerdata"
	topodatapb "github.com/youtube/vitess/go/vt/proto/topodata"
)

var (
	completeChunk       = chunk{sqltypes.NULL, sqltypes.NULL}
	singleCompleteChunk = []chunk{completeChunk}
)

// chunk holds the information which subset of the table should be worked on.
// The subset is the range of rows in the range [start, end) where start and end
// both refer to the first column of the primary key.
// If the column is not numeric, both start and end will be sqltypes.NULL.
type chunk struct {
	start sqltypes.Value
	end   sqltypes.Value
}

// String returns a human-readable presentation of the chunk range.
func (c chunk) String() string {
	return fmt.Sprintf("[%v,%v)", c.start, c.end)
}

// generateChunks returns an array of chunks to use for splitting up a table
// into multiple data chunks. It only works for tables with a primary key
// whose first column is a numeric type.
func generateChunks(ctx context.Context, wr *wrangler.Wrangler, tablet *topodatapb.Tablet, td *tabletmanagerdatapb.TableDefinition, minTableSizeForSplit uint64, chunkCount int) ([]chunk, error) {
	if len(td.PrimaryKeyColumns) == 0 {
		// No explicit primary key. Cannot chunk the rows then.
		return singleCompleteChunk, nil
	}
	if td.DataLength < minTableSizeForSplit {
		// Table is too small to split up.
		return singleCompleteChunk, nil
	}
	if chunkCount == 1 {
		return singleCompleteChunk, nil
	}

	// Get the MIN and MAX of the leading column of the primary key.
	query := fmt.Sprintf("SELECT MIN(%v), MAX(%v) FROM %v.%v", td.PrimaryKeyColumns[0], td.PrimaryKeyColumns[0], topoproto.TabletDbName(tablet), td.Name)
	shortCtx, cancel := context.WithTimeout(ctx, *remoteActionsTimeout)
	qr, err := wr.TabletManagerClient().ExecuteFetchAsApp(shortCtx, tablet, true, []byte(query), 1)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("Cannot determine MIN and MAX of the first primary key column. ExecuteFetchAsApp: %v", err)
	}
	if len(qr.Rows) != 1 {
		return nil, fmt.Errorf("Cannot determine MIN and MAX of the first primary key column. Zero rows were returned for the following query: %v", query)
	}

	result := sqltypes.Proto3ToResult(qr)
	min := result.Rows[0][0].ToNative()
	max := result.Rows[0][1].ToNative()

	if min == nil || max == nil {
		wr.Logger().Infof("Not splitting table %v into multiple chunks, min or max is NULL: %v", td.Name, qr.Rows[0])
		return singleCompleteChunk, nil
	}

	// TODO(mberlin): Write a unit test for this part of the function.
	chunks := make([]chunk, chunkCount)
	switch min := min.(type) {
	case int64:
		max := max.(int64)
		interval := (max - min) / int64(chunkCount)
		if interval == 0 {
			wr.Logger().Infof("Not splitting table %v into multiple chunks, interval=0: %v to %v", td.Name, min, max)
			return singleCompleteChunk, nil
		}

		start := min
		for i := 0; i < chunkCount; i++ {
			end := start + interval
			chunk, err := toChunk(start, end)
			if err != nil {
				return nil, err
			}
			chunks[i] = chunk
			start = end
		}
	case uint64:
		max := max.(uint64)
		interval := (max - min) / uint64(chunkCount)
		if interval == 0 {
			wr.Logger().Infof("Not splitting table %v into multiple chunks, interval=0: %v to %v", td.Name, min, max)
			return singleCompleteChunk, nil
		}

		start := min
		for i := 0; i < chunkCount; i++ {
			end := start + interval
			chunk, err := toChunk(start, end)
			if err != nil {
				return nil, err
			}
			chunks[i] = chunk
			start = end
		}
	case float64:
		max := max.(float64)
		interval := (max - min) / float64(chunkCount)
		if interval == 0 {
			wr.Logger().Infof("Not splitting table %v into multiple chunks, interval=0: %v to %v", td.Name, min, max)
			return singleCompleteChunk, nil
		}

		start := min
		for i := 0; i < chunkCount; i++ {
			end := start + interval
			chunk, err := toChunk(start, end)
			if err != nil {
				return nil, err
			}
			chunks[i] = chunk
			start = end
		}
	default:
		wr.Logger().Infof("Not splitting table %v into multiple chunks, primary key not numeric.", td.Name)
		return singleCompleteChunk, nil
	}

	// Clear out the MIN and MAX on the first and last chunk respectively
	// because other shards might have smaller or higher values than the one we
	// looked at.
	chunks[0].start = sqltypes.NULL
	chunks[chunkCount-1].end = sqltypes.NULL
	return chunks, nil
}

func toChunk(start, end interface{}) (chunk, error) {
	startValue, err := sqltypes.BuildValue(start)
	if err != nil {
		return chunk{}, fmt.Errorf("Failed to convert calculated start value (%v) into internal sqltypes.Value: %v", start, err)
	}
	endValue, err := sqltypes.BuildValue(end)
	if err != nil {
		return chunk{}, fmt.Errorf("Failed to convert calculated end value (%v) into internal sqltypes.Value: %v", end, err)
	}
	return chunk{startValue, endValue}, nil
}
