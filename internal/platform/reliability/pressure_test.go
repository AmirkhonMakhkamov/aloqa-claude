package reliability

import (
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestRedisPressureUsesActiveConnections(t *testing.T) {
	pressure := RedisPressure(&redis.PoolStats{
		TotalConns: 32,
		IdleConns:  31,
	}, 32)

	if pressure.Saturated {
		t.Fatalf("expected mostly idle redis pool to be unsaturated")
	}
	if pressure.Utilization >= 0.1 {
		t.Fatalf("Utilization = %f, want active connection ratio", pressure.Utilization)
	}
}

func TestRedisPressureIgnoresHistoricalTimeoutsForSaturation(t *testing.T) {
	pressure := RedisPressure(&redis.PoolStats{
		TotalConns: 4,
		IdleConns:  4,
		Timeouts:   3,
	}, 32)

	if pressure.Saturated {
		t.Fatalf("historical redis timeouts must not keep pressure saturated forever")
	}
}
