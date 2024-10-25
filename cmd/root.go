package cmd

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/go-zookeeper/zk"
	"github.com/spf13/cobra"
	"poizon.com/cpu-monitor/pkg/cpumonitor"
)

var (
	monitorConfig            *cpumonitor.MonitorConfig // MonitorConfig的实例
	samplingInterval         time.Duration
	windowSize               int
	highUsageThreshold       float64
	cooldownPeriod           time.Duration
	warmupPeriod             time.Duration
	ewmaAlpha                float64
	sustainedThresholdPeriod time.Duration
	logFile                  string
	maxLogSize               int64
	maxLogFiles              int
	testCollectLogs          bool          // 用于直接测试 collectLogs 的开关
	jfrDuration              time.Duration // 新增 JFR 采集时间，默认 300 秒
)

func traverseZnode(conn *zk.Conn, path string) {
	// 获取当前节点的数据
	data, stat, err := conn.Get(path)
	if err != nil {
		log.Printf("无法读取 %s 节点数据: %v\n", path, err)
		return
	}

	// 打印当前节点的路径和数据
	fmt.Printf("znode 路径: %s\n", path)
	fmt.Printf("znode 数据: %s\n", string(data))
	fmt.Printf("znode 状态: %+v\n\n", stat)

	// 获取当前节点的子节点
	children, _, err := conn.Children(path)
	if err != nil {
		log.Printf("无法获取 %s 的子节点: %v\n", path, err)
		return
	}

	// 递归遍历子节点
	for _, child := range children {
		// 构造子节点的路径
		childPath := path + "/" + child
		if path == "/" { // 处理根节点的情况，避免路径变成 "//"
			childPath = "/" + child
		}
		traverseZnode(conn, childPath)
	}
}

func testZK() {
	// 定义 Zookeeper 服务器地址
	servers := []string{"mse-a4a5e4d0-zk.mse.aliyuncs.com:2181"} // 替换为你的 Zookeeper 地址

	// 连接 Zookeeper
	conn, _, err := zk.Connect(servers, time.Second*5)
	if err != nil {
		log.Fatalf("连接 Zookeeper 失败: %v", err)
	}
	defer conn.Close()

	// 从给定的根节点开始遍历
	rootPath := "/pandora-box" // 替换为你的根节点路径
	traverseZnode(conn, rootPath)
}

// rootCmd 表示基础命令
var rootCmd = &cobra.Command{
	Use:   "cpu-monitor",
	Short: "一个用于监控CPU使用率并在高使用率时收集日志的工具",
	Long:  `该工具用于监控CPU使用率，并在检测到高使用率时触发日志收集。可以通过多种参数自定义监控行为。`,
	Run: func(cmd *cobra.Command, args []string) {
		logger := log.New(os.Stdout, "", log.LstdFlags)
		if testCollectLogs {
			// 启用测试模式：直接测试 collectLogs
			fmt.Println("启动 collectLogs 测试模式...")
			logDir := filepath.Dir(logFile)
			if err := cpumonitor.CollectLogs(10*time.Second, logDir, logger); err != nil {
				logger.Fatalf("日志收集失败: %v", err)
			}
			fmt.Println("collectLogs 测试完成")
			return
		}
		fmt.Println("未启用测试模式，请使用 monitor 子命令运行监控或使用 --test-collect-logs 进行 collectLogs 测试")
	},
}

