package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ========== 数据结构 ==========

type Config struct {
	Interface string `json:"interface"`
	LimitMbps int    `json:"limit_mbps"`
	BurstKB   int    `json:"burst_kb"`
}

type AppConfig struct {
	Devices []Config `json:"devices"`
}

type InboundRecord struct {
	Time     time.Time
	SrcIP    string
	SrcPort  string
	DstPort  string
	Protocol string
}

type IPSummary struct {
	IP       string
	Count    int
	DstPorts map[string]bool
	SrcPorts map[string]bool
	LastSeen time.Time
}

type connEntry struct {
	RemoteIP   string
	RemotePort string
	LocalPort  string
	Protocol   string
}

func (c connEntry) key() string {
	return c.RemoteIP + "|" + c.RemotePort + "|" + c.LocalPort + "|" + c.Protocol
}

var defaultConfig = AppConfig{
	Devices: []Config{
		{Interface: "eth0", LimitMbps: 50, BurstKB: 0},
	},
}

var reader = bufio.NewReader(os.Stdin)

const (
	retentionDays = 10
	logFilePath   = "/var/log/throttle-inbound.log"
	stateDir      = "/var/lib/throttle"
	stateFile     = "/var/lib/throttle/lastseen"
	markerFile    = "/var/lib/throttle/.installed"
)

// ========== 路径 ==========

func getConfigPath() string {
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "config.json")
}

func getExecPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "/opt/throttle/throttle"
	}
	return exe
}

// ========== 配置读写 ==========

func loadConfig() AppConfig {
	path := getConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			saveConfig(defaultConfig)
			return defaultConfig
		}
		fmt.Printf("读取配置失败: %v\n", err)
		os.Exit(1)
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Printf("解析配置失败: %v\n", err)
		os.Exit(1)
	}
	for i := range cfg.Devices {
		if cfg.Devices[i].Interface == "" {
			cfg.Devices[i].Interface = "eth0"
		}
		if cfg.Devices[i].LimitMbps <= 0 {
			cfg.Devices[i].LimitMbps = 50
		}
	}
	return cfg
}

func saveConfig(cfg AppConfig) {
	path := getConfigPath()
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(path, data, 0644)
}

// ========== 工具函数 ==========

func runCmd(cmd string) bool {
	fmt.Printf("  → %s\n", cmd)
	c := exec.Command("sh", "-c", cmd)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run() == nil
}

func runCmdSilent(cmd string) bool {
	c := exec.Command("sh", "-c", cmd)
	c.Stdout = nil
	c.Stderr = nil
	return c.Run() == nil
}

func runCmdOutput(cmd string) string {
	c := exec.Command("sh", "-c", cmd)
	out, _ := c.Output()
	return strings.TrimSpace(string(out))
}

func calcBurst(c Config) int {
	if c.BurstKB > 0 {
		return c.BurstKB
	}
	return c.LimitMbps * 6250 / 50
}

func readInput(prompt string, defaultVal string) string {
	fmt.Printf("%s [%s]: ", prompt, defaultVal)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		return defaultVal
	}
	return input
}

// ========== 覆盖安装检测 ==========

func promptExistingConfig() {
	cfg := loadConfig()
	fmt.Println("\n检测到已有配置:")
	for _, dev := range cfg.Devices {
		burst := calcBurst(dev)
		if dev.BurstKB > 0 {
			fmt.Printf("  网卡: %-10s 限速: %d Mbps 突发: %d KB\n", dev.Interface, dev.LimitMbps, burst)
		} else {
			fmt.Printf("  网卡: %-10s 限速: %d Mbps 突发: 自动\n", dev.Interface, dev.LimitMbps)
		}
	}
	fmt.Print("\n使用现有配置？(Y/n): ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "n" || input == "no" {
		doSetup()
	} else {
		fmt.Println("\n✓ 使用现有配置")
	}
}

func checkUpgrade() {
	configPath := getConfigPath()
	_, configErr := os.Stat(configPath)

	if os.IsNotExist(configErr) {
		// 无配置文件：首次安装
		fmt.Println("\n首次运行，请配置限速参数。")
		doSetup()
		os.MkdirAll(stateDir, 0755)
		os.WriteFile(markerFile, []byte(time.Now().Format(time.RFC3339)), 0644)
		return
	}

	// 有配置文件：脚本已写好 或 正常运行
	os.MkdirAll(stateDir, 0755)

	// 无标记文件：创建标记，直接使用现有配置
	_, markerErr := os.Stat(markerFile)
	if os.IsNotExist(markerErr) {
		os.WriteFile(markerFile, []byte(time.Now().Format(time.RFC3339)), 0644)
		return
	}

	// 有标记文件：检测是否覆盖升级（二进制比标记文件新）
	exe, exeErr := os.Executable()
	if exeErr != nil {
		return
	}
	exeInfo, err := os.Stat(exe)
	if err != nil {
		return
	}
	markerInfo, err := os.Stat(markerFile)
	if err != nil {
		return
	}
	if exeInfo.ModTime().After(markerInfo.ModTime()) {
		// 覆盖升级：二进制文件比标记新，询问是否保留配置
		promptExistingConfig()
		os.WriteFile(markerFile, []byte(time.Now().Format(time.RFC3339)), 0644)
	}
}

