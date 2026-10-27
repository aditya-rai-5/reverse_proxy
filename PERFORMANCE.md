# Local HTTP performance baseline

This benchmark provides a repeatable development reference. It is not a production capacity estimate or a pass/fail threshold.

Run from the repository root on an otherwise idle machine:

```bash
go test ./proxy -run '^$' -bench '^BenchmarkHTTP$' -benchmem -benchtime=2s -count=3 -cpu=4
```

`BenchmarkHTTP` uses a local HTTP backend returning 1 KiB, reused connections, four parallel workers, and no retries, TLS, rate limits, or probe traffic. It compares direct backend requests, the proxy alone, and the proxy with request/attempt metrics plus JSON completion logging. Logs go to `io.Discard`, so JSON encoding is measured but stdout/disk/collector backpressure is not.

Both the benchmark client and servers run inside the same Go process. Allocation counts include all of them. The parallel `ns/op` value is elapsed wall time divided by completed requests, not individual request latency or a percentile. Its inverse gives aggregate throughput for this workload.

## Recorded run

Recorded on 2026-10-25, Linux/amd64, Intel Core i5-12450HX, Go `go1.27.2-X:nodwarf5`, `GOMAXPROCS=4`, after the image build completed. Results vary with machine load and scheduling; preserve all three samples when comparing changes.

```text
BenchmarkHTTP/direct-4       196092   11671 ns/op   87.74 MB/s    5071 B/op    62 allocs/op
BenchmarkHTTP/direct-4       183692   11691 ns/op   87.59 MB/s    5070 B/op    62 allocs/op
BenchmarkHTTP/direct-4       216226   11320 ns/op   90.46 MB/s    5071 B/op    62 allocs/op
BenchmarkHTTP/proxy-4         68763   34881 ns/op   29.36 MB/s   48060 B/op   172 allocs/op
BenchmarkHTTP/proxy-4         67245   34154 ns/op   29.98 MB/s   48037 B/op   172 allocs/op
BenchmarkHTTP/proxy-4         69475   33609 ns/op   30.47 MB/s   48063 B/op   172 allocs/op
BenchmarkHTTP/observed-4      63729   36293 ns/op   28.22 MB/s   48898 B/op   188 allocs/op
BenchmarkHTTP/observed-4      68330   38515 ns/op   26.59 MB/s   48900 B/op   188 allocs/op
BenchmarkHTTP/observed-4      51468   41676 ns/op   24.57 MB/s   48909 B/op   188 allocs/op
```

The median proxy result is approximately 29,300 requests/second; with metrics and completion logging it is approximately 26,000 requests/second. The observed variant uses about 13% more elapsed time per operation and 16 more allocations in this run. These are comparisons within the current implementation, not a before/after claim against an earlier commit.

Before setting deployment capacity, measure with the real TLS setup, payload sizes, retry/failure mix, concurrency, logging destination, and CPU/memory limits. This benchmark does not measure tail latency, long-running stability, or container resource ceilings.
