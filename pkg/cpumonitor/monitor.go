package cpumonitor

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MonitorConfig 包含监控相关的配置参数
type MonitorConfig struct {
	SamplingInterval         time.Duration
	WindowSize               int
	HighUsageThreshold       float64
	CooldownPeriod           time.Duration
	WarmupPeriod             time.Duration
	EwmaAlpha                float64
	SustainedThresholdPeriod time.Duration
	LogFile                  string
	MaxLogSize               int64 // 最大日志文件大小，单位：字节
	MaxLogFiles              int   // 最多保留的日志文件数
	JFRDuration              time.Duration
}

// Logger 接口，包含 Printf 和 Println 方法
type Logger interface {
	Printf(format string, v ...interface{})
	Println(v ...interface{})
}

// logLogger 是标准 log 包的适配器，符合 Logger 接口
type logLogger struct{}

func (l logLogger) Printf(format string, v ...interface{}) { log.Printf(format, v...) }
func (l logLogger) Println(v ...interface{})               { log.Println(v...) }

// StartMonitoring 启动 CPU 监控和日志收集，包含日志轮转逻辑和 CPU 使用率监控
func StartMonitoring(config *MonitorConfig) {
	var (
		cpuUsageWindow  []float64
		ewma            float64
		lastTriggerTime time.Time
		startTime       = time.Now()
		sustainedTime   time.Duration
	)

	// 初始化 CPU 配置和初始使用情况
	cpuCores, previousUsage, previousSystemTime, err := initCPUConfig()
	if err != nil {
		log.Fatalf("初始化 CPU 配置失败: %v", err)
	}

	// 打开日志文件句柄
	logFileHandle, err := os.OpenFile(config.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("无法打开日志文件: %v", err)
	}
	defer func() {
		logFileHandle.Close() // 确保文件句柄在退出时被关闭
	}()
	logger := log.New(logFileHandle, "", log.LstdFlags)

	logger.Println("启动 CPU 监控...")
	loopCounter := 0

	for {
		time.Sleep(config.SamplingInterval)

		// 检查日志轮转的频率，通过计数器控制每隔 100 次检查一次
		if loopCounter%100 == 0 {
			rotated, err := rotateLogs(config.LogFile, config.MaxLogSize, config.MaxLogFiles)
			if err != nil {
				logger.Printf("日志轮转失败: %v", err)
			} else if rotated {
				logger.Println("日志轮转已执行，重新打开日志文件")
				// 关闭旧的日志文件并重新打开新文件
				logFileHandle.Close()
				logFileHandle, err = os.OpenFile(config.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				if err != nil {
					log.Fatalf("无法重新打开日志文件: %v", err)
				}
				logger = log.New(logFileHandle, "", log.LstdFlags)
			}
		}
		loopCounter++

		// 获取并计算 CPU 使用率
		cpuUsage, newUsage, newSystemTime, err := getCurrentCPUUsage(previousUsage, previousSystemTime, cpuCores)
		if err != nil {
			logger.Printf("获取 CPU 使用率失败: %v", err)
			continue
		}

		// 更新 previousUsage 和 previousSystemTime
		previousUsage = newUsage
		previousSystemTime = newSystemTime

		// 将 CPU 使用率加入滑动窗口
		cpuUsageWindow = append(cpuUsageWindow, cpuUsage)
		if len(cpuUsageWindow) > config.WindowSize {
			cpuUsageWindow = cpuUsageWindow[1:]
		}

		// 计算 EWMA（指数加权移动平均）
		ewma = config.EwmaAlpha*cpuUsage + (1-config.EwmaAlpha)*ewma

		// 格式化输出时间
		timestamp := time.Now().Format("20060102_150405")

		// 检查预热期是否结束
		if time.Since(startTime) >= config.WarmupPeriod {
			// 检查滑动窗口是否已填满
			if len(cpuUsageWindow) >= config.WindowSize {
				p95 := calculateP95(cpuUsageWindow)
				logger.Printf("%s 当前CPU使用率: %.2f%% - EWMA: %.2f%% - P95: %.2f%%", timestamp, cpuUsage, ewma, p95)

				// 检查是否达到高 CPU 使用率阈值
				if ewma > config.HighUsageThreshold && p95 > config.HighUsageThreshold {
					logger.Printf("高使用率检测：EWMA %.2f%% 和 P95 %.2f%% 超过阈值 %.2f%%", ewma, p95, config.HighUsageThreshold)

					// 增加高 CPU 使用时间
					sustainedTime += config.SamplingInterval
					logger.Printf("持续高 CPU 使用时间：%v, 持续阈值：%v", sustainedTime, config.SustainedThresholdPeriod)

					// 检查高 CPU 使用时间和冷却期
					if sustainedTime >= config.SustainedThresholdPeriod && time.Since(lastTriggerTime) >= config.CooldownPeriod {
						logger.Println("高CPU使用率持续超过阈值，开始执行日志收集...")
						if err := CollectLogs(config.JFRDuration, filepath.Dir(config.LogFile), logger); err != nil {
							logger.Printf("日志收集失败: %v", err)
						}
						lastTriggerTime = time.Now()
						sustainedTime = 0
					}
				} else {
					sustainedTime = 0
				}
			} else {
				logger.Printf("%s 当前CPU使用率: %.2f%% - EWMA: %.2f%% (数据不足，无法计算P95)", timestamp, cpuUsage, ewma)
			}
		} else {
			logger.Printf("%s 当前CPU使用率: %.2f%% - EWMA: %.2f%% (预热期，无检测)", timestamp, cpuUsage, ewma)
		}
	}
}

// rotateLogs 执行日志轮转，检查日志文件大小并创建备份
func rotateLogs(logFile string, maxSize int64, maxFiles int) (bool, error) {
	fileInfo, err := os.Stat(logFile)
	if err != nil {
		return false, fmt.Errorf("无法获取日志文件信息: %v", err)
	}

	if fileInfo.Size() >= maxSize {
		timestamp := time.Now().Format("20060102_150405")
		backupLogFile := fmt.Sprintf("%s_%s", logFile, timestamp)

		// 重命名当前日志文件为备份文件
		err = os.Rename(logFile, backupLogFile)
		if err != nil {
			return false, fmt.Errorf("无法备份日志文件: %v", err)
		}

		// 清理旧的日志备份文件
		err = cleanupOldLogs(logFile, maxFiles)
		if err != nil {
			return false, fmt.Errorf("无法清理旧日志文件: %v", err)
		}

		// 返回 true 表示日志轮转成功
		return true, nil
	}

	return false, nil
}

// cleanupOldLogs 清理超过 maxFiles 数量的旧日志备份文件
func cleanupOldLogs(logFile string, maxFiles int) error {
	// 获取所有的日志文件备份
	pattern := fmt.Sprintf("%s_*", logFile)
	files, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("无法获取日志文件列表: %v", err)
	}

	// 如果备份文件数量超过 maxFiles，删除最早的文件
	if len(files) > maxFiles {
		// 按文件名排序，保留最新的文件
		sort.Slice(files, func(i, j int) bool {
			return files[i] < files[j]
		})

		// 找出需要删除的旧文件
		oldFiles := files[:len(files)-maxFiles]
		for _, oldFile := range oldFiles {
			err := os.Remove(oldFile)
			if err != nil {
				return fmt.Errorf("无法删除旧日志文件 %s: %v", oldFile, err)
			}
		}
	}

	return nil
}

