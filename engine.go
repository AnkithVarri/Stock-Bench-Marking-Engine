import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"syscall"
	"time"
)

const (
	naiveSoftCap = 100_000
	naiveHardCap = 2_000_000
	histBins     = 512
)

type Mode int

const (
	ModeSequential Mode = iota
	ModeNaive
	ModePool
	ModePartition
	ModeSharedRNG
	ModeLockedAgg
)

func (m Mode) String() string {
	switch m {
	case ModeSequential:
		return "sequential"
	case ModeNaive:
		return "naive"
	case ModePool:
		return "pool"
	case ModePartition:
		return "partition"
	case ModeSharedRNG:
		return "shared-rng"
	case ModeLockedAgg:
		return "locked-agg"
	default:
		return "?"
	}
}

func ParseMode(s string) (Mode, error) {
	switch s {
	case "sequential":
		return ModeSequential, nil
	case "naive":
		return ModeNaive, nil
	case "pool":
		return ModePool, nil
	case "partition":
		return ModePartition, nil
	case "shared", "shared-rng":
		return ModeSharedRNG, nil
	case "locked", "locked-agg":
		return ModeLockedAgg, nil
	default:
		return 0, fmt.Errorf("mode must be sequential, naive, pool, partition, shared-rng, or locked-agg")
	}
}

type Result struct {
	Mode        Mode
	Product     Product
	Workers     int
	Batch       int
	Paths       int
	Elapsed     time.Duration
	PathsPerSec float64
	Cores       float64
	Mallocs     uint64
	AllocBytes  uint64
	Mean        float64
	StdErr      float64
	VaR95       float64
	ES95        float64
	VaR99       float64
	ES99        float64
	Retained    int
	Closed      bool
}