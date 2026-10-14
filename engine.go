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
