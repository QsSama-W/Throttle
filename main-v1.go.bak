package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

var defaultConfig = AppConfig{
	Devices: []Config{
		{Interface: "eth0", LimitMbps: 50, BurstKB: 0},
	},
}

var reader = bufio.NewReader(os.Stdin)

// ========== 路径 ==========

func getConfigPath() string {
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "config.json")
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
		runCmd(fmt.Sprintf(
			"tc class add dev %s parent 1: classid 1:10 htb rate %dmbit burst %dk",
			iface, dev.LimitMbps, burst,
		))
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
	fmt.Println("\n========== 重新配置 ==========")

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

depend() {
    need net
}

start() {
    ebegin "Applying network throttle"
    /opt/throttle/throttle apply
    eend $?
}

stop() {
    ebegin "Removing network throttle"
    /opt/throttle/throttle remove
    eend $?
}`
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
    start)
        /opt/throttle/throttle apply
        ;;
    stop)
        /opt/throttle/throttle remove
        ;;
    restart)
        /opt/throttle/throttle remove
        /opt/throttle/throttle apply
        ;;
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

// ========== 面板 ==========

func clearScreen() {
	fmt.Print("\033[2J\033[H")
}

func showMenu() {
	cfg := loadConfig()
	init := detectInit()

	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║          网络限速工具 v1.0           ║")
	fmt.Println("╠══════════════════════════════════════╣")
	for _, dev := range cfg.Devices {
		burst := calcBurst(dev)
		auto := burst == dev.LimitMbps*6250/50
		if auto {
			fmt.Printf("║  当前: %-8s  %3d Mbps  自动突发  ║\n", dev.Interface, dev.LimitMbps)
		} else {
			fmt.Printf("║  当前: %-8s  %3d Mbps  %4d KB    ║\n", dev.Interface, dev.LimitMbps, burst)
		}
	}
	fmt.Println("╠══════════════════════════════════════╣")
	fmt.Println("║  1. apply      应用限速              ║")
	fmt.Println("║  2. remove     移除限速              ║")
	fmt.Println("║  3. status     查看当前规则          ║")
	fmt.Println("║  4. interfaces 查看网卡列表          ║")
	fmt.Println("║  5. setup      重新配置              ║")
	fmt.Println("║  6. enable     开机自启              ║")
	fmt.Println("║  7. disable    取消自启              ║")
	fmt.Println("║  0. exit       退出                  ║")
	fmt.Printf("╚══════════════════════════════════════╝\n")
	fmt.Printf("  init: %s\n", init)
}

func main() {
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
