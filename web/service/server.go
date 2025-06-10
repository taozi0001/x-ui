package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/host"
	"github.com/shirou/gopsutil/load"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/net"
	"io"
	"io/fs"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
	"x-ui/logger"
	"x-ui/util/sys"
	"x-ui/xray"
)

type ProcessState string

const (
	Running ProcessState = "running"
	Stop    ProcessState = "stop"
	Error   ProcessState = "error"
)

type Status struct {
	T   time.Time `json:"-"`
	Cpu float64   `json:"cpu"`
	Mem struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"mem"`
	Swap struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"swap"`
	Disk struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"disk"`
	Xray struct {
		State    ProcessState `json:"state"`
		ErrorMsg string       `json:"errorMsg"`
		Version  string       `json:"version"`
	} `json:"xray"`
	Uptime   uint64    `json:"uptime"`
	Loads    []float64 `json:"loads"`
	TcpCount int       `json:"tcpCount"`
	UdpCount int       `json:"udpCount"`
	NetIO    struct {
		Up   uint64 `json:"up"`
		Down uint64 `json:"down"`
	} `json:"netIO"`
	NetTraffic struct {
		Sent uint64 `json:"sent"`
		Recv uint64 `json:"recv"`
	} `json:"netTraffic"`
	// 添加时间戳字段用于Firestore存储
	Timestamp time.Time `json:"timestamp"`
}

type Release struct {
	TagName string `json:"tag_name"`
}

type ServerService struct {
	xrayService      XrayService
	firestoreConfig  FirestoreConfig // 新增Firestore配置
}

// Firestore配置结构
type FirestoreConfig struct {
	ProjectID      string `json:"project_id"`
	CollectionName string `json:"collection_name"`
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"-"`
	Timeout        int    `json:"timeout"` // 超时时间(秒)
}

// 默认Firestore配置
var defaultFirestoreConfig = FirestoreConfig{
	ProjectID:      "datacollection-309fc", // 使用代码中定义的FIRESTORE_PROJECT_ID
	CollectionName: "dataCollection",       // 使用代码中定义的FIRESTORE_COLLECTION
	Enabled:        true,                   // 默认启用
	BaseURL:        "https://firestore.googleapis.com/v1/projects/datacollection-309fc/databases/(default)/documents/dataCollection",
	Timeout:        15,                     // 使用代码中最大的超时时间15秒
}

// 构造函数，可以设置Firestore配置（可选）
func NewServerService(xrayService XrayService, firestoreConfig ...FirestoreConfig) *ServerService {
	config := defaultFirestoreConfig
	if len(firestoreConfig) > 0 {
		config = firestoreConfig[0]
		// 确保BaseURL正确
		if config.BaseURL == "" {
			config.BaseURL = fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/%s",
				config.ProjectID, config.CollectionName)
		}
	}
	
	return &ServerService{
		xrayService:     xrayService,
		firestoreConfig: config,
	}
}

// 为了保持向后兼容，添加一个简单的构造函数
func NewServerServiceDefault(xrayService XrayService) *ServerService {
	return &ServerService{
		xrayService:     xrayService,
		firestoreConfig: defaultFirestoreConfig,
	}
}