// calculateP95 计算滑动窗口内的 P95 百分位
func calculateP95(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(float64(len(sorted)) * 0.95)
	return sorted[index]
}

// CollectLogs 执行日志收集，包含 JFR 启动、GC dump 生成、日志复制及压缩
func CollectLogs(duration time.Duration, logDir string, logger Logger) error {
	// 1. 创建并清理 key_logs 目录
	keyLogsDir := filepath.Join(logDir, "key_logs")
	if err := os.MkdirAll(keyLogsDir, 0755); err != nil {
		return fmt.Errorf("无法创建目录 %s: %v", keyLogsDir, err)
	}
	if err := clearDirectory(keyLogsDir); err != nil {
		return fmt.Errorf("无法清理目录 %s: %v", keyLogsDir, err)
	}

	// 2. 获取 Java PID 并启动 JFR 采集
	pid, err := getJavaPID()
	if err != nil || pid == "" {
		return fmt.Errorf("无法获取有效的 Java PID，将跳过 JFR 和 Heap Dump 采集: %v", err)
	} else {
		jfrFile := filepath.Join(keyLogsDir, "profile.jfr")
		durationSeconds := int(duration.Seconds())
		if err := startJFR(pid, durationSeconds, jfrFile, logger); err != nil {
			logger.Printf("启动 JFR 失败: %v", err)
		} else {
			waitForFile(jfrFile, "profile.jfr", logger)
		}

		// 生成 Heap Dump
		heapDumpFile := filepath.Join(keyLogsDir, "heap.hprof")
		if err := exec.Command("jcmd", pid, "GC.heap_dump", heapDumpFile).Run(); err != nil {
			logger.Printf("GC heap dump 生成失败: %v", err)
		} else {
			logger.Println("GC heap dump 已完成，文件位置：", heapDumpFile)
		}
	}

	// 3. 搜索并复制 GC 日志文件到 key_logs 目录
	if err := copyLogs(logDir, keyLogsDir, pid, logger); err != nil {
		logger.Printf("日志文件复制失败: %v", err)
	}

	// 4. 压缩并上传日志
	return compressAndUploadLogs(logDir, keyLogsDir, pid, logger)
}