// ========== 功能：限速 ==========

func doApply(cfg AppConfig) {
	if len(cfg.Devices) == 0 {
		fmt.Println("\n配置中没有设备，请先执行 setup\n")
		return
	}
	fmt.Println("\n正在应用限速...\n")
	for _, dev := range cfg.Devices {
		burst := calcBurst(dev)
		iface := dev.Interface
		runCmd(fmt.Sprintf("tc qdisc del dev %s root 2>/dev/null", iface))
		if !runCmd(fmt.Sprintf("tc qdisc add dev %s root handle 1: htb default 10", iface)) {
			fmt.Printf("✗ 网卡 %s 失败，请确认网卡存在且以 root 运行\n", iface)
			continue
		}
		runCmd(fmt.Sprintf("tc class add dev %s parent 1: classid 1:10 htb rate %dmbit burst %dk", iface, dev.LimitMbps, burst))
		runCmd(fmt.Sprintf("tc qdisc add dev %s parent 1:10 handle 10: sfq perturb 10", iface))
		fmt.Printf("✓ %s → %d Mbps\n\n", iface, dev.LimitMbps)
	}
}

func doRemove(cfg AppConfig) {
	fmt.Println("\n正在移除限速...\n")
	for _, dev := range cfg.Devices {
		runCmd(fmt.Sprintf("tc qdisc del dev %s root 2>/dev/null", dev.Interface))
		fmt.Printf("✓ %s 已移除\n", dev.Interface)
	}
	fmt.Println()
}

func doStatus(cfg AppConfig) {
	fmt.Println("\n当前配置:")
	for _, dev := range cfg.Devices {
		burst := calcBurst(dev)
		fmt.Printf("  网卡: %-12s 限速: %d Mbps 突发: %d KB\n", dev.Interface, dev.LimitMbps, burst)
	}
	for _, dev := range cfg.Devices {
		fmt.Printf("\n--- %s qdisc ---\n", dev.Interface)
		runCmd(fmt.Sprintf("tc qdisc show dev %s", dev.Interface))
		fmt.Printf("--- %s class ---\n", dev.Interface)
		runCmd(fmt.Sprintf("tc class show dev %s", dev.Interface))
	}
	fmt.Println()
}

func doInterfaces() {
	fmt.Println("\n可用网卡:\n")
	dirs, err := os.ReadDir("/sys/class/net")
	if err != nil {
		fmt.Printf("读取失败: %v\n", err)
		return
	}
	for _, d := range dirs {
		name := d.Name()
		if name == "lo" {
			continue
		}
		state := "unknown"
		if data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/operstate", name)); err == nil {
			state = strings.TrimSpace(string(data))
		}
		mac := ""
		if data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/address", name)); err == nil {
			mac = strings.TrimSpace(string(data))
		}
		fmt.Printf("  %-20s 状态: %-8s MAC: %s\n", name, state, mac)
	}
	fmt.Println()
}

// ========== 功能：配置 ==========

func doSetup() {
	fmt.Println("\n========== 重新配置 ==========\n")
	defaultIface := "eth0"
	if data, err := exec.Command("sh", "-c", "ip route | awk '/^default/{print $5; exit}'").Output(); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			defaultIface = s
		}
	}
	iface := readInput("网卡名称", defaultIface)
	limit := readInput("限速 Mbps", "50")
	burst := readInput("突发大小 KB (0=自动)", "0")
	var limitInt, burstInt int
	fmt.Sscanf(limit, "%d", &limitInt)
	fmt.Sscanf(burst, "%d", &burstInt)
	cfg := AppConfig{
		Devices: []Config{
			{Interface: iface, LimitMbps: limitInt, BurstKB: burstInt},
		},
	}
	saveConfig(cfg)
	fmt.Println("\n✓ 配置已保存\n")
}

// ========== 功能：开机自启 ==========

func detectInit() string {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return "systemd"
	}
	if _, err := os.Stat("/sbin/openrc"); err == nil {
		return "openrc"
	}
	if _, err := os.Stat("/etc/init.d"); err == nil {
		return "sysv"
	}
	return "unknown"
}

