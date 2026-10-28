# Stock Bench Marking Engine

A small Monte Carlo engine in Go. It prices options and estimates portfolio loss, and it is built so you can see what concurrency costs.

Each path is one draw of a stock price under geometric Brownian motion. A European call needs one draw. An Asian or a barrier walks a short path. A portfolio draws several correlated prices. The interesting part is not the formula. It is how you run tens or hundreds of millions of those draws without the coordination costing more than the math.

The engine keeps a running mean, variance, and (for risk) a fixed histogram. It does not store the paths. A 100 million path European run keeps a few dozen bytes of totals. The payoff vector it did not allocate would be about 760 MB.

## Try it

You need Go 1.23 or newer.

```bash
go run .
```

That prices a 1-year at-the-money European call (spot 100, strike 100, rate 5%, vol 20%) with 2 million paths, one worker per CPU. The mean should sit near the Black–Scholes price, 10.450584, and the program says whether it landed inside two standard errors.

```bash
go run . -bench
```

That prints three tables: worker count on the fast scheduler, the other schedulers at the same path count, and a 100 million path run. It finishes in a few seconds on a laptop.

Other useful runs:

```bash
go run . -product asian -steps 32 -paths 200000
go run . -product barrier -barrier 80
go run . -product var -drift 0
go run . -product portfolio -paths 1000000
go run . -mode pool -batch 1 -paths 1000000
go run . -workers 8 -paths 5000000
go test -bench . -benchtime 300ms
```

`-bench` on an Asian or a barrier stays at `-paths`. Those products take many steps per path, so the automatic 100 million path row is only for one-step products (GBM, European, VaR, portfolio). You can still ask for it yourself:

```bash
go run . -product asian -mode partition -paths 100000000
```

## What the columns mean

A single run looks like this:

```text
mean         10.459444
std. error   0.020795
analytic     10.450584
throughput   99.99M paths/sec
cpu          6.97 cores
allocs       73
retained     32 B  (a payoff slice would be 3.8 MB)
```

`mean` is the discounted option price, the average terminal spot, or the average loss, depending on the product. `std. error` is the sampling error of that mean. More paths shrink it. `cpu` is process CPU time divided by wall time, so 7 cores means the run used about seven cores at once. `allocs` is how many heap allocations the run made. `retained` is the size of the merged statistics.

Numbers move with the machine. On an Apple M4, 8 workers priced 1 million European paths at about 110 million paths per second, roughly 6× the one-core rate. 32 workers did not pull further ahead: the machine has 10 CPUs, and throughput flattened.

## Schedulers

All of them compute the same payoff. They differ in how work is handed out and how results are combined.

| mode | what it does |
|---|---|
| `sequential` | One loop, one random generator. The baseline. |
| `naive` | One goroutine per path, each with its own generator. Shows the cost of too much concurrency. |
| `pool` | A fixed set of workers. Paths arrive through a channel, `batch` at a time. |
| `partition` | Paths are split into one block per worker up front. No channel. Each worker has its own generator and its own totals, merged once at the end. This is the default. |
| `shared-rng` | Same split as `partition`, but every draw locks one shared generator. |
| `locked-agg` | Same split, private generators, but every path takes a lock to update one shared total. |

`partition` with `-workers 0` uses one worker per CPU. The seed is fixed (`-seed`, default 1), and a static split is reproducible: the same flags give the same mean. A pool is not. Workers pull batches as they become free, so two pool runs can differ in the last digits. The estimate is still unbiased.

Naive mode refuses more than 100,000 paths unless you pass `-force`, and it hard-stops at 2 million. One goroutine per path allocates a generator and a stack frame every time. On 100,000 paths that was about 400,000 allocations and under 3 million paths per second, against ~100 million for the static split. That is the demonstration. Launching 100 million goroutines would measure the operating system, not the pricer.

The bench is there to answer four questions:

- **Why does 8 workers beat, or tie, 32?** Past the number of CPUs, extra goroutines wait. The table prints paths per second and cores in use at 1, 2, 4, 8, 16, and 32 workers.
- **How expensive is a channel?** `pool/1` sends one path at a time. On the M4 that was about 30× slower than `partition`. `pool/8192` sends large batches and lands next to the static split. The channel was the cost, not the workers.
- **When does synchronisation win?** `shared-rng` was about 15× slower than private generators. `locked-agg` was about 20× slower than merging local totals at the end. Both still use 8 workers. The lock is the bottleneck.
- **Can you run 100 million paths without storing 100 million results?** Yes. The 100 million row keeps 32 bytes and a few dozen allocations. A slice of payoffs would be about 763 MB.

