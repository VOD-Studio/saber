package batch

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
)

// Command 解析批量执行参数并调用真实宿主入口；输出同时保留部分结果与非零错误。
func Command(ctx context.Context, args []string, run func(context.Context, Options) (Summary, error), out io.Writer) error {
	if run == nil || out == nil {
		return errors.New("batch command requires runner and output")
	}
	var opts Options
	flags := flag.NewFlagSet("batch", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.StringVar(&opts.DatasetFile, "dataset-file", "", "授权工作区内的 JSONL 文件")
	flags.StringVar(&opts.RunName, "run-name", "", "私有产物目录中的运行名")
	flags.StringVar(&opts.Model, "model", "", "已配置模型名称")
	flags.StringVar(&opts.Distribution, "distribution", "default", "工具概率分布")
	flags.IntVar(&opts.BatchSize, "batch-size", 0, "每批条目数（默认配置）")
	flags.IntVar(&opts.MaxTurns, "max-turns", 0, "模型轮数（不能超过批量及运行时配置）")
	flags.IntVar(&opts.Workers, "workers", 0, "已配置上限内的任务并发")
	flags.StringVar(&opts.ReasoningEffort, "reasoning-effort", "", "已配置模型支持的思考等级")
	flags.BoolVar(&opts.ListDistributions, "list-distributions", false, "显示实际配置的工具分布")
	flags.IntVar(&opts.MaxSamples, "max-samples", 0, "只处理前 N 条")
	flags.BoolVar(&opts.Resume, "resume", false, "恢复已知持久任务，不重放未知发生")
	flags.BoolVar(&opts.RetryFailed, "retry-failed", false, "操作员明确重新执行失败/中断条目，可能重做副作用")
	flags.BoolVar(&opts.Evaluate, "evaluate", false, "按 expected 或 rubric 评估")
	flags.BoolVar(&opts.Compress, "compress", false, "额外导出 gzip 轨迹")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("batch accepts flags only")
	}
	result, err := run(ctx, opts)
	encodeErr := json.NewEncoder(out).Encode(result)
	return errors.Join(err, encodeErr)
}
