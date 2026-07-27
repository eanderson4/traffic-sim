nit: engine/natsio/bus.go:76 - The comment "Pre-TSIB engines omit the field entirely" could be read as a deliberate action. Wording like "The field is absent on replies from pre-TSIB engines" might be slightly more precise, as they are unaware of the field. This is a minor stylistic point.

question: engine/natsio/bench_intent_test.go:452 - The `encodeSink` global variable is used to prevent compiler optimization in `BenchmarkIntentEncode`. If these benchmarks were ever run in parallel, this could introduce a race condition. Is the benchmark suite configured to always run sequentially, or would a function-scoped variable passed to a `b.ReportMetric` call (like `b.ReportMetric(float64(len(encodeSink)), "sink_len")`) be a safer pattern if parallelism is a future concern?

REVIEW-COMPLETE