// 上传数据到Firestore的方法
func (s *ServerService) uploadToFirestore(status *Status) {
	// 默认启用，如果配置为禁用才跳过
	if !s.firestoreConfig.Enabled {
		logger.Debug("Firestore upload is disabled")
		return
	}

	// 创建要上传的数据，添加时间戳
	uploadData := *status
	uploadData.Timestamp = time.Now()

	// 转换为Firestore格式
	firestoreDoc := map[string]interface{}{
		"fields": s.convertToFirestoreFields(uploadData),
	}

	firestoreJsonData, err := json.Marshal(firestoreDoc)
	if err != nil {
		logger.Warning("failed to marshal Firestore document:", err)
		return
	}

	// 异步上传，不阻塞主要逻辑
	go func() {
		// 创建HTTP请求
		req, err := http.NewRequest("POST", s.firestoreConfig.BaseURL, bytes.NewBuffer(firestoreJsonData))
		if err != nil {
			logger.Warning("failed to create Firestore request:", err)
			return
		}

		// 设置请求头
		req.Header.Set("Content-Type", "application/json")
		// 注意：在生产环境中，你需要添加适当的认证header
		// req.Header.Set("Authorization", "Bearer " + authToken)

		// 发送请求
		client := &http.Client{
			Timeout: time.Duration(s.firestoreConfig.Timeout) * time.Second,
		}
		resp, err := client.Do(req)
		if err != nil {
			logger.Warning("failed to upload to Firestore:", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			logger.Warningf("Firestore upload failed with status: %d, response: %v", resp.StatusCode, resp)
		} else {
			logger.Debug("Successfully uploaded status to Firestore")
		}
	}()
}

// 将Go结构转换为Firestore字段格式
func (s *ServerService) convertToFirestoreFields(status Status) map[string]interface{} {
	fields := make(map[string]interface{})
	
	// 系统信息
	fields["cpu"] = map[string]interface{}{"doubleValue": status.Cpu}
	fields["uptime"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.Uptime)}
	fields["tcpCount"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.TcpCount)}
	fields["udpCount"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.UdpCount)}
	fields["timestamp"] = map[string]interface{}{"timestampValue": status.Timestamp.Format(time.RFC3339)}
	
	// 内存信息
	fields["memCurrent"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.Mem.Current)}
	fields["memTotal"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.Mem.Total)}
	
	// 交换分区信息
	fields["swapCurrent"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.Swap.Current)}
	fields["swapTotal"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.Swap.Total)}
	
	// 磁盘信息
	fields["diskCurrent"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.Disk.Current)}
	fields["diskTotal"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.Disk.Total)}
	
	// Xray信息
	fields["xrayState"] = map[string]interface{}{"stringValue": string(status.Xray.State)}
	if status.Xray.ErrorMsg != "" {
		fields["xrayErrorMsg"] = map[string]interface{}{"stringValue": status.Xray.ErrorMsg}
	}
	fields["xrayVersion"] = map[string]interface{}{"stringValue": status.Xray.Version}
	
	// 网络IO
	fields["netIOUp"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.NetIO.Up)}
	fields["netIODown"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.NetIO.Down)}
	
	// 网络流量
	fields["netTrafficSent"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.NetTraffic.Sent)}
	fields["netTrafficRecv"] = map[string]interface{}{"integerValue": fmt.Sprintf("%d", status.NetTraffic.Recv)}
	
	// 负载信息
	if len(status.Loads) >= 3 {
		fields["load1"] = map[string]interface{}{"doubleValue": status.Loads[0]}
		fields["load5"] = map[string]interface{}{"doubleValue": status.Loads[1]}
		fields["load15"] = map[string]interface{}{"doubleValue": status.Loads[2]}
	}
	
	return fields
}

