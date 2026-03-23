// Package benchmark 提供基准测试运行器和性能分析工具。
package benchmark

import (
	"os"
	"runtime/pprof"
	"testing"
	"time"
)

// Config 基准测试配置。
type Config struct {
	// CPUProfile CPU 分析文件路径。
	CPUProfile string
	// MemProfile 内存分析文件路径。
	MemProfile string
	// BenchTime 基准测试时长。
	BenchTime time.Duration
}

// Runner 基准测试运行器。
//
// 提供带有性能分析功能的基准测试执行能力。
type Runner struct {
	config Config
}

// NewRunner 创建新的基准测试运行器。
//
// 参数:
//   - cfg: 基准测试配置
//
// 返回值:
//   - *Runner: 配置好的运行器
func NewRunner(cfg Config) *Runner {
	return &Runner{config: cfg}
}

// Run 执行带有性能分析的基准测试。
//
// 如果配置了 CPUProfile 或 MemProfile，会自动启用相应的性能分析。
//
// 参数:
//   - b: 基准测试上下文
//   - fn: 要执行的基准测试函数
func (r *Runner) Run(b *testing.B, fn func(*testing.B)) {
	// CPU 分析
	if r.config.CPUProfile != "" {
		f, err := os.Create(r.config.CPUProfile)
		if err != nil {
			b.Fatalf("创建 CPU profile 失败: %v", err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			b.Fatalf("启动 CPU profile 失败: %v", err)
		}
		defer pprof.StopCPUProfile()
	}

	// 运行基准测试
	b.ResetTimer()
	fn(b)

	// 内存分析
	if r.config.MemProfile != "" {
		f, err := os.Create(r.config.MemProfile)
		if err != nil {
			b.Fatalf("创建内存 profile 失败: %v", err)
		}
		defer f.Close()
		if err := pprof.WriteHeapProfile(f); err != nil {
			b.Fatalf("写入内存 profile 失败: %v", err)
		}
	}
}

// Report 表示基准测试报告。
type Report struct {
	// Name 测试名称。
	Name string
	// N 执行次数。
	N int
	// NsPerOp 每次操作纳秒数。
	NsPerOp float64
	// AllocsPerOp 每次操作分配次数。
	AllocsPerOp uint64
	// BytesPerOp 每次操作分配字节数。
	BytesPerOp uint64
}

// Comparison 表示两个报告的比较结果。
type Comparison struct {
	// Name 测试名称。
	Name string
	// NsPerOpDelta 时间变化百分比。
	NsPerOpDelta float64
	// AllocsPerOpDelta 分配次数变化百分比。
	AllocsPerOpDelta float64
	// BytesPerOpDelta 分配字节变化百分比。
	BytesPerOpDelta float64
}

// Compare 比较两个报告的性能差异。
//
// 参数:
//   - other: 要比较的报告
//
// 返回值:
//   - Comparison: 比较结果
func (r *Report) Compare(other *Report) Comparison {
	return Comparison{
		Name:             r.Name,
		NsPerOpDelta:     percentChange(r.NsPerOp, other.NsPerOp),
		AllocsPerOpDelta: percentChange(float64(r.AllocsPerOp), float64(other.AllocsPerOp)),
		BytesPerOpDelta:  percentChange(float64(r.BytesPerOp), float64(other.BytesPerOp)),
	}
}

func percentChange(old, new float64) float64 {
	if old == 0 {
		return 0
	}
	return ((new - old) / old) * 100
}

// Result 表示基准测试结果。
type Result struct {
	// Name 基准测试名称。
	Name string
	// N 迭代次数。
	N int
	// Duration 总耗时。
	Duration time.Duration
	// Bytes 处理的字节数。
	Bytes int64
	// Allocs 内存分配次数。
	Allocs int64
}

// NsPerOp 返回每次操作的纳秒数。
func (r *Result) NsPerOp() float64 {
	if r.N <= 0 {
		return 0
	}
	return float64(r.Duration.Nanoseconds()) / float64(r.N)
}

// MBPerSec 返回每秒处理的 MB 数。
func (r *Result) MBPerSec() float64 {
	if r.Duration <= 0 || r.Bytes <= 0 {
		return 0
	}
	return float64(r.Bytes) / float64(r.Duration) * 1e6 / 1e6
}

// AllocsPerOp 返回每次操作的内存分配次数。
func (r *Result) AllocsPerOp() int64 {
	if r.N <= 0 {
		return 0
	}
	return r.Allocs / int64(r.N)
}

// BytesPerOp 返回每次操作分配的字节数。
func (r *Result) BytesPerOp() int64 {
	if r.N <= 0 {
		return 0
	}
	return r.Bytes / int64(r.N)
}