func isAutostartEnabled() bool {
	init := detectInit()
	switch init {
	case "systemd":
		out := runCmdOutput("systemctl is-enabled throttle.service 2>/dev/null")
		return strings.TrimSpace(out) == "enabled"
	case "openrc":
		out := runCmdOutput("rc-update show default 2>/dev/null")
		return strings.Contains(out, "throttle")
	case "sysv":
		out := runCmdOutput("ls /etc/rc2.d/S*throttle /etc/rc3.d/S*throttle 2>/dev/null")
		return strings.TrimSpace(out) != ""
	}
	return false
}

func doEnable() {
	init := detectInit()
	switch init {
	case "systemd":
		service := `[Unit]
Description=Throttle Network Limit
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/opt/throttle/throttle apply
ExecStop=/opt/throttle/throttle remove

[Install]
WantedBy=multi-user.target`
		os.WriteFile("/etc/systemd/system/throttle.service", []byte(service), 0644)
		runCmd("systemctl daemon-reload")
		runCmd("systemctl enable throttle.service")
		fmt.Println("\n✓ 已注册 systemd 开机自启")
	case "openrc":
		script := `#!/sbin/openrc-run
description="Throttle Network Limit"
depend() { need net; }
start() { ebegin "Applying network throttle"; /opt/throttle/throttle apply; eend $?; }
stop()  { ebegin "Removing network throttle";  /opt/throttle/throttle remove; eend $?; }`
		os.WriteFile("/etc/init.d/throttle", []byte(script), 0755)
		runCmd("rc-update add throttle default")
		fmt.Println("\n✓ 已注册 OpenRC 开机自启")
	case "sysv":
		script := `#!/bin/sh
### BEGIN INIT INFO
# Provides:          throttle
# Required-Start:    $network
# Required-Stop:     $network
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Description:       Throttle Network Limit
### END INIT INFO
case "$1" in
    start)   /opt/throttle/throttle apply ;;
    stop)    /opt/throttle/throttle remove ;;
    restart) /opt/throttle/throttle remove; /opt/throttle/throttle apply ;;
esac`
		os.WriteFile("/etc/init.d/throttle", []byte(script), 0755)
		runCmd("update-rc.d throttle defaults")
		fmt.Println("\n✓ 已注册 SysV 开机自启")
	default:
		fmt.Println("\n✗ 未能识别 init 系统，请手动添加: /opt/throttle/throttle apply")
	}
}

func doDisable() {
	init := detectInit()
	switch init {
	case "systemd":
		runCmd("systemctl disable throttle.service 2>/dev/null")
		os.Remove("/etc/systemd/system/throttle.service")
		runCmd("systemctl daemon-reload")
		fmt.Println("\n✓ 已移除 systemd 开机自启")
	case "openrc":
		runCmd("rc-update del throttle default 2>/dev/null")
		os.Remove("/etc/init.d/throttle")
		fmt.Println("\n✓ 已移除 OpenRC 开机自启")
	case "sysv":
		runCmd("update-rc.d -f throttle remove 2>/dev/null")
		os.Remove("/etc/init.d/throttle")
		fmt.Println("\n✓ 已移除 SysV 开机自启")
	}
}

// ========== 入站监控 (基于 /proc，零依赖) ==========

func getLocalIPs() map[string]bool {
	ips := make(map[string]bool)
	ips["127.0.0.1"] = true
	ips["::1"] = true
	ips["0.0.0.0"] = true
	ips["::"] = true
	ips[""] = true
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			ips[ipnet.IP.String()] = true
		}
	}
	return ips
}

func parseHexIPv4(hexIP string) string {
	if len(hexIP) != 8 {
		return ""
	}
	n, _ := strconv.ParseUint(hexIP, 16, 32)
	v := uint32(n)
	return fmt.Sprintf("%d.%d.%d.%d", byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

func parseHexIPv6(hexIP string) string {
	if len(hexIP) != 32 {
		return ""
	}
	var b [16]byte
	for i := 0; i < 4; i++ {
		n, _ := strconv.ParseUint(hexIP[i*8:i*8+8], 16, 32)
		v := uint32(n)
		b[i*4] = byte(v)
		b[i*4+1] = byte(v >> 8)
		b[i*4+2] = byte(v >> 16)
		b[i*4+3] = byte(v >> 24)
	}
	if b[10] == 0xff && b[11] == 0xff {
		return fmt.Sprintf("%d.%d.%d.%d", b[12], b[13], b[14], b[15])
	}
	return net.IP(b[:]).String()
}

func parseHexPort(hexPort string) string {
	n, _ := strconv.ParseUint(hexPort, 16, 32)
	return strconv.Itoa(int(n))
}

func readProcTCP() []connEntry {
	localIPs := getLocalIPs()
	var result []connEntry
	for _, proto := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile("/proc/net/" + proto)
		if err != nil {
			continue
		}
		isIPv6 := proto == "tcp6"
		lines := strings.Split(string(data), "\n")

		listenPorts := make(map[string]bool)
		type raw struct {
			rip, rport, lport string
		}
		var conns []raw

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "sl") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			lp := strings.SplitN(fields[1], ":", 2)
			rp := strings.SplitN(fields[2], ":", 2)
			if len(lp) != 2 || len(rp) != 2 {
				continue
			}
			lport := parseHexPort(lp[1])
			rport := parseHexPort(rp[1])
			var rip string
			if isIPv6 {
				rip = parseHexIPv6(rp[0])
			} else {
				rip = parseHexIPv4(rp[0])
			}
			state := fields[3]
			if state == "0A" {
				listenPorts[lport] = true
				continue
			}
			if state == "02" {
				continue
			}
			conns = append(conns, raw{rip, rport, lport})
		}

		for _, c := range conns {
			if localIPs[c.rip] {
				continue
			}
			if !listenPorts[c.lport] {
				continue
			}
			result = append(result, connEntry{
				RemoteIP: c.rip, RemotePort: c.rport, LocalPort: c.lport, Protocol: "TCP",
			})
		}
	}
	return result
}