// monitorCmd 是一个子命令，用于启动 CPU 使用率监控
var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "监控CPU使用率并在高使用率时收集日志",
	Long: `启动CPU使用率监控。当CPU使用率超过指定阈值并且持续超出一段时间时，会自动收集日志用于分析。

参数说明：
- --interval: 采样间隔时间
- --window-size: P95 计算的滑动窗口大小
- --threshold: 高CPU使用率的阈值（百分比）
- --cooldown: 高使用率操作之间的冷却时间
- --warmup: 预热期，在该期内不进行高使用率检测
- --alpha: EWMA 平滑因子，用于计算 CPU 使用率的指数加权移动平均
- --sustain: 高CPU使用率必须持续超出的最短时间
- --log-file: 日志文件路径
- --max-log-size: 日志文件的最大大小，超过此大小将进行日志轮转
- --max-log-files: 保留的最大日志文件数，超过此数将删除旧日志
- --jfr-duration: JFR 采集的时间长度，超出此时间将自动停止采集 (默认: 300s)


示例:
1. 以默认参数运行：
   ./cpu-monitor monitor

2. 指定采样间隔为5秒，CPU高使用率阈值为90%：
   ./cpu-monitor monitor --interval=5s --threshold=90.0

3. 使用更长的冷却期（4小时），并设置日志文件路径：
   ./cpu-monitor monitor --cooldown=4h --log-file=./cpu_monitor.log`,
	Run: func(cmd *cobra.Command, args []string) {
		// 初始化监控配置
		monitorConfig = &cpumonitor.MonitorConfig{
			SamplingInterval:         samplingInterval,
			WindowSize:               windowSize,
			HighUsageThreshold:       highUsageThreshold,
			CooldownPeriod:           cooldownPeriod,
			WarmupPeriod:             warmupPeriod,
			EwmaAlpha:                ewmaAlpha,
			SustainedThresholdPeriod: sustainedThresholdPeriod,
			LogFile:                  logFile,
			MaxLogSize:               maxLogSize,
			MaxLogFiles:              maxLogFiles,
			JFRDuration:              jfrDuration, // 使用用户指定的 JFR 采集时间
		}

		// 输出配置参数，便于确认
		fmt.Println("启动 CPU 监控，实际采用的配置参数如下：")
		fmt.Printf("SamplingInterval:         %v\n", monitorConfig.SamplingInterval)
		fmt.Printf("WindowSize:               %d\n", monitorConfig.WindowSize)
		fmt.Printf("HighUsageThreshold:       %.2f\n", monitorConfig.HighUsageThreshold)
		fmt.Printf("CooldownPeriod:           %v\n", monitorConfig.CooldownPeriod)
		fmt.Printf("WarmupPeriod:             %v\n", monitorConfig.WarmupPeriod)
		fmt.Printf("EwmaAlpha:                %.2f\n", monitorConfig.EwmaAlpha)
		fmt.Printf("SustainedThresholdPeriod: %v\n", monitorConfig.SustainedThresholdPeriod)
		fmt.Printf("LogFile:                  %s\n", monitorConfig.LogFile)
		fmt.Printf("MaxLogSize:               %d bytes\n", monitorConfig.MaxLogSize)
		fmt.Printf("MaxLogFiles:              %d\n", monitorConfig.MaxLogFiles)
		fmt.Printf("JFRDuration:              %d\n", monitorConfig.JFRDuration)

		// 启动监控
		cpumonitor.StartMonitoring(monitorConfig)
	},
}

// Execute 添加所有子命令到 rootCmd 并设置 flags。
func Execute() {
	cobra.CheckErr(rootCmd.Execute())
}

// init 初始化所有命令和 flags
func init() {
	// 添加 monitorCmd 作为 rootCmd 的子命令
	rootCmd.AddCommand(monitorCmd)

	// monitorCmd flags，允许用户自定义监控配置
	monitorCmd.Flags().DurationVar(&samplingInterval, "interval", 3*time.Second, "CPU 使用率采样间隔时间 (默认: 3s)")
	monitorCmd.Flags().IntVar(&windowSize, "window-size", 60, "P95 计算的滑动窗口大小 (默认: 60)")
	monitorCmd.Flags().Float64Var(&highUsageThreshold, "threshold", 95.0, "高CPU使用率阈值 (百分比) (默认: 95.0)")
	monitorCmd.Flags().DurationVar(&cooldownPeriod, "cooldown", 3*time.Hour, "高使用率操作之间的冷却时间 (默认: 3h)")
	monitorCmd.Flags().DurationVar(&warmupPeriod, "warmup", 5*time.Minute, "预热期，在此期间不进行高使用率检测 (默认: 5m)")
	monitorCmd.Flags().Float64Var(&ewmaAlpha, "alpha", 0.2, "EWMA 平滑因子 (范围: 0 到 1) (默认: 0.2)")
	monitorCmd.Flags().DurationVar(&sustainedThresholdPeriod, "sustain", 3*time.Minute, "高CPU使用率必须持续的最短时间 (默认: 3m)")
	monitorCmd.Flags().StringVar(&logFile, "log-file", "/logs/monitor_cpu_usage.log", "日志文件路径 (默认: /logs/monitor_cpu_usage.log)")
	monitorCmd.Flags().Int64Var(&maxLogSize, "max-log-size", 100*1024*1024, "日志文件的最大大小 (字节) (默认: 100MB)")
	monitorCmd.Flags().IntVar(&maxLogFiles, "max-log-files", 5, "保留的最大日志文件数 (默认: 5)")
	monitorCmd.Flags().DurationVar(&jfrDuration, "jfr-duration", 300*time.Second, "JFR 采集时间，单位为时间 (默认: 300s)")

	// 为 rootCmd 添加用于直接测试 collectLogs 的开关
	rootCmd.PersistentFlags().BoolVar(&testCollectLogs, "test-collect-logs", false, "直接测试 collectLogs 函数")
}