## Products

Every product uses

```text
S_T = S0 * exp((μ - σ²/2) T + σ √T · Z),   Z ~ N(0,1)
```

If you omit `-drift`, μ is the risk-free rate, which is the risk-neutral setting used for option prices. Set `-drift 0` when you want a pure shock for a risk number.

| product | result |
|---|---|
| `gbm` | Average terminal spot. The closed form is `S0 · e^{μT}`. |
| `european` | Discounted call, `e^{-rT} max(S_T - K, 0)`. Checked against Black–Scholes when μ equals the rate. |
| `asian` | Call on the average of the fixings after time 0. `-steps` is the number of fixings (default 32). There is no closed form once volatility is on; the standard error is the check. |
| `barrier` | Down-and-out call. If any fixing is at or below `-barrier` (default 80), the payoff is 0. Monitoring is discrete, at the same steps as the Asian. No rebate. |
| `var` | Loss on one unit of the spot, `S0 - S_T`. Positive when the asset fell. Prints the mean loss plus 95% and 99% VaR and expected shortfall. |
| `portfolio` | Same loss on a book of up to 8 assets. Shocks are correlated with a Cholesky factor. |

VaR and expected shortfall come from a 512-bin histogram, not from a sorted slice of losses. Bins cover roughly 12 standard deviations of the terminal prices. Full bins use the real sum of the samples that landed in them. The bin that contains the quantile is treated as uniform. That is accurate enough to see the tail, and it stays a few kilobytes at any path count.

A negative mean loss means the book made money on average, which is normal when drift is positive. VaR is still the bad tail, and expected shortfall should sit above VaR.

The default book is three assets, quantities of 1, pairwise correlation 0.4:

| spot | vol |
|---:|---:|
| 100 | 20% |
| 80 | 25% |
| 50 | 30% |

Replace it from the command line. `-corr` is row-major and must have `n²` entries. Leave it empty to keep the 0.4 correlation.

```bash
go run . -product portfolio \
  -spots 100,100 -vols 0.2,0.3 -qty 1,1 \
  -corr 1,0.6,0.6,1
```

## Profiles and tests

```bash
go run . -profile -paths 5000000
go tool pprof -top cpu.prof
go tool pprof -top mem.prof
```

`-profile` is shorthand for `-cpuprofile cpu.prof -memprofile mem.prof`. The CPU profile covers the whole run. The heap profile is written after a garbage collection, so it shows what is still live, which for a European book is almost nothing.

```bash
go test .
go test -bench . -benchtime 300ms
go test -race .
```

`go test` checks Black–Scholes, the zero-volatility shortcuts, the barrier, the Cholesky factor, the histogram quantile, and that retained memory does not grow when the path count does. The benchmarks repeat the scheduler comparison and report `paths/s` plus allocations.

## Flags

| flag | default | |
|---|---|---|
| `-product` | `european` | `gbm`, `european`, `asian`, `barrier`, `var`, `portfolio` |
| `-mode` | `partition` | schedulers listed above |
| `-paths` | `2000000` | |
| `-workers` | `0` | `0` means one per CPU |
| `-batch` | `8192` | pool mode only |
| `-seed` | `1` | |
| `-spot` `-strike` | `100` `100` | |
| `-rate` `-vol` `-years` | `0.05` `0.2` `1` | |
| `-drift` | same as `-rate` | set it to override |
| `-barrier` | `80` | down-and-out level |
| `-steps` | `32` | Asian and barrier fixings |
| `-spots` `-vols` `-qty` `-corr` | built-in book | portfolio only, comma-separated |
| `-force` | off | allow naive mode between 100,001 and 2,000,000 paths |
| `-bench` | off | print the comparison tables |
| `-profile` | off | write `cpu.prof` and `mem.prof` |

`go run . -h` prints the same list.

## Layout

```text
main.go          command line, tables, pprof
engine.go        the six schedulers
model.go         GBM, payoffs, Cholesky, Black–Scholes
stats.go         online mean/variance and the loss histogram
engine_test.go   checks and Go benchmarks
```

Everything is one package so `go run .` stays the whole interface. The split is by job: products, running them, and reducing them.