func readNfConntrack() []connEntry {
	data, err := os.ReadFile("/proc/net/nf_conntrack")
	if err != nil {
		return nil
	}
	localIPs := getLocalIPs()
	var result []connEntry

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Fields(line)

		proto := ""
		for _, f := range fields {
			if f == "tcp" {
				proto = "TCP"
				break
			}
			if f == "udp" {
				proto = "UDP"
				break
			}
		}
		if proto == "" {
			continue
		}

		skip := false
		for _, f := range fields {
			if f == "SYN_SENT" {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		srcSeen := 0
		var srcIP, srcPort, dstPort string
		for _, f := range fields {
			if !strings.Contains(f, "=") {
				continue
			}
			kv := strings.SplitN(f, "=", 2)
			switch kv[0] {
			case "src":
				srcSeen++
				if srcSeen == 1 {
					srcIP = kv[1]
				}
			case "sport":
				if srcSeen == 1 && srcPort == "" {
					srcPort = kv[1]
				}
			case "dport":
				if srcSeen == 1 && dstPort == "" {
					dstPort = kv[1]
				}
			}
			if srcSeen > 1 {
				break
			}
		}
		if srcIP == "" || srcPort == "" || dstPort == "" {
			continue
		}
		if localIPs[srcIP] {
			continue
		}

		result = append(result, connEntry{
			RemoteIP: srcIP, RemotePort: srcPort, LocalPort: dstPort, Protocol: proto,
		})
	}
	return result
}

func deduplicateEntries(entries []connEntry) []connEntry {
	seen := make(map[string]bool)
	var out []connEntry
	for _, e := range entries {
		if !seen[e.key()] {
			seen[e.key()] = true
			out = append(out, e)
		}
	}
	return out
}

func getCurrentInbound() []connEntry {
	entries := readNfConntrack()
	if len(entries) > 0 {
		return deduplicateEntries(entries)
	}
	return deduplicateEntries(readProcTCP())
}

func loadState() map[string]bool {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil
	}
	result := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result[line] = true
		}
	}
	return result
}

