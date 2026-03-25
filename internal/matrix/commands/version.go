// Package commands 提供 Matrix 机器人的命令处理实现。
package commands

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"maunium.net/go/mautrix/id"
)

// BuildInfo 包含构建时的版本信息。
type BuildInfo struct {
	Version       string
	GitCommit     string
	GitBranch     string
	BuildTime     string
	GoVersion     string
	BuildPlatform string
}

// RuntimePlatform 返回运行时平台信息 (GOOS/GOARCH)。
func (b BuildInfo) RuntimePlatform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// BuildInfoProvider 定义构建信息提供者接口。
type BuildInfoProvider interface {
	GetBuildInfo() *BuildInfo
}

// VersionCommand 显示构建版本信息。
type VersionCommand struct {
	sender Sender
	info   BuildInfoProvider
}

// NewVersionCommand 创建一个新的版本命令处理器。
func NewVersionCommand(sender Sender, info BuildInfoProvider) *VersionCommand {
	return &VersionCommand{
		sender: sender,
		info:   info,
	}
}

// Handle 实现 CommandHandler，生成 HTML 表格格式的版本信息。
func (c *VersionCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	info := c.info.GetBuildInfo()
	if info == nil {
		plain := "版本信息不可用"
		return c.sender.SendFormattedText(ctx, roomID, plain, plain)
	}

	var html strings.Builder
	html.WriteString("<h3>📦 Saber 版本信息</h3>")
	html.WriteString("<table><thead><tr><th>项目</th><th>值</th></tr></thead><tbody>")
	fmt.Fprintf(&html, "<tr><td><strong>版本</strong></td><td><code>%s</code></td></tr>", info.Version)
	fmt.Fprintf(&html, "<tr><td><strong>Git 提交</strong></td><td><code>%s</code></td></tr>", info.GitCommit)
	fmt.Fprintf(&html, "<tr><td><strong>Git 分支</strong></td><td><code>%s</code></td></tr>", info.GitBranch)
	fmt.Fprintf(&html, "<tr><td><strong>构建时间</strong></td><td><code>%s</code></td></tr>", info.BuildTime)
	fmt.Fprintf(&html, "<tr><td><strong>Go 版本</strong></td><td><code>%s</code></td></tr>", info.GoVersion)
	fmt.Fprintf(&html, "<tr><td><strong>构建平台</strong></td><td><code>%s</code></td></tr>", info.BuildPlatform)
	fmt.Fprintf(&html, "<tr><td><strong>运行平台</strong></td><td><code>%s</code></td></tr>", info.RuntimePlatform())
	html.WriteString("</tbody></table>")

	var plain strings.Builder
	plain.WriteString("📦 Saber 版本信息\n\n")
	fmt.Fprintf(&plain, "版本: %s\n", info.Version)
	fmt.Fprintf(&plain, "Git 提交: %s\n", info.GitCommit)
	fmt.Fprintf(&plain, "Git 分支: %s\n", info.GitBranch)
	fmt.Fprintf(&plain, "构建时间: %s\n", info.BuildTime)
	fmt.Fprintf(&plain, "Go 版本: %s\n", info.GoVersion)
	fmt.Fprintf(&plain, "构建平台: %s\n", info.BuildPlatform)
	fmt.Fprintf(&plain, "运行平台: %s\n", info.RuntimePlatform())

	return c.sender.SendFormattedText(ctx, roomID, html.String(), plain.String())
}