// startJFR 启动 Java 飞行记录（JFR），捕获详细的错误信息
func startJFR(pid string, duration int, filename string, logger Logger) error {
	logger.Printf("准备启动 JFR，进程 PID：%s，持续时间：%ds，文件路径：%s", pid, duration, filename)
	cmd := exec.Command("jcmd", pid, "JFR.start", fmt.Sprintf("settings=profile duration=%ds filename=%s", duration, filename))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("启动 JFR 失败: %s，错误详情: %s", err, string(output))
	}
	logger.Println("JFR 启动成功，详细输出:", string(output))
	return nil
}

// copyLogs 搜索并复制符合条件的 GC 日志文件到 key_logs 目录
func copyLogs(logDir, keyLogsDir, pid string, logger Logger) error {
	gcLogPattern := `gc.*\.log`
	if pid != "" {
		gcLogPattern = fmt.Sprintf(`gc.*\.log|%s\.log`, regexp.QuoteMeta(pid))
	}
	gcLogRegex, err := regexp.Compile(gcLogPattern)
	if err != nil {
		return fmt.Errorf("无法编译日志正则表达式: %v", err)
	}

	files, err := filepath.Glob(filepath.Join(logDir, "*"))
	if err != nil {
		return fmt.Errorf("无法列出日志目录: %v", err)
	}

	for _, file := range files {
		if gcLogRegex.MatchString(filepath.Base(file)) {
			destFile := filepath.Join(keyLogsDir, filepath.Base(file))
			if err := copyFile(file, destFile); err != nil {
				logger.Printf("无法复制文件 %s: %v", file, err)
			} else {
				logger.Printf("成功复制日志文件 %s 到 %s", file, destFile)
			}
		}
	}
	return nil
}

// waitForFile 等待文件生成并打印进度日志
func waitForFile(filePath, fileDescription string, logger Logger) {
	for {
		if info, err := os.Stat(filePath); err == nil && info.Size() > 0 {
			logger.Printf("%s 文件已生成，大小：%d 字节", fileDescription, info.Size())
			break
		}
		logger.Printf("等待 %s 文件生成中...", fileDescription)
		time.Sleep(1 * time.Second)
	}
}

// compressAndUploadLogs 压缩并上传日志文件
func compressAndUploadLogs(logDir, keyLogsDir, pid string, logger Logger) error {
	clusterName := getEnv("CLUSTER_NAME", "unknown_cluster")
	podName := getEnv("MY_POD_NAME", "unknown_pod")
	podIP := getEnv("POD_IP", "unknown_ip")
	timestamp := time.Now().Format("20060102_150405")
	tarFile := filepath.Join(logDir, fmt.Sprintf("key_logs_%s_%s_%s_%s_%s.tar.gz", clusterName, podName, podIP, pid, timestamp))

	logger.Printf("开始压缩日志文件，目标位置：%s", tarFile)
	cmd := exec.Command("tar", "-zcvf", tarFile, "-C", logDir, "key_logs")
	// 使用 CombinedOutput 执行命令并获取所有输出
	output, err := cmd.CombinedOutput()
	if err != nil {
		err := fmt.Errorf("命令执行失败: %v，输出: %s", err, string(output))
		logger.Printf("%v", err)
	}
	// 如果执行成功，可以将输出记录到日志
	logger.Printf("tar 命令执行成功，输出: %s", string(output))
	if _, err := os.Stat(tarFile); os.IsNotExist(err) {
		err := fmt.Errorf("压缩失败，未生成文件：%s", tarFile)
		logger.Printf("%v", err)
		return err
	}
	logger.Printf("日志压缩成功，文件位置：%s", tarFile)

	uploadScript := "/usr/local/bin/upload_file.sh"
	if _, err := os.Stat(uploadScript); err != nil {
		logger.Printf("上传脚本未找到，跳过上传步骤：%s", uploadScript)
		return nil
	}
	logger.Printf("开始上传文件：%s", tarFile)
	if output, err := exec.Command(uploadScript, tarFile).CombinedOutput(); err != nil {
		err := fmt.Errorf("上传失败: %v, 输出: %s", err, string(output))
		logger.Printf("%v", err)
		return err
	}
	logger.Println("文件上传成功")
	return nil
}

// logPipeOutput 实时读取并打印命令输出流
func logPipeOutput(pipe io.ReadCloser, prefix string, logger Logger) {
	defer pipe.Close() // 确保在完成读取后关闭 pipe
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		logger.Printf("%s %s", prefix, scanner.Text())
	}
}