func saveState(entries []connEntry) {
	os.MkdirAll(stateDir, 0755)
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.key())
	}
	os.WriteFile(stateFile, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func appendToLog(entries []connEntry) {
	if len(entries) == 0 {
		return
	}
	os.MkdirAll(filepath.Dir(logFilePath), 0755)
	f, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02T15:04:05-0700")
	for _, e := range entries {
		fmt.Fprintf(f, "%s THROTTLE_IN SRC=%s SPT=%s DPT=%s PROTO=%s\n",
			ts, e.RemoteIP, e.RemotePort, e.LocalPort, e.Protocol)
	}
}

func doScanInbound() {
	current := getCurrentInbound()
	last := loadState()

	if last == nil {
		saveState(current)
		return
	}

	var newConns []connEntry
	added := make(map[string]bool)
	for _, e := range current {
		k := e.key()
		if !last[k] && !added[k] {
			newConns = append(newConns, e)
			added[k] = true
		}
	}

	appendToLog(newConns)
	saveState(current)
}

func isMonitorActive() bool {
	existing := runCmdOutput("crontab -l 2>/dev/null")
	return strings.Contains(existing, "throttle-scan-inbound")
}

func ensureCronDaemon() {
	init := detectInit()
	switch init {
	case "openrc":
		runCmdSilent("rc-service crond start 2>/dev/null")
		runCmdSilent("rc-update add crond default 2>/dev/null")
	case "systemd":
		runCmdSilent("sh -c 'systemctl start cron 2>/dev/null || systemctl start crond 2>/dev/null'")
		runCmdSilent("sh -c 'systemctl enable cron 2>/dev/null || systemctl enable crond 2>/dev/null'")
	}
}

func addCronJob(cronLine, marker string) {
	existing := runCmdOutput("crontab -l 2>/dev/null")
	if strings.Contains(existing, marker) {
		return
	}
	if strings.Contains(existing, "no crontab") {
		existing = ""
	}
	existing = strings.TrimSpace(existing)
	var newCrontab string
	if existing == "" {
		newCrontab = cronLine + "\n"
	} else {
		newCrontab = existing + "\n" + cronLine + "\n"
	}
	cmd := exec.Command("sh", "-c", "crontab -")
	cmd.Stdin = strings.NewReader(newCrontab)
	cmd.Run()
}

func removeCronJob(marker string) {
	existing := runCmdOutput("crontab -l 2>/dev/null")
	if !strings.Contains(existing, marker) {
		return
	}
	var kept []string
	for _, line := range strings.Split(existing, "\n") {
		if !strings.Contains(line, marker) {
			kept = append(kept, line)
		}
	}
	cmd := exec.Command("sh", "-c", "crontab -")
	cmd.Stdin = strings.NewReader(strings.Join(kept, "\n") + "\n")
	cmd.Run()
}

func doAutoCleanup() {
	data, err := os.ReadFile(logFilePath)
	if err != nil {
		return
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	lines := strings.Split(content, "\n")
	var kept []string
	removed := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		t, ok := parseLogTimestamp(line)
		if !ok {
			kept = append(kept, line)
			continue
		}
		if t.After(cutoff) {
			kept = append(kept, line)
		} else {
			removed++
		}
	}
	if removed > 0 {
		joined := strings.Join(kept, "\n")
		if strings.TrimSpace(joined) == "" {
			os.Remove(logFilePath)
		} else {
			os.WriteFile(logFilePath, []byte(joined+"\n"), 0640)
		}
	}
	// 安全阀: 超过 100MB 截断
	if info, err := os.Stat(logFilePath); err == nil && info.Size() > 100*1024*1024 {
		if len(kept) > 10000 {
			kept = kept[len(kept)-10000:]
			os.WriteFile(logFilePath, []byte(strings.Join(kept, "\n")+"\n"), 0640)
		}
	}
}

func writeLogrotateConfig() {
	cfg := fmt.Sprintf(`/var/log/throttle-inbound.log {
    rotate 3
    daily
    missingok
    notifempty
    compress
    delaycompress
    maxage %d
    copytruncate
    create 0640 root root
}
`, retentionDays)
	os.MkdirAll("/etc/logrotate.d", 0755)
	os.WriteFile("/etc/logrotate.d/throttle", []byte(cfg), 0644)
}

func doInboundSetup() {
	fmt.Println("\n========== 开启入站监控 ==========\n")
	fmt.Println("基于 /proc 采集入站 TCP 连接，无需安装额外软件包。")
	fmt.Printf("日志保留: %d 天，超期自动清理\n\n", retentionDays)

	if isMonitorActive() {
		fmt.Println("✓ 入站监控已在运行\n")
		return
	}

	os.MkdirAll(stateDir, 0755)
	ensureCronDaemon()

	exe := getExecPath()
	addCronJob(fmt.Sprintf("* * * * * %s scan-inbound # throttle-scan-inbound", exe), "throttle-scan-inbound")
	fmt.Println("  → 已安装入站扫描 (每分钟)")
	addCronJob(fmt.Sprintf("0 3 * * * %s cleanup # throttle-auto-cleanup", exe), "throttle-auto-cleanup")
	fmt.Println("  → 已安装每日清理 (03:00)")

	writeLogrotateConfig()
	if detectInit() == "openrc" {
		runCmdSilent("apk add logrotate 2>/dev/null")
	}
	fmt.Println("  → 已写入 logrotate 配置")

	doScanInbound()

	fmt.Println("\n✓ 入站监控已开启")
	fmt.Println("  查看记录: 菜单 10(24h) / 11(7天) / 12(统计)\n")
}

func doInboundTeardown() {
	fmt.Println("\n正在关闭入站监控...\n")
	removeCronJob("throttle-scan-inbound")
	removeCronJob("throttle-auto-cleanup")
	os.Remove("/etc/logrotate.d/throttle")
	os.Remove(stateFile)
	fmt.Println("✓ 入站监控已关闭\n")
}

// ========== 日志解析 ==========

func parseLogTimestamp(line string) (time.Time, bool) {
	now := time.Now()
	if len(line) >= 19 && line[4] == '-' && line[7] == '-' && line[10] == 'T' {
		end := strings.IndexAny(line[19:], " \t")
		if end == -1 {
			end = len(line) - 19
		}
		ts := line[:19+end]
		for _, layout := range []string{
			"2006-01-02T15:04:05-0700",
			"2006-01-02T15:04:05Z07:00",
			"2006-01-02T15:04:05-07:00",
			"2006-01-02T15:04:05",
		} {
			t, err := time.Parse(layout, ts)
			if err == nil {
				return t, true
			}
		}
	}
	monthMap := map[string]time.Month{
		"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6,
		"Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12,
	}
	parts := strings.Fields(line)
	if len(parts) >= 3 {
		if month, ok := monthMap[parts[0]]; ok {
			var day, hour, min, sec int
			fmt.Sscanf(parts[1], "%d", &day)
			tokens := strings.SplitN(parts[2], ":", 3)
			if len(tokens) >= 3 {
				fmt.Sscanf(tokens[0], "%d", &hour)
				fmt.Sscanf(tokens[1], "%d", &min)
				fmt.Sscanf(tokens[2], "%d", &sec)
			}
			t := time.Date(now.Year(), month, day, hour, min, sec, 0, time.Local)
			if t.After(now.Add(24 * time.Hour)) {
				t = t.AddDate(-1, 0, 0)
			}
			return t, true
		}
	}
	return time.Time{}, false
}

func parseField(line, key string) string {
	needle := " " + key + "="
	idx := strings.Index(line, needle)
	if idx == -1 {
		return ""
	}
	start := idx + len(needle)
	end := strings.IndexByte(line[start:], ' ')
	if end == -1 {
		return strings.TrimRight(line[start:], "\n\r\t")
	}
	return line[start : start+end]
}

func parseLogLine(line string) *InboundRecord {
	if !strings.Contains(line, "THROTTLE_IN") {
		return nil
	}
	t, ok := parseLogTimestamp(line)
	if !ok {
		return nil
	}
	srcIP := parseField(line, "SRC")
	if srcIP == "" {
		return nil
	}
	switch srcIP {
	case "0.0.0.0", "127.0.0.1", "255.255.255.255", "::1", "::":
		return nil
	}
	return &InboundRecord{
		Time: t, SrcIP: srcIP, SrcPort: parseField(line, "SPT"),
		DstPort: parseField(line, "DPT"), Protocol: parseField(line, "PROTO"),
	}
}

func getInboundRecords(since time.Duration) []InboundRecord {
	data, err := os.ReadFile(logFilePath)
	if err != nil {
		return nil
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return nil
	}
	cutoff := time.Now().Add(-since)
	var records []InboundRecord
	for _, line := range strings.Split(content, "\n") {
		rec := parseLogLine(line)
		if rec != nil && rec.Time.After(cutoff) {
			records = append(records, *rec)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Time.After(records[j].Time)
	})
	return records
}

func aggregateIPs(records []InboundRecord) []*IPSummary {
	ipMap := make(map[string]*IPSummary)
	for _, r := range records {
		s, exists := ipMap[r.SrcIP]
		if !exists {
			s = &IPSummary{IP: r.SrcIP, DstPorts: make(map[string]bool), SrcPorts: make(map[string]bool)}
			ipMap[r.SrcIP] = s
		}
		s.Count++
		if r.DstPort != "" {
			s.DstPorts[r.DstPort] = true
		}
		if r.SrcPort != "" {
			s.SrcPorts[r.SrcPort] = true
		}
		if r.Time.After(s.LastSeen) {
			s.LastSeen = r.Time
		}
	}
	results := make([]*IPSummary, 0, len(ipMap))
	for _, s := range ipMap {
		results = append(results, s)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Count > results[j].Count
	})
	return results
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ========== 显示功能 ==========

func doInbound24h() {
	fmt.Println("\n正在查询24小时入站记录...\n")
	if !isMonitorActive() {
		fmt.Println("✗ 入站监控未启用，请先执行菜单 8\n")
		return
	}
	records := getInboundRecords(24 * time.Hour)
	if len(records) == 0 {
		fmt.Println("暂无入站记录。可能原因:")
		fmt.Println("  - 监控刚启用，数据尚在积累中")
		fmt.Println("  - 当前无外部新连接入站\n")
		return
	}
	summaries := aggregateIPs(records)
	fmt.Printf("入站IP统计 (最近24小时)  总记录: %d 条  |  独立IP: %d 个\n", len(records), len(summaries))
	fmt.Println("──────────────────────────────────────────────────────────────────────────────")
	fmt.Printf("  %-18s %8s  %-24s %s\n", "IP地址", "次数", "目标端口", "最后出现")
	fmt.Println("  ─────────────────────────────────────────────────────────────────────────────")
	limit := 50
	if len(summaries) < limit {
		limit = len(summaries)
	}
	for i := 0; i < limit; i++ {
		s := summaries[i]
		ports := strings.Join(sortedKeys(s.DstPorts), ", ")
		fmt.Printf("  %-18s %8d  %-24s %s\n", s.IP, s.Count, ports, s.LastSeen.Format("01-02 15:04:05"))
	}
	if len(summaries) > 50 {
		fmt.Printf("\n  ... 还有 %d 个IP未显示\n", len(summaries)-50)
	}
	fmt.Printf("\n最近连接详情 (最多50条):\n")
	fmt.Println("──────────────────────────────────────────────────────────────────────────────")
	fmt.Printf("  %-14s %-18s %-8s → %-8s %s\n", "时间", "源IP", "源端口", "目标端口", "协议")
	fmt.Println("  ─────────────────────────────────────────────────────────────────────────────")
	dl := 50
	if len(records) < dl {
		dl = len(records)
	}
	for i := 0; i < dl; i++ {
		r := records[i]
		fmt.Printf("  %-14s %-18s %-8s → %-8s %s\n",
			r.Time.Format("01-02 15:04:05"), r.SrcIP, r.SrcPort, r.DstPort, r.Protocol)
	}
	fmt.Println()
}

func doInbound7d() {
	fmt.Println("\n正在查询7天入站记录...\n")
	if !isMonitorActive() {
		fmt.Println("✗ 入站监控未启用，请先执行菜单 8\n")
		return
	}
	records := getInboundRecords(7 * 24 * time.Hour)
	if len(records) == 0 {
		fmt.Println("暂无7天入站记录。\n")
		return
	}
	summaries := aggregateIPs(records)
	fmt.Printf("入站IP统计 (最近7天)  总记录: %d 条  |  独立IP: %d 个\n", len(records), len(summaries))
	fmt.Println("──────────────────────────────────────────────────────────────────────────────")
	fmt.Printf("  %-18s %8s  %-24s %s\n", "IP地址", "次数", "目标端口", "最后出现")
	fmt.Println("  ─────────────────────────────────────────────────────────────────────────────")
	limit := 100
	if len(summaries) < limit {
		limit = len(summaries)
	}
	for i := 0; i < limit; i++ {
		s := summaries[i]
		ports := strings.Join(sortedKeys(s.DstPorts), ", ")
		fmt.Printf("  %-18s %8d  %-24s %s\n", s.IP, s.Count, ports, s.LastSeen.Format("01-02 15:04:05"))
	}
	if len(summaries) > 100 {
		fmt.Printf("\n  ... 还有 %d 个IP未显示\n", len(summaries)-100)
	}
	fmt.Println()
}

func doInboundCount() {
	fmt.Println("\n正在统计7天入站数据...\n")
	if !isMonitorActive() {
		fmt.Println("✗ 入站监控未启用，请先执行菜单 8\n")
		return
	}
	records := getInboundRecords(7 * 24 * time.Hour)
	if len(records) == 0 {
		fmt.Println("暂无入站记录。\n")
		return
	}
	dayMap := make(map[string]int)
	for _, r := range records {
		dayMap[r.Time.Format("2006-01-02")]++
	}
	var days []string
	for d := range dayMap {
		days = append(days, d)
	}
	sort.Strings(days)
	fmt.Println("每日入站连接统计 (最近7天)")
	fmt.Println("══════════════════════════════════════════════")
	fmt.Printf("  %-14s %12s\n", "日期", "连接数")
	fmt.Println("  ───────────────────────────────────────────")
	total := 0
	for _, d := range days {
		fmt.Printf("  %-14s %12d\n", d, dayMap[d])
		total += dayMap[d]
	}
	fmt.Println("  ───────────────────────────────────────────")
	fmt.Printf("  %-14s %12d\n", "总计", total)

	summaries := aggregateIPs(records)
	fmt.Printf("\n独立IP总数: %d\n", len(summaries))
	if len(summaries) > 0 {
		fmt.Printf("最活跃IP:   %s (%d 次)\n", summaries[0].IP, summaries[0].Count)
		if len(summaries) > 1 {
			fmt.Printf("次活跃IP:   %s (%d 次)\n", summaries[1].IP, summaries[1].Count)
		}
	}

	portCount := make(map[string]int)
	for _, r := range records {
		if r.DstPort != "" {
			portCount[r.DstPort]++
		}
	}
	type pe struct {
		Port  string
		Count int
	}
	var pl []pe
	for p, c := range portCount {
		pl = append(pl, pe{p, c})
	}
	sort.Slice(pl, func(i, j int) bool { return pl[i].Count > pl[j].Count })
	if len(pl) > 0 {
		fmt.Println("\n热门目标端口:")
		l := 10
		if len(pl) < l {
			l = len(pl)
		}
		for i := 0; i < l; i++ {
			fmt.Printf("  端口 %-8s %8d 次\n", pl[i].Port, pl[i].Count)
		}
	}

	protoCount := make(map[string]int)
	for _, r := range records {
		if r.Protocol != "" {
			protoCount[r.Protocol]++
		}
	}
	if len(protoCount) > 0 {
		fmt.Println("\n协议分布:")
		type pr struct {
			Name  string
			Count int
		}
		var prl []pr
		for p, c := range protoCount {
			prl = append(prl, pr{p, c})
		}
		sort.Slice(prl, func(i, j int) bool { return prl[i].Count > prl[j].Count })
		for _, p := range prl {
			fmt.Printf("  %-8s %8d 次  (%.1f%%)\n", p.Name, p.Count, float64(p.Count)/float64(total)*100)
		}
	}
	fmt.Println()
}

// ========== 功能：升级 ==========

func doUpgrade() {
	fmt.Println()
	fmt.Println("即将退出程序，请再次运行安装命令完成升级")
	fmt.Println()
	time.Sleep(2 * time.Second)
	os.Exit(0)
}

// ========== 面板 ==========

func clearScreen() {
	fmt.Print("\033[2J\033[H")
}

func showMenu() {
	cfg := loadConfig()
	init := detectInit()
	monitorActive := isMonitorActive()
	autostart := isAutostartEnabled()

	fmt.Println()
	fmt.Println("  网络限速工具 v1.4")
	fmt.Println()

	// 当前状态
	for _, dev := range cfg.Devices {
		burst := calcBurst(dev)
		if dev.BurstKB > 0 {
			fmt.Printf("  限速: %-10s %3d Mbps  %d KB\n", dev.Interface, dev.LimitMbps, burst)
		} else {
			fmt.Printf("  限速: %-10s %3d Mbps  自动突发\n", dev.Interface, dev.LimitMbps)
		}
	}

	if monitorActive {
		fmt.Printf("  监控: 已启用 (保留%d天)\n", retentionDays)
	} else {
		fmt.Println("  监控: 未启用")
	}

	if autostart {
		fmt.Printf("  自启: 已启用 (%s)\n", init)
	} else {
		fmt.Printf("  自启: 未启用\n")
	}

	// 功能菜单
	fmt.Println()
	fmt.Println("──────────────────────────────────────────────")
	fmt.Println("  1.  apply          应用限速")
	fmt.Println("  2.  remove         移除限速")
	fmt.Println("  3.  status         查看当前规则")
	fmt.Println("  4.  interfaces     查看网卡列表")
	fmt.Println("  5.  setup          重新配置")
	fmt.Println("  6.  enable         开机自启")
	fmt.Println("  7.  disable        取消自启")
	fmt.Println("──────────────────────────────────────────────")
	fmt.Println("  8.  inbound setup  开启入站监控")
	fmt.Println("  9.  inbound stop   关闭入站监控")
	fmt.Println("  10. inbound 24h    24小时入站IP详情")
	fmt.Println("  11. inbound 7d     7天入站IP列表")
	fmt.Println("  12. inbound count  7天入站数量统计")
	fmt.Println("──────────────────────────────────────────────")
	fmt.Println("  13. upgrade        升级程序")
	fmt.Println("  0.  exit           退出")
	fmt.Println("──────────────────────────────────────────────")
}

func main() {
	// CLI 模式
	if len(os.Args) > 1 {
		cfg := loadConfig()
		switch os.Args[1] {
		case "apply":
			doApply(cfg)
		case "remove":
			doRemove(cfg)
		case "scan-inbound":
			doScanInbound()
		case "cleanup":
			doAutoCleanup()
		case "upgrade":
			doUpgrade()
		default:
			fmt.Printf("未知命令: %s\n", os.Args[1])
			fmt.Println("可用命令: apply, remove, scan-inbound, cleanup, upgrade")
		}
		return
	}

	// 交互模式: 升级检测 + 配置确认
	checkUpgrade()

	// 启动时自动清理过期日志
	doAutoCleanup()

	for {
		clearScreen()
		showMenu()

		fmt.Print("\n请输入命令编号: ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		cfg := loadConfig()

		switch input {
		case "1", "apply":
			doApply(cfg)
		case "2", "remove":
			doRemove(cfg)
		case "3", "status":
			doStatus(cfg)
		case "4", "interfaces":
			doInterfaces()
		case "5", "setup":
			doSetup()
		case "6", "enable":
			doEnable()
		case "7", "disable":
			doDisable()
		case "8", "inbound setup", "inbound-setup":
			doInboundSetup()
		case "9", "inbound stop", "inbound-stop":
			doInboundTeardown()
		case "10", "inbound 24h", "inbound-24h":
			doInbound24h()
		case "11", "inbound 7d", "inbound-7d":
			doInbound7d()
		case "12", "inbound count", "inbound-count":
			doInboundCount()
		case "13", "upgrade":
			doUpgrade()
		case "0", "exit", "q", "quit":
			fmt.Println("Bye!")
			return
		default:
			fmt.Println("无效输入，请重新选择")
		}

		fmt.Print("\n按回车返回菜单...")
		reader.ReadString('\n')
	}
}
