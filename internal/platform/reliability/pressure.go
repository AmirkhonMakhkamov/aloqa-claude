package reliability

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func PostgresPressure(stat *pgxpool.Stat) Pressure {
	if stat == nil {
		return Pressure{}
	}
	maxConns := float64(stat.MaxConns())
	acquired := float64(stat.AcquiredConns())
	utilization := 0.0
	if maxConns > 0 {
		utilization = acquired / maxConns
	}
	// NOTE: stat.EmptyAcquireWaitTime() is a pool-lifetime cumulative counter —
	// once it's non-zero (e.g. brief contention during startup ramp) it stays
	// non-zero forever. Using it to flag Saturated would permanently jam every
	// worker that checks pressure (outbox publisher, search indexer, recording
	// processor). Relying on live utilization instead gives the right signal.
	return Pressure{
		Utilization:   utilization,
		Saturated:     utilization >= 0.85,
		QueuedWaiters: stat.EmptyAcquireCount(),
	}
}

func RedisPressure(stat *redis.PoolStats, poolSize int) Pressure {
	if stat == nil {
		return Pressure{}
	}
	size := float64(poolSize)
	utilization := 0.0
	if size > 0 {
		utilization = float64(stat.TotalConns) / size
	}
	return Pressure{
		Utilization:   utilization,
		Saturated:     stat.Timeouts > 0 || (size > 0 && utilization >= 0.9),
		QueuedWaiters: int64(stat.Timeouts),
	}
}

func DurationMillis(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}