// getEnv 获取环境变量值，若为空则返回默认值
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// initCPUConfig 初始化 CPU 配置，包括核心数和初始使用数据
func initCPUConfig() (float64, uint64, int64, error) {
	cgroupPath := "/sys/fs/cgroup/cpu,cpuacct"
	quotaFile := filepath.Join(cgroupPath, "cpu.cfs_quota_us")
	periodFile := filepath.Join(cgroupPath, "cpu.cfs_period_us")

	cpuQuota, err := readFileAsInt(quotaFile)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("无法读取 CPU 配额: %v", err)
	}

	cpuPeriod, err := readFileAsInt(periodFile)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("无法读取 CPU 周期: %v", err)
	}

	// 计算 CPU 核心数
	var cpuCores float64
	if cpuQuota > 0 && cpuPeriod > 0 {
		cpuCores = float64(cpuQuota) / float64(cpuPeriod)
	} else {
		cpuCores = float64(runtime.NumCPU()) // 使用系统核心数
	}

	// 获取初始 CPU 使用时间和系统时间
	usageFile := filepath.Join(cgroupPath, "cpuacct.usage")
	initialUsage, err := readFileAsUint64(usageFile)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("无法获取 CPU 使用时间: %v", err)
	}
	initialSystemTime := time.Now().UnixNano()

	return cpuCores, initialUsage, initialSystemTime, nil
}

// getCurrentCPUUsage 获取当前 CPU 使用率
func getCurrentCPUUsage(previousUsage uint64, previousSystemTime int64, cpuCores float64) (float64, uint64, int64, error) {
	cgroupPath := "/sys/fs/cgroup/cpu,cpuacct"
	usageFile := filepath.Join(cgroupPath, "cpuacct.usage")

	// 获取当前 CPU 使用时间
	currentUsage, err := readFileAsUint64(usageFile)
	if err != nil {
		return 0, previousUsage, previousSystemTime, fmt.Errorf("读取 CPU 使用时间失败: %v", err)
	}

	// 获取当前系统时间（纳秒）
	currentSystemTime := time.Now().UnixNano()

	// 计算时间差和 CPU 使用时间差
	deltaUsage := currentUsage - previousUsage
	deltaTime := currentSystemTime - previousSystemTime

	// 检查 deltaTime 是否为 0 以防除零
	if deltaTime == 0 || cpuCores == 0 {
		return 0, currentUsage, currentSystemTime, fmt.Errorf("无效的时间差或 CPU 核数")
	}

	// 计算 CPU 使用率（百分比）
	cpuUsagePercentage := (100 * float64(deltaUsage) / float64(deltaTime)) / cpuCores

	return cpuUsagePercentage, currentUsage, currentSystemTime, nil
}

func readFileAsUint64(filename string) (uint64, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

func readFileAsInt(filename string) (int, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// getJavaPID 获取第一个 Java 进程的 PID
const (
	maxRetries    = 5               // 最大重试次数
	retryInterval = 2 * time.Second // 每次重试之间的等待时间
)

// getJavaPID 尝试获取第一个有效 Java 进程 PID，并增加容错性
func getJavaPID() (string, error) {
	for attempt := 1; attempt <= maxRetries; attempt++ {
		cmd := exec.Command("jps", "-l") // 使用 -l 参数，确保 jps 输出完整的类名或 JAR 路径
		var out bytes.Buffer
		cmd.Stdout = &out

		// 运行 jps 命令并捕获输出
		if err := cmd.Run(); err != nil {
			log.Printf("尝试 %d/%d 获取 Java PID 失败: %v", attempt, maxRetries, err)
			time.Sleep(retryInterval)
			continue
		}

		// 分割 jps 输出，过滤掉不需要的行
		lines := strings.Split(out.String(), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			// 使用正则表达式，确保进程包含 .jar 关键字，并过滤掉包含 'jps' 或 'grep' 的行
			matched, err := regexp.MatchString(`(?i)\b(jps|grep)\b`, line)
			if err != nil {
				return "", fmt.Errorf("正则表达式匹配失败: %v", err)
			}
			// 检查包含 '.jar' 的行
			if !matched && strings.Contains(line, ".jar") {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					log.Printf("成功获取到 Java PID: %s", fields[0])
					return fields[0], nil // 返回第一个字段，即 PID
				}
			}
		}

		log.Printf("尝试 %d/%d 未找到有效的 Java 进程，等待 %v 后重试...", attempt, maxRetries, retryInterval)
		time.Sleep(retryInterval)
	}

	return "", fmt.Errorf("在 %d 次尝试后未找到有效的 Java 进程", maxRetries)
}

func clearDirectory(dir string) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := os.RemoveAll(filepath.Join(dir, file.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