func (s *ServerService) GetStatus(lastStatus *Status) *Status {
	now := time.Now()
	status := &Status{
		T: now,
	}

	percents, err := cpu.Percent(0, false)
	if err != nil {
		logger.Warning("get cpu percent failed:", err)
	} else {
		status.Cpu = percents[0]
	}

	upTime, err := host.Uptime()
	if err != nil {
		logger.Warning("get uptime failed:", err)
	} else {
		status.Uptime = upTime
	}

	memInfo, err := mem.VirtualMemory()
	if err != nil {
		logger.Warning("get virtual memory failed:", err)
	} else {
		status.Mem.Current = memInfo.Used
		status.Mem.Total = memInfo.Total
	}

	swapInfo, err := mem.SwapMemory()
	if err != nil {
		logger.Warning("get swap memory failed:", err)
	} else {
		status.Swap.Current = swapInfo.Used
		status.Swap.Total = swapInfo.Total
	}

	distInfo, err := disk.Usage("/")
	if err != nil {
		logger.Warning("get dist usage failed:", err)
	} else {
		status.Disk.Current = distInfo.Used
		status.Disk.Total = distInfo.Total
	}

	avgState, err := load.Avg()
	if err != nil {
		logger.Warning("get load avg failed:", err)
	} else {
		status.Loads = []float64{avgState.Load1, avgState.Load5, avgState.Load15}
	}

	ioStats, err := net.IOCounters(false)
	if err != nil {
		logger.Warning("get io counters failed:", err)
	} else if len(ioStats) > 0 {
		ioStat := ioStats[0]
		status.NetTraffic.Sent = ioStat.BytesSent
		status.NetTraffic.Recv = ioStat.BytesRecv

		if lastStatus != nil {
			duration := now.Sub(lastStatus.T)
			seconds := float64(duration) / float64(time.Second)
			up := uint64(float64(status.NetTraffic.Sent-lastStatus.NetTraffic.Sent) / seconds)
			down := uint64(float64(status.NetTraffic.Recv-lastStatus.NetTraffic.Recv) / seconds)
			status.NetIO.Up = up
			status.NetIO.Down = down
		}
	} else {
		logger.Warning("can not find io counters")
	}

	status.TcpCount, err = sys.GetTCPCount()
	if err != nil {
		logger.Warning("get tcp connections failed:", err)
	}

	status.UdpCount, err = sys.GetUDPCount()
	if err != nil {
		logger.Warning("get udp connections failed:", err)
	}

	if s.xrayService.IsXrayRunning() {
		status.Xray.State = Running
		status.Xray.ErrorMsg = ""
	} else {
		err := s.xrayService.GetXrayErr()
		if err != nil {
			status.Xray.State = Error
		} else {
			status.Xray.State = Stop
		}
		status.Xray.ErrorMsg = s.xrayService.GetXrayResult()
	}
	status.Xray.Version = s.xrayService.GetXrayVersion()

	// 新增：上传数据到Firestore
	s.uploadToFirestore(status)

	return status
}

func (s *ServerService) GetXrayVersions() ([]string, error) {
	url := "https://api.github.com/repos/XTLS/Xray-core/releases"
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	buffer := bytes.NewBuffer(make([]byte, 8192))
	buffer.Reset()
	_, err = buffer.ReadFrom(resp.Body)
	if err != nil {
		return nil, err
	}

	releases := make([]Release, 0)
	err = json.Unmarshal(buffer.Bytes(), &releases)
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(releases))
	for _, release := range releases {
		versions = append(versions, release.TagName)
	}
	return versions, nil
}

func (s *ServerService) downloadXRay(version string) (string, error) {
	osName := runtime.GOOS
	arch := runtime.GOARCH

	switch osName {
	case "darwin":
		osName = "macos"
	}

	switch arch {
	case "amd64":
		arch = "64"
	case "arm64":
		arch = "arm64-v8a"
	}

	fileName := fmt.Sprintf("Xray-%s-%s.zip", osName, arch)
	url := fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/%s", version, fileName)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	os.Remove(fileName)
	file, err := os.Create(fileName)
	if err != nil {
		return "", err
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", err
	}

	return fileName, nil
}

func (s *ServerService) UpdateXray(version string) error {
	zipFileName, err := s.downloadXRay(version)
	if err != nil {
		return err
	}

	zipFile, err := os.Open(zipFileName)
	if err != nil {
		return err
	}
	defer func() {
		zipFile.Close()
		os.Remove(zipFileName)
	}()

	stat, err := zipFile.Stat()
	if err != nil {
		return err
	}
	reader, err := zip.NewReader(zipFile, stat.Size())
	if err != nil {
		return err
	}

	s.xrayService.StopXray()
	defer func() {
		err := s.xrayService.RestartXray(true)
		if err != nil {
			logger.Error("start xray failed:", err)
		}
	}()

	copyZipFile := func(zipName string, fileName string) error {
		zipFile, err := reader.Open(zipName)
		if err != nil {
			return err
		}
		os.Remove(fileName)
		file, err := os.OpenFile(fileName, os.O_CREATE|os.O_RDWR|os.O_TRUNC, fs.ModePerm)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(file, zipFile)
		return err
	}

	err = copyZipFile("xray", xray.GetBinaryPath())
	if err != nil {
		return err
	}
	err = copyZipFile("geosite.dat", xray.GetGeositePath())
	if err != nil {
		return err
	}
	err = copyZipFile("geoip.dat", xray.GetGeoipPath())
	if err != nil {
		return err
	}

	return nil
}